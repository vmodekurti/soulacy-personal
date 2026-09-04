package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/pkg/agent"
)

// TestSeedSessionHistory verifies that a forked session's copied entries
// become real LLM context on the next Handle (Story 8 — branching).
func TestSeedSessionHistory_FeedsLLMContext(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "fork-bot",
		Name:         "Fork Bot",
		Enabled:      true,
		SystemPrompt: "Continue conversations.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "continuing from the fork"}}

	e.SeedSessionHistory("fork-bot", "branch-1", []session.ConversationEntry{
		{Role: "user", Content: "what is the capital of France?"},
		{Role: "assistant", Content: "Paris, of course."},
	})

	_, err := e.Handle(context.Background(), testUserMessage("fork-bot", "branch-1", "and of Germany?"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	reqs := provider.requestsSnapshot()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests recorded")
	}
	var joined strings.Builder
	sawSeededBeforeNew := false
	seededAt, newAt := -1, -1
	for i, m := range reqs[0].Messages {
		joined.WriteString(m.Role + ":" + m.Content + "\n")
		if strings.Contains(m.Content, "Paris, of course.") {
			seededAt = i
		}
		if strings.Contains(m.Content, "and of Germany?") {
			newAt = i
		}
	}
	if seededAt == -1 {
		t.Fatalf("seeded assistant turn missing from LLM context:\n%s", joined.String())
	}
	if newAt == -1 {
		t.Fatalf("new user message missing from LLM context:\n%s", joined.String())
	}
	if seededAt < newAt {
		sawSeededBeforeNew = true
	}
	if !sawSeededBeforeNew {
		t.Errorf("seeded history should precede the new message (seeded=%d new=%d)", seededAt, newAt)
	}
}

// TestSeedSessionHistory_DoesNotClobberLiveSession ensures seeding is a
// no-op when the in-memory session already has history (prevents a fork
// racing an active conversation from wiping context).
func TestSeedSessionHistory_DoesNotClobberLiveSession(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "fork-bot-2",
		Name:         "Fork Bot 2",
		Enabled:      true,
		SystemPrompt: "Continue.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{
		{Content: "reply one"},
		{Content: "reply two"},
	}

	if _, err := e.Handle(context.Background(), testUserMessage("fork-bot-2", "live", "original turn")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Attempt to seed the now-live session — must not erase its history.
	e.SeedSessionHistory("fork-bot-2", "live", []session.ConversationEntry{
		{Role: "user", Content: "INJECTED"},
	})

	if _, err := e.Handle(context.Background(), testUserMessage("fork-bot-2", "live", "second turn")); err != nil {
		t.Fatalf("Handle 2: %v", err)
	}
	reqs := provider.requestsSnapshot()
	last := reqs[len(reqs)-1]
	for _, m := range last.Messages {
		if strings.Contains(m.Content, "INJECTED") {
			t.Fatal("seeding clobbered a live session")
		}
	}
	found := false
	for _, m := range last.Messages {
		if strings.Contains(m.Content, "original turn") {
			found = true
		}
	}
	if !found {
		t.Error("live session lost its original history")
	}
}
