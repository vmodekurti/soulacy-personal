package runtime

import (
	"context"
	"errors"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
	"sync"
	"testing"
)

func TestExplicitBuiltinGrantsAreEnforcedAtDispatch(t *testing.T) {
	for _, gate := range []string{"", "genie", "skills", "knowledge"} {
		t.Run(gate, func(t *testing.T) {
			called := false
			e := &Engine{log: zap.NewNop(), builtins: []BuiltinTool{{Name: "hidden", Gate: gate, Handler: func(context.Context, map[string]any) (string, error) { called = true; return "ok", nil }}}}
			def := &agent.Definition{ID: "restricted", Builtins: strListPtr()}
			if _, err := e.runTool(context.Background(), def, "s", message.ToolCall{Name: "hidden"}); err == nil || called {
				t.Fatal("hidden tool executed outside explicit grants")
			}
		})
	}
}
func TestStepWorkflowHonorsAgentToolGrants(t *testing.T) {
	def := &agent.Definition{ID: "workflow-agent", Enabled: true, Builtins: strListPtr()}
	e, _ := newHandleTestEngine(t, def)
	called := false
	e.builtins = []BuiltinTool{{Name: "hidden", Handler: func(context.Context, map[string]any) (string, error) { called = true; return "ok", nil }}}
	w := NewWorkflowExecutor(agent.WorkflowSpec{Steps: []agent.StepSpec{{ID: "one", Tool: "hidden"}}}, e, nil, zap.NewNop())
	if _, err := w.Run(context.Background(), workflowTestMessage("run"), "r"); err == nil || called {
		t.Fatal("linear workflow bypassed tool grants")
	}
}
func TestRepeatedFailedToolResultRemainsFailure(t *testing.T) {
	def := &agent.Definition{ID: "failed", Builtins: strListPtr("lookup")}
	e, _ := newHandleTestEngine(t, def)
	e.builtins = []BuiltinTool{{Name: "lookup", Handler: func(context.Context, map[string]any) (string, error) { return "", errors.New("source unavailable") }}}
	seen := map[string]string{}
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		res := e.executeOneToolCall(context.Background(), def, "s", message.ToolCall{ID: "same", Name: "lookup"}, seen, &mu)
		if !res.IsError {
			t.Fatal("cached failure became a successful tool result")
		}
	}
}

func TestNewToolInvocationCanReadChangedState(t *testing.T) {
	def := &agent.Definition{ID: "poll", Builtins: strListPtr("status")}
	e, _ := newHandleTestEngine(t, def)
	calls := 0
	e.builtins = []BuiltinTool{{Name: "status", Handler: func(context.Context, map[string]any) (string, error) {
		calls++
		if calls == 1 {
			return "pending", nil
		}
		return "ready", nil
	}}}
	seen := map[string]string{}
	var mu sync.Mutex
	first := e.executeOneToolCall(context.Background(), def, "s", message.ToolCall{ID: "poll-1", Name: "status"}, seen, &mu)
	second := e.executeOneToolCall(context.Background(), def, "s", message.ToolCall{ID: "poll-2", Name: "status"}, seen, &mu)
	if calls != 2 || first.Content == second.Content {
		t.Fatal("new poll reused stale evidence")
	}
}
