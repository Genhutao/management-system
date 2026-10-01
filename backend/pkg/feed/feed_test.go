package feed

// 这个包只有两件值得钉死的事：密钥不会被发给不该发的地方，
// 以及上游把字段类型改掉时不要整个功能一起崩。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearchNewsSendsBearerAndParsesResults(t *testing.T) {
	var hits int32
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotAuth = r.Header.Get("Authorization")
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"甲","url":"https://x/1","content":"正文一","published_date":"2026-10-01"},{"title":"  ","url":"https://x/2","content":"   "}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FEED_SEARCH_ENDPOINT", srv.URL)

	items, err := SearchNews("tvly-test-key", "今日 国内 要闻", 5)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("标题与正文都为空的条目应被丢掉，实际 %d 条: %+v", len(items), items)
	}
	if items[0].Title != "甲" || items[0].Published != "2026-10-01" {
		t.Errorf("字段解析不对: %+v", items[0])
	}
	if gotAuth != "Bearer tvly-test-key" {
		t.Errorf("鉴权头不对: %q", gotAuth)
	}
	// topic 与 time_range 写死在代码里：讲稿要的就是"今天的新闻"
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("请求体不是 JSON: %v / %s", err, gotBody)
	}
	if body["topic"] != "news" || body["time_range"] != "day" {
		t.Errorf("检索口径被改动了: %v", body)
	}
}

// 带密钥的请求绝不走明文 HTTP 出本机：那等于把密钥发给路径上的任何人。
func TestSearchNewsRefusesInsecureRemoteEndpoint(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	t.Cleanup(srv.Close)

	// 用一个非回环的 http 地址：请求应当在发出之前就被拒掉
	t.Setenv("FEED_SEARCH_ENDPOINT", "http://news-upstream.invalid/search")
	if _, err := SearchNews("tvly-test-key", "任意检索词", 3); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("明文 HTTP 远端应被拒绝并说明原因，实际 err=%v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatal("被拒的端点不该真的收到请求")
	}
}

func TestSearchNewsValidatesInputs(t *testing.T) {
	if _, err := SearchNews("", "词", 3); err == nil || !strings.Contains(err.Error(), "密钥") {
		t.Errorf("空密钥应直接报错，实际 %v", err)
	}
	if _, err := SearchNews("k", "   ", 3); err == nil {
		t.Error("空检索词应报错")
	}
}

// 上游实测把 temperature 发成数字；字段类型一变就整个功能崩是不可接受的。
func TestWeatherToleratesMixedFieldTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "%E6%9D%AD%E5%B7%9E") {
			t.Errorf("城市名没有正确转义: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"province":"浙江省","city":"杭州市","weather":null,"temperature":22,"wind_direction":"西北风","wind_power":"2级","humidity":"79"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FEED_WEATHER_ENDPOINT", srv.URL)

	w, err := FetchWeather("杭州")
	if err != nil {
		t.Fatalf("取天气失败: %v", err)
	}
	if w.Temperature != "22" {
		t.Errorf("数字温度应收成文本 22，实际 %q", w.Temperature)
	}
	if w.Humidity != "79" || w.WindPower != "2级" {
		t.Errorf("字段解析不对: %+v", w)
	}
	if w.Weather != "" {
		t.Errorf("null 应收成空串，实际 %q", w.Weather)
	}
	if s := w.Summary(); !strings.Contains(s, "气温22摄氏度") || strings.Contains(s, "null") {
		t.Errorf("Summary 拼接受字段类型影响: %s", s)
	}
}

// 200 但内容空 = 没有数据，不能拿去拼一句看起来真实的天气。
func TestWeatherRejectsEmptyPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"province":"浙江省","city":"杭州市"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FEED_WEATHER_ENDPOINT", srv.URL)

	if _, err := FetchWeather("杭州"); err == nil || !strings.Contains(err.Error(), "没有返回可用数据") {
		t.Fatalf("空内容应报错，实际 %v", err)
	}
}

func TestWeatherSurfacesUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FEED_WEATHER_ENDPOINT", srv.URL)

	_, err := FetchWeather("杭州")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("限流要如实上报，实际 %v", err)
	}
}

func TestFetchWeatherRequiresCity(t *testing.T) {
	if _, err := FetchWeather("  "); err == nil || !strings.Contains(err.Error(), "城市") {
		t.Errorf("空城市应报错，实际 %v", err)
	}
}
