package provider

import (
	"fmt"
	"strings"
)

// AdapterConfig 是构造单个 Provider Adapter 所需的静态配置。
type AdapterConfig struct {
	ProviderID         string
	Protocol           string
	BaseURL            string
	HTTP               HTTPConfig
	DisableStreamUsage bool
}

// NewAdapter 根据协议选择真实的 wire format 实现。
// 使用相同 OpenAI 格式的厂商共享一个 Adapter，原生协议保持独立实现。
func NewAdapter(config AdapterConfig) (Adapter, error) {
	protocol := strings.ToLower(strings.TrimSpace(config.Protocol))
	httpConfig := config.HTTP
	if httpConfig == (HTTPConfig{}) {
		httpConfig = DefaultHTTPConfig()
	}
	if err := httpConfig.Validate(); err != nil {
		return nil, err
	}

	switch protocol {
	case ProtocolAnthropic:
		return newAnthropicAdapter(config.BaseURL, httpConfig)
	case ProtocolGemini:
		return newGeminiAdapter(config.BaseURL, httpConfig)
	case ProtocolAzureOpenAI:
		return newAzureOpenAIAdapter(config.BaseURL, httpConfig)
	case ProtocolDashScope:
		return newDashScopeAdapter(config.BaseURL, httpConfig)
	case ProtocolCohere:
		return newCohereAdapter(config.BaseURL, httpConfig)
	case ProtocolOllama:
		return newOllamaAdapter(config.BaseURL, httpConfig)
	case ProtocolBedrock:
		return newBedrockAdapter(config.BaseURL, httpConfig)
	case ProtocolCloudflare:
		return newCloudflareAdapter(config.BaseURL, httpConfig)
	case ProtocolOpenAICompatible,
		ProtocolOpenAI,
		ProtocolMistral,
		ProtocolDeepSeek,
		ProtocolXAI,
		ProtocolZhipu,
		ProtocolGroq,
		ProtocolNVIDIA,
		ProtocolTogether:
		return newCompatibleChatAdapterWithUsage(
			protocol,
			config.BaseURL,
			httpConfig,
			!config.DisableStreamUsage,
		)
	default:
		return nil, fmt.Errorf("unsupported provider protocol %q", protocol)
	}
}
