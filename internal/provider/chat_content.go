package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type chatContentPart struct {
	Type     string
	Text     string
	ImageURL string
}

// decodeContent 用于 OpenAI 兼容协议，允许保留当前已认识的内容结构。
func decodeContent(raw json.RawMessage) ([]chatContentPart, error) {
	return decodeContentParts(raw, false)
}

// decodeNativeContent 会拒绝内容块中的未知字段，防止协议转换时静默降级。
func decodeNativeContent(raw json.RawMessage) ([]chatContentPart, error) {
	return decodeContentParts(raw, true)
}

func decodeContentParts(raw json.RawMessage, rejectUnknown bool) ([]chatContentPart, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, ErrInvalidRequest
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, ErrInvalidRequest
		}
		return []chatContentPart{{Type: "text", Text: text}}, nil
	}

	var encodedParts []json.RawMessage
	if err := json.Unmarshal(raw, &encodedParts); err != nil || len(encodedParts) == 0 {
		return nil, ErrInvalidRequest
	}
	parts := make([]chatContentPart, 0, len(encodedParts))
	for _, encodedPart := range encodedParts {
		var part struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(encodedPart, &part); err != nil {
			return nil, ErrInvalidRequest
		}
		if rejectUnknown {
			if err := rejectUnknownContentFields(encodedPart, part.Type); err != nil {
				return nil, err
			}
		}
		switch part.Type {
		case "text":
			parts = append(parts, chatContentPart{Type: "text", Text: part.Text})
		case "image_url":
			if strings.TrimSpace(part.ImageURL.URL) == "" {
				return nil, ErrInvalidRequest
			}
			parts = append(parts, chatContentPart{Type: "image_url", ImageURL: part.ImageURL.URL})
		default:
			return nil, fmt.Errorf("%w: content part %q", ErrUnsupportedCapability, part.Type)
		}
	}
	return parts, nil
}

func rejectUnknownContentFields(encoded json.RawMessage, partType string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || fields == nil {
		return ErrInvalidRequest
	}
	known := map[string]struct{}{"type": {}}
	switch partType {
	case "text":
		known["text"] = struct{}{}
	case "image_url":
		known["image_url"] = struct{}{}
	}
	if extra := unknownFields(fields, known); len(extra) > 0 {
		return fmt.Errorf("%w: content part field %q", ErrUnsupportedCapability, extra[0])
	}
	if partType != "image_url" {
		return nil
	}

	var imageFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["image_url"], &imageFields); err != nil || imageFields == nil {
		return ErrInvalidRequest
	}
	if extra := unknownFields(imageFields, map[string]struct{}{"url": {}}); len(extra) > 0 {
		return fmt.Errorf("%w: image_url field %q", ErrUnsupportedCapability, extra[0])
	}
	return nil
}

// textContent 将消息收敛为纯文本；不支持图片的原生协议会在这里明确失败。
func textContent(raw json.RawMessage) (string, error) {
	parts, err := decodeNativeContent(raw)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, part := range parts {
		if part.Type != "text" {
			return "", fmt.Errorf("%w: image content", ErrUnsupportedCapability)
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

// base64Image 只接受内嵌 data URL，供要求直接上传图片字节的协议使用。
func base64Image(raw string) (string, string, error) {
	mediaType, data, ok := parseDataURL(raw)
	if ok {
		return mediaType, data, nil
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "data:") {
		return "", "", ErrInvalidRequest
	}
	return "", "", fmt.Errorf("%w: remote image url", ErrUnsupportedCapability)
}

// parseDataURL 校验媒体类型和 Base64 数据，但不负责判断厂商支持哪些图片格式。
func parseDataURL(raw string) (mediaType string, data string, ok bool) {
	prefix, encoded, found := strings.Cut(raw, ",")
	if !found || !strings.HasPrefix(prefix, "data:") || !strings.HasSuffix(prefix, ";base64") {
		return "", "", false
	}
	mediaType = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
	if mediaType == "" {
		return "", "", false
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return "", "", false
	}
	return mediaType, encoded, true
}
