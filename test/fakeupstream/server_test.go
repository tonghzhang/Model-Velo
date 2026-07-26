package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestUpstreamServerChatCompletions(t *testing.T) {
	upstream, err := newUpstreamServer("test-provider", "")
	if err != nil {
		t.Fatalf("new upstream server: %v", err)
	}
	testServer := httptest.NewServer(upstream.handler())
	defer testServer.Close()

	t.Run("non-stream success", func(t *testing.T) {
		response := postChat(
			t,
			testServer.URL,
			"request-non-stream",
			"mock/instant",
			false,
		)
		defer response.Body.Close()

		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.StatusCode)
		}
		var body chatResponse
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Model != "mock/instant" ||
			len(body.Choices) != 1 ||
			body.Choices[0].Message.Role != "assistant" {
			t.Fatalf("unexpected response: %+v", body)
		}
		if got := response.Header.Get("X-Mock-Provider"); got != "test-provider" {
			t.Fatalf("X-Mock-Provider = %q, want test-provider", got)
		}
	})

	t.Run("stream success", func(t *testing.T) {
		response := postChat(
			t,
			testServer.URL,
			"request-stream",
			"mock/instant",
			true,
		)
		defer response.Body.Close()

		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.StatusCode)
		}
		contentType := response.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "text/event-stream") {
			t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
		}

		scanner := bufio.NewScanner(response.Body)
		dataLines := []string{}
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("read stream: %v", err)
		}
		if len(dataLines) != 5 {
			t.Fatalf("SSE data lines = %d, want 5", len(dataLines))
		}
		if dataLines[len(dataLines)-1] != "[DONE]" {
			t.Fatalf("last SSE event = %q, want [DONE]", dataLines[len(dataLines)-1])
		}
	})
}

func TestUpstreamServerRetrySequenceIsolatedByRequestID(t *testing.T) {
	upstream, err := newUpstreamServer("retry-provider", "")
	if err != nil {
		t.Fatalf("new upstream server: %v", err)
	}
	testServer := httptest.NewServer(upstream.handler())
	defer testServer.Close()

	requestIDs := []string{"request-a", "request-b"}
	var waitGroup sync.WaitGroup
	errorsByRequest := make(chan string, len(requestIDs))
	for _, requestID := range requestIDs {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			expectedStatuses := []int{
				http.StatusInternalServerError,
				http.StatusInternalServerError,
				http.StatusOK,
			}
			for index, expectedStatus := range expectedStatuses {
				response, err := sendChat(
					testServer.URL,
					requestID,
					"mock/retry-2",
					false,
				)
				if err != nil {
					errorsByRequest <- requestID + ": " + err.Error()
					return
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode != expectedStatus {
					errorsByRequest <- requestID + ": unexpected status"
					return
				}
				if got := response.Header.Get("X-Mock-Attempt"); got != strconv.Itoa(index+1) {
					errorsByRequest <- requestID + ": unexpected attempt"
					return
				}
			}
		}()
	}
	waitGroup.Wait()
	close(errorsByRequest)

	for message := range errorsByRequest {
		t.Error(message)
	}
}

func TestUpstreamServerLoadScenariosAndStats(t *testing.T) {
	upstream, err := newUpstreamServer("load-provider", "")
	if err != nil {
		t.Fatalf("new upstream server: %v", err)
	}
	testServer := httptest.NewServer(upstream.handler())
	defer testServer.Close()

	payloadResponse := postChat(
		t,
		testServer.URL,
		"payload-request",
		"mock/payload-10k",
		false,
	)
	defer payloadResponse.Body.Close()

	var payload chatResponse
	if err := json.NewDecoder(payloadResponse.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload response: %v", err)
	}
	if got := len(payload.Choices[0].Message.Content); got != 10<<10 {
		t.Fatalf("payload bytes = %d, want %d", got, 10<<10)
	}

	failingID := requestIDWithFailureOutcome(true)
	errorResponse := postChat(
		t,
		testServer.URL,
		failingID,
		"mock/error-rate-10",
		false,
	)
	_, _ = io.Copy(io.Discard, errorResponse.Body)
	_ = errorResponse.Body.Close()
	if errorResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failure status = %d, want 503", errorResponse.StatusCode)
	}

	successID := requestIDWithFailureOutcome(false)
	successResponse := postChat(
		t,
		testServer.URL,
		successID,
		"mock/error-rate-10",
		false,
	)
	_, _ = io.Copy(io.Discard, successResponse.Body)
	_ = successResponse.Body.Close()
	if successResponse.StatusCode != http.StatusOK {
		t.Fatalf("success status = %d, want 200", successResponse.StatusCode)
	}

	response, err := http.Get(testServer.URL + "/__admin/stats")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer response.Body.Close()
	var stats upstreamStatsResponse
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Requests != 3 || stats.Completed != 3 || stats.Errors != 1 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if stats.MaxActive < 1 {
		t.Fatalf("max active = %d, want at least 1", stats.MaxActive)
	}
}

func requestIDWithFailureOutcome(fail bool) string {
	for index := range 10_000 {
		requestID := "failure-outcome-" + strconv.Itoa(index)
		if (requestHash(requestID)%100 < 10) == fail {
			return requestID
		}
	}
	panic("unable to find deterministic failure outcome")
}

func postChat(
	t *testing.T,
	baseURL string,
	requestID string,
	model string,
	stream bool,
) *http.Response {
	t.Helper()
	response, err := sendChat(baseURL, requestID, model, stream)
	if err != nil {
		t.Fatalf("post chat: %v", err)
	}
	return response
}

func sendChat(
	baseURL string,
	requestID string,
	model string,
	stream bool,
) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": stream,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+"/v1/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	return http.DefaultClient.Do(request)
}
