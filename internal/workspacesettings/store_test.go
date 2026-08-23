package workspacesettings

import (
	"path/filepath"
	"testing"
)

func TestStoreIsolatesWorkspaceSettings(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	want := Settings{
		LLM:      LLM{Studio: Studio{ProviderModel: ProviderModel{Provider: "openai", Model: "gpt-test"}, MaxBuildTokens: 1200}},
		Search:   Search{Provider: "tavily", Timeout: "45s"},
		Costs:    Costs{AlertThreshold: 0.75, Pricing: map[string]Pricing{"openai/*": {Input: 1.25, Output: 5}}},
		Ops:      Ops{SLOWindow: "24h", MaxFailureRate: 0.1, MaxP95Duration: "5m", MinRuns: 10, AlertMinStatus: "fail"},
		Profile:  Profile{Environment: "production", Owner: "workspace-team", Region: "us-central"},
		Security: Security{IntentGate: "deny"},
		Runtime:  Runtime{ToolTimeout: "30s", DefaultMaxTurns: 20, MaxAgentCallDepth: 4},
	}
	if _, err := store.Set(t.Context(), "ws_alpha", "usr_owner", want); err != nil {
		t.Fatal(err)
	}
	alpha, err := store.Get(t.Context(), "ws_alpha")
	if err != nil {
		t.Fatal(err)
	}
	if alpha.LLM.Studio.Model != "gpt-test" || alpha.Search.Provider != "tavily" || alpha.Costs.Pricing["openai/*"].Output != 5 || alpha.Ops.MinRuns != 10 || alpha.Profile.Owner != "workspace-team" || alpha.Security.IntentGate != "deny" || alpha.Runtime.DefaultMaxTurns != 20 {
		t.Fatalf("unexpected alpha settings: %#v", alpha)
	}
	beta, err := store.Get(t.Context(), "ws_beta")
	if err != nil {
		t.Fatal(err)
	}
	if beta.LLM.Studio.Model != "" || beta.Search.Provider != "" {
		t.Fatalf("workspace leak: %#v", beta)
	}
}

func TestSettingsRejectUnsafeValues(t *testing.T) {
	for _, settings := range []Settings{
		{LLM: LLM{Studio: Studio{MaxBuildCostUSD: -1}}},
		{Search: Search{Provider: "shell"}},
		{Search: Search{Timeout: "1h"}},
		{Costs: Costs{AlertThreshold: 1.1}},
		{Costs: Costs{Pricing: map[string]Pricing{"openai/*": {Input: -1}}}},
		{Ops: Ops{MaxFailureRate: 1.1}},
		{Ops: Ops{SLOWindow: "tomorrow"}},
		{Profile: Profile{Environment: "host"}},
		{Security: Security{IntentGate: "allow"}},
		{Runtime: Runtime{ToolTimeout: "0s"}},
	} {
		if err := settings.Validate(); err == nil {
			t.Fatalf("expected validation failure for %#v", settings)
		}
	}
}
