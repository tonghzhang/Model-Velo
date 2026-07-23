package httpapi

import (
	"bytes"
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
	access       AccessController
	limiter      RateLimiter
	cache        ResponseCache
	routes       *routing.Router
	orchestrator *reliability.Orchestrator
}

type ResponseCache interface {
	Lookup(ctx context.Context, tenantID, model string, requestBody []byte) (responsecache.Result, error)
	Store(ctx context.Context, tenantID, model string, requestBody, responseBody []byte) error
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

	request, err := provider.ParseChatRequest(requestBody)
	if err != nil {
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
	requiredCapabilities, err := request.RequiredCapabilities()
	if err != nil {
		writeAPIError(
			c,
			http.StatusBadRequest,
			"request uses a capability that is not supported by this gateway",
			"invalid_request_error",
			nil,
			"unsupported_request_capability",
		)
		return
	}
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

	routePlan, err := h.routes.Plan(model, requiredCapabilities)
	if err != nil {
		if errors.Is(err, routing.ErrCapabilityUnavailable) {
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
	if request.Stream {
		h.stream(c, request, routePlan)
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

	execution, failure := h.orchestrator.Execute(c.Request.Context(), reliability.ExecutionInput{
		RequestID: requestIDFromContext(c.Request.Context()),
		Request:   request,
		Plan:      routePlan,
	})
	if failure != nil {
		writeReliabilityError(c, failure)
		return
	}
	responseBody := execution.Body
	if cacheResult.Status == responsecache.StatusMiss && execution.Fallbacks == 0 {
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

func (h chatHandler) stream(c *gin.Context, request provider.ChatRequest, plan routing.Plan) {
	flusher, ok := chatStreamFlusher(c.Writer)
	if !ok {
		writeAPIError(
			c,
			http.StatusInternalServerError,
			"streaming is unavailable on this HTTP connection",
			"server_error",
			nil,
			"streaming_unavailable",
		)
		return
	}

	prepared, failure := h.orchestrator.OpenStream(c.Request.Context(), reliability.ExecutionInput{
		RequestID: requestIDFromContext(c.Request.Context()),
		Request:   request,
		Plan:      plan,
	})
	if failure != nil {
		writeReliabilityError(c, failure)
		return
	}
	defer prepared.Abort(context.Canceled)
	if err := c.Request.Context().Err(); err != nil {
		prepared.Abort(err)
		return
	}

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	writeCacheStatusHeader(c, responsecache.StatusBypass)

	if err := writeChatStreamEvent(c.Writer, prepared.FirstEvent); err != nil {
		prepared.Abort(err)
		return
	}
	flusher.Flush()

	for {
		event, err := prepared.Next()
		if err != nil {
			failure := prepared.FinishError(c.Request.Context(), err)
			if failure != nil && failure.Category != reliability.CategoryCanceled {
				log.Printf(
					"stream interrupted request_id=%s provider=%s category=%s",
					requestIDFromContext(c.Request.Context()),
					failure.ProviderID,
					failure.Category,
				)
			}
			return
		}
		if err := writeChatStreamEvent(c.Writer, event); err != nil {
			prepared.Abort(err)
			return
		}
		flusher.Flush()
		if event.Done {
			prepared.Finish(nil)
			return
		}
	}
}

type responseWriterUnwrapper interface {
	Unwrap() http.ResponseWriter
}

func chatStreamFlusher(writer http.ResponseWriter) (http.Flusher, bool) {
	for range 8 {
		unwrapper, ok := writer.(responseWriterUnwrapper)
		if !ok {
			break
		}
		writer = unwrapper.Unwrap()
		if writer == nil {
			return nil, false
		}
	}
	flusher, ok := writer.(http.Flusher)
	return flusher, ok
}

func writeChatStreamEvent(writer io.Writer, event provider.ChatStreamEvent) error {
	payload := []byte("[DONE]")
	if !event.Done {
		var compact bytes.Buffer
		compact.Grow(len(event.Data))
		if err := json.Compact(&compact, event.Data); err != nil {
			return provider.ErrInvalidStream
		}
		payload = compact.Bytes()
	}

	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	written, err := writer.Write(frame)
	if err == nil && written != len(frame) {
		return io.ErrShortWrite
	}
	return err
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

func validateChatRequest(request provider.ChatRequest) (message string, param *string, code string) {
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
		switch messageContentStatus(message.Content) {
		case "missing":
			if message.Role == "assistant" && (message.HasField("tool_calls") || message.HasField("function_call")) {
				continue
			}
			return fmt.Sprintf("messages[%d].content is required", index), stringPointer("messages"), "missing_message_content"
		case "invalid":
			return fmt.Sprintf("messages[%d].content must be text or a non-empty content-parts array", index), stringPointer("messages"), "invalid_message_content"
		}
	}

	return "", nil, ""
}

func messageContentStatus(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "missing"
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "invalid"
		}
		if strings.TrimSpace(text) == "" {
			return "missing"
		}
		return ""
	}
	if raw[0] != '[' {
		return "invalid"
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "invalid"
	}
	if len(parts) == 0 {
		return "missing"
	}
	for _, rawPart := range parts {
		var part struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil || strings.TrimSpace(part.Type) == "" {
			return "invalid"
		}
	}
	return ""
}

func supportedMessageRole(role string) bool {
	switch role {
	case "system", "developer", "user", "assistant", "tool":
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
	case reliability.CategoryUnsupportedCapability:
		writeAPIError(
			c,
			http.StatusBadRequest,
			"no configured provider supports a requested capability",
			"invalid_request_error",
			nil,
			"unsupported_provider_capability",
		)
	case reliability.CategoryUnsupportedResponse:
		writeAPIError(
			c,
			http.StatusBadGateway,
			"upstream returned output this gateway cannot represent",
			"upstream_error",
			nil,
			"unsupported_upstream_response",
		)
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
	case reliability.CategoryModelUnavailable:
		writeAPIError(c, http.StatusServiceUnavailable, "requested model is unavailable from configured providers", "upstream_error", stringPointer("model"), "model_unavailable")
	case reliability.CategoryUpstreamRateLimit:
		if failure.RetryAfterSet {
			seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second)
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeAPIError(c, http.StatusTooManyRequests, "upstream rate limit exceeded", "upstream_error", nil, "upstream_rate_limited")
	case reliability.CategoryKeyUnauthorized, reliability.CategoryKeyForbidden, reliability.CategoryUpstream5xx:
		writeAPIError(c, http.StatusBadGateway, "upstream request failed", "upstream_error", nil, "upstream_http_error")
	case reliability.CategoryBreaker:
		if failure.RetryAfter > 0 {
			seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second)
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeAPIError(c, http.StatusServiceUnavailable, "provider circuit is temporarily open", "upstream_error", nil, "provider_circuit_open")
	case reliability.CategoryKeyExhausted:
		if failure.RetryAfter > 0 {
			seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second)
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeAPIError(c, http.StatusServiceUnavailable, "provider has no available API key", "upstream_error", nil, "provider_keys_exhausted")
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
