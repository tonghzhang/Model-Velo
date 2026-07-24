package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// bedrockAdapter 调用 Bedrock Converse API，模型名属于请求路径而不是请求体。
type bedrockAdapter struct {
	baseURL   string
	transport *jsonTransport
}

// bedrockRequest 对应 Converse API；system 与普通 messages 分开编码。
type bedrockRequest struct {
	System          []bedrockContent       `json:"system,omitempty"`
	Messages        []bedrockMessage       `json:"messages"`
	InferenceConfig bedrockInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *bedrockToolConfig     `json:"toolConfig,omitempty"`
	OutputConfig    *bedrockOutputConfig   `json:"outputConfig,omitempty"`
}

type bedrockMessage struct {
	Role    string           `json:"role"`
	Content []bedrockContent `json:"content"`
}

type bedrockContent struct {
	Text       string             `json:"text,omitempty"`
	Image      *bedrockImage      `json:"image,omitempty"`
	Document   *bedrockDocument   `json:"document,omitempty"`
	ToolUse    *bedrockToolUse    `json:"toolUse,omitempty"`
	ToolResult *bedrockToolResult `json:"toolResult,omitempty"`
}

type bedrockImage struct {
	Format string             `json:"format"`
	Source bedrockImageSource `json:"source"`
}

type bedrockImageSource struct {
	Bytes string `json:"bytes"`
}

type bedrockDocument struct {
	Format string                `json:"format"`
	Name   string                `json:"name"`
	Source bedrockDocumentSource `json:"source"`
}

type bedrockDocumentSource struct {
	Bytes string `json:"bytes"`
}

type bedrockToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type bedrockToolResult struct {
	ToolUseID string           `json:"toolUseId"`
	Content   []bedrockContent `json:"content"`
	Status    string           `json:"status,omitempty"`
}

type bedrockToolConfig struct {
	Tools      []bedrockTool      `json:"tools"`
	ToolChoice *bedrockToolChoice `json:"toolChoice,omitempty"`
}

type bedrockTool struct {
	ToolSpec bedrockToolSpec `json:"toolSpec"`
}

type bedrockToolSpec struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	InputSchema bedrockInputSchema `json:"inputSchema"`
}

type bedrockInputSchema struct {
	JSON json.RawMessage `json:"json"`
}

type bedrockToolChoice struct {
	Auto *struct{}        `json:"auto,omitempty"`
	Any  *struct{}        `json:"any,omitempty"`
	Tool *bedrockToolName `json:"tool,omitempty"`
}

type bedrockToolName struct {
	Name string `json:"name"`
}

type bedrockOutputConfig struct {
	TextFormat bedrockTextFormat `json:"textFormat"`
}

type bedrockTextFormat struct {
	Type      string                 `json:"type"`
	Structure bedrockOutputStructure `json:"structure"`
}

type bedrockOutputStructure struct {
	JSONSchema bedrockOutputJSONSchema `json:"jsonSchema"`
}

type bedrockOutputJSONSchema struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Schema      string `json:"schema"`
}

type bedrockInferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

func newBedrockAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	if _, err := parseBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &bedrockAdapter{baseURL: baseURL, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *bedrockAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 构造每个模型独立的 Converse 端点，并执行一次非流式调用。
func (adapter *bedrockAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	body, request, err := bedrockRequestBody(input)
	if err != nil {
		return nil, err
	}
	endpoint, err := bedrockConverseEndpoint(adapter.baseURL, request.Model)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.post(
		ctx, endpoint, input.RequestID, body, bedrockHeaders(apiKey),
	)
	if err != nil {
		return nil, err
	}
	return decodeBedrockResponse(responseBody, request.Model, input.RequestID)
}

func bedrockRequestBody(input ChatInput) ([]byte, ChatRequest, error) {
	request, err := decodeNativeChatRequest(input, nativeRequestSupport{
		tools: true, structured: true, developer: true, toolName: true,
	})
	if err != nil {
		return nil, ChatRequest{}, err
	}
	if err := requireLeadingInstructions(request, ProtocolBedrock); err != nil {
		return nil, ChatRequest{}, err
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, ChatRequest{}, err
	}
	upstream := bedrockRequest{
		InferenceConfig: bedrockInferenceConfig{
			MaxTokens:     requestedMaxTokens(request, 0),
			Temperature:   request.Temperature,
			TopP:          request.TopP,
			StopSequences: stopSequences,
		},
	}
	if err := applyBedrockTools(&upstream, request); err != nil {
		return nil, ChatRequest{}, err
	}
	if err := applyBedrockResponseFormat(&upstream, request); err != nil {
		return nil, ChatRequest{}, err
	}
	for _, message := range request.Messages {
		if message.Role == "system" || message.Role == "developer" {
			content, err := textContent(message.Content)
			if err != nil {
				return nil, ChatRequest{}, err
			}
			upstream.System = append(upstream.System, bedrockContent{Text: content})
			continue
		}
		content, err := bedrockMessageContents(message)
		if err != nil {
			return nil, ChatRequest{}, err
		}
		role := message.Role
		if role == "tool" {
			role = "user"
		}
		upstream.Messages = append(upstream.Messages, bedrockMessage{Role: role, Content: content})
	}
	if len(upstream.Messages) == 0 {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	return body, request, nil
}

func bedrockHeaders(apiKey string) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	return headers
}

func (adapter *bedrockAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) (*ChatEventStream, error) {
	body, request, err := bedrockRequestBody(input)
	if err != nil {
		return nil, err
	}
	endpoint, err := bedrockConverseStreamEndpoint(adapter.baseURL, request.Model)
	if err != nil {
		return nil, err
	}
	responseBody, err := adapter.transport.postStreamTypes(
		ctx, endpoint, input.RequestID, body, bedrockHeaders(apiKey),
		"application/vnd.amazon.eventstream",
	)
	if err != nil {
		return nil, err
	}
	return newMappedAWSStream(
		responseBody, newBedrockStreamMapper(request.Model, input.RequestID),
	)
}

func applyBedrockTools(upstream *bedrockRequest, request ChatRequest) error {
	if len(request.Tools) > 0 && request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		return unsupportedRequestField("parallel_tool_calls")
	}
	if len(request.Tools) > 0 {
		upstream.ToolConfig = &bedrockToolConfig{}
		for _, tool := range request.Tools {
			schema := tool.Function.Parameters
			if !valuePresent(schema) {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			upstream.ToolConfig.Tools = append(upstream.ToolConfig.Tools, bedrockTool{
				ToolSpec: bedrockToolSpec{
					Name: tool.Function.Name, Description: tool.Function.Description,
					InputSchema: bedrockInputSchema{JSON: schema},
				},
			})
		}
	}
	choice, err := decodeToolChoice(request.ToolChoice)
	if err != nil {
		return err
	}
	if choice.Mode == "" {
		return nil
	}
	if upstream.ToolConfig == nil {
		upstream.ToolConfig = &bedrockToolConfig{}
	}
	empty := &struct{}{}
	switch choice.Mode {
	case "none":
		return unsupportedRequestField("tool_choice=none")
	case "auto":
		upstream.ToolConfig.ToolChoice = &bedrockToolChoice{Auto: empty}
	case "required":
		upstream.ToolConfig.ToolChoice = &bedrockToolChoice{Any: empty}
	case "function":
		upstream.ToolConfig.ToolChoice = &bedrockToolChoice{
			Tool: &bedrockToolName{Name: choice.Name},
		}
	}
	return nil
}

func applyBedrockResponseFormat(upstream *bedrockRequest, request ChatRequest) error {
	format, err := decodeResponseFormat(request.ResponseFormat)
	if err != nil {
		return err
	}
	if format.Type == "" || format.Type == "text" {
		return nil
	}
	name := format.Name
	schema := format.Schema
	if format.Type == "json_object" {
		name = "response"
		schema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
	}
	upstream.OutputConfig = &bedrockOutputConfig{TextFormat: bedrockTextFormat{
		Type: "json_schema",
		Structure: bedrockOutputStructure{JSONSchema: bedrockOutputJSONSchema{
			Name: name, Description: format.Description, Schema: string(schema),
		}},
	}}
	return nil
}

func bedrockMessageContents(message ChatMessage) ([]bedrockContent, error) {
	if message.Role == "tool" {
		text, err := textContent(message.Content)
		if err != nil {
			return nil, err
		}
		if message.ToolCallID == "" {
			return nil, ErrInvalidRequest
		}
		return []bedrockContent{{ToolResult: &bedrockToolResult{
			ToolUseID: message.ToolCallID,
			Content:   []bedrockContent{{Text: text}},
			Status:    "success",
		}}}, nil
	}
	content := make([]bedrockContent, 0)
	raw := bytes.TrimSpace(message.Content)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		converted, err := bedrockContents(message.Content)
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
		content = append(content, bedrockContent{ToolUse: &bedrockToolUse{
			ToolUseID: call.ID, Name: call.Function.Name, Input: input,
		}})
	}
	if len(content) == 0 {
		return nil, ErrInvalidRequest
	}
	return content, nil
}

