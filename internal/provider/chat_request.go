package provider // Provider 请求解析与协议转换。

import (
	"bytes"         // 复制、清理和比较字节数据。
	"encoding/json" // 解析和生成 JSON。
	"fmt"           // 生成带字段位置的错误。
	"slices"        // 查询和排序字符串切片。
	"strings"       // 清理模型名两侧空格。
)

// ChatRequest 是网关内部保留的 OpenAI Chat 请求视图。
type ChatRequest struct {
	Model               string          `json:"model"`                           // 客户端请求的模型名。
	Messages            []ChatMessage   `json:"messages"`                        // 聊天消息列表。
	Stream              bool            `json:"stream,omitempty"`                // 是否要求流式响应。
	MaxTokens           *int            `json:"max_tokens,omitempty"`            // 旧版最大输出 Token 参数。
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"` // 新版最大输出 Token 参数。
	Temperature         *float64        `json:"temperature,omitempty"`           // 输出随机性参数。
	TopP                *float64        `json:"top_p,omitempty"`                 // 核采样参数。
	Stop                json.RawMessage `json:"stop,omitempty"`                  // 原始停止词 JSON。
	rawBody             []byte          // 兼容协议转发时保留客户端未建模字段。
	extraFields         []string        // 原生协议用它明确拒绝无法转换的字段。
}

// ChatMessage 保留 OpenAI 消息的核心字段以及尚未建模的扩展字段名。
type ChatMessage struct {
	Role        string          `json:"role"`    // 消息角色。
	Content     json.RawMessage `json:"content"` // 原始消息内容 JSON。
	extraFields []string        // 当前消息中尚未建模的字段名。
}

// ParseChatRequest 只解析 Provider 路由和协议转换需要的字段。
// 原始 JSON 会被复制保存，避免兼容协议转发时意外丢失 tools 等扩展能力。
func ParseChatRequest(body []byte) (ChatRequest, error) { // 解析客户端提交的 OpenAI Chat 请求。
	var fields map[string]json.RawMessage                                  // 保存请求顶层全部字段。
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil { // 请求必须是合法 JSON 对象。
		return ChatRequest{}, ErrInvalidRequest // 返回无效请求错误。
	}

	var request ChatRequest                                // 保存已建模的请求字段。
	if err := json.Unmarshal(body, &request); err != nil { // 按 ChatRequest 结构解析请求。
		return ChatRequest{}, ErrInvalidRequest // 字段类型不合法。
	}
	request.rawBody = bytes.Clone(body)                              // 复制原始请求，供兼容协议原样转发。
	request.extraFields = unknownFields(fields, map[string]struct{}{ // 找出顶层未建模字段。
		"model": {}, "messages": {}, "stream": {}, "max_tokens": {}, // 已支持的顶层字段。
		"max_completion_tokens": {}, "temperature": {}, "top_p": {}, "stop": {}, // 已支持的顶层字段。
	})

	var encodedMessages []map[string]json.RawMessage // 保存每条消息的全部原始字段。
	if rawMessages, ok := fields["messages"]; ok {   // 请求中存在 messages 字段。
		if err := json.Unmarshal(rawMessages, &encodedMessages); err != nil { // messages 必须是对象数组。
			return ChatRequest{}, ErrInvalidRequest // 消息结构无效。
		}
	}
	for index := range request.Messages { // 逐条记录消息中的扩展字段。
		request.Messages[index].extraFields = unknownFields(encodedMessages[index], map[string]struct{}{ // 找出该消息未建模字段。
			"role": {}, "content": {}, // 当前已建模的消息字段。
		})
	}
	if _, err := decodeStop(request.Stop); err != nil { // 校验 stop 是否为字符串或字符串数组。
		return ChatRequest{}, err // stop 格式错误。
	}
	return request, nil // 返回解析后的请求。
}

