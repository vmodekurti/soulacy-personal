// Package ratelimit implements per-user and per-agent rate limiting for the
// Soulacy gateway (Task #33).
//
// Two enforcement axes:
//
//  1. Requests per minute (RPM) — sliding fixed-window counter, checked on
//     every API request. Keyed by JWT subject (per-user) and/or agent ID
//     (per-agent). In-memory by default; Redis for multi-instance deployments.
//
//  2. Tokens per day — in-memory 24h sliding counter, incremented after each
//     LLM call by the engine. Checked on /chat and /chat/stream before
//     dispatching. Resets automatically as the window slides.
//
// Both axes are independently optional (0 = disabled). When both a per-user
// and a per-agent limit apply, both must pass.
//
// Config (config.yaml):
//
//	rate_limit:
//	  enabled: true
//	  per_user_rpm: 60          # 0 = disabled
//	  per_agent_rpm: 120        # 0 = disabled (all users to one agent combined)
//	  per_user_tokens_day: 0    # 0 = disabled
//	  backend: memory           # or "redis"
//	  redis_url: redis://localhost:6379
//
// Env vars: SOULACY_RATE_LIMIT_ENABLED, SOULACY_RATE_LIMIT_PER_USER_RPM, etc.
package ratelimit

// Config holds rate-limit parameters parsed from config.yaml.
type Config struct {
	// Enabled is a master switch. When false, all middleware is a no-op.
	Enabled bool `mapstructure:"enabled"`

	// PerUserRPM is the maximum number of API requests per minute per
	// authenticated user (JWT subject). 0 disables per-user RPM limiting.
	PerUserRPM int `mapstructure:"per_user_rpm"`

	// PerAgentRPM is the maximum number of requests per minute directed
	// at a single agent, summed across all users. 0 disables per-agent
	// RPM limiting.
	PerAgentRPM int `mapstructure:"per_agent_rpm"`

	// PerUserTokensDay is the maximum number of LLM tokens a single user
	// may consume within a 24-hour sliding window. 0 disables token quotas.
	PerUserTokensDay int `mapstructure:"per_user_tokens_day"`

	// PerAgentTokensDay is the maximum LLM tokens a single agent may consume
	// within a 24-hour sliding window across all users. 0 disables per-agent
	// token quotas.
	PerAgentTokensDay int `mapstructure:"per_agent_tokens_day"`

	// Backend selects the RPM counter backend: "memory" (default) or "redis".
	// "redis" requires RedisURL to be set; falls back to memory on connection
	// failure so the gateway can still start.
	Backend string `mapstructure:"backend"`

	// RedisURL is the Redis connection string used when Backend == "redis".
	// Format: "redis://[:password@]host:port[/db]"
	// Example: "redis://localhost:6379"
	RedisURL string `mapstructure:"redis_url"`
}

// DefaultConfig returns sensible defaults (no token quota, 60 user-RPM, memory backend).
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		PerUserRPM:       60,
		PerAgentRPM:      0,
		PerUserTokensDay: 0,
		Backend:          "memory",
	}
}
