package reliability

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	minimumQueueWaitTimeout = 10 * time.Millisecond
	maximumQueueWaitTimeout = time.Minute
	maximumQueueInFlight    = 10_000
	maximumQueueWaiting     = 100_000
)

type QueueReason string

const (
	QueueFull            QueueReason = "queue_full"
	QueueWaitTimeout     QueueReason = "queue_timeout"
	QueueUnknownProvider QueueReason = "queue_unknown_provider"
)

type QueueConfig struct {
	MaxInFlight int
	MaxWaiting  int
	WaitTimeout time.Duration
}

func DefaultQueueConfig() QueueConfig {
	return QueueConfig{
		MaxInFlight: 20,
		MaxWaiting:  100,
		WaitTimeout: 2 * time.Second,
	}
}

func (config QueueConfig) Validate() error {
	if config.MaxInFlight < 1 || config.MaxInFlight > maximumQueueInFlight {
		return fmt.Errorf("queue max in-flight must be between 1 and %d", maximumQueueInFlight)
	}
	if config.MaxWaiting < 0 || config.MaxWaiting > maximumQueueWaiting {
		return fmt.Errorf("queue max waiting must be between 0 and %d", maximumQueueWaiting)
	}
	if config.WaitTimeout < minimumQueueWaitTimeout || config.WaitTimeout > maximumQueueWaitTimeout {
		return fmt.Errorf("queue wait timeout must be between %s and %s", minimumQueueWaitTimeout, maximumQueueWaitTimeout)
	}
	return nil
}

type QueueSnapshot struct {
	ProviderID  string
	Active      int64
	Waiting     int64
	MaxInFlight int
	MaxWaiting  int
	Rejected    uint64
	TimedOut    uint64
	Canceled    uint64
}

type ProviderQueue struct {
	providerID string
	config     QueueConfig
	slots      chan struct{}

	active   atomic.Int64
	waiting  atomic.Int64
	rejected atomic.Uint64
	timedOut atomic.Uint64
	canceled atomic.Uint64
}

func NewProviderQueue(providerID string, config QueueConfig) (*ProviderQueue, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return nil, errors.New("queue provider ID is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &ProviderQueue{
		providerID: providerID,
		config:     config,
		slots:      make(chan struct{}, config.MaxInFlight),
	}, nil
}

func (queue *ProviderQueue) Acquire(ctx context.Context) (*QueueLease, *Failure) {
	if ctx == nil {
		return nil, &Failure{Category: CategoryCanceled, ProviderID: queue.providerID, Cause: errors.New("queue context is nil")}
	}
	if failure := queue.contextFailure(ctx); failure != nil {
		return nil, failure
	}
	if lease := queue.tryAcquire(); lease != nil {
		return lease, nil
	}
	if !queue.reserveWaiting() {
		queue.rejected.Add(1)
		return nil, queue.failure(QueueFull)
	}
	defer queue.waiting.Add(-1)

	timer := time.NewTimer(queue.config.WaitTimeout)
	defer stopQueueTimer(timer)

	select {
	case queue.slots <- struct{}{}:
		if failure := queue.contextFailure(ctx); failure != nil {
			<-queue.slots
			return nil, failure
		}
		queue.active.Add(1)
		return &QueueLease{queue: queue}, nil
	case <-timer.C:
		queue.timedOut.Add(1)
		return nil, queue.failure(QueueWaitTimeout)
	case <-ctx.Done():
		return nil, queue.contextFailure(ctx)
	}
}

func (queue *ProviderQueue) Snapshot() QueueSnapshot {
	return QueueSnapshot{
		ProviderID:  queue.providerID,
		Active:      queue.active.Load(),
		Waiting:     queue.waiting.Load(),
		MaxInFlight: queue.config.MaxInFlight,
		MaxWaiting:  queue.config.MaxWaiting,
		Rejected:    queue.rejected.Load(),
		TimedOut:    queue.timedOut.Load(),
		Canceled:    queue.canceled.Load(),
	}
}

func (queue *ProviderQueue) tryAcquire() *QueueLease {
	select {
	case queue.slots <- struct{}{}:
		queue.active.Add(1)
		return &QueueLease{queue: queue}
	default:
		return nil
	}
}

