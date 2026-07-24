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
	Model       string                 `json:"model"`
	Messages    []ollamaMessage        `json:"messages"`
	Stream      bool                   `json:"stream"`
	Options     map[string]interface{} `json:"options,omitempty"`
	Tools       []ollamaTool           `json:"tools,omitempty"`
	Format      json.RawMessage        `json:"format,omitempty"`
	Logprobs    *bool                  `json:"logprobs,omitempty"`
	TopLogprobs *int                   `json:"top_logprobs,omitempty"`
	Think       interface{}            `json:"think,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
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
	body, request, err := ollamaRequestBody(input, false)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, nil)
	if err != nil {
		return nil, err
	}
	return decodeOllamaResponse(responseBody, request.Model, input.RequestID)
}

func ollamaRequestBody(input ChatInput, stream bool) ([]byte, ChatRequest, error) {
	request, err := decodeNativeChatRequest(input, nativeRequestSupport{
		tools: true, structured: true, seed: true, penalties: true,
		reasoning: true, developer: true, toolName: true,
	})
	if err != nil {
		return nil, ChatRequest{}, err
	}
	upstream := ollamaRequest{
		Model: request.Model, Logprobs: request.Logprobs, TopLogprobs: request.TopLogprobs,
		Stream: stream,
	}
	if err := applyOllamaFeatures(&upstream, request); err != nil {
		return nil, ChatRequest{}, err
	}
	for index, message := range request.Messages {
		if message.Role == "tool" && message.Name == "" {
			message.Name = priorToolName(request.Messages, index, message.ToolCallID)
		}
		converted, err := ollamaChatMessage(message)
		if err != nil {
			return nil, ChatRequest{}, err
		}
		upstream.Messages = append(upstream.Messages, converted)
	}
	options, err := ollamaOptions(request)
	if err != nil {
		return nil, ChatRequest{}, err
	}
	upstream.Options = options
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	return body, request, nil
}

func (adapter *ollamaAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	_ string,
) (*ChatEventStream, error) {
	body, request, err := ollamaRequestBody(input, true)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.postStreamTypes(
		ctx, adapter.endpoint, input.RequestID, body, nil,
		"application/x-ndjson", "application/json",
	)
	if err != nil {
		return nil, err
	}
	return newMappedJSONLinesStream(
		responseBody, newOllamaStreamMapper(request.Model, input.RequestID),
	)
}

func applyOllamaFeatures(upstream *ollamaRequest, request ChatRequest) error {
	if valuePresent(request.ToolChoice) {
		return unsupportedRequestField("tool_choice")
	}
	if len(request.Tools) > 0 && request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		return unsupportedRequestField("parallel_tool_calls")
	}
	for _, tool := range request.Tools {
		upstream.Tools = append(upstream.Tools, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name: tool.Function.Name, Description: tool.Function.Description,
				Parameters: tool.Function.Parameters,
			},
		})
	}
	format, err := decodeResponseFormat(request.ResponseFormat)
	if err != nil {
		return err
	}
	switch format.Type {
	case "", "text":
	case "json_object":
		upstream.Format = json.RawMessage(`"json"`)
	case "json_schema":
		upstream.Format = format.Schema
	}
	effort := strings.ToLower(strings.TrimSpace(request.ReasoningEffort))
	switch effort {
	case "", "none":
	case "low", "medium", "high":
		upstream.Think = effort
	default:
		return unsupportedRequestField("reasoning_effort")
	}
	return nil
}

// ollamaChatMessage 将文本合并到 content，将图片 data URL 提取为 Base64 数组。
func ollamaChatMessage(message ChatMessage) (ollamaMessage, error) {
	var parts []chatContentPart
	converted := ollamaMessage{Role: message.Role}
	if message.Role == "developer" {
		converted.Role = "system"
	}
	if message.Role == "tool" {
		converted.ToolName = message.Name
	}
	var text strings.Builder
	raw := bytes.TrimSpace(message.Content)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var err error
		parts, err = decodeNativeContent(message.Content)
		if err != nil {
			return ollamaMessage{}, err
		}
	}
	for _, part := range parts {
		if part.Type == "text" {
			text.WriteString(part.Text)
			continue
		}
		if part.Type != "image_url" {
			return ollamaMessage{}, fmt.Errorf("%w: Ollama %s content", ErrUnsupportedCapability, part.Type)
		}
		if err := rejectNativeImageDetail(part, ProtocolOllama); err != nil {
			return ollamaMessage{}, err
		}
		_, data, err := base64Image(part.ImageURL)
		if err != nil {
			return ollamaMessage{}, err
		}
		converted.Images = append(converted.Images, data)
	}
	for _, call := range message.ToolCalls {
		arguments, err := toolArgumentsObject(call.Function.Arguments)
		if err != nil {
			return ollamaMessage{}, ErrInvalidRequest
		}
		converted.ToolCalls = append(converted.ToolCalls, ollamaToolCall{
			Function: ollamaFunctionCall{Name: call.Function.Name, Arguments: arguments},
		})
	}
	if text.Len() == 0 && len(converted.Images) == 0 && len(converted.ToolCalls) == 0 {
		return ollamaMessage{}, ErrInvalidRequest
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
	if request.Seed != nil {
		options["seed"] = *request.Seed
	}
	if request.FrequencyPenalty != nil {
		options["frequency_penalty"] = *request.FrequencyPenalty
	}
	if request.PresencePenalty != nil {
		options["presence_penalty"] = *request.PresencePenalty
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
			Content   string           `json:"content"`
			Thinking  string           `json:"thinking"`
			ToolCalls []ollamaToolCall `json:"tool_calls"`
		} `json:"message"`
		PromptTokens     *int `json:"prompt_eval_count"`
		CompletionTokens *int `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !response.Done {
		return nil, ErrInvalidResponse
	}
	toolCalls := make([]ChatToolCall, 0, len(response.Message.ToolCalls))
	for index, call := range response.Message.ToolCalls {
		if call.Function.Name == "" || !jsonObject(call.Function.Arguments) {
			return nil, ErrInvalidResponse
		}
		toolCalls = append(toolCalls, ChatToolCall{
			ID: fmt.Sprintf("call_%d", index), Type: "function",
			Function: ChatFunctionCall{
				Name: call.Function.Name, Arguments: string(call.Function.Arguments),
			},
		})
	}
	if response.Model != "" {
		model = response.Model
	}
	finishReason := response.DoneReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
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
	content := response.Message.Content
	message := completionMessage{Role: "assistant", ToolCalls: toolCalls}
	if response.Message.Thinking != "" {
		message.ReasoningContent = &response.Message.Thinking
	}
	if content != "" || len(toolCalls) == 0 {
		message.Content = &content
	}
	return encodeCompletionMessage("", requestID, model, message, finishReason, usage)
}

