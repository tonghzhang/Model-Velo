package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	anthropicAPIVersion = "2023-06-01"
	anthropicFilesBeta  = "files-api-2025-04-14"
)

// anthropicAdapter 使用 Messages API，而不是 OpenAI Chat Completions 格式。
type anthropicAdapter struct {
	endpoint  string
	transport *jsonTransport
}

// anthropicRequest 是 Anthropic Messages API 的非流式请求结构。
// system 指令位于顶层，不属于 messages 数组。
type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	ToolChoice    *anthropicChoice   `json:"tool_choice,omitempty"`
	OutputConfig  *anthropicOutput   `json:"output_config,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string             `json:"type"`
	Text      string             `json:"text,omitempty"`
	Source    *anthropicSource   `json:"source,omitempty"`
	ID        string             `json:"id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Input     json.RawMessage    `json:"input,omitempty"`
	ToolUseID string             `json:"tool_use_id,omitempty"`
	Content   []anthropicContent `json:"content,omitempty"`
}

type anthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	FileID    string `json:"file_id,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	Strict      *bool           `json:"strict,omitempty"`
}

type anthropicChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type anthropicOutput struct {
	Format anthropicOutputFormat `json:"format"`
}

type anthropicOutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

func newAnthropicAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	endpoint, err := fixedEndpoint(baseURL, "v1/messages")
	if err != nil {
		return nil, err
	}
	return &anthropicAdapter{endpoint: endpoint, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *anthropicAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 将统一 Chat 请求转换为 Anthropic 请求，并把结果归一化为 OpenAI 响应。
func (adapter *anthropicAdapter) Complete(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) ([]byte, error) {
	body, request, err := anthropicRequestBody(input, false)
	if err != nil {
		return nil, err
	}
	headers := anthropicHeaders(apiKey, anthropicRequestUsesFileID(request))
	responseBody, err := adapter.transport.post(ctx, adapter.endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeAnthropicResponse(responseBody, input.RequestID)
}

func anthropicRequestBody(input ChatInput, stream bool) ([]byte, ChatRequest, error) {
	request, err := decodeNativeChatRequest(input, nativeRequestSupport{
		tools: true, structured: true, developer: true, toolName: true,
	})
	if err != nil {
		return nil, ChatRequest{}, err
	}
	if err := requireLeadingInstructions(request, ProtocolAnthropic); err != nil {
		return nil, ChatRequest{}, err
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, ChatRequest{}, err
	}
	upstream := anthropicRequest{
		Model:         request.Model,
		MaxTokens:     requestedMaxTokens(request, 1024),
		Temperature:   request.Temperature,
		TopP:          request.TopP,
		StopSequences: stopSequences,
		Stream:        stream,
	}
	if err := applyAnthropicTools(&upstream, request); err != nil {
		return nil, ChatRequest{}, err
	}
	var system []string
	for _, message := range request.Messages {
		if message.Role == "system" || message.Role == "developer" {
			text, err := textContent(message.Content)
			if err != nil {
				return nil, ChatRequest{}, err
			}
			system = append(system, text)
			continue
		}
		content, err := anthropicMessageContents(message)
		if err != nil {
			return nil, ChatRequest{}, err
		}
		role := message.Role
		if role == "tool" {
			role = "user"
		}
		upstream.Messages = append(upstream.Messages, anthropicMessage{Role: role, Content: content})
	}
	if len(upstream.Messages) == 0 {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	upstream.System = strings.Join(system, "\n\n")
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	return body, request, nil
}

func anthropicHeaders(apiKey string, usesFileID bool) http.Header {
	headers := make(http.Header)
	headers.Set("x-api-key", strings.TrimSpace(apiKey))
	headers.Set("anthropic-version", anthropicAPIVersion)
	if usesFileID {
		headers.Set("anthropic-beta", anthropicFilesBeta)
	}
	return headers
}

func (adapter *anthropicAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) (*ChatEventStream, error) {
	body, request, err := anthropicRequestBody(input, true)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.postStream(
		ctx,
		adapter.endpoint,
		input.RequestID,
		body,
		anthropicHeaders(apiKey, anthropicRequestUsesFileID(request)),
	)
	if err != nil {
		return nil, err
	}
	return newMappedSSEStream(responseBody, newAnthropicStreamMapper(
		request.Model, input.RequestID,
	))
}

func anthropicRequestUsesFileID(request ChatRequest) bool {
	for _, message := range request.Messages {
		rawContent := bytes.TrimSpace(message.Content)
		if len(rawContent) == 0 || bytes.Equal(rawContent, []byte("null")) {
			continue
		}
		parts, err := decodeContent(message.Content)
		if err != nil {
			continue
		}
		for _, part := range parts {
			if part.Type == "file" && part.FileID != "" {
				return true
			}
		}
	}
	return false
}

func applyAnthropicTools(upstream *anthropicRequest, request ChatRequest) error {
	for _, tool := range request.Tools {
		schema := tool.Function.Parameters
		if !valuePresent(schema) {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		upstream.Tools = append(upstream.Tools, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: schema,
			Strict:      tool.Function.Strict,
		})
	}
	choice, err := decodeToolChoice(request.ToolChoice)
	if err != nil {
		return err
	}
	disableParallel := len(request.Tools) > 0 &&
		request.ParallelToolCalls != nil &&
		!*request.ParallelToolCalls
	if choice.Mode != "" || disableParallel {
		choiceType := choice.Mode
		if choiceType == "" {
			choiceType = "auto"
		}
		mapped := &anthropicChoice{Type: choiceType}
		switch choice.Mode {
		case "required":
			mapped.Type = "any"
		case "function":
			mapped.Type = "tool"
			mapped.Name = choice.Name
		}
		if disableParallel {
			mapped.DisableParallelToolUse = true
		}
		upstream.ToolChoice = mapped
	}
	format, err := decodeResponseFormat(request.ResponseFormat)
	if err != nil {
		return err
	}
	switch format.Type {
	case "", "text":
	case "json_object":
		upstream.OutputConfig = &anthropicOutput{Format: anthropicOutputFormat{
			Type:   "json_schema",
			Schema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
		}}
	case "json_schema":
		upstream.OutputConfig = &anthropicOutput{Format: anthropicOutputFormat{
			Type: "json_schema", Schema: format.Schema,
		}}
	}
	return nil
}

func anthropicMessageContents(message ChatMessage) ([]anthropicContent, error) {
	if message.Role == "tool" {
		if strings.TrimSpace(message.ToolCallID) == "" {
			return nil, ErrInvalidRequest
		}
		content, err := anthropicContents(message.Content)
		if err != nil {
			return nil, err
		}
		return []anthropicContent{{
			Type: "tool_result", ToolUseID: message.ToolCallID, Content: content,
		}}, nil
	}
	content := make([]anthropicContent, 0)
	raw := bytes.TrimSpace(message.Content)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		converted, err := anthropicContents(message.Content)
		if err != nil {
			return nil, err
		}
		content = append(content, converted...)
	}
	for _, call := range message.ToolCalls {
		input, err := toolArgumentsObject(call.Function.Arguments)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		content = append(content, anthropicContent{
			Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: input,
		})
	}
	if len(content) == 0 {
		return nil, ErrInvalidRequest
	}
	return content, nil
}

// anthropicContents 将 OpenAI 内容块映射为 Anthropic 的 text、url 或 base64 source。
func anthropicContents(raw json.RawMessage) ([]anthropicContent, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return nil, err
	}
	content := make([]anthropicContent, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			content = append(content, anthropicContent{Type: "text", Text: part.Text})
		case "image_url":
			if err := rejectNativeImageDetail(part, ProtocolAnthropic); err != nil {
				return nil, err
			}
			mediaType, data, ok := parseDataURL(part.ImageURL)
			source := &anthropicSource{Type: "url", URL: part.ImageURL}
			if ok {
				switch mediaType {
				case "image/jpeg", "image/png", "image/gif", "image/webp":
				default:
					return nil, fmt.Errorf(
						"%w: Anthropic image type %q",
						ErrUnsupportedCapability,
						mediaType,
					)
				}
				source = &anthropicSource{Type: "base64", MediaType: mediaType, Data: data}
			}
			content = append(content, anthropicContent{Type: "image", Source: source})
		case "file":
			if part.FileID != "" {
				content = append(content, anthropicContent{
					Type: "document",
					Source: &anthropicSource{
						Type: "file", FileID: part.FileID,
					},
				})
				continue
			}
			mediaType, data, ok := parseDataURL(part.FileData)
			if !ok {
				return nil, ErrInvalidRequest
			}
			sourceType := "base64"
			switch mediaType {
			case "application/pdf":
			case "text/plain":
				decoded, err := base64.StdEncoding.DecodeString(data)
				if err != nil || !utf8.Valid(decoded) {
					return nil, ErrInvalidRequest
				}
				sourceType = "text"
				data = string(decoded)
			default:
				return nil, fmt.Errorf(
					"%w: Anthropic document type %q",
					ErrUnsupportedCapability,
					mediaType,
				)
			}
			content = append(content, anthropicContent{
				Type: "document",
				Source: &anthropicSource{
					Type: sourceType, MediaType: mediaType, Data: data,
				},
			})
		default:
			return nil, fmt.Errorf("%w: Anthropic %s content", ErrUnsupportedCapability, part.Type)
		}
	}
	return content, nil
}

func decodeAnthropicResponse(body []byte, requestID string) ([]byte, error) {
	var response struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage *struct {
			InputTokens        *int `json:"input_tokens"`
			OutputTokens       *int `json:"output_tokens"`
			CacheCreationInput *int `json:"cache_creation_input_tokens"`
			CacheReadInput     *int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, ErrInvalidResponse
	}
	var text strings.Builder
	var toolCalls []ChatToolCall
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			if strings.TrimSpace(block.ID) == "" || strings.TrimSpace(block.Name) == "" ||
				!jsonObject(block.Input) {
				return nil, ErrInvalidResponse
			}
			toolCalls = append(toolCalls, ChatToolCall{
				ID: block.ID, Type: "function",
				Function: ChatFunctionCall{Name: block.Name, Arguments: string(block.Input)},
			})
		default:
			return nil, fmt.Errorf("%w: unsupported Anthropic content block %q", ErrInvalidResponse, block.Type)
		}
	}
	if len(response.Content) == 0 {
		return nil, ErrInvalidResponse
	}
	finishReason := "stop"
	switch response.StopReason {
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	}
	var usage *completionUsage
	if response.Usage != nil {
		inputTokens, valid := inclusiveInputTokens(
			response.Usage.InputTokens,
			response.Usage.CacheReadInput,
			response.Usage.CacheCreationInput,
		)
		if !valid {
			return nil, ErrInvalidResponse
		}
		usage = newCompletionUsage(inputTokens, response.Usage.OutputTokens, nil)
		usage.setInputDetails(
			nil,
			nil,
			response.Usage.CacheReadInput,
			response.Usage.CacheCreationInput,
		)
	}
	content := text.String()
	message := completionMessage{Role: "assistant", ToolCalls: toolCalls}
	if content != "" || len(toolCalls) == 0 {
		message.Content = &content
	}
	return encodeCompletionMessage(response.ID, requestID, response.Model, message, finishReason, usage)
}

func newAnthropicStreamMapper(model, requestID string) nativeStreamMapper {
	id := "chatcmpl-" + requestID
	var usage *completionUsage
	toolIndexByBlock := make(map[int]int)
	nextToolIndex := 0
	return func(event string, data []byte) (nativeStreamResult, error) {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nativeStreamResult{Done: true}, nil
		}
		var envelope struct {
			Type    string `json:"type"`
			Message *struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage *struct {
					InputTokens        *int `json:"input_tokens"`
					OutputTokens       *int `json:"output_tokens"`
					CacheCreationInput *int `json:"cache_creation_input_tokens"`
					CacheReadInput     *int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock *struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Text  string          `json:"text"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
			Index int `json:"index"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens *int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := invalidNativeStreamJSON(data, &envelope); err != nil {
			return nativeStreamResult{}, err
		}
		eventType := envelope.Type
		if eventType == "" {
			eventType = event
		}
		switch eventType {
		case "ping":
			return nativeStreamResult{}, nil
		case "message_start":
			if envelope.Message == nil {
				return nativeStreamResult{}, ErrInvalidStream
			}
			if envelope.Message.ID != "" {
				id = envelope.Message.ID
			}
			if envelope.Message.Model != "" {
				model = envelope.Message.Model
			}
			if envelope.Message.Usage != nil {
				inputTokens, valid := inclusiveInputTokens(
					envelope.Message.Usage.InputTokens,
					envelope.Message.Usage.CacheReadInput,
					envelope.Message.Usage.CacheCreationInput,
				)
				if !valid {
					return nativeStreamResult{}, ErrInvalidStream
				}
				usage = newCompletionUsage(inputTokens, envelope.Message.Usage.OutputTokens, nil)
				usage.setInputDetails(
					nil, nil, envelope.Message.Usage.CacheReadInput,
					envelope.Message.Usage.CacheCreationInput,
				)
			}
			return streamResult(
				id, model, openAIStreamDelta{Role: "assistant"}, "", nil, false,
			)
		case "content_block_start":
			if envelope.ContentBlock == nil {
				return nativeStreamResult{}, ErrInvalidStream
			}
			switch envelope.ContentBlock.Type {
			case "text":
				text := envelope.ContentBlock.Text
				return streamResult(
					id, model, openAIStreamDelta{Content: &text}, "", nil, false,
				)
			case "tool_use":
				if envelope.ContentBlock.ID == "" || envelope.ContentBlock.Name == "" {
					return nativeStreamResult{}, ErrInvalidStream
				}
				toolIndexByBlock[envelope.Index] = nextToolIndex
				toolIndex := nextToolIndex
				nextToolIndex++
				return streamResult(id, model, openAIStreamDelta{
					ToolCalls: []openAIStreamToolCallDelta{{
						Index: toolIndex, ID: envelope.ContentBlock.ID, Type: "function",
						Function: openAIStreamFunctionDelta{
							Name: envelope.ContentBlock.Name,
						},
					}},
				}, "", nil, false)
			default:
				return nativeStreamResult{}, fmt.Errorf(
					"%w: Anthropic stream block %q", ErrInvalidStream,
					envelope.ContentBlock.Type,
				)
			}
		case "content_block_delta":
			if envelope.Delta == nil {
				return nativeStreamResult{}, ErrInvalidStream
			}
			switch envelope.Delta.Type {
			case "text_delta":
				text := envelope.Delta.Text
				return streamResult(
					id, model, openAIStreamDelta{Content: &text}, "", nil, false,
				)
			case "input_json_delta":
				toolIndex, exists := toolIndexByBlock[envelope.Index]
				if !exists {
					return nativeStreamResult{}, ErrInvalidStream
				}
				return streamResult(id, model, openAIStreamDelta{
					ToolCalls: []openAIStreamToolCallDelta{{
						Index: toolIndex,
						Function: openAIStreamFunctionDelta{
							Arguments: envelope.Delta.PartialJSON,
						},
					}},
				}, "", nil, false)
			default:
				return nativeStreamResult{}, ErrInvalidStream
			}
		case "content_block_stop":
			return nativeStreamResult{}, nil
		case "message_delta":
			if envelope.Delta == nil {
				return nativeStreamResult{}, ErrInvalidStream
			}
			if envelope.Usage != nil && envelope.Usage.OutputTokens != nil {
				if usage == nil {
					usage = &completionUsage{}
				}
				usage.CompletionTokens = *envelope.Usage.OutputTokens
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
			finish := normalizeAnthropicFinishReason(envelope.Delta.StopReason)
			return streamResult(id, model, openAIStreamDelta{}, finish, usage, false)
		case "message_stop":
			return nativeStreamResult{Done: true}, nil
		case "error":
			return nativeStreamResult{}, ErrInvalidStream
		default:
			return nativeStreamResult{}, fmt.Errorf(
				"%w: Anthropic stream event %q", ErrInvalidStream, eventType,
			)
		}
	}
}

func normalizeAnthropicFinishReason(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}
