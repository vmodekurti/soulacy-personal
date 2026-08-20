// validate_tokenquota_test.go — a daily token quota that cannot fire is a
// configuration error, not a warning.
//
// The setting used to have two implementations. The one in internal/ratelimit
// was mounted on every chat route and read an in-memory bucket nothing ever
// filled, so it compared zero against the limit and allowed every request. An
// operator got a config line, a middleware they could see in the route table,
// a status endpoint reporting usage, and no quota — for as long as they
// believed it. That implementation is gone; this refusal is what stops its
// replacement failing the same silent way, because the surviving one only
// rejects when costs.enforcement_mode is "hard".
package config

import (
	"strings"
	"testing"
)

func quotaConfig(userTokens, agentTokens int, mode string) *Config {
	cfg := &Config{}
	cfg.RateLimit.PerUserTokensDay = userTokens
	cfg.RateLimit.PerAgentTokensDay = agentTokens
	cfg.Costs.EnforcementMode = mode
	return cfg
}

// quotaError extracts just the token-quota complaint, so these tests are not
// coupled to whatever else an empty Config fails validation for.
func quotaError(t *testing.T, cfg *Config) string {
	t.Helper()
	err := cfg.Validate()
	if err == nil {
		return ""
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.Contains(line, "tokens_day") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func TestATokenQuotaWithoutHardEnforcementIsRefused(t *testing.T) {
	for _, mode := range []string{"", "soft", "off", "SOFT"} {
		got := quotaError(t, quotaConfig(100_000, 0, mode))
		if got == "" {
			t.Errorf("enforcement_mode %q accepted a per-user token quota that can never fire", mode)
			continue
		}
		// The message has to name the setting AND the remedy. "invalid
		// configuration" sends an operator to read the whole file.
		if !strings.Contains(got, "ratelimit.per_user_tokens_day") {
			t.Errorf("mode %q: the error does not name the setting: %s", mode, got)
		}
		if !strings.Contains(got, "enforcement_mode: hard") {
			t.Errorf("mode %q: the error does not name the remedy: %s", mode, got)
		}
	}
	// The per-agent quota is named on its own when it is the one set, rather
	// than the message always blaming the per-user key.
	got := quotaError(t, quotaConfig(0, 50_000, "soft"))
	if !strings.Contains(got, "ratelimit.per_agent_tokens_day") {
		t.Fatalf("a per-agent-only quota was reported against the wrong key: %s", got)
	}
}

func TestATokenQuotaWithHardEnforcementIsAccepted(t *testing.T) {
	for _, mode := range []string{"hard", "HARD", " hard "} {
		if got := quotaError(t, quotaConfig(100_000, 50_000, mode)); got != "" {
			t.Errorf("enforcement_mode %q refused a quota it does enforce: %s", mode, got)
		}
	}
}

// Every existing installation has no quota configured, and must not acquire a
// startup error by upgrading (product invariant 7).
func TestNoQuotaIsNotAnError(t *testing.T) {
	for _, mode := range []string{"", "soft", "off", "hard"} {
		if got := quotaError(t, quotaConfig(0, 0, mode)); got != "" {
			t.Errorf("mode %q complained about an unconfigured quota: %s", mode, got)
		}
	}
}
