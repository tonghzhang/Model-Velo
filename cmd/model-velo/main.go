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
	upstreamBaseURLEnv     = "MODEL_VELO_UPSTREAM_BASE_URL"
	upstreamAPIKeyEnv      = "MODEL_VELO_UPSTREAM_API_KEY"
	upstreamTimeoutEnv     = "MODEL_VELO_UPSTREAM_TIMEOUT"
	shutdownTimeoutEnv     = "MODEL_VELO_SHUTDOWN_TIMEOUT"
	defaultHTTPAddress     = ":8080"
	defaultUpstreamTimeout = 30 * time.Second
	defaultShutdownTimeout = 10 * time.Second
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

	server, err := newHTTPServerWithRouting(access, limiter, cache, startup.routing, startup.breaker, startup.queues)
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
	breaker         *reliability.Breaker
	queues          *reliability.QueueRegistry
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
	routingDefinition, err := config.LoadRouting(responseCache.RouteVersion)
	if err != nil {
		return startupConfig{}, err
	}
	routes, err := routing.New(routingDefinition)
	if err != nil {
		return startupConfig{}, fmt.Errorf("configure routing: %w", err)
	}
	breakerConfig, err := config.LoadCircuitBreaker()
	if err != nil {
		return startupConfig{}, err
	}
	breaker, err := reliability.NewBreaker(routingDefinition.Providers[0].ID, breakerConfig)
	if err != nil {
		return startupConfig{}, fmt.Errorf("configure circuit breaker: %w", err)
	}
	queueConfig, err := config.LoadProviderQueue()
	if err != nil {
		return startupConfig{}, err
	}
	providerIDs := make([]string, 0, len(routingDefinition.Providers))
	for _, configuredProvider := range routingDefinition.Providers {
		providerIDs = append(providerIDs, configuredProvider.ID)
	}
	queues, err := reliability.NewQueueRegistry(providerIDs, queueConfig)
	if err != nil {
		return startupConfig{}, fmt.Errorf("configure provider queue: %w", err)
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
		breaker:         breaker,
		queues:          queues,
		shutdownTimeout: shutdownTimeout,
	}, nil
}

func newHTTPServer(
	access httpapi.AccessController,
	limiter httpapi.RateLimiter,
	cache httpapi.ResponseCache,
) (*http.Server, error) {
	routes, err := routing.New(routing.SingleProviderDefinition("upstream", "single-provider-v1"))
	if err != nil {
		return nil, fmt.Errorf("configure default routing: %w", err)
	}
	breaker, err := reliability.NewBreaker("upstream", reliability.DefaultBreakerConfig())
	if err != nil {
		return nil, fmt.Errorf("configure default circuit breaker: %w", err)
	}
	queues, err := reliability.NewQueueRegistry([]string{"upstream"}, reliability.DefaultQueueConfig())
	if err != nil {
		return nil, fmt.Errorf("configure default provider queue: %w", err)
	}
	return newHTTPServerWithRouting(access, limiter, cache, routes, breaker, queues)
}

func newHTTPServerWithRouting(
	access httpapi.AccessController,
	limiter httpapi.RateLimiter,
	cache httpapi.ResponseCache,
	routes *routing.Router,
	breaker *reliability.Breaker,
	queues *reliability.QueueRegistry,
) (*http.Server, error) {
	address := strings.TrimSpace(os.Getenv(httpAddressEnv))
	if address == "" {
		address = defaultHTTPAddress
	}

	upstreamTimeout, err := loadUpstreamTimeout()
	if err != nil {
		return nil, err
	}

	upstreamClient, err := provider.NewClient(
		os.Getenv(upstreamBaseURLEnv),
		os.Getenv(upstreamAPIKeyEnv),
		upstreamTimeout,
	)
	if err != nil {
		return nil, fmt.Errorf("configure upstream provider: %w", err)
	}

	return &http.Server{
		Addr:              address,
		Handler:           httpapi.NewRouterWithReliability(upstreamClient, access, limiter, cache, routes, breaker, queues),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}, nil
}

func loadUpstreamTimeout() (time.Duration, error) {
	return loadPositiveDuration(upstreamTimeoutEnv, defaultUpstreamTimeout)
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
