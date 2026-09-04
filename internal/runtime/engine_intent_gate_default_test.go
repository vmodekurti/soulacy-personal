// engine_intent_gate_default_test.go — Cohort F-Bridge coverage for the
// workspace-scoped intent-gate default. Pins the resolver's truth table
// (per-agent > workspace > empty) and confirms the engine actually
// consults the workspace default from evaluateIntent.
package runtime

import (
	"testing"

	"github.com/soulacy/soulacy/internal/injection"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestResolveIntentGate_TruthTable(t *testing.T) {
	// Cases: {perAgentMode, workspaceDefault, wantEffective}.
	cases := []struct {
		name       string
		perAgent   string
		workspace  string
		wantEffect string
	}{
		{"both empty falls through", "", "", ""},
		{"workspace wins when per-agent empty", "", "deny", "deny"},
		{"per-agent wins when both set", "off", "deny", "off"},
		{"per-agent wins over empty workspace", "prompt", "", "prompt"},
		{"trimmed per-agent still wins", " prompt ", "deny", "prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{}
			e.SetIntentGateDefault(tc.workspace)
			def := &agent.Definition{Security: &agent.SecurityConfig{IntentGate: tc.perAgent}}
			if got := e.ResolveIntentGate(def); got != tc.wantEffect {
				t.Fatalf("ResolveIntentGate = %q, want %q (perAgent=%q workspace=%q)",
					got, tc.wantEffect, tc.perAgent, tc.workspace)
			}
		})
	}
}

func TestResolveIntentGate_NilSecurityUsesWorkspace(t *testing.T) {
	e := &Engine{}
	e.SetIntentGateDefault("deny")
	// Definition with no Security block at all — the resolver must not panic
	// and must return the workspace default.
	if got := e.ResolveIntentGate(&agent.Definition{}); got != "deny" {
		t.Fatalf("nil Security path returned %q, want deny", got)
	}
	if got := e.ResolveIntentGate(nil); got != "deny" {
		t.Fatalf("nil definition returned %q, want deny", got)
	}
}

// TestEvaluateIntent_WorkspaceDefaultAppliesWhenPerAgentEmpty is the
// end-to-end pin: an agent with no per-agent IntentGate configured still
// gets Deny semantics when the workspace default is "deny". Uses the
// same shape as TestIntentGate_DeniesInjectionSteeredChannelSend but
// leaves def.Security.IntentGate empty so the workspace fallback fires.
func TestEvaluateIntent_WorkspaceDefaultAppliesWhenPerAgentEmpty(t *testing.T) {
	e := &Engine{}
	e.SetIntentGateDefault("deny")

	// A minimal Definition with an empty per-agent IntentGate. HighRisk tool
	// so evaluateIntent runs its evaluation branch.
	def := &agent.Definition{ID: "test", Security: &agent.SecurityConfig{IntentGate: ""}}
	sessionID := "s-1"

	// Seed a session where the last untrusted evidence flagged a High-severity
	// injection under fetch_url and the user's goal doesn't mention shell.
	e.sessions.Store("test|"+sessionID, &Session{
		userGoal:              "please summarize the article",
		lastEvidenceUntrusted: true,
		injectionMax:          injection.SeverityHigh,
		injectionLastSource:   "fetch_url",
	})

	ran, ev := e.evaluateIntent(def, sessionID, message.ToolCall{Name: "shell_exec"})
	if !ran {
		t.Fatalf("evaluateIntent returned ran=false for high-risk tool")
	}
	if ev.Decision.String() != "deny" {
		t.Fatalf("workspace-default deny not applied: decision=%s reason=%q",
			ev.Decision.String(), ev.Reason)
	}
	if !ev.InjectionInfluenced {
		t.Fatalf("expected InjectionInfluenced=true; ev=%+v", ev)
	}
}

// TestEvaluateIntent_PerAgentOverridesWorkspace pins the counterpoint: an
// agent with explicit "off" overrides a workspace default of "deny", so the
// gate must return Allow.
func TestEvaluateIntent_PerAgentOverridesWorkspace(t *testing.T) {
	e := &Engine{}
	e.SetIntentGateDefault("deny")

	def := &agent.Definition{ID: "test", Security: &agent.SecurityConfig{IntentGate: "off"}}
	sessionID := "s-2"
	e.sessions.Store("test|"+sessionID, &Session{
		userGoal:              "please summarize the article",
		lastEvidenceUntrusted: true,
		injectionMax:          injection.SeverityHigh,
		injectionLastSource:   "fetch_url",
	})

	ran, ev := e.evaluateIntent(def, sessionID, message.ToolCall{Name: "shell_exec"})
	if !ran {
		t.Fatalf("evaluateIntent returned ran=false")
	}
	if ev.Decision.String() != "allow" {
		t.Fatalf("per-agent 'off' override not honoured: decision=%s reason=%q",
			ev.Decision.String(), ev.Reason)
	}
}
