package httpapi // HTTP 接口处理层。

import (
	"bytes"         // 处理字节切片和 JSON 内容。
	"context"       // 传递请求取消和超时信号。
	"encoding/json" // 解析、压缩 JSON。
	"errors"        // 判断具体错误类型。
	"fmt"           // 生成带下标的错误信息。
	"io"            // 读取请求体、写入流数据。
	"log/slog"      // 记录不含敏感内容的结构化运行事件。
	"mime"          // 解析 Content-Type。
	"net/http"      // HTTP 状态码、请求和响应接口。
	"strconv"       // 将限流数值转换成响应头字符串。
	"strings"       // 清理字符串、解析请求头。
	"time"          // 处理重试等待时间。

	"github.com/gin-gonic/gin" // Gin HTTP 框架。

	"model-velo/internal/apikey" // API Key 权限错误。
	"model-velo/internal/gateway"
	"model-velo/internal/observability"
	"model-velo/internal/provider" // 聊天请求、Adapter 和流事件。
	"model-velo/internal/quota"
	"model-velo/internal/ratelimit"     // 租户限流结果。
	"model-velo/internal/reliability"   // 重试、熔断、队列和 Provider 回退。
	"model-velo/internal/responsecache" // 非流式响应缓存。
	"model-velo/internal/routing"       // 模型路由计划。
	"model-velo/internal/usage"
)

const (
	// 16 MiB keeps malformed uploads bounded while allowing ordinary inline
	// image/file inputs. Provider-specific limits are still enforced upstream.
	maxChatRequestBodyBytes int64 = 16 << 20
	streamFrameWriteTimeout       = 15 * time.Second
)

// chatHandler 保存聊天接口需要的服务。
type chatHandler struct {
	access       AccessController // 检查租户是否有权使用模型。
	limiter      RateLimiter      // 检查租户请求额度。
	cache        ResponseCache    // 查询和写入响应缓存。
	runtime      gateway.Source   // 当前不可变路由与可靠性运行时。
	usageEmitter usage.Emitter
	metrics      *observability.Metrics
	logger       *slog.Logger
	quota        *quota.Manager
}

// ResponseCache 约束响应缓存需要提供的方法。
type ResponseCache interface {
	Lookup(ctx context.Context, tenantID, model string, requestBody []byte) (responsecache.Result, error) // 查询缓存。
	Store(ctx context.Context, tenantID, model string, requestBody, responseBody []byte) error            // 保存响应。
}

