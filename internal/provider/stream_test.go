package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
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

func TestNativeAdaptersOpenStream(t *testing.T) {
	tests := []struct {
		name          string
		protocol      string
		basePath      string
		model         string
		contentType   string
		wantPath      string
		writeResponse func(*testing.T, http.ResponseWriter)
		want          []string
	}{
		{
			name: "azure", protocol: ProtocolAzureOpenAI, model: "deployment-test",
			contentType: "text/event-stream",
			wantPath:    "/openai/v1/chat/completions",
			writeResponse: func(_ *testing.T, writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, "data: {\"id\":\"azure_stream\",\"object\":\"chat.completion.chunk\",\"model\":\"deployment-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			},
			want: []string{`"content":"hello"`, `"finish_reason":"stop"`},
		},
		{
			name: "cloudflare", protocol: ProtocolCloudflare,
			basePath: "/client/v4/accounts/account", model: "@cf/meta/test",
			contentType: "text/event-stream",
			wantPath:    "/client/v4/accounts/account/ai/v1/chat/completions",
			writeResponse: func(_ *testing.T, writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, "data: {\"id\":\"cf_stream\",\"object\":\"chat.completion.chunk\",\"model\":\"@cf/meta/test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			},
			want: []string{`"content":"hello"`, `"finish_reason":"stop"`},
		},
		{
			name: "anthropic tool delta", protocol: ProtocolAnthropic, model: "claude-test",
			contentType: "text/event-stream",
			writeResponse: func(_ *testing.T, writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, `event: message_start
data: {"type":"message_start","message":{"id":"msg_stream","model":"claude-test","usage":{"input_tokens":4}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"checking"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`)
			},
			want: []string{`"content":"checking"`, `"tool_calls":[{"index":0`, `"arguments":"{\"city\":\"Paris\"}"`, `"finish_reason":"tool_calls"`, `"total_tokens":7`},
		},
		{
			name: "gemini", protocol: ProtocolGemini, basePath: "/v1beta", model: "gemini-test",
			contentType: "text/event-stream",
			writeResponse: func(_ *testing.T, writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, `data: {"responseId":"gemini_stream","candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}

`)
			},
			want: []string{`"content":"hello"`, `"finish_reason":"stop"`, `"total_tokens":6`},
		},
		{
			name: "dashscope", protocol: ProtocolDashScope, basePath: "/api/v1", model: "qwen-test",
			contentType: "text/event-stream",
			writeResponse: func(_ *testing.T, writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, `data: {"request_id":"dash_stream","output":{"choices":[{"finish_reason":"stop","message":{"content":"hello"}}]},"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}

`)
			},
			want: []string{`"content":"hello"`, `"finish_reason":"stop"`, `"total_tokens":6`},
		},
		{
			name: "cohere", protocol: ProtocolCohere, basePath: "/v2", model: "command-test",
			contentType: "text/event-stream",
			writeResponse: func(_ *testing.T, writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, `event: message-start
data: {"type":"message-start","id":"cohere_stream","delta":{"message":{"role":"assistant"}}}

event: tool-plan-delta
data: {"type":"tool-plan-delta","delta":{"message":{"tool_plan":"plan"}}}

event: content-delta
data: {"type":"content-delta","delta":{"message":{"content":{"text":"hello"}}}}

event: message-end
data: {"type":"message-end","delta":{"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":4,"output_tokens":2}}}}

`)
			},
			want: []string{`"reasoning_content":"plan"`, `"content":"hello"`, `"finish_reason":"stop"`, `"total_tokens":6`},
		},
		{
			name: "ollama", protocol: ProtocolOllama, model: "qwen-test",
			contentType: "application/x-ndjson",
			writeResponse: func(_ *testing.T, writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, "{\"model\":\"qwen-test\",\"message\":{\"content\":\"hello\"},\"done\":false}\n")
				_, _ = io.WriteString(writer, "{\"model\":\"qwen-test\",\"message\":{},\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":4,\"eval_count\":2}\n")
			},
			want: []string{`"content":"hello"`, `"finish_reason":"stop"`, `"total_tokens":6`},
		},
		{
			name: "bedrock", protocol: ProtocolBedrock, model: "us.test:0",
			contentType: "application/vnd.amazon.eventstream",
			writeResponse: func(t *testing.T, writer http.ResponseWriter) {
				writeBedrockTestEvent(t, writer, "messageStart", `{"role":"assistant"}`)
				writeBedrockTestEvent(t, writer, "contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hello"}}`)
				writeBedrockTestEvent(t, writer, "messageStop", `{"stopReason":"end_turn"}`)
				writeBedrockTestEvent(t, writer, "metadata", `{"usage":{"inputTokens":4,"outputTokens":2,"totalTokens":6}}`)
			},
			want: []string{`"content":"hello"`, `"finish_reason":"stop"`, `"total_tokens":6`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if test.wantPath != "" && request.URL.Path != test.wantPath {
					t.Errorf("path = %q; want %q", request.URL.Path, test.wantPath)
				}
				writer.Header().Set("Content-Type", test.contentType)
				test.writeResponse(t, writer)
			}))
			defer upstream.Close()

			adapter, err := NewAdapter(AdapterConfig{
				Protocol: test.protocol,
				BaseURL:  upstream.URL + test.basePath,
			})
			if err != nil {
				t.Fatalf("NewAdapter() error = %v", err)
			}
			streaming, ok := adapter.(StreamingAdapter)
			if !ok {
				t.Fatalf("%s adapter does not implement StreamingAdapter", test.protocol)
			}
			request := mustParseChatRequest(t, `{
				"model":"public-model",
				"messages":[{"role":"user","content":"hello"}],
				"stream":true
			}`)
			stream, err := streaming.OpenStream(context.Background(), ChatInput{
				RequestID:     "native-stream",
				Request:       request,
				ModelOverride: test.model,
			}, "test-key")
			if err != nil {
				t.Fatalf("OpenStream() error = %v", err)
			}
			defer stream.Close()

			var output strings.Builder
			done := false
			for eventCount := 0; eventCount < 16; eventCount++ {
				event, err := stream.Next()
				if err != nil {
					t.Fatalf("Next() error = %v", err)
				}
				if event.Done {
					done = true
					break
				}
				output.Write(event.Data)
				output.WriteByte('\n')
			}
			if !done {
				t.Fatal("stream did not produce [DONE]")
			}
			for _, fragment := range test.want {
				if !strings.Contains(output.String(), fragment) {
					t.Errorf("stream output %s does not contain %s", output.String(), fragment)
				}
			}
		})
	}
}

func writeBedrockTestEvent(
	t *testing.T,
	writer io.Writer,
	eventType string,
	payload string,
) {
	t.Helper()
	encoder := eventstream.NewEncoder()
	err := encoder.Encode(writer, eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":message-type", Value: eventstream.StringValue("event")},
			{Name: ":event-type", Value: eventstream.StringValue(eventType)},
			{Name: ":content-type", Value: eventstream.StringValue("application/json")},
		},
		Payload: []byte(payload),
	})
	if err != nil {
		t.Fatalf("encode Bedrock event: %v", err)
	}
}

func TestMappedAWSStreamRejectsOversizedFrame(t *testing.T) {
	var encoded bytes.Buffer
	encoder := eventstream.NewEncoder()
	err := encoder.Encode(&encoded, eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":message-type", Value: eventstream.StringValue("event")},
			{Name: ":event-type", Value: eventstream.StringValue("metadata")},
		},
		Payload: bytes.Repeat([]byte{'x'}, maxStreamEventBytes),
	})
	if err != nil {
		t.Fatalf("encode oversized AWS event: %v", err)
	}
	stream, err := newMappedAWSStream(
		io.NopCloser(bytes.NewReader(encoded.Bytes())),
		func(_ string, _ []byte) (nativeStreamResult, error) {
			return nativeStreamResult{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newMappedAWSStream() error = %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Next() error = %v; want ErrResponseTooLarge", err)
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
		{
			name: "malformed tool delta",
			body: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":{}}}]}}]}` + "\n\n",
			err:  ErrInvalidStream,
		},
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
