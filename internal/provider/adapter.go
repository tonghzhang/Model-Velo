package provider

import "context"

// Authentication 表示 Adapter 对上游凭据的要求。
type Authentication uint8

const (
	// AuthenticationAPIKey 表示调用上游前必须选择一个 Provider Key。
	AuthenticationAPIKey Authentication = iota
	// AuthenticationNone 用于 Ollama 等不要求网关携带密钥的本地服务。
	AuthenticationNone
)

// ChatInput 是可靠性层交给 Adapter 的厂商无关请求。
// ModelOverride 为空时保留客户端请求中的原始模型。
type ChatInput struct {
	RequestID     string
	Request       ChatRequest
	ModelOverride string
}

// Adapter 负责上游协议转换和单次网络调用。
// Retry、Fallback、限流和熔断由 reliability 包统一管理。
type Adapter interface {
	Authentication() Authentication
	Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error)
}

// StreamingAdapter 是支持增量 Chat 输出的可选能力。
// 建流只负责单次上游调用；首事件前的 Retry/Fallback 仍由 reliability 包持有。
type StreamingAdapter interface {
	Adapter
	OpenStream(ctx context.Context, input ChatInput, apiKey string) (*ChatEventStream, error)
}
