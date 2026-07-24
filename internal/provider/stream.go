package provider

import (
	"bufio"         // 带缓冲地读取流式响应
	"bytes"         // 拼接、比较和清理字节数据
	"encoding/json" // 校验和解析 JSON
	"errors"        // 判断具体错误类型
	"io"            // 读取、关闭响应体，以及 EOF
	"unicode/utf8"  // 拒绝不符合 SSE UTF-8 编码要求的数据
)

const (
	// 读取上游流时使用的缓冲区大小：64 KiB。
	streamReaderBufferBytes = 64 << 10

	// 单行 SSE 数据最大 1 MiB。
	maxStreamLineBytes = 1 << 20

	// 一个完整 SSE 事件最大 2 MiB。
	maxStreamEventBytes = 2 << 20
)

// ChatStreamEvent 表示读取到的一个有效流式事件。
type ChatStreamEvent struct {
	Data []byte // 普通事件中的 JSON 数据
	Done bool   // 是否收到 [DONE]
}

// ChatEventStream 管理一次上游流式响应。
type ChatEventStream struct {
	body     io.ReadCloser // 上游 HTTP 响应体
	reader   *bufio.Reader // 从响应体中逐行读取数据
	activity chan struct{} // 通知可靠性层上游仍在发送事件行或心跳
	finished bool          // 流是否已经结束
}

// newChatEventStream 根据上游响应体创建流读取器。
func newChatEventStream(body io.ReadCloser) (*ChatEventStream, error) {
	// 没有响应体时无法读取流。
	if body == nil {
		return nil, ErrInvalidStream
	}

	return &ChatEventStream{
		body:     body,
		reader:   bufio.NewReaderSize(body, streamReaderBufferBytes),
		activity: make(chan struct{}, 1),
	}, nil
}

// Next 读取并返回下一个有效 SSE 事件。
func (stream *ChatEventStream) Next() (ChatStreamEvent, error) {
	// 流不存在、已经关闭或已经结束时，不再返回事件。
	if stream == nil || stream.reader == nil || stream.finished {
		return ChatStreamEvent{}, io.EOF
	}

	// 一个 SSE 事件可能包含多行 data，这里统一收集。
	var data bytes.Buffer

	for {
		// 读取一整行 SSE 数据。
		line, err := readStreamLine(stream.reader)
		if err != nil {
			// 响应结束前已经收集到 data，仍然处理最后一个事件。
			if errors.Is(err, io.EOF) && data.Len() > 0 {
				return stream.decodeEvent(data.Bytes())
			}

			return ChatStreamEvent{}, err
		}
		stream.reportActivity()

		// 空行表示一个 SSE 事件结束。
		if len(line) == 0 {
			// 当前事件没有 data，跳过。
			if data.Len() == 0 {
				continue
			}

			return stream.decodeEvent(data.Bytes())
		}

		// 以冒号开头的是 SSE 注释或心跳，直接跳过。
		if line[0] == ':' {
			continue
		}

		// 拆出字段名和字段值。
		field, value := splitStreamField(line)

		// 当前只处理 data 字段。
		if !bytes.Equal(field, []byte("data")) {
			continue
		}

		// 计算加入这一行后事件的总大小。
		additional := len(value)
		if data.Len() > 0 {
			additional++ // 多行 data 之间补一个换行
		}

		if data.Len()+additional > maxStreamEventBytes {
			return ChatStreamEvent{}, ErrResponseTooLarge
		}

		if data.Len() > 0 {
			data.WriteByte('\n')
		}

		data.Write(value)
	}
}

// Activity reports bounded upstream activity without exposing heartbeat events to clients.
func (stream *ChatEventStream) Activity() <-chan struct{} {
	if stream == nil {
		return nil
	}
	return stream.activity
}

func (stream *ChatEventStream) reportActivity() {
	select {
	case stream.activity <- struct{}{}:
	default:
	}
}

