package reliability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"model-velo/internal/provider"
	"model-velo/internal/routing"
)

func TestPreparedStreamHoldsResourcesUntilFinish(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	executor, queues, breakers := newStreamAttemptExecutor(t, upstream.URL, time.Second)
	prepared, failure := executor.PrepareStream(context.Background(), streamAttemptInput(t))
	if failure != nil {
		t.Fatalf("PrepareStream() failure = %v", failure)
	}
	if prepared == nil || prepared.FirstEvent.Done || !strings.Contains(string(prepared.FirstEvent.Data), `"content":"hello"`) {
		t.Fatalf("prepared stream = %#v", prepared)
	}
	if prepared.ProviderID != "upstream" || prepared.UpstreamModel != "mapped-model" || prepared.KeyID != "key-a" {
		t.Fatalf("prepared metadata = %#v", prepared)
	}
	if snapshot, _ := queues.Snapshot("upstream"); snapshot.Active != 1 || snapshot.Waiting != 0 {
		t.Fatalf("queue while stream is prepared = %#v", snapshot)
	}

	done, err := prepared.Next()
	if err != nil || !done.Done {
		t.Fatalf("Next() = %#v, %v; want DONE", done, err)
	}
	if !prepared.Finish(nil) || prepared.Finish(nil) {
		t.Fatal("Finish() did not release exactly once")
	}
	if snapshot, _ := queues.Snapshot("upstream"); snapshot.Active != 0 || snapshot.Waiting != 0 {
		t.Fatalf("queue after Finish() = %#v", snapshot)
	}
	if snapshot, _ := breakers.Snapshot("upstream"); snapshot.State != StateClosed || snapshot.ConsecutiveFailures != 0 {
		t.Fatalf("breaker after successful stream = %#v", snapshot)
	}
	if _, err := prepared.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after Finish() error = %v, want EOF", err)
	}
}

func TestPreparedStreamTimesOutBetweenEvents(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()

	executor, queues, breakers := newStreamAttemptExecutor(t, upstream.URL, 100*time.Millisecond)
	prepared, failure := executor.PrepareStream(context.Background(), streamAttemptInput(t))
	if failure != nil {
		t.Fatalf("PrepareStream() failure = %v", failure)
	}

	_, err := prepared.Next()
	if !errors.Is(err, context.DeadlineExceeded) {
		prepared.Abort(err)
		t.Fatalf("Next() error = %v, want deadline exceeded", err)
	}
	failure = prepared.FinishError(context.Background(), err)
	if failure == nil || failure.Category != CategoryTimeout || failure.Timeout != TimeoutUpstream {
		t.Fatalf("FinishError() = %#v", failure)
	}
	assertStreamAttemptReleased(t, queues, breakers, 1)

	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("stream idle timeout did not cancel the upstream request")
	}
}

func TestPreparedStreamHeartbeatResetsIdleTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		writer.(http.Flusher).Flush()
		for range 7 {
			time.Sleep(50 * time.Millisecond)
			_, _ = io.WriteString(writer, ": heartbeat\n\n")
			writer.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	executor, queues, breakers := newStreamAttemptExecutor(t, upstream.URL, 300*time.Millisecond)
	prepared, failure := executor.PrepareStream(context.Background(), streamAttemptInput(t))
	if failure != nil {
		t.Fatalf("PrepareStream() failure = %v", failure)
	}

	event, err := prepared.Next()
	if err != nil || !strings.Contains(string(event.Data), `"content":"second"`) {
		prepared.Abort(err)
		t.Fatalf("Next() = %#v, %v; want second content event", event, err)
	}
	done, err := prepared.Next()
	if err != nil || !done.Done {
		prepared.Abort(err)
		t.Fatalf("Next() = %#v, %v; want DONE", done, err)
	}
	prepared.Finish(nil)
	assertStreamAttemptReleased(t, queues, breakers, 0)
}

