// Package feed 播音组 AI 讲稿的两个外部数据源：新闻检索（Tavily）与天气（uapis.cn）。
//
// 刻意把主机名写成包内常量，调用方只能传"城市名"和"检索词"这类文本，传不进一个 URL ——
// 否则这个功能就成了任何登录用户都能拿来探测内网的开放代理（SSRF）。
// 与素材工坊同一口径：出网由后端发起，浏览器不直连第三方。
package feed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultSearchEndpoint  = "https://api.tavily.com/search"
	defaultWeatherEndpoint = "https://uapis.cn/api/v1/misc/weather"
	searchBodyLimit        = 1 << 20 // 1MB：检索响应再大就不是几条摘要了
	weatherBodyLimit       = 1 << 16
	maxUpstreamErrSnippet  = 160
)

// 端点默认写死，只能由部署方的环境变量整体替换（测试用它指向 httptest 假上游）。
// 调用方永远传不进 URL —— 否则这个功能就成了探测内网的开放代理。
func searchEndpoint() string {
	return endpointOrEnv("FEED_SEARCH_ENDPOINT", defaultSearchEndpoint)
}

func weatherEndpoint() string {
	return endpointOrEnv("FEED_WEATHER_ENDPOINT", defaultWeatherEndpoint)
}

func endpointOrEnv(key, fallback string) string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/"); v != "" {
		return v
	}
	return fallback
}

// ensureBearerSafe 检索请求带 Authorization: Bearer <密钥>，
// 因此除了本机回环（测试）之外一律要求 https：
// 明文 HTTP 下把密钥发给中间人，等于密钥已经泄露。
func ensureBearerSafe(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("检索端点地址不合法")
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return nil
	}
	return fmt.Errorf("检索端点不是 https，拒绝携带密钥请求: %s", u.Host)
}

// NewsItem 一条检索结果。Published 上游可能不给，留空即可。
type NewsItem struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Content   string `json:"content"`
	Published string `json:"published_date"`
}

// Weather 实况天气里播报用得到的几项。字段一律收成文本：上游同一字段
// 时而给数字时而给字符串（实测 "temperature":22），统一按文本收，
// 省得一次上游改类型就把整个功能打挂。
type Weather struct {
	Province      string `json:"province"`
	City          string `json:"city"`
	Weather       string `json:"weather"`
	Temperature   string `json:"temperature"`
	WindDirection string `json:"wind_direction"`
	WindPower     string `json:"wind_power"`
	Humidity      string `json:"humidity"`
}

