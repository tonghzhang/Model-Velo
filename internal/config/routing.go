package config // 配置加载包。

import (
	"encoding/json" // 解析路由 JSON 配置。
	"errors"        // 创建和判断错误。
	"fmt"           // 生成包含配置位置的错误信息。
	"io"            // 判断 JSON 是否已经读取到结尾。
	"os"            // 读取环境变量。
	"strings"       // 清理配置字符串两侧空格。
	"time"          // 解析和保存超时配置。

	"model-velo/internal/provider"    // Provider 能力、协议和 HTTP 配置。
	"model-velo/internal/reliability" // 熔断、队列和重试配置。
	"model-velo/internal/routing"     // 路由定义、Provider 和候选目标。
)

const (
	routingJSONEnv          = "MODEL_VELO_ROUTING_JSON" // 保存完整路由 JSON 的环境变量。
	maximumRoutingJSONBytes = 64 << 10                  // 路由 JSON 最大允许 64 KiB。
)

type routingJSON struct { // 环境变量中完整路由 JSON 的顶层结构。
	Providers []routingProviderJSON `json:"providers"` // Provider 配置列表。
	Routes    []routingRuleJSON     `json:"routes"`    // 虚拟模型到候选 Provider 的路由列表。
}

type routingProviderJSON struct { // 单个 Provider 的 JSON 配置。
	ID                string                           `json:"id"`                 // 网关内部 Provider ID。
	Type              string                           `json:"type"`               // 手动指定的 Provider 协议类型。
	Vendor            string                           `json:"vendor"`             // Provider 厂商名称。
	BaseURL           string                           `json:"base_url"`           // 上游 API 基础地址。
	Models            []string                         `json:"models"`             // 该 Provider 可提供的模型列表。
	ModelCapabilities map[string][]provider.Capability `json:"model_capabilities"` // 每个模型支持的能力。
	Runtime           providerRuntimeJSON              `json:"runtime"`            // 当前 Provider 的运行参数覆盖配置。
}

type providerRuntimeJSON struct { // 单个 Provider 可覆盖的运行配置。
	Breaker breakerOverrideJSON `json:"breaker"` // 熔断配置覆盖值。
	Queue   queueOverrideJSON   `json:"queue"`   // 并发队列配置覆盖值。
	Retry   retryOverrideJSON   `json:"retry"`   // 重试配置覆盖值。
	HTTP    httpOverrideJSON    `json:"http"`    // HTTP 连接池配置覆盖值。
}

type breakerOverrideJSON struct { // Provider 熔断器的可选覆盖配置。
	FailureThreshold  *int   `json:"failure_threshold"`    // 连续失败阈值；nil 表示使用默认值。
	OpenDuration      string `json:"open_duration"`        // 熔断打开持续时间。
	HalfOpenMaxProbes *int   `json:"half_open_max_probes"` // 半开状态最大探测数；nil 表示默认值。
}

type queueOverrideJSON struct { // Provider 并发队列的可选覆盖配置。
	MaxInFlight *int   `json:"max_in_flight"` // 最大同时执行请求数。
	MaxWaiting  *int   `json:"max_waiting"`   // 最大排队请求数。
	WaitTimeout string `json:"wait_timeout"`  // 排队最大等待时间。
}

type retryOverrideJSON struct { // Provider 重试策略的可选覆盖配置。
	MaxAttempts       *int     `json:"max_attempts"`       // 最大尝试次数。
	InitialBackoff    string   `json:"initial_backoff"`    // 第一次重试等待时间。
	MaxBackoff        string   `json:"max_backoff"`        // 最大退避时间。
	BackoffMultiplier *float64 `json:"backoff_multiplier"` // 每次退避时间的增长倍数。
	JitterRatio       *float64 `json:"jitter_ratio"`       // 随机抖动比例。
	AttemptTimeout    string   `json:"attempt_timeout"`    // 单次上游调用超时。
}

type httpOverrideJSON struct { // Provider HTTP 连接池的可选覆盖配置。
	MaxIdleConnections        *int `json:"max_idle_connections"`          // 所有主机合计最大空闲连接数。
	MaxIdleConnectionsPerHost *int `json:"max_idle_connections_per_host"` // 每个主机最大空闲连接数。
	MaxConnectionsPerHost     *int `json:"max_connections_per_host"`      // 每个主机最大总连接数。
}

type ProviderDefaults struct { // 所有 Provider 默认使用的运行配置。
	Breaker reliability.BreakerConfig // 默认熔断配置。
	Queue   reliability.QueueConfig   // 默认并发队列配置。
	Retry   reliability.RetryConfig   // 默认重试配置。
	HTTP    provider.HTTPConfig       // 默认 HTTP 连接配置。
}

