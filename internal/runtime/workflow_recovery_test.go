package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"go.uber.org/zap"
)

func TestWorkflowRecoverySurvivesStoreReopenWithoutRepeatingAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	store, err := NewCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	e := &Engine{builtins: []BuiltinTool{{Name: "action", Handler: func(context.Context, map[string]any) (string, error) { calls++; return "confirmed", nil }}}}
	spec := agent.WorkflowSpec{Steps: []agent.StepSpec{{ID: "one", Tool: "action"}}}
	w := NewWorkflowExecutor(spec, e, store, zap.NewNop())
	msg := workflowTestMessage("same request")
	if _, err = w.Run(context.Background(), msg, "stable-run"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = NewCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w = NewWorkflowExecutor(spec, e, store, zap.NewNop())
	out, err := w.Run(context.Background(), msg, "stable-run")
	if err != nil || calls != 1 || !strings.Contains(string(out), "confirmed") {
		t.Fatalf("replayed action: calls=%d out=%s err=%v", calls, out, err)
	}
	_, err = w.Run(context.Background(), workflowTestMessage("different request"), "stable-run")
	if err == nil {
		t.Fatal("restored checkpoints into a different request")
	}
	if len(store.runLocks) != 0 {
		t.Fatal("finished run leaked a lock")
	}
}

func TestWorkflowRecoveryDoesNotRepeatUnknownExternalOutcome(t *testing.T) {
	store := newTestCheckpointStore(t)

	called := false
	e := &Engine{builtins: []BuiltinTool{{Name: "send", Handler: func(context.Context, map[string]any) (string, error) { called = true; return "sent", nil }}}}
	w := NewWorkflowExecutor(agent.WorkflowSpec{Steps: []agent.StepSpec{{ID: "send", Tool: "send"}}}, e, store, zap.NewNop())
	release, err := w.bindRun(context.Background(), workflowTestMessage("send"), "uncertain")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := store.Upsert(context.Background(), Checkpoint{AgentID: "workflow-agent", RunID: "uncertain", StepID: "send", Status: CheckpointInProgress, State: json.RawMessage(`null`), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_, err = w.Run(context.Background(), workflowTestMessage("send"), "uncertain")
	if err == nil || !strings.Contains(err.Error(), "unknown outcome") || called {
		t.Fatalf("uncertain action replayed: %v called=%v", err, called)
	}
}

func TestWorkflowRunLockCancellation(t *testing.T) {
	store := newTestCheckpointStore(t)
	release, err := store.lockRun(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.lockRun(ctx, "key")
	if err == nil {
		t.Fatal("cancelled waiter acquired lock")
	}
	release()
	if len(store.runLocks) != 0 {
		t.Fatal("cancelled waiter leaked lock")
	}
}

func TestWorkflowRecoveryRejectsUnboundLegacyCheckpoints(t *testing.T) {
	store := newTestCheckpointStore(t)
	if err := store.Upsert(context.Background(), Checkpoint{AgentID: "workflow-agent", RunID: "legacy", StepID: "one", Status: CheckpointCompleted, State: json.RawMessage(`"private result"`), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	w := NewWorkflowExecutor(agent.WorkflowSpec{}, &Engine{}, store, zap.NewNop())
	out, err := w.Run(context.Background(), workflowTestMessage("other request"), "legacy")
	if err == nil || out != nil {
		t.Fatalf("unbound historical result was reused: %s %v", out, err)
	}
}

func TestWorkflowRecoveryPreservesOutcomeEvidence(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(map[bool]string{false: "steps", true: "graph"}[graph], func(t *testing.T) {
			spec := agent.WorkflowSpec{Steps: []agent.StepSpec{{ID: "check", Tool: "lookup"}}}
			if graph {
				spec = agent.WorkflowSpec{Nodes: []sdkr.FlowNode{{ID: "check", Tool: "lookup"}}}
			}
			def := &agent.Definition{ID: "workflow-agent", Enabled: true, Workflow: &spec, Builtins: strListPtr("lookup"), Outcome: contract("fail", agent.OutcomeAssertion{Target: "check", Op: "count_gte", Value: "2"})}
			e, _ := newHandleTestEngine(t, def)
			calls := 0
			e.builtins = []BuiltinTool{{Name: "lookup", Handler: func(context.Context, map[string]any) (string, error) { calls++; return `{"results":["a","b"]}`, nil }}}
			w := NewWorkflowExecutor(spec, e, newTestCheckpointStore(t), zap.NewNop())
			for i := 0; i < 2; i++ {
				var report OutcomeReport
				ctx := WithOutcomeCollector(context.Background(), &report)
				if _, err := w.Run(ctx, workflowTestMessage("verify"), "verified-run"); err != nil {
					t.Fatal(err)
				}
				if report.Outcome != OutcomeComplete || !report.Met {
					t.Fatalf("outcome evidence lost on run %d: %+v", i, report)
				}
			}
			if calls != 1 {
				t.Fatalf("completed action repeated %d times", calls)
			}
		})
	}
}
