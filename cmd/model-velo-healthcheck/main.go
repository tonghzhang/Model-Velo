package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	defaultHealthURL = "http://127.0.0.1:8080/readyz"
	healthTimeout    = 3 * time.Second
)

func main() {
	target := defaultHealthURL
	if len(os.Args) == 2 {
		target = os.Args[1]
	} else if len(os.Args) > 2 {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		os.Exit(1)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		os.Exit(1)
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if readErr != nil || response.StatusCode != http.StatusOK || ctx.Err() != nil {
		os.Exit(1)
	}
}
