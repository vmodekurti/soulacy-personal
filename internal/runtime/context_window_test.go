package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestModelContextLimit(t *testing.T) {
	cases := map[string]int{
		"claude-3-5-sonnet-20241022": 200000,
		"gpt-4o-mini":                128000,
		"gpt-3.5-turbo":              16385,
		"gemini-2.5-pro":             1000000,
		"qwen2.5:72b":                32768,
		"some-unknown-model":         defaultContextLimit,
	}
	for model, want := range cases {
		if got := modelContextLimit("", model); got != want {
			t.Errorf("modelContextLimit(%q) = %d, want %d", model, got, want)
		}
	}
}

// gemma and hosted-cloud providers must NOT fall through to the tiny 8192 default
// that over-trimmed history and made tool-using agents loop (the gemma4:31b bug).
func TestModelContextLimit_GemmaAndCloud(t *testing.T) {
	if got := modelContextLimit("ollama_cloud", "gemma4:31b"); got <= defaultContextLimit {
		t.Errorf("gemma should get a generous window, got %d", got)
	}
	if got := modelContextLimit("ollama", "gemma2:9b"); got <= defaultContextLimit {
		t.Errorf("gemma (local) should still be recognised, got %d", got)
	}
	// An UNKNOWN model on a hosted cloud provider must beat the tiny local default.
	if got := modelContextLimit("ollama_cloud", "some-new-hosted-model"); got <= defaultContextLimit {
		t.Errorf("unknown model on a cloud provider should not get the tiny local default, got %d", got)
	}
	// An unknown model on a LOCAL provider keeps the conservative default.
	if got := modelContextLimit("ollama", "some-new-local-model"); got != defaultContextLimit {
		t.Errorf("unknown local model should keep the conservative default, got %d", got)
	}
}

func TestTrimMessagesToFit(t *testing.T) {
	big := strings.Repeat("x", 4000) // ~1000 tokens each
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: big},
		{Role: "assistant", Content: big},
		{Role: "user", Content: big},
		{Role: "assistant", Content: "recent"},
	}
	// Budget that forces dropping some of the old big turns.
	out, dropped := trimMessagesToFit(msgs, nil, 1500)
	if dropped == 0 {
		t.Fatal("expected some messages to be dropped")
	}
	// System message must be preserved at the front.
	if out[0].Role != "system" {
		t.Fatalf("system message must be preserved at front, got role %q", out[0].Role)
	}
	// The most recent message must survive.
	if out[len(out)-1].Content != "recent" {
		t.Fatalf("most recent message should be kept, got %q", out[len(out)-1].Content)
	}
	if estimateTokens(out, nil) > 1500 {
		t.Fatalf("trimmed estimate %d still over budget 1500", estimateTokens(out, nil))
	}
}

func TestTrimDropsOrphanToolResultAtFront(t *testing.T) {
	big := strings.Repeat("y", 8000)
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: big},
		{Role: "tool", Content: "tool result that would otherwise lead"},
		{Role: "user", Content: "latest"},
	}
	out, dropped := trimMessagesToFit(msgs, nil, 500)
	if dropped == 0 {
		t.Fatal("expected trimming")
	}
	// No surviving non-system message at the front may be an orphan tool result.
	if len(out) > 1 && out[1].Role == "tool" {
		t.Fatalf("trimmed history must not start (after system) with a tool result: %+v", out)
	}
}

func TestTrimAlwaysPreservesLatestUserInstruction(t *testing.T) {
	big := strings.Repeat("old source code(); ", 1000)
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "immutable system contract"},
		{Role: "user", Content: big},
		{Role: "assistant", Content: big},
		{Role: "user", Content: "LATEST USER REQUIREMENT"},
		{Role: "assistant", Content: big},
	}
	out, _ := trimMessagesToFit(msgs, nil, 100)
	if !chatMessagesContain(out, "system", "immutable system contract") {
		t.Fatal("system prompt was dropped")
	}
	if !chatMessagesContain(out, "user", "LATEST USER REQUIREMENT") {
		t.Fatal("latest user instruction was dropped")
	}
}

func TestContextExceededRetriesOnceThroughSameTrimFunnel(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID: "retry-context", Name: "Retry Context", Enabled: true,
		SystemPrompt: "Keep the user request.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "claude-test"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})
	sess := e.getOrCreateSession("retry-session", "retry-context")
	sess.mu.Lock()
	e.appendHistoryLocked(sess,
		llm.ChatMessage{Role: "user", Content: strings.Repeat("old context ", 2000)},
		llm.ChatMessage{Role: "assistant", Content: strings.Repeat("old answer ", 2000)},
	)
	sess.mu.Unlock()
	provider.errors = []error{errors.New("maximum context length exceeded"), nil}
	provider.responses = []llm.CompletionResponse{{}, {Content: "recovered"}}

	reply, err := e.Handle(context.Background(), testUserMessage("retry-context", "retry-session", "LATEST REQUEST"))
	if err != nil {
		t.Fatalf("reactive retry failed: %v", err)
	}
	if got := flattenParts(reply.Parts); !strings.Contains(got, "recovered") {
		t.Fatalf("retry reply = %q", got)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want one failure plus one retry", len(requests))
	}
	if len(requests[1].Messages) >= len(requests[0].Messages) {
		t.Fatalf("retry did not shrink context: %d >= %d", len(requests[1].Messages), len(requests[0].Messages))
	}
	if !chatMessagesContain(requests[1].Messages, "user", "LATEST REQUEST") {
		t.Fatal("reactive trim dropped the latest user request")
	}
}

func TestIsContextExceededErr(t *testing.T) {
	if !isContextExceededErr(errors.New("This model's maximum context length is 8192 tokens")) {
		t.Error("should match OpenAI-style context error")
	}
	if !isContextExceededErr(errors.New("input is too long for requested model")) {
		t.Error("should match 'input is too long'")
	}
	if isContextExceededErr(errors.New("401 unauthorized")) {
		t.Error("auth error must not be classified as context-exceeded")
	}
	if isContextExceededErr(nil) {
		t.Error("nil must not be context-exceeded")
	}
}
