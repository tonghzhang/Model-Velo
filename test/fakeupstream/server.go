package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultScenarioName = "mock/instant"
	maxAttemptStates    = 10_000
	maxRequestBodyBytes = 1 << 20
	attemptStateTTL     = 10 * time.Minute
)

var errRetryStateCapacity = errors.New("fake upstream: retry state capacity exceeded")

type scenario struct {
	name                  string
	description           string
	initialDelay          time.Duration
	chunkInterval         time.Duration
	chunkCount            int
	statusCode            int
	failuresBeforeSuccess int
	retryAfter            time.Duration
	hasStreamError        bool
	hasStreamDrop         bool
}

var scenarioCatalog = map[string]scenario{
	"mock/instant": {
		name:        "mock/instant",
		description: "immediate deterministic success",
		chunkCount:  1,
	},
	"mock/typical": {
		name:          "mock/typical",
		description:   "200 ms time to first event and 16 chunks at 20 ms intervals",
		initialDelay:  200 * time.Millisecond,
		chunkInterval: 20 * time.Millisecond,
		chunkCount:    16,
	},
	"mock/slow": {
		name:          "mock/slow",
		description:   "1 s time to first event and 64 chunks at 50 ms intervals",
		initialDelay:  time.Second,
		chunkInterval: 50 * time.Millisecond,
		chunkCount:    64,
	},
	"mock/error-400": {
		name:        "mock/error-400",
		description: "HTTP 400 invalid request error",
		statusCode:  http.StatusBadRequest,
	},
	"mock/error-401": {
		name:        "mock/error-401",
		description: "HTTP 401 authentication error",
		statusCode:  http.StatusUnauthorized,
	},
	"mock/error-429": {
		name:        "mock/error-429",
		description: "HTTP 429 with Retry-After: 1",
		statusCode:  http.StatusTooManyRequests,
		retryAfter:  time.Second,
	},
	"mock/error-500": {
		name:        "mock/error-500",
		description: "HTTP 500 retryable server error",
		statusCode:  http.StatusInternalServerError,
	},
	"mock/error-503": {
		name:        "mock/error-503",
		description: "HTTP 503 retryable unavailable error",
		statusCode:  http.StatusServiceUnavailable,
	},
	"mock/retry-2": {
		name:                  "mock/retry-2",
		description:           "two HTTP 500 responses followed by success per request ID",
		chunkCount:            1,
		failuresBeforeSuccess: 2,
	},
	"mock/sse-error": {
		name:           "mock/sse-error",
		description:    "HTTP 200 SSE whose first event contains an error object",
		hasStreamError: true,
	},
	"mock/sse-drop": {
		name:          "mock/sse-drop",
		description:   "close the connection after the first content chunk",
		chunkCount:    2,
		hasStreamDrop: true,
	},
}

type upstreamServer struct {
	providerName     string
	scenarioOverride string

	attemptsMu sync.Mutex
	attempts   map[string]attemptState
	sequence   atomic.Uint64
}

type attemptState struct {
	count      int
	lastSeenAt time.Time
}

func newUpstreamServer(providerName, scenarioOverride string) (*upstreamServer, error) {
	providerName = strings.TrimSpace(providerName)
	scenarioOverride = strings.TrimSpace(scenarioOverride)
	if !validProviderName(providerName) {
		return nil, errors.New("provider name must contain only letters, digits, dot, dash, or underscore")
	}
	if scenarioOverride != "" {
		if _, exists := scenarioCatalog[scenarioOverride]; !exists {
			return nil, fmt.Errorf("unknown forced scenario %q", scenarioOverride)
		}
	}
	return &upstreamServer{
		providerName:     providerName,
		scenarioOverride: scenarioOverride,
		attempts:         map[string]attemptState{},
	}, nil
}

func (s *upstreamServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /__admin/scenarios", s.handleScenarios)
	mux.HandleFunc("POST /__admin/reset", s.handleReset)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /chat/completions", s.handleChatCompletions)
	return mux
}

func (s *upstreamServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:           "ok",
		Provider:         s.providerName,
		ScenarioOverride: s.scenarioOverride,
	})
}

func (s *upstreamServer) handleScenarios(w http.ResponseWriter, _ *http.Request) {
	names := make([]string, 0, len(scenarioCatalog))
	for name := range scenarioCatalog {
		names = append(names, name)
	}
	sort.Strings(names)

	available := make([]scenarioDescription, 0, len(names))
	for _, name := range names {
		selected := scenarioCatalog[name]
		available = append(available, scenarioDescription{
			Name:        selected.name,
			Description: selected.description,
		})
	}
	writeJSON(w, http.StatusOK, scenariosResponse{
		Default:   defaultScenarioName,
		Override:  s.scenarioOverride,
		Scenarios: available,
	})
}

