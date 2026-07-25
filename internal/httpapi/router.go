package httpapi // HTTP 接口与 Gin 路由注册包。

import (
	"net/http" // 提供 HTTP 状态码。

	"github.com/gin-gonic/gin" // Gin Web 框架。

	"model-velo/internal/gateway"
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
	options ...RouterOption,
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

	settings := routerSettings{}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}

	if settings.runtime == nil {
		snapshot, err := gateway.NewSnapshot(
			adapters, routes, breakers, queues, providerKeys, retry,
		)
		if err != nil {
			panic("httpapi: create gateway runtime: " + err.Error())
		}
		manager, err := gateway.NewManager(snapshot)
		if err != nil {
			panic("httpapi: initialize gateway runtime: " + err.Error())
		}
		settings.runtime = manager
	}

	// 创建一个不自带默认中间件的 Gin Engine。
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		panic("httpapi: disable implicit trusted proxies: " + err.Error())
	}

	// 为每个请求生成或读取 Request ID，并写入请求上下文。
	router.Use(requestIDMiddleware())
	router.Use(requestSummaryMiddleware(settings.requestLogger, settings.metrics))

	// Recovery 放在请求汇总中间件内层，让 panic 被转换为 500 后仍能
	// 正确结束 span、写日志并归零 in-flight gauge。
	router.Use(safeRecoveryMiddleware(settings.requestLogger))

	// 注册无需认证的健康检查接口。
	router.GET("/healthz", health)
	router.GET("/readyz", readinessHandler(settings.readiness))
	if settings.metrics != nil {
		router.GET("/metrics", metricsHandler(settings.metrics, settings.metricsToken))
	}

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
			access:       access,  // 检查租户是否允许使用请求模型。
			limiter:      limiter, // 检查租户请求额度。
			cache:        cache,   // 查询或写入响应缓存。
			runtime:      settings.runtime,
			usageEmitter: usageEmitter,
			metrics:      settings.metrics,
			logger:       settings.requestLogger,
			quota:        settings.quota,
		}.complete, // 将 complete 方法注册为聊天接口处理函数。
	)
	protected.POST(
		"/responses",
		chatHandler{
			access:       access,
			limiter:      limiter,
			cache:        cache,
			runtime:      settings.runtime,
			usageEmitter: usageEmitter,
			metrics:      settings.metrics,
			logger:       settings.requestLogger,
			quota:        settings.quota,
		}.responses,
	)
	protected.POST(
		"/messages",
		chatHandler{
			access:       access,
			limiter:      limiter,
			cache:        cache,
			runtime:      settings.runtime,
			usageEmitter: usageEmitter,
			metrics:      settings.metrics,
			logger:       settings.requestLogger,
			quota:        settings.quota,
		}.anthropicMessages,
	)
	protected.POST(
		"/embeddings",
		embeddingHandler{
			access:       access,
			limiter:      limiter,
			cache:        cache,
			runtime:      settings.runtime,
			usageEmitter: usageEmitter,
			metrics:      settings.metrics,
			logger:       settings.requestLogger,
			quota:        settings.quota,
		}.create,
	)
	models := modelHandler{runtime: settings.runtime, access: access}
	protected.GET("/models", models.list)
	protected.GET("/models/:model", models.get)
	usageQueries := usageQueryHandler{reader: usageReader}
	protected.GET("/usage/events", usageQueries.list)
	protected.GET("/usage/summary", usageQueries.summary)
	protected.GET("/usage/series", usageQueries.series)

	if settings.adminAuth != nil && settings.controlPlane != nil {
		platformUsage, ok := usageReader.(PlatformUsageReader)
		if !ok {
			panic("httpapi: platform usage reader is unavailable")
		}
		registerAdminRoutes(
			router, settings.adminAuth, settings.controlPlane, settings.quota,
			settings.tenantAdmin, platformUsage,
		)
	}

	// 返回组装完成的 Gin 路由器，交给 http.Server 使用。
	return router
}

// health 处理 GET /healthz 健康检查请求。
func health(c *gin.Context) {
	// 返回 HTTP 200 和 {"status":"ok"}。
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}
