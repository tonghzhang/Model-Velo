package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"model-velo/internal/apikey"
	"model-velo/internal/httpapi"
	"model-velo/internal/provider"
	"model-velo/internal/ratelimit"
	"model-velo/internal/reliability"
	"model-velo/internal/responsecache"
	"model-velo/internal/routing"
	"model-velo/internal/usage"
)

func TestChatCompletionSuccess(t *testing.T) {
	wantRequestBody := `{"model":"demo-model","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"}],"temperature":0.25}`
	wantResponseBody := `{"id":"chatcmpl-test","object":"chat.completion","model":"demo-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want %s", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer provider-test-key" {
			t.Errorf("Authorization = %q, want bearer test key", got)
		}
		if got := r.Header.Get("X-Request-ID"); got != "client-success-id" {
			t.Errorf("X-Request-ID = %q, want %q", got, "client-success-id")
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
		}
		if string(body) != wantRequestBody {
			t.Errorf("upstream body = %s, want %s", body, wantRequestBody)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, wantResponseBody)
	}))
	defer upstream.Close()

	router := newTestRouter(t, upstream.URL, time.Second)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(wantRequestBody))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("X-Request-ID", "client-success-id")
	request.Header.Set("Authorization", "Bearer model-velo-test-key")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want JSON", got)
	}
	if got := response.Header().Get("X-Request-ID"); got != "client-success-id" {
		t.Errorf("X-Request-ID = %q, want %q", got, "client-success-id")
	}
	if got := response.Body.String(); got != wantResponseBody {
		t.Errorf("body = %s, want %s", got, wantResponseBody)
	}
}

func TestChatCompletionUsageLifecycle(t *testing.T) {
	t.Run("records non-stream success and tokens once", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(
				w,
				`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			)
		}))
		defer upstream.Close()

		emitter := &recordingUsageEmitter{err: errors.New("Redis unavailable")}
		router := newSingleProviderTestRouterWithUsage(
			t,
			newTestCompatibleAdapter(t, upstream.URL),
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{},
			nil,
			nil,
			nil,
			singleAttemptRetryPolicy(t, time.Second),
			emitter,
		)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, validChatRequest())
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
		}

		event := singleUsageEvent(t, emitter)
		if event.Status != usage.StatusSuccess ||
			event.TenantID != "tenant-test-id" ||
			event.APIKeyID != "api-key-test-id" ||
			event.RequestedModel != "demo-model" ||
			event.ProviderID != "upstream" ||
			event.UpstreamModel != "demo-model" ||
			event.Attempts != 1 ||
			event.Retries != 0 ||
			event.Fallbacks != 0 {
			t.Fatalf("usage event = %#v", event)
		}
		if event.Usage == nil ||
			event.UsageSource != usage.UsageSourceProvider ||
			event.Usage.Input != 7 ||
			event.Usage.Output != 3 ||
			event.Usage.Total != 10 {
			t.Fatalf("token usage = %#v", event.Usage)
		}
	})

	t.Run("records cache hit without a provider", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("cache hit called upstream")
		}))
		defer upstream.Close()

		emitter := &recordingUsageEmitter{}
		router := newSingleProviderTestRouterWithUsage(
			t,
			newTestCompatibleAdapter(t, upstream.URL),
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{result: responsecache.Result{
				Status: responsecache.StatusHit,
				Body:   []byte(`{"choices":[],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`),
			}},
			nil,
			nil,
			nil,
			singleAttemptRetryPolicy(t, time.Second),
			emitter,
		)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, validChatRequest())
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
		}

		event := singleUsageEvent(t, emitter)
		if event.Status != usage.StatusCacheHit ||
			event.CacheStatus != string(responsecache.StatusHit) ||
			event.UsageSource != usage.UsageSourceCacheReplay ||
			event.ProviderID != "" ||
			event.Attempts != 0 ||
			event.Usage == nil ||
			event.Usage.Total != 6 {
			t.Fatalf("cache usage event = %#v", event)
		}
	})

	t.Run("records a safe failure category and code", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"internal"}}`)
		}))
		defer upstream.Close()

		emitter := &recordingUsageEmitter{}
		router := newSingleProviderTestRouterWithUsage(
			t,
			newTestCompatibleAdapter(t, upstream.URL),
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{},
			nil,
			nil,
			nil,
			singleAttemptRetryPolicy(t, time.Second),
			emitter,
		)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, validChatRequest())
		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
		}

		event := singleUsageEvent(t, emitter)
		if event.Status != usage.StatusFailed ||
			event.ErrorCategory != string(reliability.CategoryUpstream5xx) ||
			event.ErrorCode != "upstream_http_error" ||
			event.Attempts != 1 {
			t.Fatalf("failure usage event = %#v", event)
		}
	})

	t.Run("collects usage from a stream chunk", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer upstream.Close()

		emitter := &recordingUsageEmitter{}
		router := newSingleProviderTestRouterWithUsage(
			t,
			newTestCompatibleAdapter(t, upstream.URL),
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{},
			nil,
			nil,
			nil,
			singleAttemptRetryPolicy(t, time.Second),
			emitter,
		)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, chatRequest(
			`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`,
		))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
		}

		event := singleUsageEvent(t, emitter)
		if event.Status != usage.StatusStreamCompleted ||
			!event.Stream ||
			event.FirstTokenMS == nil ||
			event.UsageSource != usage.UsageSourceProvider ||
			event.CacheStatus != string(responsecache.StatusBypass) ||
			event.Usage == nil ||
			event.Usage.Total != 7 {
			t.Fatalf("stream usage event = %#v", event)
		}
	})
}

func singleUsageEvent(t *testing.T, emitter *recordingUsageEmitter) usage.Event {
	t.Helper()
	events := emitter.snapshot()
	if len(events) != 1 {
		t.Fatalf("usage events = %d, want 1", len(events))
	}
	return events[0]
}

func waitForSingleUsageEvent(t *testing.T, emitter *recordingUsageEmitter) usage.Event {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events := emitter.snapshot()
		if len(events) == 1 {
			return events[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("usage event was not emitted")
	return usage.Event{}
}

func TestChatCompletionStreaming(t *testing.T) {
	t.Run("forwards validated events and bypasses cache", func(t *testing.T) {
		var cacheLookups atomic.Int32
		var cacheStores atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": upstream heartbeat\n\n")
			_, _ = io.WriteString(w, "data: { \"choices\" : [{ \"delta\" : { \"role\" : \"assistant\" } }] }\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer upstream.Close()

		adapter := newTestCompatibleAdapter(t, upstream.URL)
		queues, err := reliability.NewQueueRegistry([]string{"upstream"}, reliability.DefaultQueueConfig())
		if err != nil {
			t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
		}
		router := newSingleProviderTestRouter(
			t,
			adapter,
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{
				onLookup: func(string, string, []byte) { cacheLookups.Add(1) },
				onStore:  func(string, string, []byte, []byte) { cacheStores.Add(1) },
			},
			nil,
			nil,
			queues,
		)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, chatRequest(
			`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`,
		))

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-cache, no-transform" {
			t.Errorf("Cache-Control = %q", got)
		}
		if got := response.Header().Get("X-Model-Velo-Cache"); got != string(responsecache.StatusBypass) {
			t.Errorf("cache status = %q, want %q", got, responsecache.StatusBypass)
		}
		if !response.Flushed {
			t.Fatal("stream response was not flushed")
		}
		wantBody := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
			"data: [DONE]\n\n"
		if got := response.Body.String(); got != wantBody {
			t.Fatalf("stream body = %q, want %q", got, wantBody)
		}
		if cacheLookups.Load() != 0 || cacheStores.Load() != 0 {
			t.Fatalf("stream cache calls = lookup:%d store:%d", cacheLookups.Load(), cacheStores.Load())
		}
		if snapshot, _ := queues.Snapshot("upstream"); snapshot.Active != 0 || snapshot.Waiting != 0 {
			t.Fatalf("queue after completed stream = %#v", snapshot)
		}
	})

	t.Run("continues beyond the server write timeout", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(120 * time.Millisecond)
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer upstream.Close()

		gateway := httptest.NewUnstartedServer(newTestRouter(t, upstream.URL, time.Second))
		gateway.Config.WriteTimeout = 40 * time.Millisecond
		gateway.Start()
		defer gateway.Close()

		request, err := http.NewRequest(
			http.MethodPost,
			gateway.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		)
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer model-velo-test-key")
		response, err := gateway.Client().Do(request)
		if err != nil {
			t.Fatalf("stream request error = %v", err)
		}
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read stream response: %v", err)
		}
		wantBody := "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n" +
			"data: [DONE]\n\n"
		if string(body) != wantBody {
			t.Fatalf("stream body = %q, want %q", body, wantBody)
		}
	})

	t.Run("does not fallback after first event", func(t *testing.T) {
		var primaryCalls atomic.Int32
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			primaryCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: not-json\n\n")
		}))
		defer primary.Close()

		var secondaryCalls atomic.Int32
		secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			secondaryCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"secondary\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer secondary.Close()

		router := newFallbackTestRouter(t, primary.URL, secondary.URL, "", "", testResponseCache{})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, chatRequest(
			`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`,
		))

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
		}
		if got := response.Body.String(); got != "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" {
			t.Fatalf("body after upstream interruption = %q", got)
		}
		if primaryCalls.Load() != 1 || secondaryCalls.Load() != 0 {
			t.Fatalf("provider calls = primary:%d secondary:%d", primaryCalls.Load(), secondaryCalls.Load())
		}
	})

	t.Run("keeps an HTTP error available before the first event", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: not-json\n\n")
		}))
		defer upstream.Close()

		router := newTestRouter(t, upstream.URL, time.Second)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, chatRequest(
			`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`,
		))

		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502; body = %s", response.Code, response.Body.String())
		}
		if response.Flushed {
			t.Fatal("invalid first event flushed a response")
		}
		if got := responseErrorCode(t, response); got != "upstream_protocol_error" {
			t.Fatalf("error code = %q, want upstream_protocol_error", got)
		}
	})

	t.Run("falls back before the first event", func(t *testing.T) {
		var primaryCalls atomic.Int32
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			primaryCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: not-json\n\n")
		}))
		defer primary.Close()

		var secondaryCalls atomic.Int32
		secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			secondaryCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"secondary\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer secondary.Close()

		router := newFallbackTestRouter(t, primary.URL, secondary.URL, "", "", testResponseCache{})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, chatRequest(
			`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`,
		))

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
		}
		wantBody := "data: {\"choices\":[{\"delta\":{\"content\":\"secondary\"}}]}\n\n" +
			"data: [DONE]\n\n"
		if got := response.Body.String(); got != wantBody {
			t.Fatalf("fallback stream body = %q, want %q", got, wantBody)
		}
		if primaryCalls.Load() != 1 || secondaryCalls.Load() != 1 {
			t.Fatalf("provider calls = primary:%d secondary:%d", primaryCalls.Load(), secondaryCalls.Load())
		}
	})

	t.Run("client cancellation reaches upstream and releases queue", func(t *testing.T) {
		upstreamCanceled := make(chan struct{})
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-request.Context().Done()
			close(upstreamCanceled)
		}))
		defer upstream.Close()

		queues, err := reliability.NewQueueRegistry([]string{"upstream"}, reliability.DefaultQueueConfig())
		if err != nil {
			t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
		}
		emitter := &recordingUsageEmitter{}
		router := newSingleProviderTestRouterWithUsage(
			t,
			newTestCompatibleAdapter(t, upstream.URL),
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{},
			nil,
			nil,
			queues,
			singleAttemptRetryPolicy(t, time.Second),
			emitter,
		)
		gateway := httptest.NewServer(router)
		defer gateway.Close()

		ctx, cancel := context.WithCancel(context.Background())
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			gateway.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		)
		if err != nil {
			t.Fatalf("http.NewRequestWithContext() error = %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer model-velo-test-key")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("stream request error = %v", err)
		}
		firstFrame := "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n"
		gotFrame := make([]byte, len(firstFrame))
		if _, err := io.ReadFull(response.Body, gotFrame); err != nil {
			response.Body.Close()
			t.Fatalf("read first stream frame: %v", err)
		}
		if string(gotFrame) != firstFrame {
			response.Body.Close()
			t.Fatalf("first frame = %q, want %q", gotFrame, firstFrame)
		}

		cancel()
		response.Body.Close()
		select {
		case <-upstreamCanceled:
		case <-time.After(time.Second):
			t.Fatal("client cancellation did not reach upstream")
		}
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for {
			snapshot, _ := queues.Snapshot("upstream")
			if snapshot.Active == 0 && snapshot.Waiting == 0 {
				break
			}
			select {
			case <-deadline.C:
				t.Fatalf("queue after client cancellation = %#v", snapshot)
			case <-time.After(time.Millisecond):
			}
		}
		event := waitForSingleUsageEvent(t, emitter)
		if event.Status != usage.StatusCanceled ||
			event.ErrorCategory != string(reliability.CategoryCanceled) ||
			event.ErrorCode != "client_canceled" {
			t.Fatalf("canceled usage event = %#v", event)
		}
	})

	t.Run("concurrent streams release provider capacity", func(t *testing.T) {
		const streamCount = 8

		allArrived := make(chan struct{})
		var active atomic.Int32
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if active.Add(1) == streamCount {
				close(allArrived)
			}
			defer active.Add(-1)

			select {
			case <-allArrived:
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
				w.WriteHeader(http.StatusGatewayTimeout)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer upstream.Close()

		queues, err := reliability.NewQueueRegistry([]string{"upstream"}, reliability.DefaultQueueConfig())
		if err != nil {
			t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
		}
		router := newSingleProviderTestRouter(
			t,
			newTestCompatibleAdapter(t, upstream.URL),
			testAccessController{},
			testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
			testResponseCache{},
			nil,
			nil,
			queues,
		)
		gateway := httptest.NewServer(router)
		defer gateway.Close()

		results := make(chan error, streamCount)
		var clients sync.WaitGroup
		for range streamCount {
			clients.Add(1)
			go func() {
				defer clients.Done()
				request, err := http.NewRequest(
					http.MethodPost,
					gateway.URL+"/v1/chat/completions",
					strings.NewReader(`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`),
				)
				if err != nil {
					results <- err
					return
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer model-velo-test-key")
				response, err := gateway.Client().Do(request)
				if err != nil {
					results <- err
					return
				}
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if readErr != nil {
					results <- readErr
					return
				}
				if closeErr != nil {
					results <- closeErr
					return
				}
				if response.StatusCode != http.StatusOK || !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
					results <- fmt.Errorf("stream status/body = %d, %q", response.StatusCode, body)
					return
				}
				results <- nil
			}()
		}
		clients.Wait()
		close(results)

		for err := range results {
			if err != nil {
				t.Errorf("concurrent stream error: %v", err)
			}
		}
		if calls.Load() != streamCount || active.Load() != 0 {
			t.Fatalf("upstream calls/active = %d/%d, want %d/0", calls.Load(), active.Load(), streamCount)
		}
		if snapshot, _ := queues.Snapshot("upstream"); snapshot.Active != 0 || snapshot.Waiting != 0 {
			t.Fatalf("queue after concurrent streams = %#v", snapshot)
		}
	})

	t.Run("rejects a response writer without flush support before upstream", func(t *testing.T) {
		var upstreamCalls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer upstream.Close()

		router := newTestRouter(t, upstream.URL, time.Second)
		response := &nonFlushingResponseWriter{}
		router.ServeHTTP(response, chatRequest(
			`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`,
		))

		if response.status != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body = %s", response.status, response.body)
		}
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.body, &envelope); err != nil || envelope.Error.Code != "streaming_unavailable" {
			t.Fatalf("response = %s, error = %v", response.body, err)
		}
		if upstreamCalls.Load() != 0 {
			t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
		}
	})
}

type nonFlushingResponseWriter struct {
	header http.Header
	body   []byte
	status int
}

func (writer *nonFlushingResponseWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}

func (writer *nonFlushingResponseWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	writer.body = append(writer.body, body...)
	return len(body), nil
}

func (writer *nonFlushingResponseWriter) WriteHeader(status int) {
	writer.status = status
}

func TestChatCompletionUsesOrderedRoutePlan(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		if request.Model != "provider-model-primary" {
			t.Errorf("upstream model = %q, want provider-model-primary", request.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	client := newTestCompatibleAdapter(t, upstream.URL)
	routes, err := routing.New(routing.Definition{
		Providers: []routing.Provider{{
			ID:     "upstream",
			Type:   provider.ProtocolOpenAICompatible,
			Models: []string{"provider-model-primary", "provider-model-secondary"},
		}},
		Rules: []routing.Rule{{
			Model: "demo-model",
			Candidates: []routing.Target{
				{ProviderID: "upstream", UpstreamModel: "provider-model-primary"},
				{ProviderID: "upstream", UpstreamModel: "provider-model-primary"},
				{ProviderID: "upstream", UpstreamModel: "provider-model-secondary"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("routing.New() error = %v", err)
	}
	plan, err := routes.Plan("demo-model", nil)
	if err != nil {
		t.Fatalf("routes.Plan() error = %v", err)
	}
	if len(plan.Candidates) != 2 || plan.Candidates[0].UpstreamModel != "provider-model-primary" || plan.Candidates[1].UpstreamModel != "provider-model-secondary" {
		t.Fatalf("route candidates = %#v, want ordered de-duplicated models", plan.Candidates)
	}

	router := newSingleProviderTestRouter(t, client, testAccessController{}, testRateLimiter{
		decision: ratelimit.Decision{Allowed: true, Limit: 60, Remaining: 59, ResetAtUnix: 1_800_000_000},
	}, testResponseCache{}, routes, nil, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("mapped route status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}

	unroutedRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"unknown-model","messages":[{"role":"user","content":"hello"}]}`),
	)
	unroutedRequest.Header.Set("Content-Type", "application/json")
	unroutedRequest.Header.Set("Authorization", "Bearer model-velo-test-key")
	unroutedResponse := httptest.NewRecorder()
	router.ServeHTTP(unroutedResponse, unroutedRequest)
	if unroutedResponse.Code != http.StatusServiceUnavailable || responseErrorCode(t, unroutedResponse) != "route_unavailable" {
		t.Fatalf("unrouted response = %d %s", unroutedResponse.Code, unroutedResponse.Body.String())
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls.Load())
	}
}

