package metrics

import "strings"

// toollabel.go — the `tool` metric label, bounded.
//
// tc.Name is whatever the model called, and only some of those names are
// compiled into the binary. The rest are authored by whoever set the
// deployment up or, in a multi-tenant one, by a tenant:
//
//	read_file, web_search, …        builtins; a fixed set in this binary
//	<name from SOUL.yaml>           a Python tool an agent author named
//	plugin__<id>__<tool>            a plugin an operator or tenant installed
//	mcp__<server>__<tool>           an MCP server; since MU-017 a workspace
//	                                may add its own, so the server id is a
//	                                tenant-chosen string
//	agent__<peer-id>                a peer agent, i.e. an agent ID
//
// The Prometheus exposition is one global document that cannot be scoped to
// the caller, so the last four categories put tenant-authored prose in front
// of every principal permitted to scrape — the same disclosure the `agent`
// label was, arriving through a label whose NAME looks innocuous. That is why
// the guard in labels_guard_test.go cannot be a list of forbidden names: this
// one is called `tool`.
//
// So the label carries a CLASS for everything the binary does not define, and
// the exact name only for builtins. An operator watching for "a tool is
// suddenly slow" still sees which class; the exact tool, per workspace, comes
// from the action log, which is workspace-scoped and authorized.
//
// The classes are prefix-derived rather than looked up, deliberately: a lookup
// that failed open would emit the raw name, and failing open is how the label
// ends up carrying exactly what it must not.
const (
	toolClassMCP    = "mcp"
	toolClassPlugin = "plugin"
	toolClassPeer   = "peer_agent"
	toolClassCustom = "custom"
)

// ToolLabel returns the metric label for a tool call.
//
// builtin says whether the name is one the binary defines. The caller answers
// it because the caller owns the registry; asking here would mean this package
// importing the runtime.
//
// UNKNOWN NAMES BECOME "custom", NOT THEMSELVES. A name that matches no prefix
// and is not a builtin is a Python tool somebody wrote — the most likely place
// for a customer's own vocabulary to appear.
func ToolLabel(name string, builtin bool) string {
	switch {
	case strings.HasPrefix(name, "mcp__"):
		return toolClassMCP
	case strings.HasPrefix(name, "plugin__"):
		return toolClassPlugin
	case strings.HasPrefix(name, "agent__"):
		return toolClassPeer
	case builtin && name != "":
		return name
	default:
		return toolClassCustom
	}
}
