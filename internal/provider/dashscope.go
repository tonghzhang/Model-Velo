package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// dashScopeAdapter 使用通义千问原生 text-generation 接口。
type dashScopeAdapter struct {
	endpoint  string
	transport *jsonTransport
}

// dashScopeRequest 把消息放入 input，把生成参数放入 parameters。
type dashScopeRequest struct {
	Model      string              `json:"model"`
	Input      dashScopeInput      `json:"input"`
	Parameters dashScopeParameters `json:"parameters"`
}

type dashScopeInput struct {
	Messages []dashScopeMessage `json:"messages"`
}

type dashScopeMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
}

type dashScopeParameters struct {
	ResultFormat      string          `json:"result_format"`
	MaxTokens         int             `json:"max_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stop              json.RawMessage `json:"stop,omitempty"`
	Tools             []ChatTool      `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    json.RawMessage `json:"response_format,omitempty"`
	Seed              *int64          `json:"seed,omitempty"`
	N                 *int            `json:"n,omitempty"`
	Logprobs          *bool           `json:"logprobs,omitempty"`
	TopLogprobs       *int            `json:"top_logprobs,omitempty"`
	IncrementalOutput bool            `json:"incremental_output,omitempty"`
}

func newDashScopeAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	endpoint, err := fixedEndpoint(baseURL, "services/aigc/text-generation/generation")
	if err != nil {
		return nil, err
	}
	return &dashScopeAdapter{endpoint: endpoint, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *dashScopeAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 强制 result_format=message，使上游返回结构化消息而不是裸文本。
func (adapter *dashScopeAdapter) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	body, request, err := dashScopeRequestBody(input, false)
	if err != nil {
		return nil, err
	}
	headers := dashScopeHeaders(apiKey, false)
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeDashScopeResponse(responseBody, request.Model, input.RequestID)
}

func dashScopeRequestBody(input ChatInput, stream bool) ([]byte, ChatRequest, error) {
	request, err := decodeNativeChatRequest(input, nativeRequestSupport{
		tools: true, structured: true, seed: true,
		developer: true, messageName: true, toolName: true,
	})
	if err != nil {
		return nil, ChatRequest{}, err
	}
	format, err := decodeResponseFormat(request.ResponseFormat)
	if err != nil {
		return nil, ChatRequest{}, err
	}
	if format.Type == "json_schema" {
		return nil, ChatRequest{}, unsupportedRequestField("response_format json_schema")
	}
	choice, err := decodeToolChoice(request.ToolChoice)
	if err != nil {
		return nil, ChatRequest{}, err
	}
	if choice.Mode == "required" {
		return nil, ChatRequest{}, unsupportedRequestField("tool_choice=required")
	}
	if request.Seed != nil && (*request.Seed < 0 || *request.Seed > 1<<31-1) {
		return nil, ChatRequest{}, fmt.Errorf(
			"%w: DashScope seed must be between 0 and 2147483647",
			ErrInvalidRequest,
		)
	}
	upstream := dashScopeRequest{
		Model: request.Model,
		Parameters: dashScopeParameters{
			ResultFormat:      "message",
			MaxTokens:         requestedMaxTokens(request, 0),
			Temperature:       request.Temperature,
			TopP:              request.TopP,
			Stop:              request.Stop,
			Tools:             request.Tools,
			ToolChoice:        request.ToolChoice,
			ParallelToolCalls: request.ParallelToolCalls,
			ResponseFormat:    request.ResponseFormat,
			Seed:              request.Seed,
			N:                 request.N,
			Logprobs:          request.Logprobs,
			TopLogprobs:       request.TopLogprobs,
			IncrementalOutput: stream,
		},
	}
	for _, message := range request.Messages {
		var content string
		rawContent := strings.TrimSpace(string(message.Content))
		if rawContent != "" && rawContent != "null" {
			var err error
			content, err = textContent(message.Content)
			if err != nil {
				return nil, ChatRequest{}, err
			}
		}
		role := message.Role
		if role == "developer" {
			role = "system"
		}
		upstream.Input.Messages = append(upstream.Input.Messages, dashScopeMessage{
			Role: role, Content: content, Name: message.Name,
			ToolCallID: message.ToolCallID, ToolCalls: message.ToolCalls,
		})
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	return body, request, nil
}

func dashScopeHeaders(apiKey string, stream bool) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	if stream {
		headers.Set("X-DashScope-SSE", "enable")
	}
	return headers
}

