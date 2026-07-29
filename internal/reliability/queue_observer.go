package reliability

import (
	"context"
	"time"
)

type QueueObserver interface {
	ObserveQueueWait(provider, result string, duration time.Duration)
}

type queueObserverContextKey struct{}

func WithQueueObserver(ctx context.Context, observer QueueObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, queueObserverContextKey{}, observer)
}

func observeQueueWait(
	ctx context.Context,
	provider, result string,
	duration time.Duration,
) {
	if ctx == nil {
		return
	}
	observer, _ := ctx.Value(queueObserverContextKey{}).(QueueObserver)
	if observer != nil {
		observer.ObserveQueueWait(provider, result, duration)
	}
}
