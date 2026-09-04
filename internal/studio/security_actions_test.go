package studio

// The security review's guidance has to be actionable, not merely correct.
//
// "Set security.intent_gate:deny in the SOUL.yaml" is a true sentence that
// leaves the reader hunting for a field Studio renders nowhere. Findings that
// Studio CAN fix now carry the action id and button label that let it do so.

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func findingByCategory(fs []SecurityFinding, cat string) *SecurityFinding {
	for i := range fs {
		if fs[i].Category == cat {
			return &fs[i]
		}
	}
	return nil
}

func TestSecurityPreflight_ChannelWarningOffersToRestrictChannels(t *testing.T) {
	rev := SecurityPreflight(Draft{
		Tools:    []string{"shell_exec"},
		Channels: []string{"telegram", "http"},
	}, &agent.Definition{ID: "x", Capabilities: []string{"system"}}, "")

	w := findingByCategory(rev.Warnings, "channel")
	if w == nil {
		t.Fatalf("expected a channel warning; warnings=%+v", rev.Warnings)
	}
	if w.Action != SecurityFixInternalChannelsOnly {
		t.Fatalf("channel warning should offer the restrict-channels fix, got %q", w.Action)
	}
	if strings.TrimSpace(w.ActionLabel) == "" {
		t.Fatal("an action with no label renders no button, so the fix is unreachable")
	}
	// The half Studio cannot do must still say WHERE it happens.
	if !strings.Contains(strings.ToLower(w.Fix), "delivery") {
		t.Fatalf("the fix should name the page that owns the other half, got %q", w.Fix)
	}
}

func TestSecurityPreflight_TrustWarningOffersToDenyTheIntentGate(t *testing.T) {
	rev := SecurityPreflight(Draft{
		Tools:    []string{"web_search", "shell_exec"},
		Channels: []string{"http"},
	}, &agent.Definition{ID: "x", Capabilities: []string{"system"}}, "")

	w := findingByCategory(rev.Warnings, "trust")
	if w == nil {
		t.Fatalf("expected a trust warning; warnings=%+v", rev.Warnings)
	}
	if w.Action != SecurityFixIntentGateDeny {
		t.Fatalf("trust warning should offer the intent-gate fix, got %q", w.Action)
	}
	if strings.TrimSpace(w.ActionLabel) == "" {
		t.Fatal("an action with no label renders no button, so the fix is unreachable")
	}
}

// A fix Studio cannot apply must not pretend it can — and must say where the
// change really lives instead. Capabilities are not a field on the draft.
func TestSecurityPreflight_SystemCapabilityBlockerPointsSomewhereReal(t *testing.T) {
	rev := SecurityPreflight(Draft{Tools: []string{"shell_exec"}}, &agent.Definition{ID: "no-caps"}, "")

	b := findingByCategory(rev.Blockers, "privileged")
	if b == nil {
		t.Fatalf("expected a privileged blocker; blockers=%+v", rev.Blockers)
	}
	if b.Action != "" {
		t.Fatalf("capabilities are not on the draft, so Studio must not offer a button: %q", b.Action)
	}
	if !strings.Contains(b.Fix, "capabilities") || !strings.Contains(strings.ToLower(b.Fix), "soul.yaml") {
		t.Fatalf("the fix should name the file and the field, got %q", b.Fix)
	}
}
