package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"model-velo/internal/config"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	log.Printf(
		"usage worker started stream=%s group=%s consumer=%s",
		usageConfig.StreamKey,
		usageConfig.Group,
		usageConfig.Consumer,
	)
	if err := worker.Run(ctx); err != nil {
		return err
	}
	stats := worker.Stats()
	pending := int64(-1)
	pendingContext, cancelPending := context.WithTimeout(context.Background(), time.Second)
	if count, pendingErr := worker.Pending(pendingContext); pendingErr == nil {
		pending = count
	}
	cancelPending()
	log.Printf(
		"usage worker stopped read=%d claimed=%d stored=%d duplicates=%d failed=%d pending=%d dead_lettered=%d cleaned=%d",
		stats.Read,
		stats.Claimed,
		stats.Stored,
		stats.Duplicates,
		stats.Failed,
		pending,
		stats.DeadLettered,
		stats.Cleaned,
	)
	return nil
}
