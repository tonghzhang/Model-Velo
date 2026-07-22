package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type cohereAdapter struct {
	endpoint  string
	transport *jsonTransport
}

type cohereRequest struct {
	Model         string          `json:"model"`
	Messages      []cohereMessage `json:"messages"`
	Stream        bool            `json:"stream"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	P             *float64        `json:"p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
}

type cohereMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

func (adapter *cohereAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	request, err := decodeNativeChatRequest(input)
	if err != nil {
		return nil, err
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, err
	}
	upstream := cohereRequest{
		Model:         request.Model,
		MaxTokens:     requestedMaxTokens(request, 0),
		Temperature:   request.Temperature,
		P:             request.TopP,
		StopSequences: stopSequences,
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
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeCohereResponse(responseBody, request.Model, input.RequestID)
}

func decodeCohereResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		ID      string `json:"id"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
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
	if err := json.Unmarshal(body, &response); err != nil || len(response.Message.Content) == 0 {
		return nil, ErrInvalidResponse
	}
	var content strings.Builder
	for _, part := range response.Message.Content {
		if part.Type != "text" {
			return nil, fmt.Errorf("%w: Cohere output block %q", ErrUnsupportedResponse, part.Type)
		}
		content.WriteString(part.Text)
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
	}
	return encodeCompletion(response.ID, requestID, model, content.String(), finishReason, usage)
}
