package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// geminiAdapter 调用 Gemini generateContent 原生接口。
type geminiAdapter struct {
	baseURL   string
	transport *jsonTransport
}

// geminiRequest 将系统指令与普通 contents 分开，并使用 Gemini 的生成参数命名。
type geminiRequest struct {
	Contents          []geminiContent        `json:"contents"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

func newGeminiAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	if _, err := parseBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &geminiAdapter{baseURL: baseURL, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *geminiAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 将 assistant 角色映射为 model，并把模型名写入 generateContent 端点。
func (adapter *geminiAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	request, err := decodeNativeChatRequest(input)
	if err != nil {
		return nil, err
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, err
	}
	upstream := geminiRequest{
		GenerationConfig: geminiGenerationConfig{
			Temperature:   request.Temperature,
			TopP:          request.TopP,
			StopSequences: stopSequences,
		},
	}
	if maxTokens := requestedMaxTokens(request, 0); maxTokens > 0 {
		upstream.GenerationConfig.MaxOutputTokens = &maxTokens
	}
	var systemParts []geminiPart
	for _, message := range request.Messages {
		if message.Role == "system" {
			content, err := textContent(message.Content)
			if err != nil {
				return nil, err
			}
			systemParts = append(systemParts, geminiPart{Text: content})
			continue
		}
		parts, err := geminiParts(message.Content)
		if err != nil {
			return nil, err
		}
		role := message.Role
		if role == "assistant" {
			role = "model"
		}
		upstream.Contents = append(upstream.Contents, geminiContent{Role: role, Parts: parts})
	}
	if len(systemParts) > 0 {
		upstream.SystemInstruction = &geminiContent{Parts: systemParts}
	}
	if len(upstream.Contents) == 0 {
		return nil, ErrInvalidRequest
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	endpoint, err := modelEndpoint(adapter.baseURL, "models", request.Model, ":generateContent")
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("x-goog-api-key", strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.post(ctx, endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeGeminiResponse(responseBody, request.Model, input.RequestID)
}

// geminiParts 将内嵌图片转换为 inlineData；远程 URL 在公共内容层被拒绝。
func geminiParts(raw json.RawMessage) ([]geminiPart, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return nil, err
	}
	result := make([]geminiPart, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" {
			result = append(result, geminiPart{Text: part.Text})
			continue
		}
		mediaType, data, err := base64Image(part.ImageURL)
		if err != nil {
			return nil, err
		}
		result = append(result, geminiPart{InlineData: &geminiInlineData{MIMEType: mediaType, Data: data}})
	}
	return result, nil
}

// decodeGeminiResponse 合并文本 part，并将安全拦截统一映射为 content_filter。
func decodeGeminiResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		ResponseID string `json:"responseId"`
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Usage *struct {
			PromptTokens     *int `json:"promptTokenCount"`
			CompletionTokens *int `json:"candidatesTokenCount"`
			TotalTokens      *int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Candidates) == 0 {
		return nil, ErrInvalidResponse
	}
	var text strings.Builder
	for _, rawPart := range response.Candidates[0].Content.Parts {
		var part struct {
			Text         *string         `json:"text"`
			FunctionCall json.RawMessage `json:"functionCall"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return nil, ErrInvalidResponse
		}
		functionCall := bytes.TrimSpace(part.FunctionCall)
		if len(functionCall) > 0 && !bytes.Equal(functionCall, []byte("null")) {
			return nil, fmt.Errorf("%w: Gemini function call output", ErrUnsupportedResponse)
		}
		if part.Text == nil {
			return nil, ErrInvalidResponse
		}
		text.WriteString(*part.Text)
	}
	if len(response.Candidates[0].Content.Parts) == 0 {
		return nil, ErrInvalidResponse
	}
	finishReason := "stop"
	switch response.Candidates[0].FinishReason {
	case "MAX_TOKENS":
		finishReason = "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		finishReason = "content_filter"
	}
	var usage *completionUsage
	if response.Usage != nil {
		usage = newCompletionUsage(response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens)
	}
	return encodeCompletion(response.ResponseID, requestID, model, text.String(), finishReason, usage)
}