func (s *upstreamServer) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.attemptsMu.Lock()
	s.attempts = map[string]attemptState{}
	s.attemptsMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *upstreamServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Mock-Provider", s.providerName)

	request, err := decodeChatRequest(w, r)
	if err != nil {
		statusCode := http.StatusBadRequest
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			statusCode = http.StatusRequestEntityTooLarge
		}
		writeUpstreamError(w, statusCode, err.Error())
		return
	}
	selected, err := s.selectScenario(request.Model)
	if err != nil {
		writeUpstreamError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("X-Mock-Scenario", selected.name)

	if (selected.hasStreamError || selected.hasStreamDrop) && !request.Stream {
		writeUpstreamError(w, http.StatusBadRequest, selected.name+" requires stream=true")
		return
	}
	attempt, shouldFail, err := s.beginAttempt(r.Header.Get("X-Request-ID"), selected)
	if err != nil {
		statusCode := http.StatusBadRequest
		if errors.Is(err, errRetryStateCapacity) {
			statusCode = http.StatusServiceUnavailable
		}
		writeUpstreamError(w, statusCode, err.Error())
		return
	}
	w.Header().Set("X-Mock-Attempt", strconv.Itoa(attempt))
	if shouldFail {
		writeUpstreamError(w, http.StatusInternalServerError, "mock retryable failure")
		return
	}
	if selected.statusCode != 0 {
		if selected.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(selected.retryAfter.Seconds())))
		}
		writeUpstreamError(w, selected.statusCode, selected.description)
		return
	}

	if request.Stream {
		s.writeStream(r.Context(), w, request, selected)
		return
	}
	if !waitFor(r.Context(), selected.totalDuration()) {
		return
	}
	writeJSON(
		w,
		http.StatusOK,
		s.chatResponse(request, r.Header.Get("X-Request-ID"), selected),
	)
}

func (s *upstreamServer) selectScenario(model string) (scenario, error) {
	name := s.scenarioOverride
	if name == "" && strings.HasPrefix(model, "mock/") {
		name = model
	}
	if name == "" {
		name = defaultScenarioName
	}
	selected, exists := scenarioCatalog[name]
	if !exists {
		return scenario{}, fmt.Errorf("unknown scenario %q", name)
	}
	return selected, nil
}

func (s *upstreamServer) beginAttempt(
	requestID string,
	selected scenario,
) (int, bool, error) {
	if selected.failuresBeforeSuccess == 0 {
		return 1, false, nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 0, false, errors.New("retry scenario requires x-request-id")
	}

	key := selected.name + "\x00" + requestID
	s.attemptsMu.Lock()
	defer s.attemptsMu.Unlock()

	now := time.Now()
	state, exists := s.attempts[key]
	if !exists && len(s.attempts) >= maxAttemptStates {
		s.deleteExpiredAttempts(now)
		if len(s.attempts) >= maxAttemptStates {
			return 0, false, errRetryStateCapacity
		}
	}
	attempt := state.count + 1
	if attempt <= selected.failuresBeforeSuccess {
		s.attempts[key] = attemptState{
			count:      attempt,
			lastSeenAt: now,
		}
		return attempt, true, nil
	}
	delete(s.attempts, key)
	return attempt, false, nil
}

func (s *upstreamServer) deleteExpiredAttempts(now time.Time) {
	for key, state := range s.attempts {
		if now.Sub(state.lastSeenAt) >= attemptStateTTL {
			delete(s.attempts, key)
		}
	}
}

