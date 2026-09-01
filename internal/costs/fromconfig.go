package costs

import (
	"time"

	"github.com/soulacy/soulacy/internal/config"
)

// fromconfig.go — one translation from config.yaml to admission settings.
//
// WHY IT MOVED HERE. Boot built this struct inline in the wiring code, so the
// reload path had no way to rebuild it without copying thirty field mappings —
// and a copy is exactly how the two drift. When they drift, the running
// process enforces a policy the operator cannot see anywhere: not the file,
// and not the boot code either. One function, called from both, makes drift
// impossible rather than unlikely.

// GovernanceFrom builds the governor's admission settings from a whole config.
// It reads costs, llm and rate_limit, because a single admission decision uses
// all three and splitting it by config section would put the seam in the wrong
// place.
func GovernanceFrom(cfg *config.Config) GovernanceConfig {
	if cfg == nil {
		return GovernanceConfig{}
	}
	reservationTTL, err := time.ParseDuration(cfg.Costs.ReservationTTL)
	if err != nil || reservationTTL <= 0 {
		reservationTTL = 15 * time.Minute
	}
	circuitCooldown, err := time.ParseDuration(cfg.Costs.CircuitCooldown)
	if err != nil || circuitCooldown <= 0 {
		circuitCooldown = 30 * time.Second
	}
	policies := make(map[string]ProviderPolicy, len(cfg.LLM.Providers))
	for id, providerCfg := range cfg.LLM.Providers {
		policies[id] = ProviderPolicy{
			AllowedDataClasses:      providerCfg.AllowedDataClasses,
			CacheAllowedDataClasses: providerCfg.CacheAllowedDataClasses,
			MaxTokensPerMinute:      providerCfg.MaxTokensPerMinute,
			Region:                  providerCfg.Region,
			Retention:               providerCfg.Retention,
			PromptCaching:           providerCfg.PromptCaching,
		}
	}
	perUserTokensWorkspaceID := ""
	if cfg.PublicDemo.Enabled {
		perUserTokensWorkspaceID = cfg.PublicDemo.WorkspaceID
	}
	return GovernanceConfig{
		DailyBudgetUSD:           cfg.Costs.DailyBudgetUSD,
		MonthlyBudgetUSD:         cfg.Costs.MonthlyBudgetUSD,
		PerUserDailyBudgetUSD:    cfg.Costs.PerUserDailyBudgetUSD,
		PerAgentDailyBudgetUSD:   cfg.Costs.PerAgentDailyBudgetUSD,
		EnforcementMode:          cfg.Costs.EnforcementMode,
		UnknownPricing:           cfg.Costs.UnknownPricing,
		DefaultMaxOutput:         cfg.Costs.DefaultMaxOutputTokens,
		MaxOutputCeiling:         cfg.Costs.MaxOutputTokensCeiling,
		ConfirmationThresholdUSD: cfg.Costs.ConfirmationThresholdUSD,
		ReservationTTL:           reservationTTL,
		PerUserTokensDay:         cfg.RateLimit.PerUserTokensDay,
		PerUserTokensWorkspaceID: perUserTokensWorkspaceID,
		PerAgentTokensDay:        cfg.RateLimit.PerAgentTokensDay,
		AllowedProviders:         cfg.LLM.AllowedProviders,
		AllowedModels:            cfg.LLM.AllowedModels,
		AllowedRegions:           cfg.LLM.AllowedRegions,
		MaxConcurrentPerProvider: cfg.Costs.MaxConcurrentPerProvider,
		CircuitFailureThreshold:  cfg.Costs.CircuitFailureThreshold,
		CircuitCooldown:          circuitCooldown,
		ProviderPolicies:         policies,
	}
}