// bedrockContents 将图片 data URL 转为 Bedrock 要求的格式和 Base64 字节。
// 远程图片 URL 不会由网关代下载，避免额外的网络与安全边界。
func bedrockContents(raw json.RawMessage) ([]bedrockContent, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return nil, err
	}
	content := make([]bedrockContent, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" {
			content = append(content, bedrockContent{Text: part.Text})
			continue
		}
		switch part.Type {
		case "image_url":
			if err := rejectNativeImageDetail(part, ProtocolBedrock); err != nil {
				return nil, err
			}
			mediaType, data, err := base64Image(part.ImageURL)
			if err != nil {
				return nil, err
			}
			format := strings.TrimPrefix(mediaType, "image/")
			if format == "jpg" {
				format = "jpeg"
			}
			switch format {
			case "png", "jpeg", "gif", "webp":
			default:
				return nil, fmt.Errorf("%w: image format %q", ErrUnsupportedCapability, format)
			}
			content = append(content, bedrockContent{Image: &bedrockImage{
				Format: format,
				Source: bedrockImageSource{Bytes: data},
			}})
		case "file":
			if part.FileID != "" {
				return nil, fmt.Errorf("%w: Bedrock file_id content", ErrUnsupportedCapability)
			}
			mediaType, data, ok := parseDataURL(part.FileData)
			if !ok {
				return nil, ErrInvalidRequest
			}
			format, ok := bedrockDocumentFormat(mediaType)
			if !ok {
				return nil, fmt.Errorf("%w: Bedrock document type %q", ErrUnsupportedCapability, mediaType)
			}
			name := safeDocumentName(part.Filename)
			content = append(content, bedrockContent{Document: &bedrockDocument{
				Format: format, Name: name, Source: bedrockDocumentSource{Bytes: data},
			}})
		default:
			return nil, fmt.Errorf("%w: Bedrock %s content", ErrUnsupportedCapability, part.Type)
		}
	}
	return content, nil
}

func bedrockDocumentFormat(mediaType string) (string, bool) {
	formats := map[string]string{
		"application/pdf": "pdf",
		"text/plain":      "txt",
		"text/csv":        "csv",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	}
	format, ok := formats[strings.ToLower(mediaType)]
	return format, ok
}

func safeDocumentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "document"
	}
	var safe strings.Builder
	for _, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == ' ' {
			safe.WriteRune(character)
		}
	}
	name = strings.TrimSpace(safe.String())
	if name == "" {
		return "document"
	}
	return name
}

// bedrockConverseEndpoint 拒绝可能改变 URL 层级的模型名。
func bedrockConverseEndpoint(rawBaseURL, model string) (string, error) {
	return bedrockModelEndpoint(rawBaseURL, model, "converse")
}

func bedrockConverseStreamEndpoint(rawBaseURL, model string) (string, error) {
	return bedrockModelEndpoint(rawBaseURL, model, "converse-stream")
}

func bedrockModelEndpoint(rawBaseURL, model, operation string) (string, error) {
	parsed, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return "", err
	}
	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, "/?#") {
		return "", ErrInvalidRequest
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/model/" + model + "/" + operation
	parsed.RawPath = ""
	return parsed.String(), nil
}

