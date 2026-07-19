package reliability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"model-velo/internal/provider"
)

type Category string

const (
	CategoryLocalValidation   Category = "local_validation"
	CategoryAuthorization     Category = "authorization"
	CategoryKeyRejected       Category = "key_rejected"
	CategoryTenantRateLimit   Category = "tenant_rate_limit"
	CategoryUpstreamRateLimit Category = "upstream_rate_limit"
	CategoryUpstream4xx       Category = "upstream_4xx"
	CategoryUpstream5xx       Category = "upstream_5xx"
	CategoryUpstreamProtocol  Category = "upstream_protocol"
	CategoryNetwork           Category = "network"
	CategoryTimeout           Category = "timeout"
	CategoryQueue             Category = "queue"
	CategoryBreaker           Category = "breaker"
	CategoryCanceled          Category = "canceled"
)

type Failure struct {
	Category   Category
	ProviderID string
	Candidate  int
	Attempt    int
	StatusCode int
	Timeout    TimeoutScope
	Queue      QueueReason
	RetryAfter time.Duration
	Cause      error
}

type TimeoutScope string

const (
	TimeoutUpstream      TimeoutScope = "upstream"
	TimeoutRequestBudget TimeoutScope = "request_budget"
)

func (f *Failure) Error() string {
	if f == nil {
		return "reliability failure"
	}
	message := "reliability failure: " + string(f.Category)
	if f.ProviderID != "" {
		message += " provider=" + f.ProviderID
	}
	if f.StatusCode != 0 {
		message += fmt.Sprintf(" status=%d", f.StatusCode)
	}
	return message
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

type Signals struct {
	Retry        bool
	SwitchKey    bool
	Fallback     bool
	CountBreaker bool
}

func SignalsFor(failure *Failure) Signals {
	if failure == nil {
		return Signals{}
	}

	switch failure.Category {
	case CategoryKeyRejected:
		return Signals{SwitchKey: true, Fallback: true}
	case CategoryUpstreamRateLimit:
		return Signals{Retry: true, SwitchKey: true, Fallback: true}
	case CategoryUpstream5xx:
		if breakerHTTPStatus(failure.StatusCode) {
			return Signals{Retry: true, Fallback: true, CountBreaker: true}
		}
	case CategoryNetwork:
		return Signals{Retry: true, Fallback: true, CountBreaker: true}
	case CategoryTimeout:
		if failure.Timeout == TimeoutUpstream {
			return Signals{Retry: true, Fallback: true, CountBreaker: true}
		}
	case CategoryUpstreamProtocol, CategoryQueue, CategoryBreaker:
		return Signals{Fallback: true}
	}
	return Signals{}
}

func FromProvider(
	ctx context.Context,
	providerID string,
	candidate int,
	attempt int,
	err error,
) *Failure {
	if err == nil {
		return nil
	}

	providerID = strings.TrimSpace(providerID)
	failure := &Failure{
		ProviderID: providerID,
		Candidate:  candidate,
		Attempt:    attempt,
		Cause:      err,
	}

	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		failure.Category = CategoryCanceled
		return failure
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		failure.Category = CategoryTimeout
		failure.Timeout = TimeoutRequestBudget
		return failure
	}
	if errors.Is(err, context.DeadlineExceeded) {
		failure.Category = CategoryTimeout
		failure.Timeout = TimeoutUpstream
		return failure
	}

	var existing *Failure
	if errors.As(err, &existing) {
		copy := *existing
		if copy.ProviderID == "" {
			copy.ProviderID = providerID
		}
		if copy.Attempt == 0 {
			copy.Attempt = attempt
		}
		return &copy
	}
	if errors.Is(err, provider.ErrInvalidRequest) {
		failure.Category = CategoryLocalValidation
		return failure
	}
	if errors.Is(err, provider.ErrInvalidResponse) || errors.Is(err, provider.ErrResponseTooLarge) {
		failure.Category = CategoryUpstreamProtocol
		return failure
	}

	var httpError *provider.HTTPError
	if errors.As(err, &httpError) {
		failure.StatusCode = httpError.StatusCode
		switch httpError.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			failure.Category = CategoryKeyRejected
		case http.StatusTooManyRequests:
			failure.Category = CategoryUpstreamRateLimit
		default:
			switch {
			case httpError.StatusCode >= http.StatusBadRequest && httpError.StatusCode < http.StatusInternalServerError:
				failure.Category = CategoryUpstream4xx
			case httpError.StatusCode >= http.StatusInternalServerError:
				failure.Category = CategoryUpstream5xx
			default:
				failure.Category = CategoryUpstreamProtocol
			}
		}
		return failure
	}

	failure.Category = CategoryNetwork
	return failure
}

func breakerHTTPStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
