package metrics

import "testing"

func TestOnlyNamesThisBinaryDefinesAppearVerbatim(t *testing.T) {
	// Everything a tenant or an operator can name becomes a class. The four
	// prefixed cases each carry an identifier somebody outside this repo
	// chose: an MCP server ID, a plugin ID, a peer AGENT ID.
	for _, tc := range []struct {
		name    string
		builtin bool
		want    string
	}{
		{"read_file", true, "read_file"},
		{"web_search", true, "web_search"},
		{"mcp__acme-internal__query", false, "mcp"},
		{"plugin__acme-billing__charge", false, "plugin"},
		{"agent__invoice-reconciliation", false, "peer_agent"},
		{"scrape_customer_portal", false, "custom"},
		{"", false, "custom"},
	} {
		if got := ToolLabel(tc.name, tc.builtin); got != tc.want {
			t.Errorf("ToolLabel(%q, %v) = %q, want %q", tc.name, tc.builtin, got, tc.want)
		}
	}
}

func TestAPrefixWinsOverTheBuiltinClaim(t *testing.T) {
	// The caller decides `builtin`, and a caller that got it wrong — a stale
	// registry, a plugin shadowing a builtin name — must not be able to turn
	// a tenant-authored name into a verbatim label. The prefixes are checked
	// FIRST for that reason, so being wrong about builtin-ness can only ever
	// lose detail, never leak.
	if got := ToolLabel("mcp__acme-internal__query", true); got != "mcp" {
		t.Errorf("a caller claiming builtin turned a tenant's MCP server id into the label %q", got)
	}
}
