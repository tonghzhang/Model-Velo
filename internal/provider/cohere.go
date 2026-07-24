package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// cohereAdapter 使用 Cohere v2 Chat 的原生消息和响应结构。
type cohereAdapter struct {
	endpoint  string
	transport *jsonTransport
}

// cohereRequest 中 P 对应 OpenAI 请求里的 top_p。
type cohereRequest struct {
	Model            string          `json:"model"`
	Messages         []cohereMessage `json:"messages"`
	Stream           bool            `json:"stream"`
	MaxTokens        int             `json:"max_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	P                *float64        `json:"p,omitempty"`
	StopSequences    []string        `json:"stop_sequences,omitempty"`
	Tools            []ChatTool      `json:"tools,omitempty"`
	ToolChoice       string          `json:"tool_choice,omitempty"`
	ResponseFormat   *cohereFormat   `json:"response_format,omitempty"`
	Seed             *int64          `json:"seed,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	StrictTools      bool            `json:"strict_tools,omitempty"`
}

type cohereMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []ChatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type cohereFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

func newCohereAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	endpoint, err := fixedEndpoint(baseURL, "chat")
	if err != nil {
		return nil, err
	}
	return &cohereAdapter{endpoint: endpoint, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *cohereAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 转换 Cohere 能够无损表达的文本、图片、工具和结构化请求。
func (adapter *cohereAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	body, request, err := cohereRequestBody(input, false)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.post(
		ctx, adapter.endpoint, input.RequestID, body, cohereHeaders(apiKey),
	)
	if err != nil {
		return nil, err
	}
	return decodeCohereResponse(responseBody, request.Model, input.RequestID)
}

func cohereRequestBody(input ChatInput, stream bool) ([]byte, ChatRequest, error) {
	request, err := decodeNativeChatRequest(input, nativeRequestSupport{
		tools: true, structured: true, seed: true, penalties: true,
		developer: true, toolName: true,
	})
	if err != nil {
		return nil, ChatRequest{}, err
	}
	if request.Seed != nil && *request.Seed < 0 {
		return nil, ChatRequest{}, fmt.Errorf(
			"%w: Cohere seed cannot be negative", ErrInvalidRequest,
		)
	}
	for name, penalty := range map[string]*float64{
		"frequency_penalty": request.FrequencyPenalty,
		"presence_penalty":  request.PresencePenalty,
	} {
		if penalty != nil && (*penalty < 0 || *penalty > 1) {
			return nil, ChatRequest{}, fmt.Errorf(
				"%w: Cohere %s must be between 0 and 1",
				ErrInvalidRequest,
				name,
			)
		}
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, ChatRequest{}, err
	}
	upstream := cohereRequest{
		Model:            request.Model,
		MaxTokens:        requestedMaxTokens(request, 0),
		Temperature:      request.Temperature,
		P:                request.TopP,
		StopSequences:    stopSequences,
		Tools:            request.Tools,
		Seed:             request.Seed,
		FrequencyPenalty: request.FrequencyPenalty,
		PresencePenalty:  request.PresencePenalty,
		Stream:           stream,
	}
	if err := applyCohereFeatures(&upstream, request); err != nil {
		return nil, ChatRequest{}, err
	}
	for _, message := range request.Messages {
		var content json.RawMessage
		rawContent := strings.TrimSpace(string(message.Content))
		if rawContent != "" && rawContent != "null" {
			parts, err := decodeNativeContent(message.Content)
			if err != nil {
				return nil, ChatRequest{}, err
			}
			for _, part := range parts {
				if part.Type != "text" && part.Type != "image_url" {
					return nil, ChatRequest{}, fmt.Errorf(
						"%w: Cohere %s content", ErrUnsupportedCapability, part.Type,
					)
				}
			}
			content = bytes.Clone(message.Content)
		}
		role := message.Role
		if role == "developer" {
			role = "system"
		}
		upstream.Messages = append(upstream.Messages, cohereMessage{
			Role: role, Content: content,
			ToolCalls: message.ToolCalls, ToolCallID: message.ToolCallID,
		})
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	return body, request, nil
}

func cohereHeaders(apiKey string) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	return headers
}

func (adapter *cohereAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) (*ChatEventStream, error) {
	body, request, err := cohereRequestBody(input, true)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.postStream(
		ctx, adapter.endpoint, input.RequestID, body, cohereHeaders(apiKey),
	)
	if err != nil {
		return nil, err
	}
	return newMappedSSEStream(
		responseBody, newCohereStreamMapper(request.Model, input.RequestID),
	)
}

func applyCohereFeatures(upstream *cohereRequest, request ChatRequest) error {
	if len(request.Tools) > 0 && request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		return unsupportedRequestField("parallel_tool_calls")
	}
	choice, err := decodeToolChoice(request.ToolChoice)
	if err != nil {
		return err
	}
	switch choice.Mode {
	case "", "auto":
	case "required", "none":
		upstream.ToolChoice = strings.ToUpper(choice.Mode)
	case "function":
		return unsupportedRequestField("named tool_choice")
	}
	format, err := decodeResponseFormat(request.ResponseFormat)
	if err != nil {
		return err
	}
	switch format.Type {
	case "", "text":
	case "json_object":
		upstream.ResponseFormat = &cohereFormat{Type: "json_object"}
	case "json_schema":
		upstream.ResponseFormat = &cohereFormat{
			Type: "json_object", JSONSchema: format.Schema,
		}
	}
	if upstream.ResponseFormat != nil && len(request.Tools) > 0 {
		return fmt.Errorf(
			"%w: Cohere response_format cannot be combined with tools",
			ErrUnsupportedCapability,
		)
	}
	for _, tool := range request.Tools {
		if tool.Function.Strict != nil && *tool.Function.Strict {
			upstream.StrictTools = true
			break
		}
	}
	return nil
}