type ProviderRuntime struct { // 合并默认值和覆盖值后的 Provider 最终运行配置。
	Breaker reliability.BreakerConfig // 最终熔断配置。
	Queue   reliability.QueueConfig   // 最终并发队列配置。
	Retry   reliability.RetryConfig   // 最终重试配置。
	HTTP    provider.HTTPConfig       // 最终 HTTP 连接配置。
}

type Routing struct { // LoadRouting 返回给启动代码的完整路由配置。
	Definition routing.Definition         // Provider 和路由规则定义，交给 routing.New。
	Providers  map[string]ProviderRuntime // Provider ID 到最终运行配置的映射。
}

type routingRuleJSON struct { // 单条虚拟模型路由 JSON。
	Model      string                 `json:"model"`      // 客户端请求使用的模型名。
	Candidates []routingCandidateJSON `json:"candidates"` // 按顺序尝试的候选 Provider。
}

type routingCandidateJSON struct { // 单个候选 Provider JSON。
	Provider      string `json:"provider"`       // 候选 Provider ID。
	UpstreamModel string `json:"upstream_model"` // 实际发送给该 Provider 的模型名。
}

func LoadRouting(defaults ProviderDefaults) (Routing, error) { // 读取路由 JSON，合并默认配置并生成运行时 Routing。
	if err := validateProviderDefaults(defaults); err != nil { // 先检查外部传入的默认配置是否合法。
		return Routing{}, err // 默认配置错误时立即停止。
	}
	raw := strings.TrimSpace(os.Getenv(routingJSONEnv)) // 读取并清理路由环境变量。
	if raw == "" {                                      // 路由配置不能为空。
		return Routing{}, fmt.Errorf("%s is required", routingJSONEnv)
	}
	if len(raw) > maximumRoutingJSONBytes { // 防止环境变量内容过大。
		return Routing{}, fmt.Errorf("%s exceeds %d bytes", routingJSONEnv, maximumRoutingJSONBytes)
	}

	var document routingJSON                           // 保存解析后的顶层路由文档。
	decoder := json.NewDecoder(strings.NewReader(raw)) // 创建从环境变量字符串读取 JSON 的解码器。
	decoder.DisallowUnknownFields()                    // JSON 出现未定义字段时直接报错。
	if err := decoder.Decode(&document); err != nil {  // 解析第一份路由 JSON 对象。
		return Routing{}, fmt.Errorf("%s must be a valid routing object: %w", routingJSONEnv, err)
	}
	if err := rejectTrailingJSON(decoder); err != nil { // 检查对象后面是否还存在多余 JSON。
		return Routing{}, fmt.Errorf("%s must contain one routing object: %w", routingJSONEnv, err)
	}
	definition := routing.Definition{}                                           // 创建将交给 routing.New 的路由定义。
	providerRuntime := make(map[string]ProviderRuntime, len(document.Providers)) // 创建 Provider ID 到运行配置的映射。
	for index, configuredProvider := range document.Providers {                  // 逐个处理 JSON 中配置的 Provider。
		preset, err := provider.Resolve( // 根据 vendor、type 和 base_url 解析最终协议预设。
			configuredProvider.Vendor,  // 厂商名称。
			configuredProvider.Type,    // 手动指定的协议类型。
			configuredProvider.BaseURL, // 手动指定的上游地址。
		)
		if err != nil { // Provider 身份或协议配置无法解析。
			return Routing{}, fmt.Errorf("%s provider %d: %w", routingJSONEnv, index, err)
		}
		providerID := strings.TrimSpace(configuredProvider.ID)                                   // 清理 Provider ID 两侧空格。
		runtime, err := resolveProviderRuntime(providerID, configuredProvider.Runtime, defaults) // 合并该 Provider 的默认配置和覆盖值。
		if err != nil {                                                                          // Provider 运行配置不合法。
			return Routing{}, fmt.Errorf("%s provider %d runtime: %w", routingJSONEnv, index, err)
		}
		definition.Providers = append(definition.Providers, routing.Provider{ // 将当前 Provider 加入路由定义。
			ID:                providerID,                           // 使用清理后的 Provider ID。
			Type:              preset.Protocol,                      // 使用 Resolve 得到的最终协议。
			BaseURL:           preset.BaseURL,                       // 使用 Resolve 得到的最终上游地址。
			Models:            configuredProvider.Models,            // 保存该 Provider 的模型列表。
			ModelCapabilities: configuredProvider.ModelCapabilities, // 保存各模型支持的能力。
		})
		providerRuntime[providerID] = runtime // 保存当前 Provider 的最终运行配置。
	}
	for _, configuredRoute := range document.Routes { // 逐条处理虚拟模型路由。
		rule := routing.Rule{Model: configuredRoute.Model}               // 创建当前模型对应的路由规则。
		for _, configuredCandidate := range configuredRoute.Candidates { // 按配置顺序处理候选 Provider。
			rule.Candidates = append(rule.Candidates, routing.Target{ // 将候选目标加入当前规则。
				ProviderID:    configuredCandidate.Provider,      // 指向的 Provider ID。
				UpstreamModel: configuredCandidate.UpstreamModel, // 实际上游模型名。
			})
		}
		definition.Rules = append(definition.Rules, rule) // 将完整规则加入路由定义。
	}

	return Routing{Definition: definition, Providers: providerRuntime}, nil // 返回路由定义和各 Provider 运行配置。
}

