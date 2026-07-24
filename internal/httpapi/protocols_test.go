package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"model-velo/internal/apikey"
	"model-velo/internal/observability"
	"model-velo/internal/provider"
)

func TestInboundProtocolsPreserveToolsMultimodalAndUsage(t *testing.T) {
	responsesBody := []byte(`{
		"model":"gateway-model",
		"store":false,
		"input":[{"role":"user","content":[
			{"type":"input_text","text":"describe"},
			{"type":"input_image","image_url":"data:image/png;base64,AA=="}
		]}],
		"tools":[{"type":"function","name":"lookup","description":"lookup","parameters":{"type":"object"}}],
		"tool_choice":{"type":"function","name":"lookup"},
		"text":{"format":{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object"}}}
	}`)
	chatBody, protocol, err := convertResponsesRequest(responsesBody, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := provider.ParseChatRequest(chatBody)
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := chat.RequiredCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []provider.Capability{
		provider.CapabilityImage,
		provider.CapabilityTools,
		provider.CapabilityStructured,
	} {
		if !containsCapability(capabilities, expected) {
			t.Fatalf("capabilities %v do not include %s", capabilities, expected)
		}
	}

	encoded, err := protocol.EncodeResponse([]byte(`{
		"id":"chatcmpl-1","created":1,"model":"upstream-model",
		"choices":[{"message":{"content":"ok","tool_calls":[]},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if json.Unmarshal(encoded, &response) != nil {
		t.Fatalf("response = %s", encoded)
	}
	usageObject := response["usage"].(map[string]any)
	if usageObject["input_tokens"] != float64(3) ||
		usageObject["output_tokens"] != float64(2) ||
		response["store"] != false {
		t.Fatalf("responses conversion = %s", encoded)
	}

	anthropicBody := []byte(`{
		"model":"gateway-model","max_tokens":100,
		"system":"be concise",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"inspect"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}
		]}],
		"tools":[{"name":"lookup","description":"lookup","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"lookup"}
	}`)
	anthropicChat, _, err := convertAnthropicRequest(
		anthropicBody, "request-2",
	)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := provider.ParseChatRequest(anthropicChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(converted.Tools) != 1 ||
		!strings.Contains(string(converted.Messages[1].Content), "image_url") {
		t.Fatalf("anthropic conversion = %s", anthropicChat)
	}
}

func containsCapability(
	values []provider.Capability,
	expected provider.Capability,
) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestRequestSummaryUsesSafeFieldsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(requestIDMiddleware())
	router.Use(requestSummaryMiddleware(logger, nil))
	router.POST("/v1/test", func(c *gin.Context) {
		identity := apikey.Identity{
			TenantID: "tenant-safe",
			APIKeyID: "key-id-safe",
		}
		c.Request = c.Request.WithContext(context.WithValue(
			c.Request.Context(),
			identityContextKey{},
			identity,
		))
		c.Set("model-velo.model", "model-safe")
		c.Status(http.StatusNoContent)
	})

	bodySecret := "prompt-must-not-be-logged"
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/test",
		strings.NewReader(bodySecret),
	)
	request.Header.Set("Authorization", "Bearer gateway-secret")
	request.Header.Set("Cookie", "session=cookie-secret")
	request.Header.Set("X-Request-ID", "safe-request-id")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	logged := output.String()
	for _, expected := range []string{
		"safe-request-id", "tenant-safe", "key-id-safe", "model-safe",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("request summary does not include %q: %s", expected, logged)
		}
	}
	for _, secret := range []string{
		bodySecret, "gateway-secret", "cookie-secret",
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("request summary leaked %q: %s", secret, logged)
		}
	}
}

func TestRequestSummaryClosesMetricsAfterPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metrics := observability.NewMetrics()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(requestIDMiddleware())
	router.Use(requestSummaryMiddleware(logger, metrics))
	router.Use(safeRecoveryMiddleware(logger))
	router.GET("/panic", func(*gin.Context) {
		panic("panic-value-must-not-be-logged")
	})

	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set("x-api-key", "anthropic-secret")
	request.Header.Set("Cookie", "session=cookie-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	for _, secret := range []string{
		"panic-value-must-not-be-logged", "anthropic-secret", "cookie-secret",
	} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("panic log leaked %q: %s", secret, output.String())
		}
	}

	scrape := httptest.NewRecorder()
	metrics.Handler("").ServeHTTP(
		scrape,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)
	if !strings.Contains(scrape.Body.String(), "model_velo_http_in_flight 0") {
		t.Fatalf("in-flight gauge did not return to zero:\n%s", scrape.Body.String())
	}
}
