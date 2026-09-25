package ai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xgh-system/internal/model"
)

func configuredCfg(endpoint string) *model.AIConfig {
	return &model.AIConfig{
		ConfigKey: "vision_engine",
		Provider:  "openai_compatible",
		Endpoint:  endpoint,
		APIKey:    "sk-real-key-for-tests",
		ModelName: "gpt-4o-mini",
		IsEnabled: true,
		MaxTokens: 512,
	}
}

func stubUpstream(content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": content}},
			},
		})
	}))
}

func TestIsConfiguredRejectsUnusableConfigs(t *testing.T) {
	cases := []struct {
		name string
		cfg  *model.AIConfig
	}{
		{"nil 配置", nil},
		{"未启用", &model.AIConfig{Endpoint: "https://x/v1", APIKey: "sk-1", IsEnabled: false}},
		{"缺 Endpoint", &model.AIConfig{APIKey: "sk-1", IsEnabled: true}},
		{"缺 APIKey", &model.AIConfig{Endpoint: "https://x/v1", IsEnabled: true}},
		{"placeholder 密钥", &model.AIConfig{Endpoint: "https://x/v1", APIKey: "sk-placeholder", IsEnabled: true}},
	}
	for _, tc := range cases {
		if IsConfigured(tc.cfg) {
			t.Errorf("%s: 应判定为未配置", tc.name)
		}
	}
}

func TestCallVisionAINeverFabricatesWhenUnconfigured(t *testing.T) {
	out, err := CallVisionAI("/uploads/a.jpg", "violation", "302", &model.AIConfig{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("期望 ErrNotConfigured，实际 %v", err)
	}
	if out != "" {
		t.Fatalf("引擎未配置时必须返回空文本，实际返回了生成内容: %q", out)
	}
}

func TestCallTextAINeverFabricatesWhenUnconfigured(t *testing.T) {
	res, err := CallTextAI("任意巡查描述", "violation", &model.AIConfig{Endpoint: "https://x/v1"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("期望 ErrNotConfigured，实际 %v", err)
	}
	if res != nil {
		t.Fatalf("引擎未配置时不得产生扣分结论，实际: %+v", res)
	}
}

func TestProcessPipelineDisabledWhenUnconfigured(t *testing.T) {
	_, structured, status, err := ProcessMultimodalAndText("/uploads/a.jpg", "violation", "302", &model.AIConfig{}, &model.AIConfig{})
	if status != StatusDisabled {
		t.Fatalf("期望 status=%s，实际 %s", StatusDisabled, status)
	}
	if structured != nil {
		t.Fatalf("未配置时 structured 必须为 nil")
	}
	if err == nil {
		t.Fatalf("未配置时必须返回错误，让上层知晓本次未识别")
	}
}

func TestProcessPipelineFailedOnUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "invalid api key"}})
	}))
	defer upstream.Close()

	visionCfg := configuredCfg(upstream.URL)
	_, structured, status, err := ProcessMultimodalAndText("https://img.example.com/a.jpg", "violation", "302", visionCfg, visionCfg)
	if status != StatusFailed {
		t.Fatalf("上游报错时期望 status=%s，实际 %s", StatusFailed, status)
	}
	if structured != nil {
		t.Fatalf("上游报错时不得产生扣分结论")
	}
	if err == nil {
		t.Fatalf("上游报错时必须返回错误")
	}
}

func TestProcessPipelineRealOnlyWhenBothStagesSucceed(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := "现场发现桌面放置电磁炉并有多级插排串联"
		if calls > 1 {
			content = `{"category":"违规大功率用电","severity":"high","deduct_points":5,"summary":"查获电磁炉","action_advice":"限24小时整改"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	defer upstream.Close()

	visionCfg := configuredCfg(upstream.URL)
	textCfg := configuredCfg(upstream.URL)
	raw, structured, status, err := ProcessMultimodalAndText("https://img.example.com/a.jpg", "violation", "302", visionCfg, textCfg)
	if err != nil {
		t.Fatalf("两阶段均成功时不应报错: %v", err)
	}
	if status != StatusReal {
		t.Fatalf("期望 status=%s，实际 %s", StatusReal, status)
	}
	if structured == nil || structured.DeductPoints != 5 {
		t.Fatalf("期望来自模型的结构化结论，实际: %+v", structured)
	}
	if !strings.Contains(raw, "电磁炉") {
		t.Fatalf("期望返回上游原文，实际: %q", raw)
	}
}

func TestVisionRejectsUnreachableLocalImagePath(t *testing.T) {
	upstream := stubUpstream("不应被调用")
	defer upstream.Close()

	_, err := CallVisionAI("/uploads/sample_violation.jpg", "violation", "302", configuredCfg(upstream.URL))
	if err == nil {
		t.Fatalf("本地相对路径外部模型取不到，必须报错而不是产出不可信结论")
	}
	if !strings.Contains(err.Error(), "本地路径") {
		t.Fatalf("错误信息应说明根因，实际: %v", err)
	}
}

func TestStubUpstreamContract(t *testing.T) {
	upstream := stubUpstream("hello")
	defer upstream.Close()
	out, err := CallVisionAI("https://img.example.com/a.jpg", "violation", "302", configuredCfg(upstream.URL))
	if err != nil || out != "hello" {
		t.Fatalf("stub 上游契约异常: out=%q err=%v", out, err)
	}
}
