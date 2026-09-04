package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/studio"
)

// Saving the rulebook must produce a VERSION, not overwrite a file. Deployment
// records pin a RulesVersion hash, and before the store was adopted that hash
// pointed at text nothing could retrieve — the audit pointer dangled.
func TestSaveRulesRecordsAVersion(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	s, _ := newTestGatewayWithLLM(t, "secret")

	body := `{"rules":"# House rules\n\nAlways set a timeout.","note":"tighten timeouts"}`
	status, res := gatewayJSON(t, s, http.MethodPut, "/api/v1/studio/rules", "secret", body)
	if status != http.StatusOK {
		t.Fatalf("save status = %d body=%v", status, res)
	}
	if res["version"] == nil || res["hash"] == nil {
		t.Fatalf("save did not report a stored version: %v", res)
	}

	status, hist := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/rules/history", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("history status = %d body=%v", status, hist)
	}
	raw, _ := json.Marshal(hist["versions"])
	var versions []studio.RulesMeta
	if err := json.Unmarshal(raw, &versions); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("history has %d versions, want 1: %s", len(versions), raw)
	}
	if versions[0].Note != "tighten timeouts" {
		t.Errorf("note not recorded, got %q", versions[0].Note)
	}
	if strings.TrimSpace(versions[0].Author) == "" {
		t.Error("author not recorded; an audit entry with no actor answers nothing")
	}
}

// The effective rulebook must come back from the store, since that is what gets
// injected into every subsequent generation and fix.
func TestSavedRulesBecomeTheEffectiveRules(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	s, _ := newTestGatewayWithLLM(t, "secret")
	const rules = "# House rules\n\nNever call shell_exec."

	body, _ := json.Marshal(map[string]string{"rules": rules})
	if status, res := gatewayJSON(t, s, http.MethodPut, "/api/v1/studio/rules", "secret", string(body)); status != http.StatusOK {
		t.Fatalf("save status = %d body=%v", status, res)
	}
	if got := s.soulRules(); got != rules {
		t.Errorf("soulRules() = %q, want the saved text", got)
	}

	status, res := gatewayJSON(t, s, http.MethodGet, "/api/v1/studio/rules", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get status = %d", status)
	}
	if res["rules"] != rules {
		t.Errorf("GET returned %q, want the saved text", res["rules"])
	}
	if res["isDefault"] != false {
		t.Error("saved rules should not report as the built-in default")
	}
}

// Reset must not destroy history. The previous implementation os.Remove'd the
// only copy, so an empty PUT — which the GUI sends for "reset to default" —
// discarded the rulebook irrecoverably.
func TestResetKeepsThePreviousVersionRecoverable(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	s, _ := newTestGatewayWithLLM(t, "secret")
	const original = "# House rules\n\nAlways pin a model."

	body, _ := json.Marshal(map[string]string{"rules": original})
	if status, _ := gatewayJSON(t, s, http.MethodPut, "/api/v1/studio/rules", "secret", string(body)); status != http.StatusOK {
		t.Fatal("seed save failed")
	}
	if status, _ := gatewayJSON(t, s, http.MethodPut, "/api/v1/studio/rules", "secret", `{"rules":""}`); status != http.StatusOK {
		t.Fatal("reset failed")
	}

	if got := s.soulRules(); got != studio.DefaultSOULRules {
		t.Error("reset did not restore the built-in default")
	}

	dir, err := s.soulRulesDir()
	if err != nil {
		t.Fatalf("rules dir: %v", err)
	}
	versions, err := studio.RulesHistory(dir)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(versions) < 2 {
		t.Fatalf("reset left %d version(s); the pre-reset rulebook must remain recoverable", len(versions))
	}
	// The original text must still be retrievable by its version.
	var found bool
	for _, v := range versions {
		rec, rerr := studio.RulesAt(dir, v.Version)
		if rerr == nil && rec.Rules == original {
			found = true
		}
	}
	if !found {
		t.Error("the rulebook in force before the reset is no longer retrievable")
	}
}

// An unchanged re-save must not manufacture audit noise.
func TestResavingIdenticalRulesDoesNotAddAVersion(t *testing.T) {
	t.Setenv("SOULACY_WORKSPACE", t.TempDir())
	s, _ := newTestGatewayWithLLM(t, "secret")
	body, _ := json.Marshal(map[string]string{"rules": "# House rules\n\nBe careful."})

	for i := 0; i < 3; i++ {
		if status, _ := gatewayJSON(t, s, http.MethodPut, "/api/v1/studio/rules", "secret", string(body)); status != http.StatusOK {
			t.Fatalf("save %d failed", i)
		}
	}
	dir, _ := s.soulRulesDir()
	versions, err := studio.RulesHistory(dir)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(versions) != 1 {
		t.Errorf("three identical saves produced %d versions, want 1", len(versions))
	}
}
