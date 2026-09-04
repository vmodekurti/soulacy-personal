package caps

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Principal
// ---------------------------------------------------------------------------

func TestPluginPrincipal_Format(t *testing.T) {
	p := PluginPrincipal("matrix")
	if string(p) != "plugin:matrix" {
		t.Fatalf("PluginPrincipal = %q, want plugin:matrix", p)
	}
	if !p.IsPlugin() {
		t.Fatal("IsPlugin() = false, want true")
	}
	if p.PluginID() != "matrix" {
		t.Fatalf("PluginID() = %q, want matrix", p.PluginID())
	}
}

func TestPrincipal_NonPlugin(t *testing.T) {
	for _, s := range []string{"", "admin", "user:bob", "plugin", "Plugin:matrix"} {
		p := Principal(s)
		if p.IsPlugin() {
			t.Errorf("Principal(%q).IsPlugin() = true, want false", s)
		}
		if p.PluginID() != "" {
			t.Errorf("Principal(%q).PluginID() = %q, want empty", s, p.PluginID())
		}
	}
}

func TestPluginPrincipal_EmptyIDNotPlugin(t *testing.T) {
	// "plugin:" with no id must not count as a valid plugin principal.
	p := Principal("plugin:")
	if p.IsPlugin() {
		t.Fatal(`Principal("plugin:").IsPlugin() = true, want false`)
	}
}

// ---------------------------------------------------------------------------
// Capability grammar
// ---------------------------------------------------------------------------

func TestParseCap_Valid(t *testing.T) {
	res, act, err := ParseCap("vector.search")
	if err != nil {
		t.Fatalf("ParseCap: %v", err)
	}
	if res != "vector" || act != "search" {
		t.Fatalf("ParseCap = (%q, %q), want (vector, search)", res, act)
	}
}

func TestParseCap_Invalid(t *testing.T) {
	for _, s := range []string{
		"", "vector", "vector.", ".search", "vector.search.extra",
		"Vector.Search", "vector search", " vector.search", "vector.search ",
		"vec tor.send",
	} {
		if _, _, err := ParseCap(s); err == nil {
			t.Errorf("ParseCap(%q) = nil error, want error", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Capability registry
// ---------------------------------------------------------------------------

func TestRegistry_Builtins(t *testing.T) {
	cases := map[string]ScopeKind{
		CapVectorSearch:    ScopeAgents,
		CapChannelSend:     ScopeChannels,
		CapEventsSubscribe: ScopeTypes,
	}
	for cap, kind := range cases {
		got, ok := ScopeKindOf(cap)
		if !ok {
			t.Errorf("ScopeKindOf(%q): not registered", cap)
			continue
		}
		if got != kind {
			t.Errorf("ScopeKindOf(%q) = %q, want %q", cap, got, kind)
		}
	}
}

func TestRegistry_UnknownCap(t *testing.T) {
	if _, ok := ScopeKindOf("nuclear.launch"); ok {
		t.Fatal("ScopeKindOf(nuclear.launch) registered, want unknown")
	}
}

func TestRegister_NewCapability(t *testing.T) {
	const cap = "memory.read"
	if err := Register(cap, ScopeAgents); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { unregister(cap) })

	kind, ok := ScopeKindOf(cap)
	if !ok || kind != ScopeAgents {
		t.Fatalf("after Register, ScopeKindOf = (%q, %v)", kind, ok)
	}
}

func TestRegister_Duplicate_Error(t *testing.T) {
	err := Register(CapVectorSearch, ScopeAgents)
	if err == nil {
		t.Fatal("Register(duplicate) = nil error, want error")
	}
}

func TestRegister_BadGrammar_Error(t *testing.T) {
	if err := Register("notacap", ScopeAgents); err == nil {
		t.Fatal("Register(notacap) = nil error, want error")
	}
	if err := Register("a.b", ScopeKind("bogus")); err == nil {
		t.Fatal("Register with unknown scope kind = nil error, want error")
	}
}

func TestKnownCaps_Sorted(t *testing.T) {
	caps := KnownCaps()
	if len(caps) < 3 {
		t.Fatalf("KnownCaps() returned %d entries, want >= 3", len(caps))
	}
	if !strings.Contains(strings.Join(caps, ","), CapChannelSend) {
		t.Fatalf("KnownCaps() missing %s: %v", CapChannelSend, caps)
	}
	for i := 1; i < len(caps); i++ {
		if caps[i-1] >= caps[i] {
			t.Fatalf("KnownCaps() not sorted: %v", caps)
		}
	}
}
