package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ollamaAdapter 调用本地 Ollama Chat API，不要求 Provider Key。
type ollamaAdapter struct {
	endpoint  string
	transport *jsonTransport
}

// ollamaRequest 使用 options 承载采样参数，图片则挂在对应消息上。
type ollamaRequest struct {
	Model    string                 `json:"model"`
	Messages []ollamaMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

func newOllamaAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	endpoint, err := fixedEndpoint(baseURL, "api/chat")
	if err != nil {
		return nil, err
	}
	return &ollamaAdapter{endpoint: endpoint, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *ollamaAdapter) Authentication() Authentication {
	return AuthenticationNone
}

// Complete 发送非流式 Ollama 请求；传入的 apiKey 在该协议下没有意义。
func (adapter *ollamaAdapter) Complete(ctx context.Context, input ChatInput, _ string) ([]byte, error) {
	request, err := decodeNativeChatRequest(input)
	if err != nil {
		return nil, err
	}
	upstream := ollamaRequest{Model: request.Model}
	for _, message := range request.Messages {
		converted, err := ollamaChatMessage(message)
		if err != nil {
			return nil, err
		}
		upstream.Messages = append(upstream.Messages, converted)
	}
	options, err := ollamaOptions(request)
	if err != nil {
		return nil, err
	}
	upstream.Options = options
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, nil)
	if err != nil {
		return nil, err
	}
	return decodeOllamaResponse(responseBody, request.Model, input.RequestID)
}

// ollamaChatMessage 将文本合并到 content，将图片 data URL 提取为 Base64 数组。
func ollamaChatMessage(message ChatMessage) (ollamaMessage, error) {
	parts, err := decodeNativeContent(message.Content)
	if err != nil {
		return ollamaMessage{}, err
	}
	converted := ollamaMessage{Role: message.Role}
	var text strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			text.WriteString(part.Text)
			continue
		}
		_, data, err := base64Image(part.ImageURL)
		if err != nil {
			return ollamaMessage{}, err
		}
		converted.Images = append(converted.Images, data)
	}
	converted.Content = text.String()
	return converted, nil
}

// ollamaOptions 只写入客户端显式设置的参数，空配置保持省略。
func ollamaOptions(request ChatRequest) (map[string]interface{}, error) {
	options := make(map[string]interface{})
	if request.Temperature != nil {
		options["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		options["top_p"] = *request.TopP
	}
	if maxTokens := requestedMaxTokens(request, 0); maxTokens > 0 {
		options["num_predict"] = maxTokens
	}
	stop, err := decodeStop(request.Stop)
	if err != nil {
		return nil, err
	}
	if len(stop) > 0 {
		options["stop"] = stop
	}
	if len(options) == 0 {
		return nil, nil
	}
	return options, nil
}

// decodeOllamaResponse 要求 done=true，避免把未完成的流式片段误当完整结果。
func decodeOllamaResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		Model      string `json:"model"`
		Done       bool   `json:"done"`
		DoneReason string `json:"done_reason"`
		Message    struct {
			Content   string          `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		PromptTokens     *int `json:"prompt_eval_count"`
		CompletionTokens *int `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !response.Done {
		return nil, ErrInvalidResponse
	}
	toolCalls := bytes.TrimSpace(response.Message.ToolCalls)
	if len(toolCalls) > 0 && !bytes.Equal(toolCalls, []byte("null")) {
		return nil, fmt.Errorf("%w: Ollama tool output", ErrUnsupportedResponse)
	}
	if response.Model != "" {
		model = response.Model
	}
	finishReason := response.DoneReason
	if finishReason == "" {
		finishReason = "stop"
	}
	var usage *completionUsage
	if response.PromptTokens != nil || response.CompletionTokens != nil {
		usage = &completionUsage{}
		if response.PromptTokens != nil {
			usage.PromptTokens = *response.PromptTokens
		}
		if response.CompletionTokens != nil {
			usage.CompletionTokens = *response.CompletionTokens
		}
	}
	return encodeCompletion("", requestID, model, response.Message.Content, finishReason, usage)
}
