package reliability

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	minimumOpenDuration = time.Second
	maximumOpenDuration = 10 * time.Minute
	maximumFailures     = 1_000
	maximumProbes       = 100
)

type BreakerConfig struct {
	FailureThreshold  int
	OpenDuration      time.Duration
	HalfOpenMaxProbes int
}

func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureThreshold:  5,
		OpenDuration:      30 * time.Second,
		HalfOpenMaxProbes: 1,
	}
}

func (config BreakerConfig) Validate() error {
	if config.FailureThreshold < 1 || config.FailureThreshold > maximumFailures {
		return fmt.Errorf("breaker failure threshold must be between 1 and %d", maximumFailures)
	}
	if config.OpenDuration < minimumOpenDuration || config.OpenDuration > maximumOpenDuration {
		return fmt.Errorf("breaker open duration must be between %s and %s", minimumOpenDuration, maximumOpenDuration)
	}
	if config.HalfOpenMaxProbes < 1 || config.HalfOpenMaxProbes > maximumProbes {
		return fmt.Errorf("breaker half-open probes must be between 1 and %d", maximumProbes)
	}
	return nil
}

type BreakerState string

const (
	StateClosed   BreakerState = "closed"
	StateOpen     BreakerState = "open"
	StateHalfOpen BreakerState = "half-open"
)

type BreakerSnapshot struct {
	ProviderID          string
	State               BreakerState
	ConsecutiveFailures int
	OpenedUntil         time.Time
	HalfOpenInFlight    int
	HalfOpenSuccesses   int
	Rejected            uint64
}

type Breaker struct {
	mu sync.Mutex

	providerID string
	config     BreakerConfig
	now        func() time.Time

	state               BreakerState
	generation          uint64
	consecutiveFailures int
	openedUntil         time.Time
	halfOpenInFlight    int
	halfOpenSuccesses   int
	rejected            uint64
}

func NewBreaker(providerID string, config BreakerConfig) (*Breaker, error) {
	return NewBreakerWithClock(providerID, config, time.Now)
}

func NewBreakerWithClock(providerID string, config BreakerConfig, now func() time.Time) (*Breaker, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return nil, errors.New("breaker provider ID is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		return nil, errors.New("breaker clock is required")
	}
	return &Breaker{
		providerID: providerID,
		config:     config,
		now:        now,
		state:      StateClosed,
	}, nil
}

func (breaker *Breaker) Allow() (*Permit, *Failure) {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()

	now := breaker.now().UTC()
	breaker.advance(now)
	switch breaker.state {
	case StateClosed:
		return &Permit{
			breaker:    breaker,
			generation: breaker.generation,
		}, nil
	case StateHalfOpen:
		if breaker.halfOpenInFlight < breaker.config.HalfOpenMaxProbes {
			breaker.halfOpenInFlight++
			return &Permit{
				breaker:    breaker,
				generation: breaker.generation,
				probe:      true,
			}, nil
		}
	}

	breaker.rejected++
	retryAfter := breaker.openedUntil.Sub(now)
	if retryAfter < 0 {
		retryAfter = 0
	}
	return nil, &Failure{
		Category:   CategoryBreaker,
		ProviderID: breaker.providerID,
		RetryAfter: retryAfter,
	}
}

func (breaker *Breaker) Snapshot() BreakerSnapshot {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()

	breaker.advance(breaker.now().UTC())
	return BreakerSnapshot{
		ProviderID:          breaker.providerID,
		State:               breaker.state,
		ConsecutiveFailures: breaker.consecutiveFailures,
		OpenedUntil:         breaker.openedUntil,
		HalfOpenInFlight:    breaker.halfOpenInFlight,
		HalfOpenSuccesses:   breaker.halfOpenSuccesses,
		Rejected:            breaker.rejected,
	}
}

type Permit struct {
	breaker    *Breaker
	generation uint64
	probe      bool
	completed  atomic.Bool
}

func (permit *Permit) Complete(failure *Failure) bool {
	if permit == nil || permit.breaker == nil || !permit.completed.CompareAndSwap(false, true) {
		return false
	}
	permit.breaker.complete(permit, failure)
	return true
}

func (permit *Permit) Abandon() bool {
	if permit == nil || permit.breaker == nil || !permit.completed.CompareAndSwap(false, true) {
		return false
	}
	permit.breaker.abandon(permit)
	return true
}

func (breaker *Breaker) complete(permit *Permit, failure *Failure) {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()

	if permit.generation != breaker.generation {
		return
	}
	if permit.probe {
		breaker.completeProbe(failure)
		return
	}
	if breaker.state != StateClosed {
		return
	}
	if failure == nil {
		breaker.consecutiveFailures = 0
		return
	}
	if !SignalsFor(failure).CountBreaker {
		return
	}

	breaker.consecutiveFailures++
	if breaker.consecutiveFailures >= breaker.config.FailureThreshold {
		breaker.open(breaker.now().UTC())
	}
}

func (breaker *Breaker) completeProbe(failure *Failure) {
	if breaker.state != StateHalfOpen {
		return
	}
	if breaker.halfOpenInFlight > 0 {
		breaker.halfOpenInFlight--
	}
	if failure == nil {
		breaker.halfOpenSuccesses++
		if breaker.halfOpenSuccesses >= breaker.config.HalfOpenMaxProbes {
			breaker.close()
		}
		return
	}
	if SignalsFor(failure).CountBreaker {
		breaker.open(breaker.now().UTC())
	}
}

func (breaker *Breaker) abandon(permit *Permit) {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()

	if permit.generation == breaker.generation && permit.probe && breaker.state == StateHalfOpen && breaker.halfOpenInFlight > 0 {
		breaker.halfOpenInFlight--
	}
}

func (breaker *Breaker) advance(now time.Time) {
	if breaker.state != StateOpen || now.Before(breaker.openedUntil) {
		return
	}
	breaker.state = StateHalfOpen
	breaker.generation++
	breaker.halfOpenInFlight = 0
	breaker.halfOpenSuccesses = 0
}

func (breaker *Breaker) open(now time.Time) {
	breaker.state = StateOpen
	breaker.generation++
	breaker.consecutiveFailures = breaker.config.FailureThreshold
	breaker.openedUntil = now.Add(breaker.config.OpenDuration)
	breaker.halfOpenInFlight = 0
	breaker.halfOpenSuccesses = 0
}

func (breaker *Breaker) close() {
	breaker.state = StateClosed
	breaker.generation++
	breaker.consecutiveFailures = 0
	breaker.openedUntil = time.Time{}
	breaker.halfOpenInFlight = 0
	breaker.halfOpenSuccesses = 0
}
