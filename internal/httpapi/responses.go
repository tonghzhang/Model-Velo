package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"model-velo/internal/provider"
)

type responsesProtocol struct {
	requestID string
	model     string
	stream    responsesStreamState
}

type responsesRequest struct {
	Model             string            `json:"model"`
	Input             json.RawMessage   `json:"input"`
	Instructions      json.RawMessage   `json:"instructions,omitempty"`
	MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Text              json.RawMessage   `json:"text,omitempty"`
	Reasoning         json.RawMessage   `json:"reasoning,omitempty"`
	Stream            bool              `json:"stream,omitempty"`
	Store             *bool             `json:"store,omitempty"`
}

func (h chatHandler) responses(c *gin.Context) {
	h.serveProtocol(c, convertResponsesRequest)
}

func convertResponsesRequest(
	body []byte,
	requestID string,
) ([]byte, responseProtocol, error) {
	var fields map[string]json.RawMessage
	var request responsesRequest
	if json.Unmarshal(body, &fields) != nil || fields == nil ||
		json.Unmarshal(body, &request) != nil {
		return nil, nil, provider.ErrInvalidRequest
	}
	if field := unknownJSONField(
		fields,
		"model", "input", "instructions", "max_output_tokens",
		"temperature", "top_p", "tools", "tool_choice",
		"parallel_tool_calls", "text", "reasoning", "stream", "store",
	); field != "" {
		return nil, nil, fmt.Errorf("%w: responses field %s", provider.ErrUnsupportedCapability, field)
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, nil, provider.ErrInvalidRequest
	}
	if request.Store != nil && *request.Store {
		return nil, nil, fmt.Errorf("%w: stateful responses storage", provider.ErrUnsupportedCapability)
	}

	messages, err := responsesInputMessages(request.Input)
	if err != nil {
		return nil, nil, err
	}
	if instructions, ok, err := responsesInstructions(request.Instructions); err != nil {
		return nil, nil, err
	} else if ok {
		messages = append([]map[string]any{{
			"role": "developer", "content": instructions,
		}}, messages...)
	}
	tools, err := responsesTools(request.Tools)
	if err != nil {
		return nil, nil, err
	}
	toolChoice, err := responsesToolChoice(request.ToolChoice)
	if err != nil {
		return nil, nil, err
	}
	responseFormat, err := responsesTextFormat(request.Text)
	if err != nil {
		return nil, nil, err
	}
	reasoningEffort, err := responsesReasoningEffort(request.Reasoning)
	if err != nil {
		return nil, nil, err
	}

	chat := map[string]any{
		"model":    strings.TrimSpace(request.Model),
		"messages": messages,
		"stream":   request.Stream,
	}
	putOptional(chat, "max_completion_tokens", request.MaxOutputTokens)
	putOptional(chat, "temperature", request.Temperature)
	putOptional(chat, "top_p", request.TopP)
	putOptional(chat, "parallel_tool_calls", request.ParallelToolCalls)
	if len(tools) > 0 {
		chat["tools"] = tools
	}
	if toolChoice != nil {
		chat["tool_choice"] = toolChoice
	}
	if responseFormat != nil {
		chat["response_format"] = responseFormat
	}
	if reasoningEffort != "" {
		chat["reasoning_effort"] = reasoningEffort
	}
	encoded, err := marshalObject(chat)
	if err != nil {
		return nil, nil, err
	}
	protocol := &responsesProtocol{requestID: requestID, model: request.Model}
	protocol.stream = responsesStreamState{
		requestID: requestID,
		model:     request.Model,
		createdAt: time.Now().Unix(),
		tools:     make(map[int]*responsesStreamTool),
	}
	return encoded, protocol, nil
}

func responsesInstructions(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return "", false, provider.ErrInvalidRequest
		}
		return text, true, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return "", false, provider.ErrInvalidRequest
	}
	var combined strings.Builder
	for _, block := range blocks {
		if block.Type != "input_text" || block.Text == "" {
			return "", false, fmt.Errorf("%w: responses instructions block", provider.ErrUnsupportedCapability)
		}
		combined.WriteString(block.Text)
	}
	return combined.String(), true, nil
}