// RequiredCapabilities 从请求内容推导路由候选必须具备的能力。
func (request ChatRequest) RequiredCapabilities() ([]Capability, error) { // 判断请求需要文本、图像或工具能力。
	required := map[Capability]struct{}{CapabilityText: {}}                                                            // 所有聊天请求默认需要文本能力。
	if hasAnyField(request.extraFields, "tools", "tool_choice", "functions", "function_call", "parallel_tool_calls") { // 顶层包含工具调用字段。
		required[CapabilityTools] = struct{}{} // 要求 Provider 支持工具调用。
	}
	for _, message := range request.Messages { // 检查每一条消息。
		usesTools := message.Role == "developer" || message.Role == "tool"                                       // developer 或 tool 角色视为使用工具能力。
		usesTools = usesTools || hasAnyField(message.extraFields, "tool_calls", "tool_call_id", "function_call") // 消息包含工具相关字段。
		if usesTools {                                                                                           // 当前消息使用了工具能力。
			required[CapabilityTools] = struct{}{} // 加入工具能力要求。
		}
		toolOnlyMessage := bytes.Equal(bytes.TrimSpace(message.Content), []byte("null")) && // content 为 null。
			hasAnyField(message.extraFields, "tool_calls", "function_call") // 同时包含工具调用字段。
		if toolOnlyMessage { // assistant 仅发起工具调用而没有普通内容。
			continue // 不再解析 content。
		}
		parts, err := decodeContent(message.Content) // 将文本或内容块解析成统一内容列表。
		if err != nil {                              // content 格式错误。
			return nil, err // 返回解析错误。
		}
		for _, part := range parts { // 检查每个内容块。
			if part.Type == "image_url" { // 内容块包含图片。
				required[CapabilityImage] = struct{}{} // 要求 Provider 支持图像输入。
			}
		}
	}

	capabilities := make([]Capability, 0, len(required))                                        // 创建稳定顺序的能力结果。
	for _, capability := range []Capability{CapabilityText, CapabilityImage, CapabilityTools} { // 按固定顺序遍历能力。
		if _, ok := required[capability]; ok { // 当前能力确实被请求使用。
			capabilities = append(capabilities, capability) // 加入返回列表。
		}
	}
	return capabilities, nil // 返回请求所需能力。
}

// HasField 用于原生 Adapter 判断消息是否包含尚未建模的 OpenAI 字段。
func (message ChatMessage) HasField(name string) bool { // 判断消息中是否存在指定扩展字段。
	return hasAnyField(message.extraFields, name) // 在扩展字段列表中查询。
}

func hasAnyField(fields []string, names ...string) bool { // 判断字段列表是否包含任意目标名称。
	for _, field := range fields { // 遍历已有字段。
		if slices.Contains(names, field) { // 当前字段属于目标名称之一。
			return true // 找到后立即返回。
		}
	}
	return false // 没有找到目标字段。
}

func unknownFields(fields map[string]json.RawMessage, known map[string]struct{}) []string { // 找出 JSON 中未被当前结构建模的字段。
	unknown := make([]string, 0) // 保存未知字段名。
	for name := range fields {   // 遍历 JSON 中全部字段。
		if _, ok := known[name]; !ok { // 当前字段不在已知集合。
			unknown = append(unknown, name) // 加入未知字段列表。
		}
	}
	slices.Sort(unknown) // 排序以保证错误和测试结果稳定。
	return unknown       // 返回未知字段名。
}

func decodeChatRequest(input ChatInput) (ChatRequest, error) { // 生成发送给 Adapter 的基础聊天请求。
	request := input.Request       // 复制已经解析的请求。
	if input.ModelOverride != "" { // 路由指定了真实上游模型。
		request.Model = input.ModelOverride // 替换客户端模型名。
	}
	request.Model = strings.TrimSpace(request.Model)       // 清理最终模型名。
	if request.Model == "" || len(request.Messages) == 0 { // 模型为空或没有消息。
		return ChatRequest{}, ErrInvalidRequest // 请求无法发送给上游。
	}
	return request, nil // 返回可用请求。
}

// decodeNativeChatRequest 对原生协议采用严格转换：不能表达的字段必须报错，不能静默丢弃。
func decodeNativeChatRequest(input ChatInput) (ChatRequest, error) { // 为非 OpenAI 原生协议准备严格请求。
	request, err := decodeChatRequest(input) // 先处理模型覆盖和基础校验。
	if err != nil {                          // 基础请求无效。
		return ChatRequest{}, err // 返回原错误。
	}
	if len(request.extraFields) > 0 { // 请求包含原生协议尚未支持的顶层字段。
		return ChatRequest{}, fmt.Errorf("%w: request field %q", ErrUnsupportedCapability, request.extraFields[0]) // 明确报出首个不支持字段。
	}
	for index, message := range request.Messages { // 逐条检查消息是否可以转换。
		if len(message.extraFields) > 0 { // 消息包含尚未支持的扩展字段。
			return ChatRequest{}, fmt.Errorf( // 返回具体消息位置和字段。
				"%w: messages[%d] field %q", // 错误格式。
				ErrUnsupportedCapability,    // 归类为能力不支持。
				index,                       // 消息下标。
				message.extraFields[0],      // 首个无法转换的字段。
			)
		}
		switch message.Role { // 检查原生协议支持的角色。
		case "system", "user", "assistant": // 当前原生转换支持的角色。
		default: // 其他角色无法安全转换。
			return ChatRequest{}, fmt.Errorf( // 返回具体消息角色错误。
				"%w: messages[%d] role %q", // 错误格式。
				ErrUnsupportedCapability,   // 归类为能力不支持。
				index,                      // 消息下标。
				message.Role,               // 不支持的角色。
			)
		}
	}
	return request, nil // 返回可转换的原生请求。
}

