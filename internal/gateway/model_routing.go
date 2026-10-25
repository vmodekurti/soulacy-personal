package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
)

// normalizeRoutingPatch keeps partial and older settings clients compatible
// with routing fields added after their release. Zero/blank values are the Go
// decode result for omitted fields; inherit the live value before validation
// instead of rejecting an unrelated settings change.
func normalizeRoutingPatch(patch RoutingPatch, current config.LLMRoutingConfig) RoutingPatch {
	if strings.TrimSpace(patch.DefaultMode) == "" {
		patch.DefaultMode = current.DefaultMode
		if strings.TrimSpace(patch.DefaultMode) == "" {
			patch.DefaultMode = "auto"
		}
	}
	if strings.TrimSpace(patch.Preset) == "" {
		patch.Preset = current.Preset
		if strings.TrimSpace(patch.Preset) == "" {
			patch.Preset = "balanced"
		}
	}
	if patch.FailureThreshold == 0 {
		patch.FailureThreshold = current.FailureThreshold
		if patch.FailureThreshold == 0 {
			patch.FailureThreshold = 3
		}
	}
	if strings.TrimSpace(patch.Cooldown) == "" {
		patch.Cooldown = current.Cooldown
		if strings.TrimSpace(patch.Cooldown) == "" {
			patch.Cooldown = "30s"
		}
	}
	return patch
}

func validateRoutingPatch(patch RoutingPatch, providers map[string]config.ProviderConfig) error {
	switch strings.ToLower(strings.TrimSpace(patch.DefaultMode)) {
	case "", "auto", "fast", "strong", "pinned":
	default:
		return fmt.Errorf("llm.routing.default_mode must be auto, fast, strong, or pinned")
	}
	switch strings.ToLower(strings.TrimSpace(patch.Preset)) {
	case "", "balanced", "fast", "quality", "local":
	default:
		return fmt.Errorf("llm.routing.preset must be balanced, fast, quality, or local")
	}
	if patch.MaxFailovers < 0 || patch.MaxFailovers > 5 {
		return fmt.Errorf("llm.routing.max_failovers must be between 0 and 5")
	}
	if patch.FailureThreshold < 1 || patch.FailureThreshold > 100 {
		return fmt.Errorf("llm.routing.failure_threshold must be between 1 and 100")
	}
	if cooldown, err := time.ParseDuration(patch.Cooldown); err != nil || cooldown <= 0 {
		return fmt.Errorf("llm.routing.cooldown must be a positive duration such as 30s")
	}
	for class, candidates := range patch.Routes {
		switch class {
		case "simple", "standard", "complex":
		default:
			return fmt.Errorf("llm.routing.routes contains unsupported class %q", class)
		}
		if err := validateRouteCandidates(candidates, providers, "llm.routing.routes."+class); err != nil {
			return err
		}
	}
	return validateRouteCandidates(patch.Fallbacks, providers, "llm.routing.fallbacks")
}

func validateRouteCandidates(candidates []config.LLMRouteCandidate, providers map[string]config.ProviderConfig, field string) error {
	if len(candidates) > 16 {
		return fmt.Errorf("%s supports at most 16 candidates", field)
	}
	seen := make(map[string]bool)
	for i, candidate := range candidates {
		provider, model := strings.TrimSpace(candidate.Provider), strings.TrimSpace(candidate.Model)
		if provider == "" {
			return fmt.Errorf("%s[%d].provider is required", field, i)
		}
		if _, ok := providers[provider]; !ok {
			return fmt.Errorf("%s[%d] refers to unconfigured provider %q", field, i, provider)
		}
		if len(provider) > 256 || len(model) > 256 {
			return fmt.Errorf("%s[%d] provider/model is too long", field, i)
		}
		key := provider + "\x00" + model
		if seen[key] {
			return fmt.Errorf("%s contains duplicate provider/model %q", field, provider+"/"+model)
		}
		seen[key] = true
	}
	return nil
}

func (s *Server) hotApplyRouting() {
	if s.llmRouter == nil || s.cfg == nil {
		return
	}
	configured := s.cfg.LLM.Routing
	routes := make(map[string][]llm.RouteCandidate, len(configured.Routes))
	for class, candidates := range configured.Routes {
		for _, candidate := range candidates {
			routes[class] = append(routes[class], llm.RouteCandidate{Provider: candidate.Provider, Model: candidate.Model})
		}
	}
	fallbacks := make([]llm.RouteCandidate, 0, len(configured.Fallbacks))
	for _, candidate := range configured.Fallbacks {
		fallbacks = append(fallbacks, llm.RouteCandidate{Provider: candidate.Provider, Model: candidate.Model})
	}
	locals := make(map[string]bool, len(s.cfg.LLM.Providers))
	regions := make(map[string]string, len(s.cfg.LLM.Providers))
	for id, provider := range s.cfg.LLM.Providers {
		locals[id] = provider.Local || id == "ollama"
		regions[id] = provider.Region
	}
	cooldown, _ := time.ParseDuration(configured.Cooldown)
	s.llmRouter.SetRoutePolicy(llm.RoutePolicy{Enabled: configured.Enabled, DefaultMode: configured.DefaultMode, Preset: configured.Preset, MaxFailovers: configured.MaxFailovers, FailureThreshold: configured.FailureThreshold, Cooldown: cooldown, Routes: routes, Fallbacks: fallbacks, LocalProviders: locals, AllowedProviders: s.cfg.LLM.AllowedProviders, AllowedModels: s.cfg.LLM.AllowedModels, AllowedRegions: s.cfg.LLM.AllowedRegions, ProviderRegions: regions})
}
