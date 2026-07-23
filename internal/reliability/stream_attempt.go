package reliability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"model-velo/internal/provider"
)

// PreparedStream 在首个内容事件通过校验后接管上游流及可靠性资源。
// Next 和 Finish 由同一调用方协调；Finish 本身允许被重复调用。
type PreparedStream struct {
	FirstEvent      provider.ChatStreamEvent
	ProviderID      string
	UpstreamModel   string
	KeyID           string
	Candidate       int
	Attempts        int
	Fallbacks       int
	CandidatesTried int
	Trail           []AttemptRecord

	stream   *provider.ChatEventStream
	permit   *Permit
	lease    *QueueLease
	key      *ProviderKeySelection
	cancel   context.CancelFunc
	finished atomic.Bool
	done     bool
}

func (stream *PreparedStream) Next() (provider.ChatStreamEvent, error) {
	if stream == nil || stream.stream == nil || stream.finished.Load() {
		return provider.ChatStreamEvent{}, io.EOF
	}
	event, err := stream.stream.Next()
	if errors.Is(err, io.EOF) && !stream.done {
		return provider.ChatStreamEvent{}, fmt.Errorf("%w: stream ended before done", provider.ErrInvalidStream)
	}
	if err == nil && event.Done {
		stream.done = true
	}
	return event, err
}

// Finish 关闭上游响应，将最终结果回写 Key 和 Breaker，再释放 Queue 槽位。
func (stream *PreparedStream) Finish(failure *Failure) bool {
	if stream == nil || !stream.finished.CompareAndSwap(false, true) {
		return false
	}
	stream.describeFailure(failure)
	if stream.cancel != nil {
		stream.cancel()
	}
	if stream.stream != nil {
		_ = stream.stream.Close()
	}
	stream.key.Complete(failure)
	stream.permit.Complete(failure)
	stream.lease.Release()
	return true
}

// FinishError 将提交后的上游读取错误归入既有可靠性分类并结束当前流。
func (stream *PreparedStream) FinishError(ctx context.Context, err error) *Failure {
	if stream == nil || err == nil {
		return nil
	}
	failure := FromProvider(
		ctx,
		stream.ProviderID,
		stream.Candidate,
		stream.currentAttempt(),
		err,
	)
	stream.Finish(failure)
	return failure
}

// Abort 表示客户端传输或网关主动终止，不把结果归因于 Provider。
func (stream *PreparedStream) Abort(cause error) bool {
	if cause == nil {
		cause = context.Canceled
	}
	return stream.Finish(&Failure{Category: CategoryCanceled, Cause: cause})
}

func (stream *PreparedStream) describeFailure(failure *Failure) {
	if stream == nil || failure == nil {
		return
	}
	failure.ProviderID = stream.ProviderID
	failure.KeyID = stream.KeyID
	failure.Candidate = stream.Candidate
	failure.Attempt = stream.currentAttempt()
	failure.TotalAttempts = stream.Attempts
	failure.Fallbacks = stream.Fallbacks
	failure.Trail = append([]AttemptRecord(nil), stream.Trail...)
}

func (stream *PreparedStream) currentAttempt() int {
	if stream == nil {
		return 0
	}
	if len(stream.Trail) > 0 {
		return stream.Trail[len(stream.Trail)-1].Attempt
	}
	return stream.Attempts
}

// PrepareStream 在单个候选内执行有限 Retry，并在不提交客户端响应的前提下
// 缓冲首个有效内容事件。跨候选 Fallback 仍由 Orchestrator 决定。
func (executor *AttemptExecutor) PrepareStream(
	ctx context.Context,
	input AttemptInput,
) (*PreparedStream, *Failure) {
	result, failure := executor.prepareStream(ctx, ctx, input)
	if failure != nil {
		failure.TotalAttempts = result.attempts
		failure.Trail = append([]AttemptRecord(nil), result.trail...)
	} else {
		result.stream.CandidatesTried = 1
	}
	return result.stream, failure
}

type streamAttemptResult struct {
	stream   *PreparedStream
	keyID    string
	attempts int
	trail    []AttemptRecord
}

