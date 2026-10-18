package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestPatchConfig_RunBudgetsHotApplied verifies that changing
// runtime.default_budget / runtime.max_budget via PATCH /config updates the
// live engine immediately — no gateway restart required. Runs already in
// flight keep their original limits; newly started runs use the new values.
func TestPatchConfig_RunBudgetsHotApplied(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	if s.engine == nil {
		t.Fatal("test gateway has no engine")
	}

	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"runtime":{"default_budget":{"max_tokens":750000,"max_llm_calls":75},"max_budget":{"max_tokens":3000000,"max_llm_calls":300}}}`)
	if status != http.StatusOK {
		t.Fatalf("patch budget status = %d body=%v", status, body)
	}

	def, ceil, configured := s.engine.RunBudgets()
	if !configured {
		t.Fatal("engine reports budgets not configured after patch")
	}
	if def.MaxTokens != 750000 || def.MaxLLMCalls != 75 {
		t.Fatalf("engine default budget = %d/%d, want 750000/75", def.MaxTokens, def.MaxLLMCalls)
	}
	if ceil.MaxTokens != 3000000 || ceil.MaxLLMCalls != 300 {
		t.Fatalf("engine max budget = %d/%d, want 3000000/300", ceil.MaxTokens, ceil.MaxLLMCalls)
	}

	// The in-memory config view must agree with disk so GET /config reflects
	// the live values without a restart.
	if got := s.cfg.Runtime.DefaultBudget.MaxTokens; got != 750000 {
		t.Fatalf("in-memory default_budget.max_tokens = %d, want 750000", got)
	}
	disk, err := readRawConfig(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	rt := disk["runtime"].(map[string]any)
	defBudget := rt["default_budget"].(map[string]any)
	if defBudget["max_tokens"] != 750000 {
		t.Fatalf("on-disk default_budget.max_tokens = %v, want 750000", defBudget["max_tokens"])
	}
}

// TestReloadConfig_RunBudgetsHotApplied covers the fsnotify path: editing
// config.yaml on disk and reloading must also push budgets into the engine.
func TestReloadConfig_RunBudgetsHotApplied(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	if s.engine == nil {
		t.Fatal("test gateway has no engine")
	}

	yaml := "runtime:\n  default_budget:\n    max_tokens: 1200000\n    max_llm_calls: 120\n  max_budget:\n    max_tokens: 5000000\n    max_llm_calls: 500\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := s.ReloadConfig(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	def, ceil, configured := s.engine.RunBudgets()
	if !configured || def.MaxTokens != 1200000 || def.MaxLLMCalls != 120 {
		t.Fatalf("engine default budget after reload = %v/%v, want 1200000/120", def, ceil)
	}
	if ceil.MaxTokens != 5000000 || ceil.MaxLLMCalls != 500 {
		t.Fatalf("engine max budget after reload = %v, want 5000000/500", ceil)
	}
}

func TestPatchConfigCanClearTurnLimitsAndExposeCeiling(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	for _, values := range []string{
		`{"runtime":{"default_max_turns":500,"max_turns_ceiling":900}}`,
		`{"runtime":{"default_max_turns":0,"max_turns_ceiling":0}}`,
	} {
		status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret", values)
		if status != http.StatusOK {
			t.Fatalf("PATCH status=%d body=%v", status, body)
		}
		status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
		if status != http.StatusOK {
			t.Fatalf("GET status=%d body=%v", status, body)
		}
		rt := body["runtime"].(map[string]any)
		if int(rt["max_turns_ceiling"].(float64)) != s.cfg.Runtime.MaxTurnsCeiling {
			t.Fatal("GET does not expose the configured ceiling")
		}
	}
	if s.cfg.Runtime.DefaultMaxTurns != 0 || s.cfg.Runtime.MaxTurnsCeiling != 0 {
		t.Fatal("zero was treated as omitted")
	}
	disk, err := readRawConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	rt := disk["runtime"].(map[string]any)
	if rt["default_max_turns"] != 0 || rt["max_turns_ceiling"] != 0 {
		t.Fatalf("unlimited settings not saved: %v", rt)
	}
}
