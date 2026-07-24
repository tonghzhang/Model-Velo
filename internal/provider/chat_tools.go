package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatTool 是 OpenAI Chat function tool 的稳定内部表示。
type ChatTool struct {
	Type     string                 `json:"type"`
	Function ChatFunctionDefinition `json:"function"`
}

// ChatFunctionDefinition 保存可跨协议映射的函数定义。
type ChatFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ChatToolCall 表示 Assistant 发起的一次函数调用。
type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatFunctionCall `json:"function"`
}

// ChatFunctionCall 保存函数名和 OpenAI 要求的 JSON 字符串参数。
type ChatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responseFormat struct {
	Type        string
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      *bool
}

type toolChoice struct {
	Mode string
	Name string
}

type nativeRequestSupport struct {
	tools       bool
	structured  bool
	seed        bool
	penalties   bool
	candidates  bool
	logprobs    bool
	reasoning   bool
	developer   bool
	messageName bool
	toolName    bool
}

func (support nativeRequestSupport) validate(request ChatRequest) error {
	usesTools := len(request.Tools) > 0 ||
		valuePresent(request.ToolChoice)
	if usesTools && !support.tools {
		return unsupportedRequestField("tools")
	}
	if valuePresent(request.ResponseFormat) && !support.structured {
		return unsupportedRequestField("response_format")
	}
	if request.Seed != nil && !support.seed {
		return unsupportedRequestField("seed")
	}
	if (request.FrequencyPenalty != nil || request.PresencePenalty != nil) && !support.penalties {
		return unsupportedRequestField("frequency_penalty")
	}
	if request.N != nil && *request.N != 1 && !support.candidates {
		return unsupportedRequestField("n")
	}
	usesLogprobs := request.Logprobs != nil && *request.Logprobs
	if (usesLogprobs || request.TopLogprobs != nil) && !support.logprobs {
		return unsupportedRequestField("logprobs")
	}
	effort := strings.ToLower(strings.TrimSpace(request.ReasoningEffort))
	if effort != "" && effort != "none" && !support.reasoning {
		return unsupportedRequestField("reasoning_effort")
	}
	return nil
}

func unsupportedRequestField(name string) error {
	return fmt.Errorf("%w: request field %q", ErrUnsupportedCapability, name)
}

func validateToolRequest(request ChatRequest) error {
	toolNames := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		if strings.TrimSpace(tool.Type) != "function" {
			return fmt.Errorf("%w: only function tools are supported", ErrInvalidRequest)
		}
		if strings.TrimSpace(tool.Function.Name) == "" {
			return fmt.Errorf("%w: tool function name is required", ErrInvalidRequest)
		}
		if _, exists := toolNames[tool.Function.Name]; exists {
			return fmt.Errorf("%w: duplicate tool function name %q", ErrInvalidRequest, tool.Function.Name)
		}
		toolNames[tool.Function.Name] = struct{}{}
		if err := validateJSONObject(tool.Function.Parameters, true); err != nil {
			return fmt.Errorf("%w: tool parameters must be a JSON object", ErrInvalidRequest)
		}
	}
	choice, err := decodeToolChoice(request.ToolChoice)
	if err != nil {
		return err
	}
	if choice.Mode != "" && choice.Mode != "none" && len(request.Tools) == 0 {
		return fmt.Errorf("%w: tool_choice requires tools", ErrInvalidRequest)
	}
	if choice.Mode == "function" {
		if _, exists := toolNames[choice.Name]; !exists {
			return fmt.Errorf("%w: tool_choice names an unknown function", ErrInvalidRequest)
		}
	}
	pendingCalls := make(map[string]struct{})
	for index, message := range request.Messages {
		if message.ToolCallID != "" && message.Role != "tool" {
			return fmt.Errorf("%w: messages[%d] tool_call_id requires tool role", ErrInvalidRequest, index)
		}
		if message.Role == "tool" && strings.TrimSpace(message.ToolCallID) == "" {
			return fmt.Errorf("%w: messages[%d] tool_call_id is required", ErrInvalidRequest, index)
		}
		if len(message.ToolCalls) > 0 && message.Role != "assistant" {
			return fmt.Errorf("%w: messages[%d] tool_calls require assistant role", ErrInvalidRequest, index)
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" ||
				strings.TrimSpace(call.Type) != "function" ||
				strings.TrimSpace(call.Function.Name) == "" {
				return fmt.Errorf("%w: messages[%d] contains an invalid tool call", ErrInvalidRequest, index)
			}
			if err := validateJSONStringObject(call.Function.Arguments); err != nil {
				return fmt.Errorf("%w: messages[%d] tool arguments must be a JSON object", ErrInvalidRequest, index)
			}
			if _, exists := pendingCalls[call.ID]; exists {
				return fmt.Errorf("%w: duplicate tool call id %q", ErrInvalidRequest, call.ID)
			}
			pendingCalls[call.ID] = struct{}{}
		}
		if message.Role == "tool" {
			if _, exists := pendingCalls[message.ToolCallID]; !exists {
				return fmt.Errorf(
					"%w: messages[%d] references an unknown tool call",
					ErrInvalidRequest,
					index,
				)
			}
			if message.Name != "" {
				name := priorToolName(request.Messages, index, message.ToolCallID)
				if name == "" || name != message.Name {
					return fmt.Errorf(
						"%w: messages[%d] tool name does not match tool_call_id",
						ErrInvalidRequest,
						index,
					)
				}
			}
			delete(pendingCalls, message.ToolCallID)
		}
	}
	return nil
}