func TestChatCompletionCircuitBreakerPolicy(t *testing.T) {
	t.Run("classifies failures into independent signals", func(t *testing.T) {
		tests := []struct {
			name         string
			err          error
			wantCategory reliability.Category
			wantSignals  reliability.Signals
		}{
			{name: "local request", err: provider.ErrInvalidRequest, wantCategory: reliability.CategoryLocalValidation},
			{name: "unsupported capability", err: provider.ErrUnsupportedCapability, wantCategory: reliability.CategoryUnsupportedCapability, wantSignals: reliability.Signals{Fallback: true}},
			{name: "unsupported response", err: provider.ErrUnsupportedResponse, wantCategory: reliability.CategoryUnsupportedResponse, wantSignals: reliability.Signals{Fallback: true}},
			{name: "upstream 400", err: &provider.HTTPError{StatusCode: http.StatusBadRequest}, wantCategory: reliability.CategoryUpstream4xx},
			{name: "key unauthorized", err: &provider.HTTPError{StatusCode: http.StatusUnauthorized}, wantCategory: reliability.CategoryKeyUnauthorized, wantSignals: reliability.Signals{SwitchKey: true, Fallback: true}},
			{name: "key forbidden", err: &provider.HTTPError{StatusCode: http.StatusForbidden}, wantCategory: reliability.CategoryKeyForbidden, wantSignals: reliability.Signals{SwitchKey: true, Fallback: true}},
			{name: "model unavailable", err: &provider.HTTPError{StatusCode: http.StatusNotFound, Code: "model_not_found"}, wantCategory: reliability.CategoryModelUnavailable, wantSignals: reliability.Signals{Fallback: true}},
			{name: "upstream rate limit", err: &provider.HTTPError{StatusCode: http.StatusTooManyRequests}, wantCategory: reliability.CategoryUpstreamRateLimit, wantSignals: reliability.Signals{Retry: true, SwitchKey: true, Fallback: true}},
			{name: "counted 500", err: &provider.HTTPError{StatusCode: http.StatusInternalServerError}, wantCategory: reliability.CategoryUpstream5xx, wantSignals: reliability.Signals{Retry: true, Fallback: true, CountBreaker: true}},
			{name: "counted 502", err: &provider.HTTPError{StatusCode: http.StatusBadGateway}, wantCategory: reliability.CategoryUpstream5xx, wantSignals: reliability.Signals{Retry: true, Fallback: true, CountBreaker: true}},
			{name: "counted 503", err: &provider.HTTPError{StatusCode: http.StatusServiceUnavailable}, wantCategory: reliability.CategoryUpstream5xx, wantSignals: reliability.Signals{Retry: true, Fallback: true, CountBreaker: true}},
			{name: "counted 504", err: &provider.HTTPError{StatusCode: http.StatusGatewayTimeout}, wantCategory: reliability.CategoryUpstream5xx, wantSignals: reliability.Signals{Retry: true, Fallback: true, CountBreaker: true}},
			{name: "uncounted 501", err: &provider.HTTPError{StatusCode: http.StatusNotImplemented}, wantCategory: reliability.CategoryUpstream5xx},
			{name: "protocol", err: provider.ErrInvalidResponse, wantCategory: reliability.CategoryUpstreamProtocol, wantSignals: reliability.Signals{Fallback: true, CountBreaker: true}},
			{name: "network", err: errors.New("test network failure"), wantCategory: reliability.CategoryNetwork, wantSignals: reliability.Signals{Retry: true, Fallback: true, CountBreaker: true}},
			{name: "timeout", err: context.DeadlineExceeded, wantCategory: reliability.CategoryTimeout, wantSignals: reliability.Signals{Retry: true, Fallback: true, CountBreaker: true}},
			{name: "client cancel", err: context.Canceled, wantCategory: reliability.CategoryCanceled},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				failure := reliability.FromProvider(context.Background(), "upstream", 2, 3, test.err)
				if failure.Category != test.wantCategory {
					t.Fatalf("category = %q, want %q", failure.Category, test.wantCategory)
				}
				if failure.ProviderID != "upstream" || failure.Candidate != 2 || failure.Attempt != 3 {
					t.Fatalf("safe metadata = %#v", failure)
				}
				if got := reliability.SignalsFor(failure); got != test.wantSignals {
					t.Fatalf("signals = %#v, want %#v", got, test.wantSignals)
				}
			})
		}

		budgetContext, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		budgetFailure := reliability.FromProvider(budgetContext, "upstream", 0, 1, context.DeadlineExceeded)
		if budgetFailure.Timeout != reliability.TimeoutRequestBudget || reliability.SignalsFor(budgetFailure) != (reliability.Signals{}) {
			t.Fatalf("request budget timeout = %#v, signals = %#v", budgetFailure, reliability.SignalsFor(budgetFailure))
		}
	})

	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	breaker, err := reliability.NewBreakerWithClock("upstream", reliability.BreakerConfig{
		FailureThreshold:  2,
		OpenDuration:      10 * time.Second,
		HalfOpenMaxProbes: 1,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("reliability.NewBreakerWithClock() error = %v", err)
	}

	ignoredFailures := []*reliability.Failure{
		{Category: reliability.CategoryKeyUnauthorized, ProviderID: "upstream", StatusCode: http.StatusUnauthorized},
		{Category: reliability.CategoryUpstreamRateLimit, ProviderID: "upstream", StatusCode: http.StatusTooManyRequests},
		{Category: reliability.CategoryCanceled, ProviderID: "upstream", Cause: context.Canceled},
	}
	for _, ignoredFailure := range ignoredFailures {
		permit, rejected := breaker.Allow()
		if rejected != nil || !permit.Complete(ignoredFailure) {
			t.Fatalf("complete ignored failure = %#v, %v", rejected, ignoredFailure.Category)
		}
	}
	if snapshot := breaker.Snapshot(); snapshot.State != reliability.StateClosed || snapshot.ConsecutiveFailures != 0 {
		t.Fatalf("breaker after ignored failures = %#v", snapshot)
	}

	countedFailure := &reliability.Failure{Category: reliability.CategoryNetwork, ProviderID: "upstream"}
	for range 2 {
		permit, rejected := breaker.Allow()
		if rejected != nil || !permit.Complete(countedFailure) {
			t.Fatalf("complete counted failure = %#v", rejected)
		}
	}
	if snapshot := breaker.Snapshot(); snapshot.State != reliability.StateOpen || snapshot.ConsecutiveFailures != 2 {
		t.Fatalf("open snapshot = %#v", snapshot)
	}
	if permit, rejected := breaker.Allow(); permit != nil || rejected == nil || rejected.Category != reliability.CategoryBreaker || rejected.RetryAfter != 10*time.Second {
		t.Fatalf("open admission = %#v, %#v", permit, rejected)
	}

	now = now.Add(10 * time.Second)
	probe, rejected := breaker.Allow()
	if rejected != nil {
		t.Fatalf("half-open probe rejected: %v", rejected)
	}
	if secondProbe, secondRejection := breaker.Allow(); secondProbe != nil || secondRejection == nil {
		t.Fatalf("second half-open probe = %#v, %#v", secondProbe, secondRejection)
	}
	if !probe.Complete(countedFailure) || probe.Complete(nil) {
		t.Fatal("half-open permit was not completed exactly once")
	}
	if snapshot := breaker.Snapshot(); snapshot.State != reliability.StateOpen {
		t.Fatalf("failed probe snapshot = %#v", snapshot)
	}

	now = now.Add(10 * time.Second)
	probe, rejected = breaker.Allow()
	if rejected != nil || !probe.Complete(nil) {
		t.Fatalf("successful half-open probe = %#v", rejected)
	}
	if snapshot := breaker.Snapshot(); snapshot.State != reliability.StateClosed {
		t.Fatalf("closed snapshot = %#v", snapshot)
	}
	abandoned, rejected := breaker.Allow()
	if rejected != nil || !abandoned.Abandon() || abandoned.Complete(nil) {
		t.Fatal("abandoned permit was not released exactly once")
	}

	for range 2 {
		permit, rejected := breaker.Allow()
		if rejected != nil || !permit.Complete(countedFailure) {
			t.Fatalf("reopen breaker = %#v", rejected)
		}
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()
	client := newTestCompatibleAdapter(t, upstream.URL)
	routes, err := routing.New(singleProviderTestDefinition("upstream"))
	if err != nil {
		t.Fatalf("routing.New() error = %v", err)
	}
	router := newSingleProviderTestRouter(t, client, testAccessController{}, testRateLimiter{
		decision: ratelimit.Decision{Allowed: true, Limit: 60, Remaining: 59, ResetAtUnix: 1_800_000_000},
	}, testResponseCache{}, routes, breaker, nil)

	openResponse := httptest.NewRecorder()
	router.ServeHTTP(openResponse, validChatRequest())
	if openResponse.Code != http.StatusServiceUnavailable || responseErrorCode(t, openResponse) != "provider_circuit_open" || openResponse.Header().Get("Retry-After") != "10" {
		t.Fatalf("open HTTP response = %d %s, retry-after=%q", openResponse.Code, openResponse.Body.String(), openResponse.Header().Get("Retry-After"))
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("open breaker upstream calls = %d, want 0", upstreamCalls.Load())
	}

	now = now.Add(10 * time.Second)
	recoveredResponse := httptest.NewRecorder()
	router.ServeHTTP(recoveredResponse, validChatRequest())
	if recoveredResponse.Code != http.StatusOK || upstreamCalls.Load() != 1 {
		t.Fatalf("recovered response = %d, upstream calls = %d", recoveredResponse.Code, upstreamCalls.Load())
	}
	if snapshot := breaker.Snapshot(); snapshot.State != reliability.StateClosed {
		t.Fatalf("recovered breaker snapshot = %#v", snapshot)
	}
}

func TestChatCompletionProviderQueue(t *testing.T) {
	queueConfig := reliability.QueueConfig{
		MaxInFlight: 1,
		MaxWaiting:  1,
		WaitTimeout: time.Second,
	}

	t.Run("acquires and releases exactly once", func(t *testing.T) {
		queue, err := reliability.NewProviderQueue("provider-a", queueConfig)
		if err != nil {
			t.Fatalf("NewProviderQueue() error = %v", err)
		}
		lease, failure := queue.Acquire(context.Background())
		if failure != nil || lease == nil {
			t.Fatalf("Acquire() = %#v, %#v", lease, failure)
		}
		if snapshot := queue.Snapshot(); snapshot.Active != 1 || snapshot.Waiting != 0 || snapshot.MaxInFlight != 1 {
			t.Fatalf("acquired snapshot = %#v", snapshot)
		}
		if !lease.Release() || lease.Release() {
			t.Fatal("queue lease was not released exactly once")
		}
		if snapshot := queue.Snapshot(); snapshot.Active != 0 {
			t.Fatalf("released snapshot = %#v", snapshot)
		}
	})

	t.Run("bounds waiting and propagates cancellation", func(t *testing.T) {
		queue, err := reliability.NewProviderQueue("provider-a", queueConfig)
		if err != nil {
			t.Fatalf("NewProviderQueue() error = %v", err)
		}
		holder, failure := queue.Acquire(context.Background())
		if failure != nil {
			t.Fatalf("acquire holder: %v", failure)
		}

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan *reliability.Failure, 1)
		go func() {
			lease, acquireFailure := queue.Acquire(ctx)
			if lease != nil {
				lease.Release()
			}
			result <- acquireFailure
		}()
		waitForQueueSnapshot(t, queue, func(snapshot reliability.QueueSnapshot) bool {
			return snapshot.Waiting == 1
		})

		if lease, fullFailure := queue.Acquire(context.Background()); lease != nil || fullFailure == nil || fullFailure.Queue != reliability.QueueFull {
			t.Fatalf("full queue admission = %#v, %#v", lease, fullFailure)
		}
		cancel()
		select {
		case canceledFailure := <-result:
			if canceledFailure == nil || canceledFailure.Category != reliability.CategoryCanceled || !errors.Is(canceledFailure, context.Canceled) {
				t.Fatalf("canceled wait failure = %#v", canceledFailure)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled queue waiter did not return")
		}
		waitForQueueSnapshot(t, queue, func(snapshot reliability.QueueSnapshot) bool {
			return snapshot.Waiting == 0
		})
		if snapshot := queue.Snapshot(); snapshot.Active != 1 || snapshot.Rejected != 1 || snapshot.Canceled != 1 {
			t.Fatalf("canceled snapshot = %#v", snapshot)
		}
		holder.Release()
	})

	t.Run("hands a released slot to one waiter", func(t *testing.T) {
		queue, err := reliability.NewProviderQueue("provider-a", queueConfig)
		if err != nil {
			t.Fatalf("NewProviderQueue() error = %v", err)
		}
		holder, failure := queue.Acquire(context.Background())
		if failure != nil {
			t.Fatalf("acquire holder: %v", failure)
		}
		type acquireResult struct {
			lease   *reliability.QueueLease
			failure *reliability.Failure
		}
		result := make(chan acquireResult, 1)
		go func() {
			lease, acquireFailure := queue.Acquire(context.Background())
			result <- acquireResult{lease: lease, failure: acquireFailure}
		}()
		waitForQueueSnapshot(t, queue, func(snapshot reliability.QueueSnapshot) bool {
			return snapshot.Waiting == 1
		})
		holder.Release()

		select {
		case acquired := <-result:
			if acquired.failure != nil || acquired.lease == nil {
				t.Fatalf("waiting Acquire() = %#v, %#v", acquired.lease, acquired.failure)
			}
			if snapshot := queue.Snapshot(); snapshot.Active != 1 || snapshot.Waiting != 0 {
				t.Fatalf("transferred snapshot = %#v", snapshot)
			}
			acquired.lease.Release()
		case <-time.After(time.Second):
			t.Fatal("waiting queue acquisition did not return")
		}
	})

	t.Run("times out without leaking a waiter", func(t *testing.T) {
		config := queueConfig
		config.WaitTimeout = 20 * time.Millisecond
		queue, err := reliability.NewProviderQueue("provider-a", config)
		if err != nil {
			t.Fatalf("NewProviderQueue() error = %v", err)
		}
		holder, failure := queue.Acquire(context.Background())
		if failure != nil {
			t.Fatalf("acquire holder: %v", failure)
		}
		lease, timeoutFailure := queue.Acquire(context.Background())
		if lease != nil || timeoutFailure == nil || timeoutFailure.Queue != reliability.QueueWaitTimeout {
			t.Fatalf("timed out admission = %#v, %#v", lease, timeoutFailure)
		}
		if snapshot := queue.Snapshot(); snapshot.Active != 1 || snapshot.Waiting != 0 || snapshot.TimedOut != 1 {
			t.Fatalf("timeout snapshot = %#v", snapshot)
		}
		holder.Release()
	})

	t.Run("isolates providers", func(t *testing.T) {
		config := queueConfig
		config.MaxWaiting = 0
		registry, err := reliability.NewQueueRegistry([]string{"provider-b", "provider-a"}, config)
		if err != nil {
			t.Fatalf("NewQueueRegistry() error = %v", err)
		}
		leaseA, failureA := registry.Acquire(context.Background(), "provider-a")
		leaseB, failureB := registry.Acquire(context.Background(), "provider-b")
		if failureA != nil || failureB != nil {
			t.Fatalf("isolated admission failures = %#v, %#v", failureA, failureB)
		}
		if lease, failure := registry.Acquire(context.Background(), "provider-a"); lease != nil || failure == nil || failure.Queue != reliability.QueueFull {
			t.Fatalf("provider-a second admission = %#v, %#v", lease, failure)
		}
		snapshots := registry.Snapshots()
		if len(snapshots) != 2 || snapshots[0].ProviderID != "provider-a" || snapshots[0].Active != 1 || snapshots[1].ProviderID != "provider-b" || snapshots[1].Active != 1 {
			t.Fatalf("registry snapshots = %#v", snapshots)
		}
		leaseA.Release()
		leaseB.Release()
	})

	t.Run("rejects before calling provider and keeps breaker healthy", func(t *testing.T) {
		var upstreamCalls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		}))
		defer upstream.Close()

		client := newTestCompatibleAdapter(t, upstream.URL)
		routes, err := routing.New(singleProviderTestDefinition("upstream"))
		if err != nil {
			t.Fatalf("routing.New() error = %v", err)
		}
		breaker, err := reliability.NewBreaker("upstream", reliability.DefaultBreakerConfig())
		if err != nil {
			t.Fatalf("NewBreaker() error = %v", err)
		}
		config := queueConfig
		config.MaxWaiting = 0
		queues, err := reliability.NewQueueRegistry([]string{"upstream"}, config)
		if err != nil {
			t.Fatalf("NewQueueRegistry() error = %v", err)
		}
		holder, failure := queues.Acquire(context.Background(), "upstream")
		if failure != nil {
			t.Fatalf("acquire holder: %v", failure)
		}

		router := newSingleProviderTestRouter(t, client, testAccessController{}, testRateLimiter{
			decision: ratelimit.Decision{Allowed: true, Limit: 60, Remaining: 59, ResetAtUnix: 1_800_000_000},
		}, testResponseCache{}, routes, breaker, queues)
		fullResponse := httptest.NewRecorder()
		router.ServeHTTP(fullResponse, validChatRequest())
		if fullResponse.Code != http.StatusServiceUnavailable || responseErrorCode(t, fullResponse) != "provider_queue_full" {
			t.Fatalf("full queue HTTP response = %d %s", fullResponse.Code, fullResponse.Body.String())
		}
		if upstreamCalls.Load() != 0 {
			t.Fatalf("full queue upstream calls = %d, want 0", upstreamCalls.Load())
		}
		if snapshot := breaker.Snapshot(); snapshot.State != reliability.StateClosed || snapshot.ConsecutiveFailures != 0 {
			t.Fatalf("breaker counted queue rejection = %#v", snapshot)
		}

		holder.Release()
		successResponse := httptest.NewRecorder()
		router.ServeHTTP(successResponse, validChatRequest())
		if successResponse.Code != http.StatusOK || upstreamCalls.Load() != 1 {
			t.Fatalf("recovered response = %d, upstream calls = %d", successResponse.Code, upstreamCalls.Load())
		}
		if snapshot, ok := queues.Snapshot("upstream"); !ok || snapshot.Active != 0 || snapshot.Waiting != 0 {
			t.Fatalf("released HTTP queue snapshot = %#v, exists=%v", snapshot, ok)
		}
	})
}

