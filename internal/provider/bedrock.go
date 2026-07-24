package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// bedrockAdapter 调用 Bedrock Converse API，模型名属于请求路径而不是请求体。
type bedrockAdapter struct {
	baseURL   string
	transport *jsonTransport
}

// bedrockRequest 对应 Converse API；system 与普通 messages 分开编码。
type bedrockRequest struct {
	System          []bedrockContent       `json:"system,omitempty"`
	Messages        []bedrockMessage       `json:"messages"`
	InferenceConfig bedrockInferenceConfig `json:"inferenceConfig,omitempty"`
}

type bedrockMessage struct {
	Role    string           `json:"role"`
	Content []bedrockContent `json:"content"`
}

type bedrockContent struct {
	Text  string        `json:"text,omitempty"`
	Image *bedrockImage `json:"image,omitempty"`
}

type bedrockImage struct {
	Format string             `json:"format"`
	Source bedrockImageSource `json:"source"`
}

type bedrockImageSource struct {
	Bytes string `json:"bytes"`
}

type bedrockInferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

func newBedrockAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	if _, err := parseBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &bedrockAdapter{baseURL: baseURL, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *bedrockAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 构造每个模型独立的 Converse 端点，并执行一次非流式调用。
func (adapter *bedrockAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	request, err := decodeNativeChatRequest(input)
	if err != nil {
		return nil, err
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, err
	}
	upstream := bedrockRequest{
		InferenceConfig: bedrockInferenceConfig{
			MaxTokens:     requestedMaxTokens(request, 0),
			Temperature:   request.Temperature,
			TopP:          request.TopP,
			StopSequences: stopSequences,
		},
	}
	for _, message := range request.Messages {
		if message.Role == "system" {
			content, err := textContent(message.Content)
			if err != nil {
				return nil, err
			}
			upstream.System = append(upstream.System, bedrockContent{Text: content})
			continue
		}
		content, err := bedrockContents(message.Content)
		if err != nil {
			return nil, err
		}
		upstream.Messages = append(upstream.Messages, bedrockMessage{Role: message.Role, Content: content})
	}
	if len(upstream.Messages) == 0 {
		return nil, ErrInvalidRequest
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	endpoint, err := bedrockConverseEndpoint(adapter.baseURL, request.Model)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.post(ctx, endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeBedrockResponse(responseBody, request.Model, input.RequestID)
}

// bedrockContents 将图片 data URL 转为 Bedrock 要求的格式和 Base64 字节。
// 远程图片 URL 不会由网关代下载，避免额外的网络与安全边界。
func bedrockContents(raw json.RawMessage) ([]bedrockContent, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return nil, err
	}
	content := make([]bedrockContent, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" {
			content = append(content, bedrockContent{Text: part.Text})
			continue
		}
		mediaType, data, err := base64Image(part.ImageURL)
		if err != nil {
			return nil, err
		}
		format := strings.TrimPrefix(mediaType, "image/")
		if format == "jpg" {
			format = "jpeg"
		}
		switch format {
		case "png", "jpeg", "gif", "webp":
		default:
			return nil, fmt.Errorf("%w: image format %q", ErrUnsupportedCapability, format)
		}
		content = append(content, bedrockContent{Image: &bedrockImage{
			Format: format,
			Source: bedrockImageSource{Bytes: data},
		}})
	}
	return content, nil
}

// bedrockConverseEndpoint 拒绝可能改变 URL 层级的模型名。
func bedrockConverseEndpoint(rawBaseURL, model string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, "/?#") {
		return "", ErrInvalidRequest
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/model/" + model + "/converse"
	parsed.RawPath = ""
	return parsed.String(), nil
}

// decodeBedrockResponse 合并文本块，并把 Bedrock stopReason 映射为 OpenAI finish_reason。
func decodeBedrockResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		Output struct {
			Message struct {
				Content []json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"output"`
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens           *int `json:"inputTokens"`
			OutputTokens          *int `json:"outputTokens"`
			TotalTokens           *int `json:"totalTokens"`
			CacheReadInputTokens  *int `json:"cacheReadInputTokens"`
			CacheWriteInputTokens *int `json:"cacheWriteInputTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Output.Message.Content) == 0 {
		return nil, ErrInvalidResponse
	}
	var content strings.Builder
	for _, rawBlock := range response.Output.Message.Content {
		var block struct {
			Text    *string         `json:"text"`
			ToolUse json.RawMessage `json:"toolUse"`
		}
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return nil, ErrInvalidResponse
		}
		if toolUse := bytes.TrimSpace(block.ToolUse); len(toolUse) > 0 && !bytes.Equal(toolUse, []byte("null")) {
			return nil, fmt.Errorf("%w: Bedrock tool output", ErrUnsupportedResponse)
		}
		if block.Text == nil {
			return nil, ErrInvalidResponse
		}
		content.WriteString(*block.Text)
	}
	finishReason := response.StopReason
	switch finishReason {
	case "end_turn", "stop_sequence", "":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "guardrail_intervened", "content_filtered":
		finishReason = "content_filter"
	case "tool_use":
		return nil, fmt.Errorf("%w: Bedrock tool output", ErrUnsupportedResponse)
	}
	var usage *completionUsage
	if response.Usage != nil {
		inputTokens, valid := inclusiveInputTokens(
			response.Usage.InputTokens,
			response.Usage.CacheReadInputTokens,
			response.Usage.CacheWriteInputTokens,
		)
		if !valid {
			return nil, ErrInvalidResponse
		}
		usage = newCompletionUsage(inputTokens, response.Usage.OutputTokens, nil)
		usage.setInputDetails(
			nil,
			nil,
			response.Usage.CacheReadInputTokens,
			response.Usage.CacheWriteInputTokens,
		)
	}
	return encodeCompletion("", requestID, model, content.String(), finishReason, usage)
}
