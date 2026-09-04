// agent_test.go — tests for Clone, ResolvedRunTimeout, and internal helpers.
package agent

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// cloneStrSlice
// ---------------------------------------------------------------------------

func TestCloneStrSliceNil(t *testing.T) {
	if got := cloneStrSlice(nil); got != nil {
		t.Errorf("cloneStrSlice(nil) = %v, want nil", got)
	}
}

func TestCloneStrSliceEmpty(t *testing.T) {
	src := []string{}
	got := cloneStrSlice(src)
	if got == nil || len(got) != 0 {
		t.Errorf("cloneStrSlice([]) = %v", got)
	}
}

func TestCloneStrSliceCopies(t *testing.T) {
	src := []string{"a", "b", "c"}
	got := cloneStrSlice(src)
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("cloneStrSlice: got %v", got)
	}
	// Mutate clone — original must not change.
	got[0] = "z"
	if src[0] != "a" {
		t.Error("cloneStrSlice aliased source slice")
	}
}

// ---------------------------------------------------------------------------
// cloneMapAny
// ---------------------------------------------------------------------------

func TestCloneMapAnyNil(t *testing.T) {
	if got := cloneMapAny(nil); got != nil {
		t.Errorf("cloneMapAny(nil) = %v, want nil", got)
	}
}

func TestCloneMapAnyEmpty(t *testing.T) {
	got := cloneMapAny(map[string]any{})
	if got == nil || len(got) != 0 {
		t.Errorf("cloneMapAny({}) = %v", got)
	}
}

func TestCloneMapAnyCopies(t *testing.T) {
	src := map[string]any{"type": "object", "required": []string{"q"}}
	got := cloneMapAny(src)
	if got["type"] != "object" {
		t.Errorf("cloneMapAny: type = %v", got["type"])
	}
	// Mutating the clone's scalar key must not affect source.
	got["type"] = "array"
	if src["type"] != "object" {
		t.Error("cloneMapAny aliased source map")
	}
}

// ---------------------------------------------------------------------------
// ResolvedRunTimeout
// ---------------------------------------------------------------------------

func TestResolvedRunTimeoutNilReceiver(t *testing.T) {
	var d *Definition
	got := d.ResolvedRunTimeout(5 * time.Minute)
	if got != 5*time.Minute {
		t.Errorf("nil receiver: got %v, want 5m", got)
	}
}

func TestResolvedRunTimeoutEmpty(t *testing.T) {
	d := &Definition{}
	got := d.ResolvedRunTimeout(10 * time.Minute)
	if got != 10*time.Minute {
		t.Errorf("empty RunTimeout: got %v, want 10m", got)
	}
}

func TestResolvedRunTimeoutValid(t *testing.T) {
	d := &Definition{RunTimeout: "30m"}
	got := d.ResolvedRunTimeout(5 * time.Minute)
	if got != 30*time.Minute {
		t.Errorf("30m: got %v, want 30m", got)
	}
}

func TestResolvedRunTimeoutInvalid(t *testing.T) {
	d := &Definition{RunTimeout: "garbage"}
	got := d.ResolvedRunTimeout(7 * time.Minute)
	if got != 7*time.Minute {
		t.Errorf("invalid: got %v, want 7m fallback", got)
	}
}

func TestResolvedRunTimeoutZero(t *testing.T) {
	d := &Definition{RunTimeout: "0s"}
	got := d.ResolvedRunTimeout(3 * time.Minute)
	if got != 3*time.Minute {
		t.Errorf("0s: got %v, want 3m fallback", got)
	}
}

func TestResolvedRunTimeoutHonorsLongerReasoningTimeout(t *testing.T) {
	d := &Definition{
		RunTimeout: "10m",
		Reasoning:  ReasoningConfig{TotalTimeout: "15m"},
	}
	got := d.ResolvedRunTimeout(5 * time.Minute)
	if got != 15*time.Minute {
		t.Errorf("reasoning timeout should extend run timeout: got %v, want 15m", got)
	}
}

// ---------------------------------------------------------------------------
// Clone
// ---------------------------------------------------------------------------

func TestCloneNilReceiver(t *testing.T) {
	var d *Definition
	if got := d.Clone(); got != nil {
		t.Errorf("nil.Clone() = %v, want nil", got)
	}
}

func TestCloneScalarFields(t *testing.T) {
	d := &Definition{
		ID: "bot", Name: "Bot", Enabled: true,
		SystemTools: true, StreamReply: true, MaxTurns: 5,
	}
	cp := d.Clone()
	if cp.ID != "bot" || cp.Name != "Bot" || !cp.Enabled || !cp.SystemTools || cp.MaxTurns != 5 {
		t.Errorf("Clone scalar mismatch: %+v", cp)
	}
}

func TestCloneSlicesAreIndependent(t *testing.T) {
	d := &Definition{
		ID:           "ag",
		Tags:         []string{"tag1"},
		Channels:     []string{"http"},
		Skills:       []string{"csv"},
		Knowledge:    []string{"kb1"},
		Agents:       []string{"peer"},
		ConfirmTools: []string{"shell_exec"},
	}
	cp := d.Clone()
	cp.Tags[0] = "mutated"
	cp.Channels[0] = "mutated"
	cp.Skills[0] = "mutated"
	cp.Knowledge[0] = "mutated"
	cp.Agents[0] = "mutated"
	cp.ConfirmTools[0] = "mutated"
	if d.Tags[0] != "tag1" || d.Channels[0] != "http" || d.Skills[0] != "csv" {
		t.Error("Clone aliased string slices")
	}
}

