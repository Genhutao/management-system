package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xgh-system/internal/model"
)

// sseServer 起一个 OpenAI 兼容的假上游。calls 记录服务端收到的报文，
// 用来断言 stream 标记真的带上了 —— 少了这个标记，上游根本不会流式返回。
func sseServer(t *testing.T, body string, calls *map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		(*calls)["payload"] = payload
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
}

func cfgFor(url string) *model.AIConfig {
	return &model.AIConfig{
		ConfigKey: "text_engine", DisplayName: "文本", Provider: "openai_compatible",
		Endpoint: url, APIKey: "sk-test", ModelName: "m", SystemPrompt: "p",
		Temperature: 0.2, MaxTokens: 512, IsEnabled: true,
	}
}

func TestStreamChatPassesThroughReasoning(t *testing.T) {
	calls := map[string]interface{}{}
	srv := sseServer(t, strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"先看图里有没有电器"}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"，再判断严重级别"}}]}`,
		`data: {"choices":[{"delta":{"content":"{\"category\":\"违规电器\"}"}}]}`,
		`data: [DONE]`,
		"",
	}, "\n"), &calls)
	defer srv.Close()

	events := make(chan StreamEvent, 16)
	out, err := StreamChat(cfgFor(srv.URL), map[string]interface{}{"model": "m"}, func(e StreamEvent) error {
		events <- e
		return nil
	})
	if err != nil {
		t.Fatalf("流式调用失败: %v", err)
	}

	if payload, _ := calls["payload"].(map[string]interface{}); payload["stream"] != true {
		t.Errorf("请求必须带 stream:true，实际 payload %v", payload)
	}
	if !out.ReasoningSeen {
		t.Errorf("收到 reasoning_content 却没标记 ReasoningSeen")
	}
	if out.Reasoning != "先看图里有没有电器，再判断严重级别" {
		t.Errorf("推理内容应逐段累积，实际 %q", out.Reasoning)
	}
	if out.Content != `{"category":"违规电器"}` {
		t.Errorf("正文应逐段累积，实际 %q", out.Content)
	}

	close(events)
	var got []StreamEvent
	for e := range events {
		got = append(got, e)
	}
	if len(got) != 3 {
		t.Fatalf("应推给前端 3 个片段，实际 %d: %v", len(got), got)
	}
	if got[0].Type != EventTypeReasoning || got[2].Type != EventTypeContent {
		t.Errorf("事件类型顺序不对: %v", got)
	}
}

// 模型不给推理内容时，必须能被前端察觉。把步骤日志冒充思考过程是这轮最容易被做坏的地方。
func TestStreamChatReportsAbsentReasoning(t *testing.T) {
	calls := map[string]interface{}{}
	srv := sseServer(t, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\ndata: [DONE]\n\n", &calls)
	defer srv.Close()

	out, err := StreamChat(cfgFor(srv.URL), map[string]interface{}{}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if out.ReasoningSeen {
		t.Errorf("上游没有推理字段，ReasoningSeen 不该为真")
	}
	if out.Reasoning != "" {
		t.Errorf("不该凭空造出推理内容: %q", out.Reasoning)
	}
}

func TestStreamChatAcceptsReasoningAlias(t *testing.T) {
	calls := map[string]interface{}{}
	srv := sseServer(t, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"别名写法\"}}]}\ndata: [DONE]\n\n", &calls)
	defer srv.Close()

	out, err := StreamChat(cfgFor(srv.URL), map[string]interface{}{}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if !out.ReasoningSeen || out.Reasoning != "别名写法" {
		t.Errorf("OpenRouter 风格的 reasoning 字段也要认，实际 seen=%v 内容 %q", out.ReasoningSeen, out.Reasoning)
	}
}

func TestStreamChatSurfacesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided"}}`)
	}))
	defer srv.Close()

	var gotErr string
	_, err := StreamChat(cfgFor(srv.URL), map[string]interface{}{}, func(e StreamEvent) error {
		if e.Type == EventTypeError {
			gotErr = e.Text
		}
		return nil
	})
	if err == nil {
		t.Fatalf("401 必须报错，不能当成识别成功")
	}
	// 报错要能把"密钥不对"这类真实原因带到界面上，而不是只留一个 HTTP 码
	if !strings.Contains(err.Error(), "Incorrect API key") || !strings.Contains(gotErr, "401") {
		t.Errorf("错误原因未如实透出: err=%v event=%q", err, gotErr)
	}
}

func TestStreamChatRejectsUnconfiguredEngine(t *testing.T) {
	_, err := StreamChat(&model.AIConfig{Endpoint: "http://x/chat/completions"}, map[string]interface{}{}, func(StreamEvent) error { return nil })
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("未配置引擎应返回 ErrNotConfigured，实际 %v", err)
	}
}

// 浏览器关掉页面后 emit 会失败，此时必须停止读上游，不能继续白烧 token。
func TestStreamChatStopsWhenClientDisconnects(t *testing.T) {
	calls := map[string]interface{}{}
	srv := sseServer(t, strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"a"}}]}`,
		`data: {"choices":[{"delta":{"content":"b"}}]}`,
		`data: {"choices":[{"delta":{"content":"c"}}]}`,
		`data: [DONE]`,
		"",
	}, "\n"), &calls)
	defer srv.Close()

	stop := errors.New("客户端已断开")
	n := 0
	_, err := StreamChat(cfgFor(srv.URL), map[string]interface{}{}, func(StreamEvent) error {
		n++
		if n >= 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("emit 报错应原样返回，实际 %v", err)
	}
	if n != 2 {
		t.Errorf("断开后不该继续读，实际仍收到 %d 个片段", n)
	}
}
