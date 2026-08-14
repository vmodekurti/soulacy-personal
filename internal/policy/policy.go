// Package policy implements a small, deterministic gate for high-risk tool
// actions (shell, filesystem, and network). It answers a single question for
// each tool call: allow it outright, prompt the user for confirmation, or deny
// it. The logic is pure and side-effect free so it can be unit-tested in
// isolation and reused by both the runtime and any future policy preview UI.
//
// This closes the "policy prompts for shell/file/network actions" gap in the
// reliability/trust roadmap. It layers ON TOP of the existing per-path
// deterministic guardrail and the ConfirmTools gate: policy runs first and can
// deny a call before any handler is reached, regardless of tool category
// (built-in, system, MCP, or plugin).
package policy

import (
	"net/url"
	"path"
	"strings"
)

// Action is the decision the policy renders for a tool call.
type Action string

const (
	ActionAllow  Action = "allow"
	ActionPrompt Action = "prompt"
	ActionDeny   Action = "deny"
)

// Category buckets a tool by the kind of real-world risk it carries.
type Category string

const (
	CategoryShell   Category = "shell"
	CategoryFile    Category = "file"
	CategoryNetwork Category = "network"
	CategoryOther   Category = "other"
)

// Config is the per-agent policy. The zero value (Enabled=false) allows
// everything, so agents without a policy block are unaffected.
type Config struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Per-category base action: "allow", "prompt", or "deny". Empty falls back
	// to DefaultAction(category).
	Shell   string `yaml:"shell,omitempty" json:"shell,omitempty"`
	File    string `yaml:"file,omitempty" json:"file,omitempty"`
	Network string `yaml:"network,omitempty" json:"network,omitempty"`

	// AllowDomains, when non-empty, restricts network tool calls to hosts that
	// match one of these suffixes (e.g. "example.com" matches "api.example.com").
	// A network call to a host outside the list is denied.
	AllowDomains []string `yaml:"allow_domains,omitempty" json:"allow_domains,omitempty"`
	// DenyDomains always denies network calls to matching hosts, even if the
	// base action is allow.
	DenyDomains []string `yaml:"deny_domains,omitempty" json:"deny_domains,omitempty"`
	// DenyPaths always denies file tool calls whose path argument matches one of
	// these globs (matched against both the raw and cleaned path).
	DenyPaths []string `yaml:"deny_paths,omitempty" json:"deny_paths,omitempty"`
}

// toolCategories maps the built-in high-risk tools to their category. Tools not
// listed here are CategoryOther and are never gated by policy.
var toolCategories = map[string]Category{
	// shell / code execution
	"shell_exec":      CategoryShell,
	"run_script":      CategoryShell,
	"install_library": CategoryShell,
	"package_install": CategoryShell,
	"python_eval":     CategoryShell,
	// filesystem writes / reads
	"write_file":    CategoryFile,
	"download_file": CategoryFile,
	"read_file":     CategoryFile,
	"find_files":    CategoryFile,
	// network
	"http_request": CategoryNetwork,
	"fetch_url":    CategoryNetwork,
	"web_fetch":    CategoryNetwork,
}

// Classify returns the risk category for a tool name. MCP (mcp__…) and plugin
// (plugin__…) tools reach external systems, so they are treated as network.
func Classify(tool string) Category {
	if c, ok := toolCategories[tool]; ok {
		return c
	}
	if strings.HasPrefix(tool, "mcp__") || strings.HasPrefix(tool, "plugin__") {
		return CategoryNetwork
	}
	return CategoryOther
}

// DefaultAction is the action used for a category when the config leaves it
// unset. Shell and file default to prompting (surprising, high-blast-radius);
// network defaults to allow but is still subject to domain allow/deny lists.
func DefaultAction(cat Category) Action {
	switch cat {
	case CategoryShell, CategoryFile:
		return ActionPrompt
	case CategoryNetwork:
		return ActionAllow
	default:
		return ActionAllow
	}
}

