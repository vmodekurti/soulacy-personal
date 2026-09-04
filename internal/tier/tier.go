// Package tier classifies an agent's effective capability surface.
//
// Designed for the channel-binding policy gate documented in
// docs/CHANNEL_DESIGN.md (Q1): exposing an agent to a non-web channel
// like Telegram needs to be a per-binding decision informed by what the
// agent can actually DO, not just by an ID-based hard-block.
//
// Tier rules:
//
//   - Privileged — the agent (directly OR transitively through any peer)
//     can spawn processes, write/create files, install software, or has
//     SystemTools=true, OR has wildcard ('*'/'all') over builtins, peer
//     agents, mcp_servers, or mcp_tools (because the wildcard sets
//     include the privileged tools above, plus arbitrary MCP servers
//     whose tool surface we can't enumerate at config time).
//
//   - Active — has at least one read-class builtin (web_search,
//     kb_search, read_file, list_dir, semantic_memory_search, etc.) OR
//     declares any specific mcp_servers / mcp_tools (we don't know what
//     each MCP server can do; treat conservatively as active). Memory
//     write access alone counts as active because it can leak data
//     through replies.
//
//   - ReadOnly — no builtins, no MCP, no privileged peers, no
//     system_tools. Pure prompt+LLM with at most read-only memory and
//     non-privileged peer calls.
//
// The classifier walks the peer graph with cycle detection so a router
// that fans out to a worker with shell_exec inherits Privileged. The
// walk bounds at the engine's existing maxAgentCallDepth implicitly
// (cycle detection makes depth unbounded but safe).
package tier

