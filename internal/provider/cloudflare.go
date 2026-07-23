package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// cloudflareAdapter 调用 Workers AI 的按模型运行端点。
type cloudflareAdapter struct {
	baseURL   string
	transport *jsonTransport
}

// cloudflareRequest 使用 Workers AI 当前支持的文本消息格式。
type cloudflareRequest struct {
	Messages    []cohereMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
}

func newCloudflareAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	if _, err := parseBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &cloudflareAdapter{baseURL: baseURL, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *cloudflareAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 将文本消息提交到 /ai/run/{model}，并统一包装 Workers AI 响应。
func (adapter *cloudflareAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	request, err := decodeNativeChatRequest(input)
	if err != nil {
		return nil, err
	}
	upstream := cloudflareRequest{
		MaxTokens:   requestedMaxTokens(request, 0),
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}
	for _, message := range request.Messages {
		content, err := textContent(message.Content)
		if err != nil {
			return nil, err
		}
		upstream.Messages = append(upstream.Messages, cohereMessage{Role: message.Role, Content: content})
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	endpoint, err := cloudflareRunEndpoint(adapter.baseURL, request.Model)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.post(ctx, endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeCloudflareResponse(responseBody, request.Model, input.RequestID)
}

// cloudflareRunEndpoint 允许模型名包含 @cf/... 层级，但拒绝查询字符和路径回退。
func cloudflareRunEndpoint(rawBaseURL, model string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" || strings.ContainsAny(model, "?#") || strings.Contains(model, "..") {
		return "", ErrInvalidRequest
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ai/run/" + model
	parsed.RawPath = ""
	return parsed.String(), nil
}

// decodeCloudflareResponse 要求 success=true，避免把业务错误包装成成功补全。
func decodeCloudflareResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Response string `json:"response"`
			Usage    *struct {
				PromptTokens     *int `json:"prompt_tokens"`
				CompletionTokens *int `json:"completion_tokens"`
				TotalTokens      *int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !response.Success {
		return nil, ErrInvalidResponse
	}
	var usage *completionUsage
	if response.Result.Usage != nil {
		usage = newCompletionUsage(
			response.Result.Usage.PromptTokens,
			response.Result.Usage.CompletionTokens,
			response.Result.Usage.TotalTokens,
		)
	}
	return encodeCompletion("", requestID, model, response.Result.Response, "stop", usage)
}