func (queue *ProviderQueue) reserveWaiting() bool {
	limit := int64(queue.config.MaxWaiting)
	for {
		current := queue.waiting.Load()
		if current >= limit {
			return false
		}
		if queue.waiting.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (queue *ProviderQueue) contextFailure(ctx context.Context) *Failure {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Failure{
			Category:   CategoryTimeout,
			ProviderID: queue.providerID,
			Timeout:    TimeoutRequestBudget,
			Cause:      err,
		}
	}
	queue.canceled.Add(1)
	return &Failure{Category: CategoryCanceled, ProviderID: queue.providerID, Cause: err}
}

func (queue *ProviderQueue) failure(reason QueueReason) *Failure {
	return &Failure{Category: CategoryQueue, ProviderID: queue.providerID, Queue: reason}
}

type QueueLease struct {
	queue    *ProviderQueue
	released atomic.Bool
}

func (lease *QueueLease) Release() bool {
	if lease == nil || lease.queue == nil || !lease.released.CompareAndSwap(false, true) {
		return false
	}
	lease.queue.active.Add(-1)
	<-lease.queue.slots
	return true
}

type QueueRegistry struct {
	queues map[string]*ProviderQueue
	ids    []string
}

func NewQueueRegistry(providerIDs []string, config QueueConfig) (*QueueRegistry, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	configs := make(map[string]QueueConfig, len(providerIDs))
	for _, providerID := range providerIDs {
		configs[strings.TrimSpace(providerID)] = config
	}
	return NewQueueRegistryWithConfigs(providerIDs, configs)
}

func NewQueueRegistryWithConfigs(
	providerIDs []string,
	configs map[string]QueueConfig,
) (*QueueRegistry, error) {
	registry := &QueueRegistry{queues: make(map[string]*ProviderQueue, len(providerIDs))}
	for index, providerID := range providerIDs {
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			return nil, fmt.Errorf("queue provider ID at index %d is empty", index)
		}
		if _, exists := registry.queues[providerID]; exists {
			return nil, fmt.Errorf("queue provider ID %q is duplicated", providerID)
		}
		config, ok := configs[providerID]
		if !ok {
			return nil, fmt.Errorf("queue config for provider %q is missing", providerID)
		}
		queue, err := NewProviderQueue(providerID, config)
		if err != nil {
			return nil, err
		}
		registry.queues[providerID] = queue
		registry.ids = append(registry.ids, providerID)
	}
	if len(registry.queues) == 0 {
		return nil, errors.New("queue registry requires at least one provider")
	}
	sort.Strings(registry.ids)
	return registry, nil
}

func (registry *QueueRegistry) Acquire(ctx context.Context, providerID string) (*QueueLease, *Failure) {
	providerID = strings.TrimSpace(providerID)
	queue := registry.queues[providerID]
	if queue == nil {
		return nil, &Failure{
			Category:   CategoryQueue,
			ProviderID: providerID,
			Queue:      QueueUnknownProvider,
		}
	}
	return queue.Acquire(ctx)
}

func (registry *QueueRegistry) Snapshot(providerID string) (QueueSnapshot, bool) {
	queue := registry.queues[strings.TrimSpace(providerID)]
	if queue == nil {
		return QueueSnapshot{}, false
	}
	return queue.Snapshot(), true
}

func (registry *QueueRegistry) Snapshots() []QueueSnapshot {
	snapshots := make([]QueueSnapshot, 0, len(registry.ids))
	for _, providerID := range registry.ids {
		snapshots = append(snapshots, registry.queues[providerID].Snapshot())
	}
	return snapshots
}

// ReuseProvider keeps in-flight leases and waiting requests coordinated across
// runtime snapshots when the provider queue configuration is unchanged.
func (registry *QueueRegistry) ReuseProvider(
	providerID string,
	previous *QueueRegistry,
) bool {
	providerID = strings.TrimSpace(providerID)
	if registry == nil || previous == nil {
		return false
	}
	current := registry.queues[providerID]
	existing := previous.queues[providerID]
	if current == nil || existing == nil || current.config != existing.config {
		return false
	}
	registry.queues[providerID] = existing
	return true
}

func stopQueueTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
