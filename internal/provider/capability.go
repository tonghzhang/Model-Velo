package provider

import "strings"

// Capability 表示路由阶段可以据此筛选 Provider 的请求能力。
type Capability string

const (
	CapabilityText       Capability = "text"
	CapabilityImage      Capability = "image"
	CapabilityAudio      Capability = "audio"
	CapabilityFile       Capability = "file"
	CapabilityTools      Capability = "tools"
	CapabilityStructured Capability = "structured"
	CapabilityEmbedding  Capability = "embedding"
)

// ProtocolSupportsCapability 判断某种上游协议能否无损承载指定能力。
// 这里描述的是当前 Adapter 已实现的能力，而不是厂商 API 的全部能力。
func ProtocolSupportsCapability(protocol string, capability Capability) bool {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch capability {
	case CapabilityText:
		return SupportedProtocol(protocol)
	case CapabilityImage:
		switch protocol {
		case ProtocolOpenAICompatible,
			ProtocolOpenAI,
			ProtocolAnthropic,
			ProtocolGemini,
			ProtocolCohere,
			ProtocolAzureOpenAI,
			ProtocolOllama,
			ProtocolMistral,
			ProtocolXAI,
			ProtocolZhipu,
			ProtocolGroq,
			ProtocolNVIDIA,
			ProtocolTogether,
			ProtocolBedrock,
			ProtocolCloudflare:
			return true
		}
	case CapabilityAudio:
		switch protocol {
		case ProtocolOpenAICompatible,
			ProtocolOpenAI,
			ProtocolAzureOpenAI,
			ProtocolGemini,
			ProtocolCloudflare:
			return true
		}
	case CapabilityFile:
		switch protocol {
		case ProtocolOpenAICompatible,
			ProtocolOpenAI,
			ProtocolAzureOpenAI,
			ProtocolAnthropic,
			ProtocolGemini,
			ProtocolBedrock,
			ProtocolCloudflare:
			return true
		}
	case CapabilityTools:
		switch protocol {
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
		}
	case CapabilityStructured:
		return SupportedProtocol(protocol)
	case CapabilityEmbedding:
		switch protocol {
		case ProtocolOpenAICompatible,
			ProtocolOpenAI,
			ProtocolAzureOpenAI,
			ProtocolMistral,
			ProtocolXAI,
			ProtocolZhipu,
			ProtocolGroq,
			ProtocolNVIDIA,
			ProtocolTogether,
			ProtocolOllama,
			ProtocolGemini:
			return true
		}
	}
	return false
}

// ProtocolSupportsStream 表示 Adapter 是否能把该协议的增量事件转换成 OpenAI SSE。
func ProtocolSupportsStream(protocol string) bool {
	return SupportedProtocol(strings.ToLower(strings.TrimSpace(protocol)))
}
