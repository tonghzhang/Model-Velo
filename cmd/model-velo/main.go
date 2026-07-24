package main // 程序启动入口包。

import (
	"context"   // 传递启动取消、请求结束和关闭超时信号。
	"errors"    // 创建、判断和合并错误。
	"fmt"       // 包装带上下文的错误信息。
	"log"       // 输出启动日志和致命错误。
	"net"       // 创建 TCP 监听器。
	"net/http"  // 创建和运行 HTTP 服务器。
	"os"        // 读取环境变量和系统中断信号。
	"os/signal" // 将退出信号转换为 Context 取消信号。
	"strings"   // 清理环境变量中的空格。
	"syscall"   // 提供 SIGTERM 系统信号。
	"time"      // 配置超时和持续时间。

	"model-velo/internal/apikey"           // 创建网关 API Key 认证与授权服务。
	"model-velo/internal/config"           // 加载数据库、Redis、路由等配置。
	"model-velo/internal/httpapi"          // 创建 Gin HTTP 路由。
	"model-velo/internal/postgres"         // 连接 PostgreSQL 并同步表结构。
	"model-velo/internal/provider"         // 创建各 Provider 的 Adapter。
	"model-velo/internal/ratelimit"        // 创建租户限流器。
	redisstore "model-velo/internal/redis" // 连接 Redis，别名避免与其他 redis 名称冲突。
	"model-velo/internal/reliability"      // 创建熔断、队列、Key 和重试组件。
	"model-velo/internal/responsecache"    // 创建模型响应缓存。
	"model-velo/internal/routing"          // 创建模型路由器。
	"model-velo/internal/usage"
)

const (
	httpAddressEnv         = "MODEL_VELO_HTTP_ADDR"        // HTTP 监听地址环境变量。
	shutdownTimeoutEnv     = "MODEL_VELO_SHUTDOWN_TIMEOUT" // 优雅关闭超时环境变量。
	defaultHTTPAddress     = ":8080"                       // 默认监听所有网卡的 8080 端口。
	defaultShutdownTimeout = 10 * time.Second              // 默认最多等待 10 秒完成关闭。
	responseWriteGrace     = 15 * time.Second              // 在请求总超时后额外预留响应写出时间。
)

func main() { // Go 程序的唯一入口。
	if err := run(); err != nil { // 执行完整启动流程并检查最终错误。
		log.Fatalf("run Model-Velo: %v", err) // 输出错误并以非零状态退出进程。
	}
}

