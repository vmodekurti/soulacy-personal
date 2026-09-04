package tier

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// helper: build a Definition with the fields the classifier cares about
// without dragging in every required field on the struct.
func def(id string, opts ...func(*agent.Definition)) *agent.Definition {
	d := &agent.Definition{ID: id}
	for _, o := range opts {
		o(d)
	}
	return d
}

func builtins(names ...string) func(*agent.Definition) {
	return func(d *agent.Definition) {
		cp := append([]string(nil), names...)
		d.Builtins = &cp
	}
}
func peers(ids ...string) func(*agent.Definition) {
	return func(d *agent.Definition) { d.Agents = ids }
}
func systemTools() func(*agent.Definition) {
	return func(d *agent.Definition) { d.SystemTools = true }
}
func mcpServers(ids ...string) func(*agent.Definition) {
	return func(d *agent.Definition) {
		cp := append([]string(nil), ids...)
		d.MCPServers = &cp
	}
}
func mcpTools(names ...string) func(*agent.Definition) {
	return func(d *agent.Definition) {
		cp := append([]string(nil), names...)
		d.MCPTools = &cp
	}
}

// mapLookup is a Lookup that resolves IDs from a fixture map. Test-only.
func mapLookup(m map[string]*agent.Definition) Lookup {
	return func(id string) *agent.Definition { return m[id] }
}

// TestCompute_DirectTiers covers the no-peer base cases: classification
// is determined entirely by the agent's own declared capabilities.
func TestCompute_DirectTiers(t *testing.T) {
	cases := []struct {
		name string
		def  *agent.Definition
		want Tier
	}{
		{"nil def → Unknown", nil, Unknown},
		{"bare (no builtins, no peers, no mcp) → ReadOnly", def("a"), ReadOnly},
		{"web_search only → Active", def("a", builtins("web_search")), Active},
		{"read_file only → Active", def("a", builtins("read_file")), Active},
		{"kb_search only → Active", def("a", builtins("kb_search")), Active},
		{"channel.send only → Active", def("a", builtins("channel.send")), Active},
		{"shell_exec → Privileged", def("a", builtins("shell_exec")), Privileged},
		{"write_file → Privileged", def("a", builtins("write_file")), Privileged},
		{"run_script → Privileged", def("a", builtins("run_script")), Privileged},
		{"install_library → Privileged", def("a", builtins("install_library")), Privileged},
		{"mixed with one privileged → Privileged", def("a", builtins("web_search", "shell_exec")), Privileged},
		{"system_tools=true → Privileged", def("a", systemTools()), Privileged},
		{"wildcard builtins '*' → Privileged", def("a", builtins("*")), Privileged},
		{"wildcard builtins 'all' (case-insensitive) → Privileged", def("a", builtins("ALL")), Privileged},
		{"specific MCP server → Active", def("a", mcpServers("filesystem")), Active},
		{"wildcard MCP servers → Privileged", def("a", mcpServers("*")), Privileged},
		{"wildcard MCP tools → Privileged", def("a", mcpTools("*")), Privileged},
		{"specific MCP tools → Active", def("a", mcpTools("filesystem.read")), Active},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.def, nil)
			if got != tc.want {
				t.Errorf("Compute(%q) = %s; want %s", tc.name, got, tc.want)
			}
		})
	}
}

// TestCompute_TransitivePeers covers escalation through the peer graph.
// A router that fans out to a shell-having worker is Privileged even if
// the router itself declares no builtins.
func TestCompute_TransitivePeers(t *testing.T) {
	worker := def("worker", builtins("shell_exec"))
	router := def("router", peers("worker"))
	innocent := def("innocent", builtins("web_search"))

	lookup := mapLookup(map[string]*agent.Definition{
		"worker":   worker,
		"router":   router,
		"innocent": innocent,
	})

	if got := Compute(router, lookup); got != Privileged {
		t.Errorf("router → worker(shell_exec) should be Privileged; got %s", got)
	}

	// Same router WITHOUT lookup: can't see peers, so falls back to
	// own-tier ReadOnly. This is the documented under-classification
	// behavior — callers without a loader get a lower bound, not truth.
	if got := Compute(router, nil); got != ReadOnly {
		t.Errorf("router (no lookup) should under-classify to ReadOnly; got %s", got)
	}

	// Router fanning out to an Active worker only is itself Active.
	r2 := def("r2", peers("innocent"))
	lookup2 := mapLookup(map[string]*agent.Definition{
		"r2":       r2,
		"innocent": innocent,
	})
	if got := Compute(r2, lookup2); got != Active {
		t.Errorf("r2 → innocent(web_search) should be Active; got %s", got)
	}
}

// TestCompute_PeerCycle confirms the walk terminates even when agents
// reference each other. Without cycle detection this would stack-
// overflow; with it the second visit returns ReadOnly and the original
// tier wins.
func TestCompute_PeerCycle(t *testing.T) {
	a := def("a", peers("b"))
	b := def("b", peers("a"))
	lookup := mapLookup(map[string]*agent.Definition{"a": a, "b": b})

	if got := Compute(a, lookup); got != ReadOnly {
		t.Errorf("A↔B cycle, both bare → ReadOnly; got %s", got)
	}

	// Now make one of them Privileged and confirm the cycle doesn't
	// hide the escalation from the other.
	priv := def("priv", builtins("shell_exec"), peers("safe"))
	safe := def("safe", peers("priv"))
	lookup2 := mapLookup(map[string]*agent.Definition{"priv": priv, "safe": safe})
	if got := Compute(safe, lookup2); got != Privileged {
		t.Errorf("safe → priv(shell_exec) → safe (cycle) should still classify Privileged; got %s", got)
	}
}

