package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDMiddlewareGeneratesID(t *testing.T) {
	router := requestIDTestRouter()
	request := httptest.NewRequest(http.MethodGet, "/request-id", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	requestID := response.Header().Get(requestIDHeader)
	assertGeneratedRequestID(t, requestID)

	var body struct {
		Gin     string `json:"gin"`
		Context string `json:"context"`
	}
	if err := decodeJSONResponse(response, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Gin != requestID {
		t.Errorf("Gin request ID = %q, want %q", body.Gin, requestID)
	}
	if body.Context != requestID {
		t.Errorf("Context request ID = %q, want %q", body.Context, requestID)
	}
}

func TestRequestIDMiddlewareAcceptsAndNormalizesValidID(t *testing.T) {
	router := requestIDTestRouter()
	request := httptest.NewRequest(http.MethodGet, "/request-id", nil)
	request.Header.Set(requestIDHeader, "  client-request_123.abc  ")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if got := response.Header().Get(requestIDHeader); got != "client-request_123.abc" {
		t.Fatalf("X-Request-ID = %q, want normalized client ID", got)
	}
}

func TestRequestIDMiddlewareReplacesInvalidID(t *testing.T) {
	invalidIDs := []string{
		"contains spaces",
		"contains/slash",
		"非ASCII",
		strings.Repeat("a", maxRequestIDLength+1),
	}

	router := requestIDTestRouter()
	for _, invalidID := range invalidIDs {
		t.Run(invalidID, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/request-id", nil)
			request.Header.Set(requestIDHeader, invalidID)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			got := response.Header().Get(requestIDHeader)
			if got == invalidID {
				t.Fatalf("invalid request ID was accepted: %q", got)
			}
			assertGeneratedRequestID(t, got)
		})
	}
}

func TestRequestIDMiddlewareGeneratesUniqueIDsConcurrently(t *testing.T) {
	const requestCount = 128

	router := requestIDTestRouter()
	requestIDs := make(chan string, requestCount)
	var waitGroup sync.WaitGroup

	for range requestCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()

			request := httptest.NewRequest(http.MethodGet, "/request-id", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			requestIDs <- response.Header().Get(requestIDHeader)
		}()
	}

	waitGroup.Wait()
	close(requestIDs)

	seen := make(map[string]struct{}, requestCount)
	for requestID := range requestIDs {
		assertGeneratedRequestID(t, requestID)
		if _, exists := seen[requestID]; exists {
			t.Fatalf("duplicate generated request ID: %q", requestID)
		}
		seen[requestID] = struct{}{}
	}
	if len(seen) != requestCount {
		t.Fatalf("unique request IDs = %d, want %d", len(seen), requestCount)
	}
}

func requestIDTestRouter() *gin.Engine {
	router := gin.New()
	router.Use(requestIDMiddleware())
	router.GET("/request-id", func(c *gin.Context) {
		ginRequestID, _ := c.Get(requestIDGinKey)
		c.JSON(http.StatusOK, gin.H{
			"gin":     ginRequestID,
			"context": requestIDFromContext(c.Request.Context()),
		})
	})
	return router
}

func assertGeneratedRequestID(t *testing.T, requestID string) {
	t.Helper()

	if len(requestID) != 32 {
		t.Fatalf("generated request ID length = %d, want 32; value = %q", len(requestID), requestID)
	}
	decoded, err := hex.DecodeString(requestID)
	if err != nil {
		t.Fatalf("generated request ID is not hexadecimal: %q", requestID)
	}
	if len(decoded) != 16 {
		t.Fatalf("decoded request ID length = %d, want 16", len(decoded))
	}
}

func decodeJSONResponse(response *httptest.ResponseRecorder, target any) error {
	return json.NewDecoder(response.Body).Decode(target)
}