func run() error { // 装配基础设施、启动 HTTP 服务并等待关闭。
	startup, err := loadStartupConfig() // 加载配置并创建路由、Adapter、熔断器等启动组件。
	if err != nil {                     // 配置加载或组件创建失败。
		return fmt.Errorf("configure infrastructure: %w", err) // 补充启动阶段错误上下文。
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) // 收到 Ctrl+C 或 SIGTERM 时取消 ctx。
	defer stop()                                                                           // run 结束时释放系统信号监听资源。

	database, err := postgres.Open(ctx, startup.infrastructure.Postgres) // 使用配置连接 PostgreSQL。
	if err != nil {                                                      // 数据库连接失败。
		return fmt.Errorf("connect PostgreSQL: %w", err) // 终止启动并返回数据库错误。
	}
	defer database.Close() // run 结束时关闭数据库连接池。

	if err := database.SyncSchema(ctx); err != nil { // 将数据库表结构同步到程序要求的版本。
		return err // 表结构同步失败时终止启动。
	}

	access, err := apikey.NewManager(database.ORM(), startup.apiKeySecurity.Pepper) // 创建基于数据库的 API Key 认证授权服务。
	if err != nil {                                                                 // API Key 管理器创建失败。
		return fmt.Errorf("configure API key manager: %w", err) // 返回认证组件配置错误。
	}

	redisClient, err := redisstore.Open(ctx, startup.infrastructure.Redis) // 使用配置连接 Redis。
	if err != nil {                                                        // Redis 客户端创建失败。
		return fmt.Errorf("connect Redis: %w", err) // 终止启动并返回 Redis 错误。
	}
	defer redisClient.Close()              // run 结束时关闭 Redis 客户端。
	if !redisClient.AvailableAtStartup() { // 启动时 Redis Ping 未成功。
		log.Printf("Redis startup Ping failed; continuing because startup policy is optional") // 记录警告但按可选策略继续启动。
	}
	limiter, err := ratelimit.New(redisClient.Native(), startup.rateLimit) // 基于原生 Redis 客户端创建限流器。
	if err != nil {                                                        // 限流器配置无效。
		return fmt.Errorf("configure rate limiter: %w", err) // 返回限流组件配置错误。
	}
	cache, err := responsecache.New( // 创建基于 Redis 的非流式响应缓存。
		redisClient.Native(),               // 传入底层 Redis 客户端。
		startup.rateLimit.Environment,      // 将运行环境写入缓存键，隔离不同环境。
		startup.responseCache.RouteVersion, // 将路由版本写入缓存键，避免路由变更后误命中。
		startup.responseCache.TTL,          // 设置缓存有效时间。
	)
	if err != nil { // 响应缓存配置无效。
		return fmt.Errorf("configure response cache: %w", err) // 返回缓存组件配置错误。
	}
	usageEmitter, err := usage.NewRedisEmitter(
		redisClient.Native(),
		startup.usage.StreamKey,
		startup.usage.EmitTimeout,
	)
	if err != nil {
		return fmt.Errorf("configure usage emitter: %w", err)
	}
	usagePricing, err := usage.NewPricingCatalog(startup.usage.Pricing)
	if err != nil {
		return fmt.Errorf("configure usage pricing: %w", err)
	}
	usageStore, err := usage.NewStore(database.ORM(), usagePricing)
	if err != nil {
		return fmt.Errorf("configure usage store: %w", err)
	}

	server, err := newHTTPServer(access, limiter, cache, startup.routing, startup.adapters, startup.breakers, startup.queues, startup.providerKeys, startup.retry, usageEmitter, usageStore) // 将所有服务装配成 HTTP Server。
	if err != nil {                                                                                                                                                                          // HTTP Server 配置失败。
		return fmt.Errorf("configure HTTP server: %w", err) // 返回 HTTP 装配错误。
	}

	if err := runHTTPServer(ctx, server, startup.shutdownTimeout); err != nil { // 监听端口并在退出信号到达后优雅关闭。
		return fmt.Errorf("run HTTP server: %w", err) // 返回监听或关闭阶段错误。
	}

	return nil // 服务正常关闭。
}

type startupConfig struct { // 保存启动后续步骤需要的全部配置和已创建组件。
	infrastructure  config.Infrastructure // PostgreSQL、Redis 等基础设施配置。
	apiKeySecurity  config.APIKeySecurity // API Key 哈希 Pepper 等安全配置。
	rateLimit       config.RateLimit      // 租户限流配置。
	responseCache   config.ResponseCache  // 响应缓存配置。
	usage           config.Usage
	routing         *routing.Router                  // 已创建的模型路由器。
	adapters        *provider.AdapterRegistry        // 已创建的 Provider Adapter 注册表。
	breakers        *reliability.BreakerRegistry     // 已创建的 Provider 熔断器注册表。
	queues          *reliability.QueueRegistry       // 已创建的 Provider 并发队列注册表。
	providerKeys    *reliability.ProviderKeyRegistry // 已创建的上游 API Key 注册表，无需 Key 时可为 nil。
	retry           *reliability.RetryRegistry       // 已创建的 Provider 重试策略注册表。
	shutdownTimeout time.Duration                    // HTTP Server 优雅关闭最大等待时间。
}

