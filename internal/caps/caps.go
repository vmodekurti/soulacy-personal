// Package caps implements Soulacy's plugin principal and capability model
// (Story E5, docs/EXTENSIBILITY.md §5.4).
//
// # Principals
//
// A plugin is a security principal of the form `plugin:<id>` — distinct from
// user roles (admin/operator/viewer, see internal/rbac). User RBAC is
// untouched by this package: the capability middleware acts only on plugin
// principals and passes every other request through to the RBAC chain.
//
// # Capabilities
//
// A capability is `resource.action` (lowercase, single dot) plus a scope list
// declared in the plugin manifest (pkg/plugin.Permission). The scope kind —
// agents, channels, or event types — is fixed per capability when it is
// registered. Plugins are DEFAULT-DENY: a capability not declared in the
// manifest is refused, and an unknown capability name fails manifest
// validation outright.
//
// # Adding a new capability
//
//  1. Pick a name in the `resource.action` grammar and the scope kind that
//     limits it (ScopeAgents, ScopeChannels, or ScopeTypes).
//  2. Add a Cap* constant below and register it in init() — or call
//     Register from the subsystem that owns the resource.
//  3. Enforce it at the host-API boundary with Enforcer.Check (service code)
//     or Enforcer.RequireCapability (Fiber routes).
//  4. Document it in docs/PLUGIN_CAPABILITIES.md and add an allow + deny test.
//
// Every allow/deny decision is recorded in the audit log (internal/audit).
package caps

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Principal
// ---------------------------------------------------------------------------

// PrincipalPrefix marks plugin principals, e.g. "plugin:matrix-suite".
const PrincipalPrefix = "plugin:"

// Principal identifies the actor a request runs as. User principals are
// whatever the auth layer issues (subjects, roles); plugin principals are
// "plugin:<id>".
type Principal string

// PluginPrincipal builds the principal for a plugin ID.
func PluginPrincipal(id string) Principal {
	return Principal(PrincipalPrefix + id)
}

// IsPlugin reports whether p is a plugin principal with a non-empty ID.
func (p Principal) IsPlugin() bool {
	return strings.HasPrefix(string(p), PrincipalPrefix) &&
		len(p) > len(PrincipalPrefix)
}

// PluginID returns the plugin ID ("" when p is not a plugin principal).
func (p Principal) PluginID() string {
	if !p.IsPlugin() {
		return ""
	}
	return string(p)[len(PrincipalPrefix):]
}

// ---------------------------------------------------------------------------
// Capability grammar
// ---------------------------------------------------------------------------

// capPattern enforces `resource.action`: lowercase alphanumeric segments
// (underscores allowed inside), exactly one dot.
var capPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// ParseCap splits a capability name into (resource, action), validating the
// grammar.
func ParseCap(cap string) (resource, action string, err error) {
	if !capPattern.MatchString(cap) {
		return "", "", fmt.Errorf("capability %q: must match resource.action (lowercase, one dot)", cap)
	}
	parts := strings.SplitN(cap, ".", 2)
	return parts[0], parts[1], nil
}

// ---------------------------------------------------------------------------
// Scope kinds and capability registry
// ---------------------------------------------------------------------------

// ScopeKind names the manifest list that limits a capability.
type ScopeKind string

const (
	ScopeAgents   ScopeKind = "agents"
	ScopeChannels ScopeKind = "channels"
	ScopeTypes    ScopeKind = "types"
)

func validScopeKind(k ScopeKind) bool {
	switch k {
	case ScopeAgents, ScopeChannels, ScopeTypes:
		return true
	}
	return false
}

// Initial capability set (small on purpose — see package doc for how to grow it).
const (
	// CapVectorSearch lets a plugin run vector/knowledge searches, scoped to
	// the listed agents' knowledge bases.
	CapVectorSearch = "vector.search"
	// CapChannelSend lets a plugin send outbound messages on the listed channels.
	CapChannelSend = "channel.send"
	// CapEventsSubscribe lets a plugin subscribe to the listed event types
	// (docs/EVENTS.md).
	CapEventsSubscribe = "events.subscribe"
)

var (
	regMu    sync.RWMutex
	registry = map[string]ScopeKind{}
)

func init() {
	for cap, kind := range map[string]ScopeKind{
		CapVectorSearch:    ScopeAgents,
		CapChannelSend:     ScopeChannels,
		CapEventsSubscribe: ScopeTypes,
	} {
		if err := Register(cap, kind); err != nil {
			panic(err)
		}
	}
}

// Register adds a capability to the registry. The name must follow the
// `resource.action` grammar and must not already be registered.
func Register(cap string, kind ScopeKind) error {
	if _, _, err := ParseCap(cap); err != nil {
		return err
	}
	if !validScopeKind(kind) {
		return fmt.Errorf("capability %q: unknown scope kind %q", cap, kind)
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, exists := registry[cap]; exists {
		return fmt.Errorf("capability %q already registered", cap)
	}
	registry[cap] = kind
	return nil
}

// unregister removes a capability (test helper only).
func unregister(cap string) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(registry, cap)
}

// ScopeKindOf returns the scope kind of a registered capability.
func ScopeKindOf(cap string) (ScopeKind, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	k, ok := registry[cap]
	return k, ok
}

// KnownCaps returns the sorted list of registered capability names.
func KnownCaps() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for cap := range registry {
		out = append(out, cap)
	}
	sort.Strings(out)
	return out
}
