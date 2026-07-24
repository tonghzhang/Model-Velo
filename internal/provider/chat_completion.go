package provider

import (
	"encoding/json"
	"strings"
	"time"
)

type completionUsage struct {
	PromptTokens            int                           `json:"prompt_tokens"`
	PromptTokensDetails     *completionInputTokenDetails  `json:"prompt_tokens_details,omitempty"`
	CompletionTokens        int                           `json:"completion_tokens"`
	CompletionTokensDetails *completionOutputTokenDetails `json:"completion_tokens_details,omitempty"`
	TotalTokens             int                           `json:"total_tokens"`
}

type completionInputTokenDetails struct {
	Audio       int `json:"audio_tokens,omitempty"`
	Image       int `json:"image_tokens,omitempty"`
	CachedRead  int `json:"cached_read_tokens,omitempty"`
	CachedWrite int `json:"cached_write_tokens,omitempty"`
}

type completionOutputTokenDetails struct {
	Audio     int `json:"audio_tokens,omitempty"`
	Reasoning int `json:"reasoning_tokens,omitempty"`
}

func (usage *completionUsage) setInputDetails(audio, image, cachedRead, cachedWrite *int) {
	if usage == nil || (audio == nil && image == nil && cachedRead == nil && cachedWrite == nil) {
		return
	}
	usage.PromptTokensDetails = &completionInputTokenDetails{
		Audio:       intValue(audio),
		Image:       intValue(image),
		CachedRead:  intValue(cachedRead),
		CachedWrite: intValue(cachedWrite),
	}
}

func (usage *completionUsage) setOutputDetails(audio, reasoning *int) {
	if usage == nil || (audio == nil && reasoning == nil) {
		return
	}
	usage.CompletionTokensDetails = &completionOutputTokenDetails{
		Audio:     intValue(audio),
		Reasoning: intValue(reasoning),
	}
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func inclusiveInputTokens(base, cachedRead, cachedWrite *int) (*int, bool) {
	if base == nil && cachedRead == nil && cachedWrite == nil {
		return nil, true
	}
	var total int64
	for _, value := range []*int{base, cachedRead, cachedWrite} {
		if value == nil {
			continue
		}
		if *value < 0 {
			return nil, false
		}
		total += int64(*value)
		if total > int64(^uint(0)>>1) {
			return nil, false
		}
	}
	normalized := int(total)
	return &normalized, true
}

// newCompletionUsage 在上游完全没有返回用量时返回 nil，避免伪造零值 usage。
func newCompletionUsage(prompt, completion, total *int) *completionUsage {
	if prompt == nil && completion == nil && total == nil {
		return nil
	}
	usage := &completionUsage{}
	if prompt != nil {
		usage.PromptTokens = *prompt
	}
	if completion != nil {
		usage.CompletionTokens = *completion
	}
	if total != nil {
		usage.TotalTokens = *total
	}
	return usage
}

// completionResponse 是原生协议统一映射到的 OpenAI 非流式响应。
type completionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *completionUsage   `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int               `json:"index"`
	Message      completionMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type completionMessage struct {
	Role             string         `json:"role"`
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
}

// encodeCompletion 补齐网关响应所需的稳定外壳；上游提供的 ID 和用量优先保留。
func encodeCompletion(
	id string,
	requestID string,
	model string,
	content string,
	finishReason string,
	usage *completionUsage,
) ([]byte, error) {
	return encodeCompletionMessage(
		id,
		requestID,
		model,
		completionMessage{Role: "assistant", Content: &content},
		finishReason,
		usage,
	)
}

func encodeCompletionMessage(
	id string,
	requestID string,
	model string,
	message completionMessage,
	finishReason string,
	usage *completionUsage,
) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "chatcmpl-" + strings.TrimSpace(requestID)
	}
	if id == "chatcmpl-" {
		id = "chatcmpl-upstream"
	}
	if usage != nil && usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	response := completionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []completionChoice{{
			Message:      message,
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	return encoded, nil
}
