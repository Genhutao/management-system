package ai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"xgh-system/internal/model"
)

// ErrNotConfigured 表示引擎尚未配置可用端点，与"配置了但调用失败"必须区分开：
// 前者是运维状态，后者是需要暴露给技术组的故障。
var ErrNotConfigured = errors.New("AI 引擎未配置：缺少有效的 Endpoint 或 API Key")

const (
	StatusReal     = "real"     // 两阶段均调用真实模型成功，结论可用于核对打表
	StatusDisabled = "disabled" // 引擎未配置，系统未做任何识别
	StatusFailed   = "failed"   // 引擎已配置但调用失败
	StatusUnknown  = "unknown"  // 历史数据未记录识别状态，一律按不可信处理
)

// VisionResult 多模态视觉初筛结果
type VisionResult struct {
	SceneDescription string   `json:"scene_description"`
	IdentifiedItems  []string `json:"identified_items"`
	RawAnalysis      string   `json:"raw_analysis"`
	HasHazard        bool     `json:"has_hazard"`
}

// StructuredDeductResult 文本 AI 结构化归纳入库标准输出
type StructuredDeductResult struct {
	Category     string   `json:"category"`      // 如: 违规大功率电器, 私拉乱接电线, 宿舍卫生严重脏乱, 上工履职规范
	Severity     string   `json:"severity"`      // low, medium, high, critical
	DeductPoints int      `json:"deduct_points"` // 建议扣分
	Summary      string   `json:"summary"`       // 规范化归档摘要
	Tags         []string `json:"tags"`          // 标签
	ActionAdvice string   `json:"action_advice"` // 给部员和宿管的处置意见
}

// IsConfigured 判断一份 AI 配置是否足以发起真实调用
func IsConfigured(cfg *model.AIConfig) bool {
	return cfg != nil && cfg.IsEnabled &&
		strings.TrimSpace(cfg.Endpoint) != "" &&
		strings.TrimSpace(cfg.APIKey) != "" &&
		!strings.Contains(cfg.APIKey, "placeholder")
}

// ProcessMultimodalAndText 执行两阶段 AI 流水线：多模态识别翻译 -> 文本结构化归纳入库。
// status 非 StatusReal 时 structured 恒为 nil，调用方不得将任何结论写入打表队列。
func ProcessMultimodalAndText(imageURL string, photoType string, roomNumber string, visionCfg, textCfg *model.AIConfig) (string, *StructuredDeductResult, string, error) {
	rawVisionOutput, err := CallVisionAI(imageURL, photoType, roomNumber, visionCfg)
	if err != nil {
		return "", nil, statusForError(err), err
	}

	structuredResult, err := CallTextAI(rawVisionOutput, photoType, textCfg)
	if err != nil {
		return rawVisionOutput, nil, statusForError(err), err
	}

	return rawVisionOutput, structuredResult, StatusReal, nil
}

func statusForError(err error) string {
	if errors.Is(err, ErrNotConfigured) {
		return StatusDisabled
	}
	return StatusFailed
}

// ChatMessage 一轮对话消息，供透传终端与对话排班共用。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// maxUpstreamBodyBytes 读取上游响应体的上限；异常端点返回超大报文时不允许把内存吃光。
const maxUpstreamBodyBytes = 1 << 20

// ResolveChatEndpoint 把网关里填的上游地址归一成 chat/completions 完整地址。
// 兼容三种写法：已经填到 /chat/completions、填到 /v1、以及只填站点根。
func ResolveChatEndpoint(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case base == "":
		return ""
	case strings.HasSuffix(base, "/chat/completions"):
		return base
	case strings.HasSuffix(base, "/v1"):
		return base + "/chat/completions"
	default:
		return base + "/v1/chat/completions"
	}
}