func TestChatCompletionReturnsTenantRateLimit(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer upstream.Close()

	client := newTestCompatibleAdapter(t, upstream.URL)
	router := newSingleProviderTestRouter(t, client, testAccessController{}, testRateLimiter{
		decision: ratelimit.Decision{
			Allowed:           false,
			Limit:             60,
			Remaining:         0,
			ResetAtUnix:       1_800_000_000,
			RetryAfterSeconds: 17,
		},
	}, testResponseCache{}, nil, nil, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d; body = %s", response.Code, http.StatusTooManyRequests, response.Body.String())
	}
	if got := responseErrorCode(t, response); got != "rate_limit_exceeded" {
		t.Errorf("error code = %q, want rate_limit_exceeded", got)
	}
	for header, want := range map[string]string{
		"X-RateLimit-Limit":     "60",
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     "1800000000",
		"Retry-After":           "17",
	} {
		if got := response.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
	}
}

func TestChatCompletionAppliesRateLimitFailurePolicy(t *testing.T) {
	tests := []struct {
		name          string
		limiter       testRateLimiter
		wantStatus    int
		wantCode      string
		wantBypass    string
		wantUpstreams int32
	}{
		{
			name: "fail open",
			limiter: testRateLimiter{decision: ratelimit.Decision{
				Allowed:  true,
				Bypassed: true,
			}},
			wantStatus:    http.StatusOK,
			wantBypass:    "bypassed",
			wantUpstreams: 1,
		},
		{
			name:          "fail closed",
			limiter:       testRateLimiter{err: ratelimit.ErrUnavailable},
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      "rate_limit_unavailable",
			wantUpstreams: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			}))
			defer upstream.Close()

			client := newTestCompatibleAdapter(t, upstream.URL)
			router := newSingleProviderTestRouter(t, client, testAccessController{}, test.limiter, testResponseCache{}, nil, nil, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, validChatRequest())

			if response.Code != test.wantStatus {
				t.Fatalf("status code = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantCode != "" {
				if got := responseErrorCode(t, response); got != test.wantCode {
					t.Errorf("error code = %q, want %q", got, test.wantCode)
				}
			}
			if got := response.Header().Get("X-RateLimit-Status"); got != test.wantBypass {
				t.Errorf("X-RateLimit-Status = %q, want %q", got, test.wantBypass)
			}
			if upstreamCalls.Load() != test.wantUpstreams {
				t.Errorf("upstream calls = %d, want %d", upstreamCalls.Load(), test.wantUpstreams)
			}
		})
	}
}

