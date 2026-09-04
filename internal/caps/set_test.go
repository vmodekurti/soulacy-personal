package caps

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/plugin"
)

func mustSet(t *testing.T, id string, perms []plugin.Permission) *Set {
	t.Helper()
	s, err := NewSet(id, perms)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// NewSet validation
// ---------------------------------------------------------------------------

func TestNewSet_UnknownCap_Error(t *testing.T) {
	_, err := NewSet("p1", []plugin.Permission{{Cap: "nuclear.launch"}})
	if err == nil {
		t.Fatal("NewSet(unknown cap) = nil error, want error")
	}
}

func TestNewSet_MalformedCap_Error(t *testing.T) {
	_, err := NewSet("p1", []plugin.Permission{{Cap: "vectorsearch"}})
	if err == nil {
		t.Fatal("NewSet(malformed cap) = nil error, want error")
	}
}

func TestNewSet_WrongScopeKind_Error(t *testing.T) {
	// channel.send is scoped by channels; declaring agents is a manifest bug.
	_, err := NewSet("p1", []plugin.Permission{
		{Cap: CapChannelSend, Agents: []string{"support-bot"}},
	})
	if err == nil {
		t.Fatal("NewSet(wrong scope kind) = nil error, want error")
	}
}

func TestNewSet_EmptyPluginID_Error(t *testing.T) {
	_, err := NewSet("", nil)
	if err == nil {
		t.Fatal("NewSet(empty id) = nil error, want error")
	}
}

func TestNewSet_Valid(t *testing.T) {
	s := mustSet(t, "matrix-suite", []plugin.Permission{
		{Cap: CapVectorSearch, Agents: []string{"support-bot"}},
		{Cap: CapChannelSend, Channels: []string{"matrix"}},
		{Cap: CapEventsSubscribe, Types: []string{"run.finished"}},
	})
	if s.PluginID() != "matrix-suite" {
		t.Fatalf("PluginID = %q", s.PluginID())
	}
	if p := s.Principal(); string(p) != "plugin:matrix-suite" {
		t.Fatalf("Principal = %q", p)
	}
}

// ---------------------------------------------------------------------------
// Allows semantics — default-deny
// ---------------------------------------------------------------------------

func TestSet_DefaultDeny_UndeclaredCap(t *testing.T) {
	s := mustSet(t, "p1", []plugin.Permission{
		{Cap: CapVectorSearch, Agents: []string{"support-bot"}},
	})
	d := s.Allows(CapChannelSend, "matrix")
	if d.Allowed {
		t.Fatal("undeclared cap allowed, want deny")
	}
	if d.Reason == "" {
		t.Fatal("deny decision missing reason")
	}
}

func TestSet_NoPermissions_DeniesEverything(t *testing.T) {
	s := mustSet(t, "p1", nil)
	for _, cap := range []string{CapVectorSearch, CapChannelSend, CapEventsSubscribe} {
		if s.Allows(cap, "anything").Allowed {
			t.Errorf("cap %s allowed with empty permission list", cap)
		}
	}
}

func TestSet_ScopedAllow(t *testing.T) {
	s := mustSet(t, "p1", []plugin.Permission{
		{Cap: CapVectorSearch, Agents: []string{"support-bot", "sales-bot"}},
	})
	if !s.Allows(CapVectorSearch, "support-bot").Allowed {
		t.Fatal("listed scope denied, want allow")
	}
	if s.Allows(CapVectorSearch, "other-bot").Allowed {
		t.Fatal("unlisted scope allowed, want deny")
	}
}

func TestSet_EmptyScopeList_AllowsAnyScope(t *testing.T) {
	// A declared cap with no scope values grants the capability unscoped.
	s := mustSet(t, "p1", []plugin.Permission{{Cap: CapChannelSend}})
	if !s.Allows(CapChannelSend, "matrix").Allowed {
		t.Fatal("declared cap with empty scope denied a value, want allow")
	}
	if !s.Allows(CapChannelSend, "").Allowed {
		t.Fatal("declared cap with empty scope denied unscoped check, want allow")
	}
}

func TestSet_WildcardScope(t *testing.T) {
	s := mustSet(t, "p1", []plugin.Permission{
		{Cap: CapEventsSubscribe, Types: []string{"*"}},
	})
	if !s.Allows(CapEventsSubscribe, "run.finished").Allowed {
		t.Fatal("wildcard scope denied, want allow")
	}
}

func TestSet_RestrictedScope_EmptyValueDenied(t *testing.T) {
	// If the manifest restricts the scope, a check without a scope value must
	// be denied — the host cannot verify the restriction.
	s := mustSet(t, "p1", []plugin.Permission{
		{Cap: CapVectorSearch, Agents: []string{"support-bot"}},
	})
	if s.Allows(CapVectorSearch, "").Allowed {
		t.Fatal("empty scope value allowed against restricted scope, want deny")
	}
}

func TestSet_Nil_Denies(t *testing.T) {
	var s *Set
	if s.Allows(CapVectorSearch, "x").Allowed {
		t.Fatal("nil set allowed, want deny")
	}
}

func TestSet_MergesDuplicateCapDeclarations(t *testing.T) {
	s := mustSet(t, "p1", []plugin.Permission{
		{Cap: CapChannelSend, Channels: []string{"matrix"}},
		{Cap: CapChannelSend, Channels: []string{"irc"}},
	})
	if !s.Allows(CapChannelSend, "matrix").Allowed || !s.Allows(CapChannelSend, "irc").Allowed {
		t.Fatal("duplicate declarations not merged")
	}
	if s.Allows(CapChannelSend, "slack").Allowed {
		t.Fatal("merged set allowed unlisted scope")
	}
}
