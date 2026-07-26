package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendStreamRequestMeasuresContentAndCompletion(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			flusher := writer.(http.Flusher)
			fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
			fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
			fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n")
			fmt.Fprint(writer, "data: [DONE]\n\n")
			flusher.Flush()
		}),
	)
	defer server.Close()

	config := runConfig{
		targetURL:     server.URL,
		model:         "mock/typical",
		requestPrefix: "test-run",
		requests:      1,
		concurrency:   1,
		promptBytes:   200,
		timeout:       time.Second,
	}
	sample := sendStreamRequest(
		t.Context(),
		newHTTPClient(1),
		config,
		1,
	)
	if sample.err != "" {
		t.Fatalf("stream error = %q", sample.err)
	}
	if !sample.complete {
		t.Fatal("stream did not complete")
	}
	if sample.events != 3 || sample.contentChunks != 2 {
		t.Fatalf(
			"events = %d, content chunks = %d, want 3 and 2",
			sample.events,
			sample.contentChunks,
		)
	}
	if sample.firstEvent <= 0 || sample.firstContent <= sample.firstEvent {
		t.Fatalf(
			"first event = %s, first content = %s",
			sample.firstEvent,
			sample.firstContent,
		)
	}
	if len(sample.gaps) != 2 {
		t.Fatalf("inter-event gaps = %d, want 2", len(sample.gaps))
	}
}