func TestChatCompletionCacheFlow(t *testing.T) {
	const (
		upstreamBody = `{"id":"upstream","choices":[{"message":{"role":"assistant","content":"upstream"}}]}`
		cachedBody   = `{"id":"cached","choices":[{"message":{"role":"assistant","content":"cached"}}]}`
	)

	tests := []struct {
		name              string
		upstreamStatus    int
		cache             testResponseCache
		wantStatus        int
		wantCacheStatus   string
		wantBody          string
		wantUpstreamCalls int32
		wantLookupCalls   int32
		wantStoreCalls    int32
		cacheControl      string
	}{
		{
			name: "hit skips provider",
			cache: testResponseCache{result: responsecache.Result{
				Status: responsecache.StatusHit,
				Body:   []byte(cachedBody),
			}},
			wantStatus:      http.StatusOK,
			wantCacheStatus: string(responsecache.StatusHit),
			wantBody:        cachedBody,
			wantLookupCalls: 1,
		},
		{
			name:              "miss stores successful response",
			cache:             testResponseCache{result: responsecache.Result{Status: responsecache.StatusMiss}},
			wantStatus:        http.StatusOK,
			wantCacheStatus:   string(responsecache.StatusMiss),
			wantBody:          upstreamBody,
			wantUpstreamCalls: 1,
			wantLookupCalls:   1,
			wantStoreCalls:    1,
		},
		{
			name:              "lookup failure bypasses cache",
			cache:             testResponseCache{lookupErr: errors.New("Redis unavailable")},
			wantStatus:        http.StatusOK,
			wantCacheStatus:   string(responsecache.StatusBypass),
			wantBody:          upstreamBody,
			wantUpstreamCalls: 1,
			wantLookupCalls:   1,
		},
		{
			name:              "upstream failure is not stored",
			upstreamStatus:    http.StatusInternalServerError,
			cache:             testResponseCache{result: responsecache.Result{Status: responsecache.StatusMiss}},
			wantStatus:        http.StatusBadGateway,
			wantUpstreamCalls: 1,
			wantLookupCalls:   1,
		},
		{
			name:              "no-store bypasses cache",
			cache:             testResponseCache{result: responsecache.Result{Status: responsecache.StatusHit, Body: []byte(cachedBody)}},
			cacheControl:      "no-store",
			wantStatus:        http.StatusOK,
			wantCacheStatus:   string(responsecache.StatusBypass),
			wantBody:          upstreamBody,
			wantUpstreamCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			var lookupCalls atomic.Int32
			var storeCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				status := test.upstreamStatus
				if status == 0 {
					status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, upstreamBody)
			}))
			defer upstream.Close()

			client := newTestCompatibleAdapter(t, upstream.URL)
			cache := test.cache
			cache.onLookup = func(tenantID, model string, requestBody []byte) {
				lookupCalls.Add(1)
			}
			cache.onStore = func(tenantID, model string, requestBody, responseBody []byte) {
				storeCalls.Add(1)
				if tenantID != "tenant-test-id" || model != "demo-model" {
					t.Errorf("cache scope = tenant %q, model %q", tenantID, model)
				}
				if string(responseBody) != upstreamBody {
					t.Errorf("stored response = %s, want %s", responseBody, upstreamBody)
				}
			}
			router := newSingleProviderTestRouter(t, client, testAccessController{}, testRateLimiter{
				decision: ratelimit.Decision{Allowed: true},
			}, cache, nil, nil, nil)
			response := httptest.NewRecorder()
			request := validChatRequest()
			request.Header.Set("Cache-Control", test.cacheControl)
			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := response.Header().Get("X-Model-Velo-Cache"); got != test.wantCacheStatus {
				t.Errorf("X-Model-Velo-Cache = %q, want %q", got, test.wantCacheStatus)
			}
			if test.wantBody != "" && response.Body.String() != test.wantBody {
				t.Errorf("body = %s, want %s", response.Body.String(), test.wantBody)
			}
			if upstreamCalls.Load() != test.wantUpstreamCalls {
				t.Errorf("upstream calls = %d, want %d", upstreamCalls.Load(), test.wantUpstreamCalls)
			}
			if lookupCalls.Load() != test.wantLookupCalls {
				t.Errorf("cache lookup calls = %d, want %d", lookupCalls.Load(), test.wantLookupCalls)
			}
			if storeCalls.Load() != test.wantStoreCalls {
				t.Errorf("cache store calls = %d, want %d", storeCalls.Load(), test.wantStoreCalls)
			}
		})
	}
}

