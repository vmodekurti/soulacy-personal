// security_preflight_test.go — S6 (Cohort F) tests for the Studio
// security preflight and the buildloop privileged-regression guard.
package studio

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// TestSecurityPreflight_CleanDraftReturnsOK is the zero-state: a
// draft with only a web_search tool + http channel produces no
// blockers or warnings, and no privileged tools appear in the summary.
func TestSecurityPreflight_CleanDraftReturnsOK(t *testing.T) {
	rev := SecurityPreflight(Draft{
		Tools:    []string{"web_search"},
		Channels: []string{"http"},
	}, nil, "")
	if !rev.OK {
		t.Fatalf("expected OK; got blockers=%+v", rev.Blockers)
	}
	if len(rev.Blockers) != 0 {
		t.Errorf("unexpected blockers on clean draft: %+v", rev.Blockers)
	}
	if len(rev.Summary.PrivilegedTools) != 0 {
		t.Errorf("clean draft privileged summary should be empty: %+v", rev.Summary.PrivilegedTools)
	}
	// web_search is untrusted; the summary should reflect that.
	if !containsStrSlice(rev.Summary.UntrustedContentSources, "web_search") {
		t.Errorf("web_search should be in UntrustedContentSources; got %v", rev.Summary.UntrustedContentSources)
	}
}

// TestSecurityPreflight_BlocksPrivilegedWithoutSystemCapability pins
// the AC: a workflow that includes shell_exec / write_file but the
// underlying agent doesn't declare capabilities:[system] is refused
// at Save.
func TestSecurityPreflight_BlocksPrivilegedWithoutSystemCapability(t *testing.T) {
	def := &agent.Definition{ID: "no-caps"}
	rev := SecurityPreflight(Draft{
		ID:    "no-caps",
		Tools: []string{"shell_exec", "write_file"},
	}, def, "")
	if rev.OK {
		t.Fatalf("expected block for missing system capability; got OK")
	}
	if len(rev.Blockers) == 0 {
		t.Fatal("expected at least one blocker")
	}
	found := false
	for _, b := range rev.Blockers {
		if b.Category == "privileged" && strings.Contains(b.Message, "system") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'privileged' blocker naming system capability; got %+v", rev.Blockers)
	}
}

// TestSecurityPreflight_AllowsPrivilegedWhenSystemDeclared is the
// counterpoint: same tools, but the def declares capabilities:[system].
// The block goes away; a warning about privileged-channel-exposure
// may still appear if the draft targets a shared channel.
func TestSecurityPreflight_AllowsPrivilegedWhenSystemDeclared(t *testing.T) {
	def := &agent.Definition{
		ID:           "sys-caps",
		Capabilities: []string{"system"},
	}
	rev := SecurityPreflight(Draft{
		ID:       "sys-caps",
		Tools:    []string{"shell_exec"},
		Channels: []string{"http"},
	}, def, "")
	if !rev.OK {
		t.Fatalf("privileged tool with declared capability should pass; got blockers=%+v", rev.Blockers)
	}
	if !rev.Summary.DeclaresSystemCapability {
		t.Error("expected DeclaresSystemCapability=true")
	}
}

// TestSecurityPreflight_WarnsPrivilegedOnSharedChannel pins the AC:
// a workflow with privileged tools exposed on a shared channel
// (Telegram/Slack/etc.) produces a channel-category warning telling
// the operator to confirm every binding sets accept_privileged_exposure.
func TestSecurityPreflight_WarnsPrivilegedOnSharedChannel(t *testing.T) {
	def := &agent.Definition{
		ID:           "expo",
		Capabilities: []string{"system"},
	}
	rev := SecurityPreflight(Draft{
		ID:       "expo",
		Tools:    []string{"write_file"},
		Channels: []string{"telegram"},
	}, def, "")
	if !rev.OK {
		t.Fatalf("draft should not block on channel warning; got %+v", rev.Blockers)
	}
	if !rev.Summary.PrivilegedChannelExposure {
		t.Error("expected PrivilegedChannelExposure=true")
	}
	found := false
	for _, w := range rev.Warnings {
		if w.Category == "channel" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected channel warning; got %+v", rev.Warnings)
	}
}

// TestSecurityPreflight_WarnsWhenIngestionMeetsPrivilegedTools pins the
// injection-flow warning: fetch_url + shell_exec in the same draft is
// the classic prompt-injection pipeline.
func TestSecurityPreflight_WarnsWhenIngestionMeetsPrivilegedTools(t *testing.T) {
	def := &agent.Definition{
		ID:           "risky",
		Capabilities: []string{"system"},
	}
	rev := SecurityPreflight(Draft{
		ID:    "risky",
		Tools: []string{"fetch_url", "shell_exec"},
	}, def, "")
	found := false
	for _, w := range rev.Warnings {
		if w.Category == "trust" && strings.Contains(w.Message, "intent gate") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected trust warning for injection pipeline; got %+v", rev.Warnings)
	}
}