func responsesInputMessages(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, provider.ErrInvalidRequest
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return nil, provider.ErrInvalidRequest
		}
		return []map[string]any{{"role": "user", "content": text}}, nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil, provider.ErrInvalidRequest
	}
	messages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var itemType string
		_ = json.Unmarshal(item["type"], &itemType)
		if itemType == "" || itemType == "message" {
			message, err := responsesMessage(item)
			if err != nil {
				return nil, err
			}
			messages = append(messages, message)
			continue
		}
		switch itemType {
		case "function_call":
			var call struct {
				CallID    string          `json:"call_id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			encoded, _ := json.Marshal(item)
			if json.Unmarshal(encoded, &call) != nil || call.CallID == "" || call.Name == "" {
				return nil, provider.ErrInvalidRequest
			}
			arguments := string(call.Arguments)
			var argumentString string
			if json.Unmarshal(call.Arguments, &argumentString) == nil {
				arguments = argumentString
			}
			messages = append(messages, map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]any{{
					"id": call.CallID, "type": "function",
					"function": map[string]any{"name": call.Name, "arguments": arguments},
				}},
			})
		case "function_call_output":
			var output struct {
				CallID string          `json:"call_id"`
				Output json.RawMessage `json:"output"`
			}
			encoded, _ := json.Marshal(item)
			if json.Unmarshal(encoded, &output) != nil || output.CallID == "" || len(output.Output) == 0 {
				return nil, provider.ErrInvalidRequest
			}
			content := string(output.Output)
			_ = json.Unmarshal(output.Output, &content)
			messages = append(messages, map[string]any{
				"role": "tool", "tool_call_id": output.CallID, "content": content,
			})
		default:
			return nil, fmt.Errorf("%w: responses input item %s", provider.ErrUnsupportedCapability, itemType)
		}
	}
	return messages, nil
}

func responsesMessage(item map[string]json.RawMessage) (map[string]any, error) {
	if field := unknownJSONField(item, "type", "role", "content", "status", "id"); field != "" {
		return nil, fmt.Errorf("%w: responses message field %s", provider.ErrUnsupportedCapability, field)
	}
	var role string
	if json.Unmarshal(item["role"], &role) != nil {
		return nil, provider.ErrInvalidRequest
	}
	if role != "user" && role != "assistant" && role != "system" && role != "developer" {
		return nil, provider.ErrInvalidRequest
	}
	var text string
	if json.Unmarshal(item["content"], &text) == nil {
		return map[string]any{"role": role, "content": text}, nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(item["content"], &blocks) != nil || len(blocks) == 0 {
		return nil, provider.ErrInvalidRequest
	}
	parts := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		part, err := responsesContentPart(block)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return map[string]any{"role": role, "content": parts}, nil
}

func responsesContentPart(block map[string]json.RawMessage) (map[string]any, error) {
	var blockType string
	_ = json.Unmarshal(block["type"], &blockType)
	switch blockType {
	case "input_text", "output_text":
		var text string
		if json.Unmarshal(block["text"], &text) != nil || text == "" {
			return nil, provider.ErrInvalidRequest
		}
		return map[string]any{"type": "text", "text": text}, nil
	case "input_image":
		var imageURL string
		if json.Unmarshal(block["image_url"], &imageURL) != nil || imageURL == "" {
			return nil, provider.ErrInvalidRequest
		}
		part := map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": imageURL},
		}
		var detail string
		if json.Unmarshal(block["detail"], &detail) == nil && detail != "" {
			part["image_url"].(map[string]any)["detail"] = detail
		}
		return part, nil
	case "input_file":
		file := map[string]any{}
		for _, name := range []string{"file_id", "file_data", "filename"} {
			var value string
			if json.Unmarshal(block[name], &value) == nil && value != "" {
				file[name] = value
			}
		}
		if len(file) == 0 {
			return nil, provider.ErrInvalidRequest
		}
		return map[string]any{"type": "file", "file": file}, nil
	default:
		return nil, fmt.Errorf("%w: responses content block %s", provider.ErrUnsupportedCapability, blockType)
	}
}

func responsesTools(rawTools []json.RawMessage) ([]map[string]any, error) {
	tools := make([]map[string]any, 0, len(rawTools))
	for _, raw := range rawTools {
		var tool map[string]json.RawMessage
		if json.Unmarshal(raw, &tool) != nil {
			return nil, provider.ErrInvalidRequest
		}
		var toolType string
		_ = json.Unmarshal(tool["type"], &toolType)
		if toolType != "function" {
			return nil, fmt.Errorf("%w: hosted responses tool %s", provider.ErrUnsupportedCapability, toolType)
		}
		var name, description string
		var parameters json.RawMessage
		var strict *bool
		_ = json.Unmarshal(tool["name"], &name)
		_ = json.Unmarshal(tool["description"], &description)
		parameters = tool["parameters"]
		_ = json.Unmarshal(tool["strict"], &strict)
		if name == "" || len(parameters) == 0 {
			return nil, provider.ErrInvalidRequest
		}
		function := map[string]any{
			"name": name, "parameters": parameters,
		}
		if description != "" {
			function["description"] = description
		}
		if strict != nil {
			function["strict"] = *strict
		}
		tools = append(tools, map[string]any{"type": "function", "function": function})
	}
	return tools, nil
}

func responsesToolChoice(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch choice {
		case "auto", "none", "required":
			return choice, nil
		default:
			return nil, provider.ErrInvalidRequest
		}
	}
	var object struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &object) != nil || object.Type != "function" || object.Name == "" {
		return nil, fmt.Errorf("%w: responses tool choice", provider.ErrUnsupportedCapability)
	}
	return map[string]any{
		"type": "function", "function": map[string]any{"name": object.Name},
	}, nil
}

func responsesTextFormat(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text struct {
		Format map[string]json.RawMessage `json:"format"`
	}
	if json.Unmarshal(raw, &text) != nil || text.Format == nil {
		return nil, provider.ErrInvalidRequest
	}
	var formatType string
	_ = json.Unmarshal(text.Format["type"], &formatType)
	switch formatType {
	case "", "text":
		return nil, nil
	case "json_object":
		return map[string]any{"type": "json_object"}, nil
	case "json_schema":
		var name string
		var strict bool
		_ = json.Unmarshal(text.Format["name"], &name)
		_ = json.Unmarshal(text.Format["strict"], &strict)
		schema := text.Format["schema"]
		if name == "" || len(schema) == 0 {
			return nil, provider.ErrInvalidRequest
		}
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": name, "strict": strict, "schema": schema,
			},
		}, nil
	default:
		return nil, fmt.Errorf("%w: responses text format %s", provider.ErrUnsupportedCapability, formatType)
	}
}

func responsesReasoningEffort(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var reasoning struct {
		Effort  string          `json:"effort"`
		Summary json.RawMessage `json:"summary"`
	}
	if json.Unmarshal(raw, &reasoning) != nil {
		return "", provider.ErrInvalidRequest
	}
	if len(reasoning.Summary) > 0 && string(reasoning.Summary) != "null" {
		return "", fmt.Errorf("%w: reasoning summaries", provider.ErrUnsupportedCapability)
	}
	return reasoning.Effort, nil
}

func putOptional(target map[string]any, name string, value any) {
	switch typed := value.(type) {
	case *int:
		if typed != nil {
			target[name] = *typed
		}
	case *float64:
		if typed != nil {
			target[name] = *typed
		}
	case *bool:
		if typed != nil {
			target[name] = *typed
		}
	}
}

type chatCompletionEnvelope struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   *string                 `json:"content"`
			ToolCalls []provider.ChatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsageEnvelope `json:"usage"`
}

type chatUsageEnvelope struct {
	PromptTokens            int            `json:"prompt_tokens"`
	CompletionTokens        int            `json:"completion_tokens"`
	TotalTokens             int            `json:"total_tokens"`
	PromptTokensDetails     map[string]int `json:"prompt_tokens_details"`
	CompletionTokensDetails map[string]int `json:"completion_tokens_details"`
}

func (protocol *responsesProtocol) EncodeResponse(body []byte) ([]byte, error) {
	var completion chatCompletionEnvelope
	if json.Unmarshal(body, &completion) != nil || len(completion.Choices) == 0 {
		return nil, provider.ErrInvalidResponse
	}
	choice := completion.Choices[0]
	output := make([]any, 0, 1+len(choice.Message.ToolCalls))
	if choice.Message.Content != nil {
		output = append(output, map[string]any{
			"id":     responseItemID(completion.ID, "msg"),
			"type":   "message",
			"status": responseItemStatus(choice.FinishReason),
			"role":   "assistant",
			"content": []map[string]any{{
				"type": "output_text", "text": *choice.Message.Content, "annotations": []any{},
			}},
		})
	}
	for _, call := range choice.Message.ToolCalls {
		function := call.Function
		if function.Name == "" {
			return nil, provider.ErrInvalidResponse
		}
		output = append(output, map[string]any{
			"id": responseItemID(call.ID, "fc"), "type": "function_call",
			"status": "completed", "call_id": call.ID,
			"name": function.Name, "arguments": function.Arguments,
		})
	}
	status := "completed"
	var incomplete any
	if choice.FinishReason == "length" {
		status = "incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	}
	response := map[string]any{
		"id":                  responseID(completion.ID, protocol.requestID),
		"object":              "response",
		"created_at":          completion.Created,
		"completed_at":        time.Now().Unix(),
		"status":              status,
		"error":               nil,
		"incomplete_details":  incomplete,
		"model":               completion.Model,
		"output":              output,
		"parallel_tool_calls": true,
		"tools":               []any{},
		"store":               false,
	}
	if completion.Usage != nil {
		response["usage"] = responsesUsage(*completion.Usage)
	}
	return marshalObject(response)
}

func responseID(upstreamID, requestID string) string {
	id := strings.TrimSpace(upstreamID)
	id = strings.TrimPrefix(id, "chatcmpl-")
	if id == "" {
		id = requestID
	}
	return "resp_" + id
}

func responseItemID(value, prefix string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "chatcmpl-")
	value = strings.TrimPrefix(value, "call_")
	if value == "" {
		value = "gateway"
	}
	return prefix + "_" + value
}

func responseItemStatus(finishReason string) string {
	if finishReason == "length" {
		return "incomplete"
	}
	return "completed"
}

type responsesStreamTool struct {
	ID        string
	Name      string
	Arguments strings.Builder
	Output    int
}

type responsesStreamState struct {
	requestID    string
	model        string
	id           string
	createdAt    int64
	sequence     int
	started      bool
	textOpened   bool
	text         strings.Builder
	tools        map[int]*responsesStreamTool
	nextOutput   int
	usage        any
	finishReason string
	completed    bool
}

func (protocol *responsesProtocol) WriteStream(
	writer io.Writer,
	event provider.ChatStreamEvent,
) error {
	return protocol.stream.write(writer, event)
}

func (state *responsesStreamState) write(writer io.Writer, event provider.ChatStreamEvent) error {
	if event.Done {
		return state.complete(writer)
	}
	var chunk struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
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
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(event.Data, &chunk) != nil {
		return provider.ErrInvalidStream
	}
	if !state.started {
		state.started = true
		state.id = responseID(chunk.ID, state.requestID)
		if chunk.Created != 0 {
			state.createdAt = chunk.Created
		}
		if chunk.Model != "" {
			state.model = chunk.Model
		}
		if err := state.emit(writer, "response.created", map[string]any{
			"type": "response.created", "response": state.response("in_progress", nil),
		}); err != nil {
			return err
		}
		if err := state.emit(writer, "response.in_progress", map[string]any{
			"type": "response.in_progress", "response": state.response("in_progress", nil),
		}); err != nil {
			return err
		}
	}
	if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
		var chatUsage chatUsageEnvelope
		if json.Unmarshal(chunk.Usage, &chatUsage) != nil {
			return provider.ErrInvalidStream
		}
		state.usage = responsesUsage(chatUsage)
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil {
			if err := state.openText(writer); err != nil {
				return err
			}
			state.text.WriteString(*choice.Delta.Content)
			if err := state.emit(writer, "response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "output_index": 0,
				"content_index": 0, "delta": *choice.Delta.Content,
				"item_id": responseItemID(state.id, "msg"),
			}); err != nil {
				return err
			}
		}
		for _, call := range choice.Delta.ToolCalls {
			if err := state.writeToolDelta(writer, call.Index, call.ID, call.Function.Name, call.Function.Arguments); err != nil {
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

func (state *responsesStreamState) openText(writer io.Writer) error {
	if state.textOpened {
		return nil
	}
	state.textOpened = true
	state.nextOutput = 1
	item := map[string]any{
		"id": responseItemID(state.id, "msg"), "type": "message",
		"status": "in_progress", "role": "assistant", "content": []any{},
	}
	if err := state.emit(writer, "response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0, "item": item,
	}); err != nil {
		return err
	}
	return state.emit(writer, "response.content_part.added", map[string]any{
		"type": "response.content_part.added", "output_index": 0, "content_index": 0,
		"item_id": item["id"], "part": map[string]any{
			"type": "output_text", "text": "", "annotations": []any{},
		},
	})
}

func (state *responsesStreamState) writeToolDelta(
	writer io.Writer,
	index int,
	id, name, arguments string,
) error {
	tool := state.tools[index]
	if tool == nil {
		tool = &responsesStreamTool{ID: id, Name: name, Output: state.nextOutput}
		state.nextOutput++
		state.tools[index] = tool
		if err := state.emit(writer, "response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": tool.Output,
			"item": map[string]any{
				"id": responseItemID(id, "fc"), "type": "function_call",
				"status": "in_progress", "call_id": id, "name": name, "arguments": "",
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
	return state.emit(writer, "response.function_call_arguments.delta", map[string]any{
		"type":    "response.function_call_arguments.delta",
		"item_id": responseItemID(tool.ID, "fc"), "output_index": tool.Output,
		"delta": arguments,
	})
}

func (state *responsesStreamState) complete(writer io.Writer) error {
	if state.completed {
		return nil
	}
	state.completed = true
	output := make([]any, 0, state.nextOutput)
	if state.textOpened {
		itemID := responseItemID(state.id, "msg")
		if err := state.emit(writer, "response.output_text.done", map[string]any{
			"type": "response.output_text.done", "output_index": 0,
			"content_index": 0, "item_id": itemID, "text": state.text.String(),
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id": itemID, "type": "message", "status": responseItemStatus(state.finishReason),
			"role": "assistant", "content": []any{map[string]any{
				"type": "output_text", "text": state.text.String(), "annotations": []any{},
			}},
		}
		if err := state.emit(writer, "response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": 0, "item": item,
		}); err != nil {
			return err
		}
		output = append(output, item)
	}
	for index := 0; index < len(state.tools); index++ {
		tool := state.tools[index]
		if tool == nil {
			continue
		}
		arguments := tool.Arguments.String()
		if err := state.emit(writer, "response.function_call_arguments.done", map[string]any{
			"type":    "response.function_call_arguments.done",
			"item_id": responseItemID(tool.ID, "fc"), "output_index": tool.Output,
			"name": tool.Name, "arguments": arguments,
		}); err != nil {
			return err
		}
		item := map[string]any{
			"id": responseItemID(tool.ID, "fc"), "type": "function_call",
			"status": "completed", "call_id": tool.ID,
			"name": tool.Name, "arguments": arguments,
		}
		if err := state.emit(writer, "response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": tool.Output, "item": item,
		}); err != nil {
			return err
		}
		output = append(output, item)
	}
	status := "completed"
	eventType := "response.completed"
	var incomplete any
	if state.finishReason == "length" {
		status = "incomplete"
		eventType = "response.incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	}
	response := state.response(status, output)
	response["incomplete_details"] = incomplete
	return state.emit(writer, eventType, map[string]any{
		"type": eventType, "response": response,
	})
}

func (state *responsesStreamState) response(status string, output any) map[string]any {
	response := map[string]any{
		"id": state.id, "object": "response", "created_at": state.createdAt,
		"status": status, "model": state.model, "output": output,
		"error": nil, "store": false,
	}
	if state.usage != nil {
		response["usage"] = state.usage
	}
	return response
}

func responsesUsage(value chatUsageEnvelope) map[string]any {
	return map[string]any{
		"input_tokens":  value.PromptTokens,
		"output_tokens": value.CompletionTokens,
		"total_tokens":  value.TotalTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens": value.PromptTokensDetails["cached_tokens"],
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": value.CompletionTokensDetails["reasoning_tokens"],
		},
	}
}

func (state *responsesStreamState) emit(writer io.Writer, eventType string, payload map[string]any) error {
	state.sequence++
	payload["sequence_number"] = state.sequence
	return writeNamedSSE(writer, eventType, payload)
}
