package usage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SchemaVersion         = 2
	minimumSchemaVersion  = 1
	maximumRawUsageBytes  = 64 << 10
	maximumTokenCount     = int64(1_000_000_000)
	maximumUsageCaveatLen = 256
)

type Status string

const (
	StatusSuccess           Status = "success"
	StatusCacheHit          Status = "cache_hit"
	StatusFailed            Status = "failed"
	StatusCanceled          Status = "cancelled"
	StatusStreamCompleted   Status = "stream_completed"
	StatusStreamInterrupted Status = "stream_interrupted"
)

type UsageSource string

const (
	UsageSourceUnknown     UsageSource = "unknown"
	UsageSourceProvider    UsageSource = "provider"
	UsageSourceCacheReplay UsageSource = "cache_replay"
)

type InputTokenDetails struct {
	Text        int64 `json:"text,omitempty"`
	Audio       int64 `json:"audio,omitempty"`
	Image       int64 `json:"image,omitempty"`
	CachedRead  int64 `json:"cached_read,omitempty"`
	CachedWrite int64 `json:"cached_write,omitempty"`
}

type OutputTokenDetails struct {
	Text               int64 `json:"text,omitempty"`
	Audio              int64 `json:"audio,omitempty"`
	Reasoning          int64 `json:"reasoning,omitempty"`
	AcceptedPrediction int64 `json:"accepted_prediction,omitempty"`
	RejectedPrediction int64 `json:"rejected_prediction,omitempty"`
}

type ReportedCost struct {
	InputNanoUSD  *int64 `json:"input_nano_usd,omitempty"`
	OutputNanoUSD *int64 `json:"output_nano_usd,omitempty"`
	TotalNanoUSD  int64  `json:"total_nano_usd"`
	Currency      string `json:"currency"`
}

type TokenUsage struct {
	Input         int64               `json:"input"`
	Output        int64               `json:"output"`
	Total         int64               `json:"total"`
	InputDetails  *InputTokenDetails  `json:"input_details,omitempty"`
	OutputDetails *OutputTokenDetails `json:"output_details,omitempty"`
	ReportedCost  *ReportedCost       `json:"reported_cost,omitempty"`
	Raw           json.RawMessage     `json:"raw,omitempty"`
}