// decodeEvent 把收集到的 data 转成 ChatStreamEvent。
func (stream *ChatEventStream) decodeEvent(data []byte) (ChatStreamEvent, error) {
	// 清理事件内容两端空白。
	data = bytes.TrimSpace(data)

	// [DONE] 表示整个流结束。
	if bytes.Equal(data, []byte("[DONE]")) {
		stream.finished = true
		return ChatStreamEvent{Done: true}, nil
	}

	// 检查是否符合最基本的 OpenAI Chat Chunk 格式。
	if err := validateCompatibleChatChunk(data); err != nil {
		return ChatStreamEvent{}, err
	}

	// 复制数据后返回，避免后续读取覆盖原内容。
	return ChatStreamEvent{
		Data: bytes.Clone(data),
	}, nil
}

// Close 关闭上游响应体。
func (stream *ChatEventStream) Close() error {
	// 流不存在或已经关闭时，不需要处理。
	if stream == nil || stream.body == nil {
		return nil
	}

	stream.finished = true

	// 保存响应体，然后清空内部引用。
	body := stream.body
	stream.body = nil
	stream.reader = nil

	return body.Close()
}

// readStreamLine 读取一整行，并限制单行最大长度。
func readStreamLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte

	for {
		// 读取到换行符；缓冲区不够时可能只返回一部分。
		fragment, err := reader.ReadSlice('\n')

		// 加上本次片段后超过单行限制。
		if len(line)+len(fragment) > maxStreamLineBytes+1 {
			return nil, ErrResponseTooLarge
		}

		line = append(line, fragment...)

		switch {
		case err == nil:
			// 已读取完整一行。
			return trimStreamLineEnding(line), nil

		case errors.Is(err, bufio.ErrBufferFull):
			// 当前缓冲区装不下整行，继续读取剩余部分。
			continue

		case errors.Is(err, io.EOF) && len(line) > 0:
			// 最后一行没有换行符，但仍然返回这一行。
			return trimStreamLineEnding(line), nil

		default:
			return nil, err
		}
	}
}

// trimStreamLineEnding 删除行末的换行符。
func trimStreamLineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return line
}

// splitStreamField 把 SSE 行拆成字段名和字段值。
func splitStreamField(line []byte) ([]byte, []byte) {
	// 按第一个冒号拆分。
	field, value, found := bytes.Cut(line, []byte{':'})
	if !found {
		return line, nil
	}

	// SSE 允许冒号后存在一个空格。
	value = bytes.TrimPrefix(value, []byte{' '})

	return field, value
}

// validateCompatibleChatChunk 检查事件是否是最基本的 OpenAI 流式响应格式。
func validateCompatibleChatChunk(data []byte) error {
	// SSE data 必须是 UTF-8 编码的合法 JSON。
	if !utf8.Valid(data) || !json.Valid(data) {
		return ErrInvalidStream
	}

	// 只读取校验需要的字段。
	var envelope struct {
		Error json.RawMessage `json:"error"`

		Choices []struct {
			Delta json.RawMessage `json:"delta"`
		} `json:"choices"`

		Usage json.RawMessage `json:"usage"`
	}

	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrInvalidStream
	}

	// 上游返回 error 字段时，不算正常聊天事件。
	if valuePresent(envelope.Error) {
		return ErrInvalidStream
	}

	// 没有 choices 时，只允许它是 usage 统计事件。
	if len(envelope.Choices) == 0 {
		if !jsonObject(envelope.Usage) {
			return ErrInvalidStream
		}

		return nil
	}

	// 每个 choice 都必须包含对象形式的 delta。
	for _, choice := range envelope.Choices {
		if !jsonObject(choice.Delta) {
			return ErrInvalidStream
		}
	}

	return nil
}

// valuePresent 判断 JSON 字段是否真正存在且不为 null。
func valuePresent(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)

	return len(raw) > 0 &&
		!bytes.Equal(raw, []byte("null"))
}

// jsonObject 判断 JSON 数据是否是一个对象。
func jsonObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)

	// 对象必须以 { 开头。
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}

	var object map[string]json.RawMessage

	// 必须能成功解析，并且结果不能是 nil。
	return json.Unmarshal(raw, &object) == nil &&
		object != nil
}
