package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
)

func TestWorkflowExecutorRunsStepsSequentially(t *testing.T) {
	store := newTestCheckpointStore(t)
	e := &Engine{}
	var calls []string
	e.builtins = []BuiltinTool{
		{
			Name: "first",
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				calls = append(calls, "first")
				return "first:" + args["text"].(string), nil
			},
		},
		{
			Name: "second",
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				calls = append(calls, "second")
				return "second:" + args["prev"].(string), nil
			},
		},
	}

	w := NewWorkflowExecutor(agent.WorkflowSpec{Steps: []agent.StepSpec{
		{ID: "first", Tool: "first", Input: `{"text":"{{.trigger}}"}`, Output: "first_out"},
		{ID: "second", Tool: "second", Input: `{"prev":"{{.first_out}}"}`, Output: "second_out"},
	}}, e, store, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("hello"), "run-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rawJSONString(t, out); got != "second:first:hello" {
		t.Fatalf("output = %q, want %q", got, "second:first:hello")
	}
	if got := calls; len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("calls = %v, want [first second]", got)
	}
	assertCheckpointStatus(t, store, "workflow-agent", "run-1", "first", CheckpointCompleted)
	assertCheckpointStatus(t, store, "workflow-agent", "run-1", "second", CheckpointCompleted)
}

func TestWorkflowExecutorRetriesOnce(t *testing.T) {
	store := newTestCheckpointStore(t)
	e := &Engine{}
	attempts := 0
	e.builtins = []BuiltinTool{{
		Name: "flaky",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			attempts++
			if attempts == 1 {
				return "", errors.New("temporary failure")
			}
			return "ok", nil
		},
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{Steps: []agent.StepSpec{
		{ID: "flaky", Tool: "flaky", Input: `{}`, Output: "result", OnError: "retry"},
	}}, e, store, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("go"), "run-retry")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got := rawJSONString(t, out); got != "ok" {
		t.Fatalf("output = %q, want ok", got)
	}
	assertCheckpointStatus(t, store, "workflow-agent", "run-retry", "flaky", CheckpointCompleted)
}

func TestWorkflowExecutorSkipAndAbortErrorHandling(t *testing.T) {
	t.Run("skip completes failed step and continues", func(t *testing.T) {
		store := newTestCheckpointStore(t)
		e := &Engine{}
		e.builtins = []BuiltinTool{
			{Name: "fail", Handler: func(context.Context, map[string]any) (string, error) {
				return "", errors.New("boom")
			}},
			{Name: "next", Handler: func(context.Context, map[string]any) (string, error) {
				return "continued", nil
			}},
		}

		w := NewWorkflowExecutor(agent.WorkflowSpec{Steps: []agent.StepSpec{
			{ID: "fail", Tool: "fail", Input: `{}`, OnError: "skip"},
			{ID: "next", Tool: "next", Input: `{}`, Output: "result"},
		}}, e, store, zap.NewNop())

		out, err := w.Run(context.Background(), workflowTestMessage("go"), "run-skip")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := rawJSONString(t, out); got != "continued" {
			t.Fatalf("output = %q, want continued", got)
		}
		assertCheckpointStatus(t, store, "workflow-agent", "run-skip", "fail", CheckpointCompleted)
		assertCheckpointStatus(t, store, "workflow-agent", "run-skip", "next", CheckpointCompleted)
	})

	t.Run("abort marks failed checkpoint", func(t *testing.T) {
		store := newTestCheckpointStore(t)
		e := &Engine{builtins: []BuiltinTool{{
			Name: "fail",
			Handler: func(context.Context, map[string]any) (string, error) {
				return "", errors.New("boom")
			},
		}}}

		w := NewWorkflowExecutor(agent.WorkflowSpec{Steps: []agent.StepSpec{
			{ID: "fail", Tool: "fail", Input: `{}`},
		}}, e, store, zap.NewNop())

		if _, err := w.Run(context.Background(), workflowTestMessage("go"), "run-abort"); err == nil {
			t.Fatal("expected abort error")
		}
		assertCheckpointStatus(t, store, "workflow-agent", "run-abort", "fail", CheckpointFailed)
	})
}

func TestWorkflowExecutorResumeSkipsCompletedStepsAndRestoresOutput(t *testing.T) {
	store := newTestCheckpointStore(t)
	if err := store.Upsert(context.Background(), Checkpoint{
		AgentID: "workflow-agent",
		RunID:   "run-resume",
		StepID:  "first",
		Status:  CheckpointCompleted,
		State:   json.RawMessage(`"cached"`),
	}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	e := &Engine{}
	var firstCalled bool
	e.builtins = []BuiltinTool{
		{Name: "first", Handler: func(context.Context, map[string]any) (string, error) {
			firstCalled = true
			return "fresh", nil
		}},
		{Name: "second", Handler: func(_ context.Context, args map[string]any) (string, error) {
			return "saw:" + args["prev"].(string), nil
		}},
	}

	w := NewWorkflowExecutor(agent.WorkflowSpec{Steps: []agent.StepSpec{
		{ID: "first", Tool: "first", Input: `{}`, Output: "first_out"},
		{ID: "second", Tool: "second", Input: `{"prev":"{{.first_out}}"}`, Output: "second_out"},
	}}, e, store, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("go"), "run-resume")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if firstCalled {
		t.Fatal("completed step was called again during resume")
	}
	if got := rawJSONString(t, out); got != "saw:cached" {
		t.Fatalf("output = %q, want saw:cached", got)
	}
	assertCheckpointStatus(t, store, "workflow-agent", "run-resume", "second", CheckpointCompleted)
}

func newTestCheckpointStore(t *testing.T) *CheckpointStore {
	t.Helper()
	store, err := NewCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.db"))
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func workflowTestMessage(text string) message.Message {
	return message.Message{
		AgentID: "workflow-agent",
		Channel: "http",
		Parts:   message.Text(text),
	}
}

func rawJSONString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal raw string %s: %v", string(raw), err)
	}
	return out
}

func assertCheckpointStatus(t *testing.T, store *CheckpointStore, agentID, runID, stepID, want string) {
	t.Helper()
	cp, err := store.Get(context.Background(), agentID, runID, stepID)
	if err != nil {
		t.Fatalf("checkpoint %s/%s/%s: %v", agentID, runID, stepID, err)
	}
	if cp.Status != want {
		t.Fatalf("checkpoint %s status = %q, want %q", stepID, cp.Status, want)
	}
}
