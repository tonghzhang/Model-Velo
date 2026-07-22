package provider

import (
	"fmt"
	"strings"
)

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
	case ProtocolOpenAI:
		return newOpenAIAdapter(config.BaseURL, httpConfig)
	case ProtocolMistral:
		return newMistralAdapter(config.BaseURL, httpConfig)
	case ProtocolDeepSeek:
		return newDeepSeekAdapter(config.BaseURL, httpConfig)
	case ProtocolXAI:
		return newXAIAdapter(config.BaseURL, httpConfig)
	case ProtocolZhipu:
		return newZhipuAdapter(config.BaseURL, httpConfig)
	case ProtocolGroq:
		return newGroqAdapter(config.BaseURL, httpConfig)
	case ProtocolNVIDIA:
		return newNVIDIAAdapter(config.BaseURL, httpConfig)
	case ProtocolTogether:
		return newTogetherAdapter(config.BaseURL, httpConfig)
	case ProtocolOpenAICompatible:
		return newOpenAICompatibleAdapter(config.BaseURL, httpConfig)
	default:
		return nil, fmt.Errorf("unsupported provider protocol %q", protocol)
	}
}
