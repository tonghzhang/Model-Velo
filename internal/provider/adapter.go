package provider

import (
	"context"
	"strings"
)

type Capability string

const (
	CapabilityText  Capability = "text"
	CapabilityImage Capability = "image"
	CapabilityTools Capability = "tools"
)

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

type Authentication uint8

const (
	AuthenticationAPIKey Authentication = iota
	AuthenticationNone
)

// ChatInput is the provider-neutral request passed by the reliability layer.
// ModelOverride is empty when the original request model should be preserved.
type ChatInput struct {
	RequestID     string
	Request       ChatRequest
	ModelOverride string
}

// Adapter translates one provider-neutral chat request to an upstream protocol.
// Retry and fallback remain the responsibility of the reliability layer.
type Adapter interface {
	Authentication() Authentication
	Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error)
}