func loadStartupConfig() (startupConfig, error) { // 加载所有启动配置并创建 Provider 运行组件。
	infrastructure, err := config.LoadInfrastructure() // 读取 PostgreSQL 和 Redis 配置。
	if err != nil {                                    // 基础设施配置不合法。
		return startupConfig{}, err // 返回空配置和错误。
	}

	apiKeySecurity, err := config.LoadAPIKeySecurity() // 读取 API Key 安全配置。
	if err != nil {                                    // API Key 安全配置不合法。
		return startupConfig{}, err
	}
	rateLimit, err := config.LoadRateLimit() // 读取租户限流配置。
	if err != nil {                          // 限流配置不合法。
		return startupConfig{}, err
	}
	responseCache, err := config.LoadResponseCache() // 读取响应缓存配置。
	if err != nil {                                  // 缓存配置不合法。
		return startupConfig{}, err
	}
	usageConfig, err := config.LoadUsage()
	if err != nil {
		return startupConfig{}, err
	}
	breakerConfig, err := config.LoadCircuitBreaker() // 读取所有 Provider 默认熔断配置。
	if err != nil {                                   // 熔断配置不合法。
		return startupConfig{}, err
	}
	queueConfig, err := config.LoadProviderQueue() // 读取所有 Provider 默认队列配置。
	if err != nil {                                // 队列配置不合法。
		return startupConfig{}, err
	}
	retryConfig, err := config.LoadRetry() // 读取所有 Provider 默认重试配置。
	if err != nil {                        // 重试配置不合法。
		return startupConfig{}, err
	}
	routingConfig, err := config.LoadRouting(config.ProviderDefaults{ // 读取路由文件并把默认配置合并到各 Provider。
		Breaker: breakerConfig,                // Provider 未单独配置时使用的熔断规则。
		Queue:   queueConfig,                  // Provider 未单独配置时使用的队列规则。
		Retry:   retryConfig,                  // Provider 未单独配置时使用的重试规则。
		HTTP:    provider.DefaultHTTPConfig(), // Provider 未单独配置时使用的 HTTP 参数。
	})
	if err != nil { // 路由或 Provider 配置不合法。
		return startupConfig{}, err
	}
	routes, err := routing.New(routingConfig.Definition) // 根据路由定义创建运行时 Router。
	if err != nil {                                      // 路由定义校验失败。
		return startupConfig{}, fmt.Errorf("configure routing: %w", err)
	}
	providerIDs := make([]string, 0, len(routingConfig.Definition.Providers))                    // 收集所有 Provider ID，供可靠性组件创建使用。
	adapterConfigs := make([]provider.AdapterConfig, 0, len(routingConfig.Definition.Providers)) // 收集所有 Adapter 配置。
	breakerConfigs := make(map[string]reliability.BreakerConfig, len(routingConfig.Providers))   // 保存 Provider ID 到熔断配置的映射。
	queueConfigs := make(map[string]reliability.QueueConfig, len(routingConfig.Providers))       // 保存 Provider ID 到队列配置的映射。
	retryConfigs := make(map[string]reliability.RetryConfig, len(routingConfig.Providers))       // 保存 Provider ID 到重试配置的映射。
	for _, configuredProvider := range routingConfig.Definition.Providers {                      // 遍历路由定义中的每个 Provider。
		runtime := routingConfig.Providers[configuredProvider.ID]       // 取得该 Provider 合并默认值后的运行配置。
		providerIDs = append(providerIDs, configuredProvider.ID)        // 保存 Provider ID。
		adapterConfigs = append(adapterConfigs, provider.AdapterConfig{ // 组装该 Provider 的 Adapter 配置。
			ProviderID:         configuredProvider.ID,           // Adapter 对应的 Provider ID。
			Protocol:           configuredProvider.Type,         // Provider 使用的协议类型。
			BaseURL:            configuredProvider.BaseURL,      // Provider 上游接口基础地址。
			HTTP:               runtime.HTTP,                    // Provider 专属 HTTP 超时和连接配置。
			DisableStreamUsage: !usageConfig.EnforceStreamUsage, // 可为不支持 stream_options 的兼容上游关闭强制 Usage。
		})
		breakerConfigs[configuredProvider.ID] = runtime.Breaker // 保存该 Provider 的熔断配置。
		queueConfigs[configuredProvider.ID] = runtime.Queue     // 保存该 Provider 的队列配置。
		retryConfigs[configuredProvider.ID] = runtime.Retry     // 保存该 Provider 的重试配置。
	}
	adapters, err := provider.NewAdapterRegistry(adapterConfigs) // 为所有 Provider 创建 Adapter 注册表。
	if err != nil {                                              // Adapter 配置或协议不合法。
		return startupConfig{}, err
	}
	breakers, err := reliability.NewBreakerRegistryWithConfigs(providerIDs, breakerConfigs) // 为每个 Provider 创建独立熔断器。
	if err != nil {                                                                         // 熔断器注册表创建失败。
		return startupConfig{}, fmt.Errorf("configure circuit breakers: %w", err)
	}
	queues, err := reliability.NewQueueRegistryWithConfigs(providerIDs, queueConfigs) // 为每个 Provider 创建独立并发队列。
	if err != nil {                                                                   // 队列注册表创建失败。
		return startupConfig{}, fmt.Errorf("configure provider queue: %w", err)
	}
	var providerKeys *reliability.ProviderKeyRegistry // 声明上游 API Key 注册表，默认无 Key 时保持 nil。
	keyedProviderIDs := adapters.KeyedProviderIDs()   // 找出所有需要 API Key 鉴权的 Provider。
	if len(keyedProviderIDs) > 0 {                    // 至少有一个 Provider 需要 API Key。
		keySets, err := config.LoadProviderKeys() // 从配置中读取各 Provider 的 API Key 集合。
		if err != nil {                           // Key 配置读取失败。
			return startupConfig{}, err
		}
		providerKeys, err = reliability.NewProviderKeyRegistry(keyedProviderIDs, keySets) // 创建轮询、冷却和切换 Key 的注册表。
		if err != nil {                                                                   // Provider Key 配置不完整或重复。
			return startupConfig{}, fmt.Errorf("configure provider keys: %w", err)
		}
	}
	retry, err := reliability.NewRetryRegistry(providerIDs, retryConfigs) // 为每个 Provider 创建独立重试策略。
	if err != nil {                                                       // 重试注册表创建失败。
		return startupConfig{}, fmt.Errorf("configure retry policy: %w", err)
	}

	shutdownTimeout, err := loadShutdownTimeout() // 读取 HTTP 优雅关闭超时。
	if err != nil {                               // 关闭超时配置不合法。
		return startupConfig{}, err
	}

	return startupConfig{ // 返回启动后续阶段所需的完整配置和组件。
		infrastructure:  infrastructure, // 保存基础设施配置。
		apiKeySecurity:  apiKeySecurity, // 保存 API Key 安全配置。
		rateLimit:       rateLimit,      // 保存限流配置。
		responseCache:   responseCache,  // 保存缓存配置。
		usage:           usageConfig,
		routing:         routes,          // 保存运行时路由器。
		adapters:        adapters,        // 保存 Adapter 注册表。
		breakers:        breakers,        // 保存熔断器注册表。
		queues:          queues,          // 保存队列注册表。
		providerKeys:    providerKeys,    // 保存 Provider Key 注册表。
		retry:           retry,           // 保存重试注册表。
		shutdownTimeout: shutdownTimeout, // 保存优雅关闭超时。
	}, nil
}

