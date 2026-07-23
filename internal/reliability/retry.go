package reliability

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	maximumRetryAttempts  = 10
	minimumRetryBackoff   = 10 * time.Millisecond
	maximumRetryBackoff   = 30 * time.Second
	minimumRequestTimeout = time.Second
	maximumRequestTimeout = 5 * time.Minute
	minimumAttemptTimeout = 100 * time.Millisecond
)

type RetryConfig struct {
	MaxAttempts       int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
	JitterRatio       float64
	RequestTimeout    time.Duration
	AttemptTimeout    time.Duration
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        2 * time.Second,
		BackoffMultiplier: 2,
		JitterRatio:       0.2,
		RequestTimeout:    45 * time.Second,
		AttemptTimeout:    20 * time.Second,
	}
}

func (config RetryConfig) Validate() error {
	if config.MaxAttempts < 1 || config.MaxAttempts > maximumRetryAttempts {
		return fmt.Errorf("retry max attempts must be between 1 and %d", maximumRetryAttempts)
	}
	if config.InitialBackoff < minimumRetryBackoff || config.InitialBackoff > maximumRetryBackoff {
		return fmt.Errorf("retry initial backoff must be between %s and %s", minimumRetryBackoff, maximumRetryBackoff)
	}
	if config.MaxBackoff < config.InitialBackoff || config.MaxBackoff > maximumRetryBackoff {
		return fmt.Errorf("retry max backoff must be between initial backoff and %s", maximumRetryBackoff)
	}
	if config.BackoffMultiplier < 1 || config.BackoffMultiplier > 10 {
		return errors.New("retry backoff multiplier must be between 1 and 10")
	}
	if config.JitterRatio < 0 || config.JitterRatio > 1 {
		return errors.New("retry jitter ratio must be between 0 and 1")
	}
	if config.RequestTimeout < minimumRequestTimeout || config.RequestTimeout > maximumRequestTimeout {
		return fmt.Errorf("request timeout must be between %s and %s", minimumRequestTimeout, maximumRequestTimeout)
	}
	if config.AttemptTimeout < minimumAttemptTimeout || config.AttemptTimeout > config.RequestTimeout {
		return errors.New("attempt timeout must be at least 100ms and no greater than request timeout")
	}
	return nil
}

type RetryPolicy struct {
	config RetryConfig
	random func() float64
}

type RetryPolicies interface {
	ForProvider(providerID string) *RetryPolicy
	RequestContext(ctx context.Context) (context.Context, context.CancelFunc)
	RequestTimeout() time.Duration
}

func NewRetryPolicy(config RetryConfig) (*RetryPolicy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &RetryPolicy{config: config, random: rand.Float64}, nil
}

func (policy *RetryPolicy) ForProvider(string) *RetryPolicy {
	return policy
}

func (policy *RetryPolicy) MaxAttempts() int {
	return policy.config.MaxAttempts
}

func (policy *RetryPolicy) RequestTimeout() time.Duration {
	if policy == nil {
		return 0
	}
	return policy.config.RequestTimeout
}

func (policy *RetryPolicy) AttemptTimeout() time.Duration {
	if policy == nil {
		return 0
	}
	return policy.config.AttemptTimeout
}

func (policy *RetryPolicy) RequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, policy.config.RequestTimeout)
}

func (policy *RetryPolicy) AttemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, policy.config.AttemptTimeout)
}

func (policy *RetryPolicy) ShouldRetry(failure *Failure, attempt int) bool {
	if failure == nil || attempt >= policy.config.MaxAttempts {
		return false
	}
	signals := SignalsFor(failure)
	return signals.Retry || signals.SwitchKey
}

func (policy *RetryPolicy) Backoff(failure *Failure, attempt int) time.Duration {
	if failure == nil || SignalsFor(failure).SwitchKey {
		return 0
	}
	if failure.Category == CategoryKeyExhausted {
		return failure.RetryAfter
	}

	delay := float64(policy.config.InitialBackoff)
	maxBackoff := float64(policy.config.MaxBackoff)
	for step := 1; step < attempt; step++ {
		delay *= policy.config.BackoffMultiplier
		if delay >= maxBackoff {
			delay = maxBackoff
			break
		}
	}

	jitter := 1 + (2*policy.random()-1)*policy.config.JitterRatio
	delay *= jitter
	if delay > maxBackoff {
		return policy.config.MaxBackoff
	}
	return time.Duration(delay)
}

func (policy *RetryPolicy) Wait(ctx context.Context, delay time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if delay <= 0 {
		return true
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return false
	}

	timer := time.NewTimer(delay)
	defer stopRetryTimer(timer)
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func stopRetryTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
