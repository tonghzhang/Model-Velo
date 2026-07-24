package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	logFormatEnv          = "MODEL_VELO_LOG_FORMAT"
	logLevelEnv           = "MODEL_VELO_LOG_LEVEL"
	serviceNameEnv        = "MODEL_VELO_SERVICE_NAME"
	metricsTokenEnv       = "MODEL_VELO_METRICS_TOKEN"
	workerMetricsAddrEnv  = "MODEL_VELO_WORKER_METRICS_ADDR"
	otelEndpointEnv       = "MODEL_VELO_OTEL_EXPORTER_OTLP_ENDPOINT"
	otelInsecureEnv       = "MODEL_VELO_OTEL_EXPORTER_OTLP_INSECURE"
	otelSampleRatioEnv    = "MODEL_VELO_OTEL_SAMPLE_RATIO"
	readinessTimeoutEnv   = "MODEL_VELO_READINESS_TIMEOUT"
	defaultReadinessLimit = time.Second
)

type Observability struct {
	LogFormat         string
	LogLevel          string
	ServiceName       string
	MetricsToken      string
	WorkerMetricsAddr string
	OTELEndpoint      string
	OTELInsecure      bool
	OTELSampleRatio   float64
	ReadinessTimeout  time.Duration
}

func LoadObservability() (Observability, error) {
	settings := Observability{
		LogFormat:         strings.ToLower(strings.TrimSpace(os.Getenv(logFormatEnv))),
		LogLevel:          strings.ToLower(strings.TrimSpace(os.Getenv(logLevelEnv))),
		ServiceName:       strings.TrimSpace(os.Getenv(serviceNameEnv)),
		MetricsToken:      strings.TrimSpace(os.Getenv(metricsTokenEnv)),
		WorkerMetricsAddr: strings.TrimSpace(os.Getenv(workerMetricsAddrEnv)),
		OTELEndpoint:      strings.TrimSpace(os.Getenv(otelEndpointEnv)),
		OTELSampleRatio:   0.1,
	}
	if settings.LogFormat == "" {
		settings.LogFormat = "json"
	}
	if settings.LogLevel == "" {
		settings.LogLevel = "info"
	}
	if settings.ServiceName == "" {
		settings.ServiceName = "model-velo"
	}
	if settings.WorkerMetricsAddr == "" {
		settings.WorkerMetricsAddr = ":9091"
	}
	if settings.LogFormat != "json" && settings.LogFormat != "text" {
		return Observability{}, fmt.Errorf("%s must be json or text", logFormatEnv)
	}
	switch settings.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Observability{}, fmt.Errorf("%s must be debug, info, warn, or error", logLevelEnv)
	}
	if len(settings.ServiceName) > 100 {
		return Observability{}, fmt.Errorf("%s must not exceed 100 characters", serviceNameEnv)
	}
	if settings.MetricsToken != "" && len(settings.MetricsToken) < 32 {
		return Observability{}, fmt.Errorf("%s must contain at least 32 characters", metricsTokenEnv)
	}
	if _, _, err := net.SplitHostPort(settings.WorkerMetricsAddr); err != nil {
		return Observability{}, fmt.Errorf("%s must be a host:port address", workerMetricsAddrEnv)
	}
	if raw := strings.TrimSpace(os.Getenv(otelInsecureEnv)); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Observability{}, fmt.Errorf("%s must be true or false", otelInsecureEnv)
		}
		settings.OTELInsecure = value
	}
	if raw := strings.TrimSpace(os.Getenv(otelSampleRatioEnv)); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 || value > 1 {
			return Observability{}, fmt.Errorf("%s must be between 0 and 1", otelSampleRatioEnv)
		}
		settings.OTELSampleRatio = value
	}
	readinessTimeout, err := loadPositiveDuration(readinessTimeoutEnv, defaultReadinessLimit)
	if err != nil {
		return Observability{}, err
	}
	settings.ReadinessTimeout = readinessTimeout
	return settings, nil
}
