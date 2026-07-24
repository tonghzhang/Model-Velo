package provider

import (
	"context"
	"net/http"
	"strings"
)

// cloudflareAdapter 使用 Workers AI 官方 OpenAI-compatible Chat 端点。
type cloudflareAdapter struct {
	endpoint  string
	transport *jsonTransport
}

func newCloudflareAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	endpoint, err := cloudflareChatEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	return &cloudflareAdapter{endpoint: endpoint, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *cloudflareAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

func (adapter *cloudflareAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	body, err := compatibleRequestBody(input)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	if err := validateCompatibleChatResponse(responseBody); err != nil {
		return nil, err
	}
	return responseBody, nil
}

func (adapter *cloudflareAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) (*ChatEventStream, error) {
	body, err := compatibleStreamRequestBody(input, true)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.postStream(
		ctx, adapter.endpoint, input.RequestID, body, headers,
	)
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

func cloudflareChatEndpoint(rawBaseURL string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
	case strings.HasSuffix(path, "/ai/v1"):
		path += "/chat/completions"
	default:
		path += "/ai/v1/chat/completions"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}
