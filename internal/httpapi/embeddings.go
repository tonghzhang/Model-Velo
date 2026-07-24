package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"model-velo/internal/apikey"
	"model-velo/internal/gateway"
	"model-velo/internal/observability"
	"model-velo/internal/provider"
	"model-velo/internal/quota"
	"model-velo/internal/ratelimit"
	"model-velo/internal/reliability"
	"model-velo/internal/responsecache"
	"model-velo/internal/routing"
	"model-velo/internal/usage"
)

const maxEmbeddingRequestBodyBytes int64 = 8 << 20

type embeddingHandler struct {
	access       AccessController
	limiter      RateLimiter
	cache        ResponseCache
	runtime      gateway.Source
	usageEmitter usage.Emitter
	metrics      *observability.Metrics
	logger       *slog.Logger
	quota        *quota.Manager
}

func (h embeddingHandler) create(c *gin.Context) {
	if !hasJSONContentType(c.GetHeader("Content-Type")) {
		writeAPIError(
			c, http.StatusUnsupportedMediaType,
			"request Content-Type must be application/json",
			"invalid_request_error", nil, "invalid_content_type",
		)
		return
	}
	c.Request.Body = http.MaxBytesReader(
		c.Writer, c.Request.Body, maxEmbeddingRequestBodyBytes,
	)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			writeAPIError(
				c, http.StatusRequestEntityTooLarge,
				"request body exceeds the size limit",
				"invalid_request_error", nil, "request_too_large",
			)
			return
		}
		writeAPIError(
			c, http.StatusBadRequest, "request body could not be read",
			"invalid_request_error", nil, "invalid_request_body",
		)
		return
	}
	request, err := provider.ParseEmbeddingRequest(body)
	if err != nil {
		writeAPIError(
			c, http.StatusBadRequest, "embedding request is invalid",
			"invalid_request_error", nil, "invalid_embedding_request",
		)
		return
	}
	model := strings.TrimSpace(request.Model)
	c.Set("model-velo.model", model)
	c.Set("model-velo.stream", false)
	identity, ok := identityFromContext(c.Request.Context())
	if !ok {
		writeIdentityUnavailable(c)
		return
	}
	session, err := newUsageSession(
		c.Request.Context(), h.usageEmitter, identity.TenantID,
		identity.APIKeyID, model, false,
		h.metrics,
	)
	if err != nil {
		writeAPIError(
			c, http.StatusServiceUnavailable,
			"usage accounting is temporarily unavailable",
			"server_error", nil, "usage_accounting_unavailable",
		)
		return
	}
	defer session.finish(c)

	if err := h.access.AuthorizeModel(c.Request.Context(), identity.TenantID, model); err != nil {
		if errors.Is(err, apikey.ErrModelNotAllowed) {
			writeAPIError(
				c, http.StatusForbidden,
				"API key is not allowed to use the requested model",
				"permission_error", stringPointer("model"), "model_not_allowed",
			)
		} else if c.Request.Context().Err() == nil {
			writeAPIError(
				c, http.StatusServiceUnavailable,
				"model authorization service is unavailable",
				"server_error", nil, "authorization_unavailable",
			)
		}
		return
	}
	decision, err := h.limiter.Allow(c.Request.Context(), identity.TenantID, model)
	if err != nil {
		h.writeLimitError(c, err)
		return
	}
	writeRateLimitHeaders(c, decision)
	if !decision.Allowed {
		h.metrics.RateLimit("rejected")
		writeAPIError(
			c, http.StatusTooManyRequests,
			"tenant request quota exceeded for this model",
			"rate_limit_error", nil, "rate_limit_exceeded",
		)
		return
	}
	if decision.Bypassed {
		h.metrics.RateLimit("bypassed")
	} else {
		h.metrics.RateLimit("allowed")
	}
	active := h.runtime.Current()
	if active == nil || active.Embeddings == nil {
		writeAPIError(
			c, http.StatusServiceUnavailable,
			"no embedding provider is configured",
			"server_error", stringPointer("model"), "embedding_unavailable",
		)
		return
	}
	c.Request = c.Request.WithContext(responsecache.WithRouteVersion(
		c.Request.Context(),
		active.CacheNamespace,
	))
	plan, err := active.Routes.Plan(model, []provider.Capability{provider.CapabilityEmbedding})
	if err != nil {
		h.writeRouteError(c, err)
		return
	}
	if !h.reserveQuota(c, session, identity.TenantID, model, request, plan) {
		return
	}

	cacheResult := responsecache.Result{Status: responsecache.StatusBypass}
	if !requestBypassesResponseCache(c.Request) {
		cacheResult, err = h.cache.Lookup(
			c.Request.Context(), identity.TenantID, model, body,
		)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			h.log().Warn(
				"embedding cache lookup bypassed",
				"request_id", requestIDFromContext(c.Request.Context()),
				"error", err,
			)
			cacheResult = responsecache.Result{Status: responsecache.StatusBypass}
		}
	}
	session.setCacheStatus(string(cacheResult.Status))
	if cacheResult.Status == responsecache.StatusHit {
		h.metrics.Cache("embedding_lookup", "hit")
		session.observe(cacheResult.Body)
		session.finalize(usage.Outcome{Status: usage.StatusCacheHit})
		writeCacheStatusHeader(c, cacheResult.Status)
		c.Data(http.StatusOK, "application/json; charset=utf-8", cacheResult.Body)
		return
	}
	h.metrics.Cache("embedding_lookup", string(cacheResult.Status))

	execution, failure := active.Embeddings.Execute(
		c.Request.Context(),
		reliability.ExecutionInput{
			RequestID: requestIDFromContext(c.Request.Context()),
			Request:   request.ReliabilityRequest(),
			Plan:      plan,
		},
	)
	if failure != nil {
		session.recordFailure(failure)
		chatHandler{metrics: h.metrics, logger: h.logger}.observeFailure(
			requestIDFromContext(c.Request.Context()), failure,
		)
		writeReliabilityError(c, failure)
		status := usage.StatusFailed
		if failure.Category == reliability.CategoryCanceled {
			status = usage.StatusCanceled
		}
		session.finalize(usage.Outcome{
			Status: status, ErrorCategory: string(failure.Category),
			ErrorCode: apiErrorCode(c),
		})
		return
	}
	chatHandler{metrics: h.metrics, logger: h.logger}.observeExecution(
		requestIDFromContext(c.Request.Context()), execution,
	)
	session.recordExecution(execution)
	session.observe(execution.Body)
	if cacheResult.Status == responsecache.StatusMiss && execution.Fallbacks == 0 {
		if err := h.cache.Store(
			c.Request.Context(), identity.TenantID, model, body, execution.Body,
		); err != nil && c.Request.Context().Err() == nil {
			cacheResult.Status = responsecache.StatusBypass
			h.log().Warn(
				"embedding cache store bypassed",
				"request_id", requestIDFromContext(c.Request.Context()),
				"error", err,
			)
		}
	}
	writeCacheStatusHeader(c, cacheResult.Status)
	session.setCacheStatus(string(cacheResult.Status))
	session.finalize(usage.Outcome{Status: usage.StatusSuccess})
	c.Data(http.StatusOK, "application/json; charset=utf-8", execution.Body)
}

