package gateway

import (
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
)

// llmhot.go — keeping the live router in step with the llm config section.
//
// TWO OF THE THREE DIRECTIONS WERE ALREADY HOT, which is what made the third
// one dangerous rather than merely missing. Saving a provider's credentials
// re-registered it live, and so did setting its model — but deleting a
// provider removed it from the config file and from the config copy the API
// reads, and left the constructed client in the router for the life of the
// process.
//
// So a provider an operator had deliberately deleted stayed callable by every
// agent, and the providers page reported it as registered with no configuration
// behind it. Somebody removing a provider because its key leaked would have had
// every reason to believe they had removed it.
//
// The default-provider setting had the same shape one level up: editing
// llm.default_provider updated everything that consults the config and nothing
// that consults the router, so agents that name no provider kept using the old
// one indefinitely while the settings page agreed with the operator.

// applyLLMLive reconciles the router with an llm config section.
//
// Reconciles rather than applies a delta: it is given the whole section and
// works out what changed, so it is correct whether it is called after a
// single-provider edit, after a whole-file reload, or after a hand edit
// somebody made on disk. A delta-based version would need every caller to know
// what it changed, and the file watcher — the caller that matters most — does
// not.
func (s *Server) applyLLMLive(next config.LLMConfig) {
	if s == nil || s.llmRouter == nil {
		return
	}

	// The default first, because it is what a provider removal falls back to.
	// Setting it after the removals would leave a window in which the default
	// names a provider that has just been unregistered.
	if def := strings.TrimSpace(next.DefaultProvider); def != "" && def != s.llmRouter.DefaultProvider() {
		s.llmRouter.SetDefaultProvider(def)
		s.logger().Info("default LLM provider changed", zap.String("provider", def))
	}

	// Anything registered that the config no longer names is gone. Driven from
	// the ROUTER's list rather than from a remembered previous config, so it
	// also cleans up a provider removed by editing the file directly — the
	// case a handler-driven removal cannot see.
	configured := map[string]bool{}
	for id := range next.Providers {
		configured[id] = true
	}
	for _, id := range s.llmRouter.ProviderIDs() {
		if configured[id] {
			continue
		}
		// Plugin-contributed providers are registered by the plugin wiring and
		// never appear in llm.providers, so removing everything unconfigured
		// would delete them on the first config reload. They are recognised by
		// being absent from the config while the config itself is populated —
		// an empty providers map means "config not loaded", not "delete all".
		if len(next.Providers) == 0 {
			continue
		}
		if s.llmRouter.Unregister(id) {
			s.logger().Info("LLM provider unregistered because it is no longer configured",
				zap.String("provider", id))
		}
	}
}
