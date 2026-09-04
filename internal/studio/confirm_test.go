package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func warnKinds(r PreflightResult) map[string]int {
	m := map[string]int{}
	for _, w := range r.Warnings {
		m[w.Kind]++
	}
	return m
}

func TestUnattended_ScheduledSystemToolWarns(t *testing.T) {
	d := Draft{
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "run", Kind: "tool", Tool: "shell_exec", Input: `{"cmd":"echo hi"}`},
		}},
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"shell_exec"}}})
	if warnKinds(r)["confirmation"] == 0 {
		t.Errorf("expected unattended/confirmation warning, got %+v", r.Warnings)
	}
}

func TestUnattended_ScheduledPythonSystemWarns(t *testing.T) {
	d := Draft{
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "py", Kind: "python", Requires: []string{"network"}},
		}},
	}
	r := Preflight(d, PreflightInput{})
	if warnKinds(r)["confirmation"] == 0 {
		t.Errorf("expected confirmation warning for network python node, got %+v", r.Warnings)
	}
}

func TestUnattended_OnExplainsInsteadOfFailure(t *testing.T) {
	d := Draft{
		Unattended: true,
		Trigger:    Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "run", Kind: "tool", Tool: "shell_exec", Input: `{"cmd":"x"}`},
		}},
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"shell_exec"}}})
	var msg string
	for _, w := range r.Warnings {
		if w.Kind == "confirmation" {
			msg = w.Message
		}
	}
	if msg == "" {
		t.Fatal("expected a confirmation warning even when unattended is on")
	}
	if !strings.Contains(strings.ToLower(msg), "unattended mode is on") {
		t.Errorf("unattended-on message should explain it's on, got %q", msg)
	}
}

func TestUnattended_ManualTriggerNoWarn(t *testing.T) {
	d := Draft{
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "run", Kind: "tool", Tool: "shell_exec", Input: `{"cmd":"x"}`},
		}},
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"shell_exec"}}})
	if warnKinds(r)["confirmation"] != 0 {
		t.Errorf("manual trigger should not warn about unattended exec: %+v", r.Warnings)
	}
}

func TestUnattended_ScheduledSafeNoWarn(t *testing.T) {
	d := Draft{
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Flow: Flow{Nodes: []sdkr.FlowNode{
			{ID: "s", Kind: "tool", Tool: "web_search", Input: `{"query":"x"}`},
			{ID: "a", Kind: "agent", Agent: "summarizer"},
		}},
	}
	r := Preflight(d, PreflightInput{Catalog: Catalog{Tools: []string{"web_search"}, Agents: []string{"summarizer"}}})
	if warnKinds(r)["confirmation"] != 0 {
		t.Errorf("safe scheduled agent should not warn: %+v", r.Warnings)
	}
}
