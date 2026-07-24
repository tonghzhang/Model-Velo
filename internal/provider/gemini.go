package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// geminiAdapter 调用 Gemini generateContent 原生接口。
type geminiAdapter struct {
	baseURL   string
	transport *jsonTransport
}

// geminiRequest 将系统指令与普通 contents 分开，并使用 Gemini 的生成参数命名。
type geminiRequest struct {
	Contents          []geminiContent        `json:"contents"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool           `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig      `json:"toolConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens *int            `json:"maxOutputTokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"topP,omitempty"`
	StopSequences   []string        `json:"stopSequences,omitempty"`
	Seed            *int64          `json:"seed,omitempty"`
	ResponseMIME    string          `json:"responseMimeType,omitempty"`
	ResponseSchema  json.RawMessage `json:"responseJsonSchema,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig geminiFunctionCallingConfig `json:"functionCallingConfig"`
}

type geminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

func newGeminiAdapter(baseURL string, httpConfig HTTPConfig) (Adapter, error) {
	if _, err := parseBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &geminiAdapter{baseURL: baseURL, transport: newJSONTransport(httpConfig)}, nil
}

func (adapter *geminiAdapter) Authentication() Authentication {
	return AuthenticationAPIKey
}

// Complete 将 assistant 角色映射为 model，并把模型名写入 generateContent 端点。
func (adapter *geminiAdapter) Complete(ctx context.Context, input ChatInput, apiKey string) ([]byte, error) {
	body, request, err := geminiRequestBody(input)
	if err != nil {
		return nil, err
	}
	endpoint, err := modelEndpoint(adapter.baseURL, "models", request.Model, ":generateContent")
	if err != nil {
		return nil, err
	}
	headers := geminiHeaders(apiKey)
	responseBody, err := adapter.transport.post(ctx, endpoint, input.RequestID, body, headers)
	if err != nil {
		return nil, err
	}
	return decodeGeminiResponse(responseBody, request.Model, input.RequestID)
}

func geminiRequestBody(input ChatInput) ([]byte, ChatRequest, error) {
	request, err := decodeNativeChatRequest(input, nativeRequestSupport{
		tools: true, structured: true, seed: true, developer: true, toolName: true,
	})
	if err != nil {
		return nil, ChatRequest{}, err
	}
	if err := requireLeadingInstructions(request, ProtocolGemini); err != nil {
		return nil, ChatRequest{}, err
	}
	stopSequences, err := decodeStop(request.Stop)
	if err != nil {
		return nil, ChatRequest{}, err
	}
	upstream := geminiRequest{
		GenerationConfig: geminiGenerationConfig{
			Temperature:   request.Temperature,
			TopP:          request.TopP,
			StopSequences: stopSequences,
			Seed:          request.Seed,
		},
	}
	if err := applyGeminiTools(&upstream, request); err != nil {
		return nil, ChatRequest{}, err
	}
	if err := applyGeminiResponseFormat(&upstream, request); err != nil {
		return nil, ChatRequest{}, err
	}
	if maxTokens := requestedMaxTokens(request, 0); maxTokens > 0 {
		upstream.GenerationConfig.MaxOutputTokens = &maxTokens
	}
	var systemParts []geminiPart
	for index, message := range request.Messages {
		if message.Role == "system" || message.Role == "developer" {
			content, err := textContent(message.Content)
			if err != nil {
				return nil, ChatRequest{}, err
			}
			systemParts = append(systemParts, geminiPart{Text: content})
			continue
		}
		toolName := message.Name
		if message.Role == "tool" && toolName == "" {
			toolName = priorToolName(request.Messages, index, message.ToolCallID)
		}
		parts, err := geminiMessageParts(message, toolName)
		if err != nil {
			return nil, ChatRequest{}, err
		}
		role := message.Role
		if role == "assistant" {
			role = "model"
		} else if role == "tool" {
			role = "user"
		}
		upstream.Contents = append(upstream.Contents, geminiContent{Role: role, Parts: parts})
	}
	if len(systemParts) > 0 {
		upstream.SystemInstruction = &geminiContent{Parts: systemParts}
	}
	if len(upstream.Contents) == 0 {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return nil, ChatRequest{}, ErrInvalidRequest
	}
	return body, request, nil
}

func geminiHeaders(apiKey string) http.Header {
	headers := make(http.Header)
	headers.Set("x-goog-api-key", strings.TrimSpace(apiKey))
	return headers
}

func (adapter *geminiAdapter) OpenStream(
	ctx context.Context,
	input ChatInput,
	apiKey string,
) (*ChatEventStream, error) {
	body, request, err := geminiRequestBody(input)
	if err != nil {
		return nil, err
	}
	endpoint, err := modelEndpoint(
		adapter.baseURL, "models", request.Model, ":streamGenerateContent",
	)
	if err != nil {
		return nil, err
	}
	endpoint += "?alt=sse"
	responseBody, err := adapter.transport.postStream(
		ctx, endpoint, input.RequestID, body, geminiHeaders(apiKey),
	)
	if err != nil {
		return nil, err
	}
	return newMappedSSEStream(
		responseBody, newGeminiStreamMapper(request.Model, input.RequestID),
	)
}

func applyGeminiTools(upstream *geminiRequest, request ChatRequest) error {
	if len(request.Tools) > 0 && request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		return unsupportedRequestField("parallel_tool_calls")
	}
	if len(request.Tools) > 0 {
		declarations := make([]geminiFunctionDeclaration, 0, len(request.Tools))
		for _, tool := range request.Tools {
			declarations = append(declarations, geminiFunctionDeclaration{
				Name: tool.Function.Name, Description: tool.Function.Description,
				Parameters: tool.Function.Parameters,
			})
		}
		upstream.Tools = []geminiTool{{FunctionDeclarations: declarations}}
	}
	choice, err := decodeToolChoice(request.ToolChoice)
	if err != nil {
		return err
	}
	if choice.Mode == "" {
		return nil
	}
	config := geminiFunctionCallingConfig{Mode: strings.ToUpper(choice.Mode)}
	switch choice.Mode {
	case "required":
		config.Mode = "ANY"
	case "function":
		config.Mode = "ANY"
		config.AllowedFunctionNames = []string{choice.Name}
	}
	upstream.ToolConfig = &geminiToolConfig{FunctionCallingConfig: config}
	return nil
}

func applyGeminiResponseFormat(upstream *geminiRequest, request ChatRequest) error {
	format, err := decodeResponseFormat(request.ResponseFormat)
	if err != nil {
		return err
	}
	switch format.Type {
	case "", "text":
	case "json_object":
		upstream.GenerationConfig.ResponseMIME = "application/json"
	case "json_schema":
		upstream.GenerationConfig.ResponseMIME = "application/json"
		upstream.GenerationConfig.ResponseSchema = format.Schema
	}
	return nil
}

func geminiMessageParts(message ChatMessage, toolName string) ([]geminiPart, error) {
	if message.Role == "tool" {
		if message.ToolCallID == "" {
			return nil, ErrInvalidRequest
		}
		content, err := textContent(message.Content)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(map[string]string{"result": content})
		if err != nil {
			return nil, ErrInvalidRequest
		}
		if toolName == "" {
			return nil, ErrInvalidRequest
		}
		return []geminiPart{{FunctionResponse: &geminiFunctionResponse{
			ID: message.ToolCallID, Name: toolName, Response: encoded,
		}}}, nil
	}
	parts := make([]geminiPart, 0)
	raw := bytes.TrimSpace(message.Content)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		converted, err := geminiParts(message.Content)
		if err != nil {
			return nil, err
		}
		parts = append(parts, converted...)
	}
	for _, call := range message.ToolCalls {
		args, err := toolArgumentsObject(call.Function.Arguments)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
			ID: call.ID, Name: call.Function.Name, Args: args,
		}})
	}
	if len(parts) == 0 {
		return nil, ErrInvalidRequest
	}
	return parts, nil
}

// geminiParts 将内嵌图片转换为 inlineData；远程 URL 在公共内容层被拒绝。
func geminiParts(raw json.RawMessage) ([]geminiPart, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return nil, err
	}
	result := make([]geminiPart, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" {
			result = append(result, geminiPart{Text: part.Text})
			continue
		}
		var mediaType, data string
		switch part.Type {
		case "image_url":
			if err := rejectNativeImageDetail(part, ProtocolGemini); err != nil {
				return nil, err
			}
			var err error
			mediaType, data, err = base64Image(part.ImageURL)
			if err != nil {
				return nil, err
			}
		case "input_audio":
			mediaType = "audio/" + part.AudioFormat
			if part.AudioFormat == "mp3" {
				mediaType = "audio/mpeg"
			}
			data = part.AudioData
		case "file":
			if part.FileID != "" {
				return nil, fmt.Errorf("%w: Gemini file_id content", ErrUnsupportedCapability)
			}
			var ok bool
			mediaType, data, ok = parseDataURL(part.FileData)
			if !ok {
				return nil, ErrInvalidRequest
			}
		default:
			return nil, fmt.Errorf("%w: Gemini %s content", ErrUnsupportedCapability, part.Type)
		}
		result = append(result, geminiPart{InlineData: &geminiInlineData{MIMEType: mediaType, Data: data}})
	}
	return result, nil
}

// decodeGeminiResponse 合并文本 part，并将安全拦截统一映射为 content_filter。
func decodeGeminiResponse(body []byte, model string, requestID string) ([]byte, error) {
	var response struct {
		ResponseID string `json:"responseId"`
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Usage *struct {
			PromptTokens     *int `json:"promptTokenCount"`
			CompletionTokens *int `json:"candidatesTokenCount"`
			TotalTokens      *int `json:"totalTokenCount"`
			CachedTokens     *int `json:"cachedContentTokenCount"`
			ThoughtTokens    *int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Candidates) == 0 {
		return nil, ErrInvalidResponse
	}
	var text strings.Builder
	var reasoning strings.Builder
	var toolCalls []ChatToolCall
	for _, rawPart := range response.Candidates[0].Content.Parts {
		var part struct {
			Text         *string             `json:"text"`
			Thought      bool                `json:"thought"`
			FunctionCall *geminiFunctionCall `json:"functionCall"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return nil, ErrInvalidResponse
		}
		if part.FunctionCall != nil {
			if part.FunctionCall.Name == "" || !jsonObject(part.FunctionCall.Args) {
				return nil, ErrInvalidResponse
			}
			id := part.FunctionCall.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", len(toolCalls))
			}
			toolCalls = append(toolCalls, ChatToolCall{
				ID: id, Type: "function",
				Function: ChatFunctionCall{
					Name: part.FunctionCall.Name, Arguments: string(part.FunctionCall.Args),
				},
			})
			continue
		}
		if part.Text == nil {
			return nil, ErrInvalidResponse
		}
		if part.Thought {
			reasoning.WriteString(*part.Text)
		} else {
			text.WriteString(*part.Text)
		}
	}
	if len(response.Candidates[0].Content.Parts) == 0 {
		return nil, ErrInvalidResponse
	}
	finishReason := "stop"
	switch response.Candidates[0].FinishReason {
	case "MAX_TOKENS":
		finishReason = "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		finishReason = "content_filter"
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	var usage *completionUsage
	if response.Usage != nil {
		usage = newCompletionUsage(response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens)
		usage.setInputDetails(nil, nil, response.Usage.CachedTokens, nil)
		usage.setOutputDetails(nil, response.Usage.ThoughtTokens)
	}
	content := text.String()
	message := completionMessage{Role: "assistant", ToolCalls: toolCalls}
	if reasoning.Len() > 0 {
		reasoningContent := reasoning.String()
		message.ReasoningContent = &reasoningContent
	}
	if content != "" || len(toolCalls) == 0 {
		message.Content = &content
	}
	return encodeCompletionMessage(response.ResponseID, requestID, model, message, finishReason, usage)
}

