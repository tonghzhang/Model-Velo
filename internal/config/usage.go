package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

var usageConsumerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

const (
	usageDeadLetterMaxLenEnv = "MODEL_VELO_USAGE_DEAD_LETTER_MAX_LEN"
	legacyUsageMaxLenEnv     = "MODEL_VELO_USAGE_STREAM_MAX_LEN"
	usageEmitTimeoutEnv      = "MODEL_VELO_USAGE_EMIT_TIMEOUT"
	usageGroupEnv            = "MODEL_VELO_USAGE_GROUP"
	usageConsumerEnv         = "MODEL_VELO_USAGE_CONSUMER"
	usageBatchSizeEnv        = "MODEL_VELO_USAGE_BATCH_SIZE"
	usageReadBlockEnv        = "MODEL_VELO_USAGE_READ_BLOCK"
	usageClaimIdleEnv        = "MODEL_VELO_USAGE_CLAIM_IDLE"
	usageMaxDeliveriesEnv    = "MODEL_VELO_USAGE_MAX_DELIVERIES"
	usageRetryBackoffEnv     = "MODEL_VELO_USAGE_RETRY_BACKOFF"
	usageWorkerTimeoutEnv    = "MODEL_VELO_USAGE_WORKER_TIMEOUT"
	usageEnforceStreamEnv    = "MODEL_VELO_USAGE_ENFORCE_STREAM"
	usageRetentionDaysEnv    = "MODEL_VELO_USAGE_RETENTION_DAYS"
	usageMaintenanceEnv      = "MODEL_VELO_USAGE_MAINTENANCE_INTERVAL"
	usageMaintenanceBatchEnv = "MODEL_VELO_USAGE_MAINTENANCE_BATCH_SIZE"
	usagePricingJSONEnv      = "MODEL_VELO_USAGE_PRICING_JSON"
	defaultDeadLetterMaxLen  = 100_000
	defaultUsageBatchSize    = 50
	defaultUsageDeliveries   = 5
	defaultUsageRetention    = 90
	defaultMaintenanceBatch  = 1_000
	maximumUsagePricingBytes = 256 << 10
)

const (
	defaultUsageEmitTimeout   = 200 * time.Millisecond
	defaultUsageReadBlock     = 2 * time.Second
	defaultUsageClaimIdle     = 30 * time.Second
	defaultUsageRetryBackoff  = 500 * time.Millisecond
	defaultUsageWorkerTimeout = 10 * time.Second
	defaultMaintenance        = time.Hour
)

type UsagePrice struct {
	ProviderID                   string `json:"provider"`
	Model                        string `json:"model"`
	Version                      string `json:"version"`
	EffectiveFrom                string `json:"effective_from,omitempty"`
	EffectiveUntil               string `json:"effective_until,omitempty"`
	InputUSDPerMillion           string `json:"input_usd_per_million"`
	OutputUSDPerMillion          string `json:"output_usd_per_million"`
	CachedReadUSDPerMillion      string `json:"cached_read_usd_per_million,omitempty"`
	CachedWriteUSDPerMillion     string `json:"cached_write_usd_per_million,omitempty"`
	AudioInputUSDPerMillion      string `json:"audio_input_usd_per_million,omitempty"`
	AudioOutputUSDPerMillion     string `json:"audio_output_usd_per_million,omitempty"`
	ImageInputUSDPerMillion      string `json:"image_input_usd_per_million,omitempty"`
	ReasoningOutputUSDPerMillion string `json:"reasoning_output_usd_per_million,omitempty"`
}

type Usage struct {
	StreamKey           string
	DeadLetterKey       string
	DeadLetterMaxLen    int64
	EmitTimeout         time.Duration
	Group               string
	Consumer            string
	BatchSize           int64
	ReadBlock           time.Duration
	ClaimIdle           time.Duration
	MaxDeliveries       int64
	RetryBackoff        time.Duration
	WorkerTimeout       time.Duration
	EnforceStreamUsage  bool
	RetentionDays       int
	MaintenanceInterval time.Duration
	MaintenanceBatch    int
	Pricing             []UsagePrice
}