// complete 处理聊天接口请求。
func (h chatHandler) complete(c *gin.Context) {
	if !hasJSONContentType(c.GetHeader("Content-Type")) { // 请求体必须声明为 JSON。
		writeAPIError(
			c,
			http.StatusUnsupportedMediaType, // 返回 415。
			"request Content-Type must be application/json", // 对外错误信息。
			"invalid_request_error",                         // OpenAI 风格错误类型。
			nil,                                             // 没有具体参数。
			"invalid_content_type",                          // 网关内部错误码。
		)
		return // 结束请求。
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatRequestBodyBytes) // 限制请求体最大长度。
	requestBody, err := io.ReadAll(c.Request.Body)                                          // 读取完整原始 JSON。
	if err != nil {
		var maxBytesError *http.MaxBytesError // 用于识别请求体过大错误。
		if errors.As(err, &maxBytesError) {   // 判断是否超过请求体安全上限。
			writeAPIError(
				c,
				http.StatusRequestEntityTooLarge,      // 返回 413。
				"request body exceeds the size limit", // 请求体太大。
				"invalid_request_error",
				nil,
				"request_too_large",
			)
			return
		}

		writeAPIError(
			c,
			http.StatusBadRequest, // 其他读取错误返回 400。
			"request body could not be read",
			"invalid_request_error",
			nil,
			"invalid_request_body",
		)
		return
	}

	request, err := provider.ParseChatRequest(requestBody) // 把原始 JSON 解析为 ChatRequest。
	if err != nil {
		writeAPIError(
			c,
			http.StatusBadRequest, // JSON 格式错误返回 400。
			"request body must be a valid JSON object",
			"invalid_request_error",
			nil,
			"invalid_json",
		)
		return
	}

	if message, param, code := validateChatRequest(request); code != "" { // 校验 model、messages、role 和 content。
		writeAPIError(c, http.StatusBadRequest, message, "invalid_request_error", param, code) // 返回具体校验错误。
		return
	}

	identity, ok := identityFromContext(c.Request.Context()) // 读取认证中间件写入的租户身份。
	if !ok {
		writeAPIError(
			c,
			http.StatusInternalServerError, // 身份丢失属于服务内部错误。
			"authenticated identity is unavailable",
			"server_error",
			nil,
			"identity_unavailable",
		)
		return
	}

	model := strings.TrimSpace(request.Model) // 清理客户端请求的模型名。
	c.Set("model-velo.model", model)
	c.Set("model-velo.stream", request.Stream)
	usageSession, err := newUsageSession(
		c.Request.Context(),
		h.usageEmitter,
		identity.TenantID,
		identity.APIKeyID,
		model,
		request.Stream,
		h.metrics,
	)
	if err != nil {
		writeAPIError(
			c,
			http.StatusServiceUnavailable,
			"usage accounting is temporarily unavailable",
			"server_error",
			nil,
			"usage_accounting_unavailable",
		)
		return
	}
	defer usageSession.finish(c)

	requiredCapabilities, err := request.RequiredCapabilities() // 分析请求需要的图像、工具等能力。
	if err != nil {
		writeAPIError(
			c,
			http.StatusBadRequest, // 请求使用了网关无法识别的能力。
			"request uses a capability that is not supported by this gateway",
			"invalid_request_error",
			nil,
			"unsupported_request_capability",
		)
		return
	}

	startedAt := time.Now()
	authorizationErr := h.access.AuthorizeModel( // 检查该租户是否允许使用当前模型。
		c.Request.Context(),
		identity,
		model,
	)
	authorizationResult := "allowed"
	switch {
	case c.Request.Context().Err() != nil:
		authorizationResult = "canceled"
	case errors.Is(authorizationErr, apikey.ErrModelNotAllowed):
		authorizationResult = "denied"
	case authorizationErr != nil:
		authorizationResult = "error"
	}
	h.metrics.RequestStage(
		"authorization", authorizationResult, "", time.Since(startedAt),
	)
	if authorizationErr != nil {
		if c.Request.Context().Err() != nil { // 请求已经取消时不再写响应。
			return
		}
		if errors.Is(authorizationErr, apikey.ErrModelNotAllowed) { // API Key 没有模型权限。
			writeAPIError(
				c,
				http.StatusForbidden, // 返回 403。
				"API key is not allowed to use the requested model",
				"permission_error",
				stringPointer("model"), // 指明错误参数是 model。
				"model_not_allowed",
			)
			return
		}

		writeAPIError(
			c,
			http.StatusServiceUnavailable, // 授权服务本身不可用。
			"model authorization service is unavailable",
			"server_error",
			nil,
			"authorization_unavailable",
		)
		return
	}

	startedAt = time.Now()
	limitDecision, err := h.limiter.Allow(c.Request.Context(), identity.TenantID, model) // 查询租户和模型的限流额度。
	limitResult := "allowed"
	switch {
	case c.Request.Context().Err() != nil:
		limitResult = "canceled"
	case err != nil:
		limitResult = "error"
	case !limitDecision.Allowed:
		limitResult = "rejected"
	case limitDecision.Bypassed:
		limitResult = "bypassed"
	}
	h.metrics.RequestStage(
		"rate_limit", limitResult, "", time.Since(startedAt),
	)
	if err != nil {
		if c.Request.Context().Err() != nil { // 请求已经取消。
			return
		}
		if errors.Is(err, ratelimit.ErrUnavailable) { // Redis 等限流依赖不可用。
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
			http.StatusInternalServerError, // 其他限流计算错误。
			"rate limit decision could not be evaluated",
			"server_error",
			nil,
			"rate_limit_error",
		)
		return
	}

	writeRateLimitHeaders(c, limitDecision) // 把额度、剩余额度和重置时间写入响应头。
	if !limitDecision.Allowed {             // 当前租户已超过额度。
		h.metrics.RateLimit("rejected")
		writeAPIError(
			c,
			http.StatusTooManyRequests, // 返回 429。
			"tenant request quota exceeded for this model",
			"rate_limit_error",
			nil,
			"rate_limit_exceeded",
		)
		return
	}
	if limitDecision.Bypassed {
		h.metrics.RateLimit("bypassed")
	} else {
		h.metrics.RateLimit("allowed")
	}

	active := h.runtime.Current()
	if active == nil {
		writeAPIError(
			c,
			http.StatusServiceUnavailable,
			"gateway runtime is unavailable",
			"server_error",
			nil,
			"runtime_unavailable",
		)
		return
	}
	c.Request = c.Request.WithContext(responsecache.WithRouteVersion(
		c.Request.Context(),
		active.CacheNamespace,
	))
	startedAt = time.Now()
	routePlan, err := active.Routes.Plan(model, requiredCapabilities) // 生成符合模型和能力要求的候选 Provider。
	routeResult := "planned"
	if err != nil {
		routeResult = "error"
	}
	h.metrics.RequestStage(
		"route_plan", routeResult, "", time.Since(startedAt),
	)
	if err != nil {
		if errors.Is(err, routing.ErrCapabilityUnavailable) { // 没有 Provider 支持请求能力。
			writeAPIError(
				c,
				http.StatusBadRequest,
				"no configured provider model supports the requested capabilities",
				"invalid_request_error",
				stringPointer("model"),
				"unsupported_provider_capability",
			)
			return
		}
		if errors.Is(err, routing.ErrNoRoute) { // 没有为该模型配置路由。
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
			http.StatusInternalServerError, // 其他路由生成错误。
			"provider route could not be planned",
			"server_error",
			nil,
			"route_error",
		)
		return
	}
	if !h.reserveQuota(
		c, usageSession, identity.TenantID, model, requestBody, request, routePlan,
	) {
		return
	}

	if request.Stream { // 流式请求不走普通响应缓存流程。
		usageSession.setCacheStatus(string(responsecache.StatusBypass))
		h.stream(c, active.Chat, request, routePlan, usageSession) // 转入 SSE 处理。
		return
	}

	cacheResult := responsecache.Result{Status: responsecache.StatusBypass} // 默认标记为绕过缓存。
	if !requestBypassesResponseCache(c.Request) {                           // 请求头没有 no-store 时才查询缓存。
		startedAt = time.Now()
		cacheResult, err = h.cache.Lookup(c.Request.Context(), identity.TenantID, model, requestBody) // 用租户、模型和请求体查缓存。
		cacheLookupResult := string(cacheResult.Status)
		if err != nil {
			cacheLookupResult = "error"
		}
		h.metrics.RequestStage(
			"cache_lookup", cacheLookupResult, "", time.Since(startedAt),
		)
		if err != nil {
			if c.Request.Context().Err() != nil { // 请求取消时停止处理。
				return
			}
			h.log().Warn(
				"response cache lookup bypassed",
				"request_id", requestIDFromContext(c.Request.Context()),
				"error", err,
			)
			cacheResult = responsecache.Result{Status: responsecache.StatusBypass} // 缓存故障时继续请求模型。
		}
	}

	if cacheResult.Status == responsecache.StatusHit { // 缓存命中。
		h.metrics.Cache("lookup", "hit")
		usageSession.setCacheStatus(string(cacheResult.Status))
		usageSession.observe(cacheResult.Body)
		clientBody, encodeErr := encodeProtocolResponse(c, cacheResult.Body)
		if encodeErr != nil {
			usageSession.finalize(usage.Outcome{
				Status:        usage.StatusFailed,
				ErrorCategory: "gateway",
				ErrorCode:     "protocol_encoding_failed",
			})
			writeAPIError(
				c,
				http.StatusBadGateway,
				"cached response could not be represented in the requested protocol",
				"upstream_error",
				nil,
				"unsupported_upstream_response",
			)
			return
		}
		usageSession.finalize(usage.Outcome{Status: usage.StatusCacheHit})
		writeCacheStatusHeader(c, cacheResult.Status)                        // 写入 HIT 响应头。
		c.Data(http.StatusOK, "application/json; charset=utf-8", clientBody) // 直接返回缓存响应。
		return
	}
	h.metrics.Cache("lookup", string(cacheResult.Status))
	usageSession.setCacheStatus(string(cacheResult.Status))

	startedAt = time.Now()
	execution, failure := active.Chat.Execute(c.Request.Context(), reliability.ExecutionInput{ // 执行 Provider 调用和回退。
		RequestID: requestIDFromContext(c.Request.Context()), // 当前网关请求 ID。
		Request:   request,                                   // 解析后的聊天请求。
		Plan:      routePlan,                                 // 路由候选计划。
	})
	executionResult := "success"
	if failure != nil {
		executionResult = string(failure.Category)
	}
	h.metrics.RequestStage(
		"reliability", executionResult, "", time.Since(startedAt),
	)
	if failure != nil {
		h.observeFailure(requestIDFromContext(c.Request.Context()), failure)
		usageSession.recordFailure(failure)
		writeReliabilityError(c, failure) // 把内部 Failure 转成 HTTP 错误。
		status := usage.StatusFailed
		errorCode := apiErrorCode(c)
		if failure.Category == reliability.CategoryCanceled {
			status = usage.StatusCanceled
			if errorCode == "" {
				errorCode = "client_canceled"
			}
		}
		usageSession.finalize(usage.Outcome{
			Status:        status,
			ErrorCategory: string(failure.Category),
			ErrorCode:     errorCode,
		})
		return
	}

	responseBody := execution.Body // 取得最终 Provider 响应体。
	h.observeExecution(requestIDFromContext(c.Request.Context()), execution)
	usageSession.recordExecution(execution)
	usageSession.observe(responseBody)
	if cacheResult.Status == responsecache.StatusMiss && execution.Fallbacks == 0 { // 只有缓存未命中且未切换 Provider 时才缓存。
		startedAt = time.Now()
		err := h.cache.Store(
			c.Request.Context(),
			identity.TenantID,
			model,
			requestBody,
			responseBody,
		)
		cacheStoreResult := "stored"
		if err != nil {
			cacheStoreResult = "error"
		}
		h.metrics.RequestStage(
			"cache_store", cacheStoreResult, "", time.Since(startedAt),
		)
		if err != nil {
			if c.Request.Context().Err() != nil { // 请求取消时停止。
				return
			}
			h.log().Warn(
				"response cache store bypassed",
				"request_id", requestIDFromContext(c.Request.Context()),
				"error", err,
			)
			cacheResult.Status = responsecache.StatusBypass // 告诉客户端本次缓存已绕过。
		}
	}

	writeCacheStatusHeader(c, cacheResult.Status) // 写入 MISS、BYPASS 等缓存状态。
	usageSession.setCacheStatus(string(cacheResult.Status))
	clientBody, err := encodeProtocolResponse(c, responseBody)
	if err != nil {
		usageSession.finalize(usage.Outcome{
			Status:        usage.StatusFailed,
			ErrorCategory: "gateway",
			ErrorCode:     "protocol_encoding_failed",
		})
		writeAPIError(
			c,
			http.StatusBadGateway,
			"upstream response could not be represented in the requested protocol",
			"upstream_error",
			nil,
			"unsupported_upstream_response",
		)
		return
	}
	usageSession.finalize(usage.Outcome{Status: usage.StatusSuccess})
	c.Data(http.StatusOK, "application/json; charset=utf-8", clientBody) // 返回最终 JSON。
}

