package ai

import (
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
	Category      string   `json:"category"`       // 如: 违规大功率电器, 私拉乱接电线, 宿舍卫生严重脏乱, 上工履职规范
	Severity      string   `json:"severity"`       // low, medium, high, critical
	DeductPoints  int      `json:"deduct_points"`  // 建议扣分
	Summary       string   `json:"summary"`        // 规范化归档摘要
	Tags          []string `json:"tags"`           // 标签
	ActionAdvice  string   `json:"action_advice"`  // 给部员和宿管的处置意见
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

// CallVisionAI 调用多模态 AI（兼容 OpenAI 格式的任意端点），未配置或调用失败均返回错误
func CallVisionAI(imageURL, photoType, roomNumber string, cfg *model.AIConfig) (string, error) {
	if !IsConfigured(cfg) {
		return "", ErrNotConfigured
	}

	output, err := requestOpenAIVision(cfg, imageURL, photoType)
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

// requestOpenAIVision 发送多模态请求给任何兼容 OpenAI 格式的端点 (OpenAI, Qwen-VL, Ollama, 智谱等)
func requestOpenAIVision(cfg *model.AIConfig, imageURL, photoType string) (string, error) {
	// 本地相对路径外部模型取不到，若照常请求只会产出一份不可信的"AI 结论"
	if strings.HasPrefix(imageURL, "/") {
		return "", fmt.Errorf("图片地址 %q 是服务器本地路径，外部多模态模型无法访问；需为系统配置公网可访问的图片基础地址后 AI 识别才能真实生效", imageURL)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	payload := map[string]interface{}{
		"model": cfg.ModelName,
		"messages": []map[string]interface{}{
			{"role": "system", "content": cfg.SystemPrompt},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("请分析这张宿管上传的巡查图片（类型：%s）。请详细提取翻译识别出的安全隐患或上工情况。", photoType)},
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