func validateGenerationFields(request ChatRequest) error {
	for name, value := range map[string]*int{
		"max_tokens":            request.MaxTokens,
		"max_completion_tokens": request.MaxCompletionTokens,
		"n":                     request.N,
	} {
		if value != nil && *value < 1 {
			return fmt.Errorf("%w: %s must be positive", ErrInvalidRequest, name)
		}
	}
	if request.Temperature != nil &&
		(*request.Temperature < 0 || *request.Temperature > 2) {
		return fmt.Errorf("%w: temperature must be between 0 and 2", ErrInvalidRequest)
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return fmt.Errorf("%w: top_p must be between 0 and 1", ErrInvalidRequest)
	}
	for name, value := range map[string]*float64{
		"frequency_penalty": request.FrequencyPenalty,
		"presence_penalty":  request.PresencePenalty,
	} {
		if value != nil && (*value < -2 || *value > 2) {
			return fmt.Errorf("%w: %s must be between -2 and 2", ErrInvalidRequest, name)
		}
	}
	if request.TopLogprobs != nil {
		if *request.TopLogprobs < 0 || *request.TopLogprobs > 20 {
			return fmt.Errorf("%w: top_logprobs must be between 0 and 20", ErrInvalidRequest)
		}
		if request.Logprobs == nil || !*request.Logprobs {
			return fmt.Errorf("%w: top_logprobs requires logprobs=true", ErrInvalidRequest)
		}
	}
	return nil
}

func decodeToolChoice(raw json.RawMessage) (toolChoice, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return toolChoice{}, nil
	}
	if raw[0] == '"' {
		var mode string
		if err := json.Unmarshal(raw, &mode); err != nil {
			return toolChoice{}, ErrInvalidRequest
		}
		mode = strings.ToLower(strings.TrimSpace(mode))
		switch mode {
		case "none", "auto", "required":
			return toolChoice{Mode: mode}, nil
		default:
			return toolChoice{}, fmt.Errorf("%w: unsupported tool_choice %q", ErrInvalidRequest, mode)
		}
	}

	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil ||
		strings.TrimSpace(choice.Type) != "function" ||
		strings.TrimSpace(choice.Function.Name) == "" {
		return toolChoice{}, fmt.Errorf("%w: invalid tool_choice object", ErrInvalidRequest)
	}
	return toolChoice{Mode: "function", Name: strings.TrimSpace(choice.Function.Name)}, nil
}

func decodeResponseFormat(raw json.RawMessage) (responseFormat, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return responseFormat{}, nil
	}
	var value struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict,omitempty"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return responseFormat{}, ErrInvalidRequest
	}
	value.Type = strings.ToLower(strings.TrimSpace(value.Type))
	switch value.Type {
	case "text", "json_object":
		return responseFormat{Type: value.Type}, nil
	case "json_schema":
		if strings.TrimSpace(value.JSONSchema.Name) == "" {
			return responseFormat{}, fmt.Errorf("%w: response_format json_schema name is required", ErrInvalidRequest)
		}
		if err := validateJSONObject(value.JSONSchema.Schema, false); err != nil {
			return responseFormat{}, fmt.Errorf("%w: response_format schema must be a JSON object", ErrInvalidRequest)
		}
		return responseFormat{
			Type:        value.Type,
			Name:        strings.TrimSpace(value.JSONSchema.Name),
			Description: value.JSONSchema.Description,
			Schema:      bytes.Clone(value.JSONSchema.Schema),
			Strict:      value.JSONSchema.Strict,
		}, nil
	default:
		return responseFormat{}, fmt.Errorf("%w: unsupported response_format %q", ErrInvalidRequest, value.Type)
	}
}

func validateJSONStringObject(value string) error {
	raw := bytes.TrimSpace([]byte(value))
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	return validateJSONObject(raw, false)
}

func validateJSONObject(raw json.RawMessage, allowEmpty bool) error {
	raw = bytes.TrimSpace(raw)
	if allowEmpty && (len(raw) == 0 || bytes.Equal(raw, []byte("null"))) {
		return nil
	}
	if len(raw) == 0 || raw[0] != '{' {
		return ErrInvalidRequest
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return ErrInvalidRequest
	}
	return nil
}

func toolArgumentsObject(arguments string) (json.RawMessage, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return json.RawMessage("{}"), nil
	}
	if err := validateJSONStringObject(arguments); err != nil {
		return nil, err
	}
	return json.RawMessage(arguments), nil
}

func validateResponseToolCalls(calls []ChatToolCall) error {
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" ||
			strings.TrimSpace(call.Type) != "function" ||
			strings.TrimSpace(call.Function.Name) == "" ||
			validateJSONStringObject(call.Function.Arguments) != nil {
			return ErrInvalidResponse
		}
		if _, duplicate := seen[call.ID]; duplicate {
			return ErrInvalidResponse
		}
		seen[call.ID] = struct{}{}
	}
	return nil
}

func priorToolName(messages []ChatMessage, before int, toolCallID string) string {
	for index := before - 1; index >= 0; index-- {
		for _, call := range messages[index].ToolCalls {
			if call.ID == toolCallID {
				return call.Function.Name
			}
		}
	}
	return ""
}