func TestPrepareStreamClassifiesPrecommitFailures(t *testing.T) {
	tests := []struct {
		name                string
		status              int
		contentType         string
		body                string
		category            Category
		wantBreakerFailures int
	}{
		{
			name:                "invalid first event",
			contentType:         "text/event-stream",
			body:                "data: not-json\n\n",
			category:            CategoryUpstreamProtocol,
			wantBreakerFailures: 1,
		},
		{
			name:                "EOF before first event",
			contentType:         "text/event-stream",
			category:            CategoryUpstreamProtocol,
			wantBreakerFailures: 1,
		},
		{
			name:                "DONE before first event",
			contentType:         "text/event-stream",
			body:                "data: [DONE]\n\n",
			category:            CategoryUpstreamProtocol,
			wantBreakerFailures: 1,
		},
		{
			name:                "successful non-SSE response",
			contentType:         "application/json",
			body:                `{}`,
			category:            CategoryUpstreamProtocol,
			wantBreakerFailures: 1,
		},
		{
			name:                "upstream service unavailable",
			status:              http.StatusServiceUnavailable,
			contentType:         "application/json",
			body:                `{"error":{"code":"overloaded"}}`,
			category:            CategoryUpstream5xx,
			wantBreakerFailures: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				if test.status != 0 {
					writer.WriteHeader(test.status)
				}
				_, _ = io.WriteString(writer, test.body)
			}))
			defer upstream.Close()

			executor, queues, breakers := newStreamAttemptExecutor(t, upstream.URL, time.Second)
			prepared, failure := executor.PrepareStream(context.Background(), streamAttemptInput(t))
			if prepared != nil {
				prepared.Finish(nil)
				t.Fatal("PrepareStream() returned a stream for an invalid first event")
			}
			if failure == nil || failure.Category != test.category || failure.TotalAttempts != 1 {
				t.Fatalf("PrepareStream() failure = %#v", failure)
			}
			if snapshot, _ := queues.Snapshot("upstream"); snapshot.Active != 0 || snapshot.Waiting != 0 {
				t.Fatalf("queue after precommit failure = %#v", snapshot)
			}
			if snapshot, _ := breakers.Snapshot("upstream"); snapshot.ConsecutiveFailures != test.wantBreakerFailures {
				t.Fatalf("breaker after precommit failure = %#v", snapshot)
			}
		})
	}
}

func TestPrepareStreamStopsFirstEventWait(t *testing.T) {
	t.Run("attempt timeout", func(t *testing.T) {
		upstreamCanceled := make(chan struct{})
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
			close(upstreamCanceled)
		}))
		defer upstream.Close()

		executor, queues, breakers := newStreamAttemptExecutor(t, upstream.URL, 100*time.Millisecond)
		prepared, failure := executor.PrepareStream(context.Background(), streamAttemptInput(t))
		if prepared != nil || failure == nil || failure.Category != CategoryTimeout || failure.Timeout != TimeoutUpstream {
			t.Fatalf("PrepareStream() = %#v, %#v", prepared, failure)
		}
		assertStreamAttemptReleased(t, queues, breakers, 1)
		select {
		case <-upstreamCanceled:
		case <-time.After(time.Second):
			t.Fatal("attempt timeout did not cancel the upstream request")
		}
	})

	t.Run("parent cancellation", func(t *testing.T) {
		upstreamStarted := make(chan struct{})
		upstreamCanceled := make(chan struct{})
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			close(upstreamStarted)
			<-request.Context().Done()
			close(upstreamCanceled)
		}))
		defer upstream.Close()

		executor, queues, breakers := newStreamAttemptExecutor(t, upstream.URL, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		input := streamAttemptInput(t)
		result := make(chan *Failure, 1)
		go func() {
			prepared, failure := executor.PrepareStream(ctx, input)
			if prepared != nil {
				prepared.Finish(nil)
			}
			result <- failure
		}()
		select {
		case <-upstreamStarted:
			cancel()
		case <-time.After(time.Second):
			cancel()
			t.Fatal("stream attempt did not reach the upstream")
		}

		failure := <-result
		if failure == nil || failure.Category != CategoryCanceled {
			t.Fatalf("PrepareStream() failure = %#v", failure)
		}
		assertStreamAttemptReleased(t, queues, breakers, 0)
		select {
		case <-upstreamCanceled:
		case <-time.After(time.Second):
			t.Fatal("parent cancellation did not cancel the upstream request")
		}
	})
}

