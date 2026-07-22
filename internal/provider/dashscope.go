package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type dashScopeAdapter struct {
	endpoint  string
	transport *jsonTransport
}

type dashScopeRequest struct {
	Model      string              `json:"model"`
	Input      dashScopeInput      `json:"input"`
	Parameters dashScopeParameters `json:"parameters"`
}

type dashScopeInput struct {
	Messages []dashScopeMessage `json:"messages"`
}

type dashScopeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type dashScopeParameters struct {
	ResultFormat string          `json:"result_format"`
	MaxTokens    int             `json:"max_tokens,omitempty"`
	Temperature  *float64        `json:"temperature,omitempty"`
	TopP         *float64        `json:"top_p,omitempty"`
	Stop         json.RawMessage `json:"stop,omitempty"`
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

func (adapter *dashScopeAdapter) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	request, err := decodeNativeChatRequest(input)
	if err != nil {
		return nil, err
	}
	upstream := dashScopeRequest{
		Model: request.Model,
		Parameters: dashScopeParameters{
			ResultFormat: "message",
			MaxTokens:    requestedMaxTokens(request, 0),
			Temperature:  request.Temperature,
			TopP:         request.TopP,
			Stop:         request.Stop,
		},
	}
	for _, message := range request.Messages {
		content, err := textContent(message.Content)
		if err != nil {
			return nil, err
		}
		upstream.Input.Messages = append(upstream.Input.Messages, dashScopeMessage{
			Role: message.Role, Content: content,
		})
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeDashScopeResponse(responseBody, request.Model, input.RequestID)
}

func decodeDashScopeResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		RequestID string `json:"request_id"`
		Output    struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content   string          `json:"content"`
					ToolCalls json.RawMessage `json:"tool_calls"`
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
	if toolCalls := bytes.TrimSpace(choice.Message.ToolCalls); len(toolCalls) > 0 && !bytes.Equal(toolCalls, []byte("null")) {
		return nil, fmt.Errorf("%w: DashScope tool output", ErrUnsupportedResponse)
	}
	finishReason := strings.ToLower(choice.FinishReason)
	if finishReason == "null" || finishReason == "" {
		finishReason = "stop"
	}
	var usage *completionUsage
	if response.Usage != nil {
		usage = newCompletionUsage(response.Usage.InputTokens, response.Usage.OutputTokens, response.Usage.TotalTokens)
	}
	return encodeCompletion(response.RequestID, requestID, model, choice.Message.Content, finishReason, usage)
}
