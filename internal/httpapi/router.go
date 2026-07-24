package httpapi // HTTP 接口与 Gin 路由注册包。

import (
	"net/http" // 提供 HTTP 状态码。

	"github.com/gin-gonic/gin" // Gin Web 框架。

	"model-velo/internal/provider"    // Provider Adapter 注册表。
	"model-velo/internal/reliability" // 重试、熔断、队列和 Provider 回退。
	"model-velo/internal/routing"     // 模型路由规则。
	"model-velo/internal/usage"
)

// healthResponse 是健康检查接口返回的 JSON 数据。
type healthResponse struct {
	Status string `json:"status"` // 服务状态，例如 {"status":"ok"}。
}

// NewRouter 创建并装配整个网关的 Gin 路由。
//
// 该函数通常在程序启动阶段由 main 或 newHTTPServer 调用，
// 最终返回给 http.Server 作为请求处理器。
func NewRouter(
	adapters *provider.AdapterRegistry, // Provider ID 与具体 Adapter 的注册表。
	access AccessController, // 负责 API Key 认证和模型授权。
	limiter RateLimiter, // 负责租户请求限流。
	cache ResponseCache, // 负责非流式响应缓存。
	routes *routing.Router, // 负责生成 Provider 候选路由。
	breakers *reliability.BreakerRegistry, // 每个 Provider 的熔断器注册表。
	queues *reliability.QueueRegistry, // 每个 Provider 的并发队列注册表。
	providerKeys *reliability.ProviderKeyRegistry, // 每个 Provider 的上游 API Key 注册表。
	retry reliability.RetryPolicies, // 每个 Provider 的重试策略集合。
	usageEmitter usage.Emitter,
	usageReader UsageReader,
) *gin.Engine {
	// Provider Adapter 注册表不能为空，否则无法调用上游。
	if adapters == nil {
		panic("httpapi: provider adapter registry is nil")
	}

	// 认证和授权服务不能为空。
	if access == nil {
		panic("httpapi: access controller is nil")
	}

	// 限流服务不能为空。
	if limiter == nil {
		panic("httpapi: rate limiter is nil")
	}

	// 响应缓存不能为空。
	if cache == nil {
		panic("httpapi: response cache is nil")
	}

	// 模型路由器不能为空。
	if routes == nil {
		panic("httpapi: routing is nil")
	}

	// 熔断器注册表不能为空。
	if breakers == nil {
		panic("httpapi: circuit breaker registry is nil")
	}

	// Provider 并发队列注册表不能为空。
	if queues == nil {
		panic("httpapi: provider queue registry is nil")
	}

	// 重试策略不能为空。
	if retry == nil {
		panic("httpapi: retry policy is nil")
	}
	if usageEmitter == nil {
		panic("httpapi: usage emitter is nil")
	}
	if usageReader == nil {
		panic("httpapi: usage reader is nil")
	}

	// 创建单个 Provider 内部的请求执行器。
	//
	// attempts 负责：
	// 熔断检查 → 排队 → 选择 Key → 调用 Adapter → Provider 内重试。
	attempts, err := reliability.NewAttemptExecutor(
		adapters,
		breakers,
		queues,
		providerKeys,
		retry,
	)
	if err != nil {
		// 启动阶段依赖装配失败，直接终止程序。
		panic("httpapi: create attempt executor: " + err.Error())
	}

	// 创建 Provider 候选调度器。
	//
	// orchestrator 负责遍历路由候选，
	// 当前 Provider 失败后决定是否切换到下一个 Provider。
	orchestrator, err := reliability.NewOrchestrator(attempts, retry)
	if err != nil {
		// 无法创建上游调度器时终止启动。
		panic("httpapi: create fallback orchestrator: " + err.Error())
	}

	// 创建一个不自带默认中间件的 Gin Engine。
	router := gin.New()

	// 注册异常恢复中间件，防止单个请求 panic 导致整个进程退出。
	router.Use(gin.Recovery())

	// 为每个请求生成或读取 Request ID，并写入请求上下文。
	router.Use(requestIDMiddleware())

	// 注册无需认证的健康检查接口。
	router.GET("/healthz", health)

	// 创建以 /v1 为前缀的路由组。
	protected := router.Group("/v1")

	// 为 /v1 下的接口注册身份认证中间件。
	//
	// 客户端必须提供有效的网关 API Key，
	// 认证结果会写入请求 Context。
	protected.Use(authenticationMiddleware(access))

	// 注册聊天接口：
	// POST /v1/chat/completions
	protected.POST(
		"/chat/completions",

		// 创建 chatHandler，并注入处理请求所需的服务。
		chatHandler{
			access:       access,       // 检查租户是否允许使用请求模型。
			limiter:      limiter,      // 检查租户请求额度。
			cache:        cache,        // 查询或写入响应缓存。
			routes:       routes,       // 生成 Provider 候选计划。
			orchestrator: orchestrator, // 执行重试、熔断和 Provider 回退。
			usageEmitter: usageEmitter,
		}.complete, // 将 complete 方法注册为聊天接口处理函数。
	)
	usageQueries := usageQueryHandler{reader: usageReader}
	protected.GET("/usage/events", usageQueries.list)
	protected.GET("/usage/summary", usageQueries.summary)
	protected.GET("/usage/series", usageQueries.series)

	// 返回组装完成的 Gin 路由器，交给 http.Server 使用。
	return router
}

// health 处理 GET /healthz 健康检查请求。
func health(c *gin.Context) {
	// 返回 HTTP 200 和 {"status":"ok"}。
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}