func validateProviderDefaults(defaults ProviderDefaults) error { // 检查所有 Provider 默认配置是否合法。
	if err := defaults.Breaker.Validate(); err != nil { // 校验默认熔断配置。
		return fmt.Errorf("invalid default circuit breaker configuration: %w", err)
	}
	if err := defaults.Queue.Validate(); err != nil { // 校验默认队列配置。
		return fmt.Errorf("invalid default provider queue configuration: %w", err)
	}
	if err := defaults.Retry.Validate(); err != nil { // 校验默认重试配置。
		return fmt.Errorf("invalid default retry configuration: %w", err)
	}
	if err := defaults.HTTP.Validate(); err != nil { // 校验默认 HTTP 配置。
		return fmt.Errorf("invalid default provider HTTP configuration: %w", err)
	}
	return nil // 所有默认配置合法。
}

func resolveProviderRuntime( // 合并某个 Provider 的默认配置和 JSON 覆盖配置。
	providerID string, // 当前 Provider ID，用于生成具体错误信息。
	override providerRuntimeJSON, // JSON 中为该 Provider 配置的覆盖值。
	defaults ProviderDefaults, // 所有 Provider 共用的默认配置。
) (ProviderRuntime, error) {
	configured := ProviderRuntime{ // 先复制一份完整默认配置。
		Breaker: defaults.Breaker, // 使用默认熔断配置。
		Queue:   defaults.Queue,   // 使用默认队列配置。
		Retry:   defaults.Retry,   // 使用默认重试配置。
		HTTP:    defaults.HTTP,    // 使用默认 HTTP 配置。
	}
	if override.Breaker.FailureThreshold != nil { // JSON 明确配置了失败阈值。
		configured.Breaker.FailureThreshold = *override.Breaker.FailureThreshold // 覆盖默认失败阈值。
	}
	if override.Breaker.HalfOpenMaxProbes != nil { // JSON 明确配置了半开探测数。
		configured.Breaker.HalfOpenMaxProbes = *override.Breaker.HalfOpenMaxProbes // 覆盖默认探测数。
	}
	if override.Queue.MaxInFlight != nil { // JSON 明确配置了最大并发数。
		configured.Queue.MaxInFlight = *override.Queue.MaxInFlight // 覆盖默认最大并发数。
	}
	if override.Queue.MaxWaiting != nil { // JSON 明确配置了最大排队数。
		configured.Queue.MaxWaiting = *override.Queue.MaxWaiting // 覆盖默认最大排队数。
	}
	// 队列决定实际并发上限；没有显式配置 HTTP 连接池时，让连接池跟随队列并发数。
	// 这样不会允许 HTTP 连接数量明显超过网关实际允许的上游并发数量。
	configured.HTTP.MaxConnectionsPerHost = configured.Queue.MaxInFlight     // 每个主机最大连接数默认等于最大并发数。
	configured.HTTP.MaxIdleConnectionsPerHost = configured.Queue.MaxInFlight // 每个主机最大空闲连接数默认等于最大并发数。
	if configured.HTTP.MaxIdleConnections < configured.Queue.MaxInFlight {   // 全局空闲连接数不能小于单个 Provider 并发数。
		configured.HTTP.MaxIdleConnections = configured.Queue.MaxInFlight // 提高全局空闲连接上限。
	}
	if override.Retry.MaxAttempts != nil { // JSON 明确配置了最大尝试次数。
		configured.Retry.MaxAttempts = *override.Retry.MaxAttempts // 覆盖默认最大尝试次数。
	}
	if override.Retry.BackoffMultiplier != nil { // JSON 明确配置了退避倍数。
		configured.Retry.BackoffMultiplier = *override.Retry.BackoffMultiplier // 覆盖默认退避倍数。
	}
	if override.Retry.JitterRatio != nil { // JSON 明确配置了随机抖动比例。
		configured.Retry.JitterRatio = *override.Retry.JitterRatio // 覆盖默认抖动比例。
	}
	if override.HTTP.MaxIdleConnections != nil { // JSON 明确配置了全局最大空闲连接数。
		configured.HTTP.MaxIdleConnections = *override.HTTP.MaxIdleConnections // 覆盖自动计算值。
	}
	if override.HTTP.MaxIdleConnectionsPerHost != nil { // JSON 明确配置了每个主机最大空闲连接数。
		configured.HTTP.MaxIdleConnectionsPerHost = *override.HTTP.MaxIdleConnectionsPerHost // 覆盖自动计算值。
	}
	if override.HTTP.MaxConnectionsPerHost != nil { // JSON 明确配置了每个主机最大总连接数。
		configured.HTTP.MaxConnectionsPerHost = *override.HTTP.MaxConnectionsPerHost                                                             // 覆盖自动计算值。
		if override.HTTP.MaxIdleConnectionsPerHost == nil && configured.HTTP.MaxIdleConnectionsPerHost > configured.HTTP.MaxConnectionsPerHost { // 未单独设置空闲连接且空闲数超过总连接数。
			configured.HTTP.MaxIdleConnectionsPerHost = configured.HTTP.MaxConnectionsPerHost // 将空闲连接上限压到总连接上限。
		}
	}

	var err error                                                                                                                                                // 复用同一个错误变量解析多个时间配置。
	configured.Breaker.OpenDuration, err = durationOverride(providerID+" breaker open_duration", override.Breaker.OpenDuration, configured.Breaker.OpenDuration) // 解析或保留熔断持续时间。
	if err != nil {                                                                                                                                              // 熔断持续时间格式错误。
		return ProviderRuntime{}, err
	}
	configured.Queue.WaitTimeout, err = durationOverride(providerID+" queue wait_timeout", override.Queue.WaitTimeout, configured.Queue.WaitTimeout) // 解析或保留排队等待超时。
	if err != nil {                                                                                                                                  // 排队等待时间格式错误。
		return ProviderRuntime{}, err
	}
	configured.Retry.InitialBackoff, err = durationOverride(providerID+" retry initial_backoff", override.Retry.InitialBackoff, configured.Retry.InitialBackoff) // 解析或保留初始退避时间。
	if err != nil {                                                                                                                                              // 初始退避时间格式错误。
		return ProviderRuntime{}, err
	}
	configured.Retry.MaxBackoff, err = durationOverride(providerID+" retry max_backoff", override.Retry.MaxBackoff, configured.Retry.MaxBackoff) // 解析或保留最大退避时间。
	if err != nil {                                                                                                                              // 最大退避时间格式错误。
		return ProviderRuntime{}, err
	}
	configured.Retry.AttemptTimeout, err = durationOverride(providerID+" retry attempt_timeout", override.Retry.AttemptTimeout, configured.Retry.AttemptTimeout) // 解析或保留单次尝试超时。
	if err != nil {                                                                                                                                              // 单次尝试超时格式错误。
		return ProviderRuntime{}, err
	}

	if err := configured.Breaker.Validate(); err != nil { // 校验合并后的熔断配置。
		return ProviderRuntime{}, err
	}
	if err := configured.Queue.Validate(); err != nil { // 校验合并后的队列配置。
		return ProviderRuntime{}, err
	}
	if err := configured.Retry.Validate(); err != nil { // 校验合并后的重试配置。
		return ProviderRuntime{}, err
	}
	if err := configured.HTTP.Validate(); err != nil { // 校验合并后的 HTTP 配置。
		return ProviderRuntime{}, err
	}
	return configured, nil // 返回当前 Provider 的最终运行配置。
}

func durationOverride(name, raw string, fallback time.Duration) (time.Duration, error) { // 解析可选时间字符串，未配置时使用默认值。
	raw = strings.TrimSpace(raw) // 清理时间字符串两侧空格。
	if raw == "" {               // JSON 没有配置该时间值。
		return fallback, nil // 保留默认时间。
	}
	value, err := time.ParseDuration(raw) // 解析 30s、2m 等 Go 时间格式。
	if err != nil || value <= 0 {         // 格式错误或时间不为正数。
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil // 返回解析后的合法时间。
}

func rejectTrailingJSON(decoder *json.Decoder) error { // 检查首个 JSON 对象后是否还有多余 JSON 值。
	var extra json.RawMessage     // 用于接收可能存在的第二个 JSON 值。
	err := decoder.Decode(&extra) // 尝试继续读取下一份 JSON。
	if errors.Is(err, io.EOF) {   // 已经到文件结尾，说明没有多余内容。
		return nil
	}
	if err != nil { // 后续内容本身不是合法 JSON。
		return err
	}
	return errors.New("unexpected trailing JSON value") // 成功读到第二个值，说明配置包含多余 JSON。
}