func (w *Weather) UnmarshalJSON(b []byte) error {
	var raw struct {
		Province      json.RawMessage `json:"province"`
		City          json.RawMessage `json:"city"`
		Weather       json.RawMessage `json:"weather"`
		Temperature   json.RawMessage `json:"temperature"`
		WindDirection json.RawMessage `json:"wind_direction"`
		WindPower     json.RawMessage `json:"wind_power"`
		Humidity      json.RawMessage `json:"humidity"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	w.Province = rawToText(raw.Province)
	w.City = rawToText(raw.City)
	w.Weather = rawToText(raw.Weather)
	w.Temperature = rawToText(raw.Temperature)
	w.WindDirection = rawToText(raw.WindDirection)
	w.WindPower = rawToText(raw.WindPower)
	w.Humidity = rawToText(raw.Humidity)
	return nil
}

func rawToText(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return ""
		}
		return v
	}
	return s // 数字、布尔等按字面量收
}

// Summary 拼成一句可直接进稿子的话；字段缺失时只说已知的部分，不编造。
func (w *Weather) Summary() string {
	if w == nil {
		return ""
	}
	parts := []string{}
	if city := strings.TrimSpace(w.City); city != "" {
		parts = append(parts, city)
	}
	if sky := strings.TrimSpace(w.Weather); sky != "" {
		parts = append(parts, sky)
	}
	if t := strings.TrimSpace(w.Temperature); t != "" {
		parts = append(parts, "气温"+t+"摄氏度")
	}
	if wd := strings.TrimSpace(w.WindDirection); wd != "" {
		wp := strings.TrimSpace(w.WindPower)
		if wp != "" {
			wd += wp
		}
		parts = append(parts, wd)
	}
	if h := strings.TrimSpace(w.Humidity); h != "" {
		parts = append(parts, "相对湿度"+h+"%")
	}
	return strings.Join(parts, "，")
}

// SearchNews 按检索词取一批新闻摘要。topic 固定 news、time_range 固定 day：
// 讲稿要的就是"今天发生的事"，把这两个参数开给调用方只会让每次生成结果不可控。
func SearchNews(apiKey, query string, maxResults int) ([]NewsItem, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("未配置新闻检索密钥")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("检索词为空")
	}
	if maxResults < 1 {
		maxResults = 5
	}
	if maxResults > 10 {
		maxResults = 10
	}

	endpoint := searchEndpoint()
	if err := ensureBearerSafe(endpoint); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"query":          q,
		"topic":          "news",
		"time_range":     "day",
		"search_depth":   "basic",
		"max_results":    maxResults,
		"include_answer": false,
	})
	if err != nil {
		return nil, fmt.Errorf("构造检索请求失败: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("检索请求不合法: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 12 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		// err 里只有主机名，不含请求头；密钥因此不会进日志
		return nil, fmt.Errorf("新闻检索上游不可达: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, searchBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("读取检索响应失败: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("新闻检索上游返回 %d：%s", res.StatusCode, snippet(body))
	}

	var out struct {
		Results []NewsItem `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("检索响应不是合法 JSON: %w", err)
	}

	items := make([]NewsItem, 0, len(out.Results))
	for _, it := range out.Results {
		it.Title = strings.TrimSpace(it.Title)
		it.Content = strings.TrimSpace(it.Content)
		if it.Title == "" && it.Content == "" {
			continue
		}
		if isBannedNewsSource(it.URL) {
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

// bannedNewsHosts 讲稿新闻检索的来源黑名单（主域，含其子域）。
// 上游检索偶尔会把这类站点混进结果里；它们不适合作为校园播报素材，
// 与其让部长们在出处列表里手动识别，不如在客户端这一层统一拦掉。
var bannedNewsHosts = []string{"bannedbook.org"}

func isBannedNewsSource(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, bad := range bannedNewsHosts {
		if host == bad || strings.HasSuffix(host, "."+bad) {
			return true
		}
	}
	return false
}

// FetchWeather 取一个城市的实况天气。调用频率由上层的按日缓存控制，这里不做缓存。
func FetchWeather(city string) (*Weather, error) {
	c := strings.TrimSpace(city)
	if c == "" {
		return nil, fmt.Errorf("未配置天气城市")
	}
	if len([]rune(c)) > 32 {
		return nil, fmt.Errorf("城市名过长")
	}

	u := weatherEndpoint() + "?city=" + url.QueryEscape(c)
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("天气上游不可达: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, weatherBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("读取天气响应失败: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("天气上游返回 %d：%s", res.StatusCode, snippet(body))
	}

	var w Weather
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("天气响应不是合法 JSON: %w", err)
	}
	if strings.TrimSpace(string(w.Temperature)) == "" && strings.TrimSpace(w.Weather) == "" {
		// 上游 200 但内容空：宁缺毋滥，不能拿空字段拼出一句假天气
		return nil, fmt.Errorf("天气上游没有返回可用数据")
	}
	return &w, nil
}

// snippet 只取响应体开头一小段用于排错；上游错误体里不该有敏感数据，
// 但仍要限长，免得整页 HTML 灌进日志与接口响应。
func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > maxUpstreamErrSnippet {
		return s[:maxUpstreamErrSnippet] + "…"
	}
	if s == "" {
		return "空响应"
	}
	return s
}