// ChatCompletion 直连任意 OpenAI 兼容端点发起一次真实对话补全。
// 与两阶段流水线不同，这里不存在任何"兜底应答"：未配置、上游非 200、解析不出正文、
// 正文为空一律返回错误。调用方据此如实上报，并且在失败时不得扣减积分额度——
// 上一版实现正是拿调用者自己的提问拼了一段假回复还照样扣次数。
func ChatCompletion(endpoint, apiKey, model string, messages []ChatMessage, temperature float64, maxTokens int) (string, error) {
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(apiKey) == "" {
		return "", ErrNotConfigured
	}

	payload := map[string]interface{}{
		"model":    model,
		"messages": messages,
	}
	if temperature > 0 {
		payload["temperature"] = temperature
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("对话请求构造失败: %w", err)
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("对话请求构造失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("上游模型连接失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("上游模型返回 %d：%s", resp.StatusCode, firstLineOf(bodyBytes))
	}

	var respObj struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &respObj); err != nil {
		return "", fmt.Errorf("上游模型报文解析失败：%s", firstLineOf(bodyBytes))
	}
	if respObj.Error.Message != "" {
		return "", fmt.Errorf("上游模型报错：%s", firstLineOf([]byte(respObj.Error.Message)))
	}
	if len(respObj.Choices) == 0 {
		return "", fmt.Errorf("上游模型未返回任何候选内容")
	}
	content := strings.TrimSpace(respObj.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("上游模型返回空内容")
	}
	return content, nil
}

// firstLineOf 只取上游报文首行并截断，避免把整页 HTML 错误页塞进接口响应。
func firstLineOf(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	// 上游错误页常见中文，按字节截断会砍出半个字
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	if s == "" {
		return "无详细原因"
	}
	return s
}

// CallVisionAI 调用多模态 AI（兼容 OpenAI 格式的任意端点），未配置或调用失败均返回错误
func CallVisionAI(imageURL, photoType, roomNumber string, cfg *model.AIConfig) (string, error) {
	if !IsConfigured(cfg) {
		return "", ErrNotConfigured
	}

	instruction := fmt.Sprintf("请分析这张宿管上传的巡查图片（类型：%s）。请详细提取翻译识别出的安全隐患或上工情况。", photoType)
	output, err := requestOpenAIVision(cfg, imageURL, instruction)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("多模态模型返回空内容")
	}
	return output, nil
}

// CallTextAI 调用文本 AI 进行规范化提取与 JSON 归类，未配置或调用失败均返回错误
func CallTextAI(rawVisionText, photoType string, cfg *model.AIConfig) (*StructuredDeductResult, error) {
	if !IsConfigured(cfg) {
		return nil, ErrNotConfigured
	}
	return requestOpenAIText(cfg, rawVisionText)
}

// maxInlineImageBytes 直传给视觉模型的图片上限（4MB）。
const maxInlineImageBytes = 4 << 20

// normalizeVisionImage 收口送入视觉模型的图片来源：
// 本地 /uploads 路径读盘转 base64；浏览器直接提交的 data:image URL 限 4MB（对话排班传的就是这种）；
// 其余仅放行 http/https，避免把"任意字符串"当图片交给上游。
func normalizeVisionImage(imageURL string) (string, error) {
	trimmed := strings.TrimSpace(imageURL)
	switch {
	case trimmed == "":
		return "", fmt.Errorf("图片地址为空")
	case strings.HasPrefix(trimmed, "/"):
		return inlineLocalImage(trimmed)
	case strings.HasPrefix(trimmed, "data:image/"):
		if len(trimmed) > maxInlineImageBytes {
			return "", fmt.Errorf("图片 data URL 长度 %d 超过上限 4MB，无法送入视觉模型", len(trimmed))
		}
		return trimmed, nil
	case strings.HasPrefix(trimmed, "http://"), strings.HasPrefix(trimmed, "https://"):
		return trimmed, nil
	default:
		return "", fmt.Errorf("图片地址仅支持服务器本地上传件、data:image 或 http(s) 链接")
	}
}

