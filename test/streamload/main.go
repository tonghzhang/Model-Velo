package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultRequests    = 500
	defaultConcurrency = 20
	defaultTimeout     = 30 * time.Second
	maximumPromptBytes = 512 << 10
)

type runConfig struct {
	targetURL     string
	apiKey        string
	model         string
	requestPrefix string
	output        string
	requests      int
	concurrency   int
	promptBytes   int
	duration      time.Duration
	timeout       time.Duration
}

type requestSample struct {
	headers       time.Duration
	firstEvent    time.Duration
	firstContent  time.Duration
	total         time.Duration
	gaps          []time.Duration
	status        int
	events        int
	contentChunks int
	bytes         int64
	complete      bool
	err           string
}

type durationSummary struct {
	Count int     `json:"count"`
	AvgMS float64 `json:"avg_ms,omitempty"`
	P50MS float64 `json:"p50_ms,omitempty"`
	P90MS float64 `json:"p90_ms,omitempty"`
	P95MS float64 `json:"p95_ms,omitempty"`
	P99MS float64 `json:"p99_ms,omitempty"`
	MaxMS float64 `json:"max_ms,omitempty"`
}

type streamSummary struct {
	TargetURL     string          `json:"target_url"`
	Model         string          `json:"model"`
	RequestPrefix string          `json:"request_prefix"`
	Requested     int             `json:"requested,omitempty"`
	Duration      string          `json:"duration,omitempty"`
	Concurrency   int             `json:"concurrency"`
	PromptBytes   int             `json:"prompt_bytes"`
	StartedAt     time.Time       `json:"started_at"`
	WallMS        float64         `json:"wall_ms"`
	Requests      int             `json:"requests"`
	Successes     int             `json:"successes"`
	Failures      int             `json:"failures"`
	Incomplete    int             `json:"incomplete"`
	SuccessRate   float64         `json:"success_rate"`
	ThroughputRPS float64         `json:"throughput_rps"`
	Events        int64           `json:"events"`
	ContentChunks int64           `json:"content_chunks"`
	ResponseBytes int64           `json:"response_bytes"`
	StatusCounts  map[string]int  `json:"status_counts"`
	ErrorCounts   map[string]int  `json:"error_counts"`
	Headers       durationSummary `json:"headers"`
	FirstEvent    durationSummary `json:"first_event"`
	FirstContent  durationSummary `json:"first_content"`
	Total         durationSummary `json:"total"`
	InterChunk    durationSummary `json:"inter_chunk"`
}

func main() {
	config := runConfig{}
	flag.StringVar(&config.targetURL, "url", "", "chat completions endpoint URL")
	flag.StringVar(&config.apiKey, "api-key", "", "bearer API key; defaults to MODEL_VELO_API_KEY")
	flag.StringVar(&config.model, "model", "mock/typical", "model or fake-upstream scenario")
	flag.StringVar(&config.requestPrefix, "request-prefix", "streamload", "request ID prefix")
	flag.StringVar(&config.output, "output", "", "JSON output path; stdout when empty")
	flag.IntVar(
		&config.requests,
		"n",
		defaultRequests,
		"fixed request count; ignored when duration is set",
	)
	flag.IntVar(&config.concurrency, "c", defaultConcurrency, "concurrent workers")
	flag.IntVar(&config.promptBytes, "prompt-bytes", 200, "approximate user prompt bytes")
	flag.DurationVar(&config.duration, "duration", 0, "time-boxed launch duration")
	flag.DurationVar(&config.timeout, "timeout", defaultTimeout, "per-request timeout")
	flag.Parse()

	if config.apiKey == "" {
		config.apiKey = strings.TrimSpace(os.Getenv("MODEL_VELO_API_KEY"))
	}
	if err := config.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	client := newHTTPClient(config.concurrency)
	summary := run(ctx, client, config)
	if err := writeSummary(config.output, summary); err != nil {
		fmt.Fprintf(os.Stderr, "write stream summary: %v\n", err)
		os.Exit(1)
	}
	if summary.Failures > 0 {
		os.Exit(1)
	}
}

