package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompatibleChatCompletionsEndpoint(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "host", base: "https://example.com", want: "https://example.com/v1/chat/completions"},
		{name: "trailing slash", base: "https://example.com/", want: "https://example.com/v1/chat/completions"},
		{name: "version path", base: "https://example.com/v1", want: "https://example.com/v1/chat/completions"},
		{name: "full endpoint", base: "https://example.com/v1/chat/completions/", want: "https://example.com/v1/chat/completions"},
		{name: "provider prefix", base: "https://example.com/openai", want: "https://example.com/openai/chat/completions"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := compatibleChatCompletionsEndpoint(test.base)
			if err != nil {
				t.Fatalf("compatibleChatCompletionsEndpoint() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("compatibleChatCompletionsEndpoint() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOpenAIAdapterValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "missing URL", baseURL: ""},
		{name: "unsupported scheme", baseURL: "ftp://example.com"},
		{name: "missing host", baseURL: "http:///v1"},
		{name: "embedded credentials", baseURL: "https://user:pass@example.com"},
		{name: "query", baseURL: "https://example.com?secret=value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := newOpenAIAdapter(test.baseURL, DefaultHTTPConfig())
			if err == nil {
				t.Fatalf("newOpenAIAdapter() = %#v, nil; want error", adapter)
			}
		})
	}
}

func TestOpenAIAdapterComplete(t *testing.T) {
	wantRequestBody := `{"model":"demo-model","messages":[{"role":"user","content":"hello"}],"temperature":0.2}`
	wantResponseBody := `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`

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
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
		if got := r.Header.Get("X-Request-ID"); got != "request-test-id" {
			t.Errorf("X-Request-ID = %q, want %q", got, "request-test-id")
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if string(body) != wantRequestBody {
			t.Errorf("request body = %s, want %s", body, wantRequestBody)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, wantResponseBody)
	}))
	defer upstream.Close()

	adapter, err := newOpenAIAdapter(upstream.URL+"/v1", DefaultHTTPConfig())
	if err != nil {
		t.Fatalf("newOpenAIAdapter() error = %v", err)
	}

	responseBody, err := adapter.Complete(context.Background(), ChatInput{
		RequestID: "request-test-id",
		Request:   mustParseChatRequest(t, wantRequestBody),
	}, "provider-test-key")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if string(responseBody) != wantResponseBody {
		t.Fatalf("response body = %s, want %s", responseBody, wantResponseBody)
	}
}

func TestOpenAIAdapterPropagatesCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	upstreamCancelled := make(chan struct{})

	adapter, err := newOpenAIAdapter("https://example.com/v1", DefaultHTTPConfig())
	if err != nil {
		t.Fatalf("newOpenAIAdapter() error = %v", err)
	}
	adapter.transport.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-request.Context().Done()
		close(upstreamCancelled)
		return nil, request.Context().Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	request := mustParseChatRequest(t, `{"model":"demo-model"}`)
	result := make(chan error, 1)
	go func() {
		_, callErr := adapter.Complete(ctx, ChatInput{
			RequestID: "request-test-id",
			Request:   request,
		}, "provider-test-key")
		result <- callErr
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()

	select {
	case callErr := <-result:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatalf("Complete() error = %v, want context.Canceled", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Complete() did not return after cancellation")
	}

	select {
	case <-upstreamCancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream context was not cancelled")
	}
}

func TestOpenAIAdapterRejectsInvalidResponse(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "wrong content type", contentType: "text/plain", body: `{"choices":[]}`},
		{name: "invalid JSON", contentType: "application/json", body: `not-json`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()

			adapter, err := newOpenAIAdapter(upstream.URL+"/v1", DefaultHTTPConfig())
			if err != nil {
				t.Fatalf("newOpenAIAdapter() error = %v", err)
			}

			_, err = adapter.Complete(context.Background(), ChatInput{
				RequestID: "request-test-id",
				Request:   mustParseChatRequest(t, `{"model":"demo-model"}`),
			}, "provider-test-key")
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("Complete() error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestOpenAIAdapterLimitsResponseBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":"response larger than test limit"}`)
	}))
	defer upstream.Close()

	adapter, err := newOpenAIAdapter(upstream.URL+"/v1", DefaultHTTPConfig())
	if err != nil {
		t.Fatalf("newOpenAIAdapter() error = %v", err)
	}
	adapter.transport.maxResponseBytes = 16

	_, err = adapter.Complete(context.Background(), ChatInput{
		RequestID: "request-test-id",
		Request:   mustParseChatRequest(t, `{"model":"demo-model"}`),
	}, "provider-test-key")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Complete() error = %v, want ErrResponseTooLarge", err)
	}
}

func TestOpenAIAdapterClosesBodyAfterReadError(t *testing.T) {
	body := &failingReadCloser{}
	adapter, err := newOpenAIAdapter("https://example.com/v1", DefaultHTTPConfig())
	if err != nil {
		t.Fatalf("newOpenAIAdapter() error = %v", err)
	}
	adapter.transport.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
			Request:    request,
		}, nil
	})

	_, err = adapter.Complete(context.Background(), ChatInput{
		RequestID: "request-test-id",
		Request:   mustParseChatRequest(t, `{"model":"demo-model"}`),
	}, "provider-test-key")
	if err == nil || !strings.Contains(err.Error(), "read upstream response") {
		t.Fatalf("Chat() error = %v, want response read error", err)
	}
	if !body.closed {
		t.Fatal("upstream response body was not closed")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingReadCloser struct {
	closed bool
}

func (f *failingReadCloser) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("forced read failure")
}

func (f *failingReadCloser) Close() error {
	f.closed = true
	return nil
}
