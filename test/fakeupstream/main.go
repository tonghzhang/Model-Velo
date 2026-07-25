package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultAddress         = ":9000"
	defaultProviderName    = "fake-upstream"
	defaultShutdownTimeout = 5 * time.Second
)

type commandConfig struct {
	address         string
	providerName    string
	scenario        string
	shutdownTimeout time.Duration
}

func main() {
	config := commandConfig{}
	flag.StringVar(
		&config.address,
		"addr",
		defaultAddress,
		"HTTP listen address",
	)
	flag.StringVar(
		&config.providerName,
		"name",
		defaultProviderName,
		"provider name returned by the fake upstream",
	)
	flag.StringVar(
		&config.scenario,
		"scenario",
		"",
		"force one scenario for every chat request",
	)
	flag.DurationVar(
		&config.shutdownTimeout,
		"shutdown-timeout",
		defaultShutdownTimeout,
		"maximum graceful shutdown duration",
	)
	flag.Parse()

	if err := run(config); err != nil {
		log.Fatalf("run fake upstream: %v", err)
	}
}

func run(config commandConfig) error {
	if config.shutdownTimeout <= 0 {
		return errors.New("shutdown timeout must be positive")
	}
	upstream, err := newUpstreamServer(config.providerName, config.scenario)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.address)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", config.address, err)
	}

	server := &http.Server{
		Handler:           upstream.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	slog.Info(
		"fake upstream listening",
		"address",
		listener.Addr().String(),
		"name",
		upstream.providerName,
		"scenario",
		upstream.scenarioOverride,
	)

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)

	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			config.shutdownTimeout,
		)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP during shutdown: %w", err)
		}
		return nil
	}
}
