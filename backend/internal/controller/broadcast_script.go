package controller

// 播音组「AI 自动编写今日讲稿」。
//
// 数据流：三路新闻检索（Tavily）+ 一条按日缓存的实况天气（uapis.cn）
// → 只把新闻素材交给已配置的文本引擎写三段话 → 固定开场白与天气句由 Go 拼，
// 不让模型碰。这样"今日天气"四个字永远来自真实接口，模型写不出假天气。
//
// 三条硬口径：
//  1. 天气一天只打一次上游（BroadcastWeatherCache 的 date 唯一索引），取不到就如实说取不到；
//  2. 稿子不落库（2026-10-01 使用方定：只显示 + 复制），本文件里没有任何新闻写表动作；
//  3. 检索密钥只在服务端解密后使用，任何响应与审计明细都不出现它，界面只拿 has_key/key_mask。

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/ai"
	"xgh-system/pkg/feed"
	"xgh-system/pkg/secretbox"
)

const (
	broadcastNewsPerTopic  = 5   // 每个主题取几条摘要喂给模型
	broadcastNewsHardMin   = 60  // 低于这个字数说明模型根本没写内容
	broadcastNewsHardMax   = 260 // 高于这个字数说明它开始自由发挥
	broadcastNewsTargetMin = 120 // 使用方要求的正文区间
	broadcastNewsTargetMax = 150
)

// broadcastNewsBuckets 讲稿固定覆盖的三类内容。检索词写死在这里而不是开给调用方：
// 界面只需要"今天的世界/国内/科技"，放开参数只会让每次生成的口径不可控。
var broadcastNewsBuckets = []struct {
	Label string
	Query string
}{
	{Label: "世界局势", Query: "今日 国际局势 要闻"},
	{Label: "国内大事", Query: "今日 国内 要闻 政策 民生"},
	{Label: "科技新闻", Query: "今日 科技 人工智能 新产品 发布"},
}

// loadBroadcastFeedConfig 取唯一一行外部数据源配置；还没有配置行时返回零值。
func loadBroadcastFeedConfig() model.BroadcastFeedConfig {
	var cfg model.BroadcastFeedConfig
	if err := repository.DB.First(&cfg).Error; err != nil {
		return model.BroadcastFeedConfig{}
	}
	return cfg
}

// GetBroadcastFeedConfig 播音组与技术组都要读它（前者要知道能不能生成），
// 但一律不回传密钥本体。
func (pc *PublicityController) GetBroadcastFeedConfig(c *gin.Context) {
	cfg := loadBroadcastFeedConfig()
	c.JSON(http.StatusOK, gin.H{
		"weather_city":   cfg.WeatherCity,
		"search_enabled": cfg.SearchEnabled,
		"has_key":        strings.TrimSpace(cfg.SearchAPIKey) != "",
		"key_mask":       secretbox.Mask(cfg.SearchAPIKey),
		"updated_at":     cfg.UpdatedAt,
		"topics":         broadcastNewsBucketLabels(),
	})
}