func newHTTPServer( // 根据已创建的服务组装 net/http Server。
	access httpapi.AccessController, // API Key 认证与模型授权服务。
	limiter httpapi.RateLimiter, // 租户限流服务。
	cache httpapi.ResponseCache, // 非流式响应缓存。
	routes *routing.Router, // 模型路由器。
	adapters *provider.AdapterRegistry, // Provider Adapter 注册表。
	breakers *reliability.BreakerRegistry, // Provider 熔断器注册表。
	queues *reliability.QueueRegistry, // Provider 并发队列注册表。
	providerKeys *reliability.ProviderKeyRegistry, // Provider 上游 Key 注册表。
	retry reliability.RetryPolicies, // Provider 重试策略接口。
	usageEmitter usage.Emitter,
	usageReader httpapi.UsageReader,
) (*http.Server, error) {
	if retry == nil { // WriteTimeout 和路由执行都依赖重试策略。
		return nil, errors.New("retry policy is required") // 缺少重试策略时拒绝创建 Server。
	}
	address := strings.TrimSpace(os.Getenv(httpAddressEnv)) // 读取 HTTP 监听地址。
	if address == "" {                                      // 环境变量未设置。
		address = defaultHTTPAddress // 使用默认 :8080。
	}

	return &http.Server{ // 创建标准库 HTTP Server。
		Addr:              address,                                                                                                                       // 设置 TCP 监听地址。
		Handler:           httpapi.NewRouter(adapters, access, limiter, cache, routes, breakers, queues, providerKeys, retry, usageEmitter, usageReader), // 创建 Gin 路由并作为 Server 的请求处理器。
		ReadHeaderTimeout: 5 * time.Second,                                                                                                               // 最多等待 5 秒读取完整请求头。
		ReadTimeout:       15 * time.Second,                                                                                                              // 最多等待 15 秒读取完整 HTTP 请求。
		WriteTimeout:      retry.RequestTimeout() + responseWriteGrace,                                                                                   // 给上游请求总超时加上响应写出缓冲时间。
		IdleTimeout:       60 * time.Second,                                                                                                              // 空闲长连接最多保留 60 秒。
	}, nil
}

