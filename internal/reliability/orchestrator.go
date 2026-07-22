package reliability

import (
	"context"
	"errors"

	"model-velo/internal/provider"
	"model-velo/internal/routing"
)

type ExecutionInput struct {
	RequestID string
	Request   provider.ChatRequest
	Plan      routing.Plan
}

type ExecutionResult struct {
	Body            []byte
	ProviderID      string
	UpstreamModel   string
	KeyID           string
	Attempts        int
	Fallbacks       int
	CandidatesTried int
	Trail           []AttemptRecord
}

type Orchestrator struct {
	attempts *AttemptExecutor
	retries  RetryPolicies
}

func NewOrchestrator(attempts *AttemptExecutor, retries RetryPolicies) (*Orchestrator, error) {
	if attempts == nil {
		return nil, errors.New("orchestrator requires an attempt executor")
	}
	if retries == nil {
		return nil, errors.New("orchestrator requires a retry policy")
	}
	return &Orchestrator{attempts: attempts, retries: retries}, nil
}

func (orchestrator *Orchestrator) Execute(ctx context.Context, input ExecutionInput) (ExecutionResult, *Failure) {
	if len(input.Plan.Candidates) == 0 {
		return ExecutionResult{}, &Failure{
			Category: CategoryLocalValidation,
			Cause:    routing.ErrNoRoute,
		}
	}

	executionContext, cancelExecution := orchestrator.retries.RequestContext(ctx)
	defer cancelExecution()

	totalAttempts := 0
	fallbacks := 0
	var trail []AttemptRecord
	var lastFailure *Failure
	for index, candidate := range input.Plan.Candidates {
		attemptResult, failure := orchestrator.attempts.Execute(executionContext, AttemptInput{
			RequestID:      input.RequestID,
			RequestedModel: input.Plan.RequestedModel,
			Request:        input.Request,
			Candidate:      candidate,
		})
		totalAttempts += attemptResult.Attempts
		trail = append(trail, attemptResult.Trail...)
		if failure == nil {
			return ExecutionResult{
				Body:            attemptResult.Body,
				ProviderID:      attemptResult.ProviderID,
				UpstreamModel:   attemptResult.UpstreamModel,
				KeyID:           attemptResult.KeyID,
				Attempts:        totalAttempts,
				Fallbacks:       fallbacks,
				CandidatesTried: index + 1,
				Trail:           trail,
			}, nil
		}

		lastFailure = failure
		failure.TotalAttempts = totalAttempts
		failure.Fallbacks = fallbacks
		failure.Trail = trail
		if executionContext.Err() != nil {
			failure = FromProvider(executionContext, candidate.ProviderID, candidate.Priority, failure.Attempt, executionContext.Err())
			failure.TotalAttempts = totalAttempts
			failure.Fallbacks = fallbacks
			failure.Trail = trail
			return ExecutionResult{}, failure
		}
		if !SignalsFor(failure).Fallback || index == len(input.Plan.Candidates)-1 {
			return ExecutionResult{}, failure
		}
		fallbacks++
	}

	if lastFailure == nil {
		lastFailure = &Failure{Category: CategoryLocalValidation, Cause: routing.ErrNoRoute}
	}
	lastFailure.TotalAttempts = totalAttempts
	lastFailure.Fallbacks = fallbacks
	lastFailure.Trail = trail
	return ExecutionResult{}, lastFailure
}
