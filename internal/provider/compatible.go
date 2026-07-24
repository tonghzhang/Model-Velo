package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type compatibleChatAdapter struct {
	endpoint           string
	protocol           string
	enforceStreamUsage bool
	transport          *jsonTransport
}

// newCompatibleChatAdapter 创建共享 OpenAI Chat Completions wire format 的 Adapter。
// 厂商身份仍由 protocol 保留，用于能力检查和错误说明。
func newCompatibleChatAdapter(
	protocol string,
	baseURL string,
	httpConfig HTTPConfig,
) (*compatibleChatAdapter, error) {
	return newCompatibleChatAdapterWithUsage(protocol, baseURL, httpConfig, true)
}

func newCompatibleChatAdapterWithUsage(
	protocol string,
	baseURL string,
	httpConfig HTTPConfig,
	enforceStreamUsage bool,
) (*compatibleChatAdapter, error) {
	endpoint, err := compatibleChatEndpoint(protocol, baseURL)
	if err != nil {
		return nil, err
	}
	return &compatibleChatAdapter{
		endpoint:           endpoint,
		protocol:           protocol,
		enforceStreamUsage: enforceStreamUsage,
		transport:          newJSONTransport(httpConfig),
	}, nil
}

func (adapter *compatibleChatAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 原样保留兼容请求字段，仅承担能力校验、鉴权和一次上游调用。
func (adapter *compatibleChatAdapter) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	if err := adapter.validateInput(input); err != nil {
		return nil, err
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, ErrInvalidRequest
	}
	body, err := compatibleRequestBody(input)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+apiKey)
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	if err := validateCompatibleChatResponse(responseBody); err != nil {
		return nil, err
	}
	return responseBody, nil
}

// OpenStream 建立一次兼容 SSE 调用，不读取首事件，也不决定 Retry/Fallback。
func (adapter *compatibleChatAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) (*ChatEventStream, error) {
	if err := adapter.validateInput(input); err != nil {
		return nil, err
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, ErrInvalidRequest
	}
	body, err := compatibleStreamRequestBody(input, adapter.enforceStreamUsage)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+apiKey)
	responseBody, err := adapter.transport.postStream(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	stream, err := newChatEventStream(responseBody)
	if err != nil {
		responseBody.Close()
		return nil, err
	}
	return stream, nil
}

func (adapter *compatibleChatAdapter) validateInput(input ChatInput) error {
	request, err := decodeChatRequest(input)
	if err != nil {
		return err
	}
	capabilities, err := request.RequiredCapabilities()
	if err != nil {
		return err
	}
	for _, capability := range capabilities {
		if !ProtocolSupportsCapability(adapter.protocol, capability) {
			return fmt.Errorf(
				"%w: %s %s content",
				ErrUnsupportedCapability,
				adapter.protocol,
				capability,
			)
		}
	}
	return nil
}

// validateCompatibleChatResponse 拦截 HTTP 200 中夹带的错误体和缺失消息的畸形响应。
func validateCompatibleChatResponse(body []byte) error {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ErrInvalidResponse
	}
	if errorBody := bytes.TrimSpace(envelope.Error); len(errorBody) > 0 && !bytes.Equal(errorBody, []byte("null")) {
		return ErrInvalidResponse
	}
	if len(envelope.Choices) == 0 {
		return ErrInvalidResponse
	}
	for _, choice := range envelope.Choices {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(choice.Message, &message); err != nil || message == nil {
			return ErrInvalidResponse
		}
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil || role != "assistant" {
			return ErrInvalidResponse
		}
		if err := validateCompatibleMessagePayload(message); err != nil {
			return ErrInvalidResponse
		}
	}
	return nil
}

func validateCompatibleMessagePayload(message map[string]json.RawMessage) error {
	hasPayload := false
	if valuePresent(message["content"]) {
		var content string
		if err := json.Unmarshal(message["content"], &content); err != nil {
			return ErrInvalidResponse
		}
		hasPayload = true
	}
	if valuePresent(message["tool_calls"]) {
		var calls []ChatToolCall
		if err := json.Unmarshal(message["tool_calls"], &calls); err != nil ||
			len(calls) == 0 ||
			validateResponseToolCalls(calls) != nil {
			return ErrInvalidResponse
		}
		hasPayload = true
	}
	if valuePresent(message["function_call"]) {
		var call ChatFunctionCall
		if err := json.Unmarshal(message["function_call"], &call); err != nil ||
			strings.TrimSpace(call.Name) == "" ||
			validateJSONStringObject(call.Arguments) != nil {
			return ErrInvalidResponse
		}
		hasPayload = true
	}
	if valuePresent(message["refusal"]) {
		var refusal string
		if err := json.Unmarshal(message["refusal"], &refusal); err != nil {
			return ErrInvalidResponse
		}
		hasPayload = true
	}
	if !hasPayload {
		return ErrInvalidResponse
	}
	return nil
}

// compatibleChatEndpoint 区分自定义兼容地址与厂商已带版本前缀的地址。
func compatibleChatEndpoint(protocol, baseURL string) (string, error) {
	if protocol == ProtocolOpenAICompatible {
		return compatibleChatCompletionsEndpoint(baseURL)
	}
	return fixedEndpoint(baseURL, "chat/completions")
}

// compatibleChatCompletionsEndpoint 允许用户传入主机、版本前缀或完整端点。
func compatibleChatCompletionsEndpoint(rawBaseURL string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(basePath, "/chat/completions"):
		parsed.Path = basePath
	case basePath == "":
		parsed.Path = "/v1/chat/completions"
	default:
		parsed.Path = basePath + "/chat/completions"
	}
	parsed.RawPath = ""
	return parsed.String(), nil
}