func normalizeAction(s string) (Action, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return ActionAllow, true
	case "prompt", "confirm", "ask":
		return ActionPrompt, true
	case "deny", "block":
		return ActionDeny, true
	default:
		return "", false
	}
}

// Evaluate renders a decision for a tool call. It returns the action and a
// short human-readable reason suitable for a confirmation prompt or audit log.
func Evaluate(cfg Config, tool string, args map[string]any) (Action, string) {
	if !cfg.Enabled {
		return ActionAllow, ""
	}
	cat := Classify(tool)
	if cat == CategoryOther {
		return ActionAllow, ""
	}

	base := DefaultAction(cat)
	switch cat {
	case CategoryShell:
		if a, ok := normalizeAction(cfg.Shell); ok {
			base = a
		}
	case CategoryFile:
		if a, ok := normalizeAction(cfg.File); ok {
			base = a
		}
	case CategoryNetwork:
		if a, ok := normalizeAction(cfg.Network); ok {
			base = a
		}
	}

	switch cat {
	case CategoryNetwork:
		host := hostFromArgs(args)
		if host != "" {
			if matchDomain(host, cfg.DenyDomains) {
				return ActionDeny, "network host " + host + " is on the deny list"
			}
			if len(cfg.AllowDomains) > 0 && !matchDomain(host, cfg.AllowDomains) {
				return ActionDeny, "network host " + host + " is not on the allow list"
			}
		}
		return base, networkReason(base, host)
	case CategoryFile:
		if p := pathFromArgs(args); p != "" && matchGlobs(p, cfg.DenyPaths) {
			return ActionDeny, "file path " + p + " is on the deny list"
		}
		return base, categoryReason(base, cat)
	default: // shell
		return base, categoryReason(base, cat)
	}
}

func categoryReason(a Action, cat Category) string {
	if a == ActionAllow {
		return ""
	}
	verb := "requires confirmation"
	if a == ActionDeny {
		verb = "is denied by policy"
	}
	return string(cat) + " action " + verb
}

func networkReason(a Action, host string) string {
	if a == ActionAllow {
		return ""
	}
	suffix := ""
	if host != "" {
		suffix = " to " + host
	}
	if a == ActionDeny {
		return "network action" + suffix + " is denied by policy"
	}
	return "network action" + suffix + " requires confirmation"
}

// hostFromArgs extracts a hostname from common URL argument keys.
func hostFromArgs(args map[string]any) string {
	for _, key := range []string{"url", "endpoint", "uri", "address", "host"} {
		if v, ok := args[key].(string); ok {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if !strings.Contains(v, "://") {
				// bare host[:port]/path
				if i := strings.IndexAny(v, "/"); i >= 0 {
					v = v[:i]
				}
				if i := strings.Index(v, ":"); i >= 0 {
					v = v[:i]
				}
				return strings.ToLower(v)
			}
			if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
				return strings.ToLower(u.Hostname())
			}
		}
	}
	return ""
}

func pathFromArgs(args map[string]any) string {
	for _, key := range []string{"path", "file", "filename", "dest", "destination", "target"} {
		if v, ok := args[key].(string); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

// matchDomain reports whether host equals or is a subdomain of any entry.
func matchDomain(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "*.")))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// matchGlobs reports whether p matches any glob, testing both the raw and the
// path.Clean form so "./x" and "x" behave the same.
func matchGlobs(p string, globs []string) bool {
	cleaned := path.Clean(p)
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if ok, _ := path.Match(g, p); ok {
			return true
		}
		if ok, _ := path.Match(g, cleaned); ok {
			return true
		}
		// substring convenience: a bare directory name denies anything under it
		if strings.Contains(cleaned, strings.Trim(g, "*/")) && !strings.ContainsAny(g, "*?[") {
			return true
		}
	}
	return false
}
