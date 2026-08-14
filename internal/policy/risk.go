// risk.go — a 5-tier risk taxonomy for tools (Epic 11, Policy & Safety UX).
//
// Where Category (policy.go) buckets tools for the deterministic allow/prompt/deny
// gate, RiskTier answers a finer, user-facing question: "how dangerous is this
// tool?" on a fixed 5-point scale the UI can show before an agent is bound to a
// channel, and that the runtime can use to require confirmation for the top
// tiers unless explicitly allowed.
//
// Pure and side-effect free so it is unit-tested in isolation and reused by the
// tool catalog, the agent-tier command, and the runtime confirmation gate.
package policy

import "strings"

// RiskTier ranks a tool by real-world blast radius, low to high.
type RiskTier int

const (
	RiskSafe        RiskTier = iota // read-only, no external side effects
	RiskWrite                       // local writes / state changes
	RiskNetwork                     // reaches external networks or services
	RiskPrivileged                  // installs software or changes broad system config
	RiskShellSystem                 // arbitrary shell / code execution
)

// String is the stable machine identifier for a tier.
func (t RiskTier) String() string {
	switch t {
	case RiskSafe:
		return "safe"
	case RiskWrite:
		return "write"
	case RiskNetwork:
		return "network"
	case RiskPrivileged:
		return "privileged"
	case RiskShellSystem:
		return "shell_system"
	default:
		return "safe"
	}
}

// Label is a short human-readable description of the tier.
func (t RiskTier) Label() string {
	switch t {
	case RiskSafe:
		return "Safe — reads only, no side effects"
	case RiskWrite:
		return "Write — changes local files or state"
	case RiskNetwork:
		return "Network — reaches external services"
	case RiskPrivileged:
		return "Privileged — installs software or changes system config"
	case RiskShellSystem:
		return "Shell/System — runs arbitrary commands or code"
	default:
		return "Safe"
	}
}

// HighRisk reports whether a tier should require confirmation by default
// (the top two tiers) unless the agent explicitly allows it.
func (t RiskTier) HighRisk() bool {
	return t == RiskPrivileged || t == RiskShellSystem
}

// riskTiers maps known builtin tools to their tier. Anything not listed is
// classified heuristically by RiskTierOf.
var riskTiers = map[string]RiskTier{
	// safe — read-only / inert
	"read_file":              RiskSafe,
	"list_dir":               RiskSafe,
	"find_files":             RiskSafe,
	"env_get":                RiskSafe,
	"sys_info":               RiskSafe,
	"generate_chart":         RiskSafe,
	"session_search":         RiskSafe,
	"read_skill":             RiskSafe,
	"read_skill_file":        RiskSafe,
	"kb_search":              RiskSafe,
	"semantic_memory_search": RiskSafe,
	"queue_names":            RiskSafe,
	"queue_list":             RiskSafe,

	// write — local state changes
	"write_file":   RiskWrite,
	"kb_write":     RiskWrite,
	"queue_create": RiskWrite,
	"queue_put":    RiskWrite,
	"queue_take":   RiskWrite,
	"queue_clear":  RiskWrite,

	// network — reaches out
	"fetch_url":     RiskNetwork,
	"http_request":  RiskNetwork,
	"web_fetch":     RiskNetwork,
	"web_search":    RiskNetwork,
	"channel.send":  RiskNetwork,
	"download_file": RiskNetwork, // also writes a file, but reaching out is the higher risk

	// privileged — installs software / broad config
	"install_library": RiskPrivileged,
	"package_install": RiskPrivileged,

	// shell/system — arbitrary execution
	"shell_exec":  RiskShellSystem,
	"run_script":  RiskShellSystem,
	"python_eval": RiskShellSystem,
}

// RiskTierOf returns the risk tier for a tool name. Known builtins use the
// table; MCP/plugin tools reach external systems (network); otherwise it falls
// back to keyword heuristics, defaulting unknown tools to Write (they plausibly
// change something, but aren't assumed to run shells).
func RiskTierOf(tool string) RiskTier {
	if t, ok := riskTiers[tool]; ok {
		return t
	}
	if strings.HasPrefix(tool, "mcp__") || strings.HasPrefix(tool, "plugin__") {
		return RiskNetwork
	}
	name := strings.ToLower(tool)
	switch {
	case containsAny(name, "shell", "exec", "bash", "command", "eval", "spawn", "subprocess"):
		return RiskShellSystem
	case containsAny(name, "install", "sudo", "chmod", "chown", "privileg"):
		return RiskPrivileged
	case containsAny(name, "http", "fetch", "url", "web", "request", "api", "send", "post", "email", "webhook", "browse", "search", "download", "upload"):
		return RiskNetwork
	case containsAny(name, "write", "create", "update", "delete", "put", "save", "edit", "remove", "mkdir"):
		return RiskWrite
	case containsAny(name, "read", "list", "get", "find", "search", "info", "status", "view"):
		return RiskSafe
	default:
		return RiskWrite
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// MaxRiskTier returns the highest risk tier among a set of tool names — the
// capability tier of an agent, computed from the tools it can use.
func MaxRiskTier(tools []string) RiskTier {
	max := RiskSafe
	for _, t := range tools {
		if r := RiskTierOf(t); r > max {
			max = r
		}
	}
	return max
}