// compatibleRequestBody 尽量原样转发请求，只在路由要求时替换 model。
func compatibleRequestBody(input ChatInput) ([]byte, error) { // 生成 OpenAI 兼容 Provider 的请求体。
	if input.ModelOverride == "" { // 不需要替换模型名。
		return bytes.Clone(input.Request.rawBody), nil // 原样复制并转发客户端 JSON。
	}
	var payload map[string]json.RawMessage                                                    // 保存请求全部原始字段。
	if err := json.Unmarshal(input.Request.rawBody, &payload); err != nil || payload == nil { // 原始请求必须仍是合法对象。
		return nil, ErrInvalidRequest // 无法构造兼容请求。
	}
	model := strings.TrimSpace(input.ModelOverride) // 清理路由指定的模型名。
	if model == "" {                                // 覆盖模型不能为空。
		return nil, ErrInvalidRequest // 返回无效请求。
	}
	encodedModel, err := json.Marshal(model) // 将模型名编码为 JSON 字符串。
	if err != nil {                          // 理论上的编码失败。
		return nil, ErrInvalidRequest // 统一按无效请求处理。
	}
	payload["model"] = encodedModel          // 只替换原始请求中的 model 字段。
	mappedBody, err := json.Marshal(payload) // 重新生成完整请求 JSON。
	if err != nil {                          // 请求对象编码失败。
		return nil, ErrInvalidRequest // 返回无效请求。
	}
	return mappedBody, nil // 返回替换模型后的请求体。
}

func compatibleStreamRequestBody(input ChatInput, includeUsage bool) ([]byte, error) { // 生成 OpenAI 兼容流式请求体。
	body, err := compatibleRequestBody(input) // 先生成普通兼容请求体。
	if err != nil {                           // 请求体生成失败。
		return nil, err // 返回原错误。
	}
	var payload map[string]json.RawMessage                                   // 保存完整请求对象。
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil { // 请求体必须是合法 JSON 对象。
		return nil, ErrInvalidRequest // 无法设置 stream 字段。
	}
	payload["stream"] = json.RawMessage("true") // 强制要求上游返回流式响应。
	if includeUsage {
		var streamOptions map[string]json.RawMessage
		rawOptions := bytes.TrimSpace(payload["stream_options"])
		if len(rawOptions) > 0 && !bytes.Equal(rawOptions, []byte("null")) {
			if err := json.Unmarshal(rawOptions, &streamOptions); err != nil || streamOptions == nil {
				return nil, ErrInvalidRequest
			}
		}
		if streamOptions == nil {
			streamOptions = make(map[string]json.RawMessage)
		}
		streamOptions["include_usage"] = json.RawMessage("true")
		encodedOptions, err := json.Marshal(streamOptions)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		payload["stream_options"] = encodedOptions
	}
	streamBody, err := json.Marshal(payload) // 重新编码流式请求。
	if err != nil {                          // JSON 编码失败。
		return nil, ErrInvalidRequest // 返回无效请求。
	}
	return streamBody, nil // 返回最终流式请求体。
}

func requestedMaxTokens(request ChatRequest, fallback int) int { // 取得请求最终使用的最大输出 Token 数。
	if request.MaxCompletionTokens != nil { // 优先使用新版字段。
		return *request.MaxCompletionTokens // 返回 max_completion_tokens。
	}
	if request.MaxTokens != nil { // 未提供新版字段但提供了旧版字段。
		return *request.MaxTokens // 返回 max_tokens。
	}
	return fallback // 两个字段都未提供时使用 Adapter 默认值。
}

// decodeStop 同时接受 OpenAI 的单字符串和字符串数组形式。
func decodeStop(raw json.RawMessage) ([]string, error) { // 将 stop 统一解析成字符串切片。
	raw = bytes.TrimSpace(raw)                             // 清理 JSON 两侧空白。
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) { // stop 未提供或为 null。
		return nil, nil // 表示没有停止词。
	}
	var values []string // 保存最终停止词列表。
	if raw[0] == '"' {  // stop 是单个 JSON 字符串。
		var value string                                    // 保存解析后的字符串。
		if err := json.Unmarshal(raw, &value); err != nil { // 解析单字符串。
			return nil, ErrInvalidRequest // 字符串格式错误。
		}
		return []string{value}, nil // 转换成单元素切片。
	}
	if err := json.Unmarshal(raw, &values); err != nil { // 否则按字符串数组解析。
		return nil, ErrInvalidRequest // 数组格式或元素类型错误。
	}
	return values, nil // 返回停止词列表。
}
