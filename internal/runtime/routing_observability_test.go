package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestCompletionEventPayloadRecordsEvidenceWithoutContent(t *testing.T) {
	event := llm.CompletionEvent{Kind: "completion", ID: "call-1", RequestedProvider: "fast", RequestedModel: "small", Request: llm.CompletionRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "private text"}}}, Response: &llm.CompletionResponse{Content: "secret answer", InputTokens: 4, OutputTokens: 3}, Duration: 25 * time.Millisecond}
	payload := completionEventPayload(event, false, 1024)
	if payload["prompt"] != nil || payload["response"] != nil {
		t.Fatalf("content leaked: %#v", payload)
	}
	if payload["prompt_sha256"] == "" || payload["response_sha256"] == "" || payload["duration_ms"] != int64(25) {
		t.Fatalf("evidence missing: %#v", payload)
	}
}

func TestCompletionEventPayloadCapturesAndBoundsContent(t *testing.T) {
	event := llm.CompletionEvent{Kind: "completion", ID: "call-1", Request: llm.CompletionRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "private text"}}}, Response: &llm.CompletionResponse{Content: "secret answer", ToolCalls: []message.ToolCall{{Name: "lookup", Arguments: map[string]any{"secret": "value"}}}}}
	payload := completionEventPayload(event, true, 48)
	response, _ := payload["response"].(string)
	if payload["prompt"] == nil || !strings.Contains(response, "secret answer") || payload["response_truncated"] != true || payload["content_captured"] != true || payload["tool_calls"] != 1 {
		t.Fatalf("capture policy not applied: %#v", payload)
	}
}