// decodeBedrockResponse 合并文本块，并把 Bedrock stopReason 映射为 OpenAI finish_reason。
func decodeBedrockResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		Output struct {
			Message struct {
				Content []json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"output"`
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens           *int `json:"inputTokens"`
			OutputTokens          *int `json:"outputTokens"`
			TotalTokens           *int `json:"totalTokens"`
			CacheReadInputTokens  *int `json:"cacheReadInputTokens"`
			CacheWriteInputTokens *int `json:"cacheWriteInputTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Output.Message.Content) == 0 {
		return nil, ErrInvalidResponse
	}
	var content strings.Builder
	var toolCalls []ChatToolCall
	for _, rawBlock := range response.Output.Message.Content {
		var block struct {
			Text    *string         `json:"text"`
			ToolUse *bedrockToolUse `json:"toolUse"`
		}
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return nil, ErrInvalidResponse
		}
		if block.ToolUse != nil {
			if block.ToolUse.ToolUseID == "" || block.ToolUse.Name == "" ||
				!jsonObject(block.ToolUse.Input) {
				return nil, ErrInvalidResponse
			}
			toolCalls = append(toolCalls, ChatToolCall{
				ID: block.ToolUse.ToolUseID, Type: "function",
				Function: ChatFunctionCall{
					Name: block.ToolUse.Name, Arguments: string(block.ToolUse.Input),
				},
			})
			continue
		}
		if block.Text == nil {
			return nil, ErrInvalidResponse
		}
		content.WriteString(*block.Text)
	}
	finishReason := response.StopReason
	switch finishReason {
	case "end_turn", "stop_sequence", "":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "guardrail_intervened", "content_filtered":
		finishReason = "content_filter"
	case "tool_use":
		finishReason = "tool_calls"
	}
	var usage *completionUsage
	if response.Usage != nil {
		inputTokens, valid := inclusiveInputTokens(
			response.Usage.InputTokens,
			response.Usage.CacheReadInputTokens,
			response.Usage.CacheWriteInputTokens,
		)
		if !valid {
			return nil, ErrInvalidResponse
		}
		usage = newCompletionUsage(inputTokens, response.Usage.OutputTokens, nil)
		usage.setInputDetails(
			nil,
			nil,
			response.Usage.CacheReadInputTokens,
			response.Usage.CacheWriteInputTokens,
		)
	}
	text := content.String()
	message := completionMessage{Role: "assistant", ToolCalls: toolCalls}
	if text != "" || len(toolCalls) == 0 {
		message.Content = &text
	}
	return encodeCompletionMessage("", requestID, model, message, finishReason, usage)
}

