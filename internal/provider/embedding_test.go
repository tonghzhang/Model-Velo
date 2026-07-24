package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiEmbeddingConvertsBatchAndUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("x-goog-api-key") != "gemini-secret" {
			t.Errorf("x-goog-api-key was not propagated")
		}
		body, _ := io.ReadAll(request.Body)
		var payload struct {
			Requests []struct {
				OutputDimensions int `json:"outputDimensionality"`
			} `json:"requests"`
		}
		if json.Unmarshal(body, &payload) != nil ||
			len(payload.Requests) != 2 ||
			payload.Requests[0].OutputDimensions != 2 ||
			payload.Requests[1].OutputDimensions != 2 {
			t.Errorf("request body = %s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}],
			"usageMetadata":{"promptTokenCount":7}
		}`)
	}))
	defer upstream.Close()

	adapterValue, err := newGeminiAdapter(upstream.URL+"/v1beta", DefaultHTTPConfig())
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*geminiAdapter)
	request, err := ParseEmbeddingRequest([]byte(
		`{"model":"gemini-embedding-001","input":["first","second"],"dimensions":2}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	response, err := adapter.Embed(context.Background(), EmbeddingInput{
		RequestID: "embedding-test", Request: request,
	}, "gemini-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEmbeddingResponse(response); err != nil {
		t.Fatalf("converted response is invalid: %v, body=%s", err, response)
	}
	var converted struct {
		Data  []json.RawMessage `json:"data"`
		Usage struct {
			Prompt int `json:"prompt_tokens"`
			Total  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(response, &converted) != nil ||
		len(converted.Data) != 2 ||
		converted.Usage.Prompt != 7 ||
		converted.Usage.Total != 7 {
		t.Fatalf("converted response = %s", response)
	}
}
