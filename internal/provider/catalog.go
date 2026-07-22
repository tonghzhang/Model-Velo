package provider

import (
	"fmt"
	"strings"
)

const (
	ProtocolOpenAICompatible = "openai-compatible"
	ProtocolOpenAI           = "openai"
	ProtocolAnthropic        = "anthropic"
	ProtocolGemini           = "gemini"
	ProtocolAzureOpenAI      = "azure-openai"
	ProtocolDashScope        = "dashscope"
	ProtocolCohere           = "cohere"
	ProtocolOllama           = "ollama"
	ProtocolMistral          = "mistral"
	ProtocolDeepSeek         = "deepseek"
	ProtocolXAI              = "xai"
	ProtocolZhipu            = "zhipu"
	ProtocolGroq             = "groq"
	ProtocolNVIDIA           = "nvidia"
	ProtocolTogether         = "together"
	ProtocolBedrock          = "bedrock"
	ProtocolCloudflare       = "cloudflare"
)

const (
	VendorCustom     = "custom"
	VendorOpenAI     = "openai"
	VendorAnthropic  = "anthropic"
	VendorGoogle     = "google"
	VendorAzure      = "azure"
	VendorXAI        = "xai"
	VendorDeepSeek   = "deepseek"
	VendorAlibaba    = "alibaba"
	VendorZhipu      = "zhipu"
	VendorMistral    = "mistral"
	VendorGroq       = "groq"
	VendorNVIDIA     = "nvidia"
	VendorTogether   = "together"
	VendorCohere     = "cohere"
	VendorOllama     = "ollama"
	VendorBedrock    = "bedrock"
	VendorCloudflare = "cloudflare"
)

type Preset struct {
	Protocol string
	BaseURL  string
}

var presets = map[string]Preset{
	VendorCustom:     {Protocol: ProtocolOpenAICompatible},
	VendorOpenAI:     {Protocol: ProtocolOpenAI, BaseURL: "https://api.openai.com/v1"},
	VendorAnthropic:  {Protocol: ProtocolAnthropic, BaseURL: "https://api.anthropic.com"},
	VendorGoogle:     {Protocol: ProtocolGemini, BaseURL: "https://generativelanguage.googleapis.com/v1beta"},
	VendorAzure:      {Protocol: ProtocolAzureOpenAI},
	VendorXAI:        {Protocol: ProtocolXAI, BaseURL: "https://api.x.ai/v1"},
	VendorDeepSeek:   {Protocol: ProtocolDeepSeek, BaseURL: "https://api.deepseek.com"},
	VendorAlibaba:    {Protocol: ProtocolDashScope, BaseURL: "https://dashscope.aliyuncs.com/api/v1"},
	VendorZhipu:      {Protocol: ProtocolZhipu, BaseURL: "https://open.bigmodel.cn/api/paas/v4"},
	VendorMistral:    {Protocol: ProtocolMistral, BaseURL: "https://api.mistral.ai/v1"},
	VendorGroq:       {Protocol: ProtocolGroq, BaseURL: "https://api.groq.com/openai/v1"},
	VendorNVIDIA:     {Protocol: ProtocolNVIDIA, BaseURL: "https://integrate.api.nvidia.com/v1"},
	VendorTogether:   {Protocol: ProtocolTogether, BaseURL: "https://api.together.ai/v1"},
	VendorCohere:     {Protocol: ProtocolCohere, BaseURL: "https://api.cohere.com/v2"},
	VendorOllama:     {Protocol: ProtocolOllama, BaseURL: "http://localhost:11434"},
	VendorBedrock:    {Protocol: ProtocolBedrock},
	VendorCloudflare: {Protocol: ProtocolCloudflare},
}

var compatibleBaseURLs = map[string]string{
	VendorOpenAI:   "https://api.openai.com/v1",
	VendorGoogle:   "https://generativelanguage.googleapis.com/v1beta/openai",
	VendorXAI:      "https://api.x.ai/v1",
	VendorDeepSeek: "https://api.deepseek.com",
	VendorAlibaba:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
	VendorZhipu:    "https://open.bigmodel.cn/api/paas/v4",
	VendorMistral:  "https://api.mistral.ai/v1",
	VendorGroq:     "https://api.groq.com/openai/v1",
	VendorNVIDIA:   "https://integrate.api.nvidia.com/v1",
	VendorTogether: "https://api.together.ai/v1",
}

func Resolve(vendor, protocol, configuredBaseURL string) (Preset, error) {
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	configuredBaseURL = strings.TrimSpace(configuredBaseURL)
	preset, exists := presets[vendor]
	if !exists {
		return Preset{}, fmt.Errorf("unsupported provider vendor %q", vendor)
	}

	if protocol == "" {
		return Preset{}, fmt.Errorf("provider vendor %q requires protocol", vendor)
	}
	if !SupportedProtocol(protocol) {
		return Preset{}, fmt.Errorf("unsupported provider protocol %q", protocol)
	}
	if vendor == VendorCustom && protocol != ProtocolOpenAICompatible {
		return Preset{}, fmt.Errorf("provider vendor %q only supports protocol %q", vendor, ProtocolOpenAICompatible)
	}
	if vendor != VendorCustom && protocol != preset.Protocol && protocol != ProtocolOpenAICompatible {
		return Preset{}, fmt.Errorf("provider vendor %q does not support protocol %q", vendor, protocol)
	}
	if protocol == ProtocolOpenAICompatible && vendor != VendorCustom {
		_, hasCompatibleDefault := compatibleBaseURLs[vendor]
		if !hasCompatibleDefault {
			return Preset{}, fmt.Errorf("provider vendor %q does not expose the configured compatible protocol", vendor)
		}
	}

	baseURL := configuredBaseURL
	if baseURL == "" {
		switch {
		case vendor == VendorCustom || vendor == VendorAzure || vendor == VendorBedrock || vendor == VendorCloudflare:
			return Preset{}, fmt.Errorf("provider vendor %q requires base_url", vendor)
		case protocol == ProtocolOpenAICompatible:
			baseURL = compatibleBaseURLs[vendor]
		default:
			baseURL = preset.BaseURL
		}
	}
	if baseURL == "" {
		return Preset{}, fmt.Errorf("provider vendor %q requires base_url for protocol %q", vendor, protocol)
	}
	if _, err := parseBaseURL(baseURL); err != nil {
		return Preset{}, fmt.Errorf("provider vendor %q base_url: %w", vendor, err)
	}
	return Preset{Protocol: protocol, BaseURL: baseURL}, nil
}

func SupportedProtocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case ProtocolOpenAICompatible,
		ProtocolOpenAI,
		ProtocolAnthropic,
		ProtocolGemini,
		ProtocolAzureOpenAI,
		ProtocolDashScope,
		ProtocolCohere,
		ProtocolOllama,
		ProtocolMistral,
		ProtocolDeepSeek,
		ProtocolXAI,
		ProtocolZhipu,
		ProtocolGroq,
		ProtocolNVIDIA,
		ProtocolTogether,
		ProtocolBedrock,
		ProtocolCloudflare:
		return true
	default:
		return false
	}
}
