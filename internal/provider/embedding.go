package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
)

const maximumEmbeddingInputs = 2048

type EmbeddingRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     *int            `json:"dimensions,omitempty"`
	User           string          `json:"user,omitempty"`
	rawBody        []byte
}

type EmbeddingInput struct {
	RequestID     string
	Request       EmbeddingRequest
	ModelOverride string
}

type EmbeddingAdapter interface {
	Adapter
	Embed(context.Context, EmbeddingInput, string) ([]byte, error)
}

func ParseEmbeddingRequest(body []byte) (EmbeddingRequest, error) {
	var fields map[string]json.RawMessage
	var request EmbeddingRequest
	if json.Unmarshal(body, &fields) != nil || fields == nil ||
		json.Unmarshal(body, &request) != nil {
		return EmbeddingRequest{}, ErrInvalidRequest
	}
	for name := range fields {
		switch name {
		case "model", "input", "encoding_format", "dimensions", "user":
		default:
			return EmbeddingRequest{}, fmt.Errorf("%w: embedding field %q", ErrUnsupportedCapability, name)
		}
	}
	request.Model = strings.TrimSpace(request.Model)
	request.EncodingFormat = strings.ToLower(strings.TrimSpace(request.EncodingFormat))
	if request.EncodingFormat == "" {
		request.EncodingFormat = "float"
	}
	if request.Model == "" ||
		(request.EncodingFormat != "float" && request.EncodingFormat != "base64") ||
		(request.Dimensions != nil && *request.Dimensions <= 0) {
		return EmbeddingRequest{}, ErrInvalidRequest
	}
	if _, _, err := request.StringInputs(); err != nil &&
		!errors.Is(err, ErrUnsupportedCapability) {
		return EmbeddingRequest{}, err
	}
	if !validEmbeddingInputShape(request.Input) {
		return EmbeddingRequest{}, ErrInvalidRequest
	}
	request.rawBody = bytes.Clone(body)
	return request, nil
}

func validEmbeddingInputShape(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != ""
	}
	var stringsInput []string
	if json.Unmarshal(raw, &stringsInput) == nil {
		if len(stringsInput) == 0 || len(stringsInput) > maximumEmbeddingInputs {
			return false
		}
		for _, value := range stringsInput {
			if value == "" {
				return false
			}
		}
		return true
	}
	var tokens []int
	if json.Unmarshal(raw, &tokens) == nil {
		return len(tokens) > 0
	}
	var batches [][]int
	if json.Unmarshal(raw, &batches) == nil {
		if len(batches) == 0 || len(batches) > maximumEmbeddingInputs {
			return false
		}
		for _, batch := range batches {
			if len(batch) == 0 {
				return false
			}
		}
		return true
	}
	return false
}

func (request EmbeddingRequest) StringInputs() ([]string, bool, error) {
	var text string
	if json.Unmarshal(request.Input, &text) == nil {
		if text == "" {
			return nil, false, ErrInvalidRequest
		}
		return []string{text}, false, nil
	}
	var values []string
	if json.Unmarshal(request.Input, &values) == nil {
		if len(values) == 0 {
			return nil, true, ErrInvalidRequest
		}
		return values, true, nil
	}
	return nil, false, fmt.Errorf("%w: tokenized embedding input", ErrUnsupportedCapability)
}

func (request EmbeddingRequest) InputCount() int {
	var text string
	if json.Unmarshal(request.Input, &text) == nil {
		return 1
	}
	var stringsInput []string
	if json.Unmarshal(request.Input, &stringsInput) == nil {
		return len(stringsInput)
	}
	var tokens []int
	if json.Unmarshal(request.Input, &tokens) == nil {
		return 1
	}
	var batches [][]int
	if json.Unmarshal(request.Input, &batches) == nil {
		return len(batches)
	}
	return 0
}

func (request EmbeddingRequest) ReliabilityRequest() ChatRequest {
	return ChatRequest{
		Model: request.Model,
		Messages: []ChatMessage{{
			Role: "user", Content: json.RawMessage(`"embedding"`),
		}},
		rawBody: bytes.Clone(request.rawBody),
	}
}