func newGeminiStreamMapper(model, requestID string) nativeStreamMapper {
	id := "chatcmpl-" + requestID
	toolIndex := 0
	return func(_ string, data []byte) (nativeStreamResult, error) {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nativeStreamResult{Done: true}, nil
		}
		var response struct {
			ResponseID string `json:"responseId"`
			Candidates []struct {
				FinishReason string `json:"finishReason"`
				Content      struct {
					Parts []struct {
						Text         *string             `json:"text"`
						Thought      bool                `json:"thought"`
						FunctionCall *geminiFunctionCall `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			Usage *struct {
				PromptTokens     *int `json:"promptTokenCount"`
				CompletionTokens *int `json:"candidatesTokenCount"`
				TotalTokens      *int `json:"totalTokenCount"`
				CachedTokens     *int `json:"cachedContentTokenCount"`
				ThoughtTokens    *int `json:"thoughtsTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := invalidNativeStreamJSON(data, &response); err != nil ||
			len(response.Candidates) == 0 {
			return nativeStreamResult{}, ErrInvalidStream
		}
		if response.ResponseID != "" {
			id = response.ResponseID
		}
		var chunks [][]byte
		candidate := response.Candidates[0]
		for _, part := range candidate.Content.Parts {
			var delta openAIStreamDelta
			switch {
			case part.Text != nil:
				if part.Thought {
					delta.ReasoningContent = part.Text
				} else {
					delta.Content = part.Text
				}
			case part.FunctionCall != nil:
				if part.FunctionCall.Name == "" || !jsonObject(part.FunctionCall.Args) {
					return nativeStreamResult{}, ErrInvalidStream
				}
				callID := part.FunctionCall.ID
				if callID == "" {
					callID = fmt.Sprintf("call_%d", toolIndex)
				}
				delta.ToolCalls = []openAIStreamToolCallDelta{{
					Index: toolIndex, ID: callID, Type: "function",
					Function: openAIStreamFunctionDelta{
						Name:      part.FunctionCall.Name,
						Arguments: string(part.FunctionCall.Args),
					},
				}}
				toolIndex++
			default:
				return nativeStreamResult{}, ErrInvalidStream
			}
			chunk, err := encodeStreamChunk(id, model, delta, "", nil)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		finish := normalizeGeminiFinishReason(candidate.FinishReason)
		done := candidate.FinishReason != ""
		if done && toolIndex > 0 && finish == "stop" {
			finish = "tool_calls"
		}
		if done {
			var usage *completionUsage
			if response.Usage != nil {
				usage = newCompletionUsage(
					response.Usage.PromptTokens,
					response.Usage.CompletionTokens,
					response.Usage.TotalTokens,
				)
				usage.setInputDetails(nil, nil, response.Usage.CachedTokens, nil)
				usage.setOutputDetails(nil, response.Usage.ThoughtTokens)
			}
			chunk, err := encodeStreamChunk(
				id, model, openAIStreamDelta{}, finish, usage,
			)
			if err != nil {
				return nativeStreamResult{}, err
			}
			chunks = append(chunks, chunk)
		}
		if len(chunks) == 0 {
			return nativeStreamResult{}, nil
		}
		return nativeStreamResult{Chunks: chunks, Done: done}, nil
	}
}

func normalizeGeminiFinishReason(reason string) string {
	switch reason {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	case "":
		return ""
	default:
		return "stop"
	}
}
