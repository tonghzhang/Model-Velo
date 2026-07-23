package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ChatRequest 是网关内部保留的 OpenAI Chat 请求视图。
type ChatRequest struct {
	Model               string          `json:"model"`
	Messages            []ChatMessage   `json:"messages"`
	Stream              bool            `json:"stream,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	rawBody             []byte          // 兼容协议转发时保留客户端未建模字段。
	extraFields         []string        // 原生协议用它明确拒绝无法转换的字段。
}

// ChatMessage 保留 OpenAI 消息的核心字段以及尚未建模的扩展字段名。
type ChatMessage struct {
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	extraFields []string
}

// ParseChatRequest 只解析 Provider 路由和协议转换需要的字段。
// 原始 JSON 会被复制保存，避免兼容协议转发时意外丢失 tools 等扩展能力。
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

// RequiredCapabilities 从请求内容推导路由候选必须具备的能力。
func (request ChatRequest) RequiredCapabilities() ([]Capability, error) {
	required := map[Capability]struct{}{CapabilityText: {}}
	if hasAnyField(request.extraFields, "tools", "tool_choice", "functions", "function_call", "parallel_tool_calls") {
		required[CapabilityTools] = struct{}{}
	}
	for _, message := range request.Messages {
		usesTools := message.Role == "developer" || message.Role == "tool"
		usesTools = usesTools || hasAnyField(message.extraFields, "tool_calls", "tool_call_id", "function_call")
		if usesTools {
			required[CapabilityTools] = struct{}{}
		}
		toolOnlyMessage := bytes.Equal(bytes.TrimSpace(message.Content), []byte("null")) &&
			hasAnyField(message.extraFields, "tool_calls", "function_call")
		if toolOnlyMessage {
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

// HasField 用于原生 Adapter 判断消息是否包含尚未建模的 OpenAI 字段。
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

// decodeNativeChatRequest 对原生协议采用严格转换：不能表达的字段必须报错，不能静默丢弃。
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

// compatibleRequestBody 尽量原样转发请求，只在路由要求时替换 model。
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

func compatibleStreamRequestBody(input ChatInput) ([]byte, error) {
	body, err := compatibleRequestBody(input)
	if err != nil {
		return nil, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, ErrInvalidRequest
	}
	payload["stream"] = json.RawMessage("true")
	streamBody, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return streamBody, nil
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

// decodeStop 同时接受 OpenAI 的单字符串和字符串数组形式。
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
