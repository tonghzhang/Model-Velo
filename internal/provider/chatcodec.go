package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ChatRequest struct {
	Model               string          `json:"model"`
	Messages            []ChatMessage   `json:"messages"`
	Stream              bool            `json:"stream,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	rawBody             []byte
	extraFields         []string
}

type ChatMessage struct {
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	extraFields []string
}

type chatContentPart struct {
	Type     string
	Text     string
	ImageURL string
}

func ParseChatRequest(body []byte) (ChatRequest, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return ChatRequest{}, ErrInvalidRequest
	}

	var request ChatRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return ChatRequest{}, ErrInvalidRequest
	}
	request.rawBody = bytes.Clone(body)
	request.extraFields = unknownFields(fields, map[string]struct{}{
		"model": {}, "messages": {}, "stream": {}, "max_tokens": {},
		"max_completion_tokens": {}, "temperature": {}, "top_p": {}, "stop": {},
	})

	var encodedMessages []map[string]json.RawMessage
	if rawMessages, ok := fields["messages"]; ok {
		if err := json.Unmarshal(rawMessages, &encodedMessages); err != nil {
			return ChatRequest{}, ErrInvalidRequest
		}
	}
	for index := range request.Messages {
		request.Messages[index].extraFields = unknownFields(encodedMessages[index], map[string]struct{}{
			"role": {}, "content": {},
		})
	}
	if _, err := decodeStop(request.Stop); err != nil {
		return ChatRequest{}, err
	}
	return request, nil
}

func (request ChatRequest) RequiredCapabilities() ([]Capability, error) {
	required := map[Capability]struct{}{CapabilityText: {}}
	if hasAnyField(request.extraFields, "tools", "tool_choice", "functions", "function_call", "parallel_tool_calls") {
		required[CapabilityTools] = struct{}{}
	}
	for _, message := range request.Messages {
		if message.Role == "developer" || message.Role == "tool" || hasAnyField(message.extraFields, "tool_calls", "tool_call_id", "function_call") {
			required[CapabilityTools] = struct{}{}
		}
		if bytes.Equal(bytes.TrimSpace(message.Content), []byte("null")) && hasAnyField(message.extraFields, "tool_calls", "function_call") {
			continue
		}
		parts, err := decodeContent(message.Content)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			if part.Type == "image_url" {
				required[CapabilityImage] = struct{}{}
			}
		}
	}

	capabilities := make([]Capability, 0, len(required))
	for _, capability := range []Capability{CapabilityText, CapabilityImage, CapabilityTools} {
		if _, ok := required[capability]; ok {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities, nil
}

func (message ChatMessage) HasField(name string) bool {
	return hasAnyField(message.extraFields, name)
}

func hasAnyField(fields []string, names ...string) bool {
	for _, field := range fields {
		if slices.Contains(names, field) {
			return true
		}
	}
	return false
}

func unknownFields(fields map[string]json.RawMessage, known map[string]struct{}) []string {
	unknown := make([]string, 0)
	for name := range fields {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	return unknown
}

func decodeChatRequest(input ChatInput) (ChatRequest, error) {
	request := input.Request
	if input.ModelOverride != "" {
		request.Model = input.ModelOverride
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" || len(request.Messages) == 0 {
		return ChatRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func decodeNativeChatRequest(input ChatInput) (ChatRequest, error) {
	request, err := decodeChatRequest(input)
	if err != nil {
		return ChatRequest{}, err
	}
	if len(request.extraFields) > 0 {
		return ChatRequest{}, fmt.Errorf("%w: request field %q", ErrUnsupportedCapability, request.extraFields[0])
	}
	for index, message := range request.Messages {
		if len(message.extraFields) > 0 {
			return ChatRequest{}, fmt.Errorf(
				"%w: messages[%d] field %q",
				ErrUnsupportedCapability,
				index,
				message.extraFields[0],
			)
		}
		switch message.Role {
		case "system", "user", "assistant":
		default:
			return ChatRequest{}, fmt.Errorf(
				"%w: messages[%d] role %q",
				ErrUnsupportedCapability,
				index,
				message.Role,
			)
		}
	}
	return request, nil
}

func compatibleRequestBody(input ChatInput) ([]byte, error) {
	if input.ModelOverride == "" {
		return bytes.Clone(input.Request.rawBody), nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(input.Request.rawBody, &payload); err != nil || payload == nil {
		return nil, ErrInvalidRequest
	}
	model := strings.TrimSpace(input.ModelOverride)
	if model == "" {
		return nil, ErrInvalidRequest
	}
	encodedModel, err := json.Marshal(model)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	payload["model"] = encodedModel
	mappedBody, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return mappedBody, nil
}

func decodeContent(raw json.RawMessage) ([]chatContentPart, error) {
	return decodeContentParts(raw, false)
}

func decodeNativeContent(raw json.RawMessage) ([]chatContentPart, error) {
	return decodeContentParts(raw, true)
}

func decodeContentParts(raw json.RawMessage, rejectUnknown bool) ([]chatContentPart, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, ErrInvalidRequest
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, ErrInvalidRequest
		}
		return []chatContentPart{{Type: "text", Text: text}}, nil
	}

	var encodedParts []json.RawMessage
	if err := json.Unmarshal(raw, &encodedParts); err != nil || len(encodedParts) == 0 {
		return nil, ErrInvalidRequest
	}
	parts := make([]chatContentPart, 0, len(encodedParts))
	for _, encodedPart := range encodedParts {
		var part struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(encodedPart, &part); err != nil {
			return nil, ErrInvalidRequest
		}
		if rejectUnknown {
			if err := rejectUnknownContentFields(encodedPart, part.Type); err != nil {
				return nil, err
			}
		}
		switch part.Type {
		case "text":
			parts = append(parts, chatContentPart{Type: "text", Text: part.Text})
		case "image_url":
			if strings.TrimSpace(part.ImageURL.URL) == "" {
				return nil, ErrInvalidRequest
			}
			parts = append(parts, chatContentPart{Type: "image_url", ImageURL: part.ImageURL.URL})
		default:
			return nil, fmt.Errorf("%w: content part %q", ErrUnsupportedCapability, part.Type)
		}
	}
	return parts, nil
}

func rejectUnknownContentFields(encoded json.RawMessage, partType string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || fields == nil {
		return ErrInvalidRequest
	}
	known := map[string]struct{}{"type": {}}
	switch partType {
	case "text":
		known["text"] = struct{}{}
	case "image_url":
		known["image_url"] = struct{}{}
	}
	if extra := unknownFields(fields, known); len(extra) > 0 {
		return fmt.Errorf("%w: content part field %q", ErrUnsupportedCapability, extra[0])
	}
	if partType != "image_url" {
		return nil
	}
	var imageFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["image_url"], &imageFields); err != nil || imageFields == nil {
		return ErrInvalidRequest
	}
	if extra := unknownFields(imageFields, map[string]struct{}{"url": {}}); len(extra) > 0 {
		return fmt.Errorf("%w: image_url field %q", ErrUnsupportedCapability, extra[0])
	}
	return nil
}

func textContent(raw json.RawMessage) (string, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, part := range parts {
		if part.Type != "text" {
			return "", fmt.Errorf("%w: image content", ErrUnsupportedCapability)
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

func base64Image(raw string) (string, string, error) {
	mediaType, data, ok := parseDataURL(raw)
	if ok {
		return mediaType, data, nil
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "data:") {
		return "", "", ErrInvalidRequest
	}
	return "", "", fmt.Errorf("%w: remote image url", ErrUnsupportedCapability)
}

func parseDataURL(raw string) (mediaType string, data string, ok bool) {
	prefix, encoded, found := strings.Cut(raw, ",")
	if !found || !strings.HasPrefix(prefix, "data:") || !strings.HasSuffix(prefix, ";base64") {
		return "", "", false
	}
	mediaType = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
	if mediaType == "" {
		return "", "", false
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return "", "", false
	}
	return mediaType, encoded, true
}

type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func newCompletionUsage(prompt, completion, total *int) *completionUsage {
	if prompt == nil && completion == nil && total == nil {
		return nil
	}
	usage := &completionUsage{}
	if prompt != nil {
		usage.PromptTokens = *prompt
	}
	if completion != nil {
		usage.CompletionTokens = *completion
	}
	if total != nil {
		usage.TotalTokens = *total
	}
	return usage
}

type completionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *completionUsage   `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int               `json:"index"`
	Message      completionMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type completionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func encodeCompletion(
	id string,
	requestID string,
	model string,
	content string,
	finishReason string,
	usage *completionUsage,
) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "chatcmpl-" + strings.TrimSpace(requestID)
	}
	if id == "chatcmpl-" {
		id = "chatcmpl-upstream"
	}
	if usage != nil && usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	response := completionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []completionChoice{{
			Message:      completionMessage{Role: "assistant", Content: content},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	return encoded, nil
}

func requestedMaxTokens(request ChatRequest, fallback int) int {
	if request.MaxCompletionTokens != nil {
		return *request.MaxCompletionTokens
	}
	if request.MaxTokens != nil {
		return *request.MaxTokens
	}
	return fallback
}

func decodeStop(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var values []string
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, ErrInvalidRequest
		}
		return []string{value}, nil
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, ErrInvalidRequest
	}
	return values, nil
}
