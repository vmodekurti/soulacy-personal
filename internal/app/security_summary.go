package app

import (
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
)

func effectiveSecuritySummary(cfg *config.Config, authEffective bool) map[string]any {
	mode := strings.ToLower(strings.TrimSpace(cfg.Runtime.Sandbox.Mode))
	if !cfg.Runtime.Sandbox.Enabled {
		mode = "unsandboxed"
	}
	return map[string]any{
		"deployment_profile":    strings.ToLower(strings.TrimSpace(cfg.Deployment.Profile)),
		"bind_host":             cfg.Server.Host,
		"auth_mode":             cfg.Auth.Mode,
		"auth_effective":        authEffective,
		"allow_unauthenticated": cfg.Server.AllowUnauthenticated,
		"ssrf_protection":       cfg.Runtime.SSRFProtection,
		"privileged_isolation":  mode,
		"system_agent_grants":   len(cfg.Runtime.AllowSystemAgents),
		"filesystem_root_count": len(cfg.Runtime.FilesystemRoots),
		"default_budget_tokens": cfg.Runtime.DefaultBudget.MaxTokens,
		"default_budget_calls":  cfg.Runtime.DefaultBudget.MaxLLMCalls,
	}
}

func logEffectiveSecuritySummary(log *zap.Logger, cfg *config.Config, authEffective bool) {
	s := effectiveSecuritySummary(cfg, authEffective)
	log.Info("effective security configuration (secret values omitted)",
		zap.String("deployment_profile", s["deployment_profile"].(string)),
		zap.String("bind_host", s["bind_host"].(string)),
		zap.String("auth_mode", s["auth_mode"].(string)),
		zap.Bool("auth_effective", s["auth_effective"].(bool)),
		zap.Bool("allow_unauthenticated", s["allow_unauthenticated"].(bool)),
		zap.Bool("ssrf_protection", s["ssrf_protection"].(bool)),
		zap.String("privileged_isolation", s["privileged_isolation"].(string)),
		zap.Int("system_agent_grants", s["system_agent_grants"].(int)),
		zap.Int("filesystem_root_count", s["filesystem_root_count"].(int)),
		zap.Int("default_budget_tokens", s["default_budget_tokens"].(int)),
		zap.Int("default_budget_calls", s["default_budget_calls"].(int)),
	)
}