// UpdateBroadcastFeedConfig 仅技术维护组可改（Casbin 的 /publicity/* 对部员也开放 PUT，
// 所以这道判定必须在 controller 层，漏了就是任意部员能改全校播报的数据源）。
func (pc *PublicityController) UpdateBroadcastFeedConfig(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if operator.Role != model.RoleTechAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅技术维护组可配置播报数据源"})
		return
	}

	var req struct {
		WeatherCity   *string `json:"weather_city"`
		SearchEnabled *bool   `json:"search_enabled"`
		APIKey        *string `json:"api_key"`
		ClearKey      *bool   `json:"clear_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式错误: " + err.Error()})
		return
	}

	cfg := loadBroadcastFeedConfig()
	keyState := "保持不变"

	if req.WeatherCity != nil {
		city := strings.TrimSpace(*req.WeatherCity)
		if utf8.RuneCountInString(city) > 32 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "城市名过长（不超过 32 个字）"})
			return
		}
		cfg.WeatherCity = city
	}
	if req.SearchEnabled != nil {
		cfg.SearchEnabled = *req.SearchEnabled
	}
	if req.APIKey != nil {
		key := strings.TrimSpace(*req.APIKey)
		if key == "" {
			// 与福利网关同一口径：留空表示保留原值。真要清空请显式传 clear_key。
			keyState = "保持不变（提交为空）"
		} else {
			cfg.SearchAPIKey = key
			keyState = "已更新"
		}
	}
	if req.ClearKey != nil && *req.ClearKey {
		cfg.SearchAPIKey = ""
		keyState = "已清空"
	}

	cfg.UpdatedAt = time.Now()
	if err := repository.DB.Save(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}

	// 审计只记结构与密钥状态，绝不记密钥本体，也不记任何讲稿正文
	logOperationAs(c, operator, "broadcast.feed_config", "broadcast_feed_config", cfg.ID,
		fmt.Sprintf("天气城市=%s；新闻检索=%s；密钥=%s",
			orDefault(cfg.WeatherCity, "（未填）"),
			map[bool]string{true: "开", false: "关"}[cfg.SearchEnabled],
			keyState))

	c.JSON(http.StatusOK, gin.H{
		"message":        "播报数据源配置已保存",
		"weather_city":   cfg.WeatherCity,
		"search_enabled": cfg.SearchEnabled,
		"has_key":        strings.TrimSpace(cfg.SearchAPIKey) != "",
		"key_mask":       secretbox.Mask(cfg.SearchAPIKey),
	})
}

// GenerateBroadcastScript 生成今日讲稿：只读不写，返回稿子与出处供人工核对。
func (pc *PublicityController) GenerateBroadcastScript(c *gin.Context) {
	cfg := loadBroadcastFeedConfig()
	if !cfg.SearchEnabled || strings.TrimSpace(cfg.SearchAPIKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":  "feed_not_configured",
			"error": "尚未开启新闻检索或未配置检索密钥：请技术维护组在「播报数据源」里填好检索密钥、打开开关并设置天气城市。",
		})
		return
	}

	var textCfg model.AIConfig
	if err := repository.DB.Where("config_key = ?", "text_engine").First(&textCfg).Error; err != nil || !ai.IsConfigured(&textCfg) {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":      "ai_not_configured",
			"ai_status": "disabled",
			"error":     "尚未配置可用的文本 AI 引擎：讲稿正文需要它来写，请技术维护组在「AI 引擎配置与调度中枢」填好端点、密钥与模型名并启用。",
		})
		return
	}

	// 1. 三路检索。全挂 = 没有素材，直接报错；部分挂 = 如实标注，用剩下的继续
	news, failedBuckets := collectBroadcastNews(cfg.SearchAPIKey)
	if len(news) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{
			"code":   "news_upstream_failed",
			"error":  "三路新闻检索全部失败，没有可用素材。为避免编造内容，本次不生成讲稿，请稍后重试。",
			"failed": failedBuckets,
		})
		return
	}

	// 2. 天气：先查当天缓存，命中就绝不再打上游
	weather, weatherSource, weatherErr := todayBroadcastWeather(cfg.WeatherCity)

	// 3. 让模型只写三条新闻正文，开场白与天气由 Go 拼
	body, err := writeBroadcastNewsBody(&textCfg, news)
	if err != nil {
		// 模型不合规就报错，不拿素材原文硬凑一段"看起来像稿子"的东西
		c.JSON(http.StatusBadGateway, gin.H{
			"code":  "ai_upstream_failed",
			"error": "文本引擎没有产出合规的讲稿正文：" + err.Error(),
		})
		return
	}

	words := countRunes(body)
	if words < broadcastNewsHardMin || words > broadcastNewsHardMax {
		c.JSON(http.StatusBadGateway, gin.H{
			"code":       "ai_output_off_spec",
			"error":      fmt.Sprintf("文本引擎产出的正文 %d 字，超出可接受区间（%d–%d 字），已拒绝采用。请重试或调整文本引擎的系统提示词。", words, broadcastNewsHardMin, broadcastNewsHardMax),
			"news_words": words,
		})
		return
	}

	script := assembleBroadcastScript(body, weather, weatherErr)

	warnings := make([]string, 0, 3)
	if len(failedBuckets) > 0 {
		warnings = append(warnings, "有 "+fmt.Sprintf("%d", len(failedBuckets))+" 类新闻检索失败（"+strings.Join(failedBuckets, "、")+"），本批只用了其余主题")
	}
	if weatherErr != nil {
		warnings = append(warnings, "天气未取到（"+weatherErr.Error()+"），稿子里的天气句已写成待补充，请播音时以实际为准")
	}
	if weatherSource == "cache" {
		warnings = append(warnings, "天气用的是今天已缓存的那一条（上游今天只调一次）")
	}

	c.JSON(http.StatusOK, gin.H{
		"script":         script,
		"news_words":     words,
		"target_words":   fmt.Sprintf("%d–%d", broadcastNewsTargetMin, broadcastNewsTargetMax),
		"target_ok":      words >= broadcastNewsTargetMin && words <= broadcastNewsTargetMax,
		"weather":        weather,
		"weather_source": weatherSource,
		"news_refs":      news,
		"warnings":       warnings,
		"model":          textCfg.ModelName,
		"generated_at":   time.Now().Format("2006-01-02 15:04:05"),
		"saved":          false,
	})
}

// broadcastNewsRef 一条喂给模型的素材，同时原样回给界面做人工核对。
type broadcastNewsRef struct {
	Bucket  string `json:"bucket"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func collectBroadcastNews(apiKey string) ([]broadcastNewsRef, []string) {
	refs := make([]broadcastNewsRef, 0, len(broadcastNewsBuckets)*broadcastNewsPerTopic)
	failed := make([]string, 0, len(broadcastNewsBuckets))
	for _, bucket := range broadcastNewsBuckets {
		items, err := feed.SearchNews(apiKey, bucket.Query, broadcastNewsPerTopic)
		if err != nil {
			failed = append(failed, bucket.Label)
			continue
		}
		for _, it := range items {
			refs = append(refs, broadcastNewsRef{
				Bucket:  bucket.Label,
				Title:   it.Title,
				URL:     it.URL,
				Snippet: clipRunes(it.Content, 160),
			})
		}
	}
	return refs, failed
}

// todayBroadcastWeather 按日缓存取天气。返回值里的 source 是 cache/online/none。
func todayBroadcastWeather(city string) (*feed.Weather, string, error) {
	city = strings.TrimSpace(city)
	if city == "" {
		return nil, "none", fmt.Errorf("技术组还没有填写天气城市")
	}
	today := time.Now().Format("2006-01-02")

	var row model.BroadcastWeatherCache
	if err := repository.DB.Where("date = ?", today).First(&row).Error; err == nil {
		return &feed.Weather{
			Province: row.Province, City: row.City, Weather: row.Weather,
			Temperature: row.Temperature, WindDirection: row.WindDirection,
			WindPower: row.WindPower, Humidity: row.Humidity,
		}, "cache", nil
	}

	w, err := feed.FetchWeather(city)
	if err != nil {
		return nil, "none", err
	}
	row = model.BroadcastWeatherCache{
		Date: today, City: w.City, Province: w.Province, Weather: w.Weather,
		Temperature: string(w.Temperature), WindDirection: w.WindDirection,
		WindPower: string(w.WindPower), Humidity: string(w.Humidity), FetchedAt: time.Now(),
	}
	// 落缓存失败不影响本次播报，只是下次还要再打一次上游
	_ = repository.DB.Save(&row).Error
	return w, "online", nil
}

// writeBroadcastNewsBody 只向模型要三条新闻正文，格式与字数在提示词里钉死。
func writeBroadcastNewsBody(cfg *model.AIConfig, refs []broadcastNewsRef) (string, error) {
	var sb strings.Builder
	for _, r := range refs {
		sb.WriteString("【")
		sb.WriteString(r.Bucket)
		sb.WriteString("】")
		sb.WriteString(r.Title)
		if r.Snippet != "" {
			sb.WriteString(" —— ")
			sb.WriteString(r.Snippet)
		}
		sb.WriteString("\n")
	}

	system := "你是校园广播站的播音稿撰稿助手。只写用户要求的三条新闻正文，" +
		"不得输出开场白、天气、标题、解释或任何多余文字。" +
		"三条分别对应【世界局势】【国内大事】【科技新闻】，各占一行，行首依次是 1. 2. 3.；" +
		"三行合计 120 到 150 个字；只允许使用给定素材里的事实，不得补充素材中没有的数字、人名、结论或展望；" +
		"素材不足时如实概括，不要编造。"

	user := "今日素材（每行一条，方括号内是主题）：\n" + sb.String() +
		"\n请据此写出三条新闻正文，格式：\n1. ……\n2. ……\n3. ……"

	out, err := ai.ChatCompletion(
		ai.ResolveChatEndpoint(cfg.Endpoint), cfg.APIKey, cfg.ModelName,
		[]ai.ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		0.4, 600,
	)
	if err != nil {
		return "", err
	}
	return normalizeBroadcastNewsBody(out), nil
}

// normalizeBroadcastNewsBody 把模型常见的 Markdown 列表符号与多余空行收敛成纯文本三行。
func normalizeBroadcastNewsBody(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s == "" {
			continue
		}
		s = strings.TrimPrefix(s, "- ")
		s = strings.TrimPrefix(s, "* ")
		s = strings.TrimSpace(s)
		if s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, "\n")
}