func (h chatHandler) reserveQuota(
	c *gin.Context,
	session *usageSession,
	tenantID string,
	model string,
	body []byte,
	request provider.ChatRequest,
	plan routing.Plan,
) bool {
	if h.quota == nil {
		return true
	}
	if !h.quota.HasPolicy(tenantID, model) {
		h.metrics.RequestStage("quota_reserve", "no_policy", "", 0)
		h.metrics.Quota("no_policy")
		return true
	}
	output := h.quota.DefaultMaxOutputTokens()
	if request.MaxCompletionTokens != nil {
		output = int64(*request.MaxCompletionTokens)
	} else if request.MaxTokens != nil {
		output = int64(*request.MaxTokens)
	}
	if request.N != nil && *request.N > 1 {
		output *= int64(*request.N)
	}
	startedAt := time.Now()
	decision, err := h.quota.Reserve(c.Request.Context(), quota.ReserveInput{
		GroupID:               session.eventID,
		TenantID:              tenantID,
		GatewayModel:          model,
		EstimatedInputTokens:  int64(len(body)),
		EstimatedOutputTokens: output,
		Plan:                  plan,
	})
	result := "allowed"
	switch {
	case c.Request.Context().Err() != nil:
		result = "canceled"
	case errors.Is(err, quota.ErrExceeded):
		result = "denied"
	case err != nil:
		result = "error"
	case decision.Exceeded:
		result = "allowed_overage"
	case len(decision.Alerts) > 0:
		result = "allowed_alert"
	case decision.AppliedPolicies == 0:
		result = "no_policy"
	}
	h.metrics.RequestStage(
		"quota_reserve", result, "", time.Since(startedAt),
	)
	if err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			h.metrics.Quota("denied")
		} else {
			h.metrics.Quota("error")
		}
		h.writeQuotaError(c, err)
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

