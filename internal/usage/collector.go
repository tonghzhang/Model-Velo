package usage

import (
	"sync"
	"time"
)

type Collector struct {
	mu        sync.Mutex
	event     Event
	finalized bool
}

func NewCollector(input NewEventInput) (*Collector, error) {
	event, err := newEvent(input)
	if err != nil {
		return nil, err
	}
	return &Collector{event: event}, nil
}

func (collector *Collector) SetCacheStatus(status string) {
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if !collector.finalized && status != "" {
		collector.event.CacheStatus = status
	}
}

func (collector *Collector) SetRoute(
	providerID string,
	upstreamModel string,
	attempts int,
	retries int,
	fallbacks int,
) {
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.finalized {
		return
	}
	collector.event.ProviderID = providerID
	collector.event.UpstreamModel = upstreamModel
	collector.event.Attempts = attempts
	collector.event.Retries = retries
	collector.event.Fallbacks = fallbacks
}

func (collector *Collector) ObserveResponse(payload []byte) {
	if collector == nil {
		return
	}
	collector.observe(payload, false, time.Time{})
}

func (collector *Collector) ObserveStreamResponse(payload []byte, observedAt time.Time) {
	if collector == nil {
		return
	}
	collector.observe(payload, true, observedAt)
}

func (collector *Collector) observe(payload []byte, stream bool, observedAt time.Time) {
	metadata := parseResponseMetadata(payload)
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.finalized {
		return
	}
	if metadata.usage != nil {
		collector.event.Usage = metadata.usage
		collector.event.UsageCaveat = metadata.usageCaveat
	} else if metadata.usageCaveat != "" && collector.event.Usage == nil {
		collector.event.UsageCaveat = metadata.usageCaveat
	}
	if metadata.finishReason != "" {
		collector.event.FinishReason = metadata.finishReason
	}
	if stream && metadata.hasChunk && collector.event.FirstTokenMS == nil {
		if observedAt.IsZero() {
			observedAt = time.Now().UTC()
		} else {
			observedAt = observedAt.UTC()
		}
		firstTokenMS := observedAt.Sub(collector.event.StartedAt).Milliseconds()
		if firstTokenMS < 0 {
			firstTokenMS = 0
		}
		collector.event.FirstTokenMS = &firstTokenMS
	}
}

func (collector *Collector) Finalize(outcome Outcome) (Event, bool) {
	if collector == nil {
		return Event{}, false
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.finalized {
		return Event{}, false
	}

	endedAt := outcome.EndedAt.UTC()
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	if endedAt.Before(collector.event.StartedAt) {
		endedAt = collector.event.StartedAt
	}
	collector.event.Status = outcome.Status
	collector.event.ErrorCategory = outcome.ErrorCategory
	collector.event.ErrorCode = outcome.ErrorCode
	collector.event.EndedAt = endedAt
	collector.event.LatencyMS = endedAt.Sub(collector.event.StartedAt).Milliseconds()
	collector.event.UsageSource = inferredUsageSource(collector.event)
	if collector.event.Usage == nil && collector.event.UsageCaveat == "" {
		collector.event.UsageCaveat = "provider_usage_unavailable"
	}
	collector.finalized = true
	return collector.event, true
}
