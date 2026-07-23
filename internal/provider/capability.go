package provider

import "strings"

// Capability 表示路由阶段可以据此筛选 Provider 的请求能力。
type Capability string

const (
	// 三种能力是当前路由层能够判定并筛选的最小集合。
	CapabilityText  Capability = "text"
	CapabilityImage Capability = "image"
	CapabilityTools Capability = "tools"
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
			ProtocolAzureOpenAI,
			ProtocolOllama,
			ProtocolMistral,
			ProtocolXAI,
			ProtocolZhipu,
			ProtocolGroq,
			ProtocolNVIDIA,
			ProtocolTogether,
			ProtocolBedrock:
			return true
		}
	case CapabilityTools:
		switch protocol {
		case ProtocolOpenAICompatible,
			ProtocolOpenAI,
			ProtocolAzureOpenAI,
			ProtocolMistral,
			ProtocolDeepSeek,
			ProtocolXAI,
			ProtocolZhipu,
			ProtocolGroq,
			ProtocolNVIDIA,
			ProtocolTogether:
			return true
		}
	}
	return false
}
