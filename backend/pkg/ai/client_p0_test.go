package ai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestVisionRejectsMissingLocalImagePath(t *testing.T) {
	upstream := stubUpstream("不应被调用")
	defer upstream.Close()

	_, err := CallVisionAI("/uploads/definitely-missing.jpg", "violation", "302", configuredCfg(upstream.URL))
	if err == nil {
		t.Fatalf("本地图片不存在时必须报错")
	}
	if !strings.Contains(err.Error(), "读取本地上传图片失败") {
		t.Fatalf("错误信息应说明根因，实际: %v", err)
	}
}

func TestVisionInlinesLocalImageAsDataURL(t *testing.T) {
	var receivedURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Messages []struct {
				Content []struct {
					Type     string `json:"type"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &payload)
		for _, m := range payload.Messages {
			for _, part := range m.Content {
				if part.Type == "image_url" {
					receivedURL = part.ImageURL.URL
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0600); err != nil {
		t.Fatalf("写临时图片失败: %v", err)
	}

	// 生产上入库的是 /uploads/xxx 相对路径；切到临时目录模拟同构环境
	t.Chdir(dir)
	if _, err := CallVisionAI("/shot.png", "violation", "302", configuredCfg(upstream.URL)); err != nil {
		t.Fatalf("本地图存在时识别应可执行: %v", err)
	}
	if !strings.HasPrefix(receivedURL, "data:image/png;base64,") {
		t.Fatalf("上游应收到 base64 data URL，实际前缀: %q", truncate(receivedURL))
	}
}

func truncate(s string) string {
	if len(s) > 30 {
		return s[:30]
	}
	return s
}

func TestStubUpstreamContract(t *testing.T) {
	upstream := stubUpstream("hello")
	defer upstream.Close()
	out, err := CallVisionAI("https://img.example.com/a.jpg", "violation", "302", configuredCfg(upstream.URL))
	if err != nil || out != "hello" {
		t.Fatalf("stub 上游契约异常: out=%q err=%v", out, err)
	}
}