func TestChatRequestDependencyOrder(t *testing.T) {
	tests := []struct {
		name            string
		authenticateErr error
		authorizeErr    error
		limitDecision   ratelimit.Decision
		cacheResult     responsecache.Result
		wantStatus      int
		wantEvents      string
	}{
		{
			name:            "authentication stops the chain",
			authenticateErr: apikey.ErrInvalidCredential,
			wantStatus:      http.StatusUnauthorized,
			wantEvents:      "authenticate",
		},
		{
			name:         "authorization stops the chain",
			authorizeErr: apikey.ErrModelNotAllowed,
			wantStatus:   http.StatusForbidden,
			wantEvents:   "authenticate,authorize",
		},
		{
			name:          "rate limit stops the chain",
			limitDecision: ratelimit.Decision{Allowed: false},
			wantStatus:    http.StatusTooManyRequests,
			wantEvents:    "authenticate,authorize,limit",
		},
		{
			name:          "cache hit skips provider",
			limitDecision: ratelimit.Decision{Allowed: true},
			cacheResult: responsecache.Result{
				Status: responsecache.StatusHit,
				Body:   []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`),
			},
			wantStatus: http.StatusOK,
			wantEvents: "authenticate,authorize,limit,cache_lookup",
		},
		{
			name:          "cache miss calls provider then stores",
			limitDecision: ratelimit.Decision{Allowed: true},
			cacheResult:   responsecache.Result{Status: responsecache.StatusMiss},
			wantStatus:    http.StatusOK,
			wantEvents:    "authenticate,authorize,limit,cache_lookup,provider,cache_store",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const tenantID = "tenant-order-test"
			var eventsMu sync.Mutex
			events := make([]string, 0, 6)
			record := func(event string) {
				eventsMu.Lock()
				events = append(events, event)
				eventsMu.Unlock()
			}
			assertScope := func(gotTenantID, model string) {
				t.Helper()
				if gotTenantID != tenantID || model != "demo-model" {
					t.Errorf("dependency scope = tenant %q, model %q", gotTenantID, model)
				}
			}

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				record("provider")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			}))
			defer upstream.Close()

			client := newTestCompatibleAdapter(t, upstream.URL)
			access := testAccessController{
				identity:        apikey.Identity{TenantID: tenantID, APIKeyID: "key-order-test"},
				authenticateErr: test.authenticateErr,
				authorizeErr:    test.authorizeErr,
				onAuthenticate: func() {
					record("authenticate")
				},
				onAuthorize: func(gotTenantID, model string) {
					record("authorize")
					assertScope(gotTenantID, model)
				},
			}
			limiter := testRateLimiter{
				decision: test.limitDecision,
				onAllow: func(gotTenantID, model string) {
					record("limit")
					assertScope(gotTenantID, model)
				},
			}
			cache := testResponseCache{
				result: test.cacheResult,
				onLookup: func(gotTenantID, model string, _ []byte) {
					record("cache_lookup")
					assertScope(gotTenantID, model)
				},
				onStore: func(gotTenantID, model string, _, _ []byte) {
					record("cache_store")
					assertScope(gotTenantID, model)
				},
			}
			router := newSingleProviderTestRouter(t, client, access, limiter, cache, nil, nil, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, validChatRequest())

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			eventsMu.Lock()
			gotEvents := strings.Join(events, ",")
			eventsMu.Unlock()
			if gotEvents != test.wantEvents {
				t.Errorf("events = %q, want %q", gotEvents, test.wantEvents)
			}
		})
	}
}

func TestChatCompletionRejectsInvalidRequests(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		{name: "missing content type", body: `{}`, wantStatus: http.StatusUnsupportedMediaType, wantCode: "invalid_content_type"},
		{name: "wrong content type", contentType: "text/plain", body: `{}`, wantStatus: http.StatusUnsupportedMediaType, wantCode: "invalid_content_type"},
		{name: "empty body", contentType: "application/json", body: ``, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "null body", contentType: "application/json", body: `null`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "malformed JSON", contentType: "application/json", body: `{"model":`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "multiple JSON values", contentType: "application/json", body: `{} {}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "missing model", contentType: "application/json", body: `{"messages":[{"role":"user","content":"hello"}]}`, wantStatus: http.StatusBadRequest, wantCode: "missing_model"},
		{name: "blank model", contentType: "application/json", body: `{"model":"  ","messages":[{"role":"user","content":"hello"}]}`, wantStatus: http.StatusBadRequest, wantCode: "missing_model"},
		{name: "missing messages", contentType: "application/json", body: `{"model":"demo-model"}`, wantStatus: http.StatusBadRequest, wantCode: "missing_messages"},
		{name: "empty messages", contentType: "application/json", body: `{"model":"demo-model","messages":[]}`, wantStatus: http.StatusBadRequest, wantCode: "missing_messages"},
		{name: "unsupported role", contentType: "application/json", body: `{"model":"demo-model","messages":[{"role":"critic","content":"hello"}]}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_message_role"},
		{name: "missing content", contentType: "application/json", body: `{"model":"demo-model","messages":[{"role":"user","content":""}]}`, wantStatus: http.StatusBadRequest, wantCode: "missing_message_content"},
		{name: "empty content parts", contentType: "application/json", body: `{"model":"demo-model","messages":[{"role":"user","content":[]}]}`, wantStatus: http.StatusBadRequest, wantCode: "missing_message_content"},
	}

	router := newTestRouter(t, upstream.URL, time.Second)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
			request.Header.Set("X-Request-ID", "client-error-id")
			request.Header.Set("Authorization", "Bearer model-velo-test-key")
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status code = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := responseErrorCode(t, response); got != test.wantCode {
				t.Errorf("error code = %q, want %q", got, test.wantCode)
			}
			if got := response.Header().Get("X-Request-ID"); got != "client-error-id" {
				t.Errorf("X-Request-ID = %q, want %q", got, "client-error-id")
			}
		})
	}

	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls)
	}
}

func TestChatCompletionRejectsLargeBody(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer upstream.Close()

	router := newTestRouter(t, upstream.URL, time.Second)
	requestBody := strings.Repeat("x", (16<<20)+1)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer model-velo-test-key")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if got := responseErrorCode(t, response); got != "request_too_large" {
		t.Errorf("error code = %q, want %q", got, "request_too_large")
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls)
	}
}

func TestChatCompletionMapsUpstreamHTTPError(t *testing.T) {
	tests := []struct {
		name           string
		upstream       int
		retryAfter     string
		wantStatus     int
		wantCode       string
		wantRetryAfter string
	}{
		{name: "bad request", upstream: http.StatusBadRequest, wantStatus: http.StatusBadRequest, wantCode: "upstream_rejected_request"},
		{name: "unauthorized", upstream: http.StatusUnauthorized, wantStatus: http.StatusBadGateway, wantCode: "upstream_http_error"},
		{name: "forbidden", upstream: http.StatusForbidden, wantStatus: http.StatusBadGateway, wantCode: "upstream_http_error"},
		{name: "rate limited", upstream: http.StatusTooManyRequests, retryAfter: "17", wantStatus: http.StatusTooManyRequests, wantCode: "upstream_rate_limited", wantRetryAfter: "17"},
		{name: "internal server error", upstream: http.StatusInternalServerError, wantStatus: http.StatusBadGateway, wantCode: "upstream_http_error"},
		{name: "bad gateway", upstream: http.StatusBadGateway, wantStatus: http.StatusBadGateway, wantCode: "upstream_http_error"},
		{name: "service unavailable", upstream: http.StatusServiceUnavailable, wantStatus: http.StatusBadGateway, wantCode: "upstream_http_error"},
		{name: "gateway timeout", upstream: http.StatusGatewayTimeout, wantStatus: http.StatusBadGateway, wantCode: "upstream_http_error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				w.WriteHeader(test.upstream)
				_, _ = io.WriteString(w, `<p>internal upstream detail</p>`)
			}))
			defer upstream.Close()

			router := newTestRouter(t, upstream.URL, time.Second)
			request := validChatRequest()
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status code = %d, want %d", response.Code, test.wantStatus)
			}
			if got := responseErrorCode(t, response); got != test.wantCode {
				t.Errorf("error code = %q, want %q", got, test.wantCode)
			}
			if got := response.Header().Get("Retry-After"); got != test.wantRetryAfter {
				t.Errorf("Retry-After = %q, want %q", got, test.wantRetryAfter)
			}
			if strings.Contains(response.Body.String(), "internal upstream detail") {
				t.Fatal("response leaked upstream error body")
			}
			if got := response.Header().Get("X-Request-ID"); got != "request-test-id" {
				t.Errorf("X-Request-ID = %q, want %q", got, "request-test-id")
			}
		})
	}
}

func TestChatCompletionRetriesOnlyRecoverableFailures(t *testing.T) {
	tests := []struct {
		name       string
		statuses   []int
		wantStatus int
		wantCalls  int32
	}{
		{name: "retry service unavailable", statuses: []int{http.StatusServiceUnavailable, http.StatusOK}, wantStatus: http.StatusOK, wantCalls: 2},
		{name: "do not retry bad request", statuses: []int{http.StatusBadRequest}, wantStatus: http.StatusBadRequest, wantCalls: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := int(calls.Add(1))
				status := test.statuses[len(test.statuses)-1]
				if call <= len(test.statuses) {
					status = test.statuses[call-1]
				}
				if status != http.StatusOK {
					w.WriteHeader(status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			}))
			defer upstream.Close()

			router := newRetryTestRouter(t, upstream.URL)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, validChatRequest())

			if response.Code != test.wantStatus {
				t.Fatalf("status code = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := calls.Load(); got != test.wantCalls {
				t.Fatalf("upstream calls = %d, want %d", got, test.wantCalls)
			}
		})
	}

	t.Run("wait for a cooling key only when the budget permits", func(t *testing.T) {
		keys, err := reliability.NewProviderKeyRegistry(
			[]string{"upstream"},
			[]reliability.ProviderKeySet{{
				ProviderID: "upstream",
				Keys:       []reliability.ProviderKey{{ID: "primary", Secret: "provider-test-key"}},
			}},
		)
		if err != nil {
			t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
		}
		selection, failure := keys.Select("upstream")
		if failure != nil {
			t.Fatalf("keys.Select() failure = %v", failure)
		}
		selection.Complete(&reliability.Failure{
			Category:      reliability.CategoryUpstreamRateLimit,
			RetryAfter:    20 * time.Millisecond,
			RetryAfterSet: true,
		})

		_, failure = keys.Select("upstream")
		if failure == nil || failure.Category != reliability.CategoryKeyExhausted {
			t.Fatalf("cooling key failure = %#v", failure)
		}
		policy, err := reliability.NewRetryPolicy(reliability.DefaultRetryConfig())
		if err != nil {
			t.Fatalf("reliability.NewRetryPolicy() error = %v", err)
		}
		if !policy.ShouldRetry(failure, 1) {
			t.Fatal("cooling key exhaustion should be retryable")
		}
		if !policy.Wait(context.Background(), policy.Backoff(failure, 1)) {
			t.Fatal("cooldown wait ended before the key became available")
		}
		if _, failure = keys.Select("upstream"); failure != nil {
			t.Fatalf("key after cooldown failure = %v", failure)
		}

		shortContext, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if policy.Wait(shortContext, time.Second) {
			t.Fatal("wait longer than the remaining budget was accepted")
		}
		if err := shortContext.Err(); err != nil {
			t.Fatalf("budget was consumed instead of rejected immediately: %v", err)
		}
	})

	t.Run("older success cannot erase a newer key cooldown", func(t *testing.T) {
		keys, err := reliability.NewProviderKeyRegistry(
			[]string{"upstream"},
			[]reliability.ProviderKeySet{{
				ProviderID: "upstream",
				Keys:       []reliability.ProviderKey{{ID: "primary", Secret: "provider-test-key"}},
			}},
		)
		if err != nil {
			t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
		}
		older, _ := keys.Select("upstream")
		newer, _ := keys.Select("upstream")
		shorter, _ := keys.Select("upstream")
		newer.Complete(&reliability.Failure{
			Category:      reliability.CategoryUpstreamRateLimit,
			RetryAfter:    time.Minute,
			RetryAfterSet: true,
		})
		shorter.Complete(&reliability.Failure{
			Category:      reliability.CategoryUpstreamRateLimit,
			RetryAfter:    time.Second,
			RetryAfterSet: true,
		})
		older.Complete(nil)

		snapshot := keys.Snapshots()[0]
		if snapshot.State != reliability.ProviderKeyCooling || time.Until(snapshot.CooldownUntil) < 50*time.Second {
			t.Fatalf("key cooldown was cleared or shortened: %#v", snapshot)
		}
	})

	t.Run("admission waits do not consume an upstream attempt", func(t *testing.T) {
		var upstreamCalls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		}))
		defer upstream.Close()

		adapters, err := provider.NewAdapterRegistry([]provider.AdapterConfig{{
			ProviderID: "upstream",
			Protocol:   provider.ProtocolOpenAICompatible,
			BaseURL:    upstream.URL,
		}})
		if err != nil {
			t.Fatalf("provider.NewAdapterRegistry() error = %v", err)
		}
		breakers, err := reliability.NewBreakerRegistry([]string{"upstream"}, reliability.DefaultBreakerConfig())
		if err != nil {
			t.Fatalf("reliability.NewBreakerRegistry() error = %v", err)
		}
		queues, err := reliability.NewQueueRegistry([]string{"upstream"}, reliability.DefaultQueueConfig())
		if err != nil {
			t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
		}
		keys, err := reliability.NewProviderKeyRegistry(
			[]string{"upstream"},
			[]reliability.ProviderKeySet{{
				ProviderID: "upstream",
				Keys:       []reliability.ProviderKey{{ID: "primary", Secret: "provider-test-key"}},
			}},
		)
		if err != nil {
			t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
		}
		cooling, _ := keys.Select("upstream")
		cooling.Complete(&reliability.Failure{
			Category:      reliability.CategoryUpstreamRateLimit,
			RetryAfter:    20 * time.Millisecond,
			RetryAfterSet: true,
		})
		retryConfig := reliability.DefaultRetryConfig()
		retryConfig.MaxAttempts = 1
		retryConfig.InitialBackoff = 10 * time.Millisecond
		retryConfig.MaxBackoff = 20 * time.Millisecond
		retryConfig.JitterRatio = 0
		retryConfig.RequestTimeout = time.Second
		retryConfig.AttemptTimeout = time.Second
		retry, err := reliability.NewRetryPolicy(retryConfig)
		if err != nil {
			t.Fatalf("reliability.NewRetryPolicy() error = %v", err)
		}
		executor, err := reliability.NewAttemptExecutor(adapters, breakers, queues, keys, retry)
		if err != nil {
			t.Fatalf("reliability.NewAttemptExecutor() error = %v", err)
		}
		request, err := provider.ParseChatRequest([]byte(`{"model":"demo-model","messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			t.Fatalf("provider.ParseChatRequest() error = %v", err)
		}
		result, failure := executor.Execute(context.Background(), reliability.AttemptInput{
			RequestID:      "attempt-count-test",
			RequestedModel: "demo-model",
			Request:        request,
			Candidate: routing.Candidate{
				ProviderID:    "upstream",
				UpstreamModel: "demo-model",
			},
		})
		if failure != nil {
			t.Fatalf("executor.Execute() failure = %v", failure)
		}
		if result.Attempts != 1 || len(result.Trail) != 1 || upstreamCalls.Load() != 1 {
			t.Fatalf("result = %#v, upstream calls = %d", result, upstreamCalls.Load())
		}
	})

	t.Run("403 remains request-local while 401 disables the key", func(t *testing.T) {
		keys, err := reliability.NewProviderKeyRegistry(
			[]string{"upstream"},
			[]reliability.ProviderKeySet{{
				ProviderID: "upstream",
				Keys: []reliability.ProviderKey{
					{ID: "forbidden", Secret: "forbidden-secret"},
					{ID: "unauthorized", Secret: "unauthorized-secret"},
				},
			}},
		)
		if err != nil {
			t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
		}
		forbidden, failure := keys.Select("upstream")
		if failure != nil {
			t.Fatalf("select forbidden key failure = %v", failure)
		}
		forbidden.Complete(&reliability.Failure{Category: reliability.CategoryKeyForbidden})

		unauthorized, failure := keys.Select("upstream")
		if failure != nil {
			t.Fatalf("select unauthorized key failure = %v", failure)
		}
		unauthorized.Complete(&reliability.Failure{Category: reliability.CategoryKeyUnauthorized})

		states := make(map[string]reliability.ProviderKeyState)
		for _, snapshot := range keys.Snapshots() {
			states[snapshot.KeyID] = snapshot.State
		}
		if states[forbidden.KeyID()] != reliability.ProviderKeyAvailable {
			t.Errorf("403 key state = %q, want available", states[forbidden.KeyID()])
		}
		if states[unauthorized.KeyID()] != reliability.ProviderKeyDisabled {
			t.Errorf("401 key state = %q, want disabled", states[unauthorized.KeyID()])
		}
	})
}

func newRetryTestRouter(t *testing.T, baseURL string) http.Handler {
	t.Helper()

	adapters, err := provider.NewAdapterRegistry([]provider.AdapterConfig{{
		ProviderID: "upstream",
		Protocol:   provider.ProtocolOpenAICompatible,
		BaseURL:    baseURL,
	}})
	if err != nil {
		t.Fatalf("provider.NewAdapterRegistry() error = %v", err)
	}
	routes, err := routing.New(singleProviderTestDefinition("upstream"))
	if err != nil {
		t.Fatalf("routing.New() error = %v", err)
	}
	breakers, err := reliability.NewBreakerRegistry([]string{"upstream"}, reliability.BreakerConfig{
		FailureThreshold:  5,
		OpenDuration:      time.Second,
		HalfOpenMaxProbes: 1,
	})
	if err != nil {
		t.Fatalf("reliability.NewBreakerRegistry() error = %v", err)
	}
	queues, err := reliability.NewQueueRegistry([]string{"upstream"}, reliability.DefaultQueueConfig())
	if err != nil {
		t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
	}
	keys, err := reliability.NewProviderKeyRegistry([]string{"upstream"}, []reliability.ProviderKeySet{{
		ProviderID: "upstream",
		Keys:       []reliability.ProviderKey{{ID: "primary", Secret: "provider-test-key"}},
	}})
	if err != nil {
		t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
	}
	retry, err := reliability.NewRetryPolicy(reliability.RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2,
		JitterRatio:       0,
		RequestTimeout:    2 * time.Second,
		AttemptTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("reliability.NewRetryPolicy() error = %v", err)
	}

	return httpapi.NewRouter(
		adapters,
		testAccessController{},
		testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
		testResponseCache{},
		routes,
		breakers,
		queues,
		keys,
		retry,
		&recordingUsageEmitter{},
		emptyUsageReader{},
	)
}

func TestChatCompletionFallbackPolicy(t *testing.T) {
	tests := []struct {
		name               string
		primaryStatus      int
		primaryErrorBody   string
		primaryProtocol    string
		secondaryProtocol  string
		requestBody        string
		wantStatus         int
		wantPrimaryCalls   int32
		wantSecondaryCalls int32
		wantCacheStores    int32
	}{
		{name: "stop after primary success", primaryStatus: http.StatusOK, wantStatus: http.StatusOK, wantPrimaryCalls: 1, wantCacheStores: 1},
		{name: "fallback after service unavailable", primaryStatus: http.StatusServiceUnavailable, wantStatus: http.StatusOK, wantPrimaryCalls: 1, wantSecondaryCalls: 1},
		{name: "fallback when model is unavailable", primaryStatus: http.StatusNotFound, primaryErrorBody: `{"error":{"code":"model_not_found"}}`, wantStatus: http.StatusOK, wantPrimaryCalls: 1, wantSecondaryCalls: 1},
		{name: "do not fallback after bad request", primaryStatus: http.StatusBadRequest, wantStatus: http.StatusBadRequest, wantPrimaryCalls: 1},
		{
			name:               "skip primary when it lacks vision",
			primaryProtocol:    provider.ProtocolDeepSeek,
			requestBody:        `{"model":"demo-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`,
			wantStatus:         http.StatusOK,
			wantSecondaryCalls: 1,
			wantCacheStores:    1,
		},
		{
			name:              "reject when every candidate lacks vision",
			primaryProtocol:   provider.ProtocolDeepSeek,
			secondaryProtocol: provider.ProtocolDeepSeek,
			requestBody:       `{"model":"demo-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`,
			wantStatus:        http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cacheStores atomic.Int32
			var primaryCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				if test.primaryStatus == http.StatusOK {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
					return
				}
				w.WriteHeader(test.primaryStatus)
				_, _ = io.WriteString(w, test.primaryErrorBody)
			}))
			defer primary.Close()

			var secondaryCalls atomic.Int32
			secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondaryCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			}))
			defer secondary.Close()

			router := newFallbackTestRouter(
				t,
				primary.URL,
				secondary.URL,
				test.primaryProtocol,
				test.secondaryProtocol,
				testResponseCache{onStore: func(string, string, []byte, []byte) {
					cacheStores.Add(1)
				}},
			)
			response := httptest.NewRecorder()
			request := validChatRequest()
			if test.requestBody != "" {
				request = chatRequest(test.requestBody)
			}
			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status code = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := primaryCalls.Load(); got != test.wantPrimaryCalls {
				t.Errorf("primary calls = %d, want %d", got, test.wantPrimaryCalls)
			}
			if got := secondaryCalls.Load(); got != test.wantSecondaryCalls {
				t.Errorf("secondary calls = %d, want %d", got, test.wantSecondaryCalls)
			}
			if got := cacheStores.Load(); got != test.wantCacheStores {
				t.Errorf("cache stores = %d, want %d", got, test.wantCacheStores)
			}
		})
	}
}