func TestPrepareStreamRejectsNonStreamingAdapterBeforeAdmission(t *testing.T) {
	adapters, err := provider.NewAdapterRegistryFromAdapters(map[string]provider.Adapter{
		"upstream": nonStreamingTestAdapter{},
	})
	if err != nil {
		t.Fatalf("NewAdapterRegistryFromAdapters() error = %v", err)
	}
	breakers, err := NewBreakerRegistry([]string{"upstream"}, DefaultBreakerConfig())
	if err != nil {
		t.Fatalf("NewBreakerRegistry() error = %v", err)
	}
	queues, err := NewQueueRegistry([]string{"upstream"}, DefaultQueueConfig())
	if err != nil {
		t.Fatalf("NewQueueRegistry() error = %v", err)
	}
	retry, err := NewRetryPolicy(DefaultRetryConfig())
	if err != nil {
		t.Fatalf("NewRetryPolicy() error = %v", err)
	}
	executor, err := NewAttemptExecutor(adapters, breakers, queues, nil, retry)
	if err != nil {
		t.Fatalf("NewAttemptExecutor() error = %v", err)
	}

	prepared, failure := executor.PrepareStream(context.Background(), streamAttemptInput(t))
	if prepared != nil || failure == nil || failure.Category != CategoryUnsupportedCapability {
		t.Fatalf("PrepareStream() = %#v, %#v", prepared, failure)
	}
	assertStreamAttemptReleased(t, queues, breakers, 0)
}

func TestPrepareStreamRetriesBeforeReturningFirstEvent(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"code":"overloaded"}}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"retry-ok\"}}]}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	retryConfig := streamRetryTestConfig(2)
	executor, queues, breakers := newStreamAttemptExecutorWithRetry(t, upstream.URL, retryConfig)
	prepared, failure := executor.PrepareStream(context.Background(), streamAttemptInput(t))
	if failure != nil {
		t.Fatalf("PrepareStream() failure = %v", failure)
	}
	if calls.Load() != 2 || prepared.Attempts != 2 || len(prepared.Trail) != 2 {
		t.Fatalf("calls = %d, prepared metadata = %#v", calls.Load(), prepared)
	}
	if prepared.Trail[0].Category != CategoryUpstream5xx || prepared.Trail[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("first attempt record = %#v", prepared.Trail[0])
	}
	if prepared.Trail[1].Category != "" || !strings.Contains(string(prepared.FirstEvent.Data), "retry-ok") {
		t.Fatalf("successful attempt = %#v, first event = %s", prepared.Trail[1], prepared.FirstEvent.Data)
	}
	if snapshot, _ := queues.Snapshot("upstream"); snapshot.Active != 1 {
		t.Fatalf("queue while retried stream is prepared = %#v", snapshot)
	}
	if snapshot, _ := breakers.Snapshot("upstream"); snapshot.ConsecutiveFailures != 1 {
		t.Fatalf("breaker before successful stream finishes = %#v", snapshot)
	}
	done, err := prepared.Next()
	if err != nil || !done.Done {
		t.Fatalf("Next() = %#v, %v; want DONE", done, err)
	}
	prepared.Finish(nil)
	assertStreamAttemptReleased(t, queues, breakers, 0)
}

