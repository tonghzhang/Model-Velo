package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompatibleAdapterOpenStream(t *testing.T) {
	type observedRequest struct {
		authorization string
		accept        string
		contentType   string
		requestID     string
		body          map[string]json.RawMessage
	}
	observed := make(chan observedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(request.Body).Decode(&body)
		observed <- observedRequest{
			authorization: request.Header.Get("Authorization"),
			accept:        request.Header.Get("Accept"),
			contentType:   request.Header.Get("Content-Type"),
			requestID:     request.Header.Get("X-Request-ID"),
			body:          body,
		}

		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(writer, ": heartbeat\r\n\r\n")
		_, _ = io.WriteString(writer, "event: message\n")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":\n")
		_, _ = io.WriteString(writer, "data: {\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	adapter, err := newCompatibleChatAdapter(ProtocolOpenAICompatible, upstream.URL, DefaultHTTPConfig())
	if err != nil {
		t.Fatalf("newCompatibleChatAdapter() error = %v", err)
	}
	request, err := ParseChatRequest([]byte(`{"model":"requested-model","messages":[{"role":"user","content":"hello"}],"stream":false,"seed":7}`))
	if err != nil {
		t.Fatalf("ParseChatRequest() error = %v", err)
	}
	stream, err := adapter.OpenStream(context.Background(), ChatInput{
		RequestID:     "stream-request-id",
		Request:       request,
		ModelOverride: "mapped-model",
	}, "provider-test-key")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	defer stream.Close()

	gotRequest := <-observed
	if gotRequest.authorization != "Bearer provider-test-key" || gotRequest.accept != "text/event-stream" {
		t.Fatalf("upstream auth/accept = %q, %q", gotRequest.authorization, gotRequest.accept)
	}
	if gotRequest.contentType != "application/json" || gotRequest.requestID != "stream-request-id" {
		t.Fatalf("upstream content-type/request ID = %q, %q", gotRequest.contentType, gotRequest.requestID)
	}
	var streamEnabled bool
	var model string
	if json.Unmarshal(gotRequest.body["stream"], &streamEnabled) != nil || !streamEnabled {
		t.Fatalf("upstream stream field = %s", gotRequest.body["stream"])
	}
	if json.Unmarshal(gotRequest.body["model"], &model) != nil || model != "mapped-model" {
		t.Fatalf("upstream model = %q", model)
	}
	if string(gotRequest.body["seed"]) != "7" {
		t.Fatalf("upstream seed = %s", gotRequest.body["seed"])
	}
	var streamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if json.Unmarshal(gotRequest.body["stream_options"], &streamOptions) != nil || !streamOptions.IncludeUsage {
		t.Fatalf("upstream stream_options = %s, want include_usage=true", gotRequest.body["stream_options"])
	}

	first, err := stream.Next()
	if err != nil {
		t.Fatalf("first Next() error = %v", err)
	}
	if first.Done || !strings.Contains(string(first.Data), `"content":"hello"`) {
		t.Fatalf("first event = %#v", first)
	}
	done, err := stream.Next()
	if err != nil || !done.Done || len(done.Data) != 0 {
		t.Fatalf("done event = %#v, error = %v", done, err)
	}
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after done error = %v, want EOF", err)
	}
}

func TestCompatibleAdapterRejectsInvalidStreamResponse(t *testing.T) {
	t.Run("upstream status remains classifiable", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"code":"overloaded"}}`)
		}))
		defer upstream.Close()

		adapter, input := streamTestAdapterAndInput(t, upstream.URL)
		stream, err := adapter.OpenStream(context.Background(), input, "provider-test-key")
		if stream != nil {
			stream.Close()
			t.Fatal("OpenStream() returned a stream for an upstream error")
		}
		var httpError *HTTPError
		if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusServiceUnavailable || httpError.Code != "overloaded" {
			t.Fatalf("OpenStream() error = %#v", err)
		}
	})

	t.Run("successful response must be SSE", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{}`)
		}))
		defer upstream.Close()

		adapter, input := streamTestAdapterAndInput(t, upstream.URL)
		stream, err := adapter.OpenStream(context.Background(), input, "provider-test-key")
		if stream != nil {
			stream.Close()
			t.Fatal("OpenStream() returned a non-SSE stream")
		}
		if !errors.Is(err, ErrInvalidStream) {
			t.Fatalf("OpenStream() error = %v", err)
		}
	})
}

