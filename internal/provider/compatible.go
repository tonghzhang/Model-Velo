package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const maxResponseBodyBytes int64 = 8 << 20

var (
	ErrInvalidRequest        = errors.New("upstream request could not be prepared")
	ErrUnsupportedCapability = errors.New("provider does not support a requested capability")
	ErrUnsupportedResponse   = errors.New("provider returned a capability the gateway cannot represent")
	ErrInvalidResponse       = errors.New("upstream response is not valid JSON")
	ErrResponseTooLarge      = errors.New("upstream response exceeds the size limit")
)

type HTTPError struct {
	StatusCode int
	RetryAfter string
	Code       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream returned HTTP status %d", e.StatusCode)
}

// compatibleChatAdapter owns the OpenAI Chat wire format shared by providers
// whose public chat API uses that format. Provider identity stays in the
// concrete adapter returned by the factory.
type compatibleChatAdapter struct {
	endpoint           string
	protocol           string
	supportsImageInput bool
	transport          *jsonTransport
}

type openAICompatibleAdapter struct {
	*compatibleChatAdapter
}

func newOpenAICompatibleAdapter(baseURL string, httpConfig HTTPConfig) (*openAICompatibleAdapter, error) {
	endpoint, err := compatibleChatCompletionsEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	chat := newCompatibleChatAdapter(ProtocolOpenAICompatible, endpoint, true, httpConfig)
	return &openAICompatibleAdapter{compatibleChatAdapter: chat}, nil
}

func newVendorCompatibleChat(
	protocol string,
	baseURL string,
	supportsImageInput bool,
	httpConfig HTTPConfig,
) (*compatibleChatAdapter, error) {
	endpoint, err := fixedEndpoint(baseURL, "chat/completions")
	if err != nil {
		return nil, err
	}
	return newCompatibleChatAdapter(protocol, endpoint, supportsImageInput, httpConfig), nil
}

func newCompatibleChatAdapter(
	protocol string,
	endpoint string,
	supportsImageInput bool,
	httpConfig HTTPConfig,
) *compatibleChatAdapter {
	return &compatibleChatAdapter{
		endpoint:           endpoint,
		protocol:           protocol,
		supportsImageInput: supportsImageInput,
		transport:          newJSONTransport(httpConfig),
	}
}

func (adapter *compatibleChatAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

func (adapter *compatibleChatAdapter) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	if !adapter.supportsImageInput {
		usesImage, err := requestUsesImage(input)
		if err != nil {
			return nil, err
		}
		if usesImage {
			return nil, fmt.Errorf("%w: %s image content", ErrUnsupportedCapability, adapter.protocol)
		}
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
		if err := json.Unmarshal(message["role"], &role); err != nil || strings.TrimSpace(role) == "" {
			return ErrInvalidResponse
		}
		if !hasResponseValue(message, "content", "tool_calls", "function_call", "refusal") {
			return ErrInvalidResponse
		}
	}
	return nil
}

func hasResponseValue(message map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		value := bytes.TrimSpace(message[field])
		if len(value) > 0 && !bytes.Equal(value, []byte("null")) {
			return true
		}
	}
	return false
}

func requestUsesImage(input ChatInput) (bool, error) {
	request, err := decodeChatRequest(input)
	if err != nil {
		return false, err
	}
	for _, message := range request.Messages {
		parts, err := decodeContent(message.Content)
		if err != nil {
			return false, err
		}
		for _, part := range parts {
			if part.Type == "image_url" {
				return true, nil
			}
		}
	}
	return false, nil
}

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

func parseBaseURL(rawBaseURL string) (*url.URL, error) {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		return nil, errors.New("upstream base URL is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("upstream base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("upstream base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("upstream base URL must include a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("upstream base URL must not contain credentials, query, or fragment")
	}
	return parsed, nil
}
