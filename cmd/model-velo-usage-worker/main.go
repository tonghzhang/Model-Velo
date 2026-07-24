package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"model-velo/internal/config"
	"model-velo/internal/health"
	"model-velo/internal/observability"
	"model-velo/internal/postgres"
	redisstore "model-velo/internal/redis"
	"model-velo/internal/usage"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("run Model-Velo usage worker: %v", err)
	}
}

func run() error {
	infrastructure, err := config.LoadInfrastructure()
	if err != nil {
		return fmt.Errorf("configure infrastructure: %w", err)
	}
	usageConfig, err := config.LoadUsage()
	if err != nil {
		return fmt.Errorf("configure usage worker: %w", err)
	}
	observabilityConfig, err := config.LoadObservability()
	if err != nil {
		return fmt.Errorf("configure observability: %w", err)
	}
	logger := observability.NewLogger(observabilityConfig).With("component", "usage-worker")
	slog.SetDefault(logger)
	log.SetFlags(0)
	log.SetOutput(observability.LogWriter(logger, "legacy"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTracing, err := observability.ConfigureTracing(ctx, observabilityConfig)
	if err != nil {
		return fmt.Errorf("configure tracing: %w", err)
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownContext); err != nil {
			logger.Error("trace shutdown failed", "error", err)
		}
	}()

	database, err := postgres.Open(ctx, infrastructure.Postgres)
	if err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	defer database.Close()
	if err := database.SyncSchema(ctx); err != nil {
		return err
	}

	redisClient, err := redisstore.Open(ctx, infrastructure.Redis)
	if err != nil {
		return fmt.Errorf("connect Redis: %w", err)
	}
	defer redisClient.Close()
	if !redisClient.AvailableAtStartup() {
		return errors.New("usage worker requires Redis at startup")
	}

	pricing, err := usage.NewPricingCatalog(usageConfig.Pricing)
	if err != nil {
		return fmt.Errorf("configure usage pricing: %w", err)
	}
	store, err := usage.NewStore(database.ORM(), pricing)
	if err != nil {
		return fmt.Errorf("configure usage store: %w", err)
	}
	worker, err := usage.NewWorker(redisClient.Native(), store, usageConfig)
	if err != nil {
		return fmt.Errorf("configure usage worker: %w", err)
	}
	redisEmitter, err := usage.NewRedisEmitter(
		redisClient.Native(),
		usageConfig.StreamKey,
		usageConfig.EmitTimeout,
	)
	if err != nil {
		return fmt.Errorf("configure usage outbox emitter: %w", err)
	}
	relay, err := usage.NewOutboxRelay(database.ORM(), redisEmitter, usageConfig.WorkerTimeout)
	if err != nil {
		return fmt.Errorf("configure usage outbox relay: %w", err)
	}
	if err := relay.SetPendingTimeout(usageConfig.PendingTimeout); err != nil {
		return fmt.Errorf("configure usage pending recovery: %w", err)
	}
	worker.SetOutboxRelay(relay)
	metrics := observability.NewMetrics()
	if err := metrics.RegisterUsageWorker(worker); err != nil {
		return fmt.Errorf("register usage worker metrics: %w", err)
	}
	statusServer := newWorkerStatusServer(
		observabilityConfig.WorkerMetricsAddr,
		metrics,
		observabilityConfig.MetricsToken,
		health.NewChecker(
			database.SQL(), redisClient.Native(),
			observabilityConfig.ReadinessTimeout,
		),
	)

	logger.Info(
		"usage worker started",
		"stream", usageConfig.StreamKey,
		"group", usageConfig.Group,
		"consumer", usageConfig.Consumer,
		"status_address", observabilityConfig.WorkerMetricsAddr,
	)
	if err := runWorker(ctx, worker, statusServer); err != nil {
		return err
	}
	stats := worker.Stats()
	pending := int64(-1)
	pendingContext, cancelPending := context.WithTimeout(context.Background(), time.Second)
	if count, pendingErr := worker.Pending(pendingContext); pendingErr == nil {
		pending = count
	}
	cancelPending()
	logger.Info(
		"usage worker stopped",
		"read", stats.Read,
		"claimed", stats.Claimed,
		"stored", stats.Stored,
		"duplicates", stats.Duplicates,
		"failed", stats.Failed,
		"pending", pending,
		"dead_lettered", stats.DeadLettered,
		"cleaned", stats.Cleaned,
		"relayed", stats.Relayed,
	)
	return nil
}

type dependencyChecker interface {
	Check(context.Context) map[string]error
}

func newWorkerStatusServer(
	address string,
	metrics *observability.Metrics,
	metricsToken string,
	checker dependencyChecker,
) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		ready := true
		for _, err := range checker.Check(request.Context()) {
			if err != nil {
				ready = false
				break
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		if !ready {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"status":"ready"}`))
	})
	mux.Handle("/metrics", metrics.Handler(metricsToken))
	return &http.Server{
		Addr: address, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func runWorker(
	ctx context.Context,
	worker *usage.Worker,
	statusServer *http.Server,
) error {
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	workerErrors := make(chan error, 1)
	serverErrors := make(chan error, 1)
	go func() {
		workerErrors <- worker.Run(runContext)
	}()
	go func() {
		serverErrors <- statusServer.ListenAndServe()
	}()

	var result error
	workerDone := false
	select {
	case <-ctx.Done():
	case err := <-workerErrors:
		result = err
		workerDone = true
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve usage worker status endpoint: %w", err)
		}
	}
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(
		context.Background(), 5*time.Second,
	)
	defer shutdownCancel()
	if err := statusServer.Shutdown(shutdownContext); err != nil &&
		!errors.Is(err, http.ErrServerClosed) && result == nil {
		result = fmt.Errorf("shutdown usage worker status endpoint: %w", err)
	}
	if !workerDone {
		select {
		case err := <-workerErrors:
			if err != nil && result == nil {
				result = err
			}
		case <-shutdownContext.Done():
			if result == nil {
				result = errors.New("usage worker did not stop before shutdown timeout")
			}
		}
	}
	return result
}
