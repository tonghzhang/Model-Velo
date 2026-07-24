package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"model-velo/internal/provider"
)

type anthropicProtocol struct {
	requestID string
	model     string
	stream    anthropicStreamState
}

type anthropicRequest struct {
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	System        json.RawMessage   `json:"system,omitempty"`
	MaxTokens     int               `json:"max_tokens"`
	StopSequences []string          `json:"stop_sequences,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	Tools         []json.RawMessage `json:"tools,omitempty"`
	ToolChoice    json.RawMessage   `json:"tool_choice,omitempty"`
}

func (h chatHandler) anthropicMessages(c *gin.Context) {
	versions := c.Request.Header.Values("anthropic-version")
	if len(versions) != 1 || strings.TrimSpace(versions[0]) != anthropicVersion {
		writeAPIError(
			c,
			400,
			"anthropic-version must be 2023-06-01",
			"invalid_request_error",
			nil,
			"invalid_anthropic_version",
		)
		return
	}
	h.serveProtocol(c, convertAnthropicRequest)
}

func convertAnthropicRequest(
	body []byte,
	requestID string,
) ([]byte, responseProtocol, error) {
	var fields map[string]json.RawMessage
	var request anthropicRequest
	if json.Unmarshal(body, &fields) != nil || fields == nil ||
		json.Unmarshal(body, &request) != nil {
		return nil, nil, provider.ErrInvalidRequest
	}
	if field := unknownJSONField(
		fields,
		"model", "messages", "system", "max_tokens", "stop_sequences",
		"stream", "temperature", "top_p", "tools", "tool_choice",
	); field != "" {
		return nil, nil, fmt.Errorf("%w: Anthropic field %s", provider.ErrUnsupportedCapability, field)
	}
	if strings.TrimSpace(request.Model) == "" || request.MaxTokens <= 0 ||
		len(request.Messages) == 0 {
		return nil, nil, provider.ErrInvalidRequest
	}
	messages := make([]map[string]any, 0, len(request.Messages)+1)
	if system, ok, err := anthropicSystem(request.System); err != nil {
		return nil, nil, err
	} else if ok {
		messages = append(messages, map[string]any{
			"role": "system", "content": system,
		})
	}
	for _, raw := range request.Messages {
		converted, err := anthropicMessage(raw)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, converted...)
	}
	tools, err := anthropicTools(request.Tools)
	if err != nil {
		return nil, nil, err
	}
	choice, err := anthropicToolChoice(request.ToolChoice)
	if err != nil {
		return nil, nil, err
	}
	chat := map[string]any{
		"model":                 strings.TrimSpace(request.Model),
		"messages":              messages,
		"stream":                request.Stream,
		"max_completion_tokens": request.MaxTokens,
	}
	putOptional(chat, "temperature", request.Temperature)
	putOptional(chat, "top_p", request.TopP)
	if len(request.StopSequences) > 0 {
		chat["stop"] = request.StopSequences
	}
	if len(tools) > 0 {
		chat["tools"] = tools
	}
	if choice != nil {
		chat["tool_choice"] = choice
	}
	encoded, err := marshalObject(chat)
	if err != nil {
		return nil, nil, err
	}
	protocol := &anthropicProtocol{
		requestID: requestID,
		model:     request.Model,
		stream: anthropicStreamState{
			requestID: requestID,
			model:     request.Model,
			tools:     make(map[int]*anthropicStreamTool),
		},
	}
	return encoded, protocol, nil
}

func anthropicSystem(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, strings.TrimSpace(text) != "", nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return "", false, provider.ErrInvalidRequest
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type != "text" || block.Text == "" {
			return "", false, fmt.Errorf("%w: Anthropic system block", provider.ErrUnsupportedCapability)
		}
		result.WriteString(block.Text)
	}
	return result.String(), true, nil
}

func anthropicMessage(raw json.RawMessage) ([]map[string]any, error) {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil ||
		(message.Role != "user" && message.Role != "assistant") {
		return nil, provider.ErrInvalidRequest
	}
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		if text == "" {
			return nil, provider.ErrInvalidRequest
		}
		return []map[string]any{{"role": message.Role, "content": text}}, nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(message.Content, &blocks) != nil || len(blocks) == 0 {
		return nil, provider.ErrInvalidRequest
	}
	parts := make([]map[string]any, 0, len(blocks))
	toolCalls := make([]map[string]any, 0)
	toolResults := make([]map[string]any, 0)
	for _, block := range blocks {
		var blockType string
		_ = json.Unmarshal(block["type"], &blockType)
		switch blockType {
		case "text":
			var value string
			if json.Unmarshal(block["text"], &value) != nil {
				return nil, provider.ErrInvalidRequest
			}
			parts = append(parts, map[string]any{"type": "text", "text": value})
		case "image":
			part, err := anthropicImagePart(block["source"])
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case "document":
			part, err := anthropicDocumentPart(block)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case "tool_use":
			if message.Role != "assistant" {
				return nil, provider.ErrInvalidRequest
			}
			var id, name string
			var input json.RawMessage
			_ = json.Unmarshal(block["id"], &id)
			_ = json.Unmarshal(block["name"], &name)
			input = block["input"]
			if id == "" || name == "" || len(input) == 0 || !json.Valid(input) {
				return nil, provider.ErrInvalidRequest
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": string(input)},
			})
		case "tool_result":
			if message.Role != "user" {
				return nil, provider.ErrInvalidRequest
			}
			result, err := anthropicToolResult(block)
			if err != nil {
				return nil, err
			}
			toolResults = append(toolResults, result)
		default:
			return nil, fmt.Errorf("%w: Anthropic content block %s", provider.ErrUnsupportedCapability, blockType)
		}
	}
	result := make([]map[string]any, 0, len(toolResults)+1)
	result = append(result, toolResults...)
	if len(parts) > 0 || len(toolCalls) > 0 {
		content := any(parts)
		if len(parts) == 0 {
			content = nil
		}
		converted := map[string]any{"role": message.Role, "content": content}
		if len(toolCalls) > 0 {
			converted["tool_calls"] = toolCalls
		}
		result = append(result, converted)
	}
	if len(result) == 0 {
		return nil, provider.ErrInvalidRequest
	}
	return result, nil
}

func anthropicImagePart(raw json.RawMessage) (map[string]any, error) {
	var source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return nil, provider.ErrInvalidRequest
	}
	imageURL := source.URL
	switch source.Type {
	case "base64":
		if source.MediaType == "" || source.Data == "" {
			return nil, provider.ErrInvalidRequest
		}
		if _, err := base64.StdEncoding.DecodeString(source.Data); err != nil {
			return nil, provider.ErrInvalidRequest
		}
		imageURL = "data:" + source.MediaType + ";base64," + source.Data
	case "url":
		if source.URL == "" {
			return nil, provider.ErrInvalidRequest
		}
	default:
		return nil, fmt.Errorf("%w: Anthropic image source %s", provider.ErrUnsupportedCapability, source.Type)
	}
	return map[string]any{
		"type": "image_url", "image_url": map[string]any{"url": imageURL},
	}, nil
}

func anthropicDocumentPart(block map[string]json.RawMessage) (map[string]any, error) {
	var source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	}
	if json.Unmarshal(block["source"], &source) != nil {
		return nil, provider.ErrInvalidRequest
	}
	var filename string
	_ = json.Unmarshal(block["title"], &filename)
	file := map[string]any{"filename": filename}
	switch source.Type {
	case "base64":
		if source.MediaType == "" || source.Data == "" {
			return nil, provider.ErrInvalidRequest
		}
		if _, err := base64.StdEncoding.DecodeString(source.Data); err != nil {
			return nil, provider.ErrInvalidRequest
		}
		file["file_data"] = "data:" + source.MediaType + ";base64," + source.Data
	default:
		return nil, fmt.Errorf("%w: Anthropic document source %s", provider.ErrUnsupportedCapability, source.Type)
	}
	return map[string]any{"type": "file", "file": file}, nil
}

func anthropicToolResult(block map[string]json.RawMessage) (map[string]any, error) {
	var id string
	var isError bool
	_ = json.Unmarshal(block["tool_use_id"], &id)
	_ = json.Unmarshal(block["is_error"], &isError)
	if id == "" {
		return nil, provider.ErrInvalidRequest
	}
	content := ""
	if json.Unmarshal(block["content"], &content) != nil {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(block["content"], &parts) != nil {
			return nil, fmt.Errorf("%w: non-text Anthropic tool result", provider.ErrUnsupportedCapability)
		}
		var combined strings.Builder
		for _, part := range parts {
			if part.Type != "text" {
				return nil, fmt.Errorf("%w: non-text Anthropic tool result", provider.ErrUnsupportedCapability)
			}
			combined.WriteString(part.Text)
		}
		content = combined.String()
	}
	if isError {
		content = "tool_error: " + content
	}
	return map[string]any{"role": "tool", "tool_call_id": id, "content": content}, nil
}

func anthropicTools(rawTools []json.RawMessage) ([]map[string]any, error) {
	tools := make([]map[string]any, 0, len(rawTools))
	for _, raw := range rawTools {
		var tool struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
			Type        string          `json:"type"`
		}
		if json.Unmarshal(raw, &tool) != nil || tool.Name == "" ||
			len(tool.InputSchema) == 0 {
			return nil, provider.ErrInvalidRequest
		}
		if tool.Type != "" && tool.Type != "custom" {
			return nil, fmt.Errorf("%w: Anthropic server tool %s", provider.ErrUnsupportedCapability, tool.Type)
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tool.Name, "description": tool.Description,
				"parameters": tool.InputSchema,
			},
		})
	}
	return tools, nil
}

func anthropicToolChoice(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var choice struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if json.Unmarshal(raw, &choice) != nil || choice.DisableParallelToolUse {
		return nil, fmt.Errorf("%w: Anthropic tool choice", provider.ErrUnsupportedCapability)
	}
	switch choice.Type {
	case "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "any":
		return "required", nil
	case "tool":
		if choice.Name == "" {
			return nil, provider.ErrInvalidRequest
		}
		return map[string]any{
			"type": "function", "function": map[string]any{"name": choice.Name},
		}, nil
	default:
		return nil, provider.ErrInvalidRequest
	}
}

func (protocol *anthropicProtocol) EncodeResponse(body []byte) ([]byte, error) {
	var completion chatCompletionEnvelope
	if json.Unmarshal(body, &completion) != nil || len(completion.Choices) == 0 {
		return nil, provider.ErrInvalidResponse
	}
	choice := completion.Choices[0]
	content := make([]any, 0, 1+len(choice.Message.ToolCalls))
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": *choice.Message.Content})
	}
	for _, call := range choice.Message.ToolCalls {
		var input any
		if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
			return nil, provider.ErrInvalidResponse
		}
		content = append(content, map[string]any{
			"type": "tool_use", "id": call.ID,
			"name": call.Function.Name, "input": input,
		})
	}
	response := map[string]any{
		"id": responseItemID(completion.ID, "msg"), "type": "message",
		"role": "assistant", "content": content, "model": completion.Model,
		"stop_reason":   anthropicStopReason(choice.FinishReason),
		"stop_sequence": nil,
	}
	if completion.Usage != nil {
		response["usage"] = anthropicUsage(completion.Usage)
	}
	return marshalObject(response)
}

func anthropicUsage(usage *chatUsageEnvelope) map[string]any {
	return map[string]any{
		"input_tokens":                usage.PromptTokens,
		"output_tokens":               usage.CompletionTokens,
		"cache_read_input_tokens":     usage.PromptTokensDetails["cached_tokens"],
		"cache_creation_input_tokens": usage.PromptTokensDetails["cached_write_tokens"],
	}
}

func anthropicStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "stop":
		return "end_turn"
	case "content_filter":
		return "refusal"
	default:
		return reason
	}
}

type anthropicStreamTool struct {
	ID        string
	Name      string
	Arguments strings.Builder
	Block     int
}

type anthropicStreamState struct {
	requestID    string
	model        string
	id           string
	started      bool
	textOpen     bool
	textBlock    int
	nextBlock    int
	tools        map[int]*anthropicStreamTool
	finishReason string
	outputTokens int
	completed    bool
}

func (protocol *anthropicProtocol) WriteStream(
	writer io.Writer,
	event provider.ChatStreamEvent,
) error {
	return protocol.stream.write(writer, event)
}

func (state *anthropicStreamState) write(writer io.Writer, event provider.ChatStreamEvent) error {
	if event.Done {
		return state.complete(writer)
	}
	var chunk struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(event.Data, &chunk) != nil {
		return provider.ErrInvalidStream
	}
	if !state.started {
		state.started = true
		state.id = responseItemID(chunk.ID, "msg")
		if chunk.Model != "" {
			state.model = chunk.Model
		}
		inputTokens := 0
		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
		}
		if err := writeNamedSSE(writer, "message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": state.id, "type": "message", "role": "assistant",
				"content": []any{}, "model": state.model,
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
			},
		}); err != nil {
			return err
		}
	}
	if chunk.Usage != nil {
		state.outputTokens = chunk.Usage.CompletionTokens
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil {
			if err := state.openTextBlock(writer); err != nil {
				return err
			}
			if err := writeNamedSSE(writer, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": state.textBlock,
				"delta": map[string]any{"type": "text_delta", "text": *choice.Delta.Content},
			}); err != nil {
				return err
			}
		}
		for _, call := range choice.Delta.ToolCalls {
			if err := state.toolDelta(
				writer, call.Index, call.ID, call.Function.Name, call.Function.Arguments,
			); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil {
			state.finishReason = *choice.FinishReason
		}
	}
	if state.finishReason != "" {
		return state.complete(writer)
	}
	return nil
}

func (state *anthropicStreamState) openTextBlock(writer io.Writer) error {
	if state.textOpen {
		return nil
	}
	state.textOpen = true
	state.textBlock = state.nextBlock
	state.nextBlock++
	return writeNamedSSE(writer, "content_block_start", map[string]any{
		"type": "content_block_start", "index": state.textBlock,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (state *anthropicStreamState) toolDelta(
	writer io.Writer,
	index int,
	id, name, arguments string,
) error {
	tool := state.tools[index]
	if tool == nil {
		tool = &anthropicStreamTool{
			ID: id, Name: name, Block: state.nextBlock,
		}
		state.nextBlock++
		state.tools[index] = tool
		if err := writeNamedSSE(writer, "content_block_start", map[string]any{
			"type": "content_block_start", "index": tool.Block,
			"content_block": map[string]any{
				"type": "tool_use", "id": id, "name": name, "input": map[string]any{},
			},
		}); err != nil {
			return err
		}
	}
	if id != "" {
		tool.ID = id
	}
	if name != "" {
		tool.Name = name
	}
	tool.Arguments.WriteString(arguments)
	if arguments == "" {
		return nil
	}
	return writeNamedSSE(writer, "content_block_delta", map[string]any{
		"type": "content_block_delta", "index": tool.Block,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": arguments},
	})
}

func (state *anthropicStreamState) complete(writer io.Writer) error {
	if state.completed {
		return nil
	}
	state.completed = true
	if state.textOpen {
		if err := writeNamedSSE(writer, "content_block_stop", map[string]any{
			"type": "content_block_stop", "index": state.textBlock,
		}); err != nil {
			return err
		}
	}
	indices := make([]int, 0, len(state.tools))
	for index := range state.tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		if err := writeNamedSSE(writer, "content_block_stop", map[string]any{
			"type": "content_block_stop", "index": state.tools[index].Block,
		}); err != nil {
			return err
		}
	}
	if err := writeNamedSSE(writer, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   anthropicStopReason(state.finishReason),
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": state.outputTokens},
	}); err != nil {
		return err
	}
	return writeNamedSSE(writer, "message_stop", map[string]any{"type": "message_stop"})
}