func LoadUsage() (Usage, error) {
	environment := strings.ToLower(strings.TrimSpace(os.Getenv(environmentEnv)))
	if !environmentPattern.MatchString(environment) {
		return Usage{}, fmt.Errorf(
			"%s must contain 1 to 32 lowercase letters, digits, underscores, or hyphens",
			environmentEnv,
		)
	}

	deadLetterMaxLen, err := loadUsageDeadLetterMaxLen()
	if err != nil {
		return Usage{}, err
	}
	batchSize, err := loadInt64(usageBatchSizeEnv, defaultUsageBatchSize, false, 64)
	if err != nil {
		return Usage{}, err
	}
	maxDeliveries, err := loadInt64(usageMaxDeliveriesEnv, defaultUsageDeliveries, false, 64)
	if err != nil {
		return Usage{}, err
	}
	if deadLetterMaxLen > 10_000_000 {
		return Usage{}, fmt.Errorf("%s must not exceed 10000000", usageDeadLetterMaxLenEnv)
	}
	if batchSize > 1_000 {
		return Usage{}, fmt.Errorf("%s must not exceed 1000", usageBatchSizeEnv)
	}
	if maxDeliveries > 100 {
		return Usage{}, fmt.Errorf("%s must not exceed 100", usageMaxDeliveriesEnv)
	}

	emitTimeout, err := loadPositiveDuration(usageEmitTimeoutEnv, defaultUsageEmitTimeout)
	if err != nil {
		return Usage{}, err
	}
	readBlock, err := loadPositiveDuration(usageReadBlockEnv, defaultUsageReadBlock)
	if err != nil {
		return Usage{}, err
	}
	claimIdle, err := loadPositiveDuration(usageClaimIdleEnv, defaultUsageClaimIdle)
	if err != nil {
		return Usage{}, err
	}
	retryBackoff, err := loadPositiveDuration(usageRetryBackoffEnv, defaultUsageRetryBackoff)
	if err != nil {
		return Usage{}, err
	}
	workerTimeout, err := loadPositiveDuration(usageWorkerTimeoutEnv, defaultUsageWorkerTimeout)
	if err != nil {
		return Usage{}, err
	}
	maintenanceInterval, err := loadPositiveDuration(usageMaintenanceEnv, defaultMaintenance)
	if err != nil {
		return Usage{}, err
	}
	enforceStreamUsage, err := loadUsageBool(usageEnforceStreamEnv, true)
	if err != nil {
		return Usage{}, err
	}
	retentionDays, err := loadUsageBoundedInt(usageRetentionDaysEnv, defaultUsageRetention, 0, 3_650)
	if err != nil {
		return Usage{}, err
	}
	maintenanceBatch, err := loadUsageBoundedInt(
		usageMaintenanceBatchEnv,
		defaultMaintenanceBatch,
		1,
		10_000,
	)
	if err != nil {
		return Usage{}, err
	}
	pricing, err := loadUsagePricing()
	if err != nil {
		return Usage{}, err
	}

	group := strings.TrimSpace(os.Getenv(usageGroupEnv))
	if group == "" {
		group = "model-velo-usage-workers"
	}
	if !usageConsumerPattern.MatchString(group) {
		return Usage{}, fmt.Errorf("%s contains invalid characters", usageGroupEnv)
	}

	consumer := strings.TrimSpace(os.Getenv(usageConsumerEnv))
	if consumer == "" {
		host, hostErr := os.Hostname()
		if hostErr != nil || strings.TrimSpace(host) == "" {
			host = "worker"
		}
		consumer = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if !usageConsumerPattern.MatchString(consumer) {
		return Usage{}, fmt.Errorf("%s contains invalid characters", usageConsumerEnv)
	}

	streamKey := "model-velo:usage:v1:" + environment
	return Usage{
		StreamKey:           streamKey,
		DeadLetterKey:       streamKey + ":dead-letter",
		DeadLetterMaxLen:    deadLetterMaxLen,
		EmitTimeout:         emitTimeout,
		Group:               group,
		Consumer:            consumer,
		BatchSize:           batchSize,
		ReadBlock:           readBlock,
		ClaimIdle:           claimIdle,
		MaxDeliveries:       maxDeliveries,
		RetryBackoff:        retryBackoff,
		WorkerTimeout:       workerTimeout,
		EnforceStreamUsage:  enforceStreamUsage,
		RetentionDays:       retentionDays,
		MaintenanceInterval: maintenanceInterval,
		MaintenanceBatch:    maintenanceBatch,
		Pricing:             pricing,
	}, nil
}

func loadUsageDeadLetterMaxLen() (int64, error) {
	current := strings.TrimSpace(os.Getenv(usageDeadLetterMaxLenEnv))
	legacy := strings.TrimSpace(os.Getenv(legacyUsageMaxLenEnv))
	if current != "" && legacy != "" {
		return 0, fmt.Errorf(
			"%s and deprecated %s cannot both be set",
			usageDeadLetterMaxLenEnv,
			legacyUsageMaxLenEnv,
		)
	}
	if current != "" {
		return loadInt64(usageDeadLetterMaxLenEnv, defaultDeadLetterMaxLen, false, 64)
	}
	return loadInt64(legacyUsageMaxLenEnv, defaultDeadLetterMaxLen, false, 64)
}

func loadUsageBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	switch strings.ToLower(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

func loadUsageBoundedInt(name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	var value int
	if _, err := fmt.Sscan(raw, &value); err != nil || fmt.Sprint(value) != raw {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func loadUsagePricing() ([]UsagePrice, error) {
	raw := strings.TrimSpace(os.Getenv(usagePricingJSONEnv))
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maximumUsagePricingBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", usagePricingJSONEnv, maximumUsagePricingBytes)
	}

	var pricing []UsagePrice
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pricing); err != nil {
		return nil, fmt.Errorf("%s must be a valid pricing array: %w", usagePricingJSONEnv, err)
	}
	if err := rejectUsageTrailingJSON(decoder); err != nil {
		return nil, fmt.Errorf("%s must contain one pricing array: %w", usagePricingJSONEnv, err)
	}
	if len(pricing) > 10_000 {
		return nil, fmt.Errorf("%s must not contain more than 10000 prices", usagePricingJSONEnv)
	}
	return pricing, nil
}

func rejectUsageTrailingJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing JSON")
}