func newFallbackTestRouter(
	t *testing.T,
	primaryURL string,
	secondaryURL string,
	primaryProtocol string,
	secondaryProtocol string,
	cache testResponseCache,
) http.Handler {
	t.Helper()
	if primaryProtocol == "" {
		primaryProtocol = provider.ProtocolOpenAICompatible
	}
	if secondaryProtocol == "" {
		secondaryProtocol = provider.ProtocolOpenAICompatible
	}

	providerIDs := []string{"primary", "secondary"}
	adapters, err := provider.NewAdapterRegistry([]provider.AdapterConfig{
		{ProviderID: "primary", Protocol: primaryProtocol, BaseURL: primaryURL},
		{ProviderID: "secondary", Protocol: secondaryProtocol, BaseURL: secondaryURL},
	})
	if err != nil {
		t.Fatalf("provider.NewAdapterRegistry() error = %v", err)
	}
	routes, err := routing.New(routing.Definition{
		Providers: []routing.Provider{
			{
				ID:                "primary",
				Type:              primaryProtocol,
				Models:            []string{"demo-model"},
				ModelCapabilities: testModelCapabilities(primaryProtocol),
			},
			{
				ID:                "secondary",
				Type:              secondaryProtocol,
				Models:            []string{"demo-model"},
				ModelCapabilities: testModelCapabilities(secondaryProtocol),
			},
		},
		Rules: []routing.Rule{{
			Model: "demo-model",
			Candidates: []routing.Target{
				{ProviderID: "primary"},
				{ProviderID: "secondary"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("routing.New() error = %v", err)
	}
	breakers, err := reliability.NewBreakerRegistry(providerIDs, reliability.DefaultBreakerConfig())
	if err != nil {
		t.Fatalf("reliability.NewBreakerRegistry() error = %v", err)
	}
	queues, err := reliability.NewQueueRegistry(providerIDs, reliability.DefaultQueueConfig())
	if err != nil {
		t.Fatalf("reliability.NewQueueRegistry() error = %v", err)
	}
	keys, err := reliability.NewProviderKeyRegistry(providerIDs, []reliability.ProviderKeySet{
		{ProviderID: "primary", Keys: []reliability.ProviderKey{{ID: "primary-key", Secret: "primary-secret"}}},
		{ProviderID: "secondary", Keys: []reliability.ProviderKey{{ID: "secondary-key", Secret: "secondary-secret"}}},
	})
	if err != nil {
		t.Fatalf("reliability.NewProviderKeyRegistry() error = %v", err)
	}
	retry, err := reliability.NewRetryPolicy(reliability.RetryConfig{
		MaxAttempts:       1,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2,
		RequestTimeout:    2 * time.Second,
		AttemptTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("reliability.NewRetryPolicy() error = %v", err)
	}

	return httpapi.NewRouter(
		adapters,
		testAccessController{},
		testRateLimiter{decision: ratelimit.Decision{Allowed: true}},
		cache,
		routes,
		breakers,
		queues,
		keys,
		retry,
		&recordingUsageEmitter{},
		emptyUsageReader{},
	)
}

type recordingUsageEmitter struct {
	mu     sync.Mutex
	events []usage.Event
	err    error
}

func (emitter *recordingUsageEmitter) Emit(_ context.Context, event usage.Event) (string, error) {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	emitter.events = append(emitter.events, event)
	return "test-entry", emitter.err
}

func (emitter *recordingUsageEmitter) snapshot() []usage.Event {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	return append([]usage.Event(nil), emitter.events...)
}

func testModelCapabilities(protocol string) map[string][]provider.Capability {
	capabilities := []provider.Capability{provider.CapabilityText, provider.CapabilityImage}
	if protocol == provider.ProtocolDeepSeek {
		capabilities = capabilities[:1]
	}
	return map[string][]provider.Capability{"demo-model": capabilities}
}

func TestChatCompletionMapsInvalidUpstreamResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `not a JSON response`)
	}))
	defer upstream.Close()

	router := newTestRouter(t, upstream.URL, time.Second)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if got := responseErrorCode(t, response); got != "invalid_upstream_response" {
		t.Errorf("error code = %q, want %q", got, "invalid_upstream_response")
	}
}

func TestChatCompletionMapsOversizedUpstreamResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", (8<<20)+1))
	}))
	defer upstream.Close()

	router := newTestRouter(t, upstream.URL, time.Second)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if got := responseErrorCode(t, response); got != "upstream_response_too_large" {
		t.Errorf("error code = %q, want %q", got, "upstream_response_too_large")
	}
}

