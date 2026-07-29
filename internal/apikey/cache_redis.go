package apikey

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

var errSharedCacheMiss = errors.New("shared authentication cache miss")

type sharedAuthCache interface {
	Load(context.Context, string) ([]byte, error)
	Store(
		context.Context,
		string,
		string,
		string,
		[]byte,
		time.Duration,
	) error
	DeleteValue(context.Context, string) error
	InvalidateKey(context.Context, string) error
	InvalidateTenant(context.Context, string) error
	Publish(context.Context, string, []byte) error
	Listen(context.Context, string, func([]byte)) error
}

type redisAuthCache struct {
	client *goredis.Client
	prefix string
}

func (cache redisAuthCache) Load(
	ctx context.Context,
	cacheKey string,
) ([]byte, error) {
	payload, err := cache.client.Get(ctx, cacheKey).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, errSharedCacheMiss
	}
	return payload, err
}

func (cache redisAuthCache) Store(
	ctx context.Context,
	cacheKey string,
	keyID string,
	tenantID string,
	payload []byte,
	ttl time.Duration,
) error {
	keyIndex := cache.keyIndex(keyID)
	tenantIndex := cache.tenantIndex(tenantID)
	pipeline := cache.client.TxPipeline()
	pipeline.Set(ctx, cacheKey, payload, ttl)
	pipeline.Set(ctx, keyIndex, cacheKey, ttl)
	pipeline.SAdd(ctx, tenantIndex, cacheKey, keyIndex)
	pipeline.Expire(ctx, tenantIndex, ttl)
	_, err := pipeline.Exec(ctx)
	return err
}

func (cache redisAuthCache) DeleteValue(
	ctx context.Context,
	cacheKey string,
) error {
	return cache.client.Del(ctx, cacheKey).Err()
}

func (cache redisAuthCache) InvalidateKey(
	ctx context.Context,
	keyID string,
) error {
	keyIndex := cache.keyIndex(keyID)
	cacheKey, err := cache.client.Get(ctx, keyIndex).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return err
	}
	keys := []string{keyIndex}
	if cacheKey != "" {
		keys = append(keys, cacheKey)
	}
	return cache.client.Del(ctx, keys...).Err()
}

func (cache redisAuthCache) InvalidateTenant(
	ctx context.Context,
	tenantID string,
) error {
	tenantIndex := cache.tenantIndex(tenantID)
	keys, err := cache.client.SMembers(ctx, tenantIndex).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return err
	}
	keys = append(keys, tenantIndex)
	return cache.client.Del(ctx, keys...).Err()
}

func (cache redisAuthCache) Publish(
	ctx context.Context,
	channel string,
	payload []byte,
) error {
	return cache.client.Publish(ctx, channel, payload).Err()
}

func (cache redisAuthCache) Listen(
	ctx context.Context,
	channel string,
	handle func([]byte),
) error {
	subscription := cache.client.Subscribe(ctx, channel)
	defer subscription.Close()
	if _, err := subscription.Receive(ctx); err != nil {
		return err
	}
	for {
		message, err := subscription.ReceiveMessage(ctx)
		if err != nil {
			return err
		}
		handle([]byte(message.Payload))
	}
}

func (cache redisAuthCache) keyIndex(keyID string) string {
	return cache.prefix + ":key:" + keyID
}

func (cache redisAuthCache) tenantIndex(tenantID string) string {
	return cache.prefix + ":tenant:" + tenantID
}
