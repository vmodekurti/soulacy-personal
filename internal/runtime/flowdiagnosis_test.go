package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
)

func TestDiagnoseFlowTraceJSONArgsFailure(t *testing.T) {
	tr := FlowRunTrace{
		AgentID:   "agent",
		RunID:     "run",
		StartedAt: time.Now(),
		Entries: []reasoning.FlowNodeRun{
			{NodeID: "fetch", Kind: "tool", Output: json.RawMessage(`{"ok":true}`), StartedAt: time.Now()},
			{NodeID: "store", Kind: "tool", Error: `workflow: tool args for "kb_write" not valid JSON: invalid character '"' looking for beginning of value`, StartedAt: time.Now()},
		},
	}
	got := DiagnoseFlowTrace(tr)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.FailedNode != "store" {
		t.Fatalf("failed node = %q, want store", got.FailedNode)
	}
	if !strings.Contains(strings.ToLower(got.RootCause), "json") {
		t.Fatalf("root cause = %q, want JSON guidance", got.RootCause)
	}
	if got.Retryable {
		t.Fatalf("retryable = true, want false for malformed static args")
	}
}

func TestDiagnoseFlowTraceToolOutputFailure(t *testing.T) {
	tr := FlowRunTrace{
		AgentID: "agent",
		RunID:   "run",
		Entries: []reasoning.FlowNodeRun{
			{
				NodeID: "notify",
				Kind:   "tool",
				Output: json.RawMessage(`{"status":"error","error":"channel.send: to is required"}`),
			},
		},
	}
	got := DiagnoseFlowTrace(tr)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "to is required") {
		t.Fatalf("error = %q, want tool output error", got.Error)
	}
	if !strings.Contains(strings.ToLower(got.NextAction), "required field") {
		t.Fatalf("next action = %q, want required field guidance", got.NextAction)
	}
}

func TestDiagnoseFlowTraceEmpty(t *testing.T) {
	got := DiagnoseFlowTrace(FlowRunTrace{AgentID: "agent", RunID: "run"})
	if got.Status != "empty" {
		t.Fatalf("status = %q, want empty", got.Status)
	}
	if !got.Retryable {
		t.Fatalf("empty trace should be retryable")
	}
}

func TestDiagnoseFlowTraceSuccess(t *testing.T) {
	got := DiagnoseFlowTrace(FlowRunTrace{
		AgentID: "agent",
		RunID:   "run",
		Entries: []reasoning.FlowNodeRun{
			{NodeID: "fetch", Kind: "tool", Output: json.RawMessage(`{"ok":true}`)},
		},
	})
	if got.Status != "success" {
		t.Fatalf("status = %q, want success", got.Status)
	}
	if got.FailedNode != "" {
		t.Fatalf("failed node = %q, want empty", got.FailedNode)
	}
}
