package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const anthropicAPIVersion = "2023-06-01"

type anthropicAdapter struct {
	endpoint  string
	transport *jsonTransport
}

type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type   string           `json:"type"`
	Text   string           `json:"text,omitempty"`
	Source *anthropicSource `json:"source,omitempty"`
}

type anthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

func newAnthropicAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	endpoint, err := fixedEndpoint(baseURL, "v1/messages")
	if err != nil {
		return nil, err
	}
	return &anthropicAdapter{endpoint: endpoint, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *anthropicAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

func (adapter *anthropicAdapter) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	request, err := decodeNativeChatRequest(input)
	if err != nil {
		return nil, err
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, err
	}
	upstream := anthropicRequest{
		Model:         request.Model,
		MaxTokens:     requestedMaxTokens(request, 1024),
		Temperature:   request.Temperature,
		TopP:          request.TopP,
		StopSequences: stopSequences,
	}
	var system []string
	for _, message := range request.Messages {
		if message.Role == "system" {
			text, err := textContent(message.Content)
			if err != nil {
				return nil, err
			}
			system = append(system, text)
			continue
		}
		content, err := anthropicContents(message.Content)
		if err != nil {
			return nil, err
		}
		upstream.Messages = append(upstream.Messages, anthropicMessage{Role: message.Role, Content: content})
	}
	if len(upstream.Messages) == 0 {
		return nil, ErrInvalidRequest
	}
	upstream.System = strings.Join(system, "\n\n")
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	headers := make(http.Header)
	headers.Set("x-api-key", strings.TrimSpace(apiKey))
	headers.Set("anthropic-version", anthropicAPIVersion)
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeAnthropicResponse(responseBody, input.RequestID)
}

func anthropicContents(raw json.RawMessage) ([]anthropicContent, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return nil, err
	}
	content := make([]anthropicContent, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			content = append(content, anthropicContent{Type: "text", Text: part.Text})
		case "image_url":
			mediaType, data, ok := parseDataURL(part.ImageURL)
			source := &anthropicSource{Type: "url", URL: part.ImageURL}
			if ok {
				source = &anthropicSource{Type: "base64", MediaType: mediaType, Data: data}
			}
			content = append(content, anthropicContent{Type: "image", Source: source})
		default:
			return nil, ErrInvalidRequest
		}
	}
	return content, nil
}

func decodeAnthropicResponse(body []byte, requestID string) ([]byte, error) {
	var response struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, ErrInvalidResponse
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type != "text" {
			if block.Type == "tool_use" {
				return nil, fmt.Errorf("%w: Anthropic tool output", ErrUnsupportedResponse)
			}
			return nil, fmt.Errorf("%w: unsupported Anthropic content block %q", ErrInvalidResponse, block.Type)
		}
		text.WriteString(block.Text)
	}
	if len(response.Content) == 0 {
		return nil, ErrInvalidResponse
	}
	finishReason := "stop"
	switch response.StopReason {
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		return nil, fmt.Errorf("%w: Anthropic tool output", ErrUnsupportedResponse)
	}
	var usage *completionUsage
	if response.Usage != nil {
		usage = newCompletionUsage(response.Usage.InputTokens, response.Usage.OutputTokens, nil)
	}
	return encodeCompletion(response.ID, requestID, response.Model, text.String(), finishReason, usage)
}
