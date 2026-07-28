package observability

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"model-velo/internal/gateway"
	"model-velo/internal/reliability"
	"model-velo/internal/usage"
)

type Metrics struct {
	registry          *prometheus.Registry
	requests          *prometheus.CounterVec
	requestDuration   *prometheus.HistogramVec
	requestErrors     *prometheus.CounterVec
	stageDuration     *prometheus.HistogramVec
	inFlight          prometheus.Gauge
	providerAttempts  *prometheus.CounterVec
	providerDuration  *prometheus.HistogramVec
	retries           *prometheus.CounterVec
	fallbacks         *prometheus.CounterVec
	cache             *prometheus.CounterVec
	rateLimit         *prometheus.CounterVec
	auth              *prometheus.CounterVec
	authCache         *prometheus.CounterVec
	authCacheDuration *prometheus.HistogramVec
	authCacheEvents   *prometheus.CounterVec
	authFallback      *prometheus.CounterVec
	authDBQueries     prometheus.Histogram
	authorization     *prometheus.CounterVec
	usageDelivery     *prometheus.CounterVec
	quota             *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_http_requests_total",
			Help: "Completed HTTP requests.",
		}, []string{"route", "method", "status", "stream"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "model_velo_http_request_duration_seconds",
			Help:    "End-to-end HTTP request duration.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"route", "method", "status", "stream"}),
		requestErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_http_errors_total",
			Help: "Completed HTTP errors by stable gateway error code.",
		}, []string{"route", "status", "code"}),
		stageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "model_velo_request_stage_duration_seconds",
			Help: "Duration of bounded gateway request stages.",
			Buckets: []float64{
				0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005,
				0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
			},
		}, []string{"stage", "result", "provider"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "model_velo_http_in_flight",
			Help: "Current in-flight HTTP requests.",
		}),
		providerAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_provider_attempts_total",
			Help: "Provider calls including retries.",
		}, []string{"provider", "result", "category"}),
		providerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "model_velo_provider_attempt_duration_seconds",
			Help:    "Duration of individual provider calls.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"provider", "result"}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_retries_total",
			Help: "Provider retry attempts.",
		}, []string{"provider", "category"}),
		fallbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_fallbacks_total",
			Help: "Provider fallback transitions.",
		}, []string{"result"}),
		cache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_cache_operations_total",
			Help: "Response cache outcomes.",
		}, []string{"operation", "result"}),
		rateLimit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_rate_limit_decisions_total",
			Help: "Tenant rate-limit decisions.",
		}, []string{"result"}),
		auth: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_authentication_total",
			Help: "Gateway authentication outcomes.",
		}, []string{"result"}),
		authCache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_auth_cache_lookups_total",
			Help: "Authentication cache lookups by bounded layer and outcome.",
		}, []string{"layer", "result"}),
		authCacheDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "model_velo_auth_cache_lookup_duration_seconds",
			Help: "Authentication cache lookup duration by bounded layer and outcome.",
			Buckets: []float64{
				0.00001, 0.000025, 0.00005, 0.0001, 0.00025,
				0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025,
			},
		}, []string{"layer", "result"}),
		authCacheEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_auth_cache_events_total",
			Help: "Authentication cache writes, invalidations, evictions, and subscription outcomes.",
		}, []string{"event", "result"}),
		authFallback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_auth_postgres_fallback_total",
			Help: "Authentication snapshot PostgreSQL fallback outcomes.",
		}, []string{"result"}),
		authDBQueries: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "model_velo_auth_postgres_queries",
			Help:    "Physical PostgreSQL queries performed by one authentication request.",
			Buckets: []float64{0, 1, 2, 3, 4, 5},
		}),
		authorization: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_model_authorization_total",
			Help: "Model authorization decisions made from authentication snapshots.",
		}, []string{"result"}),
		usageDelivery: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_usage_delivery_total",
			Help: "Usage delivery outcomes.",
		}, []string{"stage", "result"}),
		quota: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "model_velo_quota_decisions_total",
			Help: "Token and spending quota decisions.",
		}, []string{"result"}),
	}
	metrics.registry.MustRegister(
		metrics.requests,
		metrics.requestDuration,
		metrics.requestErrors,
		metrics.stageDuration,
		metrics.inFlight,
		metrics.providerAttempts,
		metrics.providerDuration,
		metrics.retries,
		metrics.fallbacks,
		metrics.cache,
		metrics.rateLimit,
		metrics.auth,
		metrics.authCache,
		metrics.authCacheDuration,
		metrics.authCacheEvents,
		metrics.authFallback,
		metrics.authDBQueries,
		metrics.authorization,
		metrics.usageDelivery,
		metrics.quota,
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)
	return metrics
}

func (metrics *Metrics) RegisterRuntime(source gateway.Source) error {
	if metrics == nil || source == nil {
		return nil
	}
	return metrics.registry.Register(newRuntimeCollector(source))
}