func (h embeddingHandler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

func (h embeddingHandler) reserveQuota(
	c *gin.Context,
	session *usageSession,
	tenantID string,
	model string,
	request provider.EmbeddingRequest,
	plan routing.Plan,
) bool {
	if h.quota == nil {
		return true
	}
	decision, err := h.quota.Reserve(c.Request.Context(), quota.ReserveInput{
		GroupID: session.eventID, TenantID: tenantID, GatewayModel: model,
		EstimatedInputTokens: estimateEmbeddingTokens(request.Input),
		Plan:                 plan,
	})
	if err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			h.metrics.Quota("denied")
		} else {
			h.metrics.Quota("error")
		}
		chatHandler{quota: h.quota}.writeQuotaError(c, err)
		return false
	}
	session.attachQuota(h.quota, decision.ReservationID)
	if decision.Exceeded {
		h.metrics.Quota("allowed_overage")
		c.Header("X-Model-Velo-Quota-Warning", "overage")
	} else if len(decision.Alerts) > 0 {
		h.metrics.Quota("allowed_alert")
		c.Header("X-Model-Velo-Quota-Warning", "cost_unknown")
	} else if decision.AppliedPolicies == 0 {
		h.metrics.Quota("no_policy")
	} else {
		h.metrics.Quota("allowed")
	}
	return true
}

func estimateEmbeddingTokens(input json.RawMessage) int64 {
	var tokens []int
	if json.Unmarshal(input, &tokens) == nil {
		return int64(len(tokens))
	}
	var batches [][]int
	if json.Unmarshal(input, &batches) == nil {
		total := int64(0)
		for _, batch := range batches {
			total += int64(len(batch))
		}
		return total
	}
	return int64(len(input))
}

func (h embeddingHandler) writeLimitError(c *gin.Context, err error) {
	if c.Request.Context().Err() != nil {
		return
	}
	if errors.Is(err, ratelimit.ErrUnavailable) {
		writeAPIError(
			c, http.StatusServiceUnavailable,
			"rate limit service is unavailable",
			"server_error", nil, "rate_limit_unavailable",
		)
		return
	}
	writeAPIError(
		c, http.StatusInternalServerError,
		"rate limit decision could not be evaluated",
		"server_error", nil, "rate_limit_error",
	)
}

func (h embeddingHandler) writeRouteError(c *gin.Context, err error) {
	if errors.Is(err, routing.ErrCapabilityUnavailable) ||
		errors.Is(err, routing.ErrNoRoute) {
		writeAPIError(
			c, http.StatusServiceUnavailable,
			"no embedding route is available for the requested model",
			"server_error", stringPointer("model"), "embedding_unavailable",
		)
		return
	}
	writeAPIError(
		c, http.StatusInternalServerError,
		"embedding route could not be planned",
		"server_error", nil, "route_error",
	)
}
