package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdapterContracts(t *testing.T) {
	const textRequest = `{"model":"public-model","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hi"}],"max_tokens":12}`
	const visionRequest = `{"model":"public-model","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}}]}]}`

	tests := []struct {
		name              string
		protocol          string
		basePath          string
		model             string
		requestBody       string
		responseBody      string
		wantPath          string
		wantHeader        string
		wantHeaderValue   string
		wantBodyFragments []string
		wantResponseParts []string
	}{
		{
			name: "anthropic", protocol: ProtocolAnthropic, model: "claude-test", requestBody: visionRequest,
			responseBody: `{"id":"msg_1","model":"claude-test","stop_reason":"end_turn","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}`,
			wantPath:     "/v1/messages", wantHeader: "x-api-key", wantHeaderValue: "test-key",
			wantBodyFragments: []string{`"model":"claude-test"`, `"type":"image"`, `"type":"base64"`},
			wantResponseParts: []string{`"prompt_tokens":9`, `"total_tokens":10`, `"cached_read_tokens":3`, `"cached_write_tokens":4`},
		},
		{
			name: "gemini", protocol: ProtocolGemini, basePath: "/v1beta", model: "gemini-test", requestBody: visionRequest,
			responseBody: `{"responseId":"gemini_1","candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`,
			wantPath:     "/v1beta/models/gemini-test:generateContent", wantHeader: "x-goog-api-key", wantHeaderValue: "test-key",
			wantBodyFragments: []string{`"inlineData"`, `"mimeType":"image/png"`},
		},
		{
			name: "azure", protocol: ProtocolAzureOpenAI, model: "deployment-test", requestBody: textRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"deployment-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
			wantPath:     "/openai/v1/chat/completions", wantHeader: "api-key", wantHeaderValue: "test-key",
			wantBodyFragments: []string{`"model":"deployment-test"`},
		},
		{
			name: "dashscope", protocol: ProtocolDashScope, basePath: "/api/v1", model: "qwen-test", requestBody: textRequest,
			responseBody: `{"request_id":"dash_1","output":{"choices":[{"finish_reason":"stop","message":{"content":"hello"}}]},"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`,
			wantPath:     "/api/v1/services/aigc/text-generation/generation", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"qwen-test"`, `"result_format":"message"`},
		},
		{
			name: "cohere", protocol: ProtocolCohere, basePath: "/v2", model: "command-test", requestBody: textRequest,
			responseBody: `{"id":"cohere_1","message":{"content":[{"type":"text","text":"hello"}]},"finish_reason":"COMPLETE","usage":{"tokens":{"input_tokens":2,"output_tokens":1}}}`,
			wantPath:     "/v2/chat", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"command-test"`, `"stream":false`},
		},
		{
			name: "ollama", protocol: ProtocolOllama, model: "llava-test", requestBody: visionRequest,
			responseBody:      `{"model":"llava-test","done":true,"done_reason":"stop","message":{"role":"assistant","content":"hello"},"prompt_eval_count":2,"eval_count":1}`,
			wantPath:          "/api/chat",
			wantBodyFragments: []string{`"model":"llava-test"`, `"images":["aGk="]`, `"stream":false`},
		},
		{
			name: "bedrock", protocol: ProtocolBedrock, model: "us.test-model:0", requestBody: visionRequest,
			responseBody: `{"output":{"message":{"role":"assistant","content":[{"text":"hello"}]}},"stopReason":"end_turn","usage":{"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadInputTokens":3,"cacheWriteInputTokens":4}}`,
			wantPath:     "/model/us.test-model:0/converse", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"image":{"format":"png"`, `"bytes":"aGk="`},
			wantResponseParts: []string{`"prompt_tokens":9`, `"total_tokens":10`, `"cached_read_tokens":3`, `"cached_write_tokens":4`},
		},
		{
			name: "cloudflare", protocol: ProtocolCloudflare, basePath: "/client/v4/accounts/account", model: "@cf/meta/test-model", requestBody: textRequest,
			responseBody: `{"success":true,"result":{"response":"hello","usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}}`,
			wantPath:     "/client/v4/accounts/account/ai/run/@cf/meta/test-model", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"messages"`, `"stream":false`},
		},
		{
			name: "openai", protocol: ProtocolOpenAI, basePath: "/v1", model: "gpt-test", requestBody: textRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/v1/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"gpt-test"`},
		},
		{
			name: "mistral", protocol: ProtocolMistral, basePath: "/v1", model: "mistral-test", requestBody: textRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"mistral-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/v1/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"mistral-test"`},
		},
		{
			name: "xai", protocol: ProtocolXAI, basePath: "/v1", model: "grok-test", requestBody: visionRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"grok-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/v1/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"grok-test"`, `"type":"image_url"`},
		},
		{
			name: "deepseek", protocol: ProtocolDeepSeek, model: "deepseek-test", requestBody: textRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"deepseek-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"deepseek-test"`},
		},
		{
			name: "zhipu", protocol: ProtocolZhipu, basePath: "/api/paas/v4", model: "glm-test", requestBody: visionRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"glm-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/api/paas/v4/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"glm-test"`, `"type":"image_url"`},
		},
		{
			name: "groq", protocol: ProtocolGroq, basePath: "/openai/v1", model: "groq-test", requestBody: visionRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"groq-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/openai/v1/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"groq-test"`, `"type":"image_url"`},
		},
		{
			name: "nvidia", protocol: ProtocolNVIDIA, basePath: "/v1", model: "nvidia-test", requestBody: visionRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"nvidia-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/v1/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"nvidia-test"`, `"type":"image_url"`},
		},
		{
			name: "together", protocol: ProtocolTogether, basePath: "/v1", model: "together-test", requestBody: visionRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"together-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/v1/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"together-test"`, `"type":"image_url"`},
		},
		{
			name: "custom compatible", protocol: ProtocolOpenAICompatible, model: "custom-test", requestBody: textRequest,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"custom-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantPath:     "/v1/chat/completions", wantHeader: "Authorization", wantHeaderValue: "Bearer test-key",
			wantBodyFragments: []string{`"model":"custom-test"`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != test.wantPath {
					t.Errorf("path = %q; want %q", request.URL.Path, test.wantPath)
				}
				if got := request.Header.Get(test.wantHeader); got != test.wantHeaderValue {
					t.Errorf("header %q = %q; want %q", test.wantHeader, got, test.wantHeaderValue)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
				}
				for _, fragment := range test.wantBodyFragments {
					if !strings.Contains(string(body), fragment) {
						t.Errorf("request body %s does not contain %s", body, fragment)
					}
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.responseBody))
			}))
			defer upstream.Close()

			adapter, err := NewAdapter(AdapterConfig{
				Protocol: test.protocol,
				BaseURL:  upstream.URL + test.basePath,
			})
			if err != nil {
				t.Fatalf("NewAdapter() error = %v", err)
			}
			response, err := adapter.Complete(context.Background(), ChatInput{
				RequestID:     "request-test",
				Request:       mustParseChatRequest(t, test.requestBody),
				ModelOverride: test.model,
			}, "test-key")
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if !strings.Contains(string(response), `"content":"hello"`) {
				t.Fatalf("Complete() response = %s; want normalized chat completion", response)
			}
			for _, fragment := range test.wantResponseParts {
				if !strings.Contains(string(response), fragment) {
					t.Errorf("response body %s does not contain %s", response, fragment)
				}
			}
		})
	}

	t.Run("remote image rejected before Bedrock call", func(t *testing.T) {
		adapter, err := newBedrockAdapter("https://example.com", DefaultHTTPConfig())
		if err != nil {
			t.Fatalf("newBedrockAdapter() error = %v", err)
		}
		_, err = adapter.Complete(context.Background(), ChatInput{Request: mustParseChatRequest(t,
			`{"model":"demo","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`,
		)}, "test-key")
		if !errors.Is(err, ErrUnsupportedCapability) {
			t.Fatalf("Complete() error = %v; want ErrUnsupportedCapability", err)
		}
	})

	t.Run("compatible fields are preserved and native fields are rejected", func(t *testing.T) {
		const body = `{"model":"public-model","messages":[{"role":"user","content":"hi","name":"caller"}],"tools":[]}`
		request := mustParseChatRequest(t, body)
		mapped, err := compatibleRequestBody(ChatInput{Request: request, ModelOverride: "provider-model"})
		if err != nil {
			t.Fatalf("compatibleRequestBody() error = %v", err)
		}
		for _, fragment := range []string{`"model":"provider-model"`, `"name":"caller"`, `"tools":[]`} {
			if !strings.Contains(string(mapped), fragment) {
				t.Errorf("compatible body %s does not contain %s", mapped, fragment)
			}
		}

		adapter, err := newAnthropicAdapter("https://example.com", DefaultHTTPConfig())
		if err != nil {
			t.Fatalf("newAnthropicAdapter() error = %v", err)
		}
		_, err = adapter.Complete(context.Background(), ChatInput{Request: request}, "test-key")
		if !errors.Is(err, ErrUnsupportedCapability) {
			t.Fatalf("native Complete() error = %v; want ErrUnsupportedCapability", err)
		}
	})
}

func mustParseChatRequest(t *testing.T, body string) ChatRequest {
	t.Helper()
	request, err := ParseChatRequest([]byte(body))
	if err != nil {
		t.Fatalf("ParseChatRequest() error = %v", err)
	}
	return request
}