func loadShutdownTimeout() (time.Duration, error) { // 读取优雅关闭超时配置。
	return loadPositiveDuration(shutdownTimeoutEnv, defaultShutdownTimeout) // 未配置时返回默认 10 秒。
}

func loadPositiveDuration(environmentVariable string, defaultValue time.Duration) (time.Duration, error) { // 从环境变量读取正数时间长度。
	rawTimeout := strings.TrimSpace(os.Getenv(environmentVariable)) // 读取并清理环境变量值。
	if rawTimeout == "" {                                           // 环境变量没有配置。
		return defaultValue, nil // 使用调用方提供的默认值。
	}

	timeout, err := time.ParseDuration(rawTimeout) // 解析 10s、1m 等 Go 时间格式。
	if err != nil || timeout <= 0 {                // 格式错误或结果不是正数。
		return 0, fmt.Errorf("%s must be a positive duration", environmentVariable) // 返回具体变量名对应的配置错误。
	}

	return timeout, nil // 返回合法时间长度。
}

func runHTTPServer(ctx context.Context, server *http.Server, shutdownTimeout time.Duration) error { // 创建 TCP 监听器并运行 HTTP Server。
	if err := ctx.Err(); err != nil { // 启动前已收到退出信号。
		return nil // 不再启动服务，按正常结束处理。
	}
	if shutdownTimeout <= 0 { // 防止调用方传入非法关闭超时。
		return errors.New("shutdown timeout must be positive")
	}

	listener, err := net.Listen("tcp", server.Addr) // 在配置地址上创建 TCP 监听器。
	if err != nil {                                 // 端口被占用或地址无效。
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}

	return serveHTTPServer(ctx, server, listener, shutdownTimeout) // 使用现有监听器启动服务并处理关闭。
}

func serveHTTPServer( // 并发运行 HTTP Server，并等待服务错误或退出信号。
	ctx context.Context, // 收到系统退出信号时会被取消。
	server *http.Server, // 已配置好的 HTTP Server。
	listener net.Listener, // 已绑定端口的 TCP 监听器。
	shutdownTimeout time.Duration, // 优雅关闭最大等待时间。
) error {
	serveErrors := make(chan error, 1) // 接收 Server.Serve 的最终返回值，缓冲避免 goroutine 阻塞。
	go func() {                        // 在独立 goroutine 中运行阻塞式 HTTP 服务。
		serveErrors <- server.Serve(listener) // 接收请求，停止后把结果发送到通道。
	}()

	select { // 等待 HTTP Server 自己退出或系统发出关闭信号。
	case err := <-serveErrors: // Server 在收到关闭信号前异常退出。
		return normalizeServeError(err) // 统一处理 Serve 返回值。
	case <-ctx.Done(): // 收到 Ctrl+C 或 SIGTERM。
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout) // 创建独立的优雅关闭超时 Context。
	defer cancel()                                                                    // 函数结束时释放关闭 Context 资源。

	if err := server.Shutdown(shutdownCtx); err != nil { // 停止接收新请求并等待现有请求完成。
		closeErr := server.Close() // 优雅关闭失败后强制断开所有连接。
		<-serveErrors              // 等待 Serve goroutine 完全退出，避免泄漏。
		if closeErr != nil {       // 强制关闭也失败。
			return errors.Join(fmt.Errorf("shutdown HTTP server: %w", err), fmt.Errorf("force close HTTP server: %w", closeErr)) // 同时返回两个关闭错误。
		}
		return fmt.Errorf("shutdown HTTP server: %w", err) // 强制关闭成功，但保留优雅关闭失败信息。
	}

	return normalizeServeError(<-serveErrors) // 优雅关闭成功后等待 Serve 返回并统一处理结果。
}

func normalizeServeError(err error) error { // 将 HTTP Server 的正常关闭错误转换为 nil。
	if err == nil || errors.Is(err, http.ErrServerClosed) { // nil 或 Shutdown 导致的 ErrServerClosed 都属于正常结束。
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err) // 其他错误表示 HTTP 服务异常中断。
}
