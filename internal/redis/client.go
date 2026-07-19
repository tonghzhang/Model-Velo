package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"model-velo/internal/config"
)

var ErrUnavailable = errors.New("Redis is unavailable")

type Client struct {
	native             *goredis.Client
	availableAtStartup bool
	closeOnce          sync.Once
	closeErr           error
}

func Open(ctx context.Context, settings config.Redis) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open Redis: %w", err)
	}

	native := goredis.NewClient(&goredis.Options{
		Addr:                  settings.Address,
		Password:              settings.Password,
		DB:                    settings.DB,
		DialTimeout:           settings.DialTimeout,
		ReadTimeout:           settings.ReadTimeout,
		WriteTimeout:          settings.WriteTimeout,
		PoolSize:              settings.PoolSize,
		MinIdleConns:          settings.MinIdleConns,
		PoolTimeout:           settings.PoolTimeout,
		ContextTimeoutEnabled: true,
	})
	client := &Client{native: native}

	pingContext, cancel := context.WithTimeout(ctx, settings.DialTimeout)
	defer cancel()

	if err := native.Ping(pingContext).Err(); err != nil {
		if ctx.Err() != nil {
			_ = client.Close()
			return nil, fmt.Errorf("ping Redis: %w", ctx.Err())
		}
		if settings.StartupPolicy == config.RedisStartupRequired {
			_ = client.Close()
			return nil, fmt.Errorf("ping Redis: %w", ErrUnavailable)
		}
		return client, nil
	}

	client.availableAtStartup = true
	return client, nil
}

func (client *Client) AvailableAtStartup() bool {
	return client.availableAtStartup
}

func (client *Client) Native() *goredis.Client {
	return client.native
}

func (client *Client) Close() error {
	client.closeOnce.Do(func() {
		client.closeErr = client.native.Close()
	})
	return client.closeErr
}