type Event struct {
	SchemaVersion  int         `json:"schema_version"`
	EventID        string      `json:"event_id"`
	RequestID      string      `json:"request_id"`
	TenantID       string      `json:"tenant_id"`
	APIKeyID       string      `json:"api_key_id,omitempty"`
	RequestedModel string      `json:"requested_model"`
	ProviderID     string      `json:"provider_id,omitempty"`
	UpstreamModel  string      `json:"upstream_model,omitempty"`
	CacheStatus    string      `json:"cache_status"`
	Stream         bool        `json:"stream"`
	Attempts       int         `json:"attempts"`
	Retries        int         `json:"retries"`
	Fallbacks      int         `json:"fallbacks"`
	Usage          *TokenUsage `json:"usage,omitempty"`
	UsageSource    UsageSource `json:"usage_source,omitempty"`
	UsageCaveat    string      `json:"usage_caveat,omitempty"`
	FinishReason   string      `json:"finish_reason,omitempty"`
	Status         Status      `json:"status"`
	ErrorCategory  string      `json:"error_category,omitempty"`
	ErrorCode      string      `json:"error_code,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	EndedAt        time.Time   `json:"ended_at"`
	LatencyMS      int64       `json:"latency_ms"`
	FirstTokenMS   *int64      `json:"first_token_ms,omitempty"`
}

type NewEventInput struct {
	RequestID      string
	TenantID       string
	APIKeyID       string
	RequestedModel string
	Stream         bool
	StartedAt      time.Time
}

type Outcome struct {
	Status        Status
	ErrorCategory string
	ErrorCode     string
	EndedAt       time.Time
}

type responseMetadata struct {
	usage        *TokenUsage
	usageCaveat  string
	finishReason string
	hasChunk     bool
}

func newEvent(input NewEventInput) (Event, error) {
	eventID, err := generateEventID()
	if err != nil {
		return Event{}, err
	}
	startedAt := input.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	event := Event{
		SchemaVersion:  SchemaVersion,
		EventID:        eventID,
		RequestID:      strings.TrimSpace(input.RequestID),
		TenantID:       strings.TrimSpace(input.TenantID),
		APIKeyID:       strings.TrimSpace(input.APIKeyID),
		RequestedModel: strings.TrimSpace(input.RequestedModel),
		CacheStatus:    "bypass",
		Stream:         input.Stream,
		UsageSource:    UsageSourceUnknown,
		StartedAt:      startedAt,
	}
	switch {
	case !validRequiredText(event.RequestID, 128):
		return Event{}, errors.New("usage request ID is invalid")
	case !validRequiredText(event.TenantID, 128):
		return Event{}, errors.New("usage tenant ID is invalid")
	case !validRequiredText(event.APIKeyID, 64):
		return Event{}, errors.New("usage API key ID is invalid")
	case !validRequiredText(event.RequestedModel, 200):
		return Event{}, errors.New("usage requested model is invalid")
	}
	return event, nil
}

func generateEventID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate usage event ID: %w", err)
	}
	return "use_" + hex.EncodeToString(random), nil
}

func (event Event) Marshal() ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal usage event: %w", err)
	}
	return payload, nil
}

func Decode(payload []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return Event{}, errors.New("decode usage event JSON")
	}
	if event.SchemaVersion == 1 && event.UsageSource == "" {
		event.UsageSource = inferredUsageSource(event)
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (event Event) Validate() error {
	switch {
	case event.SchemaVersion < minimumSchemaVersion || event.SchemaVersion > SchemaVersion:
		return fmt.Errorf("unsupported usage schema version %d", event.SchemaVersion)
	case !validRequiredText(event.EventID, 64):
		return errors.New("usage event ID is invalid")
	case !validRequiredText(event.RequestID, 128):
		return errors.New("usage request ID is invalid")
	case !validRequiredText(event.TenantID, 128):
		return errors.New("usage tenant ID is invalid")
	case event.SchemaVersion >= 2 && !validRequiredText(event.APIKeyID, 64):
		return errors.New("usage API key ID is invalid")
	case !validRequiredText(event.RequestedModel, 200):
		return errors.New("usage requested model is invalid")
	case len(event.ProviderID) > 100 || len(event.UpstreamModel) > 200:
		return errors.New("usage route metadata is too long")
	case !validCacheStatus(event.CacheStatus):
		return errors.New("usage cache status is invalid")
	case event.Attempts < 0 || event.Retries < 0 || event.Fallbacks < 0:
		return errors.New("usage reliability counters must not be negative")
	case !validUsageStatus(event.Status):
		return errors.New("usage status is invalid")
	case len(event.ErrorCategory) > 64 || len(event.ErrorCode) > 100:
		return errors.New("usage error metadata is too long")
	case len(event.FinishReason) > 64 || len(event.UsageCaveat) > maximumUsageCaveatLen:
		return errors.New("usage response metadata is too long")
	case event.StartedAt.IsZero() || event.EndedAt.IsZero() || event.EndedAt.Before(event.StartedAt):
		return errors.New("usage timestamps are invalid")
	case event.LatencyMS < 0:
		return errors.New("usage latency must not be negative")
	case event.LatencyMS != event.EndedAt.Sub(event.StartedAt).Milliseconds():
		return errors.New("usage latency does not match timestamps")
	case event.FirstTokenMS != nil && (*event.FirstTokenMS < 0 || *event.FirstTokenMS > event.LatencyMS):
		return errors.New("usage first token latency is invalid")
	case event.UsageSource != UsageSourceUnknown &&
		event.UsageSource != UsageSourceProvider &&
		event.UsageSource != UsageSourceCacheReplay:
		return errors.New("usage source is invalid")
	case event.Usage == nil && event.UsageSource != UsageSourceUnknown:
		return errors.New("usage source requires token data")
	case event.Usage != nil && event.UsageSource == UsageSourceUnknown && event.SchemaVersion >= 2:
		return errors.New("usage token data requires a source")
	case event.UsageSource == UsageSourceCacheReplay && event.Status != StatusCacheHit:
		return errors.New("cache replay usage requires a cache hit")
	case event.Status == StatusCacheHit &&
		(event.Stream || event.CacheStatus != "hit" ||
			(event.Usage != nil && event.UsageSource != UsageSourceCacheReplay)):
		return errors.New("usage cache hit metadata is inconsistent")
	case event.Status != StatusCacheHit && event.CacheStatus == "hit":
		return errors.New("usage cache status is inconsistent")
	case event.Status == StatusStreamCompleted && !event.Stream:
		return errors.New("stream completion requires a stream request")
	case event.Status == StatusStreamInterrupted && !event.Stream:
		return errors.New("stream interruption requires a stream request")
	case event.FirstTokenMS != nil && !event.Stream:
		return errors.New("first token latency requires a stream request")
	}
	if failedStatus(event.Status) && (event.ErrorCategory == "" || event.ErrorCode == "") {
		return errors.New("usage failure metadata is required")
	}
	if event.Usage != nil {
		if err := event.Usage.validate(); err != nil {
			return err
		}
	}
	return nil
}

func validCacheStatus(status string) bool {
	return status == "bypass" || status == "miss" || status == "hit"
}

func validUsageStatus(status Status) bool {
	switch status {
	case StatusSuccess,
		StatusCacheHit,
		StatusFailed,
		StatusCanceled,
		StatusStreamCompleted,
		StatusStreamInterrupted:
		return true
	default:
		return false
	}
}

func failedStatus(status Status) bool {
	return status == StatusFailed ||
		status == StatusCanceled ||
		status == StatusStreamInterrupted
}

func (usage *TokenUsage) validate() error {
	if invalidTokenCount(usage.Input) ||
		invalidTokenCount(usage.Output) ||
		invalidTokenCount(usage.Total) {
		return errors.New("usage token counts are invalid")
	}
	if usage.InputDetails != nil {
		for _, count := range []int64{
			usage.InputDetails.Text,
			usage.InputDetails.Audio,
			usage.InputDetails.Image,
			usage.InputDetails.CachedRead,
			usage.InputDetails.CachedWrite,
		} {
			if invalidTokenCount(count) {
				return errors.New("usage input token details are invalid")
			}
		}
	}
	if usage.OutputDetails != nil {
		for _, count := range []int64{
			usage.OutputDetails.Text,
			usage.OutputDetails.Audio,
			usage.OutputDetails.Reasoning,
			usage.OutputDetails.AcceptedPrediction,
			usage.OutputDetails.RejectedPrediction,
		} {
			if invalidTokenCount(count) {
				return errors.New("usage output token details are invalid")
			}
		}
	}
	if len(usage.Raw) > maximumRawUsageBytes || (len(usage.Raw) > 0 && !json.Valid(usage.Raw)) {
		return errors.New("usage raw data is invalid")
	}
	if usage.ReportedCost != nil {
		if usage.ReportedCost.Currency != "USD" ||
			usage.ReportedCost.TotalNanoUSD < 0 ||
			(usage.ReportedCost.InputNanoUSD != nil && *usage.ReportedCost.InputNanoUSD < 0) ||
			(usage.ReportedCost.OutputNanoUSD != nil && *usage.ReportedCost.OutputNanoUSD < 0) {
			return errors.New("usage reported cost is invalid")
		}
	}
	return nil
}

func invalidTokenCount(count int64) bool {
	return count < 0 || count > maximumTokenCount
}

func validRequiredText(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && len(value) > 0 && len(value) <= maximum
}

func inferredUsageSource(event Event) UsageSource {
	if event.Usage == nil {
		return UsageSourceUnknown
	}
	if event.Status == StatusCacheHit {
		return UsageSourceCacheReplay
	}
	return UsageSourceProvider
}

func parseResponseMetadata(payload []byte) responseMetadata {
	var envelope struct {
		Usage   json.RawMessage `json:"usage"`
		Choices []struct {
			FinishReason *string         `json:"finish_reason"`
			Delta        json.RawMessage `json:"delta"`
			Message      json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return responseMetadata{}
	}

	metadata := responseMetadata{}
	for _, choice := range envelope.Choices {
		if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
			finishReason := strings.TrimSpace(*choice.FinishReason)
			if len(finishReason) <= 64 {
				metadata.finishReason = finishReason
			} else {
				metadata.usageCaveat = joinCaveats(
					metadata.usageCaveat,
					"provider_finish_reason_omitted",
				)
			}
		}
		if jsonObjectPresent(choice.Delta) || jsonObjectPresent(choice.Message) {
			metadata.hasChunk = true
		}
	}
	if len(bytes.TrimSpace(envelope.Usage)) == 0 ||
		bytes.Equal(bytes.TrimSpace(envelope.Usage), []byte("null")) {
		return metadata
	}

	usage, caveat := parseTokenUsage(envelope.Usage)
	metadata.usage = usage
	metadata.usageCaveat = joinCaveats(metadata.usageCaveat, caveat)
	return metadata
}

func parseTokenUsage(raw json.RawMessage) (*TokenUsage, string) {
	if len(raw) > maximumRawUsageBytes {
		return nil, "provider_usage_oversized"
	}
	var fields struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		InputTokens      *int64 `json:"input_tokens"`
		OutputTokens     *int64 `json:"output_tokens"`
		TotalTokens      *int64 `json:"total_tokens"`

		CacheReadInputTokens  *int64 `json:"cache_read_input_tokens"`
		CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
		CacheCreationTokens   *int64 `json:"cache_creation_input_tokens"`

		PromptDetails     *providerInputDetails  `json:"prompt_tokens_details"`
		InputDetails      *providerInputDetails  `json:"input_tokens_details"`
		CompletionDetails *providerOutputDetails `json:"completion_tokens_details"`
		OutputDetails     *providerOutputDetails `json:"output_tokens_details"`
		Cost              json.RawMessage        `json:"cost"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return nil, "provider_usage_invalid"
	}

	input := firstTokenCount(fields.PromptTokens, fields.InputTokens)
	output := firstTokenCount(fields.CompletionTokens, fields.OutputTokens)
	if input == nil && output == nil && fields.TotalTokens == nil {
		return nil, "provider_usage_missing_tokens"
	}

	usage := &TokenUsage{}
	if input != nil {
		usage.Input = *input
	}
	if output != nil {
		usage.Output = *output
	}
	if fields.TotalTokens != nil {
		usage.Total = *fields.TotalTokens
	} else {
		usage.Total = usage.Input + usage.Output
	}
	if invalidTokenCount(usage.Input) ||
		invalidTokenCount(usage.Output) ||
		invalidTokenCount(usage.Total) {
		return nil, "provider_usage_invalid_tokens"
	}

	inputDetailsPresent := fields.PromptDetails != nil ||
		fields.InputDetails != nil ||
		fields.CacheReadInputTokens != nil ||
		fields.CacheWriteInputTokens != nil ||
		fields.CacheCreationTokens != nil
	usage.InputDetails = mergeInputDetails(
		fields.PromptDetails,
		fields.InputDetails,
		fields.CacheReadInputTokens,
		firstTokenCount(fields.CacheWriteInputTokens, fields.CacheCreationTokens),
	)
	caveat := ""
	if inputDetailsPresent && usage.InputDetails == nil {
		caveat = joinCaveats(caveat, "provider_input_details_invalid")
	}
	outputDetailsPresent := fields.CompletionDetails != nil || fields.OutputDetails != nil
	usage.OutputDetails = mergeOutputDetails(fields.CompletionDetails, fields.OutputDetails)
	if outputDetailsPresent && usage.OutputDetails == nil {
		caveat = joinCaveats(caveat, "provider_output_details_invalid")
	}
	usage.ReportedCost = parseReportedCost(fields.Cost)
	costRaw := bytes.TrimSpace(fields.Cost)
	if len(costRaw) > 0 &&
		!bytes.Equal(costRaw, []byte("null")) &&
		usage.ReportedCost == nil {
		caveat = joinCaveats(caveat, "provider_cost_invalid")
	}

	var compact bytes.Buffer
	if len(raw) <= maximumRawUsageBytes && json.Compact(&compact, raw) == nil {
		usage.Raw = compact.Bytes()
		return usage, caveat
	}
	return usage, joinCaveats(caveat, "provider_usage_raw_omitted")
}

