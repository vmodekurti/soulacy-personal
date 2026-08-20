package caps

import (
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// AuditSink receives one entry per capability decision. *audit.Logger
// satisfies it; tests use an in-memory fake.
type AuditSink interface {
	Log(e audit.Entry)
}

// Enforcer is the host-API boundary check for plugin principals. It holds the
// compiled capability set of every loaded plugin and records each allow/deny
// decision in the audit log. User requests are NOT handled here — the
// RequireCapability middleware passes non-plugin principals straight through
// to the existing RBAC chain.
type Enforcer struct {
	mu    sync.RWMutex
	sets  map[string]*Set // plugin ID → set
	sink  AuditSink
	log   *zap.Logger
	clock func() time.Time
}

// NewEnforcer creates an Enforcer. sink may be nil (decisions are then only
// logged via log); log must not be nil (use zap.NewNop() in tests).
func NewEnforcer(sink AuditSink, log *zap.Logger) *Enforcer {
	return &Enforcer{
		sets:  map[string]*Set{},
		sink:  sink,
		log:   log,
		clock: time.Now,
	}
}

// setKey is the map key: workspace first, then plugin.
//
// THE WORKSPACE IS PART OF THE KEY rather than a filter applied at lookup
// time, and that is the whole reason this function exists. Plugin IDs are
// chosen by whoever wrote the plugin, so two workspaces can install different
// plugins under the same ID — and with a map keyed by plugin ID alone the
// second install silently replaced the first's capability set for BOTH
// tenants. Whichever workspace installed last decided what the other one was
// allowed to do.
//
// A NUL separator because plugin IDs and workspace IDs are both restricted
// character sets that exclude it, so no pair of distinct inputs can produce
// the same key.
func setKey(workspaceID, pluginID string) string {
	return wsroot.Normalize(workspaceID) + "\x00" + pluginID
}

// SetPluginSet installs (or replaces) one plugin's capability set in one
// workspace.
//
// The workspace is a required argument, not an option with a default. A
// defaulted workspace here would be a filter every future caller has to
// remember, and forgetting it grants a plugin's capabilities to the personal
// workspace — which in a Team deployment is the one workspace that
// structurally contains all the others.
func (e *Enforcer) SetPluginSet(workspaceID string, s *Set) {
	if s == nil || s.PluginID() == "" {
		return
	}
	e.mu.Lock()
	e.sets[setKey(workspaceID, s.PluginID())] = s
	e.mu.Unlock()
}

// RemovePluginSet drops a plugin's capability set in one workspace.
//
// IT HAD NO CALLERS. The function existed from the start and nothing invoked
// it, so revoking a plugin removed its TOOLS — the agent could no longer call
// it — and left its capability grant standing. A plugin revoked because it was
// doing something it should not could still reach every host API its manifest
// had asked for, until the gateway restarted.
//
// That is the worse half of a revocation to get wrong, and it was the silent
// one: the tools disappear visibly, so the revocation looks complete.
func (e *Enforcer) RemovePluginSet(workspaceID, pluginID string) {
	e.mu.Lock()
	delete(e.sets, setKey(workspaceID, pluginID))
	e.mu.Unlock()
}

// ReplaceWorkspaceSets makes one workspace's capability grants exactly those
// of the plugins it currently has.
//
// Reconciles rather than removes, because a lifecycle change can add as well
// as take away: approving a plugin has to grant its capabilities, re-approving
// one whose manifest changed has to replace them, and revoking has to drop
// them. A caller that had to work out which of the three happened would get it
// wrong on the case nobody tests, which is the second one.
func (e *Enforcer) ReplaceWorkspaceSets(workspaceID string, sets []*Set) {
	prefix := wsroot.Normalize(workspaceID) + "\x00"
	e.mu.Lock()
	defer e.mu.Unlock()
	for key := range e.sets {
		if strings.HasPrefix(key, prefix) {
			delete(e.sets, key)
		}
	}
	for _, set := range sets {
		if set == nil || set.PluginID() == "" {
			continue
		}
		e.sets[setKey(workspaceID, set.PluginID())] = set
	}
}

// Check decides whether principal may use cap with the given scope value
// ("" = unscoped) and writes the decision to the audit log. Non-plugin
// principals are denied: user access is RBAC's job, not the capability
// model's.
func (e *Enforcer) Check(workspaceID string, principal Principal, cap, scope string) Decision {
	var d Decision
	if !principal.IsPlugin() {
		d = deny("principal %q is not a plugin; capability checks apply to plugins only", principal)
	} else {
		e.mu.RLock()
		// nil → default-deny via Set.Allows, which is also what a plugin
		// installed in a DIFFERENT workspace resolves to here. That is the
		// point: a plugin's grant is worth nothing outside the workspace that
		// approved it.
		set := e.sets[setKey(workspaceID, principal.PluginID())]
		e.mu.RUnlock()
		d = set.Allows(cap, scope)
	}
	e.record(principal, cap, scope, d)
	return d
}

// record writes one audit entry per decision. SessionID carries the
// principal so each plugin's decisions land in their own audit file.
func (e *Enforcer) record(principal Principal, cap, scope string, d Decision) {
	if e.sink != nil {
		entry := audit.Entry{
			Timestamp: e.clock(),
			SessionID: string(principal),
			Tool:      "cap:" + cap,
			Args:      map[string]any{"scope": scope},
			Denied:    !d.Allowed,
		}
		if !d.Allowed {
			entry.Error = d.Reason
		}
		e.sink.Log(entry)
	}
	if !d.Allowed {
		e.log.Info("caps: denied",
			zap.String("principal", string(principal)),
			zap.String("cap", cap),
			zap.String("scope", scope),
			zap.String("reason", d.Reason),
		)
	}
}

// RequireCapability returns Fiber middleware enforcing cap at the host-API
// boundary. scopeFn extracts the scope value from the request (nil = unscoped
// check).
//
// Decision order:
//  1. No claims (open/dev mode) → pass.
//  2. Claims whose subject is not a plugin principal → pass; the user RBAC
//     middleware further down the chain governs users. (User RBAC untouched.)
//  3. Plugin principal → Check; deny → 403 with the required capability.
func (e *Enforcer) RequireCapability(cap string, scopeFn func(*fiber.Ctx) string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cl := auth.ClaimsFromCtx(c)
		if cl == nil || !strings.HasPrefix(cl.Subject, PrincipalPrefix) {
			return c.Next()
		}
		principal := Principal(cl.Subject)
		scope := ""
		if scopeFn != nil {
			scope = scopeFn(c)
		}
		// The workspace comes from the verified claims, never from a header or
		// a body: it decides WHICH grant applies, so a caller-supplied value
		// would let a plugin token pick the workspace whose grant it prefers.
		if d := e.Check(cl.WorkspaceID, principal, cap, scope); !d.Allowed {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error":     "capability denied",
				"principal": string(principal),
				"required":  cap,
				"reason":    d.Reason,
			})
		}
		return c.Next()
	}
}