// TestCompute_SelfReference — agent listing itself as a peer (common in
// the wildcard pattern where the engine filters self-refs at runtime
// but the YAML still has the ID). Should not loop or escalate.
func TestCompute_SelfReference(t *testing.T) {
	a := def("a", peers("a"))
	lookup := mapLookup(map[string]*agent.Definition{"a": a})
	if got := Compute(a, lookup); got != ReadOnly {
		t.Errorf("self-reference, bare → ReadOnly; got %s", got)
	}
}

// TestCompute_WildcardPeers — agents: ['*'] is privileged because it
// can dispatch to any loaded agent, including future privileged ones.
// We can't enumerate the full set through a Get-only Lookup, so the
// classifier fails closed.
func TestCompute_WildcardPeers(t *testing.T) {
	a := def("a", peers("*"))
	if got := Compute(a, nil); got != Privileged {
		t.Errorf("wildcard peers (no lookup) → Privileged; got %s", got)
	}
	// Even with a lookup, the wildcard is treated as Privileged: we
	// can't enumerate all loaded agents through Get(id), so we fail
	// closed. Document this behavior.
	lookup := mapLookup(map[string]*agent.Definition{"a": a})
	if got := Compute(a, lookup); got != Privileged {
		t.Errorf("wildcard peers (with lookup) → Privileged; got %s", got)
	}
}

// TestCompute_UnresolvedPeer — peer listed but not loaded yet. Don't
// crash, don't escalate, don't pretend it's ReadOnly. Today: peer is
// silently skipped from the walk (treated as ReadOnly contribution) and
// the runtime peer-call guard will refuse the call at execution.
func TestCompute_UnresolvedPeer(t *testing.T) {
	a := def("a", peers("ghost"))
	lookup := mapLookup(map[string]*agent.Definition{"a": a})
	if got := Compute(a, lookup); got != ReadOnly {
		t.Errorf("unresolved peer should not escalate; got %s", got)
	}
}

// TestTierString — log/metric labels must remain stable across versions.
// A change here is a breaking change for anyone querying Prometheus or
// grepping logs by tier.
func TestTierString(t *testing.T) {
	cases := map[Tier]string{
		Unknown:    "unknown",
		ReadOnly:   "read_only",
		Active:     "active",
		Privileged: "privileged",
	}
	for tier, want := range cases {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q; want %q", tier, got, want)
		}
	}
}

// TestExplain_ReasonsMatchTier validates that the Explain accessor's
// reason set always includes the trigger that produced the tier. Without
// this, the GUI badge / sy CLI / API endpoint could happily say
// "privileged" with no reason text, which defeats the point of the
// "why is this agent privileged?" debugging surface.
func TestExplain_ReasonsMatchTier(t *testing.T) {
	cases := []struct {
		name      string
		def       *agent.Definition
		wantTier  Tier
		reasonHas string // substring that must appear in at least one reason
	}{
		{"system_tools", def("a", systemTools()), Privileged, "capabilities: [system]"},
		{"shell_exec", def("a", builtins("shell_exec")), Privileged, "shell_exec"},
		{"wildcard builtins", def("a", builtins("*")), Privileged, "wildcard builtins"},
		{"wildcard mcp", def("a", mcpServers("*")), Privileged, "wildcard mcp_servers"},
		{"active builtin", def("a", builtins("web_search")), Active, "web_search"},
		{"active mcp", def("a", mcpServers("filesystem")), Active, "mcp_servers"},
		{"bare → readonly with no reasons", def("a"), ReadOnly, ""}, // expect empty reasons
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := Explain(tc.def, nil)
			if exp.Tier != tc.wantTier {
				t.Errorf("tier=%s want=%s", exp.Tier, tc.wantTier)
			}
			if tc.reasonHas == "" {
				if len(exp.Reasons) != 0 {
					t.Errorf("expected no reasons; got %v", exp.Reasons)
				}
				return
			}
			found := false
			for _, r := range exp.Reasons {
				if contains(r, tc.reasonHas) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no reason contained %q; reasons=%v", tc.reasonHas, exp.Reasons)
			}
		})
	}
}

// TestExplain_TransitivePeerReason — when the agent is privileged
// because of a peer, the reason must NAME that peer so an operator can
// follow the chain. "transitive: peer 'X'..." is the contract.
func TestExplain_TransitivePeerReason(t *testing.T) {
	worker := def("worker", builtins("shell_exec"))
	router := def("router", peers("worker"))
	lookup := mapLookup(map[string]*agent.Definition{"worker": worker, "router": router})

	exp := Explain(router, lookup)
	if exp.Tier != Privileged {
		t.Fatalf("router → worker(shell_exec) tier = %s; want privileged", exp.Tier)
	}
	mentions := false
	for _, r := range exp.Reasons {
		if contains(r, "peer 'worker'") {
			mentions = true
			break
		}
	}
	if !mentions {
		t.Errorf("expected a reason naming peer 'worker'; got %v", exp.Reasons)
	}
}

// contains is a tiny shim so tests don't import strings just for this.
// In Go 1.21+ we could use strings.Contains directly; the stdlib import
// is trivially cheap either way, but a local helper keeps imports tight.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