func TestCloneMemoryPolicy(t *testing.T) {
	d := &Definition{
		ID: "ag",
		Memory: MemoryPolicy{
			MaxTokens:   100,
			ReadScopes:  []string{"session"},
			WriteScopes: []string{"agent"},
		},
	}
	cp := d.Clone()
	cp.Memory.ReadScopes[0] = "mutated"
	if d.Memory.ReadScopes[0] != "session" {
		t.Error("Clone aliased Memory.ReadScopes")
	}
}

func TestCloneLLMWithOutputSchema(t *testing.T) {
	schema := map[string]any{"type": "object"}
	d := &Definition{
		ID:  "ag",
		LLM: LLMConfig{Provider: "anthropic", Model: "claude-3", OutputSchema: schema},
	}
	cp := d.Clone()
	cp.LLM.OutputSchema["type"] = "array"
	if d.LLM.OutputSchema["type"] != "object" {
		t.Error("Clone aliased LLM.OutputSchema")
	}
}

func TestCloneBuiltinsPointer(t *testing.T) {
	list := []string{"web_search"}
	d := &Definition{ID: "ag", Builtins: &list}
	cp := d.Clone()
	(*cp.Builtins)[0] = "mutated"
	if list[0] != "web_search" {
		t.Error("Clone aliased Builtins pointer slice")
	}
}

func TestCloneNilBuiltinsPointer(t *testing.T) {
	d := &Definition{ID: "ag", Builtins: nil}
	cp := d.Clone()
	if cp.Builtins != nil {
		t.Error("Clone should preserve nil Builtins pointer")
	}
}

func TestCloneMCPAllowlists(t *testing.T) {
	servers := []string{"rocketmoney"}
	tools := []string{"mcp__rm__get"}
	d := &Definition{ID: "ag", MCPServers: &servers, MCPTools: &tools}
	cp := d.Clone()
	(*cp.MCPServers)[0] = "mutated"
	(*cp.MCPTools)[0] = "mutated"
	if servers[0] != "rocketmoney" || tools[0] != "mcp__rm__get" {
		t.Error("Clone aliased MCPServers/MCPTools")
	}
}

func TestCloneLabels(t *testing.T) {
	d := &Definition{ID: "ag", Labels: map[string]string{"env": "prod"}}
	cp := d.Clone()
	cp.Labels["env"] = "mutated"
	if d.Labels["env"] != "prod" {
		t.Error("Clone aliased Labels map")
	}
}

func TestCloneNilLabels(t *testing.T) {
	d := &Definition{ID: "ag"}
	cp := d.Clone()
	if cp.Labels != nil {
		t.Error("Clone should not create Labels when source is nil")
	}
}

func TestCloneTools(t *testing.T) {
	d := &Definition{
		ID: "ag",
		Tools: []ToolDef{
			{Name: "search", Parameters: map[string]any{"type": "object"}},
		},
	}
	cp := d.Clone()
	cp.Tools[0].Parameters["type"] = "array"
	if d.Tools[0].Parameters["type"] != "object" {
		t.Error("Clone aliased ToolDef Parameters")
	}
}

func TestCloneNotifyOnFailure(t *testing.T) {
	d := &Definition{
		ID:              "ag",
		NotifyOnFailure: &NotifyOnFailure{Channel: "telegram", To: "123"},
	}
	cp := d.Clone()
	cp.NotifyOnFailure.Channel = "slack"
	if d.NotifyOnFailure.Channel != "telegram" {
		t.Error("Clone aliased NotifyOnFailure")
	}
}

func TestCloneNilNotifyOnFailure(t *testing.T) {
	d := &Definition{ID: "ag", NotifyOnFailure: nil}
	cp := d.Clone()
	if cp.NotifyOnFailure != nil {
		t.Error("Clone should preserve nil NotifyOnFailure")
	}
}

func TestCloneWorkflow(t *testing.T) {
	d := &Definition{
		ID: "ag",
		Workflow: &WorkflowSpec{
			Steps: []StepSpec{{ID: "step1", Tool: "web_search"}},
		},
	}
	cp := d.Clone()
	cp.Workflow.Steps[0].ID = "mutated"
	if d.Workflow.Steps[0].ID != "step1" {
		t.Error("Clone aliased Workflow steps")
	}
}

func TestCloneNilWorkflow(t *testing.T) {
	d := &Definition{ID: "ag", Workflow: nil}
	if cp := d.Clone(); cp.Workflow != nil {
		t.Error("Clone should preserve nil Workflow")
	}
}

func TestCloneScheduleWithOutput(t *testing.T) {
	d := &Definition{
		ID: "ag",
		Schedule: &Schedule{
			Cron:   "0 9 * * *",
			Output: &ScheduleOutput{Channel: "telegram", To: "123"},
		},
	}
	cp := d.Clone()
	cp.Schedule.Output.Channel = "slack"
	if d.Schedule.Output.Channel != "telegram" {
		t.Error("Clone aliased Schedule.Output")
	}
}

func TestCloneNilSchedule(t *testing.T) {
	d := &Definition{ID: "ag", Schedule: nil}
	if cp := d.Clone(); cp.Schedule != nil {
		t.Error("Clone should preserve nil Schedule")
	}
}

func TestCloneHooks(t *testing.T) {
	d := &Definition{
		ID:    "ag",
		Hooks: []ContextHook{{Event: "before_llm", PythonFile: "hook.py"}},
	}
	cp := d.Clone()
	if len(cp.Hooks) != 1 || cp.Hooks[0].Event != "before_llm" {
		t.Errorf("Clone hooks: %+v", cp.Hooks)
	}
}