func (h chatHandler) writeQuotaError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, quota.ErrExceeded):
		writeAPIError(
			c, http.StatusTooManyRequests,
			"tenant token or spending quota exceeded",
			"rate_limit_error", nil, "quota_exceeded",
		)
	case errors.Is(err, quota.ErrCostUnknown):
		writeAPIError(
			c, http.StatusServiceUnavailable,
			"request cost cannot be reserved because pricing is unavailable",
			"server_error", nil, "quota_pricing_unavailable",
		)
	default:
		writeAPIError(
			c, http.StatusServiceUnavailable,
			"quota service is unavailable",
			"server_error", nil, "quota_unavailable",
		)
	}
}

func (h chatHandler) observeExecution(
	requestID string,
	result reliability.ExecutionResult,
) {
	h.observeAttempts(requestID, result.Trail)
	h.metrics.Fallbacks(result.Fallbacks, "success")
}

func (h chatHandler) observeAttempts(
	requestID string,
	trail []reliability.AttemptRecord,
) {
	for _, attempt := range trail {
		outcome := "success"
		switch attempt.Category {
		case reliability.CategoryCanceled:
			outcome = "canceled"
		case "":
		default:
			outcome = "error"
		}
		h.metrics.ProviderAttempt(
			attempt.ProviderID,
			outcome,
			string(attempt.Category),
			attempt.Duration,
			attempt.Attempt > 1,
		)
		h.log().Info(
			"provider attempt completed",
			"request_id", requestID,
			"provider", attempt.ProviderID,
			"upstream_model", attempt.UpstreamModel,
			"key_id", attempt.KeyID,
			"candidate", attempt.Candidate,
			"attempt", attempt.Attempt,
			"result", outcome,
			"category", attempt.Category,
			"status", attempt.StatusCode,
			"duration_ms", attempt.Duration.Milliseconds(),
		)
	}
}