func (config runConfig) validate() error {
	parsedURL, err := url.ParseRequestURI(strings.TrimSpace(config.targetURL))
	if err != nil || parsedURL.Host == "" {
		return errors.New("url must be an absolute HTTP or HTTPS URL")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return errors.New("url must use HTTP or HTTPS")
	}
	if strings.TrimSpace(config.model) == "" {
		return errors.New("model must not be empty")
	}
	if !validRequestPrefix(config.requestPrefix) {
		return errors.New(
			"request-prefix must contain 1 to 64 letters, digits, dots, " +
				"dashes, or underscores",
		)
	}
	if config.requests <= 0 {
		return errors.New("n must be positive")
	}
	if config.concurrency <= 0 {
		return errors.New("c must be positive")
	}
	if config.promptBytes < 64 || config.promptBytes > maximumPromptBytes {
		return fmt.Errorf("prompt-bytes must be between 64 and %d", maximumPromptBytes)
	}
	if config.duration < 0 {
		return errors.New("duration must not be negative")
	}
	if config.timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

func validRequestPrefix(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		isLetter := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		return !isLetter &&
			!isDigit &&
			character != '.' &&
			character != '-' &&
			character != '_'
	}) == -1
}

func newHTTPClient(concurrency int) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = concurrency * 2
	transport.MaxIdleConnsPerHost = concurrency
	transport.MaxConnsPerHost = concurrency
	return &http.Client{Transport: transport}
}

func run(
	ctx context.Context,
	client *http.Client,
	config runConfig,
) streamSummary {
	startedAt := time.Now().UTC()
	stopAt := time.Time{}
	if config.duration > 0 {
		stopAt = time.Now().Add(config.duration)
	}

	samples := make(chan requestSample, config.concurrency)
	var next atomic.Int64
	var workers sync.WaitGroup
	for range config.concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				index := int(next.Add(1))
				if config.duration == 0 && index > config.requests {
					return
				}
				if !stopAt.IsZero() && !time.Now().Before(stopAt) {
					return
				}
				sample := sendStreamRequest(
					ctx,
					client,
					config,
					index,
				)
				samples <- sample
				if ctx.Err() != nil {
					return
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(samples)
	}()

	collected := make([]requestSample, 0, config.requests)
	for sample := range samples {
		collected = append(collected, sample)
	}
	return summarize(config, startedAt, time.Since(startedAt), collected)
}

func sendStreamRequest(
	parent context.Context,
	client *http.Client,
	config runConfig,
	index int,
) requestSample {
	requestID := config.requestPrefix + "-sse-" + strconv.Itoa(index)
	body, err := streamRequestBody(config.model, requestID, config.promptBytes)
	if err != nil {
		return requestSample{err: "encode_request"}
	}
	ctx, cancel := context.WithTimeout(parent, config.timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		config.targetURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return requestSample{err: "build_request"}
	}
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	if config.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+config.apiKey)
	}

	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return requestSample{
			total: time.Since(startedAt),
			err:   classifyRequestError(err),
		}
	}
	defer response.Body.Close()

	sample := requestSample{
		headers: time.Since(startedAt),
		status:  response.StatusCode,
		gaps:    []time.Duration{},
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		sample.total = time.Since(startedAt)
		sample.err = "http_" + strconv.Itoa(response.StatusCode)
		return sample
	}
	if !strings.Contains(
		strings.ToLower(response.Header.Get("Content-Type")),
		"text/event-stream",
	) {
		sample.total = time.Since(startedAt)
		sample.err = "invalid_content_type"
		return sample
	}
	return readSSE(response.Body, startedAt, sample)
}

func streamRequestBody(model, requestID string, promptBytes int) ([]byte, error) {
	prefix := "model-velo stream benchmark " + requestID + " "
	content := prefix
	if len(content) < promptBytes {
		content += strings.Repeat("x", promptBytes-len(content))
	} else if len(content) > promptBytes {
		content = content[:promptBytes]
	}
	return json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
		"stream": true,
	})
}

func readSSE(
	body io.Reader,
	startedAt time.Time,
	sample requestSample,
) requestSample {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var previousEvent time.Duration
	for scanner.Scan() {
		line := scanner.Text()
		sample.bytes += int64(len(line) + 1)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		now := time.Since(startedAt)
		if payload == "[DONE]" {
			sample.complete = true
			sample.total = now
			return sample
		}
		if payload == "" {
			continue
		}
		if sample.firstEvent == 0 {
			sample.firstEvent = now
		}
		if previousEvent > 0 {
			sample.gaps = append(sample.gaps, now-previousEvent)
		}
		previousEvent = now
		sample.events++
		sample.total = now

		if streamContent(payload) == "" {
			continue
		}
		sample.contentChunks++
		if sample.firstContent == 0 {
			sample.firstContent = now
		}
	}
	if err := scanner.Err(); err != nil {
		sample.err = classifyRequestError(err)
	} else {
		sample.err = "stream_incomplete"
	}
	if sample.total == 0 {
		sample.total = time.Since(startedAt)
	}
	return sample
}