type UsageWorkerSource interface {
	Stats() usage.WorkerStats
}

func (metrics *Metrics) RegisterUsageWorker(source UsageWorkerSource) error {
	if metrics == nil || source == nil {
		return nil
	}
	return metrics.registry.Register(newUsageWorkerCollector(source))
}

func (metrics *Metrics) Handler(token string) http.Handler {
	handler := promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
	if token == "" {
		return handler
	}
	expectedAuthorization := []byte("Bearer " + token)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Values("Authorization")
		authorized := len(authorization) == 1 &&
			subtle.ConstantTimeCompare(
				[]byte(authorization[0]),
				expectedAuthorization,
			) == 1
		if !authorized {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(writer, request)
	})
}

func (metrics *Metrics) BeginRequest() func(route, method string, status int, stream bool, duration time.Duration) {
	if metrics == nil {
		return func(string, string, int, bool, time.Duration) {}
	}
	metrics.inFlight.Inc()
	return func(route, method string, status int, stream bool, duration time.Duration) {
		metrics.inFlight.Dec()
		labels := []string{route, method, strconv.Itoa(status), strconv.FormatBool(stream)}
		metrics.requests.WithLabelValues(labels...).Inc()
		metrics.requestDuration.WithLabelValues(labels...).Observe(duration.Seconds())
	}
}

func (metrics *Metrics) Authentication(result string) {
	if metrics != nil {
		metrics.auth.WithLabelValues(result).Inc()
	}
}

func (metrics *Metrics) AuthCacheLookup(
	layer string,
	result string,
	duration time.Duration,
) {
	if metrics == nil {
		return
	}
	metrics.authCache.WithLabelValues(layer, result).Inc()
	metrics.authCacheDuration.WithLabelValues(layer, result).
		Observe(duration.Seconds())
}

func (metrics *Metrics) AuthCacheEvent(event, result string) {
	if metrics != nil {
		metrics.authCacheEvents.WithLabelValues(event, result).Inc()
	}
}

func (metrics *Metrics) AuthPostgresFallback(result string) {
	if metrics != nil {
		metrics.authFallback.WithLabelValues(result).Inc()
	}
}

func (metrics *Metrics) AuthDatabaseQueries(count int) {
	if metrics != nil {
		metrics.authDBQueries.Observe(float64(count))
	}
}

func (metrics *Metrics) ModelAuthorization(result string) {
	if metrics != nil {
		metrics.authorization.WithLabelValues(result).Inc()
	}
}

func (metrics *Metrics) HTTPError(route string, status int, code string) {
	if metrics == nil || status < http.StatusBadRequest {
		return
	}
	if code == "" {
		code = "unclassified"
	}
	metrics.requestErrors.WithLabelValues(route, strconv.Itoa(status), code).Inc()
}

func (metrics *Metrics) RequestStage(
	stage, result, provider string,
	duration time.Duration,
) {
	if metrics != nil {
		metrics.stageDuration.WithLabelValues(stage, result, provider).
			Observe(duration.Seconds())
	}
}

func (metrics *Metrics) ObserveQueueWait(
	provider, result string,
	duration time.Duration,
) {
	metrics.RequestStage("provider_queue", result, provider, duration)
}

func (metrics *Metrics) RateLimit(result string) {
	if metrics != nil {
		metrics.rateLimit.WithLabelValues(result).Inc()
	}
}

func (metrics *Metrics) Cache(operation, result string) {
	if metrics != nil {
		metrics.cache.WithLabelValues(operation, result).Inc()
	}
}

func (metrics *Metrics) ProviderAttempt(
	provider, result, category string,
	duration time.Duration,
	retry bool,
) {
	if metrics == nil {
		return
	}
	metrics.providerAttempts.WithLabelValues(provider, result, category).Inc()
	metrics.providerDuration.WithLabelValues(provider, result).Observe(duration.Seconds())
	metrics.RequestStage("provider_call", result, provider, duration)
	if retry {
		metrics.retries.WithLabelValues(provider, category).Inc()
	}
}

func (metrics *Metrics) Fallbacks(count int, result string) {
	if metrics == nil {
		return
	}
	for range count {
		metrics.fallbacks.WithLabelValues(result).Inc()
	}
}

func (metrics *Metrics) UsageDelivery(stage, result string) {
	if metrics != nil {
		metrics.usageDelivery.WithLabelValues(stage, result).Inc()
	}
}

func (metrics *Metrics) Quota(result string) {
	if metrics != nil {
		metrics.quota.WithLabelValues(result).Inc()
	}
}

type runtimeCollector struct {
	source       gateway.Source
	breakerState *prometheus.Desc
	queueActive  *prometheus.Desc
	queueWaiting *prometheus.Desc
	queueLimit   *prometheus.Desc
	keyState     *prometheus.Desc
}

type usageWorkerCollector struct {
	source  UsageWorkerSource
	events  *prometheus.Desc
	pending *prometheus.Desc
}

