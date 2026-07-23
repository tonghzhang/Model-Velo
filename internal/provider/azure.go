package provider

import (
	"context"
	"net/http"
	"strings"
)

// azureOpenAIAdapter 使用 OpenAI 消息格式，但端点规则和 api-key 鉴权由 Azure 定义。
type azureOpenAIAdapter struct {
	endpoint  string
	transport *jsonTransport
}

func newAzureOpenAIAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	endpoint, err := azureChatEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	return &azureOpenAIAdapter{endpoint: endpoint, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *azureOpenAIAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 保留 OpenAI 请求字段，只替换路由选出的模型并使用 Azure 鉴权头。
func (adapter *azureOpenAIAdapter) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	body, err := compatibleRequestBody(input)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("api-key", strings.TrimSpace(apiKey))
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	if err := validateCompatibleChatResponse(responseBody); err != nil {
		return nil, err
	}
	return responseBody, nil
}

// azureChatEndpoint 兼容资源根地址、/openai/v1 前缀以及完整 Chat 端点。
func azureChatEndpoint(rawBaseURL string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(basePath, "/chat/completions"):
	case strings.HasSuffix(basePath, "/openai/v1"):
		basePath += "/chat/completions"
	default:
		basePath += "/openai/v1/chat/completions"
	}
	parsed.Path = basePath
	parsed.RawPath = ""
	return parsed.String(), nil
}