func (s *upstreamServer) chatResponse(
	request chatRequest,
	requestID string,
	selected scenario,
) chatResponse {
	content := "mock response from " + s.providerName
	return chatResponse{
		ID:      s.responseID(requestID),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   request.Model,
		Choices: []chatChoice{
			{
				Index: 0,
				Message: chatMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: tokenUsage{
			PromptTokens:     4,
			CompletionTokens: selected.chunkCount,
			TotalTokens:      4 + selected.chunkCount,
		},
		SystemFingerprint: "mock-" + s.providerName,
	}
}

func (s *upstreamServer) writeStream(
	ctx context.Context,
	w http.ResponseWriter,
	request chatRequest,
	selected scenario,
) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if !waitFor(ctx, selected.initialDelay) {
		return
	}
	if selected.hasStreamError {
		_ = writeSSEJSON(w, errorEnvelope{
			Error: upstreamError{
				Message: "mock embedded stream error",
				Type:    "server_error",
				Code:    "mock_stream_error",
			},
		})
		_ = http.NewResponseController(w).Flush()
		return
	}

	responseID := s.responseID("")
	created := time.Now().Unix()
	if !writeAndFlushSSE(w, streamChunk{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   request.Model,
		Choices: []streamChoice{
			{
				Index: 0,
				Delta: streamDelta{Role: "assistant"},
			},
		},
	}) {
		return
	}

	for i := range selected.chunkCount {
		if !waitFor(ctx, selected.chunkInterval) {
			return
		}
		if !writeAndFlushSSE(w, streamChunk{
			ID:      responseID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   request.Model,
			Choices: []streamChoice{
				{
					Index: 0,
					Delta: streamDelta{
						Content: fmt.Sprintf("token-%02d ", i+1),
					},
				},
			},
		}) {
			return
		}
		if selected.hasStreamDrop && i == 0 {
			dropConnection(w)
			return
		}
	}

	finishReason := "stop"
	if !writeAndFlushSSE(w, streamChunk{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   request.Model,
		Choices: []streamChoice{
			{
				Index:        0,
				Delta:        streamDelta{},
				FinishReason: &finishReason,
			},
		},
	}) {
		return
	}
	if !writeAndFlushSSE(w, streamChunk{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   request.Model,
		Choices: []streamChoice{},
		Usage: &tokenUsage{
			PromptTokens:     4,
			CompletionTokens: selected.chunkCount,
			TotalTokens:      4 + selected.chunkCount,
		},
	}) {
		return
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	_ = http.NewResponseController(w).Flush()
}

func (s *upstreamServer) responseID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = strconv.FormatUint(s.sequence.Add(1), 10)
	}
	return "chatcmpl-" + s.providerName + "-" + requestID
}

func decodeChatRequest(w http.ResponseWriter, r *http.Request) (chatRequest, error) {
	reader := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(reader)

	request := chatRequest{
		Messages: []json.RawMessage{},
	}
	if err := decoder.Decode(&request); err != nil {
		return chatRequest{}, fmt.Errorf("decode chat request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return chatRequest{}, errors.New("chat request must contain one JSON object")
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		return chatRequest{}, errors.New("model is required")
	}
	if len(request.Messages) == 0 {
		return chatRequest{}, errors.New("at least one message is required")
	}
	return request, nil
}

func (s scenario) totalDuration() time.Duration {
	return s.initialDelay + time.Duration(s.chunkCount)*s.chunkInterval
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func writeAndFlushSSE(w http.ResponseWriter, payload streamChunk) bool {
	if err := writeSSEJSON(w, payload); err != nil {
		return false
	}
	return http.NewResponseController(w).Flush() == nil
}

func writeSSEJSON(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

func dropConnection(w http.ResponseWriter) {
	hijacker, supported := w.(http.Hijacker)
	if !supported {
		return
	}
	connection, _, err := hijacker.Hijack()
	if err != nil {
		return
	}
	_ = connection.Close()
}

func writeUpstreamError(w http.ResponseWriter, statusCode int, message string) {
	errorType := "server_error"
	code := "mock_upstream_error"
	switch statusCode {
	case http.StatusBadRequest:
		errorType = "invalid_request_error"
		code = "invalid_request"
	case http.StatusUnauthorized:
		errorType = "authentication_error"
		code = "invalid_api_key"
	case http.StatusTooManyRequests:
		errorType = "rate_limit_error"
		code = "rate_limit_exceeded"
	}
	writeJSON(w, statusCode, errorEnvelope{
		Error: upstreamError{
			Message: message,
			Type:    errorType,
			Code:    code,
		},
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func validProviderName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	return strings.IndexFunc(name, func(character rune) bool {
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

type chatRequest struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Stream   bool              `json:"stream"`
}

type chatResponse struct {
	ID                string       `json:"id"`
	Object            string       `json:"object"`
	Created           int64        `json:"created"`
	Model             string       `json:"model"`
	Choices           []chatChoice `json:"choices"`
	Usage             tokenUsage   `json:"usage"`
	SystemFingerprint string       `json:"system_fingerprint"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *tokenUsage    `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type errorEnvelope struct {
	Error upstreamError `json:"error"`
}

type upstreamError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type healthResponse struct {
	Status           string `json:"status"`
	Provider         string `json:"provider"`
	ScenarioOverride string `json:"scenario_override,omitempty"`
}

type scenarioDescription struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type scenariosResponse struct {
	Default   string                `json:"default"`
	Override  string                `json:"override,omitempty"`
	Scenarios []scenarioDescription `json:"scenarios"`
}