func (h chatHandler) observePreparedStream(
	requestID string,
	stream *reliability.PreparedStream,
) {
	trail := stream.Trail
	if len(trail) > 0 && trail[len(trail)-1].Category == "" {
		trail = trail[:len(trail)-1]
	}
	h.observeAttempts(requestID, trail)
}

func (h chatHandler) observeFinishedStream(
	requestID string,
	stream *reliability.PreparedStream,
) {
	result := "failed"
	if attempt, ok := stream.FinalAttempt(); ok {
		h.observeAttempts(requestID, []reliability.AttemptRecord{attempt})
		if attempt.Category == "" {
			result = "success"
		}
	}
	h.metrics.Fallbacks(stream.Fallbacks, result)
}

func (h chatHandler) observeFailure(
	requestID string,
	failure *reliability.Failure,
) {
	if failure == nil {
		return
	}
	h.observeAttempts(requestID, failure.Trail)
	h.metrics.Fallbacks(failure.Fallbacks, "failed")
}

// stream 处理 SSE 流式聊天响应。
func (h chatHandler) stream(
	c *gin.Context,
	orchestrator *reliability.Orchestrator,
	request provider.ChatRequest,
	plan routing.Plan,
	usageSession *usageSession,
) {
	streamController, ok := chatStreamController(c.Writer)
	if !ok {
		writeAPIError(
			c,
			http.StatusInternalServerError,
			"streaming is unavailable on this HTTP connection", // 当前连接不支持流式刷新。
			"server_error",
			nil,
			"streaming_unavailable",
		)
		return
	}

	startedAt := time.Now()
	prepared, failure := orchestrator.OpenStream(c.Request.Context(), reliability.ExecutionInput{ // 打开上游流并取得首个事件。
		RequestID: requestIDFromContext(c.Request.Context()),
		Request:   request,
		Plan:      plan,
	})
	result := "success"
	if failure != nil {
		result = string(failure.Category)
	}
	h.metrics.RequestStage(
		"reliability", result, "", time.Since(startedAt),
	)
	if failure != nil {
		h.observeFailure(requestIDFromContext(c.Request.Context()), failure)
		usageSession.recordFailure(failure)
		writeReliabilityError(c, failure) // 打开流失败时返回统一错误。
		status := usage.StatusFailed
		errorCode := apiErrorCode(c)
		if failure.Category == reliability.CategoryCanceled {
			status = usage.StatusCanceled
			if errorCode == "" {
				errorCode = "client_canceled"
			}
		}
		usageSession.finalize(usage.Outcome{
			Status:        status,
			ErrorCategory: string(failure.Category),
			ErrorCode:     errorCode,
		})
		return
	}
	requestID := requestIDFromContext(c.Request.Context())
	h.observePreparedStream(requestID, prepared)
	usageSession.recordStream(prepared)
	usageSession.observeStream(prepared.FirstEvent.Data)

	defer func() {
		prepared.Abort(context.Canceled) // 函数异常退出时确保关闭上游流。
		h.observeFinishedStream(requestID, prepared)
	}()
	if err := c.Request.Context().Err(); err != nil {
		prepared.Abort(err) // 客户端已经取消时中止流。
		usageSession.finalize(usage.Outcome{
			Status:        usage.StatusCanceled,
			ErrorCategory: "client",
			ErrorCode:     "client_canceled",
		})
		return
	}
	if err := setChatStreamWriteDeadline(streamController, time.Time{}); err != nil {
		prepared.Abort(err)
		writeAPIError(
			c,
			http.StatusInternalServerError,
			"streaming is unavailable on this HTTP connection",
			"server_error",
			nil,
			"streaming_unavailable",
		)
		usageSession.finalize(usage.Outcome{
			Status:        usage.StatusFailed,
			ErrorCategory: "gateway",
			ErrorCode:     apiErrorCode(c),
		})
		return
	}

	c.Header("Content-Type", "text/event-stream; charset=utf-8") // 声明 SSE。
	c.Header("Cache-Control", "no-cache, no-transform")          // 禁止缓存和代理转换。
	c.Header("Connection", "keep-alive")                         // 保持长连接。
	c.Header("X-Accel-Buffering", "no")                          // 禁止 Nginx 缓冲。
	writeCacheStatusHeader(c, responsecache.StatusBypass)        // 流式响应不使用缓存。

	if err := writeAndFlushChatStreamEvent(c, c.Writer, streamController, prepared.FirstEvent); err != nil {
		prepared.Abort(err) // 客户端写入失败时关闭上游流。
		usageSession.finalize(usage.Outcome{
			Status:        usage.StatusStreamInterrupted,
			ErrorCategory: "client_write",
			ErrorCode:     "stream_write_failed",
		})
		return
	}

	for { // 持续读取后续 SSE 事件。
		event, err := prepared.Next() // 从上次读取位置继续读取下一个事件。
		if err != nil {
			failure := prepared.FinishError(c.Request.Context(), err) // 将流读取错误转换为统一 Failure。
			if failure != nil && failure.Category != reliability.CategoryCanceled {
				h.log().Warn(
					"stream interrupted",
					"request_id", requestIDFromContext(c.Request.Context()),
					"provider", failure.ProviderID,
					"category", failure.Category,
				)
			}
			if failure != nil && failure.Category == reliability.CategoryCanceled {
				usageSession.finalize(usage.Outcome{
					Status:        usage.StatusCanceled,
					ErrorCategory: string(failure.Category),
					ErrorCode:     "client_canceled",
				})
			} else if failure != nil {
				usageSession.finalize(usage.Outcome{
					Status:        usage.StatusStreamInterrupted,
					ErrorCategory: string(failure.Category),
					ErrorCode:     "stream_interrupted",
				})
			}
			return
		}

		usageSession.observeStream(event.Data)
		if err := writeAndFlushChatStreamEvent(c, c.Writer, streamController, event); err != nil {
			prepared.Abort(err) // 写入失败时中止上游流。
			usageSession.finalize(usage.Outcome{
				Status:        usage.StatusStreamInterrupted,
				ErrorCategory: "client_write",
				ErrorCode:     "stream_write_failed",
			})
			return
		}

		if event.Done { // 收到上游结束事件。
			prepared.Finish(nil) // 标记本次流正常结束。
			usageSession.finalize(usage.Outcome{Status: usage.StatusStreamCompleted})
			return
		}
	}
}

