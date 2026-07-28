package usage

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Emitter interface {
	// Emit returns a Redis entry ID for immediate delivery. A durable emitter
	// may return an empty ID after the event is safely queued in its outbox.
	Emit(ctx context.Context, event Event) (string, error)
}

type RedisEmitter struct {
	client  *goredis.Client
	stream  string
	timeout time.Duration
}

func NewRedisEmitter(
	client *goredis.Client,
	stream string,
	timeout time.Duration,
) (*RedisEmitter, error) {
	switch {
	case client == nil:
		return nil, errors.New("usage emitter requires Redis")
	case stream == "":
		return nil, errors.New("usage emitter requires a stream key")
	case timeout <= 0:
		return nil, errors.New("usage emitter timeout must be positive")
	}
	return &RedisEmitter{
		client:  client,
		stream:  stream,
		timeout: timeout,
	}, nil
}

func (emitter *RedisEmitter) Emit(ctx context.Context, event Event) (string, error) {
	payload, err := event.Marshal()
	if err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	emitContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.timeout)
	defer cancel()

	return emitter.client.XAdd(emitContext, &goredis.XAddArgs{
		Stream: emitter.stream,
		Values: map[string]any{
			"event_id":       event.EventID,
			"schema_version": event.SchemaVersion,
			"payload":        payload,
		},
	}).Result()
}
