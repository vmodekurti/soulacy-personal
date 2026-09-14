package app

import (
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/runtime"
)

// buildAdaptiveMemory constructs the adaptive memory engine selected by
// cfg.Memory.Adaptive and installs it on the runtime engine. It is called at
// startup and again by the gateway when the memory.adaptive config section is
// patched, so operators can switch between the local engine and Mem0 without
// a restart.
//
// Fallback policy (Story E31): an unconfigured or invalid Mem0 provider logs
// a warning and uses the local engine. Adaptive memory is only fully off when
// enabled=false.
func (a *App) buildAdaptiveMemory(ws config.Paths, engine *runtime.Engine, stack *closerStack, ac config.AdaptiveMemoryConfig) error {
	log := a.log
	opts := runtime.AdaptiveMemoryOptions{
		Enabled:           ac.Enabled,
		ModelProvider:     strings.TrimSpace(ac.ModelProvider),
		Model:             strings.TrimSpace(ac.Model),
		MaxPromptFacts:    ac.MaxPromptFacts,
		PromptTokenBudget: ac.PromptTokenBudget,
		GraphEnabled:      ac.GraphOn(),
	}
	if !ac.Enabled {
		engine.SetAdaptiveMemory(nil, opts)
		log.Info("adaptive memory disabled by config")
		return nil
	}

	provider := strings.ToLower(strings.TrimSpace(ac.Provider))
	if provider == "mem0" {
		if !ac.Mem0Configured() {
			log.Warn("adaptive memory: mem0 selected but not configured (base_url/api_key); using the local engine")
			provider = "local"
		} else {
			m, err := memory.NewMem0Adaptive(memory.Mem0Config{
				BaseURL: ac.Mem0.BaseURL, APIKey: ac.Mem0.APIKey, EnableGraph: ac.Mem0.EnableGraph || ac.GraphOn(), APIStyle: ac.Mem0.APIStyle,
				Instructions: ac.Instructions, CustomCategories: ac.CustomCategories,
			})
			if err != nil {
				log.Warn("adaptive memory: mem0 provider invalid; using the local engine", zap.Error(err))
				provider = "local"
			} else {
				engine.SetAdaptiveMemory(m, opts)
				log.Info("adaptive memory enabled (mem0)", zap.String("base_url", ac.Mem0.BaseURL), zap.String("api_style", m.Style()), zap.Bool("graph", ac.Mem0.EnableGraph))
				return nil
			}
		}
	}
	if provider != "local" && provider != "" {
		log.Warn("adaptive memory: unknown provider; using the local engine", zap.String("provider", provider))
	}

	// Local engine. Reuse an already-open store across hot reloads so facts
	// written moments ago are never lost to a reopen.
	a.adaptiveMu.Lock()
	store := a.adaptiveStore
	if store == nil {
		var err error
		store, err = memory.OpenFactSQLite(ws.DB("adaptive-memory"))
		if err != nil {
			a.adaptiveMu.Unlock()
			return fmt.Errorf("open adaptive memory: %w", err)
		}
		a.adaptiveStore = store
		if stack != nil {
			stack.pushClose("adaptive-memory", store)
		}
	}
	a.adaptiveMu.Unlock()

	local := memory.NewLocalAdaptive(store, a.memEmbedder, engine.AdaptiveCompleter(opts.ModelProvider, opts.Model), memory.LocalOptions{
		MinConfidence:       float32(ac.MinConfidence),
		SimilarityThreshold: ac.SimilarityThreshold,
		MaxRecall:           ac.MaxPromptFacts,
		Instructions:        ac.Instructions,
		CustomCategories:    ac.CustomCategories,
		GraphDisabled:       !ac.GraphOn(),
	})
	engine.SetAdaptiveMemory(local, opts)
	log.Info("adaptive memory enabled (local)",
		zap.Bool("embeddings", a.memEmbedder != nil),
		zap.String("model_provider", opts.ModelProvider), zap.String("model", opts.Model),
		zap.Int("max_prompt_facts", opts.MaxPromptFacts), zap.Int("prompt_token_budget", opts.PromptTokenBudget))
	return nil
}