func (h chatHandler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// responseWriterUnwrapper 表示能够取出底层 ResponseWriter 的包装器。
type responseWriterUnwrapper interface {
	Unwrap() http.ResponseWriter // 返回被包装的底层 Writer。
}

// chatStreamController 取得支持 Flush 和单响应写截止时间的底层 Writer。
func chatStreamController(writer http.ResponseWriter) (*http.ResponseController, bool) {
	for range 8 { // 最多向下解除八层包装。
		unwrapper, ok := writer.(responseWriterUnwrapper) // 判断当前 Writer 是否支持解包。
		if !ok {
			break // 已经无法继续解包。
		}
		writer = unwrapper.Unwrap() // 取得下一层 Writer。
		if writer == nil {
			return nil, false // 底层 Writer 无效。
		}
	}
	if _, ok := writer.(http.Flusher); !ok {
		return nil, false
	}
	return http.NewResponseController(writer), true
}

func writeAndFlushChatStreamEvent(
	c *gin.Context,
	writer io.Writer,
	controller *http.ResponseController,
	event provider.ChatStreamEvent,
) error {
	if err := setChatStreamWriteDeadline(controller, time.Now().Add(streamFrameWriteTimeout)); err != nil {
		return err
	}
	if err := writeProtocolStream(c, writer, event); err != nil {
		_ = setChatStreamWriteDeadline(controller, time.Time{})
		return err
	}
	if err := controller.Flush(); err != nil {
		_ = setChatStreamWriteDeadline(controller, time.Time{})
		return err
	}
	return setChatStreamWriteDeadline(controller, time.Time{})
}

func setChatStreamWriteDeadline(controller *http.ResponseController, deadline time.Time) error {
	err := controller.SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

// writeChatStreamEvent 把 ChatStreamEvent 写成 SSE data 帧。
func writeChatStreamEvent(writer io.Writer, event provider.ChatStreamEvent) error {
	payload := []byte("[DONE]") // 结束事件默认写为 data: [DONE]。
	if !event.Done {
		var compact bytes.Buffer      // 保存压缩后的单行 JSON。
		compact.Grow(len(event.Data)) // 提前申请大致所需容量。
		if err := json.Compact(&compact, event.Data); err != nil {
			return provider.ErrInvalidStream // 事件数据不是合法 JSON。
		}
		payload = compact.Bytes() // 普通事件使用压缩后的 JSON。
	}

	frame := make([]byte, 0, len(payload)+8) // 创建 SSE 帧缓冲区。
	frame = append(frame, "data: "...)       // 写入 SSE data 前缀。
	frame = append(frame, payload...)        // 写入事件内容。
	frame = append(frame, '\n', '\n')        // 空行表示一个 SSE 事件结束。
	written, err := writer.Write(frame)      // 将完整事件写入客户端连接。
	if err == nil && written != len(frame) {
		return io.ErrShortWrite // 没报错但没有完整写入。
	}
	return err // 返回底层写入错误或 nil。
}

// writeCacheStatusHeader 写入响应缓存状态。
func writeCacheStatusHeader(c *gin.Context, status responsecache.Status) {
	if status == "" {
		status = responsecache.StatusBypass // 空状态按 BYPASS 处理。
	}
	c.Header("X-Model-Velo-Cache", string(status)) // 告诉客户端本次缓存结果。
}

// requestBypassesResponseCache 判断请求是否携带 Cache-Control: no-store。
func requestBypassesResponseCache(request *http.Request) bool {
	for _, value := range request.Header.Values("Cache-Control") { // 遍历所有 Cache-Control 请求头。
		for _, directive := range strings.Split(value, ",") { // 一个请求头可能包含多个指令。
			if strings.EqualFold(strings.TrimSpace(directive), "no-store") {
				return true // 客户端明确禁止缓存。
			}
		}
	}
	return false // 可以正常使用缓存。
}

// writeRateLimitHeaders 把限流结果写入 HTTP 响应头。
func writeRateLimitHeaders(c *gin.Context, decision ratelimit.Decision) {
	if decision.Bypassed {
		c.Header("X-RateLimit-Status", "bypassed") // 限流服务故障时可能采用 fail-open。
		return
	}

	c.Header("X-RateLimit-Limit", strconv.FormatInt(decision.Limit, 10))         // 当前窗口最大请求数。
	c.Header("X-RateLimit-Remaining", strconv.FormatInt(decision.Remaining, 10)) // 当前窗口剩余请求数。
	c.Header("X-RateLimit-Reset", strconv.FormatInt(decision.ResetAtUnix, 10))   // 当前窗口重置时间。
	if !decision.Allowed {
		c.Header("Retry-After", strconv.FormatInt(decision.RetryAfterSeconds, 10)) // 被限流后建议等待秒数。
	}
}

// hasJSONContentType 判断请求 Content-Type 是否为 application/json。
func hasJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType) // 去除 charset 等附加参数。
	return err == nil && mediaType == "application/json"  // 解析成功且主类型为 JSON。
}

