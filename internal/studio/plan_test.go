package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// privilegedDraft builds a draft whose flow uses a privileged builtin
// (write_file, from internal/tier's privilegedBuiltins) and binds a channel.
func privilegedDraft() Draft {
	return Draft{
		Name:     "Disk Writer Bot",
		Trigger:  Trigger{Type: "channel"},
		Channels: []string{"telegram"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "write", Kind: "tool", Tool: "write_file", Input: "hello", Output: "ok", X: 0, Y: 0},
			},
			Edges: []sdkr.FlowEdge{
				{From: "write", To: "end"},
			},
			Entry: "write",
		},
	}
}

// readOnlyDraft builds a draft whose flow uses no builtins (a pure agent
// node) and binds a channel — it should classify ReadOnly (no builtins, no
// MCP, no peers resolved via lookup) and need no consent.
func readOnlyDraft() Draft {
	return Draft{
		Name:     "Chatty Bot",
		Trigger:  Trigger{Type: "channel"},
		Channels: []string{"telegram"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "reply", Kind: "agent", Agent: "responder", Input: "{{.input}}", Output: "out", X: 0, Y: 0},
			},
			Edges: []sdkr.FlowEdge{
				{From: "reply", To: "end"},
			},
			Entry: "reply",
		},
	}
}

func TestPlan_PrivilegedWithChannel_RequiresConsent(t *testing.T) {
	res, err := Plan(privilegedDraft())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Tier != "privileged" {
		t.Fatalf("tier = %q, want privileged", res.Tier)
	}
	if !res.RequiresConsent {
		t.Fatalf("requiresConsent = false, want true")
	}
	if len(res.ConsentItems) != 1 {
		t.Fatalf("consentItems = %d, want 1", len(res.ConsentItems))
	}
	ci := res.ConsentItems[0]
	if ci.Kind != "channel" || ci.Name != "telegram" {
		t.Errorf("consent item = %+v, want kind=channel name=telegram", ci)
	}
	// The reason must surface the privileged builtin from tier.Explain.
	if !strings.Contains(ci.Reason, "write_file") {
		t.Errorf("consent reason = %q, want it to mention write_file", ci.Reason)
	}
	// The plan's top-level reasons mirror the tier reasons.
	if len(res.Reasons) == 0 || !strings.Contains(strings.Join(res.Reasons, " "), "write_file") {
		t.Errorf("reasons = %v, want a write_file reason", res.Reasons)
	}
}

func TestPlan_ReadOnlyWithChannel_NoConsent(t *testing.T) {
	res, err := Plan(readOnlyDraft())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Tier != "readonly" {
		t.Fatalf("tier = %q, want readonly", res.Tier)
	}
	if res.RequiresConsent {
		t.Errorf("requiresConsent = true, want false for a read-only draft")
	}
	if len(res.ConsentItems) != 0 {
		t.Errorf("consentItems = %v, want empty", res.ConsentItems)
	}
}

func TestPlan_PrivilegedNoChannel_NoConsent(t *testing.T) {
	d := privilegedDraft()
	d.Channels = nil // privileged but nothing to expose it on
	res, err := Plan(d)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Tier != "privileged" {
		t.Fatalf("tier = %q, want privileged", res.Tier)
	}
	if res.RequiresConsent {
		t.Errorf("requiresConsent = true, want false (no channel bound)")
	}
	if len(res.ConsentItems) != 0 {
		t.Errorf("consentItems = %v, want empty", res.ConsentItems)
	}
}

func TestTierLabel_Mapping(t *testing.T) {
	// readonly maps from tier.ReadOnly (whose String() is "read_only").
	cases := []struct {
		draft Draft
		want  string
	}{
		{readOnlyDraft(), "readonly"},
		{privilegedDraft(), "privileged"},
	}
	for _, tc := range cases {
		res, err := Plan(tc.draft)
		if err != nil {
			t.Fatalf("Plan(%s): %v", tc.draft.Name, err)
		}
		if res.Tier != tc.want {
			t.Errorf("tier for %q = %q, want %q", tc.draft.Name, res.Tier, tc.want)
		}
	}
}

// ── Save consent threading ───────────────────────────────────────────────────

func TestToAgentDefinition_ConsentStampsLabel(t *testing.T) {
	// Privileged draft saved WITH consent → label present, Enabled=false,
	// and the privileged builtin is projected onto def.Builtins.
	def, err := ToAgentDefinition(privilegedDraft(), true)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Enabled {
		t.Errorf("Enabled = true, want false (Studio saves are staged)")
	}
	if def.Labels[StudioPrivilegeAckLabel] != "true" {
		t.Errorf("label %q = %q, want \"true\"", StudioPrivilegeAckLabel, def.Labels[StudioPrivilegeAckLabel])
	}
	if !containsString(def.Capabilities, "system") {
		t.Errorf("Capabilities = %v, want system for approved privileged workflow", def.Capabilities)
	}
	if def.Builtins == nil {
		t.Fatalf("Builtins nil, want write_file projected from the flow")
	}
	found := false
	for _, b := range *def.Builtins {
		if b == "write_file" {
			found = true
		}
	}
	if !found {
		t.Errorf("Builtins = %v, want it to contain write_file", *def.Builtins)
	}
}

func TestToAgentDefinition_NoConsent_NoLabel(t *testing.T) {
	// Privileged draft saved WITHOUT consent → Plan still reports
	// requiresConsent, and the produced definition carries no consent label.
	plan, err := Plan(privilegedDraft())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.RequiresConsent {
		t.Fatalf("requiresConsent = false, want true")
	}
	def, err := ToAgentDefinition(privilegedDraft(), false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if _, ok := def.Labels[StudioPrivilegeAckLabel]; ok {
		t.Errorf("label %q present without consent, want absent", StudioPrivilegeAckLabel)
	}
	if containsString(def.Capabilities, "system") {
		t.Errorf("Capabilities = %v, want no system capability without consent", def.Capabilities)
	}
}

func TestToAgentDefinition_ConsentNoChannel_NoLabel(t *testing.T) {
	// Consent given but no channel bound → no privileged exposure exists, so
	// no stray label is written.
	d := privilegedDraft()
	d.Channels = nil
	def, err := ToAgentDefinition(d, true)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if _, ok := def.Labels[StudioPrivilegeAckLabel]; ok {
		t.Errorf("label present with no channel bound, want absent")
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
