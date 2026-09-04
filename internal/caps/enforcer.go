package caps

import (
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/auth"
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

// SetPluginSet installs (or replaces) the capability set of one plugin.
func (e *Enforcer) SetPluginSet(s *Set) {
	if s == nil || s.PluginID() == "" {
		return
	}
	e.mu.Lock()
	e.sets[s.PluginID()] = s
	e.mu.Unlock()
}

// RemovePluginSet drops a plugin's capability set (plugin unloaded).
func (e *Enforcer) RemovePluginSet(pluginID string) {
	e.mu.Lock()
	delete(e.sets, pluginID)
	e.mu.Unlock()
}

// Check decides whether principal may use cap with the given scope value
// ("" = unscoped) and writes the decision to the audit log. Non-plugin
// principals are denied: user access is RBAC's job, not the capability
// model's.
func (e *Enforcer) Check(principal Principal, cap, scope string) Decision {
	var d Decision
	if !principal.IsPlugin() {
		d = deny("principal %q is not a plugin; capability checks apply to plugins only", principal)
	} else {
		e.mu.RLock()
		set := e.sets[principal.PluginID()] // nil → default-deny via Set.Allows
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
		if d := e.Check(principal, cap, scope); !d.Allowed {
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