type providerInputDetails struct {
	Text           *int64 `json:"text_tokens"`
	Audio          *int64 `json:"audio_tokens"`
	Image          *int64 `json:"image_tokens"`
	Cached         *int64 `json:"cached_tokens"`
	CachedRead     *int64 `json:"cached_read_tokens"`
	CachedWrite    *int64 `json:"cached_write_tokens"`
	CacheReadInput *int64 `json:"cache_read_input_tokens"`
	CacheWrite     *int64 `json:"cache_write_input_tokens"`
	CacheCreation  *int64 `json:"cache_creation_input_tokens"`
}

type providerOutputDetails struct {
	Text               *int64 `json:"text_tokens"`
	Audio              *int64 `json:"audio_tokens"`
	Reasoning          *int64 `json:"reasoning_tokens"`
	Thoughts           *int64 `json:"thoughts_tokens"`
	AcceptedPrediction *int64 `json:"accepted_prediction_tokens"`
	RejectedPrediction *int64 `json:"rejected_prediction_tokens"`
}

func mergeInputDetails(
	first *providerInputDetails,
	second *providerInputDetails,
	directRead *int64,
	directWrite *int64,
) *InputTokenDetails {
	details := first
	if details == nil {
		details = second
	}
	if details == nil && directRead == nil && directWrite == nil {
		return nil
	}
	result := &InputTokenDetails{}
	if details != nil {
		result.Text = tokenValue(details.Text)
		result.Audio = tokenValue(details.Audio)
		result.Image = tokenValue(details.Image)
		result.CachedRead = tokenValue(firstTokenCount(
			details.CachedRead,
			details.Cached,
			details.CacheReadInput,
		))
		result.CachedWrite = tokenValue(firstTokenCount(
			details.CachedWrite,
			details.CacheWrite,
			details.CacheCreation,
		))
	}
	if directRead != nil {
		result.CachedRead = *directRead
	}
	if directWrite != nil {
		result.CachedWrite = *directWrite
	}
	if invalidInputDetails(result) {
		return nil
	}
	return result
}

