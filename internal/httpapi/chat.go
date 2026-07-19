package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"model-velo/internal/apikey"
	"model-velo/internal/provider"
	"model-velo/internal/ratelimit"
	"model-velo/internal/reliability"
	"model-velo/internal/responsecache"
	"model-velo/internal/routing"
)

const maxChatRequestBodyBytes int64 = 1 << 20

type chatHandler struct {
	client  *provider.Client
	access  AccessController
	limiter RateLimiter
	cache   ResponseCache
	routes  *routing.Router
	breaker *reliability.Breaker
	queues  *reliability.QueueRegistry
}

type ResponseCache interface {
	Lookup(ctx context.Context, tenantID, model string, requestBody []byte) (responsecache.Result, error)
	Store(ctx context.Context, tenantID, model string, requestBody, responseBody []byte) error
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (h chatHandler) complete(c *gin.Context) {
	if !hasJSONContentType(c.GetHeader("Content-Type")) {
		writeAPIError(
			c,
			http.StatusUnsupportedMediaType,
			"request Content-Type must be application/json",
			"invalid_request_error",
			nil,
			"invalid_content_type",
		)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatRequestBodyBytes)
	requestBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(
				c,
				http.StatusRequestEntityTooLarge,
				"request body exceeds the size limit",
				"invalid_request_error",
				nil,
				"request_too_large",
			)
			return
		}

		writeAPIError(
			c,
			http.StatusBadRequest,
			"request body could not be read",
			"invalid_request_error",
			nil,
			"invalid_request_body",
		)
		return
	}

	var request *chatRequest
	if err := json.Unmarshal(requestBody, &request); err != nil || request == nil {
		writeAPIError(
			c,
			http.StatusBadRequest,
			"request body must be a valid JSON object",
			"invalid_request_error",
			nil,
			"invalid_json",
		)
		return
	}

	if message, param, code := validateChatRequest(request); code != "" {
		writeAPIError(c, http.StatusBadRequest, message, "invalid_request_error", param, code)
		return
	}

	identity, ok := identityFromContext(c.Request.Context())
	if !ok {
		writeAPIError(
			c,
			http.StatusInternalServerError,
			"authenticated identity is unavailable",
			"server_error",
			nil,
			"identity_unavailable",
		)
		return
	}

	model := strings.TrimSpace(request.Model)
	if err := h.access.AuthorizeModel(
		c.Request.Context(),
		identity.TenantID,
		model,
	); err != nil {
		if c.Request.Context().Err() != nil {
			return
		}
		if errors.Is(err, apikey.ErrModelNotAllowed) {
			writeAPIError(
				c,
				http.StatusForbidden,
				"API key is not allowed to use the requested model",
				"permission_error",
				stringPointer("model"),
				"model_not_allowed",
			)
			return
		}

		writeAPIError(
			c,
			http.StatusServiceUnavailable,
			"model authorization service is unavailable",
			"server_error",
			nil,
			"authorization_unavailable",
		)
		return
	}

	limitDecision, err := h.limiter.Allow(c.Request.Context(), identity.TenantID, model)
	if err != nil {
		if c.Request.Context().Err() != nil {
			return
		}
		if errors.Is(err, ratelimit.ErrUnavailable) {
			writeAPIError(
				c,
				http.StatusServiceUnavailable,
				"rate limit service is unavailable",
				"server_error",
				nil,
				"rate_limit_unavailable",
			)
			return
		}

		writeAPIError(
			c,
			http.StatusInternalServerError,
			"rate limit decision could not be evaluated",
			"server_error",
			nil,
			"rate_limit_error",
		)
		return
	}
	writeRateLimitHeaders(c, limitDecision)
	if !limitDecision.Allowed {
		writeAPIError(
			c,
			http.StatusTooManyRequests,
			"tenant request quota exceeded for this model",
			"rate_limit_error",
			nil,
			"rate_limit_exceeded",
		)
		return
	}

	routePlan, err := h.routes.Plan(identity.TenantID, model)
	if err != nil {
		if errors.Is(err, routing.ErrNoRoute) {
			writeAPIError(
				c,
				http.StatusServiceUnavailable,
				"no provider route is available for the requested model",
				"server_error",
				stringPointer("model"),
				"route_unavailable",
			)
			return
		}
		writeAPIError(
			c,
			http.StatusInternalServerError,
			"provider route could not be planned",
			"server_error",
			nil,
			"route_error",
		)
		return
	}
	primary, ok := routePlan.Primary()
	if !ok {
		writeAPIError(
			c,
			http.StatusServiceUnavailable,
			"no provider route is available for the requested model",
			"server_error",
			stringPointer("model"),
			"route_unavailable",
		)
		return
	}

	cacheResult := responsecache.Result{Status: responsecache.StatusBypass}
	if !requestBypassesResponseCache(c.Request) {
		cacheResult, err = h.cache.Lookup(c.Request.Context(), identity.TenantID, model, requestBody)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			log.Printf("response cache lookup bypass request_id=%s: %v", requestIDFromContext(c.Request.Context()), err)
			cacheResult = responsecache.Result{Status: responsecache.StatusBypass}
		}
	}
	if cacheResult.Status == responsecache.StatusHit {
		writeCacheStatusHeader(c, cacheResult.Status)
		c.Data(http.StatusOK, "application/json; charset=utf-8", cacheResult.Body)
		return
	}

	responseBody, failure := h.callPrimary(
		c.Request.Context(),
		requestIDFromContext(c.Request.Context()),
		model,
		requestBody,
		primary,
	)
	if failure != nil {
		writeReliabilityError(c, failure)
		return
	}
	if cacheResult.Status == responsecache.StatusMiss {
		if err := h.cache.Store(
			c.Request.Context(),
			identity.TenantID,
			model,
			requestBody,
			responseBody,
		); err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			log.Printf("response cache store bypass request_id=%s: %v", requestIDFromContext(c.Request.Context()), err)
			cacheResult.Status = responsecache.StatusBypass
		}
	}

	writeCacheStatusHeader(c, cacheResult.Status)
	c.Data(http.StatusOK, "application/json; charset=utf-8", responseBody)
}

