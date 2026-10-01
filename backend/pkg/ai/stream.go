package ai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"xgh-system/internal/model"
)

// 流式调用通道。与 client.go 里的一次性调用并存，旧路径不动，新场景（宿管端逐步识别）走这里。
//
// 之所以要单开一个文件：识别过程需要把"模型正在想什么、卡在哪一步"实时吐给浏览器，
// 而 ChatCompletion 是一次性读完整个 body，拿不到中间态。

// 事件类型。前端按类型分区渲染，不要在这里塞业务语义。
const (
	EventTypeReasoning = "reasoning" // 模型推理内容（只有具备深度思考输出的模型才会给）
	EventTypeContent   = "content"   // 模型正式输出
	EventTypeError     = "error"     // 上游报错原文（截断后），必须如实透出，不许换成"识别失败"这种模糊话
)

// StreamEvent 一个增量片段。
type StreamEvent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// StreamOutcome 一次流式调用的汇总结果。
type StreamOutcome struct {
	Content   string
	Reasoning string
	// ReasoningSeen 表示这次调用**确实**收到过推理内容。
	// 没收到时必须让界面如实显示"本引擎未输出推理链"，不能拿步骤日志拼一份假的思考过程冒充。
	ReasoningSeen bool
	ElapsedMS     int64
	Chunks        int
}

// streamClientTimeout 覆盖整个流式响应的读取过程。识别一次要给宿管看到进展，
// 宁可比一次性调用宽松，但必须有上限：没有超时的流式请求会一直挂着 goroutine。
const streamClientTimeout = 150 * time.Second

// maxStreamLineBytes 单行 SSE 的读取上限。沿用非流式那套 body 上限的思路，
// 异常端点吐超长单行时不能把内存吃光（这里按 512KB 收紧，比整包上限更小）。
const maxStreamLineBytes = 512 << 10

var ErrStreamAborted = errors.New("识别已被中止")

// StreamChat 以 OpenAI 兼容的 /chat/completions 流式接口发起一次调用，
// 每收到一个片段就回调 emit。emit 返回 error 表示对端已断开，立刻停止读取。
//
// payload 由调用方组装（视觉要带 image_url，文本要带 response_format），
// 这里只负责 stream=true、SSE 解析、推理字段识别和超时。
func StreamChat(cfg *model.AIConfig, payload map[string]interface{}, emit func(StreamEvent) error) (*StreamOutcome, error) {
	if !IsConfigured(cfg) {
		return nil, ErrNotConfigured
	}

	body := make(map[string]interface{}, len(payload)+1)
	for k, v := range payload {
		body[k] = v
	}
	body["stream"] = true

	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, ResolveChatEndpoint(cfg.Endpoint), bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: streamClientTimeout}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		detail := firstLineOf(snippet)
		if detail == "" {
			detail = "上游无报错正文"
		}
		emitErr := emit(StreamEvent{Type: EventTypeError, Text: fmt.Sprintf("HTTP %d：%s", resp.StatusCode, detail)})
		if emitErr != nil {
			return nil, emitErr
		}
		return nil, fmt.Errorf("上游返回 HTTP %d：%s", resp.StatusCode, detail)
	}

	out := &StreamOutcome{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxStreamLineBytes)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// SSE 里注释行与空行是心跳/分隔，忽略
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		out.Chunks++
		reasoning, content, errMsg, perr := parseStreamChunk([]byte(data))
		if perr != nil {
			// 解析不动的片段说明对端不是 SSE 或吐了杂质：如实报出来，不要静默跳过当成成功
			_ = emit(StreamEvent{Type: EventTypeError, Text: fmt.Sprintf("无法解析上游片段：%s", firstLineOf([]byte(data)))})
			continue
		}
		if errMsg != "" {
			_ = emit(StreamEvent{Type: EventTypeError, Text: errMsg})
			return out, fmt.Errorf("上游报错：%s", errMsg)
		}
		if reasoning != "" {
			out.ReasoningSeen = true
			out.Reasoning += reasoning
			if err := emit(StreamEvent{Type: EventTypeReasoning, Text: reasoning}); err != nil {
				return out, err
			}
		}
		if content != "" {
			out.Content += content
			if err := emit(StreamEvent{Type: EventTypeContent, Text: content}); err != nil {
				return out, err
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("读取上游流失败：%w", err)
	}
	out.ElapsedMS = time.Since(started).Milliseconds()
	if out.Content == "" && out.Reasoning == "" {
		return out, fmt.Errorf("上游未返回任何内容（stream 是否被支持？）")
	}
	return out, nil
}

// StreamVisionWith 视觉阶段的可流式版本。图片归一化沿用 client.go 的本地内联逻辑，
// 否则浏览器给的相对路径 /uploads/xxx 上游根本取不到。
func StreamVisionWith(cfg *model.AIConfig, imageURL, instruction string, emit func(StreamEvent) error) (*StreamOutcome, error) {
	if !IsConfigured(cfg) {
		return nil, ErrNotConfigured
	}
	normalized, err := normalizeVisionImage(imageURL)
	if err != nil {
		return nil, err
	}
	payload := map[string]interface{}{
		"model": cfg.ModelName,
		"messages": []map[string]interface{}{
			{"role": "system", "content": cfg.SystemPrompt},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": instruction},
					{"type": "image_url", "image_url": map[string]string{"url": normalized}},
				},
			},
		},
		"temperature": cfg.Temperature,
		"max_tokens":  cfg.MaxTokens,
	}
	return StreamChat(cfg, payload, emit)
}

// StreamTextWith 文本归纳阶段的可流式版本。JSON 仍要求整包输出，
// 推理过程实时给宿管看，最后再由调用方解析正文。
func StreamTextWith(cfg *model.AIConfig, instruction, rawVisionText string, emit func(StreamEvent) error) (*StreamOutcome, error) {
	if !IsConfigured(cfg) {
		return nil, ErrNotConfigured
	}
	payload := map[string]interface{}{
		"model": cfg.ModelName,
		"messages": []map[string]interface{}{
			{"role": "system", "content": instruction},
			{"role": "user", "content": rawVisionText},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     cfg.Temperature,
		"max_tokens":      cfg.MaxTokens,
	}
	return StreamChat(cfg, payload, emit)
}

// parseStreamChunk 解析一条 SSE data。三个厂商的推理字段写法不统一：
// DeepSeek/Qwen/vLLM 用 reasoning_content，OpenRouter 一类用 reasoning，这里都认。
func parseStreamChunk(data []byte) (reasoning, content, errMsg string, err error) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
				Reasoning        *string `json:"reasoning"`
			} `json:"delta"`
			Message *struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if e := json.Unmarshal(data, &chunk); e != nil {
		return "", "", "", e
	}
	if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
		return "", "", chunk.Error.Message, nil
	}
	for _, ch := range chunk.Choices {
		if ch.Delta.ReasoningContent != nil {
			reasoning += *ch.Delta.ReasoningContent
		}
		if ch.Delta.Reasoning != nil {
			reasoning += *ch.Delta.Reasoning
		}
		if ch.Delta.Content != nil {
			content += *ch.Delta.Content
		}
		// 少数端点不走 delta，最后一帧整包给 message
		if ch.Message != nil {
			if ch.Message.ReasoningContent != nil && reasoning == "" && content == "" {
				reasoning += *ch.Message.ReasoningContent
			}
			if ch.Message.Content != nil && content == "" {
				content += *ch.Message.Content
			}
		}
	}
	return reasoning, content, "", nil
}
