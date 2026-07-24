package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"model-velo/internal/adminauth"
	"model-velo/internal/apikey"
	"model-velo/internal/controlplane"
	"model-velo/internal/gateway"
	"model-velo/internal/observability"
	"model-velo/internal/quota"
)

type ReadinessChecker interface {
	Check(context.Context) map[string]error
}

type RouterOption func(*routerSettings)

type routerSettings struct {
	readiness     ReadinessChecker
	metrics       *observability.Metrics
	metricsToken  string
	requestLogger *slog.Logger
	runtime       gateway.Source
	adminAuth     *adminauth.Manager
	tenantAdmin   *apikey.Manager
	controlPlane  *controlplane.Service
	quota         *quota.Manager
}

const (
	protocolKindKey   = "model-velo.protocol-kind"
	protocolAnthropic = "anthropic"
	anthropicPath     = "/v1/messages"
	anthropicVersion  = "2023-06-01"
)

func WithReadiness(checker ReadinessChecker) RouterOption {
	return func(settings *routerSettings) {
		settings.readiness = checker
	}
}

func WithMetrics(metrics *observability.Metrics, token string) RouterOption {
	return func(settings *routerSettings) {
		settings.metrics = metrics
		settings.metricsToken = token
	}
}

func WithRequestLogger(logger *slog.Logger) RouterOption {
	return func(settings *routerSettings) {
		settings.requestLogger = logger
	}
}

func WithRuntimeSource(source gateway.Source) RouterOption {
	return func(settings *routerSettings) {
		settings.runtime = source
	}
}

func WithAdminAPI(
	auth *adminauth.Manager,
	service *controlplane.Service,
	tenantAdmin *apikey.Manager,
) RouterOption {
	return func(settings *routerSettings) {
		settings.adminAuth = auth
		settings.controlPlane = service
		settings.tenantAdmin = tenantAdmin
	}
}

func WithQuota(manager *quota.Manager) RouterOption {
	return func(settings *routerSettings) {
		settings.quota = manager
	}
}

func requestSummaryMiddleware(
	logger *slog.Logger,
	metrics *observability.Metrics,
) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		startedAt := time.Now()
		if c.Request.URL.Path == anthropicPath {
			c.Set(protocolKindKey, protocolAnthropic)
			c.Header("request-id", requestIDFromContext(c.Request.Context()))
		}
		c.Set("model-velo.metrics", metrics)
		finishMetrics := metrics.BeginRequest()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		stream, _ := c.Get("model-velo.stream")
		isStream, _ := stream.(bool)
		status := c.Writer.Status()
		duration := time.Since(startedAt)
		finishMetrics(route, c.Request.Method, status, isStream, duration)

		attributes := []any{
			"request_id", requestIDFromContext(c.Request.Context()),
			"method", c.Request.Method,
			"route", route,
			"status", status,
			"duration_ms", duration.Milliseconds(),
			"stream", isStream,
		}
		if identity, ok := identityFromContext(c.Request.Context()); ok {
			attributes = append(attributes,
				"tenant_id", identity.TenantID,
				"api_key_id", identity.APIKeyID,
			)
		}
		if model, ok := c.Get("model-velo.model"); ok {
			attributes = append(attributes, "model", model)
		}
		logger.Info("request completed", attributes...)

		span := trace.SpanFromContext(c.Request.Context())
		span.SetAttributes(
			attribute.String("model_velo.request_id", requestIDFromContext(c.Request.Context())),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
			attribute.Bool("model_velo.stream", isStream),
		)
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	}
}

func safeRecoveryMiddleware(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		logger.Error(
			"request panic recovered",
			"request_id", requestIDFromContext(c.Request.Context()),
			"method", c.Request.Method,
			"route", route,
			"stack", string(debug.Stack()),
		)
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

func protocolKind(c *gin.Context) string {
	value, _ := c.Get(protocolKindKey)
	kind, _ := value.(string)
	return kind
}

func routerMetrics(c *gin.Context) *observability.Metrics {
	value, exists := c.Get("model-velo.metrics")
	if !exists {
		return nil
	}
	metrics, _ := value.(*observability.Metrics)
	return metrics
}

func readinessHandler(checker ReadinessChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		if checker == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "not_ready",
				"checks": gin.H{"configuration": "missing"},
			})
			return
		}
		checks := checker.Check(c.Request.Context())
		public := make(map[string]string, len(checks))
		ready := true
		for name, err := range checks {
			if err == nil {
				public[name] = "ok"
				continue
			}
			public[name] = "unavailable"
			ready = false
		}
		status := http.StatusOK
		state := "ready"
		if !ready {
			status = http.StatusServiceUnavailable
			state = "not_ready"
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(status, gin.H{"status": state, "checks": public})
	}
}

func metricsHandler(metrics *observability.Metrics, token string) gin.HandlerFunc {
	handler := metrics.Handler(strings.TrimSpace(token))
	return gin.WrapH(handler)
}