func (adapter *dashScopeAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) (*ChatEventStream, error) {
	body, request, err := dashScopeRequestBody(input, true)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.postStream(
		ctx, adapter.endpoint, input.RequestID, body,
		dashScopeHeaders(apiKey, true),
	)
	if err != nil {
		return nil, err
	}
	return newMappedSSEStream(
		responseBody, newDashScopeStreamMapper(request.Model, input.RequestID),
	)
}

func decodeDashScopeResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		RequestID string `json:"request_id"`
		Output    struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content          string         `json:"content"`
					ReasoningContent string         `json:"reasoning_content"`
					ToolCalls        []ChatToolCall `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
			TotalTokens  *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Output.Choices) == 0 {
		return nil, ErrInvalidResponse
	}
	choice := response.Output.Choices[0]
	if err := validateResponseToolCalls(choice.Message.ToolCalls); err != nil {
		return nil, err
	}
	finishReason := strings.ToLower(choice.FinishReason)
	if finishReason == "null" || finishReason == "" {
		finishReason = "stop"
	}
	if len(choice.Message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	var usage *completionUsage
	if response.Usage != nil {
		usage = newCompletionUsage(response.Usage.InputTokens, response.Usage.OutputTokens, response.Usage.TotalTokens)
	}
	content := choice.Message.Content
	message := completionMessage{Role: "assistant", ToolCalls: choice.Message.ToolCalls}
	if choice.Message.ReasoningContent != "" {
		message.ReasoningContent = &choice.Message.ReasoningContent
	}
	if content != "" || len(choice.Message.ToolCalls) == 0 {
		message.Content = &content
	}
	return encodeCompletionMessage(response.RequestID, requestID, model, message, finishReason, usage)
}

func newDashScopeStreamMapper(model, requestID string) nativeStreamMapper {
	id := requestID
	return func(_ string, data []byte) (nativeStreamResult, error) {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nativeStreamResult{Done: true}, nil
		}
		var response struct {
			RequestID string `json:"request_id"`
			Output    struct {
				Choices []struct {
					FinishReason string `json:"finish_reason"`
					Message      struct {
						Content          string                      `json:"content"`
						ReasoningContent string                      `json:"reasoning_content"`
						ToolCalls        []openAIStreamToolCallDelta `json:"tool_calls"`
					} `json:"message"`
				} `json:"choices"`
			} `json:"output"`
			Usage *struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
				TotalTokens  *int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := invalidNativeStreamJSON(data, &response); err != nil ||
			len(response.Output.Choices) == 0 {
			return nativeStreamResult{}, ErrInvalidStream
		}
		if response.RequestID != "" {
			id = response.RequestID
		}
		choice := response.Output.Choices[0]
		var chunks [][]byte
		if choice.Message.Content != "" {
			content := choice.Message.Content
			chunk, err := encodeStreamChunk(
				id, model, openAIStreamDelta{Content: &content}, "", nil,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if choice.Message.ReasoningContent != "" {
			reasoning := choice.Message.ReasoningContent
			chunk, err := encodeStreamChunk(
				id, model,
				openAIStreamDelta{ReasoningContent: &reasoning},
				"", nil,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if len(choice.Message.ToolCalls) > 0 {
			chunk, err := encodeStreamChunk(id, model, openAIStreamDelta{
				ToolCalls: choice.Message.ToolCalls,
			}, "", nil)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		finish := strings.ToLower(strings.TrimSpace(choice.FinishReason))
		done := finish != "" && finish != "null"
		if done {
			if finish == "max_tokens" {
				finish = "length"
			} else if len(choice.Message.ToolCalls) > 0 || finish == "tool_calls" {
				finish = "tool_calls"
			} else {
				finish = "stop"
			}
			var usage *completionUsage
			if response.Usage != nil {
				usage = newCompletionUsage(
					response.Usage.InputTokens,
					response.Usage.OutputTokens,
					response.Usage.TotalTokens,
				)
			}
			chunk, err := encodeStreamChunk(
				id, model, openAIStreamDelta{}, finish, usage,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if len(chunks) == 0 {
			return nativeStreamResult{}, nil
		}
		return nativeStreamResult{Chunks: chunks, Done: done}, nil
	}
}
