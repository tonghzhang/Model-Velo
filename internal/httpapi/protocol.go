package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"model-velo/internal/provider"
)

const protocolStateKey = "model-velo.protocol-state"

type responseProtocol interface {
	EncodeResponse([]byte) ([]byte, error)
	WriteStream(io.Writer, provider.ChatStreamEvent) error
}

func (h chatHandler) serveProtocol(
	c *gin.Context,
	convert func([]byte, string) ([]byte, responseProtocol, error),
) {
	if !hasJSONContentType(c.GetHeader("Content-Type")) {
		writeAPIError(
			c,
			http.StatusUnsupportedMediaType,
			"request Content-Type must be application/json",
			"invalid_request_error",
			nil,
			"invalid_content_type",
		)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatRequestBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			writeAPIError(
				c,
				http.StatusRequestEntityTooLarge,
				"request body exceeds the size limit",
				"invalid_request_error",
				nil,
				"request_too_large",
			)
			return
		}
		writeAPIError(
			c,
			http.StatusBadRequest,
			"request body could not be read",
			"invalid_request_error",
			nil,
			"invalid_request_body",
		)
		return
	}
	chatBody, protocol, err := convert(body, requestIDFromContext(c.Request.Context()))
	if err != nil {
		writeAPIError(
			c,
			http.StatusBadRequest,
			"request contains fields that cannot be represented by the configured gateway",
			"invalid_request_error",
			nil,
			"unsupported_protocol_feature",
		)
		return
	}
	c.Set(protocolStateKey, protocol)
	c.Request.Body = io.NopCloser(bytes.NewReader(chatBody))
	c.Request.ContentLength = int64(len(chatBody))
	h.complete(c)
}

func protocolFromContext(c *gin.Context) responseProtocol {
	value, exists := c.Get(protocolStateKey)
	if !exists {
		return nil
	}
	protocol, _ := value.(responseProtocol)
	return protocol
}

func encodeProtocolResponse(c *gin.Context, chatBody []byte) ([]byte, error) {
	protocol := protocolFromContext(c)
	if protocol == nil {
		return chatBody, nil
	}
	return protocol.EncodeResponse(chatBody)
}

func writeProtocolStream(
	c *gin.Context,
	writer io.Writer,
	event provider.ChatStreamEvent,
) error {
	protocol := protocolFromContext(c)
	if protocol == nil {
		return writeChatStreamEvent(writer, event)
	}
	return protocol.WriteStream(writer, event)
}

func marshalObject(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, provider.ErrInvalidRequest
	}
	return encoded, nil
}

func unknownJSONField(
	fields map[string]json.RawMessage,
	allowed ...string,
) string {
	known := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		known[name] = struct{}{}
	}
	for name := range fields {
		if _, ok := known[name]; !ok {
			return name
		}
	}
	return ""
}

func writeNamedSSE(writer io.Writer, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return provider.ErrInvalidStream
	}
	frame := make([]byte, 0, len(encoded)+len(eventType)+18)
	frame = append(frame, "event: "...)
	frame = append(frame, eventType...)
	frame = append(frame, '\n')
	frame = append(frame, "data: "...)
	frame = append(frame, encoded...)
	frame = append(frame, '\n', '\n')
	written, err := writer.Write(frame)
	if err == nil && written != len(frame) {
		return io.ErrShortWrite
	}
	return err
}