func newBedrockStreamMapper(model, requestID string) nativeStreamMapper {
	id := "chatcmpl-" + requestID
	finishReason := ""
	toolIndexByBlock := make(map[int]int)
	nextToolIndex := 0
	return func(event string, data []byte) (nativeStreamResult, error) {
		switch event {
		case "messageStart":
			var payload struct {
				Role string `json:"role"`
			}
			if err := invalidNativeStreamJSON(data, &payload); err != nil ||
				payload.Role != "assistant" {
				return nativeStreamResult{}, ErrInvalidStream
			}
			return streamResult(
				id, model, openAIStreamDelta{Role: "assistant"}, "", nil, false,
			)
		case "contentBlockStart":
			var payload struct {
				ContentBlockIndex int `json:"contentBlockIndex"`
				Start             struct {
					ToolUse *struct {
						ToolUseID string `json:"toolUseId"`
						Name      string `json:"name"`
					} `json:"toolUse"`
				} `json:"start"`
			}
			if err := invalidNativeStreamJSON(data, &payload); err != nil {
				return nativeStreamResult{}, err
			}
			if payload.Start.ToolUse == nil {
				return nativeStreamResult{}, nil
			}
			if payload.Start.ToolUse.ToolUseID == "" ||
				payload.Start.ToolUse.Name == "" {
				return nativeStreamResult{}, ErrInvalidStream
			}
			toolIndexByBlock[payload.ContentBlockIndex] = nextToolIndex
			toolIndex := nextToolIndex
			nextToolIndex++
			return streamResult(id, model, openAIStreamDelta{
				ToolCalls: []openAIStreamToolCallDelta{{
					Index: toolIndex,
					ID:    payload.Start.ToolUse.ToolUseID,
					Type:  "function",
					Function: openAIStreamFunctionDelta{
						Name: payload.Start.ToolUse.Name,
					},
				}},
			}, "", nil, false)
		case "contentBlockDelta":
			var payload struct {
				ContentBlockIndex int `json:"contentBlockIndex"`
				Delta             struct {
					Text    *string `json:"text"`
					ToolUse *struct {
						Input string `json:"input"`
					} `json:"toolUse"`
				} `json:"delta"`
			}
			if err := invalidNativeStreamJSON(data, &payload); err != nil {
				return nativeStreamResult{}, err
			}
			switch {
			case payload.Delta.Text != nil:
				return streamResult(id, model, openAIStreamDelta{
					Content: payload.Delta.Text,
				}, "", nil, false)
			case payload.Delta.ToolUse != nil:
				toolIndex, exists := toolIndexByBlock[payload.ContentBlockIndex]
				if !exists {
					return nativeStreamResult{}, ErrInvalidStream
				}
				return streamResult(id, model, openAIStreamDelta{
					ToolCalls: []openAIStreamToolCallDelta{{
						Index: toolIndex,
						Function: openAIStreamFunctionDelta{
							Arguments: payload.Delta.ToolUse.Input,
						},
					}},
				}, "", nil, false)
			default:
				return nativeStreamResult{}, ErrInvalidStream
			}
		case "contentBlockStop":
			return nativeStreamResult{}, nil
		case "messageStop":
			var payload struct {
				StopReason string `json:"stopReason"`
			}
			if err := invalidNativeStreamJSON(data, &payload); err != nil {
				return nativeStreamResult{}, err
			}
			finishReason = normalizeBedrockFinishReason(payload.StopReason)
			return nativeStreamResult{}, nil
		case "metadata":
			var payload struct {
				Usage *struct {
					InputTokens           *int `json:"inputTokens"`
					OutputTokens          *int `json:"outputTokens"`
					TotalTokens           *int `json:"totalTokens"`
					CacheReadInputTokens  *int `json:"cacheReadInputTokens"`
					CacheWriteInputTokens *int `json:"cacheWriteInputTokens"`
				} `json:"usage"`
			}
			if err := invalidNativeStreamJSON(data, &payload); err != nil ||
				finishReason == "" {
				return nativeStreamResult{}, ErrInvalidStream
			}
			var usage *completionUsage
			if payload.Usage != nil {
				inputTokens, valid := inclusiveInputTokens(
					payload.Usage.InputTokens,
					payload.Usage.CacheReadInputTokens,
					payload.Usage.CacheWriteInputTokens,
				)
				if !valid {
					return nativeStreamResult{}, ErrInvalidStream
				}
				usage = newCompletionUsage(
					inputTokens, payload.Usage.OutputTokens, nil,
				)
				usage.setInputDetails(
					nil, nil, payload.Usage.CacheReadInputTokens,
					payload.Usage.CacheWriteInputTokens,
				)
			}
			return streamResult(
				id, model, openAIStreamDelta{}, finishReason, usage, true,
			)
		case nativeStreamEOF:
			if finishReason == "" {
				return nativeStreamResult{}, ErrInvalidStream
			}
			return streamResult(
				id, model, openAIStreamDelta{}, finishReason, nil, true,
			)
		case "internalServerException", "modelStreamErrorException",
			"throttlingException", "validationException",
			"serviceUnavailableException":
			return nativeStreamResult{}, ErrInvalidStream
		default:
			return nativeStreamResult{}, fmt.Errorf(
				"%w: Bedrock stream event %q", ErrInvalidStream, event,
			)
		}
	}
}

func normalizeBedrockFinishReason(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "guardrail_intervened", "content_filtered":
		return "content_filter"
	default:
		return "stop"
	}
}
