package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestRunHTTPServerReportsListenFailure(t *testing.T) {
	occupiedListener := newTestListener(t)
	defer occupiedListener.Close()

	server := &http.Server{
		Addr:    occupiedListener.Addr().String(),
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}

	err := runHTTPServer(context.Background(), server, time.Second)
	if err == nil {
		t.Fatal("runHTTPServer() error = nil, want listen error")
	}
}

func TestServeHTTPServerStopsAfterContextCancellation(t *testing.T) {
	listener := newTestListener(t)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveHTTPServer(ctx, server, listener, time.Second)
	}()

	response, err := testHTTPClient().Get("http://" + listener.Addr().String())
	if err != nil {
		cancel()
		t.Fatalf("GET test server: %v", err)
	}
	_ = response.Body.Close()
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serveHTTPServer() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveHTTPServer() did not stop after cancellation")
	}
}

func TestServeHTTPServerWaitsForActiveRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequest) }) }
	defer release()

	listener := newTestListener(t)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		_, _ = io.WriteString(w, "done")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- serveHTTPServer(ctx, server, listener, time.Second)
	}()
	requestResult := make(chan error, 1)
	go func() {
		response, err := testHTTPClient().Get("http://" + listener.Addr().String())
		if err == nil {
			_, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		requestResult <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("active request did not start")
	}
	cancel()

	select {
	case err := <-serveResult:
		t.Fatalf("serveHTTPServer() returned before active request completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	if err := waitForError(t, requestResult); err != nil {
		t.Fatalf("active request error = %v", err)
	}
	if err := waitForError(t, serveResult); err != nil {
		t.Fatalf("serveHTTPServer() error = %v", err)
	}
}

func TestServeHTTPServerForcesCloseAfterShutdownTimeout(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequest) }) }
	defer release()

	listener := newTestListener(t)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		_, _ = io.WriteString(w, "late response")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- serveHTTPServer(ctx, server, listener, 20*time.Millisecond)
	}()
	requestResult := make(chan error, 1)
	go func() {
		response, err := testHTTPClient().Get("http://" + listener.Addr().String())
		if err == nil {
			_, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		requestResult <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("active request did not start")
	}
	cancel()

	err := waitForError(t, serveResult)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serveHTTPServer() error = %v, want context deadline exceeded", err)
	}

	release()
	select {
	case <-requestResult:
	case <-time.After(time.Second):
		t.Fatal("forced-close request goroutine did not stop")
	}
}

func newTestListener(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on test address: %v", err)
	}
	return listener
}

func testHTTPClient() *http.Client {
	return &http.Client{Timeout: time.Second}
}

func waitForError(t *testing.T, result <-chan error) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
		return nil
	}
}
