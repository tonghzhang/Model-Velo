package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

type nativeStreamResult struct {
	Chunks [][]byte
	Done   bool
}

type nativeStreamMapper func(event string, data []byte) (nativeStreamResult, error)

const nativeStreamEOF = "__model_velo_eof__"

type convertedStreamBody struct {
	reader *io.PipeReader
	source io.ReadCloser
	once   sync.Once
}

func (body *convertedStreamBody) Read(data []byte) (int, error) {
	return body.reader.Read(data)
}

func (body *convertedStreamBody) Close() error {
	var closeErr error
	body.once.Do(func() {
		closeErr = body.source.Close()
		_ = body.reader.Close()
	})
	return closeErr
}

func newMappedSSEStream(
	body io.ReadCloser,
	mapper nativeStreamMapper,
) (*ChatEventStream, error) {
	if body == nil || mapper == nil {
		return nil, ErrInvalidStream
	}
	reader, writer := io.Pipe()
	converted := &convertedStreamBody{reader: reader, source: body}
	go mapSSEStream(body, writer, mapper)
	stream, err := newChatEventStream(converted)
	if err != nil {
		converted.Close()
		return nil, err
	}
	return stream, nil
}

func newMappedJSONLinesStream(
	body io.ReadCloser,
	mapper nativeStreamMapper,
) (*ChatEventStream, error) {
	if body == nil || mapper == nil {
		return nil, ErrInvalidStream
	}
	reader, writer := io.Pipe()
	converted := &convertedStreamBody{reader: reader, source: body}
	go mapJSONLinesStream(body, writer, mapper)
	stream, err := newChatEventStream(converted)
	if err != nil {
		converted.Close()
		return nil, err
	}
	return stream, nil
}

func newMappedAWSStream(
	body io.ReadCloser,
	mapper nativeStreamMapper,
) (*ChatEventStream, error) {
	if body == nil || mapper == nil {
		return nil, ErrInvalidStream
	}
	reader, writer := io.Pipe()
	converted := &convertedStreamBody{reader: reader, source: body}
	go mapAWSStream(body, writer, mapper)
	stream, err := newChatEventStream(converted)
	if err != nil {
		converted.Close()
		return nil, err
	}
	return stream, nil
}

func mapSSEStream(body io.ReadCloser, writer *io.PipeWriter, mapper nativeStreamMapper) {
	defer body.Close()
	reader := bufio.NewReaderSize(body, streamReaderBufferBytes)
	for {
		event, data, activity, err := readNativeSSEEvent(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if finishNativeStream(writer, mapper) {
					return
				}
				err = ErrInvalidStream
			}
			_ = writer.CloseWithError(err)
			return
		}
		if activity && len(data) == 0 {
			if _, err := writer.Write([]byte(": upstream-activity\n\n")); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			continue
		}
		result, err := mapper(event, data)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if len(result.Chunks) == 0 && !result.Done {
			if _, err := writer.Write([]byte(": upstream-activity\n\n")); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		}
		if err := writeMappedChunks(writer, result); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if result.Done {
			_ = writer.Close()
			return
		}
	}
}

func mapJSONLinesStream(body io.ReadCloser, writer *io.PipeWriter, mapper nativeStreamMapper) {
	defer body.Close()
	reader := bufio.NewReaderSize(body, streamReaderBufferBytes)
	for {
		line, err := readStreamLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if finishNativeStream(writer, mapper) {
					return
				}
				err = ErrInvalidStream
			}
			_ = writer.CloseWithError(err)
			return
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		result, err := mapper("", line)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if err := writeMappedChunks(writer, result); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if result.Done {
			_ = writer.Close()
			return
		}
	}
}

func mapAWSStream(body io.ReadCloser, writer *io.PipeWriter, mapper nativeStreamMapper) {
	defer body.Close()
	decoder := eventstream.NewDecoder()
	payload := make([]byte, 0, streamReaderBufferBytes)
	for {
		limited := &eventReadLimiter{
			reader: body, remaining: maxStreamEventBytes,
		}
		message, err := decoder.Decode(limited, payload)
		if err != nil {
			if limited.exceeded {
				err = ErrResponseTooLarge
			} else if errors.Is(err, io.EOF) {
				if finishNativeStream(writer, mapper) {
					return
				}
				err = ErrInvalidStream
			}
			_ = writer.CloseWithError(err)
			return
		}
		payload = message.Payload[:0]
		messageType := eventstreamHeader(message.Headers, ":message-type")
		eventType := eventstreamHeader(message.Headers, ":event-type")
		if messageType == "exception" {
			_ = writer.CloseWithError(ErrInvalidStream)
			return
		}
		result, err := mapper(eventType, message.Payload)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if len(result.Chunks) == 0 && !result.Done {
			if _, err := writer.Write([]byte(": upstream-activity\n\n")); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		}
		if err := writeMappedChunks(writer, result); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		if result.Done {
			_ = writer.Close()
			return
		}
	}
}