func TestChatEventStreamBoundaries(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{name: "invalid JSON", body: "data: not-json\n\n", err: ErrInvalidStream},
		{
			name: "invalid UTF-8",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"\xff\"}}]}\n\n",
			err:  ErrInvalidStream,
		},
		{name: "error envelope", body: `data: {"error":{"message":"nope"}}` + "\n\n", err: ErrInvalidStream},
		{name: "missing delta", body: `data: {"choices":[{}]}` + "\n\n", err: ErrInvalidStream},
		{name: "oversized line", body: "data: " + strings.Repeat("x", maxStreamLineBytes) + "\n\n", err: ErrResponseTooLarge},
		{name: "fake done token", body: "data: [DONE] trailing\n\n", err: ErrInvalidStream},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, err := newChatEventStream(io.NopCloser(strings.NewReader(test.body)))
			if err != nil {
				t.Fatalf("newChatEventStream() error = %v", err)
			}
			defer stream.Close()
			if _, err := stream.Next(); !errors.Is(err, test.err) {
				t.Fatalf("Next() error = %v, want %v", err, test.err)
			}
		})
	}

	stream, err := newChatEventStream(io.NopCloser(strings.NewReader(": one\n\nevent: ping\n\n")))
	if err != nil {
		t.Fatalf("newChatEventStream() error = %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("heartbeat-only Next() error = %v, want EOF", err)
	}

	t.Run("unbounded line is rejected at the configured limit", func(t *testing.T) {
		stream, err := newChatEventStream(endlessStreamBody{})
		if err != nil {
			t.Fatalf("newChatEventStream() error = %v", err)
		}
		defer stream.Close()

		if _, err := stream.Next(); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("Next() error = %v, want %v", err, ErrResponseTooLarge)
		}
	})

	t.Run("multi-line event is bounded", func(t *testing.T) {
		value := strings.Repeat("x", 700<<10)
		body := "data: " + value + "\n" +
			"data: " + value + "\n" +
			"data: " + value + "\n\n"
		stream, err := newChatEventStream(io.NopCloser(strings.NewReader(body)))
		if err != nil {
			t.Fatalf("newChatEventStream() error = %v", err)
		}
		defer stream.Close()

		if _, err := stream.Next(); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("Next() error = %v, want %v", err, ErrResponseTooLarge)
		}
	})

	t.Run("done text inside JSON is not a terminator", func(t *testing.T) {
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"[DONE]\"}}]}\n\n" +
			"data: [DONE]\n\n"
		stream, err := newChatEventStream(io.NopCloser(strings.NewReader(body)))
		if err != nil {
			t.Fatalf("newChatEventStream() error = %v", err)
		}
		defer stream.Close()

		event, err := stream.Next()
		if err != nil || event.Done || !strings.Contains(string(event.Data), `"[DONE]"`) {
			t.Fatalf("content event = %#v, error = %v", event, err)
		}
		done, err := stream.Next()
		if err != nil || !done.Done {
			t.Fatalf("done event = %#v, error = %v", done, err)
		}
	})
}

func TestCompatibleAdapterStreamCancellation(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()

	adapter, input := streamTestAdapterAndInput(t, upstream.URL)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := adapter.OpenStream(ctx, input, "provider-test-key")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	defer stream.Close()

	result := make(chan error, 1)
	go func() {
		_, nextErr := stream.Next()
		result <- nextErr
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Next() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled stream read did not return")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream request context was not canceled")
	}
}

func streamTestAdapterAndInput(t *testing.T, baseURL string) (*compatibleChatAdapter, ChatInput) {
	t.Helper()
	adapter, err := newCompatibleChatAdapter(ProtocolOpenAICompatible, baseURL, DefaultHTTPConfig())
	if err != nil {
		t.Fatalf("newCompatibleChatAdapter() error = %v", err)
	}
	request, err := ParseChatRequest([]byte(`{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatalf("ParseChatRequest() error = %v", err)
	}
	return adapter, ChatInput{RequestID: "stream-test-id", Request: request}
}

type endlessStreamBody struct{}

func (endlessStreamBody) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}

func (endlessStreamBody) Close() error {
	return nil
}