func newUsageWorkerCollector(source UsageWorkerSource) *usageWorkerCollector {
	return &usageWorkerCollector{
		source: source,
		events: prometheus.NewDesc(
			"model_velo_usage_worker_events_total",
			"Usage worker cumulative processing outcomes.",
			[]string{"result"}, nil,
		),
		pending: prometheus.NewDesc(
			"model_velo_usage_worker_pending",
			"Current Redis consumer-group pending entry count.",
			nil, nil,
		),
	}
}

func (collector *usageWorkerCollector) Describe(output chan<- *prometheus.Desc) {
	output <- collector.events
	output <- collector.pending
}

func (collector *usageWorkerCollector) Collect(output chan<- prometheus.Metric) {
	stats := collector.source.Stats()
	values := []struct {
		result string
		value  int64
	}{
		{"read", stats.Read},
		{"claimed", stats.Claimed},
		{"stored", stats.Stored},
		{"duplicate", stats.Duplicates},
		{"failed", stats.Failed},
		{"dead_lettered", stats.DeadLettered},
		{"cleaned", stats.Cleaned},
		{"relayed", stats.Relayed},
	}
	for _, value := range values {
		output <- prometheus.MustNewConstMetric(
			collector.events,
			prometheus.CounterValue,
			float64(value.value),
			value.result,
		)
	}
	source, ok := collector.source.(interface {
		Pending(context.Context) (int64, error)
	})
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	pending, err := source.Pending(ctx)
	cancel()
	if err == nil {
		output <- prometheus.MustNewConstMetric(
			collector.pending,
			prometheus.GaugeValue,
			float64(pending),
		)
	}
}

func newRuntimeCollector(source gateway.Source) *runtimeCollector {
	return &runtimeCollector{
		source: source,
		breakerState: prometheus.NewDesc(
			"model_velo_provider_breaker_state",
			"Current circuit-breaker state as a labeled one-hot gauge.",
			[]string{"provider", "state"}, nil,
		),
		queueActive: prometheus.NewDesc(
			"model_velo_provider_queue_active",
			"Current active provider requests.", []string{"provider"}, nil,
		),
		queueWaiting: prometheus.NewDesc(
			"model_velo_provider_queue_waiting",
			"Current waiting provider requests.", []string{"provider"}, nil,
		),
		queueLimit: prometheus.NewDesc(
			"model_velo_provider_queue_limit",
			"Configured provider queue limits.", []string{"provider", "kind"}, nil,
		),
		keyState: prometheus.NewDesc(
			"model_velo_provider_key_state",
			"Current provider-key state as a labeled one-hot gauge.",
			[]string{"provider", "key_id", "state"}, nil,
		),
	}
}

func (collector *runtimeCollector) Describe(output chan<- *prometheus.Desc) {
	output <- collector.breakerState
	output <- collector.queueActive
	output <- collector.queueWaiting
	output <- collector.queueLimit
	output <- collector.keyState
}

func (collector *runtimeCollector) Collect(output chan<- prometheus.Metric) {
	snapshot := collector.source.Current()
	if snapshot == nil {
		return
	}
	for _, breaker := range snapshot.Breakers.Snapshots() {
		for _, state := range []reliability.BreakerState{
			reliability.StateClosed, reliability.StateOpen, reliability.StateHalfOpen,
		} {
			value := float64(0)
			if breaker.State == state {
				value = 1
			}
			output <- prometheus.MustNewConstMetric(
				collector.breakerState, prometheus.GaugeValue, value,
				breaker.ProviderID, string(state),
			)
		}
	}
	for _, queue := range snapshot.Queues.Snapshots() {
		output <- prometheus.MustNewConstMetric(
			collector.queueActive, prometheus.GaugeValue,
			float64(queue.Active), queue.ProviderID,
		)
		output <- prometheus.MustNewConstMetric(
			collector.queueWaiting, prometheus.GaugeValue,
			float64(queue.Waiting), queue.ProviderID,
		)
		output <- prometheus.MustNewConstMetric(
			collector.queueLimit, prometheus.GaugeValue,
			float64(queue.MaxInFlight), queue.ProviderID, "in_flight",
		)
		output <- prometheus.MustNewConstMetric(
			collector.queueLimit, prometheus.GaugeValue,
			float64(queue.MaxWaiting), queue.ProviderID, "waiting",
		)
	}
	if snapshot.Keys == nil {
		return
	}
	for _, key := range snapshot.Keys.Snapshots() {
		for _, state := range []reliability.ProviderKeyState{
			reliability.ProviderKeyAvailable,
			reliability.ProviderKeyCooling,
			reliability.ProviderKeyDisabled,
		} {
			value := float64(0)
			if key.State == state {
				value = 1
			}
			output <- prometheus.MustNewConstMetric(
				collector.keyState, prometheus.GaugeValue, value,
				key.ProviderID, key.KeyID, string(state),
			)
		}
	}
}