func finishNativeStream(writer *io.PipeWriter, mapper nativeStreamMapper) bool {
	result, err := mapper(nativeStreamEOF, nil)
	if err != nil || !result.Done {
		return false
	}
	if err := writeMappedChunks(writer, result); err != nil {
		_ = writer.CloseWithError(err)
		return true
	}
	_ = writer.Close()
	return true
}

type eventReadLimiter struct {
	reader    io.Reader
	remaining int
	exceeded  bool
}

func (reader *eventReadLimiter) Read(data []byte) (int, error) {
	if reader.remaining <= 0 {
		reader.exceeded = true
		return 0, ErrResponseTooLarge
	}
	if len(data) > reader.remaining {
		data = data[:reader.remaining]
	}
	count, err := reader.reader.Read(data)
	reader.remaining -= count
	return count, err
}

func eventstreamHeader(headers eventstream.Headers, name string) string {
	value := headers.Get(name)
	if value == nil {
		return ""
	}
	stringValue, ok := value.(eventstream.StringValue)
	if !ok {
		return ""
	}
	return string(stringValue)
}

func writeMappedChunks(writer io.Writer, result nativeStreamResult) error {
	for _, chunk := range result.Chunks {
		if err := validateCompatibleChatChunk(chunk); err != nil {
			return err
		}
		if _, err := writer.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err := writer.Write(chunk); err != nil {
			return err
		}
		if _, err := writer.Write([]byte("\n\n")); err != nil {
			return err
		}
	}
	if result.Done {
		_, err := writer.Write([]byte("data: [DONE]\n\n"))
		return err
	}
	return nil
}

func readNativeSSEEvent(reader *bufio.Reader) (string, []byte, bool, error) {
	var event string
	var data bytes.Buffer
	activity := false
	for {
		line, err := readStreamLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) && data.Len() > 0 {
				return event, bytes.Clone(data.Bytes()), true, nil
			}
			return "", nil, activity, err
		}
		activity = true
		if len(line) == 0 {
			if data.Len() == 0 {
				return event, nil, activity, nil
			}
			return event, bytes.Clone(data.Bytes()), activity, nil
		}
		if line[0] == ':' {
			continue
		}
		field, value := splitStreamField(line)
		switch string(field) {
		case "event":
			if !utf8.Valid(value) {
				return "", nil, activity, ErrInvalidStream
			}
			event = string(value)
		case "data":
			additional := len(value)
			if data.Len() > 0 {
				additional++
			}
			if data.Len()+additional > maxStreamEventBytes {
				return "", nil, activity, ErrResponseTooLarge
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(value)
		}
	}
}

type openAIStreamChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *completionUsage     `json:"usage,omitempty"`
}

type openAIStreamChoice struct {
	Index        int               `json:"index"`
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason *string           `json:"finish_reason,omitempty"`
}

type openAIStreamDelta struct {
	Role             string                      `json:"role,omitempty"`
	Content          *string                     `json:"content,omitempty"`
	ReasoningContent *string                     `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIStreamToolCallDelta `json:"tool_calls,omitempty"`
}

type openAIStreamToolCallDelta struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type,omitempty"`
	Function openAIStreamFunctionDelta `json:"function"`
}

type openAIStreamFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func encodeStreamChunk(
	id string,
	model string,
	delta openAIStreamDelta,
	finishReason string,
	usage *completionUsage,
) ([]byte, error) {
	if id == "" {
		id = "chatcmpl-upstream"
	}
	if usage != nil && usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	choice := openAIStreamChoice{Delta: delta}
	if finishReason != "" {
		choice.FinishReason = &finishReason
	}
	chunk := openAIStreamChunk{
		ID: id, Object: "chat.completion.chunk", Created: time.Now().Unix(),
		Model: model, Choices: []openAIStreamChoice{choice}, Usage: usage,
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return nil, ErrInvalidStream
	}
	return encoded, nil
}

func streamResult(
	id string,
	model string,
	delta openAIStreamDelta,
	finishReason string,
	usage *completionUsage,
	done bool,
) (nativeStreamResult, error) {
	chunk, err := encodeStreamChunk(id, model, delta, finishReason, usage)
	if err != nil {
		return nativeStreamResult{}, err
	}
	return nativeStreamResult{Chunks: [][]byte{chunk}, Done: done}, nil
}

func invalidNativeStreamJSON(data []byte, target interface{}) error {
	if !utf8.Valid(data) || !json.Valid(data) {
		return ErrInvalidStream
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidStream, err)
	}
	return nil
}
