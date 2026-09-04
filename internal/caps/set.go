package caps

import (
	"fmt"

	"github.com/soulacy/soulacy/pkg/plugin"
)

// Decision is the outcome of a capability check. Reason is always populated
// on deny so the audit trail explains itself.
type Decision struct {
	Allowed bool
	Reason  string
}

func allow(reason string) Decision { return Decision{Allowed: true, Reason: reason} }
func deny(format string, args ...any) Decision {
	return Decision{Allowed: false, Reason: fmt.Sprintf(format, args...)}
}

// grant is one compiled capability: the set of scope values it covers.
// unrestricted == true means the manifest declared the cap with an empty
// scope list (or "*"), granting it for any scope value.
type grant struct {
	unrestricted bool
	values       map[string]bool
}

// Set is the compiled, validated capability set of one plugin.
// The zero/nil Set denies everything.
type Set struct {
	pluginID string
	grants   map[string]*grant // cap name → grant
}

// NewSet validates the manifest permissions of a plugin and compiles them
// into a Set. It fails on: empty plugin ID, malformed or unknown capability
// names, and scope lists that don't match the capability's registered scope
// kind (e.g. channel.send declared with agents).
func NewSet(pluginID string, perms []plugin.Permission) (*Set, error) {
	if pluginID == "" {
		return nil, fmt.Errorf("caps: plugin ID must not be empty")
	}
	s := &Set{pluginID: pluginID, grants: map[string]*grant{}}
	for i, p := range perms {
		kind, ok := ScopeKindOf(p.Cap)
		if !ok {
			if _, _, err := ParseCap(p.Cap); err != nil {
				return nil, fmt.Errorf("caps: permission %d: %w", i, err)
			}
			return nil, fmt.Errorf("caps: permission %d: unknown capability %q (known: %v)", i, p.Cap, KnownCaps())
		}
		values, err := scopeValues(p, kind)
		if err != nil {
			return nil, fmt.Errorf("caps: permission %d (%s): %w", i, p.Cap, err)
		}
		g := s.grants[p.Cap]
		if g == nil {
			g = &grant{values: map[string]bool{}}
			s.grants[p.Cap] = g
		}
		if len(values) == 0 {
			g.unrestricted = true
			continue
		}
		for _, v := range values {
			if v == "*" {
				g.unrestricted = true
				continue
			}
			g.values[v] = true
		}
	}
	return s, nil
}

// scopeValues returns the scope list matching the capability's kind and
// rejects lists of any other kind.
func scopeValues(p plugin.Permission, kind ScopeKind) ([]string, error) {
	lists := map[ScopeKind][]string{
		ScopeAgents:   p.Agents,
		ScopeChannels: p.Channels,
		ScopeTypes:    p.Types,
	}
	for k, l := range lists {
		if k != kind && len(l) > 0 {
			return nil, fmt.Errorf("scope kind is %q; %q list not allowed", kind, k)
		}
	}
	return lists[kind], nil
}

// PluginID returns the owning plugin's ID.
func (s *Set) PluginID() string {
	if s == nil {
		return ""
	}
	return s.pluginID
}

// Principal returns the plugin principal this set belongs to.
func (s *Set) Principal() Principal { return PluginPrincipal(s.PluginID()) }

// Allows checks capability cap for one scope value (pass "" for an unscoped
// check). Default-deny: undeclared capabilities are refused. A capability
// declared with a restricted scope list refuses unscoped checks, because the
// restriction cannot be verified.
func (s *Set) Allows(cap, scope string) Decision {
	if s == nil {
		return deny("no capability set (default-deny)")
	}
	g, ok := s.grants[cap]
	if !ok {
		return deny("capability %s not declared by plugin %s (default-deny)", cap, s.pluginID)
	}
	if g.unrestricted {
		return allow("declared unscoped")
	}
	if scope == "" {
		return deny("capability %s is scope-restricted; unscoped use refused", cap)
	}
	if g.values[scope] {
		return allow("scope listed")
	}
	return deny("scope %q not listed for capability %s", scope, cap)
}