func (h chatHandler) callPrimary(
	ctx context.Context,
	requestID string,
	requestedModel string,
	requestBody []byte,
	candidate routing.Candidate,
) ([]byte, *reliability.Failure) {
	permit, failure := h.breaker.Allow()
	if failure != nil {
		failure.Candidate = candidate.Priority
		failure.Attempt = 1
		return nil, failure
	}
	defer permit.Abandon()

	lease, failure := h.queues.Acquire(ctx, candidate.ProviderID)
	if failure != nil {
		failure.Candidate = candidate.Priority
		failure.Attempt = 1
		permit.Complete(failure)
		return nil, failure
	}
	defer lease.Release()

	var (
		responseBody []byte
		err          error
	)
	if candidate.UpstreamModel == requestedModel {
		responseBody, err = h.client.Chat(ctx, requestID, requestBody)
	} else {
		responseBody, err = h.client.ChatModel(ctx, requestID, requestBody, candidate.UpstreamModel)
	}
	failure = reliability.FromProvider(ctx, candidate.ProviderID, candidate.Priority, 1, err)
	lease.Release()
	permit.Complete(failure)
	return responseBody, failure
}

func writeCacheStatusHeader(c *gin.Context, status responsecache.Status) {
	if status == "" {
		status = responsecache.StatusBypass
	}
	c.Header("X-Model-Velo-Cache", string(status))
}

func requestBypassesResponseCache(request *http.Request) bool {
	for _, value := range request.Header.Values("Cache-Control") {
		for _, directive := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(directive), "no-store") {
				return true
			}
		}
	}
	return false
}

func writeRateLimitHeaders(c *gin.Context, decision ratelimit.Decision) {
	if decision.Bypassed {
		c.Header("X-RateLimit-Status", "bypassed")
		return
	}

	c.Header("X-RateLimit-Limit", strconv.FormatInt(decision.Limit, 10))
	c.Header("X-RateLimit-Remaining", strconv.FormatInt(decision.Remaining, 10))
	c.Header("X-RateLimit-Reset", strconv.FormatInt(decision.ResetAtUnix, 10))
	if !decision.Allowed {
		c.Header("Retry-After", strconv.FormatInt(decision.RetryAfterSeconds, 10))
	}
}

func hasJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

func validateChatRequest(request *chatRequest) (message string, param *string, code string) {
	if strings.TrimSpace(request.Model) == "" {
		return "model is required", stringPointer("model"), "missing_model"
	}
	if len(request.Messages) == 0 {
		return "messages must contain at least one message", stringPointer("messages"), "missing_messages"
	}

	for index, message := range request.Messages {
		if !supportedMessageRole(message.Role) {
			return fmt.Sprintf("messages[%d].role is not supported", index), stringPointer("messages"), "invalid_message_role"
		}
		if strings.TrimSpace(message.Content) == "" {
			return fmt.Sprintf("messages[%d].content is required", index), stringPointer("messages"), "missing_message_content"
		}
	}

	if request.Stream {
		return "streaming chat completions are not supported yet", stringPointer("stream"), "stream_not_supported"
	}

	return "", nil, ""
}

func supportedMessageRole(role string) bool {
	switch role {
	case "system", "user", "assistant":
		return true
	default:
		return false
	}
}

func writeReliabilityError(c *gin.Context, failure *reliability.Failure) {
	if failure == nil {
		return
	}
	if failure.Category == reliability.CategoryCanceled && c.Request.Context().Err() != nil {
		return
	}

	switch failure.Category {
	case reliability.CategoryTimeout:
		writeAPIError(c, http.StatusGatewayTimeout, "upstream request timed out", "upstream_error", nil, "upstream_timeout")
	case reliability.CategoryUpstreamProtocol:
		switch {
		case errors.Is(failure, provider.ErrResponseTooLarge):
			writeAPIError(c, http.StatusBadGateway, "upstream response exceeded the size limit", "upstream_error", nil, "upstream_response_too_large")
		case errors.Is(failure, provider.ErrInvalidResponse):
			writeAPIError(c, http.StatusBadGateway, "upstream returned an invalid response", "upstream_error", nil, "invalid_upstream_response")
		default:
			writeAPIError(c, http.StatusBadGateway, "upstream returned an unsupported response", "upstream_error", nil, "upstream_protocol_error")
		}
	case reliability.CategoryUpstream4xx:
		if failure.StatusCode == http.StatusBadRequest {
			writeAPIError(c, http.StatusBadRequest, "upstream rejected the request", "invalid_request_error", nil, "upstream_rejected_request")
		} else {
			writeAPIError(c, http.StatusBadGateway, "upstream request failed", "upstream_error", nil, "upstream_http_error")
		}
	case reliability.CategoryUpstreamRateLimit:
		writeAPIError(c, http.StatusTooManyRequests, "upstream rate limit exceeded", "upstream_error", nil, "upstream_rate_limited")
	case reliability.CategoryKeyRejected, reliability.CategoryUpstream5xx:
		writeAPIError(c, http.StatusBadGateway, "upstream request failed", "upstream_error", nil, "upstream_http_error")
	case reliability.CategoryBreaker:
		if failure.RetryAfter > 0 {
			seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second)
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeAPIError(c, http.StatusServiceUnavailable, "provider circuit is temporarily open", "upstream_error", nil, "provider_circuit_open")
	case reliability.CategoryQueue:
		switch failure.Queue {
		case reliability.QueueFull:
			writeAPIError(c, http.StatusServiceUnavailable, "provider queue is full", "upstream_error", nil, "provider_queue_full")
		case reliability.QueueWaitTimeout:
			writeAPIError(c, http.StatusServiceUnavailable, "provider queue wait timed out", "upstream_error", nil, "provider_queue_timeout")
		default:
			writeAPIError(c, http.StatusServiceUnavailable, "provider capacity is temporarily unavailable", "upstream_error", nil, "provider_queue_unavailable")
		}
	case reliability.CategoryLocalValidation:
		writeAPIError(c, http.StatusInternalServerError, "provider request could not be prepared", "server_error", nil, "provider_request_error")
	case reliability.CategoryCanceled:
		writeAPIError(c, http.StatusGatewayTimeout, "upstream request was canceled", "upstream_error", nil, "upstream_canceled")
	default:
		writeAPIError(c, http.StatusBadGateway, "upstream is unavailable", "upstream_error", nil, "upstream_unavailable")
	}
}