// validateChatRequest 校验聊天请求的基本字段。
func validateChatRequest(request provider.ChatRequest) (message string, param *string, code string) {
	if strings.TrimSpace(request.Model) == "" {
		return "model is required", stringPointer("model"), "missing_model" // model 不能为空。
	}
	if len(request.Messages) == 0 {
		return "messages must contain at least one message", stringPointer("messages"), "missing_messages" // 至少需要一条消息。
	}

	for index, message := range request.Messages { // 逐条检查消息。
		if !supportedMessageRole(message.Role) {
			return fmt.Sprintf("messages[%d].role is not supported", index), stringPointer("messages"), "invalid_message_role" // role 不受支持。
		}
		switch messageContentStatus(message.Content) { // 判断 content 是否缺失或格式错误。
		case "missing":
			if message.Role == "assistant" &&
				(len(message.ToolCalls) > 0 || message.HasField("function_call")) {
				continue // assistant 发起工具调用时允许 content 为空。
			}
			return fmt.Sprintf("messages[%d].content is required", index), stringPointer("messages"), "missing_message_content"
		case "invalid":
			return fmt.Sprintf("messages[%d].content must be text or a non-empty content-parts array", index), stringPointer("messages"), "invalid_message_content"
		}
	}

	return "", nil, "" // 所有字段合法。
}

// messageContentStatus 返回 content 的状态：missing、invalid 或空字符串。
func messageContentStatus(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw) // 去掉 JSON 前后的空白。
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "missing" // 没有字段内容或值为 null。
	}

	if raw[0] == '"' { // content 是字符串。
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "invalid" // 字符串 JSON 无法解析。
		}
		if strings.TrimSpace(text) == "" {
			return "missing" // 空字符串视为缺失。
		}
		return "" // 非空文本合法。
	}

	if raw[0] != '[' {
		return "invalid" // 非字符串内容必须是数组。
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "invalid" // 内容块数组格式错误。
	}
	if len(parts) == 0 {
		return "missing" // 空内容块数组视为缺失。
	}

	for _, rawPart := range parts { // 检查每个内容块。
		var part struct {
			Type string `json:"type"` // 只提取内容块的 type。
		}
		if err := json.Unmarshal(rawPart, &part); err != nil || strings.TrimSpace(part.Type) == "" {
			return "invalid" // 内容块必须是合法对象并包含非空 type。
		}
	}
	return "" // 内容块数组合法。
}

// supportedMessageRole 判断消息角色是否受支持。
func supportedMessageRole(role string) bool {
	switch role {
	case "system", "developer", "user", "assistant", "tool":
		return true // 网关支持的角色。
	default:
		return false // 其他角色不支持。
	}
}

