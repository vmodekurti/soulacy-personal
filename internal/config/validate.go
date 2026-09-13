package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/publishedfiles"
	"github.com/soulacy/soulacy/internal/safeundo"
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
	dur("auth.jwt_access_ttl", c.Auth.JWTAccessTTL)
	dur("auth.jwt_refresh_ttl", c.Auth.JWTRefreshTTL)
	dur("queue.nats_ack_wait", c.Queue.NATSAckWait)
	dur("voice.timeout", c.Voice.Timeout)
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
	if err := safeundo.Validate(c.Server.SafeUndo); err != nil {
		errs = append(errs, fmt.Errorf("server.safe_undo: %w", err))
	}
	if len(c.Server.PublishedFiles) > 64 {
		errs = append(errs, fmt.Errorf("server.published_files: at most 64 dedicated folders may be shared"))
	}
	shares := make(map[string]bool)
	for _, share := range c.Server.PublishedFiles {
		if strings.TrimSpace(share.AgentID) == "" || shares[share.AgentID] || !publishedfiles.ValidRoot(share.Root) {
			errs = append(errs, fmt.Errorf("server.published_files: each entry needs a unique agent_id and an absolute, dedicated folder (not home or filesystem root)"))
		}
		shares[share.AgentID] = true
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port: %d is out of range (1–65535)", c.Server.Port))
	}
	if c.Server.Discovery.Enabled && strings.TrimSpace(c.Server.Discovery.Interface) == "" {
		errs = append(errs, fmt.Errorf("server.discovery.interface: required when LAN discovery is enabled"))
	}
	if c.Server.Discovery.Enabled && strings.TrimSpace(c.Server.Discovery.Hostname) == "" {
		errs = append(errs, fmt.Errorf("server.discovery.hostname: a stable, unique .local hostname is required when LAN discovery is enabled"))
	}
	switch strings.ToLower(strings.TrimSpace(c.Voice.Provider)) {
	case "", "openai", "sidecar":
	default:
		errs = append(errs, fmt.Errorf("voice.provider: unsupported value %q", c.Voice.Provider))
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
	case "", "process", "pool", "docker", "ssh":
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

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s",
			strings.Join(errStrings(errs), "\n  - "))
	}
	return nil
}

func errStrings(errs []error) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}