func newOllamaStreamMapper(model, requestID string) nativeStreamMapper {
	id := "chatcmpl-" + requestID
	nextToolIndex := 0
	return func(_ string, data []byte) (nativeStreamResult, error) {
		var response struct {
			Model      string `json:"model"`
			Done       bool   `json:"done"`
			DoneReason string `json:"done_reason"`
			Message    struct {
				Content   string           `json:"content"`
				Thinking  string           `json:"thinking"`
				ToolCalls []ollamaToolCall `json:"tool_calls"`
			} `json:"message"`
			PromptTokens     *int `json:"prompt_eval_count"`
			CompletionTokens *int `json:"eval_count"`
		}
		if err := invalidNativeStreamJSON(data, &response); err != nil {
			return nativeStreamResult{}, err
		}
		if response.Model != "" {
			model = response.Model
		}
		var chunks [][]byte
		if response.Message.Content != "" {
			content := response.Message.Content
			chunk, err := encodeStreamChunk(
				id, model, openAIStreamDelta{Content: &content}, "", nil,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if response.Message.Thinking != "" {
			thinking := response.Message.Thinking
			chunk, err := encodeStreamChunk(
				id, model, openAIStreamDelta{ReasoningContent: &thinking}, "", nil,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if len(response.Message.ToolCalls) > 0 {
			deltas := make([]openAIStreamToolCallDelta, 0, len(response.Message.ToolCalls))
			for _, call := range response.Message.ToolCalls {
				if call.Function.Name == "" || !jsonObject(call.Function.Arguments) {
					return nativeStreamResult{}, ErrInvalidStream
				}
				deltas = append(deltas, openAIStreamToolCallDelta{
					Index: nextToolIndex,
					ID:    fmt.Sprintf("call_%d", nextToolIndex),
					Type:  "function",
					Function: openAIStreamFunctionDelta{
						Name: call.Function.Name, Arguments: string(call.Function.Arguments),
					},
				})
				nextToolIndex++
			}
			chunk, err := encodeStreamChunk(
				id, model, openAIStreamDelta{ToolCalls: deltas}, "", nil,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if response.Done {
			finish := response.DoneReason
			if finish == "" {
				finish = "stop"
			}
			if nextToolIndex > 0 {
				finish = "tool_calls"
			}
			usage := newCompletionUsage(
				response.PromptTokens, response.CompletionTokens, nil,
			)
			chunk, err := encodeStreamChunk(
				id, model, openAIStreamDelta{}, finish, usage,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if len(chunks) == 0 {
			return nativeStreamResult{}, ErrInvalidStream
		}
		return nativeStreamResult{Chunks: chunks, Done: response.Done}, nil
	}
}