// decodeCohereResponse 优先使用实际 tokens，缺失时才回退到 billed_units。
func decodeCohereResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		ID      string `json:"id"`
		Message struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content"`
			ToolCalls []ChatToolCall  `json:"tool_calls"`
			ToolPlan  string          `json:"tool_plan"`
			Citations json.RawMessage `json:"citations"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
		Usage        *struct {
			Tokens struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
			} `json:"tokens"`
			BilledUnits struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
			} `json:"billed_units"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil ||
		(len(response.Message.Content) == 0 &&
			len(response.Message.ToolCalls) == 0 &&
			response.Message.ToolPlan == "") {
		return nil, ErrInvalidResponse
	}
	if hasNonEmptyJSONValue(response.Message.Citations) {
		return nil, fmt.Errorf(
			"%w: Cohere citations", ErrUnsupportedResponse,
		)
	}
	var content strings.Builder
	var reasoning strings.Builder
	for _, part := range response.Message.Content {
		switch part.Type {
		case "text":
			content.WriteString(part.Text)
		case "thinking":
			reasoning.WriteString(part.Thinking)
		default:
			return nil, fmt.Errorf("%w: Cohere output block %q", ErrUnsupportedResponse, part.Type)
		}
	}
	if response.Message.ToolPlan != "" {
		if reasoning.Len() > 0 {
			reasoning.WriteByte('\n')
		}
		reasoning.WriteString(response.Message.ToolPlan)
	}
	if err := validateResponseToolCalls(response.Message.ToolCalls); err != nil {
		return nil, err
	}
	var usage *completionUsage
	if response.Usage != nil {
		promptTokens := response.Usage.Tokens.InputTokens
		completionTokens := response.Usage.Tokens.OutputTokens
		if promptTokens == nil && completionTokens == nil {
			promptTokens = response.Usage.BilledUnits.InputTokens
			completionTokens = response.Usage.BilledUnits.OutputTokens
		}
		usage = newCompletionUsage(promptTokens, completionTokens, nil)
	}
	finishReason := strings.ToLower(response.FinishReason)
	if finishReason == "max_tokens" {
		finishReason = "length"
	} else if finishReason == "complete" || finishReason == "" {
		finishReason = "stop"
	} else if finishReason == "tool_call" {
		finishReason = "tool_calls"
	}
	text := content.String()
	message := completionMessage{Role: "assistant", ToolCalls: response.Message.ToolCalls}
	if reasoning.Len() > 0 {
		reasoningContent := reasoning.String()
		message.ReasoningContent = &reasoningContent
	}
	if text != "" || len(response.Message.ToolCalls) == 0 {
		message.Content = &text
	}
	return encodeCompletionMessage(response.ID, requestID, model, message, finishReason, usage)
}