func TestChatCompletionMapsInterruptedUpstreamResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server does not support connection hijacking")
			return
		}

		connection, buffer, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack upstream connection: %v", err)
			return
		}
		defer connection.Close()

		_, _ = buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 64\r\nConnection: close\r\n\r\n{\"choices\":[")
		_ = buffer.Flush()
	}))
	defer upstream.Close()

	router := newTestRouter(t, upstream.URL, time.Second)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if got := responseErrorCode(t, response); got != "upstream_unavailable" {
		t.Errorf("error code = %q, want %q", got, "upstream_unavailable")
	}
}

func TestChatCompletionMapsUpstreamTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	router := newTestRouter(t, upstream.URL, 100*time.Millisecond)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())

	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusGatewayTimeout)
	}
	if got := responseErrorCode(t, response); got != "upstream_timeout" {
		t.Errorf("error code = %q, want %q", got, "upstream_timeout")
	}
}

func TestChatCompletionMapsNetworkFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstreamURL := upstream.URL
	upstream.Close()

	router := newTestRouter(t, upstreamURL, time.Second)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, validChatRequest())

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if got := responseErrorCode(t, response); got != "upstream_unavailable" {
		t.Errorf("error code = %q, want %q", got, "upstream_unavailable")
	}
}

func responseErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, response.Body.String())
	}

	return envelope.Error.Code
}

func waitForQueueSnapshot(
	t *testing.T,
	queue *reliability.ProviderQueue,
	ready func(reliability.QueueSnapshot) bool,
) reliability.QueueSnapshot {
	t.Helper()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := queue.Snapshot()
		if ready(snapshot) {
			return snapshot
		}
		select {
		case <-deadline.C:
			t.Fatalf("queue snapshot did not reach expected state; last = %#v", snapshot)
		case <-ticker.C:
		}
	}
}

func validChatRequest() *http.Request {
	return chatRequest(`{"model":"demo-model","messages":[{"role":"user","content":"hello"}]}`)
}

func chatRequest(body string) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "request-test-id")
	request.Header.Set("Authorization", "Bearer model-velo-test-key")
	return request
}