func TestOpenStreamFallsBackAfterRetryExhaustion(t *testing.T) {
	callOrder := make(chan string, 3)
	primary := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		callOrder <- "primary"
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(writer, `{"error":{"code":"overloaded"}}`)
	}))
	defer primary.Close()

	secondaryCanceled := make(chan struct{})
	secondary := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		callOrder <- "secondary"
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"fallback-ok\"}}]}\n\n")
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(secondaryCanceled)
	}))
	defer secondary.Close()

	providerIDs := []string{"primary", "secondary"}
	adapters, err := provider.NewAdapterRegistry([]provider.AdapterConfig{
		{ProviderID: "primary", Protocol: provider.ProtocolOpenAICompatible, BaseURL: primary.URL},
		{ProviderID: "secondary", Protocol: provider.ProtocolOpenAICompatible, BaseURL: secondary.URL},
	})
	if err != nil {
		t.Fatalf("NewAdapterRegistry() error = %v", err)
	}
	breakers, err := NewBreakerRegistry(providerIDs, DefaultBreakerConfig())
	if err != nil {
		t.Fatalf("NewBreakerRegistry() error = %v", err)
	}
	queueConfig := DefaultQueueConfig()
	queueConfig.MaxInFlight = 1
	queues, err := NewQueueRegistry(providerIDs, queueConfig)
	if err != nil {
		t.Fatalf("NewQueueRegistry() error = %v", err)
	}
	keys, err := NewProviderKeyRegistry(providerIDs, []ProviderKeySet{
		{ProviderID: "primary", Keys: []ProviderKey{{ID: "primary-key", Secret: "primary-secret"}}},
		{ProviderID: "secondary", Keys: []ProviderKey{{ID: "secondary-key", Secret: "secondary-secret"}}},
	})
	if err != nil {
		t.Fatalf("NewProviderKeyRegistry() error = %v", err)
	}
	retry, err := NewRetryPolicy(streamRetryTestConfig(2))
	if err != nil {
		t.Fatalf("NewRetryPolicy() error = %v", err)
	}
	executor, err := NewAttemptExecutor(adapters, breakers, queues, keys, retry)
	if err != nil {
		t.Fatalf("NewAttemptExecutor() error = %v", err)
	}
	orchestrator, err := NewOrchestrator(executor, retry)
	if err != nil {
		t.Fatalf("NewOrchestrator() error = %v", err)
	}
	request := streamAttemptInput(t).Request
	prepared, failure := orchestrator.OpenStream(context.Background(), ExecutionInput{
		RequestID: "stream-fallback-id",
		Request:   request,
		Plan: routing.Plan{
			RequestedModel: "requested-model",
			Candidates: []routing.Candidate{
				{ProviderID: "primary", UpstreamModel: "primary-model", Priority: 0},
				{ProviderID: "secondary", UpstreamModel: "secondary-model", Priority: 1},
			},
		},
	})
	if failure != nil {
		t.Fatalf("OpenStream() failure = %v", failure)
	}

	wantOrder := []string{"primary", "primary", "secondary"}
	for index, want := range wantOrder {
		select {
		case got := <-callOrder:
			if got != want {
				t.Fatalf("call %d = %q, want %q", index+1, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("call %d missing, want %q", index+1, want)
		}
	}
	if prepared.ProviderID != "secondary" || prepared.KeyID != "secondary-key" ||
		prepared.Attempts != 3 || prepared.Fallbacks != 1 || prepared.CandidatesTried != 2 {
		t.Fatalf("prepared fallback metadata = %#v", prepared)
	}
	if len(prepared.Trail) != 3 || prepared.Trail[0].ProviderID != "primary" ||
		prepared.Trail[1].ProviderID != "primary" || prepared.Trail[2].ProviderID != "secondary" {
		t.Fatalf("fallback trail = %#v", prepared.Trail)
	}
	if !strings.Contains(string(prepared.FirstEvent.Data), "fallback-ok") {
		t.Fatalf("fallback first event = %s", prepared.FirstEvent.Data)
	}
	if snapshot, _ := queues.Snapshot("primary"); snapshot.Active != 0 {
		t.Fatalf("primary queue after fallback = %#v", snapshot)
	}
	if snapshot, _ := breakers.Snapshot("primary"); snapshot.ConsecutiveFailures != 2 {
		t.Fatalf("primary breaker after retry exhaustion = %#v", snapshot)
	}
	if snapshot, _ := queues.Snapshot("secondary"); snapshot.Active != 1 {
		t.Fatalf("secondary queue while stream is prepared = %#v", snapshot)
	}
	select {
	case <-secondaryCanceled:
		t.Fatal("precommit execution context canceled the successful fallback stream")
	case <-time.After(50 * time.Millisecond):
	}

	prepared.Finish(&Failure{Category: CategoryCanceled, ProviderID: "secondary"})
	select {
	case <-secondaryCanceled:
	case <-time.After(time.Second):
		t.Fatal("Finish() did not cancel the successful fallback stream")
	}
	if snapshot, _ := queues.Snapshot("secondary"); snapshot.Active != 0 {
		t.Fatalf("secondary queue after Finish() = %#v", snapshot)
	}
}

type nonStreamingTestAdapter struct{}

func (nonStreamingTestAdapter) Authentication() provider.Authentication {
	return provider.AuthenticationNone
}

func (nonStreamingTestAdapter) Complete(context.Context, provider.ChatInput, string) ([]byte, error) {
	return nil, nil
}

func newStreamAttemptExecutor(
	t *testing.T,
	baseURL string,
	attemptTimeout time.Duration,
) (*AttemptExecutor, *QueueRegistry, *BreakerRegistry) {
	t.Helper()
	retryConfig := streamRetryTestConfig(1)
	retryConfig.AttemptTimeout = attemptTimeout
	return newStreamAttemptExecutorWithRetry(t, baseURL, retryConfig)
}

func newStreamAttemptExecutorWithRetry(
	t *testing.T,
	baseURL string,
	retryConfig RetryConfig,
) (*AttemptExecutor, *QueueRegistry, *BreakerRegistry) {
	t.Helper()
	adapters, err := provider.NewAdapterRegistry([]provider.AdapterConfig{{
		ProviderID: "upstream",
		Protocol:   provider.ProtocolOpenAICompatible,
		BaseURL:    baseURL,
	}})
	if err != nil {
		t.Fatalf("NewAdapterRegistry() error = %v", err)
	}
	breakers, err := NewBreakerRegistry([]string{"upstream"}, DefaultBreakerConfig())
	if err != nil {
		t.Fatalf("NewBreakerRegistry() error = %v", err)
	}
	queueConfig := DefaultQueueConfig()
	queueConfig.MaxInFlight = 1
	queues, err := NewQueueRegistry([]string{"upstream"}, queueConfig)
	if err != nil {
		t.Fatalf("NewQueueRegistry() error = %v", err)
	}
	keys, err := NewProviderKeyRegistry([]string{"upstream"}, []ProviderKeySet{{
		ProviderID: "upstream",
		Keys:       []ProviderKey{{ID: "key-a", Secret: "secret-a"}},
	}})
	if err != nil {
		t.Fatalf("NewProviderKeyRegistry() error = %v", err)
	}
	retry, err := NewRetryPolicy(retryConfig)
	if err != nil {
		t.Fatalf("NewRetryPolicy() error = %v", err)
	}
	executor, err := NewAttemptExecutor(adapters, breakers, queues, keys, retry)
	if err != nil {
		t.Fatalf("NewAttemptExecutor() error = %v", err)
	}
	return executor, queues, breakers
}

func streamRetryTestConfig(maxAttempts int) RetryConfig {
	config := DefaultRetryConfig()
	config.MaxAttempts = maxAttempts
	config.InitialBackoff = 10 * time.Millisecond
	config.MaxBackoff = 10 * time.Millisecond
	config.BackoffMultiplier = 1
	config.JitterRatio = 0
	config.AttemptTimeout = time.Second
	return config
}

func streamAttemptInput(t *testing.T) AttemptInput {
	t.Helper()
	request, err := provider.ParseChatRequest([]byte(`{"model":"requested-model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatalf("ParseChatRequest() error = %v", err)
	}
	return AttemptInput{
		RequestID:      "stream-attempt-id",
		RequestedModel: "requested-model",
		Request:        request,
		Candidate: routing.Candidate{
			ProviderID:    "upstream",
			UpstreamModel: "mapped-model",
			Priority:      0,
		},
	}
}

func assertStreamAttemptReleased(
	t *testing.T,
	queues *QueueRegistry,
	breakers *BreakerRegistry,
	wantBreakerFailures int,
) {
	t.Helper()
	if snapshot, _ := queues.Snapshot("upstream"); snapshot.Active != 0 || snapshot.Waiting != 0 {
		t.Fatalf("queue after stream attempt = %#v", snapshot)
	}
	if snapshot, _ := breakers.Snapshot("upstream"); snapshot.ConsecutiveFailures != wantBreakerFailures {
		t.Fatalf("breaker after stream attempt = %#v", snapshot)
	}
}
