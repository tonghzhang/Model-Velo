package reliability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"model-velo/internal/provider"
)

type Category string

const (
	CategoryLocalValidation       Category = "local_validation"
	CategoryKeyUnauthorized       Category = "key_unauthorized"
	CategoryKeyForbidden          Category = "key_forbidden"
	CategoryUpstreamRateLimit     Category = "upstream_rate_limit"
	CategoryUpstream4xx           Category = "upstream_4xx"
	CategoryModelUnavailable      Category = "model_unavailable"
	CategoryUpstream5xx           Category = "upstream_5xx"
	CategoryUpstreamProtocol      Category = "upstream_protocol"
	CategoryUnsupportedCapability Category = "unsupported_capability"
	CategoryUnsupportedResponse   Category = "unsupported_response"
	CategoryNetwork               Category = "network"
	CategoryTimeout               Category = "timeout"
	CategoryKeyExhausted          Category = "key_exhausted"
	CategoryQueue                 Category = "queue"
	CategoryBreaker               Category = "breaker"
	CategoryCanceled              Category = "canceled"
)

type Failure struct {
	Category      Category
	ProviderID    string
	KeyID         string
	Candidate     int
	Attempt       int
	TotalAttempts int
	Fallbacks     int
	StatusCode    int
	Timeout       TimeoutScope
	Queue         QueueReason
	RetryAfter    time.Duration
	RetryAfterSet bool
	Trail         []AttemptRecord
	Cause         error
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
	case CategoryKeyUnauthorized, CategoryKeyForbidden:
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
	case CategoryUpstreamProtocol:
		return Signals{
			Fallback:     true,
			CountBreaker: !errors.Is(failure.Cause, provider.ErrResponseTooLarge),
		}
	case CategoryUnsupportedCapability,
		CategoryUnsupportedResponse,
		CategoryModelUnavailable,
		CategoryQueue,
		CategoryBreaker:
		return Signals{Fallback: true}
	case CategoryKeyExhausted:
		return Signals{
			Retry:    failure.RetryAfter > 0,
			Fallback: true,
		}
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
	if errors.Is(err, provider.ErrUnsupportedCapability) {
		failure.Category = CategoryUnsupportedCapability
		return failure
	}
	if errors.Is(err, provider.ErrUnsupportedResponse) {
		failure.Category = CategoryUnsupportedResponse
		return failure
	}
	if errors.Is(err, provider.ErrInvalidRequest) {
		failure.Category = CategoryLocalValidation
		return failure
	}
	if errors.Is(err, provider.ErrInvalidResponse) ||
		errors.Is(err, provider.ErrInvalidStream) ||
		errors.Is(err, provider.ErrResponseTooLarge) {
		failure.Category = CategoryUpstreamProtocol
		return failure
	}

	var httpError *provider.HTTPError
	if errors.As(err, &httpError) {
		failure.StatusCode = httpError.StatusCode
		switch httpError.StatusCode {
		case http.StatusUnauthorized:
			failure.Category = CategoryKeyUnauthorized
		case http.StatusForbidden:
			failure.Category = CategoryKeyForbidden
		case http.StatusTooManyRequests:
			failure.Category = CategoryUpstreamRateLimit
			failure.RetryAfter, failure.RetryAfterSet = parseRetryAfter(httpError.RetryAfter, time.Now())
			if !failure.RetryAfterSet {
				failure.RetryAfter = defaultProviderKeyCooldown
				failure.RetryAfterSet = true
			}
		default:
			switch {
			case modelUnavailable(httpError.StatusCode, httpError.Code):
				failure.Category = CategoryModelUnavailable
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

func modelUnavailable(statusCode int, code string) bool {
	switch statusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
	default:
		return false
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if statusCode == http.StatusNotFound && (code == "not_found" || code == "not_found_error") {
		return true
	}
	if strings.Contains(code, "deployment_not_found") || strings.Contains(code, "deploymentnotfound") {
		return true
	}
	if !strings.Contains(code, "model") {
		return false
	}
	for _, marker := range []string{
		"not_found", "notfound", "not_available", "unavailable",
		"unsupported", "invalid", "deprecated", "retired", "disabled",
	} {
		if strings.Contains(code, marker) {
			return true
		}
	}
	return false
}

const maximumRetryAfter = 24 * time.Hour

func parseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}

	allDigits := true
	for _, character := range raw {
		if character < '0' || character > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		maximumSeconds := uint64(maximumRetryAfter / time.Second)
		if err != nil || seconds > maximumSeconds {
			return maximumRetryAfter, true
		}
		return time.Duration(seconds) * time.Second, true
	}

	retryAt, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	delay := retryAt.Sub(now.UTC())
	if delay <= 0 {
		return 0, true
	}
	if delay > maximumRetryAfter {
		return maximumRetryAfter, true
	}
	return delay, true
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