// assembleBroadcastScript 固定框架由代码拼：开场白 + 模型写的三条 + 真实天气句。
func assembleBroadcastScript(newsBody string, w *feed.Weather, weatherErr error) string {
	var sb strings.Builder
	sb.WriteString("世界新闻早知道，今日新闻我来报。学管会带你了解今日新闻。\n")
	sb.WriteString(strings.TrimSpace(newsBody))
	sb.WriteString("\n\n下面是天气预报：\n")
	sb.WriteString(broadcastWeatherLine(w, weatherErr))
	return sb.String()
}

// broadcastWeatherLine 天气句完全来自接口字段：取不到就写"待补充"，绝不拿一句套话冒充真天气。
func broadcastWeatherLine(w *feed.Weather, err error) string {
	if err != nil || w == nil {
		return "今日天气数据暂未取到，请播音时以室外实际观察为准，并在播报前补充。"
	}
	city := strings.TrimSpace(w.City)
	if city == "" {
		city = strings.TrimSpace(w.Province)
	}
	parts := make([]string, 0, 5)
	if city != "" {
		parts = append(parts, "今日"+city)
	}
	if sky := strings.TrimSpace(w.Weather); sky != "" {
		parts = append(parts, "天气"+sky)
	}
	if t := strings.TrimSpace(string(w.Temperature)); t != "" {
		parts = append(parts, "温度"+t+"℃")
	}
	if wd := strings.TrimSpace(w.WindDirection); wd != "" {
		wp := strings.TrimSpace(string(w.WindPower))
		if wp != "" {
			wd += wp
		}
		parts = append(parts, "风向风力"+wd)
	}
	if h := strings.TrimSpace(string(w.Humidity)); h != "" {
		parts = append(parts, "相对湿度"+h+"%")
	}
	if len(parts) == 0 {
		return "今日天气数据暂未取到，请播音时以室外实际观察为准，并在播报前补充。"
	}
	return strings.Join(parts, "，") + "。"
}

func broadcastNewsBucketLabels() []string {
	out := make([]string, 0, len(broadcastNewsBuckets))
	for _, b := range broadcastNewsBuckets {
		out = append(out, b.Label)
	}
	return out
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func countRunes(s string) int {
	return utf8.RuneCountInString(strings.ReplaceAll(s, "\n", ""))
}
