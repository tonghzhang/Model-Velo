package provider

import (
	"encoding/json"
	"strings"
	"time"
)

type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
	Role    string `json:"role"`
	Content string `json:"content"`
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
			Message:      completionMessage{Role: "assistant", Content: content},
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