func (executor *AttemptExecutor) prepareStream(
	ctx context.Context,
	streamParent context.Context,
	input AttemptInput,
) (streamAttemptResult, *Failure) {
	candidate := input.Candidate
	result := streamAttemptResult{}
	if ctx == nil || streamParent == nil {
		return result, &Failure{
			Category:   CategoryCanceled,
			ProviderID: candidate.ProviderID,
			Candidate:  candidate.Priority,
			Cause:      errors.New("stream attempt context is nil"),
		}
	}
	retry := executor.retries.ForProvider(candidate.ProviderID)
	if retry == nil {
		return result, &Failure{
			Category:   CategoryLocalValidation,
			ProviderID: candidate.ProviderID,
			Candidate:  candidate.Priority,
			Cause:      errors.New("provider retry policy is not configured"),
		}
	}

	preferredKeyID := ""
	excludedKeyIDs := make(map[string]struct{})
	for {
		attempt := result.attempts + 1
		outcome := executor.prepareStreamOnce(
			ctx,
			streamParent,
			input,
			attempt,
			preferredKeyID,
			excludedKeyIDs,
			retry,
		)
		if outcome.KeyID != "" {
			result.keyID = outcome.KeyID
		}
		if outcome.Attempted {
			result.attempts++
			result.trail = append(result.trail, AttemptRecord{
				ProviderID:    candidate.ProviderID,
				UpstreamModel: candidate.UpstreamModel,
				KeyID:         outcome.KeyID,
				Candidate:     candidate.Priority,
				Attempt:       result.attempts,
				Duration:      outcome.Duration,
				Category:      failureCategory(outcome.Failure),
				StatusCode:    failureStatus(outcome.Failure),
			})
		}

		failure := outcome.Failure
		if failure == nil {
			result.stream = outcome.Stream
			result.stream.Attempts = result.attempts
			result.stream.Trail = append([]AttemptRecord(nil), result.trail...)
			return result, nil
		}
		if !outcome.Attempted {
			failure.Attempt = result.attempts
		}
		if failure.KeyID == "" {
			failure.KeyID = result.keyID
		}
		failure.TotalAttempts = result.attempts
		if !retry.ShouldRetry(failure, result.attempts) {
			return result, failure
		}

		if SignalsFor(failure).SwitchKey {
			if outcome.KeyID != "" && (failure.Category == CategoryKeyUnauthorized || failure.Category == CategoryKeyForbidden) {
				excludedKeyIDs[outcome.KeyID] = struct{}{}
			}
			preferredKeyID = ""
		} else if outcome.KeyID != "" {
			preferredKeyID = outcome.KeyID
		}
		if !retry.Wait(ctx, retry.Backoff(failure, result.attempts)) {
			if err := ctx.Err(); err != nil {
				failure = FromProvider(ctx, candidate.ProviderID, candidate.Priority, result.attempts, err)
			}
			failure.TotalAttempts = result.attempts
			return result, failure
		}
	}
}

type streamAttemptOutcome struct {
	Stream    *PreparedStream
	Failure   *Failure
	KeyID     string
	Attempted bool
	Duration  time.Duration
}

