package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type chatContentPart struct {
	Type        string
	Text        string
	ImageURL    string
	ImageDetail string
	AudioData   string
	AudioFormat string
	FileData    string
	FileID      string
	Filename    string
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
				URL    string `json:"url"`
				Detail string `json:"detail,omitempty"`
			} `json:"image_url"`
			InputAudio struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"input_audio"`
			File struct {
				FileData string `json:"file_data"`
				FileID   string `json:"file_id"`
				Filename string `json:"filename"`
			} `json:"file"`
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
		case "text", "input_text":
			parts = append(parts, chatContentPart{Type: "text", Text: part.Text})
		case "image_url":
			if strings.TrimSpace(part.ImageURL.URL) == "" {
				return nil, ErrInvalidRequest
			}
			detail := strings.ToLower(strings.TrimSpace(part.ImageURL.Detail))
			if detail != "" && detail != "auto" && detail != "low" && detail != "high" {
				return nil, ErrInvalidRequest
			}
			parts = append(parts, chatContentPart{
				Type:        "image_url",
				ImageURL:    part.ImageURL.URL,
				ImageDetail: detail,
			})
		case "input_audio":
			format := strings.ToLower(strings.TrimSpace(part.InputAudio.Format))
			if strings.TrimSpace(part.InputAudio.Data) == "" ||
				(format != "wav" && format != "mp3") ||
				!validBase64(part.InputAudio.Data) {
				return nil, ErrInvalidRequest
			}
			parts = append(parts, chatContentPart{
				Type:        "input_audio",
				AudioData:   part.InputAudio.Data,
				AudioFormat: format,
			})
		case "file":
			fileData := strings.TrimSpace(part.File.FileData)
			fileID := strings.TrimSpace(part.File.FileID)
			if (fileData == "") == (fileID == "") {
				return nil, ErrInvalidRequest
			}
			if fileData != "" {
				if _, _, ok := parseDataURL(fileData); !ok {
					return nil, ErrInvalidRequest
				}
			}
			parts = append(parts, chatContentPart{
				Type:     "file",
				FileData: fileData,
				FileID:   fileID,
				Filename: strings.TrimSpace(part.File.Filename),
			})
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
	case "text", "input_text":
		known["text"] = struct{}{}
	case "image_url":
		known["image_url"] = struct{}{}
	case "input_audio":
		known["input_audio"] = struct{}{}
	case "file":
		known["file"] = struct{}{}
	}
	if extra := unknownFields(fields, known); len(extra) > 0 {
		return fmt.Errorf("%w: content part field %q", ErrUnsupportedCapability, extra[0])
	}
	nestedName := ""
	nestedKnown := map[string]struct{}{}
	switch partType {
	case "image_url":
		nestedName = "image_url"
		nestedKnown = map[string]struct{}{"url": {}, "detail": {}}
	case "input_audio":
		nestedName = "input_audio"
		nestedKnown = map[string]struct{}{"data": {}, "format": {}}
	case "file":
		nestedName = "file"
		nestedKnown = map[string]struct{}{"file_data": {}, "file_id": {}, "filename": {}}
	default:
		return nil
	}
	var nestedFields map[string]json.RawMessage
	if err := json.Unmarshal(fields[nestedName], &nestedFields); err != nil || nestedFields == nil {
		return ErrInvalidRequest
	}
	if extra := unknownFields(nestedFields, nestedKnown); len(extra) > 0 {
		return fmt.Errorf("%w: %s field %q", ErrUnsupportedCapability, nestedName, extra[0])
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
			return "", fmt.Errorf("%w: %s content", ErrUnsupportedCapability, part.Type)
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
	if !validBase64(encoded) {
		return "", "", false
	}
	return mediaType, encoded, true
}

func validBase64(encoded string) bool {
	_, err := base64.StdEncoding.DecodeString(encoded)
	return err == nil
}

func rejectNativeImageDetail(part chatContentPart, protocol string) error {
	if part.ImageDetail == "" || part.ImageDetail == "auto" {
		return nil
	}
	return fmt.Errorf(
		"%w: %s image detail %q",
		ErrUnsupportedCapability,
		protocol,
		part.ImageDetail,
	)
}