func newCohereStreamMapper(model, requestID string) nativeStreamMapper {
	id := "chatcmpl-" + requestID
	return func(event string, data []byte) (nativeStreamResult, error) {
		var envelope struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Index int    `json:"index"`
			Delta struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Role     string `json:"role"`
					ToolPlan string `json:"tool_plan"`
					Content  struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						Thinking string `json:"thinking"`
					} `json:"content"`
					ToolCalls *struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				Usage *struct {
					Tokens struct {
						InputTokens  *int `json:"input_tokens"`
						OutputTokens *int `json:"output_tokens"`
					} `json:"tokens"`
					BilledUnits struct {
						InputTokens  *int `json:"input_tokens"`
						OutputTokens *int `json:"output_tokens"`
					} `json:"billed_units"`
				} `json:"usage"`
			} `json:"delta"`
		}
		if err := invalidNativeStreamJSON(data, &envelope); err != nil {
			return nativeStreamResult{}, err
		}
		eventType := envelope.Type
		if eventType == "" {
			eventType = event
		}
		if envelope.ID != "" {
			id = envelope.ID
		}
		switch eventType {
		case "message-start":
			return streamResult(
				id, model, openAIStreamDelta{Role: "assistant"}, "", nil, false,
			)
		case "content-start", "content-end", "tool-call-end", "debug":
			return nativeStreamResult{}, nil
		case "content-delta":
			content := envelope.Delta.Message.Content
			if content.Type == "thinking" || content.Thinking != "" {
				thinking := content.Thinking
				return streamResult(
					id, model,
					openAIStreamDelta{ReasoningContent: &thinking},
					"", nil, false,
				)
			}
			text := content.Text
			return streamResult(
				id, model, openAIStreamDelta{Content: &text}, "", nil, false,
			)
		case "tool-plan-delta":
			if envelope.Delta.Message.ToolPlan == "" {
				return nativeStreamResult{}, ErrInvalidStream
			}
			plan := envelope.Delta.Message.ToolPlan
			return streamResult(
				id, model,
				openAIStreamDelta{ReasoningContent: &plan},
				"", nil, false,
			)
		case "tool-call-start", "tool-call-delta":
			call := envelope.Delta.Message.ToolCalls
			if call == nil {
				return nativeStreamResult{}, ErrInvalidStream
			}
			return streamResult(id, model, openAIStreamDelta{
				ToolCalls: []openAIStreamToolCallDelta{{
					Index: envelope.Index, ID: call.ID, Type: call.Type,
					Function: openAIStreamFunctionDelta{
						Name: call.Function.Name, Arguments: call.Function.Arguments,
					},
				}},
			}, "", nil, false)
		case "message-end":
			finish := strings.ToLower(envelope.Delta.FinishReason)
			switch finish {
			case "max_tokens":
				finish = "length"
			case "tool_call":
				finish = "tool_calls"
			default:
				finish = "stop"
			}
			var usage *completionUsage
			if envelope.Delta.Usage != nil {
				prompt := envelope.Delta.Usage.Tokens.InputTokens
				completion := envelope.Delta.Usage.Tokens.OutputTokens
				if prompt == nil && completion == nil {
					prompt = envelope.Delta.Usage.BilledUnits.InputTokens
					completion = envelope.Delta.Usage.BilledUnits.OutputTokens
				}
				usage = newCompletionUsage(prompt, completion, nil)
			}
			return streamResult(
				id, model, openAIStreamDelta{}, finish, usage, true,
			)
		case "citation-start", "citation-end":
			return nativeStreamResult{}, fmt.Errorf(
				"%w: Cohere citations", ErrUnsupportedResponse,
			)
		default:
			return nativeStreamResult{}, fmt.Errorf(
				"%w: Cohere stream event %q", ErrInvalidStream, eventType,
			)
		}
	}
}

func hasNonEmptyJSONValue(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 &&
		!bytes.Equal(raw, []byte("null")) &&
		!bytes.Equal(raw, []byte("[]")) &&
		!bytes.Equal(raw, []byte("{}"))
}