func decodeEmbeddingInput(input EmbeddingInput) (EmbeddingRequest, error) {
	request := input.Request
	if input.ModelOverride != "" {
		request.Model = strings.TrimSpace(input.ModelOverride)
	}
	if request.Model == "" {
		return EmbeddingRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func compatibleEmbeddingBody(input EmbeddingInput) ([]byte, error) {
	request, err := decodeEmbeddingInput(input)
	if err != nil {
		return nil, err
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(request.rawBody, &payload) != nil || payload == nil {
		return nil, ErrInvalidRequest
	}
	payload["model"], _ = json.Marshal(request.Model)
	return json.Marshal(payload)
}

func validateEmbeddingResponse(body []byte) error {
	return validateEmbeddingResponseShape(body, 0, nil)
}

func validateEmbeddingResponseCount(body []byte, expected int) error {
	return validateEmbeddingResponseShape(body, expected, nil)
}

func validateEmbeddingResponseShape(
	body []byte,
	expected int,
	expectedDimensions *int,
) error {
	var response struct {
		Object string `json:"object"`
		Data   []struct {
			Object    string          `json:"object"`
			Embedding json.RawMessage `json:"embedding"`
			Index     int             `json:"index"`
		} `json:"data"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.Object != "list" ||
		len(response.Data) == 0 || strings.TrimSpace(response.Model) == "" {
		return ErrInvalidResponse
	}
	if expected > 0 && len(response.Data) != expected {
		return ErrInvalidResponse
	}
	seen := make(map[int]struct{}, len(response.Data))
	for _, item := range response.Data {
		if item.Object != "embedding" || len(item.Embedding) == 0 || item.Index < 0 {
			return ErrInvalidResponse
		}
		if _, duplicate := seen[item.Index]; duplicate {
			return ErrInvalidResponse
		}
		seen[item.Index] = struct{}{}
		if item.Index >= len(response.Data) {
			return ErrInvalidResponse
		}
		var vector []float64
		if json.Unmarshal(item.Embedding, &vector) == nil {
			if len(vector) == 0 ||
				(expectedDimensions != nil && len(vector) != *expectedDimensions) {
				return ErrInvalidResponse
			}
			continue
		}
		var encoded string
		if json.Unmarshal(item.Embedding, &encoded) != nil || encoded == "" {
			return ErrInvalidResponse
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || len(decoded)%4 != 0 ||
			(expectedDimensions != nil && len(decoded)/4 != *expectedDimensions) {
			return ErrInvalidResponse
		}
	}
	return nil
}

type embeddingBridge struct {
	adapter EmbeddingAdapter
}

func (bridge *embeddingBridge) Authentication() Authentication {
	return bridge.adapter.Authentication()
}

func (bridge *embeddingBridge) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	request, err := ParseEmbeddingRequest(input.Request.rawBody)
	if err != nil {
		return nil, err
	}
	return bridge.adapter.Embed(ctx, EmbeddingInput{
		RequestID: input.RequestID, Request: request,
		ModelOverride: input.ModelOverride,
	}, apiKey)
}

func (registry *AdapterRegistry) EmbeddingRegistry() (*AdapterRegistry, error) {
	if registry == nil {
		return nil, nil
	}
	adapters := make(map[string]Adapter)
	for providerID, adapter := range registry.adapters {
		embedding, ok := adapter.(EmbeddingAdapter)
		if ok {
			adapters[providerID] = &embeddingBridge{adapter: embedding}
		}
	}
	if len(adapters) == 0 {
		return nil, nil
	}
	return NewAdapterRegistryFromAdapters(adapters)
}

func (adapter *compatibleChatAdapter) Embed(
	ctx context.Context,
	input EmbeddingInput,
	apiKey string,
) ([]byte, error) {
	request, err := decodeEmbeddingInput(input)
	if err != nil {
		return nil, err
	}
	body, err := compatibleEmbeddingBody(input)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSuffix(adapter.endpoint, "/chat/completions") + "/embeddings"
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	response, err := adapter.transport.post(ctx, endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	if err := validateEmbeddingResponseShape(
		response, request.InputCount(), request.Dimensions,
	); err != nil {
		return nil, err
	}
	return response, nil
}

func (adapter *azureOpenAIAdapter) Embed(
	ctx context.Context,
	input EmbeddingInput,
	apiKey string,
) ([]byte, error) {
	request, err := decodeEmbeddingInput(input)
	if err != nil {
		return nil, err
	}
	body, err := compatibleEmbeddingBody(input)
	if err != nil {
		return nil, err
	}
	endpoint := strings.Replace(adapter.endpoint, "/chat/completions", "/embeddings", 1)
	headers := make(http.Header)
	headers.Set("api-key", strings.TrimSpace(apiKey))
	response, err := adapter.transport.post(ctx, endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	if err := validateEmbeddingResponseShape(
		response, request.InputCount(), request.Dimensions,
	); err != nil {
		return nil, err
	}
	return response, nil
}

func (adapter *ollamaAdapter) Embed(
	ctx context.Context,
	input EmbeddingInput,
	_ string,
) ([]byte, error) {
	request, err := decodeEmbeddingInput(input)
	if err != nil {
		return nil, err
	}
	values, _, err := request.StringInputs()
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"model": request.Model, "input": values}
	if request.Dimensions != nil {
		payload["dimensions"] = *request.Dimensions
	}
	body, _ := json.Marshal(payload)
	endpoint := strings.TrimSuffix(adapter.endpoint, "/api/chat") + "/api/embed"
	responseBody, err := adapter.transport.post(ctx, endpoint, input.RequestID, body, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Model           string      `json:"model"`
		Embeddings      [][]float64 `json:"embeddings"`
		PromptEvalCount int         `json:"prompt_eval_count"`
	}
	if json.Unmarshal(responseBody, &response) != nil ||
		len(response.Embeddings) != len(values) {
		return nil, ErrInvalidResponse
	}
	data := make([]map[string]any, 0, len(response.Embeddings))
	for index, vector := range response.Embeddings {
		if len(vector) == 0 {
			return nil, ErrInvalidResponse
		}
		var embedding any = vector
		if request.EncodingFormat == "base64" {
			embedding = encodeFloatVector(vector)
		}
		data = append(data, map[string]any{
			"object": "embedding", "embedding": embedding, "index": index,
		})
	}
	return json.Marshal(map[string]any{
		"object": "list", "data": data, "model": response.Model,
		"usage": map[string]any{
			"prompt_tokens": response.PromptEvalCount,
			"total_tokens":  response.PromptEvalCount,
		},
	})
}

func (adapter *geminiAdapter) Embed(
	ctx context.Context,
	input EmbeddingInput,
	apiKey string,
) ([]byte, error) {
	request, err := decodeEmbeddingInput(input)
	if err != nil {
		return nil, err
	}
	values, _, err := request.StringInputs()
	if err != nil {
		return nil, err
	}
	modelName := "models/" + strings.TrimPrefix(request.Model, "models/")
	requests := make([]map[string]any, 0, len(values))
	for _, value := range values {
		item := map[string]any{
			"model": modelName,
			"content": map[string]any{
				"parts": []map[string]string{{"text": value}},
			},
		}
		if request.Dimensions != nil {
			item["outputDimensionality"] = *request.Dimensions
		}
		requests = append(requests, item)
	}
	body, err := json.Marshal(map[string]any{"requests": requests})
	if err != nil {
		return nil, ErrInvalidRequest
	}
	endpoint, err := modelEndpoint(
		adapter.baseURL, "models", request.Model, ":batchEmbedContents",
	)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.post(
		ctx, endpoint, input.RequestID, body, geminiHeaders(apiKey),
	)
	if err != nil {
		return nil, err
	}
	var response struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
		Usage *struct {
			PromptTokens int `json:"promptTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(responseBody, &response) != nil ||
		len(response.Embeddings) != len(values) {
		return nil, ErrInvalidResponse
	}
	data := make([]map[string]any, 0, len(response.Embeddings))
	for index, result := range response.Embeddings {
		if len(result.Values) == 0 {
			return nil, ErrInvalidResponse
		}
		var embedding any = result.Values
		if request.EncodingFormat == "base64" {
			embedding = encodeFloatVector(result.Values)
		}
		data = append(data, map[string]any{
			"object": "embedding", "embedding": embedding, "index": index,
		})
	}
	converted := map[string]any{
		"object": "list", "data": data, "model": request.Model,
	}
	if response.Usage != nil {
		converted["usage"] = map[string]any{
			"prompt_tokens": response.Usage.PromptTokens,
			"total_tokens":  response.Usage.PromptTokens,
		}
	}
	return json.Marshal(converted)
}

func encodeFloatVector(vector []float64) string {
	encoded := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(float32(value)))
	}
	return base64.StdEncoding.EncodeToString(encoded)
}