// writeReliabilityError 将内部可靠性错误转换成 HTTP API 错误。
func writeReliabilityError(c *gin.Context, failure *reliability.Failure) {
	if failure == nil {
		return // 没有错误，无需处理。
	}
	if failure.Category == reliability.CategoryCanceled && c.Request.Context().Err() != nil {
		return // 客户端已经断开时不再尝试写响应。
	}

	switch failure.Category {
	case reliability.CategoryTimeout:
		writeAPIError(c, http.StatusGatewayTimeout, "upstream request timed out", "upstream_error", nil, "upstream_timeout") // 上游超时返回 504。

	case reliability.CategoryUnsupportedCapability:
		writeAPIError(
			c,
			http.StatusBadRequest,
			"no configured provider supports a requested capability",
			"invalid_request_error",
			nil,
			"unsupported_provider_capability",
		) // Provider 不支持请求能力，返回 400。

	case reliability.CategoryUnsupportedResponse:
		writeAPIError(
			c,
			http.StatusBadGateway,
			"upstream returned output this gateway cannot represent",
			"upstream_error",
			nil,
			"unsupported_upstream_response",
		) // 上游响应无法转换，返回 502。

	case reliability.CategoryUpstreamProtocol:
		switch {
		case errors.Is(failure, provider.ErrResponseTooLarge):
			writeAPIError(c, http.StatusBadGateway, "upstream response exceeded the size limit", "upstream_error", nil, "upstream_response_too_large") // 响应体过大。
		case errors.Is(failure, provider.ErrInvalidResponse):
			writeAPIError(c, http.StatusBadGateway, "upstream returned an invalid response", "upstream_error", nil, "invalid_upstream_response") // 响应不是合法格式。
		default:
			writeAPIError(c, http.StatusBadGateway, "upstream returned an unsupported response", "upstream_error", nil, "upstream_protocol_error") // 其他协议错误。
		}

	case reliability.CategoryUpstream4xx:
		if failure.StatusCode == http.StatusBadRequest {
			writeAPIError(c, http.StatusBadRequest, "upstream rejected the request", "invalid_request_error", nil, "upstream_rejected_request") // 上游 400 转给客户端。
		} else {
			writeAPIError(c, http.StatusBadGateway, "upstream request failed", "upstream_error", nil, "upstream_http_error") // 其他上游 4xx 统一转 502。
		}

	case reliability.CategoryModelUnavailable:
		writeAPIError(c, http.StatusServiceUnavailable, "requested model is unavailable from configured providers", "upstream_error", stringPointer("model"), "model_unavailable") // 所有候选都无法提供模型。

	case reliability.CategoryUpstreamRateLimit:
		if failure.RetryAfterSet {
			seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second) // 向上取整为秒。
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))                // 告诉客户端等待多久。
		}
		writeAPIError(c, http.StatusTooManyRequests, "upstream rate limit exceeded", "upstream_error", nil, "upstream_rate_limited") // 上游限流返回 429。

	case reliability.CategoryKeyUnauthorized, reliability.CategoryKeyForbidden, reliability.CategoryUpstream5xx:
		writeAPIError(c, http.StatusBadGateway, "upstream request failed", "upstream_error", nil, "upstream_http_error") // Key 无效或上游 5xx 返回 502。

	case reliability.CategoryBreaker:
		if failure.RetryAfter > 0 {
			seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second) // 计算熔断剩余秒数。
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeAPIError(c, http.StatusServiceUnavailable, "provider circuit is temporarily open", "upstream_error", nil, "provider_circuit_open") // 熔断器打开返回 503。

	case reliability.CategoryKeyExhausted:
		if failure.RetryAfter > 0 {
			seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second) // 计算最早 Key 恢复时间。
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeAPIError(c, http.StatusServiceUnavailable, "provider has no available API key", "upstream_error", nil, "provider_keys_exhausted") // 没有可用 Key 返回 503。

	case reliability.CategoryQueue:
		switch failure.Queue {
		case reliability.QueueFull:
			writeAPIError(c, http.StatusServiceUnavailable, "provider queue is full", "upstream_error", nil, "provider_queue_full") // 等待队列已满。
		case reliability.QueueWaitTimeout:
			writeAPIError(c, http.StatusServiceUnavailable, "provider queue wait timed out", "upstream_error", nil, "provider_queue_timeout") // 排队等待超时。
		default:
			writeAPIError(c, http.StatusServiceUnavailable, "provider capacity is temporarily unavailable", "upstream_error", nil, "provider_queue_unavailable") // 其他队列错误。
		}

	case reliability.CategoryLocalValidation:
		writeAPIError(c, http.StatusInternalServerError, "provider request could not be prepared", "server_error", nil, "provider_request_error") // 网关本地 Provider 配置错误。

	case reliability.CategoryCanceled:
		writeAPIError(c, http.StatusGatewayTimeout, "upstream request was canceled", "upstream_error", nil, "upstream_canceled") // 非客户端断开导致的取消。

	default:
		writeAPIError(c, http.StatusBadGateway, "upstream is unavailable", "upstream_error", nil, "upstream_unavailable") // 未分类上游错误统一返回 502。
	}
}
