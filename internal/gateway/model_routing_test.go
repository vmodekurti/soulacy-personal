package gateway

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
)

func TestValidateRoutingPatchRejectsUnknownProvider(t *testing.T) {
	patch := RoutingPatch{DefaultMode: "auto", Preset: "balanced", MaxFailovers: 2, FailureThreshold: 3, Cooldown: "30s", Routes: map[string][]config.LLMRouteCandidate{"simple": {{Provider: "missing"}}}}
	if err := validateRoutingPatch(patch, map[string]config.ProviderConfig{"ollama": {}}); err == nil {
		t.Fatal("expected unknown provider rejection")
	}
}

func TestNormalizeRoutingPatchInheritsOmittedSafetyDefaults(t *testing.T) {
	current := config.LLMRoutingConfig{DefaultMode: "pinned", Preset: "quality", FailureThreshold: 7, Cooldown: "45s"}
	patch := normalizeRoutingPatch(RoutingPatch{}, current)
	if patch.DefaultMode != "pinned" || patch.Preset != "quality" || patch.FailureThreshold != 7 || patch.Cooldown != "45s" {
		t.Fatalf("normalized patch = %#v", patch)
	}
	if err := validateRoutingPatch(patch, nil); err != nil {
		t.Fatalf("normalized partial patch should validate: %v", err)
	}
}

func TestNormalizeRoutingPatchUsesFrameworkDefaultsWithoutCurrentValues(t *testing.T) {
	patch := normalizeRoutingPatch(RoutingPatch{}, config.LLMRoutingConfig{})
	if patch.DefaultMode != "auto" || patch.Preset != "balanced" || patch.FailureThreshold != 3 || patch.Cooldown != "30s" {
		t.Fatalf("normalized patch = %#v", patch)
	}
}

func TestHotApplyRoutingUpdatesLiveRouter(t *testing.T) {
	router := llm.NewRouter("ollama")
	s := &Server{llmRouter: router, cfg: &config.Config{LLM: config.LLMConfig{Providers: map[string]config.ProviderConfig{"ollama": {Local: true}}, Routing: config.LLMRoutingConfig{Enabled: true, DefaultMode: "auto", Preset: "local", MaxFailovers: 1, FailureThreshold: 2, Cooldown: "15s", Routes: map[string][]config.LLMRouteCandidate{"simple": {{Provider: "ollama", Model: "small"}}}}}}}
	s.hotApplyRouting()
	policy := router.RoutePolicy()
	if !policy.Enabled || policy.Cooldown != 15*time.Second || policy.Routes["simple"][0].Model != "small" || !policy.LocalProviders["ollama"] {
		t.Fatalf("policy=%#v", policy)
	}
}
