package reliability

import (
	"context"
	"errors"
	"time"

	"model-velo/internal/provider"
	"model-velo/internal/routing"
)

type AttemptInput struct {
	RequestID      string
	RequestedModel string
	Request        provider.ChatRequest
	Candidate      routing.Candidate
}

type AttemptResult struct {
	Body          []byte
	ProviderID    string
	UpstreamModel string
	KeyID         string
	Attempts      int
	Trail         []AttemptRecord
}

type AttemptRecord struct {
	ProviderID    string
	UpstreamModel string
	KeyID         string
	Candidate     int
	Attempt       int
	Duration      time.Duration
	Category      Category
	StatusCode    int
}

type AttemptExecutor struct {
	adapters *provider.AdapterRegistry
	breakers *BreakerRegistry
	queues   *QueueRegistry
	keys     *ProviderKeyRegistry
	retries  RetryPolicies
}

func NewAttemptExecutor(
	adapters *provider.AdapterRegistry,
	breakers *BreakerRegistry,
	queues *QueueRegistry,
	keys *ProviderKeyRegistry,
	retries RetryPolicies,
) (*AttemptExecutor, error) {
	if adapters == nil {
		return nil, errors.New("attempt executor requires a provider adapter registry")
	}
	if breakers == nil {
		return nil, errors.New("attempt executor requires a circuit breaker registry")
	}
	if queues == nil {
		return nil, errors.New("attempt executor requires a provider queue registry")
	}
	if keys == nil && len(adapters.KeyedProviderIDs()) > 0 {
		return nil, errors.New("attempt executor requires a provider key registry for API-key adapters")
	}
	if retries == nil {
		return nil, errors.New("attempt executor requires a retry policy")
	}
	return &AttemptExecutor{
		adapters: adapters,
		breakers: breakers,
		queues:   queues,
		keys:     keys,
		retries:  retries,
	}, nil
}

func (executor *AttemptExecutor) Execute(ctx context.Context, input AttemptInput) (AttemptResult, *Failure) {
	result := AttemptResult{
		ProviderID:    input.Candidate.ProviderID,
		UpstreamModel: input.Candidate.UpstreamModel,
	}
	retry := executor.retries.ForProvider(input.Candidate.ProviderID)
	if retry == nil {
		return result, &Failure{
			Category:   CategoryLocalValidation,
			ProviderID: input.Candidate.ProviderID,
			Candidate:  input.Candidate.Priority,
			Cause:      errors.New("provider retry policy is not configured"),
		}
	}
	preferredKeyID := ""
	excludedKeyIDs := make(map[string]struct{})
	for {
		attempt := result.Attempts + 1
		outcome := executor.executeOnce(ctx, input, attempt, preferredKeyID, excludedKeyIDs, retry)
		if outcome.KeyID != "" {
			result.KeyID = outcome.KeyID
		}
		if outcome.Attempted {
			result.Attempts++
			result.Trail = append(result.Trail, AttemptRecord{
				ProviderID:    input.Candidate.ProviderID,
				UpstreamModel: input.Candidate.UpstreamModel,
				KeyID:         outcome.KeyID,
				Candidate:     input.Candidate.Priority,
				Attempt:       result.Attempts,
				Duration:      outcome.Duration,
				Category:      failureCategory(outcome.Failure),
				StatusCode:    failureStatus(outcome.Failure),
			})
		}
		failure := outcome.Failure
		if failure == nil {
			result.Body = outcome.Body
			return result, nil
		}
		if !outcome.Attempted {
			failure.Attempt = result.Attempts
		}
		if failure.KeyID == "" {
			failure.KeyID = result.KeyID
		}
		failure.TotalAttempts = result.Attempts
		if !retry.ShouldRetry(failure, result.Attempts) {
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
		if !retry.Wait(ctx, retry.Backoff(failure, result.Attempts)) {
			if err := ctx.Err(); err != nil {
				failure = FromProvider(ctx, input.Candidate.ProviderID, input.Candidate.Priority, result.Attempts, err)
			}
			failure.TotalAttempts = result.Attempts
			return result, failure
		}
	}
}

type attemptOutcome struct {
	Body      []byte
	Failure   *Failure
	KeyID     string
	Attempted bool
	Duration  time.Duration
}

func (executor *AttemptExecutor) executeOnce(
	ctx context.Context,
	input AttemptInput,
	attempt int,
	preferredKeyID string,
	excludedKeyIDs map[string]struct{},
	retry *RetryPolicy,
) attemptOutcome {
	candidate := input.Candidate
	adapter, ok := executor.adapters.Adapter(candidate.ProviderID)
	if !ok {
		return attemptOutcome{Failure: &Failure{
			Category:   CategoryLocalValidation,
			ProviderID: candidate.ProviderID,
			Candidate:  candidate.Priority,
			Attempt:    attempt,
			Cause:      provider.ErrUnknownProvider,
		}}
	}

	permit, failure := executor.breakers.Allow(candidate.ProviderID)
	if failure != nil {
		failure.Candidate = candidate.Priority
		failure.Attempt = attempt
		return attemptOutcome{Failure: failure}
	}
	defer permit.Abandon()

	lease, failure := executor.queues.Acquire(ctx, candidate.ProviderID)
	if failure != nil {
		failure.Candidate = candidate.Priority
		failure.Attempt = attempt
		permit.Complete(failure)
		return attemptOutcome{Failure: failure}
	}
	defer lease.Release()

	var selectedKey *ProviderKeySelection
	if executor.keys != nil && adapter.Authentication() == provider.AuthenticationAPIKey {
		selectedKey, failure = executor.keys.selectKey(candidate.ProviderID, preferredKeyID, excludedKeyIDs)
		if failure != nil {
			failure.Candidate = candidate.Priority
			failure.Attempt = attempt
			permit.Complete(failure)
			return attemptOutcome{Failure: failure}
		}
	}

	attemptContext, cancelAttempt := retry.AttemptContext(ctx)
	defer cancelAttempt()
	modelOverride := ""
	if candidate.UpstreamModel != input.RequestedModel {
		modelOverride = candidate.UpstreamModel
	}
	apiKey := ""
	if selectedKey != nil {
		apiKey = selectedKey.Secret()
	}
	startedAt := time.Now()
	responseBody, err := adapter.Complete(attemptContext, provider.ChatInput{
		RequestID:     input.RequestID,
		Request:       input.Request,
		ModelOverride: modelOverride,
	}, apiKey)
	duration := time.Since(startedAt)

	failure = FromProvider(ctx, candidate.ProviderID, candidate.Priority, attempt, err)
	selectedKeyID := ""
	if selectedKey != nil {
		selectedKeyID = selectedKey.KeyID()
		if failure != nil {
			failure.KeyID = selectedKeyID
		}
		selectedKey.Complete(failure)
	}
	permit.Complete(failure)
	attempted := !errors.Is(err, provider.ErrInvalidRequest) && !errors.Is(err, provider.ErrUnsupportedCapability)
	return attemptOutcome{
		Body:      responseBody,
		Failure:   failure,
		KeyID:     selectedKeyID,
		Attempted: attempted,
		Duration:  duration,
	}
}

func failureCategory(failure *Failure) Category {
	if failure == nil {
		return ""
	}
	return failure.Category
}

func failureStatus(failure *Failure) int {
	if failure == nil {
		return 0
	}
	return failure.StatusCode
}