// inlineLocalImage 把 /uploads/... 这类服务器本地路径读出并转为 base64 data URL。
// 宿管上传的图只存在本机，转 base64 直传后视觉识别不再依赖"图片必须公网可达"。
func inlineLocalImage(imageURL string) (string, error) {
	localPath := strings.TrimPrefix(imageURL, "/")
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("读取本地上传图片失败: %w", err)
	}
	if len(data) > maxInlineImageBytes {
		return "", fmt.Errorf("图片体积 %d MB 超过上限 4MB，无法送入视觉模型", len(data)>>20)
	}
	mime := "image/jpeg"
	switch {
	case strings.HasSuffix(strings.ToLower(localPath), ".png"):
		mime = "image/png"
	case strings.HasSuffix(strings.ToLower(localPath), ".webp"):
		mime = "image/webp"
	case strings.HasSuffix(strings.ToLower(localPath), ".gif"):
		mime = "image/gif"
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)), nil
}

// CallVisionDescription 按调用方给定的指令读图（例如把课程表/请假便签转写成文字）。
// 与 CallVisionAI 的差别只在提示词，同样要求引擎已配置，且拿不到结论时报错而不是编造。
func CallVisionDescription(cfg *model.AIConfig, imageURL, instruction string) (string, error) {
	if !IsConfigured(cfg) {
		return "", ErrNotConfigured
	}
	out, err := requestOpenAIVision(cfg, imageURL, instruction)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("视觉模型返回空内容")
	}
	return out, nil
}

// requestOpenAIVision 发送多模态请求给任何兼容 OpenAI 格式的端点 (OpenAI, Qwen-VL, Ollama, 智谱等)。
// instruction 既可以是宿管巡查那句固定提示词，也可以是调用方自建的读图指令。
func requestOpenAIVision(cfg *model.AIConfig, imageURL, instruction string) (string, error) {
	normalized, err := normalizeVisionImage(imageURL)
	if err != nil {
		return "", err
	}
	imageURL = normalized

	client := &http.Client{Timeout: 90 * time.Second}
	payload := map[string]interface{}{
		"model": cfg.ModelName,
		"messages": []map[string]interface{}{
			{"role": "system", "content": cfg.SystemPrompt},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": instruction},
					{"type": "image_url", "image_url": map[string]string{"url": imageURL}},
				},
			},
		},
		"temperature": cfg.Temperature,
		"max_tokens":  cfg.MaxTokens,
	}

	jsonData, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", cfg.Endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var respObj struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(bodyBytes, &respObj); err != nil {
		return "", err
	}
	if len(respObj.Choices) > 0 {
		return respObj.Choices[0].Message.Content, nil
	}
	if respObj.Error.Message != "" {
		return "", fmt.Errorf("API Error: %s", respObj.Error.Message)
	}
	return "", fmt.Errorf("empty response")
}

// requestOpenAIText 发送文本结构化请求
func requestOpenAIText(cfg *model.AIConfig, textToStructure string) (*StructuredDeductResult, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	payload := map[string]interface{}{
		"model": cfg.ModelName,
		"messages": []map[string]interface{}{
			{"role": "system", "content": cfg.SystemPrompt + "\n必须只输出JSON格式：{\"category\":\"...\",\"severity\":\"...\",\"deduct_points\":0,\"summary\":\"...\",\"action_advice\":\"...\"}"},
			{"role": "user", "content": "请将如下多模态初步识别信息归纳入库并生成结构化数据：\n" + textToStructure},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     cfg.Temperature,
		"max_tokens":      cfg.MaxTokens,
	}

	jsonData, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", cfg.Endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var respObj struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bodyBytes, &respObj); err != nil {
		return nil, err
	}
	if len(respObj.Choices) > 0 {
		var result StructuredDeductResult
		if err := json.Unmarshal([]byte(respObj.Choices[0].Message.Content), &result); err == nil {
			return &result, nil
		}
	}
	return nil, fmt.Errorf("failed to parse structured result")
}