import (
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// Tier names the capability tier of an agent. The zero value (Unknown)
// means classification hasn't been run or the agent definition was nil;
// callers must treat Unknown as "do not bind" — failing closed.
type Tier int

const (
	Unknown Tier = iota
	ReadOnly
	Active
	Privileged
)

// String renders the tier as a lowercase token suitable for log fields,
// metric labels, and YAML status reports.
func (t Tier) String() string {
	switch t {
	case ReadOnly:
		return "read_only"
	case Active:
		return "active"
	case Privileged:
		return "privileged"
	default:
		return "unknown"
	}
}

// Lookup resolves an agent ID to its Definition. Implemented by the
// runtime.Loader in production; tests stub with a map-backed fake.
type Lookup func(id string) *agent.Definition

// privilegedBuiltins are the built-in tool names that imply Privileged
// tier whenever they appear in an agent's allowlist. Adding a new
// builtin that can spawn processes / write to disk / install packages
// belongs here.
var privilegedBuiltins = map[string]bool{
	"shell_exec":      true,
	"run_script":      true,
	"install_library": true,
	"package_install": true,
	"write_file":      true,
}

// activeBuiltins are read/inspect-class builtins that escalate ReadOnly
// → Active but don't reach Privileged on their own. Listed for
// documentation; the classifier also defaults any non-privileged builtin
// to Active.
//
//nolint:unused // referenced via fallthrough in Compute; kept for clarity
var activeBuiltins = map[string]bool{
	"web_search":             true,
	"kb_search":              true,
	"read_file":              true,
	"list_dir":               true,
	"semantic_memory_search": true,
	"channel.send":           true,
	"queue_create":           true,
	"queue_names":            true,
	"queue_put":              true,
	"queue_take":             true,
	"queue_list":             true,
	"queue_clear":            true,
}

// Compute returns the capability tier of `def`, walking peer agents
// through `lookup`. A nil lookup is safe but means transitive peer
// detection is disabled — callers without a loader (tests, validation
// passes) get a tier based on the agent itself only, which under-
// classifies orchestrators. Production callers must pass a real lookup.
//
// Definition is treated read-only; cycle detection keeps the walk
// terminating even with self-references or A↔B loops.
func Compute(def *agent.Definition, lookup Lookup) Tier {
	if def == nil {
		return Unknown
	}
	seen := map[string]bool{}
	return compute(def, lookup, seen)
}

// Explanation pairs a Tier with the concrete reasons that produced it.
// Used by the "why is this agent privileged?" debugging surfaces (the
// `sy agent tier` CLI and the GET /api/v1/agents/:id/tier endpoint).
//
// Reasons are short human-readable strings, ordered to match the
// classifier's evaluation path (own-agent reasons first, then peers).
// An empty Reasons slice with Tier == ReadOnly means "nothing
// noteworthy" — a pure prompt+LLM agent.
type Explanation struct {
	Tier    Tier     `json:"tier"`
	Reasons []string `json:"reasons"`
}

// Explain returns the tier of `def` plus the reasons that justified it.
// Same walk as Compute, just with reason collection. Safe for nil def
// (returns Unknown + an empty reason set).
func Explain(def *agent.Definition, lookup Lookup) Explanation {
	if def == nil {
		return Explanation{Tier: Unknown}
	}
	seen := map[string]bool{}
	var reasons []string
	t := explain(def, lookup, seen, &reasons)
	return Explanation{Tier: t, Reasons: reasons}
}

// explain is compute's reason-collecting twin. Kept parallel rather
// than fused to avoid muddying the hot path (Compute is called per
// channel binding at boot AND potentially per request in the future);
// Explain is rarer (debugging/UI) and the duplicated walk is cheap.
func explain(def *agent.Definition, lookup Lookup, seen map[string]bool, out *[]string) Tier {
	if def == nil {
		return Unknown
	}
	if seen[def.ID] {
		return ReadOnly
	}
	seen[def.ID] = true

	if def.HasCapability("system") {
		*out = append(*out, "capabilities: [system] (OS-level shell access)")
		return Privileged
	}
	if hasWildcard(def.Builtins) {
		*out = append(*out, "wildcard builtins ('*' or 'all') — includes shell_exec, write_file, etc.")
		return Privileged
	}
	if hasWildcardStrPtr(def.MCPServers) {
		*out = append(*out, "wildcard mcp_servers — arbitrary MCP server capabilities can't be enumerated")
		return Privileged
	}
	if hasWildcardStrPtr(def.MCPTools) {
		*out = append(*out, "wildcard mcp_tools — arbitrary MCP tool capabilities can't be enumerated")
		return Privileged
	}
	if def.Builtins != nil {
		for _, name := range *def.Builtins {
			if privilegedBuiltins[name] {
				*out = append(*out, "privileged builtin: "+name)
				return Privileged
			}
		}
	}

	maxFromPeers := ReadOnly
	if hasWildcardSlice(def.Agents) {
		*out = append(*out, "wildcard peer agents ('*' or 'all') — could dispatch to any loaded agent")
		return Privileged
	}
	for _, peerID := range def.Agents {
		if peerID == "" || lookup == nil {
			continue
		}
		peerDef := lookup(peerID)
		if peerDef == nil {
			continue
		}
		// Use a per-branch `seen` clone so reason collection isn't
		// silenced by an earlier branch having visited the same peer.
		// (For tier value the global seen map is fine; for reasons we
		// want each privileged peer surfaced exactly once across the
		// whole walk, which the parent seen map already enforces.)
		var peerReasons []string
		peerTier := explain(peerDef, lookup, seen, &peerReasons)
		if peerTier > maxFromPeers {
			maxFromPeers = peerTier
		}
		if peerTier == Privileged {
			*out = append(*out, "transitive: peer '"+peerID+"' is privileged ("+strings.Join(peerReasons, "; ")+")")
			return Privileged
		}
		if peerTier == Active && len(peerReasons) > 0 {
			*out = append(*out, "transitive: peer '"+peerID+"' is active ("+strings.Join(peerReasons, "; ")+")")
		}
	}

	own := ReadOnly
	if hasNonEmptyStrPtr(def.MCPServers) {
		*out = append(*out, "specific mcp_servers declared — capabilities depend on server")
		own = Active
	}
	if hasNonEmptyStrPtr(def.MCPTools) {
		*out = append(*out, "specific mcp_tools declared")
		if own < Active {
			own = Active
		}
	}
	if def.Builtins != nil && len(*def.Builtins) > 0 {
		names := strings.Join(*def.Builtins, ", ")
		*out = append(*out, "non-privileged builtins: "+names)
		if own < Active {
			own = Active
		}
	}

	if maxFromPeers > own {
		return maxFromPeers
	}
	return own
}

func compute(def *agent.Definition, lookup Lookup, seen map[string]bool) Tier {
	if def == nil {
		return Unknown
	}
	if seen[def.ID] {
		// Already counted this agent on a prior path through the graph.
		// Returning ReadOnly is safe: the original visit contributed its
		// real tier; we just avoid infinite recursion.
		return ReadOnly
	}
	seen[def.ID] = true

	// 1. The "system" capability (or legacy system_tools) is an unambiguous escalation.
	if def.HasCapability("system") {
		return Privileged
	}

	// 2. Wildcard builtins — '*' or 'all' includes shell_exec/write_file.
	if hasWildcard(def.Builtins) {
		return Privileged
	}

	// 3. Wildcard MCP — we can't enumerate what each MCP server exposes,
	//    and 'filesystem' MCP servers in particular have write surface
	//    equivalent to write_file. Conservative: '*' = privileged.
	if hasWildcardStrPtr(def.MCPServers) || hasWildcardStrPtr(def.MCPTools) {
		return Privileged
	}

	// 4. Specific privileged builtin in the agent's allowlist.
	if def.Builtins != nil {
		for _, name := range *def.Builtins {
			if privilegedBuiltins[name] {
				return Privileged
			}
		}
	}

	// 5. Walk peers. If any peer is Privileged, this agent inherits it.
	//    Wildcard agents — '*' or 'all' — means the agent can dispatch
	//    to ANY loaded agent, so we must conservatively assume one of
	//    them is privileged unless the lookup proves otherwise.
	maxFromPeers := ReadOnly
	if hasWildcardSlice(def.Agents) {
		// Without a lookup, we can't scan all agents; default to
		// Privileged because the agent COULD call anything.
		if lookup == nil {
			return Privileged
		}
		// We'd need a "list all" hook to scan exhaustively. Lookup is
		// just a Get(id) function; we can't enumerate. Treat wildcard
		// as Privileged here too — operators wanting tighter
		// classification should name their peers explicitly.
		return Privileged
	}
	for _, peerID := range def.Agents {
		if peerID == "" {
			continue
		}
		if lookup == nil {
			// No way to verify the peer; under-classify rather than
			// fabricate a tier. The caller knows lookup was nil.
			continue
		}
		peerDef := lookup(peerID)
		if peerDef == nil {
			// Unresolvable peer — treat as ReadOnly contribution rather
			// than crashing the classification. The runtime peer-call
			// guard will refuse the call at execution time.
			continue
		}
		peerTier := compute(peerDef, lookup, seen)
		if peerTier > maxFromPeers {
			maxFromPeers = peerTier
		}
		if maxFromPeers == Privileged {
			return Privileged
		}
	}

	// 6. Determine this agent's own non-peer-derived tier.
	own := ReadOnly
	if hasNonEmptyStrPtr(def.MCPServers) || hasNonEmptyStrPtr(def.MCPTools) {
		// Specific MCP servers/tools — bump to Active. Without per-
		// server capability metadata we can't tell which servers are
		// safe; Active is the right default.
		if own < Active {
			own = Active
		}
	}
	if def.Builtins != nil && len(*def.Builtins) > 0 {
		// Any specific builtin that isn't on the privileged list lands
		// in Active. Privileged was already checked at step 4.
		if own < Active {
			own = Active
		}
	}

	// Combine own tier with the max from peers — promote, never demote.
	if maxFromPeers > own {
		return maxFromPeers
	}
	return own
}

// --- helpers ------------------------------------------------------------

func hasWildcard(p *[]string) bool {
	if p == nil {
		return false
	}
	for _, v := range *p {
		if v == "*" || strings.EqualFold(v, "all") {
			return true
		}
	}
	return false
}

func hasWildcardSlice(s []string) bool {
	for _, v := range s {
		if v == "*" || strings.EqualFold(v, "all") {
			return true
		}
	}
	return false
}

func hasWildcardStrPtr(p *[]string) bool {
	if p == nil {
		return false
	}
	for _, v := range *p {
		if v == "*" || strings.EqualFold(v, "all") {
			return true
		}
	}
	return false
}

func hasNonEmptyStrPtr(p *[]string) bool {
	return p != nil && len(*p) > 0
}
