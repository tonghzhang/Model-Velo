package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"model-velo/internal/apikey"
	"model-velo/internal/config"
	"model-velo/internal/httpapi"
	"model-velo/internal/postgres"
	"model-velo/internal/provider"
	"model-velo/internal/ratelimit"
	redisstore "model-velo/internal/redis"
	"model-velo/internal/reliability"
	"model-velo/internal/responsecache"
	"model-velo/internal/routing"
)

const (
	httpAddressEnv         = "MODEL_VELO_HTTP_ADDR"
	shutdownTimeoutEnv     = "MODEL_VELO_SHUTDOWN_TIMEOUT"
	defaultHTTPAddress     = ":8080"
	defaultShutdownTimeout = 10 * time.Second
	responseWriteGrace     = 15 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("run Model-Velo: %v", err)
	}
}

func run() error {
	startup, err := loadStartupConfig()
	if err != nil {
		return fmt.Errorf("configure infrastructure: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := postgres.Open(ctx, startup.infrastructure.Postgres)
	if err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	defer database.Close()

	if err := database.SyncSchema(ctx); err != nil {
		return err
	}

	access, err := apikey.NewManager(database.ORM(), startup.apiKeySecurity.Pepper)
	if err != nil {
		return fmt.Errorf("configure API key manager: %w", err)
	}

	redisClient, err := redisstore.Open(ctx, startup.infrastructure.Redis)
	if err != nil {
		return fmt.Errorf("connect Redis: %w", err)
	}
	defer redisClient.Close()
	if !redisClient.AvailableAtStartup() {
		log.Printf("Redis startup Ping failed; continuing because startup policy is optional")
	}
	limiter, err := ratelimit.New(redisClient.Native(), startup.rateLimit)
	if err != nil {
		return fmt.Errorf("configure rate limiter: %w", err)
	}
	cache, err := responsecache.New(
		redisClient.Native(),
		startup.rateLimit.Environment,
		startup.responseCache.RouteVersion,
		startup.responseCache.TTL,
	)
	if err != nil {
		return fmt.Errorf("configure response cache: %w", err)
	}

	server, err := newHTTPServer(access, limiter, cache, startup.routing, startup.adapters, startup.breakers, startup.queues, startup.providerKeys, startup.retry)
	if err != nil {
		return fmt.Errorf("configure HTTP server: %w", err)
	}

	if err := runHTTPServer(ctx, server, startup.shutdownTimeout); err != nil {
		return fmt.Errorf("run HTTP server: %w", err)
	}

	return nil
}

type startupConfig struct {
	infrastructure  config.Infrastructure
	apiKeySecurity  config.APIKeySecurity
	rateLimit       config.RateLimit
	responseCache   config.ResponseCache
	routing         *routing.Router
	adapters        *provider.AdapterRegistry
	breakers        *reliability.BreakerRegistry
	queues          *reliability.QueueRegistry
	providerKeys    *reliability.ProviderKeyRegistry
	retry           *reliability.RetryRegistry
	shutdownTimeout time.Duration
}

func loadStartupConfig() (startupConfig, error) {
	infrastructure, err := config.LoadInfrastructure()
	if err != nil {
		return startupConfig{}, err
	}

	apiKeySecurity, err := config.LoadAPIKeySecurity()
	if err != nil {
		return startupConfig{}, err
	}
	rateLimit, err := config.LoadRateLimit()
	if err != nil {
		return startupConfig{}, err
	}
	responseCache, err := config.LoadResponseCache()
	if err != nil {
		return startupConfig{}, err
	}
	breakerConfig, err := config.LoadCircuitBreaker()
	if err != nil {
		return startupConfig{}, err
	}
	queueConfig, err := config.LoadProviderQueue()
	if err != nil {
		return startupConfig{}, err
	}
	retryConfig, err := config.LoadRetry()
	if err != nil {
		return startupConfig{}, err
	}
	routingConfig, err := config.LoadRouting(config.ProviderDefaults{
		Breaker: breakerConfig,
		Queue:   queueConfig,
		Retry:   retryConfig,
		HTTP:    provider.DefaultHTTPConfig(),
	})
	if err != nil {
		return startupConfig{}, err
	}
	routes, err := routing.New(routingConfig.Definition)
	if err != nil {
		return startupConfig{}, fmt.Errorf("configure routing: %w", err)
	}
	providerIDs := make([]string, 0, len(routingConfig.Definition.Providers))
	adapterConfigs := make([]provider.AdapterConfig, 0, len(routingConfig.Definition.Providers))
	breakerConfigs := make(map[string]reliability.BreakerConfig, len(routingConfig.Providers))
	queueConfigs := make(map[string]reliability.QueueConfig, len(routingConfig.Providers))
	retryConfigs := make(map[string]reliability.RetryConfig, len(routingConfig.Providers))
	for _, configuredProvider := range routingConfig.Definition.Providers {
		runtime := routingConfig.Providers[configuredProvider.ID]
		providerIDs = append(providerIDs, configuredProvider.ID)
		adapterConfigs = append(adapterConfigs, provider.AdapterConfig{
			ProviderID: configuredProvider.ID,
			Protocol:   configuredProvider.Type,
			BaseURL:    configuredProvider.BaseURL,
			HTTP:       runtime.HTTP,
		})
		breakerConfigs[configuredProvider.ID] = runtime.Breaker
		queueConfigs[configuredProvider.ID] = runtime.Queue
		retryConfigs[configuredProvider.ID] = runtime.Retry
	}
	adapters, err := provider.NewAdapterRegistry(adapterConfigs)
	if err != nil {
		return startupConfig{}, err
	}
	breakers, err := reliability.NewBreakerRegistryWithConfigs(providerIDs, breakerConfigs)
	if err != nil {
		return startupConfig{}, fmt.Errorf("configure circuit breakers: %w", err)
	}
	queues, err := reliability.NewQueueRegistryWithConfigs(providerIDs, queueConfigs)
	if err != nil {
		return startupConfig{}, fmt.Errorf("configure provider queue: %w", err)
	}
	var providerKeys *reliability.ProviderKeyRegistry
	keyedProviderIDs := adapters.KeyedProviderIDs()
	if len(keyedProviderIDs) > 0 {
		keySets, err := config.LoadProviderKeys()
		if err != nil {
			return startupConfig{}, err
		}
		providerKeys, err = reliability.NewProviderKeyRegistry(keyedProviderIDs, keySets)
		if err != nil {
			return startupConfig{}, fmt.Errorf("configure provider keys: %w", err)
		}
	}
	retry, err := reliability.NewRetryRegistry(providerIDs, retryConfigs)
	if err != nil {
		return startupConfig{}, fmt.Errorf("configure retry policy: %w", err)
	}

	shutdownTimeout, err := loadShutdownTimeout()
	if err != nil {
		return startupConfig{}, err
	}

	return startupConfig{
		infrastructure:  infrastructure,
		apiKeySecurity:  apiKeySecurity,
		rateLimit:       rateLimit,
		responseCache:   responseCache,
		routing:         routes,
		adapters:        adapters,
		breakers:        breakers,
		queues:          queues,
		providerKeys:    providerKeys,
		retry:           retry,
		shutdownTimeout: shutdownTimeout,
	}, nil
}

func newHTTPServer(
	access httpapi.AccessController,
	limiter httpapi.RateLimiter,
	cache httpapi.ResponseCache,
	routes *routing.Router,
	adapters *provider.AdapterRegistry,
	breakers *reliability.BreakerRegistry,
	queues *reliability.QueueRegistry,
	providerKeys *reliability.ProviderKeyRegistry,
	retry reliability.RetryPolicies,
) (*http.Server, error) {
	if retry == nil {
		return nil, errors.New("retry policy is required")
	}
	address := strings.TrimSpace(os.Getenv(httpAddressEnv))
	if address == "" {
		address = defaultHTTPAddress
	}

	return &http.Server{
		Addr:              address,
		Handler:           httpapi.NewRouter(adapters, access, limiter, cache, routes, breakers, queues, providerKeys, retry),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      retry.RequestTimeout() + responseWriteGrace,
		IdleTimeout:       60 * time.Second,
	}, nil
}

func loadShutdownTimeout() (time.Duration, error) {
	return loadPositiveDuration(shutdownTimeoutEnv, defaultShutdownTimeout)
}

func loadPositiveDuration(environmentVariable string, defaultValue time.Duration) (time.Duration, error) {
	rawTimeout := strings.TrimSpace(os.Getenv(environmentVariable))
	if rawTimeout == "" {
		return defaultValue, nil
	}

	timeout, err := time.ParseDuration(rawTimeout)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", environmentVariable)
	}

	return timeout, nil
}

func runHTTPServer(ctx context.Context, server *http.Server, shutdownTimeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if shutdownTimeout <= 0 {
		return errors.New("shutdown timeout must be positive")
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}

	return serveHTTPServer(ctx, server, listener, shutdownTimeout)
}

func serveHTTPServer(
	ctx context.Context,
	server *http.Server,
	listener net.Listener,
	shutdownTimeout time.Duration,
) error {
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		return normalizeServeError(err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		closeErr := server.Close()
		<-serveErrors
		if closeErr != nil {
			return errors.Join(fmt.Errorf("shutdown HTTP server: %w", err), fmt.Errorf("force close HTTP server: %w", closeErr))
		}
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}

	return normalizeServeError(<-serveErrors)
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err)
}