func streamContent(payload string) string {
	var event struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return ""
	}
	if len(event.Choices) == 0 {
		return ""
	}
	return event.Choices[0].Delta.Content
}

func classifyRequestError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "transport_error"
	}
}

func summarize(
	config runConfig,
	startedAt time.Time,
	wall time.Duration,
	samples []requestSample,
) streamSummary {
	headers := []time.Duration{}
	firstEvents := []time.Duration{}
	firstContents := []time.Duration{}
	totals := []time.Duration{}
	gaps := []time.Duration{}
	statusCounts := map[string]int{}
	errorCounts := map[string]int{}
	var successes int
	var incomplete int
	var eventCount int64
	var contentChunks int64
	var responseBytes int64

	for _, sample := range samples {
		statusCounts[strconv.Itoa(sample.status)]++
		eventCount += int64(sample.events)
		contentChunks += int64(sample.contentChunks)
		responseBytes += sample.bytes
		if sample.headers > 0 {
			headers = append(headers, sample.headers)
		}
		if sample.firstEvent > 0 {
			firstEvents = append(firstEvents, sample.firstEvent)
		}
		if sample.firstContent > 0 {
			firstContents = append(firstContents, sample.firstContent)
		}
		if sample.total > 0 {
			totals = append(totals, sample.total)
		}
		if sample.complete && sample.err == "" {
			successes++
			gaps = append(gaps, sample.gaps...)
			continue
		}
		if sample.status == http.StatusOK && !sample.complete {
			incomplete++
		}
		reason := sample.err
		if reason == "" {
			reason = "stream_incomplete"
		}
		errorCounts[reason]++
	}

	requests := len(samples)
	successRate := 0.0
	throughput := 0.0
	if requests > 0 {
		successRate = float64(successes) / float64(requests)
	}
	if wall > 0 {
		throughput = float64(successes) / wall.Seconds()
	}
	duration := ""
	if config.duration > 0 {
		duration = config.duration.String()
	}
	return streamSummary{
		TargetURL:     config.targetURL,
		Model:         config.model,
		RequestPrefix: config.requestPrefix,
		Requested:     config.requests,
		Duration:      duration,
		Concurrency:   config.concurrency,
		PromptBytes:   config.promptBytes,
		StartedAt:     startedAt,
		WallMS:        milliseconds(wall),
		Requests:      requests,
		Successes:     successes,
		Failures:      requests - successes,
		Incomplete:    incomplete,
		SuccessRate:   successRate,
		ThroughputRPS: throughput,
		Events:        eventCount,
		ContentChunks: contentChunks,
		ResponseBytes: responseBytes,
		StatusCounts:  statusCounts,
		ErrorCounts:   errorCounts,
		Headers:       summarizeDurations(headers),
		FirstEvent:    summarizeDurations(firstEvents),
		FirstContent:  summarizeDurations(firstContents),
		Total:         summarizeDurations(totals),
		InterChunk:    summarizeDurations(gaps),
	}
}

func summarizeDurations(values []time.Duration) durationSummary {
	if len(values) == 0 {
		return durationSummary{}
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left] < values[right]
	})
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return durationSummary{
		Count: len(values),
		AvgMS: milliseconds(total) / float64(len(values)),
		P50MS: milliseconds(percentile(values, 50)),
		P90MS: milliseconds(percentile(values, 90)),
		P95MS: milliseconds(percentile(values, 95)),
		P99MS: milliseconds(percentile(values, 99)),
		MaxMS: milliseconds(values[len(values)-1]),
	}
}

func percentile(values []time.Duration, percent float64) time.Duration {
	index := int(math.Ceil(percent/100*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func writeSummary(path string, summary streamSummary) error {
	writer := io.Writer(os.Stdout)
	var file *os.File
	if path != "" {
		created, err := os.Create(path)
		if err != nil {
			return err
		}
		file = created
		writer = created
		defer file.Close()
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}
