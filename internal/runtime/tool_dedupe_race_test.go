package runtime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestConcurrentIdenticalToolCallsExecuteExactlyOnce(t *testing.T) {
	e, _ := newHandleTestEngine(t, &agent.Definition{
		ID: "dedupe", Name: "Dedupe", Enabled: true,
		LLM: agent.LLMConfig{Provider: "test", Model: "fake-model"},
	})
	var executions atomic.Int32
	e.builtins = []BuiltinTool{{
		Name: "side_effect", Description: "test",
		Handler: func(context.Context, map[string]any) (string, error) {
			executions.Add(1)
			time.Sleep(30 * time.Millisecond)
			return "committed-once", nil
		},
	}}
	def := e.loader.Get("dedupe")
	seen := map[string]string{}
	var seenMu sync.Mutex
	call := message.ToolCall{ID: "same", Name: "side_effect", Arguments: map[string]any{"id": "42"}}
	start := make(chan struct{})
	results := make(chan message.ToolResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- e.executeOneToolCall(context.Background(), def, "session", call, seen, &seenMu)
		}()
	}
	close(start)
	a, b := <-results, <-results
	if got := executions.Load(); got != 1 {
		t.Fatalf("identical concurrent side effects executed %d times", got)
	}
	for _, result := range []message.ToolResult{a, b} {
		if !strings.Contains(result.Content, "committed-once") {
			t.Fatalf("duplicate did not receive original result: %+v", result)
		}
	}
}