func mergeOutputDetails(first, second *providerOutputDetails) *OutputTokenDetails {
	details := first
	if details == nil {
		details = second
	}
	if details == nil {
		return nil
	}
	result := &OutputTokenDetails{
		Text:               tokenValue(details.Text),
		Audio:              tokenValue(details.Audio),
		Reasoning:          tokenValue(firstTokenCount(details.Reasoning, details.Thoughts)),
		AcceptedPrediction: tokenValue(details.AcceptedPrediction),
		RejectedPrediction: tokenValue(details.RejectedPrediction),
	}
	if invalidOutputDetails(result) {
		return nil
	}
	return result
}

func invalidInputDetails(details *InputTokenDetails) bool {
	for _, count := range []int64{
		details.Text,
		details.Audio,
		details.Image,
		details.CachedRead,
		details.CachedWrite,
	} {
		if invalidTokenCount(count) {
			return true
		}
	}
	return false
}

func invalidOutputDetails(details *OutputTokenDetails) bool {
	for _, count := range []int64{
		details.Text,
		details.Audio,
		details.Reasoning,
		details.AcceptedPrediction,
		details.RejectedPrediction,
	} {
		if invalidTokenCount(count) {
			return true
		}
	}
	return false
}

func parseReportedCost(raw json.RawMessage) *ReportedCost {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] != '{' {
		total, ok := parseUSDJSON(raw)
		if !ok {
			return nil
		}
		return &ReportedCost{TotalNanoUSD: total, Currency: "USD"}
	}

	var reported struct {
		Input    json.RawMessage `json:"input_cost"`
		Output   json.RawMessage `json:"output_cost"`
		Total    json.RawMessage `json:"total_cost"`
		Currency string          `json:"currency"`
	}
	if json.Unmarshal(raw, &reported) != nil {
		return nil
	}
	input, hasInput := parseUSDJSON(reported.Input)
	output, hasOutput := parseUSDJSON(reported.Output)
	total, hasTotal := parseUSDJSON(reported.Total)
	if !hasTotal {
		if !hasInput && !hasOutput {
			return nil
		}
		var ok bool
		total, ok = safeAdd(input, output)
		if !ok {
			return nil
		}
	}
	currency := strings.ToUpper(strings.TrimSpace(reported.Currency))
	if currency == "" {
		currency = "USD"
	}
	if currency != "USD" {
		return nil
	}
	result := &ReportedCost{TotalNanoUSD: total, Currency: currency}
	if hasInput {
		result.InputNanoUSD = &input
	}
	if hasOutput {
		result.OutputNanoUSD = &output
	}
	return result
}

func parseUSDJSON(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	var value string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return 0, false
		}
	} else {
		value = string(raw)
	}
	nanos, err := parseUSD(value)
	return nanos, err == nil
}

func jsonObjectPresent(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 1 && raw[0] == '{' && raw[len(raw)-1] == '}'
}

func firstTokenCount(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func tokenValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
