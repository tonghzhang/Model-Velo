package provider

import (
	"fmt"
	"strings"
)

const (
	// ProtocolOpenAICompatible 用于用户自定义的 OpenAI Chat Completions 兼容端点。
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
	// VendorCustom 表示没有内置地址、必须显式提供 base_url 的兼容厂商。
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

// Preset 是厂商解析后的协议和基础地址，不包含密钥或运行时状态。
type Preset struct {
	Protocol string
	BaseURL  string
}

// presets 保存每个厂商推荐的原生协议和默认公开地址。
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

// compatibleBaseURLs 仅列出官方提供 OpenAI 兼容入口的厂商。
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

// Resolve 校验 vendor、protocol 和 base_url 的组合，并补齐可信的默认地址。
// 自定义、Azure、Bedrock 和 Cloudflare 的地址包含部署信息，因此不猜测默认值。
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

// SupportedProtocol 判断工厂是否有对应的协议实现。
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
