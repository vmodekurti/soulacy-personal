package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Validate performs strict, fail-fast validation of a loaded Config (Story 5 /
// S8.1). Its whole purpose is to turn the framework's previous "silently fall
// back to a default" behaviour into a loud startup error, so that a typo like
// `tool_timeout: 120` (missing the `s`, which viper would happily keep as the
// string "120" and the runtime would then fail to parse and quietly default to
// 30s) is caught before the gateway serves a single request.
//
// It checks two classes of problem:
//
//  1. Duration strings that don't parse with time.ParseDuration.
//  2. Numeric values outside sane bounds (negative counts, ports out of range,
//     overlap >= chunk size, etc.).
//
// All problems are accumulated and returned together so the operator can fix
// them in one pass rather than one-error-per-restart.
func (c *Config) Validate() error {
	var errs []error

	// --- Deployment operating mode ---
	for _, issue := range c.DeploymentReadinessIssues() {
		errs = append(errs, fmt.Errorf("%s", issue))
	}

	// --- Durations: every string duration field must parse. ---
	dur := func(field, val string) {
		if strings.TrimSpace(val) == "" {
			return // empty means "use default" downstream; not an error here
		}
		if _, err := time.ParseDuration(val); err != nil {
			errs = append(errs, fmt.Errorf(
				"%s: %q is not a valid duration (did you forget the unit, e.g. %q?)",
				field, val, val+"s"))
		}
	}
	dur("runtime.tool_timeout", c.Runtime.ToolTimeout)
	dur("runtime.timeouts.tool", c.Runtime.Timeouts.Tool)
	dur("runtime.timeouts.llm", c.Runtime.Timeouts.LLM)
	dur("runtime.timeouts.step", c.Runtime.Timeouts.Step)
	dur("runtime.timeouts.run", c.Runtime.Timeouts.Run)
	dur("runtime.timeouts.http", c.Runtime.Timeouts.HTTP)
	dur("runtime.session_ttl", c.Runtime.SessionTTL)
	dur("runtime.retention.conversation_history", c.Runtime.Retention.ConversationHistory)
	dur("runtime.retention.action_events", c.Runtime.Retention.ActionEvents)
	dur("runtime.retention.audit_logs", c.Runtime.Retention.AuditLogs)
	// Retention is bounded, not merely parseable. Two failures were reachable
	// before: a NEGATIVE duration parses fine, and downstream `retention > 0`
	// guards then read it as "disabled" — so `-1h` silently turned pruning
	// off rather than erroring, and an operator reading the config saw a
	// retention policy that was not in force. And an absurdly short one (say
	// `1m`) is a way to make the audit trail unable to answer anything, set
	// through a route that is itself audited but whose effect nobody sees.
	retentionFloor := func(field, val string) {
		raw := strings.TrimSpace(val)
		if raw == "" || raw == "0" {
			// Empty is "use the default" and "0" is "keep forever". A floor
			// bounds how short retention may be; forever is not short.
			return
		}
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return // already reported by dur()
		}
		if parsed < 0 {
			errs = append(errs, fmt.Errorf(
				"%s: %s is negative, which silently disables pruning rather than shortening it", field, raw))
			return
		}
		if parsed < MinAuditRetention {
			errs = append(errs, fmt.Errorf(
				"%s: %s is below the %s platform minimum for records that answer \"who changed this\"",
				field, raw, MinAuditRetention))
		}
	}
	retentionFloor("runtime.retention.action_events", c.Runtime.Retention.ActionEvents)
	retentionFloor("runtime.retention.audit_logs", c.Runtime.Retention.AuditLogs)
	// Conversation history is user content rather than an audit record, so it
	// gets the negative check but no floor: a deployment that wants to keep
	// less of what people said is making a privacy choice, not evading one.
	if raw := strings.TrimSpace(c.Runtime.Retention.ConversationHistory); raw != "" && raw != "0" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed < 0 {
			errs = append(errs, fmt.Errorf(
				"runtime.retention.conversation_history: %s is negative, which silently disables pruning", raw))
		}
	}
	dur("auth.jwt_access_ttl", c.Auth.JWTAccessTTL)
	dur("auth.jwt_refresh_ttl", c.Auth.JWTRefreshTTL)
	if access, accessErr := time.ParseDuration(c.Auth.JWTAccessTTL); accessErr == nil && access > time.Hour {
		errs = append(errs, fmt.Errorf("auth.jwt_access_ttl: %s exceeds the 1h interactive-session maximum", access))
	}
	if access, accessErr := time.ParseDuration(c.Auth.JWTAccessTTL); accessErr == nil {
		if refresh, refreshErr := time.ParseDuration(c.Auth.JWTRefreshTTL); refreshErr == nil && refresh <= access {
			errs = append(errs, fmt.Errorf("auth.jwt_refresh_ttl must be greater than auth.jwt_access_ttl"))
		}
	}
	if strings.TrimSpace(c.Auth.OIDCIssuer) != "" {
		if c.Auth.Mode != "jwt" {
			errs = append(errs, fmt.Errorf("auth.oidc_issuer requires auth.mode=jwt"))
		}
		if strings.TrimSpace(c.Auth.OIDCClientID) == "" {
			errs = append(errs, fmt.Errorf("auth.oidc_client_id is required when auth.oidc_issuer is set"))
		}
		if strings.TrimSpace(c.Auth.OIDCRedirectURL) != "" {
			if parsed, err := url.Parse(c.Auth.OIDCRedirectURL); err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || (net.ParseIP(parsed.Hostname()) != nil && net.ParseIP(parsed.Hostname()).IsLoopback())))) {
				errs = append(errs, fmt.Errorf("auth.oidc_redirect_url must use HTTPS (HTTP is allowed only for loopback development)"))
			}
		}
	}
	dur("queue.nats_ack_wait", c.Queue.NATSAckWait)
	dur("voice.timeout", c.Voice.Timeout)
	dur("billing.webhook_tolerance", c.Billing.WebhookTolerance)
	if c.Runtime.ToolTimeout != "" && c.Runtime.Timeouts.Tool != "" && c.Runtime.ToolTimeout != c.Runtime.Timeouts.Tool {
		errs = append(errs, fmt.Errorf("runtime.tool_timeout (%s) must match runtime.timeouts.tool (%s); prefer runtime.timeouts.tool", c.Runtime.ToolTimeout, c.Runtime.Timeouts.Tool))
	}
	ordered := []struct{ name, raw string }{{"tool", c.Runtime.Timeouts.Tool}, {"llm", c.Runtime.Timeouts.LLM}, {"step", c.Runtime.Timeouts.Step}, {"run", c.Runtime.Timeouts.Run}, {"http", c.Runtime.Timeouts.HTTP}}
	for i := 1; i < len(ordered); i++ {
		prev, e1 := time.ParseDuration(ordered[i-1].raw)
		next, e2 := time.ParseDuration(ordered[i].raw)
		if e1 == nil && e2 == nil && prev >= next {
			errs = append(errs, fmt.Errorf("runtime timeout hierarchy requires %s < %s (got %s >= %s)", ordered[i-1].name, ordered[i].name, prev, next))
		}
	}

	// --- Server ---
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port: %d is out of range (1–65535)", c.Server.Port))
	}
	switch strings.ToLower(strings.TrimSpace(c.Voice.Provider)) {
	case "", "openai", "sidecar":
	default:
		errs = append(errs, fmt.Errorf("voice.provider: unsupported value %q", c.Voice.Provider))
	}
	switch strings.ToLower(strings.TrimSpace(c.Billing.Enforcement)) {
	case "", "migration", "strict":
	default:
		errs = append(errs, fmt.Errorf("billing.enforcement: unsupported value %q", c.Billing.Enforcement))
	}
	switch strings.ToLower(strings.TrimSpace(c.Billing.Provider)) {
	case "", "stripe":
	default:
		errs = append(errs, fmt.Errorf("billing.provider: unsupported value %q", c.Billing.Provider))
	}
	if strings.EqualFold(c.Billing.Provider, "stripe") {
		if strings.TrimSpace(c.Billing.StripeWebhookSecret) == "" {
			errs = append(errs, fmt.Errorf("billing.stripe_webhook_secret is required when billing.provider=stripe"))
		}
		if strings.EqualFold(c.Billing.Enforcement, "strict") {
			plan := strings.TrimSpace(c.Billing.DefaultPlan)
			if strings.TrimSpace(c.Billing.StripeSecretKey) == "" {
				errs = append(errs, fmt.Errorf("billing.stripe_secret_key is required for strict Stripe billing"))
			}
			if plan == "" || strings.TrimSpace(c.Billing.StripePrices[plan]) == "" {
				errs = append(errs, fmt.Errorf("billing.default_plan must name a configured billing.stripe_prices entry"))
			}
			for field, raw := range map[string]string{
				"billing.checkout_success_url": c.Billing.CheckoutSuccessURL,
				"billing.checkout_cancel_url":  c.Billing.CheckoutCancelURL,
				"billing.portal_return_url":    c.Billing.PortalReturnURL,
			} {
				parsed, err := url.Parse(strings.TrimSpace(raw))
				if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "https" {
					errs = append(errs, fmt.Errorf("%s must be an absolute HTTPS URL", field))
				}
			}
		}
	}

	// --- Runtime numeric bounds ---
	// max_concurrent_sessions sizes the worker pool; <=0 would mean "no workers"
	// and silently drop every channel message.
	if c.Runtime.MaxConcurrentSessions < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_concurrent_sessions: %d must not be negative", c.Runtime.MaxConcurrentSessions))
	}
	// default_max_turns bounds the agentic loop. Negative is nonsensical; an
	// absurdly high value is a cost/runaway foot-gun (see Story 1).
	if c.Runtime.DefaultMaxTurns < 0 {
		errs = append(errs, fmt.Errorf("runtime.default_max_turns: %d must not be negative", c.Runtime.DefaultMaxTurns))
	}
	if c.Runtime.MaxTurnsCeiling < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_turns_ceiling: %d must not be negative", c.Runtime.MaxTurnsCeiling))
	}
	if c.Runtime.MaxAgentCallDepth < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_agent_call_depth: %d must not be negative", c.Runtime.MaxAgentCallDepth))
	}
	// When both are set, the default must not exceed the hard ceiling.
	if c.Runtime.MaxTurnsCeiling > 0 && c.Runtime.DefaultMaxTurns > c.Runtime.MaxTurnsCeiling {
		errs = append(errs, fmt.Errorf(
			"runtime.default_max_turns (%d) exceeds runtime.max_turns_ceiling (%d)",
			c.Runtime.DefaultMaxTurns, c.Runtime.MaxTurnsCeiling))
	}
	if c.Runtime.MaxSessions < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_sessions: %d must not be negative", c.Runtime.MaxSessions))
	}
	if c.Runtime.MaxHistoryTurns < 0 {
		errs = append(errs, fmt.Errorf("runtime.max_history_turns: %d must not be negative", c.Runtime.MaxHistoryTurns))
	}
	for field, value := range map[string]int{
		"runtime.default_budget.max_tokens":    c.Runtime.DefaultBudget.MaxTokens,
		"runtime.default_budget.max_llm_calls": c.Runtime.DefaultBudget.MaxLLMCalls,
		"runtime.max_budget.max_tokens":        c.Runtime.MaxBudget.MaxTokens,
		"runtime.max_budget.max_llm_calls":     c.Runtime.MaxBudget.MaxLLMCalls,
	} {
		if value < 0 {
			errs = append(errs, fmt.Errorf("%s: %d must not be negative", field, value))
		}
	}

	// --- Sandbox: all rlimit knobs are "0 = unlimited", negatives are invalid. ---
	if c.Runtime.Sandbox.CPUSeconds < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.cpu_seconds: %d must not be negative", c.Runtime.Sandbox.CPUSeconds))
	}
	if c.Runtime.Sandbox.MemoryMB < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.memory_mb: %d must not be negative", c.Runtime.Sandbox.MemoryMB))
	}
	if c.Runtime.Sandbox.OpenFiles < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.open_files: %d must not be negative", c.Runtime.Sandbox.OpenFiles))
	}
	if c.Runtime.Sandbox.FileSizeMB < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.file_size_mb: %d must not be negative", c.Runtime.Sandbox.FileSizeMB))
	}
	if c.Runtime.Sandbox.PIDs < 0 {
		errs = append(errs, fmt.Errorf("runtime.sandbox.pids: %d must not be negative", c.Runtime.Sandbox.PIDs))
	}
	switch strings.ToLower(strings.TrimSpace(c.Runtime.Sandbox.Mode)) {
	case "", "docker", "unsandboxed":
	default:
		errs = append(errs, fmt.Errorf("runtime.sandbox.mode: unsupported value %q", c.Runtime.Sandbox.Mode))
	}

	// --- Executor ---
	switch c.Executor.Backend {
	case "", "process", "pool", "docker", "ssh", "worker":
	default:
		errs = append(errs, fmt.Errorf("executor.backend: unsupported value %q", c.Executor.Backend))
	}
	if c.Executor.Backend == "pool" && c.Executor.Workers < 1 {
		errs = append(errs, fmt.Errorf("executor.workers: %d must be >= 1 when executor.backend is \"pool\"", c.Executor.Workers))
	}
	if c.Executor.Backend == "ssh" && strings.TrimSpace(c.Executor.SSHHost) == "" {
		errs = append(errs, fmt.Errorf("executor.ssh_host: required when executor.backend is \"ssh\""))
	}

	// --- Knowledge / RAG chunking ---
	if c.Knowledge.ChunkSize < 0 {
		errs = append(errs, fmt.Errorf("knowledge.chunk_size: %d must not be negative", c.Knowledge.ChunkSize))
	}
	if c.Knowledge.ChunkOverlap < 0 {
		errs = append(errs, fmt.Errorf("knowledge.chunk_overlap: %d must not be negative", c.Knowledge.ChunkOverlap))
	}
	if c.Knowledge.ChunkSize > 0 && c.Knowledge.ChunkOverlap >= c.Knowledge.ChunkSize {
		errs = append(errs, fmt.Errorf(
			"knowledge.chunk_overlap (%d) must be smaller than knowledge.chunk_size (%d)",
			c.Knowledge.ChunkOverlap, c.Knowledge.ChunkSize))
	}
	if c.Knowledge.MaxDocumentBytes < 0 {
		errs = append(errs, fmt.Errorf("knowledge.max_document_bytes: %d must not be negative", c.Knowledge.MaxDocumentBytes))
	}

	// --- Daily token quotas are enforced by the cost governor, in hard mode ---
	//
	// `ratelimit.per_user_tokens_day` and `per_agent_tokens_day` live in the
	// ratelimit section and are read by internal/costs, which is the only
	// thing that enforces them — and only when costs.enforcement_mode is
	// "hard". In soft or off mode the limit is carried all the way into a
	// ReservationPolicy and then not applied.
	//
	// This is an ERROR rather than a warning because of what the previous
	// behaviour was. internal/ratelimit used to carry a second implementation:
	// in-memory buckets that nothing ever filled, mounted on every chat route,
	// comparing zero against the limit and allowing every request. An operator
	// setting a daily token quota got a config line, a middleware visible in
	// the route table, a status endpoint reporting usage, and no quota. That
	// mechanism is gone; refusing to start is what makes sure its replacement
	// cannot fail the same silent way.
	//
	// The remedy is one line either way — set enforcement_mode, or remove the
	// quota — and both are better than believing in a limit that does not
	// exist.
	if quota := maxInt(c.RateLimit.PerUserTokensDay, c.RateLimit.PerAgentTokensDay); quota > 0 {
		mode := strings.ToLower(strings.TrimSpace(c.Costs.EnforcementMode))
		if mode != "hard" {
			named := "ratelimit.per_user_tokens_day"
			if c.RateLimit.PerUserTokensDay <= 0 {
				named = "ratelimit.per_agent_tokens_day"
			}
			errs = append(errs, fmt.Errorf(
				"%s is set to %d but costs.enforcement_mode is %q — daily token quotas are "+
					"enforced by the cost governor, which only rejects in \"hard\" mode, so this "+
					"limit would never fire. Set costs.enforcement_mode: hard, or remove the quota",
				named, quota, c.Costs.EnforcementMode))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s",
			strings.Join(errStrings(errs), "\n  - "))
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func errStrings(errs []error) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}
