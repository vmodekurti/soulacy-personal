package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/agent"
)

// panicProvider is an llm.Provider whose Complete panics, simulating a bug in a
// provider adapter, tool handler, or any helper reached during a run.
type panicProvider struct{}

func (panicProvider) ID() string { return "test" }

func (panicProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	panic("simulated provider panic")
}

func (panicProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}

// TestHandleRecoversPanic verifies S2.1: a panic anywhere inside a run is
// converted into an ordinary error return rather than crashing the process.
// Before this fix, a panic on the channel/cron worker path (which is NOT behind
// Fiber's recover middleware) would take down the entire gateway.
func TestHandleRecoversPanic(t *testing.T) {
	agentDir := t.TempDir()
	loader := NewLoader([]string{agentDir})
	def := &agent.Definition{
		ID: "panic-bot", Name: "Panic Bot", Enabled: true,
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"},
	}
	if err := loader.Upsert(agentDir, def); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	router := llm.NewRouter("test")
	router.Register(panicProvider{})
	mem, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	// The call must return — not panic past us and crash the test binary.
	_, err = e.Handle(context.Background(), testUserMessage("panic-bot", "session-1", "hello"))
	if err == nil {
		t.Fatal("expected an error from a panicking run, got nil")
	}
	if !strings.Contains(err.Error(), "recovered panic") {
		t.Fatalf("error should identify the recovered panic, got: %v", err)
	}
}