func (executor *AttemptExecutor) prepareStreamOnce(
	ctx context.Context,
	streamParent context.Context,
	input AttemptInput,
	attempt int,
	preferredKeyID string,
	excludedKeyIDs map[string]struct{},
	retry *RetryPolicy,
) streamAttemptOutcome {
	candidate := input.Candidate
	adapter, ok := executor.adapters.Adapter(candidate.ProviderID)
	if !ok {
		return streamAttemptOutcome{Failure: &Failure{
			Category:   CategoryLocalValidation,
			ProviderID: candidate.ProviderID,
			Candidate:  candidate.Priority,
			Attempt:    attempt,
			Cause:      provider.ErrUnknownProvider,
		}}
	}
	streamingAdapter, ok := adapter.(provider.StreamingAdapter)
	if !ok {
		return streamAttemptOutcome{Failure: FromProvider(
			ctx,
			candidate.ProviderID,
			candidate.Priority,
			attempt,
			provider.ErrUnsupportedCapability,
		)}
	}

	permit, failure := executor.breakers.Allow(candidate.ProviderID)
	if failure != nil {
		failure.Candidate = candidate.Priority
		failure.Attempt = attempt
		return streamAttemptOutcome{Failure: failure}
	}
	handedOff := false
	defer func() {
		if !handedOff {
			permit.Abandon()
		}
	}()

	lease, failure := executor.queues.Acquire(ctx, candidate.ProviderID)
	if failure != nil {
		failure.Candidate = candidate.Priority
		failure.Attempt = attempt
		permit.Complete(failure)
		return streamAttemptOutcome{Failure: failure}
	}
	defer func() {
		if !handedOff {
			lease.Release()
		}
	}()

	var selectedKey *ProviderKeySelection
	if executor.keys != nil && adapter.Authentication() == provider.AuthenticationAPIKey {
		selectedKey, failure = executor.keys.selectKey(candidate.ProviderID, preferredKeyID, excludedKeyIDs)
		if failure != nil {
			failure.Candidate = candidate.Priority
			failure.Attempt = attempt
			permit.Complete(failure)
			return streamAttemptOutcome{Failure: failure}
		}
	}

	modelOverride := ""
	if candidate.UpstreamModel != input.RequestedModel {
		modelOverride = candidate.UpstreamModel
	}
	apiKey := ""
	if selectedKey != nil {
		apiKey = selectedKey.Secret()
	}

	startedAt := time.Now()
	upstream, firstEvent, cancel, err := openFirstStreamEvent(
		ctx,
		streamParent,
		streamingAdapter,
		provider.ChatInput{
			RequestID:     input.RequestID,
			Request:       input.Request,
			ModelOverride: modelOverride,
		},
		apiKey,
		retry.AttemptTimeout(),
	)
	duration := time.Since(startedAt)
	failure = FromProvider(ctx, candidate.ProviderID, candidate.Priority, attempt, err)
	keyID := ""
	if selectedKey != nil {
		keyID = selectedKey.KeyID()
		if failure != nil {
			failure.KeyID = keyID
		}
	}
	if failure != nil {
		selectedKey.Complete(failure)
		permit.Complete(failure)
		attempted := !errors.Is(err, provider.ErrInvalidRequest) && !errors.Is(err, provider.ErrUnsupportedCapability)
		return streamAttemptOutcome{
			Failure:   failure,
			KeyID:     keyID,
			Attempted: attempted,
			Duration:  duration,
		}
	}

	handedOff = true
	return streamAttemptOutcome{
		Stream: &PreparedStream{
			FirstEvent:    firstEvent,
			ProviderID:    candidate.ProviderID,
			UpstreamModel: candidate.UpstreamModel,
			KeyID:         keyID,
			Candidate:     candidate.Priority,
			stream:        upstream,
			permit:        permit,
			lease:         lease,
			key:           selectedKey,
			cancel:        cancel,
		},
		KeyID:     keyID,
		Attempted: true,
		Duration:  duration,
	}
}

type firstStreamEventResult struct {
	stream      *provider.ChatEventStream
	event       provider.ChatStreamEvent
	err         error
	completedAt time.Time
}

func openFirstStreamEvent(
	waitContext context.Context,
	streamParent context.Context,
	adapter provider.StreamingAdapter,
	input provider.ChatInput,
	apiKey string,
	timeout time.Duration,
) (*provider.ChatEventStream, provider.ChatStreamEvent, context.CancelFunc, error) {
	streamContext, cancel := context.WithCancel(streamParent)
	result := make(chan firstStreamEventResult, 1)
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer stopRetryTimer(timer)
	go func() {
		upstream, err := adapter.OpenStream(streamContext, input, apiKey)
		if err != nil {
			result <- firstStreamEventResult{err: err, completedAt: time.Now()}
			return
		}
		firstEvent, err := upstream.Next()
		result <- firstStreamEventResult{
			stream:      upstream,
			event:       firstEvent,
			err:         err,
			completedAt: time.Now(),
		}
	}()

	select {
	case outcome := <-result:
		if waitContext.Err() != nil {
			cancel()
			_ = outcome.stream.Close()
			return nil, provider.ChatStreamEvent{}, nil, waitContext.Err()
		}
		if !outcome.completedAt.Before(deadline) {
			cancel()
			_ = outcome.stream.Close()
			return nil, provider.ChatStreamEvent{}, nil, context.DeadlineExceeded
		}
		if outcome.err == nil && !outcome.event.Done {
			return outcome.stream, outcome.event, cancel, nil
		}
		cancel()
		_ = outcome.stream.Close()
		if outcome.err == nil || errors.Is(outcome.err, io.EOF) {
			outcome.err = fmt.Errorf("%w: stream ended before first content event", provider.ErrInvalidStream)
		}
		return nil, provider.ChatStreamEvent{}, nil, outcome.err
	case <-waitContext.Done():
		cancel()
		outcome := <-result
		_ = outcome.stream.Close()
		return nil, provider.ChatStreamEvent{}, nil, waitContext.Err()
	case <-timer.C:
		cancel()
		outcome := <-result
		_ = outcome.stream.Close()
		return nil, provider.ChatStreamEvent{}, nil, context.DeadlineExceeded
	}
}
