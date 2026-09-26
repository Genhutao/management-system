package ai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xgh-system/internal/model"
)

// 透传终端与对话排班都走 ChatCompletion：这里锁住"发出去的请求长什么样"和
// "拿不到正文时绝不能返回一段兜底话术"两件事。

func TestResolveChatEndpointHandlesCommonSpellings(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"   ":                        "",
		"https://api.openai.com/v1":  "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1/": "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com":     "https://api.openai.com/v1/chat/completions",
		"https://api.deepseek.com":   "https://api.deepseek.com/v1/chat/completions",
		"https://example.com/v1/chat/completions":       "https://example.com/v1/chat/completions",
		"https://gw.internal:11434/v1/chat/completions": "https://gw.internal:11434/v1/chat/completions",
	}
	for in, want := range cases {
		if got := ResolveChatEndpoint(in); got != want {
			t.Errorf("ResolveChatEndpoint(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestChatCompletionSendsRealMultiTurnRequest(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "  第二句话  "}}},
		})
	}))
	defer upstream.Close()

	msgs := []ChatMessage{
		{Role: "system", Content: "服务端系统提示词"},
		{Role: "user", Content: "上一轮提问"},
		{Role: "assistant", Content: "上一轮回答"},
		{Role: "user", Content: "本轮提问"},
	}
	out, err := ChatCompletion(upstream.URL+"/chat/completions", "sk-test-key", "deepseek-chat", msgs, 0.7, 0)
	if err != nil {
		t.Fatalf("正常上游应成功: %v", err)
	}
	if out != "第二句话" {
		t.Fatalf("应返回去掉首尾空白的正文，实际 %q", out)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Fatalf("密钥应只出现在发往上游的请求头里，实际 %q", gotAuth)
	}
	if gotBody["model"] != "deepseek-chat" {
		t.Fatalf("模型名应原样透传，实际 %v", gotBody["model"])
	}
	sent, _ := json.Marshal(gotBody["messages"])
	if !strings.Contains(string(sent), "上一轮回答") {
		t.Fatalf("多轮上下文必须带到上游，实际 %s", sent)
	}
	if _, ok := gotBody["max_tokens"]; ok {
		t.Fatalf("maxTokens=0 表示不限制，不应把 0 发给上游: %s", sent)
	}
}

func TestChatCompletionNeverFabricates(t *testing.T) {
	upstream := func(handler http.HandlerFunc) string {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		return srv.URL
	}
	jsonBody := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = io.WriteString(w, body)
		}
	}

	cases := []struct {
		name     string
		endpoint string
		key      string
		body     http.HandlerFunc
		wantErr  string
	}{
		{
			name:    "端点为空",
			key:     "sk-x",
			wantErr: "AI 引擎未配置",
		},
		{
			name:     "密钥为空",
			endpoint: "https://api.openai.com/v1/chat/completions",
			wantErr:  "AI 引擎未配置",
		},
		{
			name:    "上游 401",
			key:     "sk-x",
			body:    jsonBody(http.StatusUnauthorized, `{"error":{"message":"Incorrect API key"}}`),
			wantErr: "上游模型返回 401",
		},
		{
			name:    "上游 429 且返回整页 HTML",
			key:     "sk-x",
			body:    jsonBody(http.StatusTooManyRequests, "<html>网关限流<br>请稍后再试</html>"),
			wantErr: "上游模型返回 429",
		},
		{
			name:    "200 但带 error 字段",
			key:     "sk-x",
			body:    jsonBody(http.StatusOK, `{"error":{"message":"余额不足"}}`),
			wantErr: "余额不足",
		},
		{
			name:    "200 但无候选",
			key:     "sk-x",
			body:    jsonBody(http.StatusOK, `{"choices":[]}`),
			wantErr: "未返回任何候选",
		},
		{
			name:    "200 但正文空白",
			key:     "sk-x",
			body:    jsonBody(http.StatusOK, `{"choices":[{"message":{"content":"   "}}]}`),
			wantErr: "返回空内容",
		},
		{
			name:    "报文不是 JSON",
			key:     "sk-x",
			body:    jsonBody(http.StatusOK, `upstream blew up`),
			wantErr: "报文解析失败",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := tc.endpoint
			if tc.body != nil {
				endpoint = upstream(tc.body)
			}
			out, err := ChatCompletion(endpoint, tc.key, "m", []ChatMessage{{Role: "user", Content: "hi"}}, 0.7, 0)
			if err == nil {
				t.Fatalf("必须返回错误，实际返回了 %q", out)
			}
			if out != "" {
				t.Fatalf("失败时不得带回任何应答文本，实际 %q", out)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误应说明根因 %q，实际 %v", tc.wantErr, err)
			}
			if strings.Contains(err.Error(), "hi") {
				t.Fatalf("不得把调用方的提问回声成应答: %v", err)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Fatalf("上游报文详情只保留首行: %q", err.Error())
			}
		})
	}
}

func TestChatCompletionDistinguishesUnconfiguredFromFailure(t *testing.T) {
	_, err := ChatCompletion("", "", "m", nil, 0, 0)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("未配置必须是 ErrNotConfigured（调用方据此区分 400 与 502），实际 %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err = ChatCompletion(srv.URL, "sk-x", "m", []ChatMessage{{Role: "user", Content: "q"}}, 0, 0)
	if err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("配置齐全但上游失败时不得归为未配置，实际 %v", err)
	}
}

func TestChatCompletionRejectsHugeUpstreamBody(t *testing.T) {
	// 异常端点返回超大报文时，读取上限既保护内存，也不应让解析"成功"出内容
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+strings.Repeat("A", 1<<20)+`"}}]}`)
	}))
	defer srv.Close()

	out, err := ChatCompletion(srv.URL, "sk-x", "m", []ChatMessage{{Role: "user", Content: "q"}}, 0, 0)
	if err == nil {
		t.Fatalf("超限报文必须判为失败，实际返回长度 %d", len(out))
	}
	if len(out) != 0 {
		t.Fatalf("失败时不得返回半截正文，实际 %d 字节", len(out))
	}
}

func TestNormalizeVisionImageRejectsArbitraryStrings(t *testing.T) {
	// 只放行本地上游件 / data:image / http(s)，防止任意字符串被当图片发给模型
	for _, bad := range []string{
		"",
		"   ",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:application/x-sh;base64,cm0gLXJm",
		"localhost:11434/img.png",
	} {
		if _, err := normalizeVisionImage(bad); err == nil {
			t.Errorf("图片来源 %q 必须被拒绝", bad)
		}
	}
	for _, ok := range []string{
		"https://img.example.com/a.jpg",
		"http://img.example.com/a.jpg",
		"data:image/png;base64,AAAA",
	} {
		if got, err := normalizeVisionImage(ok); err != nil || got != ok {
			t.Errorf("图片来源 %q 应原样放行: got=%q err=%v", ok, got, err)
		}
	}

	oversized := "data:image/png;base64," + strings.Repeat("A", maxInlineImageBytes)
	if _, err := normalizeVisionImage(oversized); err == nil {
		t.Fatal("超过 4MB 的 data URL 必须被拒绝，否则请求体会把上游打爆")
	}
}

func TestCallVisionDescriptionRequiresConfiguredEngine(t *testing.T) {
	if _, err := CallVisionDescription(&model.AIConfig{IsEnabled: true}, "https://x/y.jpg", "读图"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("引擎未配置时应返回 ErrNotConfigured，实际 %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := CallVisionDescription(configuredCfg(srv.URL), "https://x/y.jpg", "读图"); err == nil {
		t.Fatal("上游 500 时必须报错而不是返回空转写")
	}
}