func TestSecurityPreflight_UsesDraftGeneratedGuardrails(t *testing.T) {
	rev := SecurityPreflight(Draft{
		Tools:        []string{"web_search", "channel.send"},
		Channels:     []string{"telegram"},
		ConfirmTools: []string{"channel.send"},
		Security:     &agent.SecurityConfig{IntentGate: "deny"},
	}, nil, "")
	if !containsStrSlice(rev.Summary.ConfirmTools, "channel.send") {
		t.Fatalf("draft confirm_tools not reflected in preflight summary: %#v", rev.Summary.ConfirmTools)
	}
	if rev.Summary.IntentGateMode != "deny" {
		t.Fatalf("draft security.intent_gate not reflected: %q", rev.Summary.IntentGateMode)
	}
	for _, w := range rev.Warnings {
		if w.Category == "trust" {
			t.Fatalf("deny intent gate should suppress 'consider deny' trust warning: %+v", rev.Warnings)
		}
	}
}

// TestSecurityPreflight_RecommendsScopedAlternatives verifies that
// safer alternatives are surfaced separately from warnings so
// operators can pick them without treating them as errors.
func TestSecurityPreflight_RecommendsScopedAlternatives(t *testing.T) {
	def := &agent.Definition{
		ID:           "raw-tools",
		Capabilities: []string{"system"},
	}
	rev := SecurityPreflight(Draft{
		ID:    "raw-tools",
		Tools: []string{"shell_exec", "write_file", "http_request"},
	}, def, "")
	if len(rev.Recommendations) == 0 {
		t.Fatal("expected at least one recommendation")
	}
	suggestions := map[string]bool{}
	for _, r := range rev.Recommendations {
		suggestions[r.From] = true
	}
	if !suggestions["shell_exec"] || !suggestions["http_request"] || !suggestions["write_file"] {
		t.Errorf("expected recommendations for shell_exec + http_request + write_file; got %+v", rev.Recommendations)
	}
}

// TestDetectPrivilegedRegression_AddedToolFlagged pins the S6 buildloop
// guard: an LLM repair that introduces shell_exec into a draft that
// didn't have it before must be rejected.
func TestDetectPrivilegedRegression_AddedToolFlagged(t *testing.T) {
	before := Draft{Tools: []string{"web_search"}}
	after := Draft{Tools: []string{"web_search", "shell_exec"}}
	blocked, reason := detectPrivilegedRegression(before, after)
	if !blocked {
		t.Fatal("expected added privileged tool to be blocked")
	}
	if !strings.Contains(reason, "shell_exec") {
		t.Errorf("reason should name the tool; got %q", reason)
	}
}

// TestDetectPrivilegedRegression_AddedChannelFlagged pins the same
// guard on the channel side.
func TestDetectPrivilegedRegression_AddedChannelFlagged(t *testing.T) {
	before := Draft{Channels: []string{"http"}}
	after := Draft{Channels: []string{"http", "telegram"}}
	blocked, reason := detectPrivilegedRegression(before, after)
	if !blocked {
		t.Fatal("expected added shared-channel exposure to be blocked")
	}
	if !strings.Contains(reason, "telegram") {
		t.Errorf("reason should name the channel; got %q", reason)
	}
}

// TestDetectPrivilegedRegression_UnchangedIsSafe pins the negative:
// repair that only changes non-privileged fields is allowed to proceed.
func TestDetectPrivilegedRegression_UnchangedIsSafe(t *testing.T) {
	before := Draft{Tools: []string{"web_search", "read_file"}}
	after := Draft{Tools: []string{"web_search", "read_file", "kb_search"}}
	blocked, reason := detectPrivilegedRegression(before, after)
	if blocked {
		t.Errorf("safe repair should not be blocked; reason=%q", reason)
	}
}

// F-Bridge — the review summary picks up the workspace intent-gate default
// when the per-agent security block is empty. Also verifies that an explicit
// per-agent value continues to win.
func TestSecurityPreflight_WorkspaceIntentGateDefaultAppliedInSummary(t *testing.T) {
	// Per-agent empty → workspace default surfaces in the summary.
	rev := SecurityPreflight(Draft{Tools: []string{"web_search"}}, &agent.Definition{ID: "wsdef"}, "deny")
	if rev.Summary.IntentGateMode != "deny" {
		t.Errorf("workspace default not applied: IntentGateMode=%q, want deny", rev.Summary.IntentGateMode)
	}
	// Per-agent explicit → per-agent wins even when workspace differs.
	def := &agent.Definition{
		ID:       "perAgent",
		Security: &agent.SecurityConfig{IntentGate: "prompt"},
	}
	rev2 := SecurityPreflight(Draft{Tools: []string{"web_search"}}, def, "deny")
	if rev2.Summary.IntentGateMode != "prompt" {
		t.Errorf("per-agent value should win: IntentGateMode=%q, want prompt", rev2.Summary.IntentGateMode)
	}
	// Both empty → display sentinel is preserved.
	rev3 := SecurityPreflight(Draft{Tools: []string{"web_search"}}, &agent.Definition{ID: "empty"}, "")
	if rev3.Summary.IntentGateMode != "prompt (default)" {
		t.Errorf("empty fallback lost: IntentGateMode=%q, want 'prompt (default)'", rev3.Summary.IntentGateMode)
	}
}
