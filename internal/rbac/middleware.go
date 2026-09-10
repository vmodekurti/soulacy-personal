package rbac

import (
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
)

// AgentIDSource describes where a route carries its object identifier. Sources
// are checked in order, allowing POST/GET variants of one route to share a
// single policy declaration.
type AgentIDSource struct {
	PathParam  string
	QueryParam string
	BodyField  string
	FormField  string
}

// Manager wires the Store into Fiber middleware helpers.
// All methods return fiber.Handler values so they can be used inline on any
// route or route group.
type Manager struct {
	store Store
	log   *zap.Logger
}

// NewManager creates a Manager. store may be NoopStore{} for deployments that
// don't need per-agent overrides (apikey-only, single-user).
func NewManager(store Store, log *zap.Logger) *Manager {
	return &Manager{store: store, log: log}
}

// CanAccessAgentResource exposes the same object decision used by HTTP
// middleware to non-HTTP transports such as the WebSocket event stream.
func (m *Manager) CanAccessAgentResource(role, agentID, resource, action string) (bool, error) {
	if m == nil || m.store == nil {
		return false, nil
	}
	if rs, ok := m.store.(resourceAgentStore); ok {
		return rs.CanAccessAgentResource(role, agentID, resource, action)
	}
	return m.store.CanAccessAgent(role, agentID, action)
}

// ---------------------------------------------------------------------------
// Core middleware
// ---------------------------------------------------------------------------

// Require returns a Fiber middleware that enforces (resource, action) access.
//
// Decision order:
//  1. No claims in context (open / dev mode) → allow.
//  2. Claims present → consult static default policy.
//  3. Access denied → 403 with JSON error body.
func (m *Manager) Require(resource, action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cl := auth.ClaimsFromCtx(c)
		if cl == nil {
			// Open mode or auth bypass (health endpoint etc.) — allow.
			return c.Next()
		}
		if HasPermission(cl.Role, resource, action) && cl.Allows(resource, action) {
			return c.Next()
		}
		m.log.Info("rbac: access denied",
			zap.Strings("scopes", cl.Scopes),
			zap.String("role", cl.Role),
			zap.String("resource", resource),
			zap.String("action", action),
			zap.String("sub", cl.Subject),
		)
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error":    "insufficient permissions",
			"role":     cl.Role,
			"required": resource + ":" + action,
		})
	}
}

// RequireAgent returns a Fiber middleware that enforces per-agent access.
// It extracts the agent ID from the route parameter named by agentParam
// (typically "id"), then calls Store.CanAccessAgent.
//
// Falls back to the static default policy when the store has no specific row
// for (role, agentID). If the route param is absent (e.g. /agents list),
// the check degrades to a plain resource:action check.
func (m *Manager) RequireAgent(agentParam, action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cl := auth.ClaimsFromCtx(c)
		if cl == nil {
			return c.Next()
		}
		// A scoped credential is narrowed here too, before either the
		// resource-level fallback or the per-agent grant lookup — otherwise a
		// key scoped to [chat] would still reach every /agents/:id route,
		// because the per-agent store consults the ROLE and knows nothing about
		// the credential the request arrived on.
		if !cl.Allows(ResourceAgents, action) {
			return m.deny(c, cl.Role, ResourceAgents+":"+action)
		}
		agentID := c.Params(agentParam)
		if agentID == "" {
			// No agent ID in path — fall back to resource-level check.
			if HasPermission(cl.Role, ResourceAgents, action) {
				return c.Next()
			}
			return m.deny(c, cl.Role, ResourceAgents+":"+action)
		}

		allowed, err := m.store.CanAccessAgent(cl.Role, agentID, action)
		if err != nil {
			m.log.Error("rbac: store error", zap.Error(err))
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "rbac store error",
			})
		}
		if allowed {
			return c.Next()
		}
		m.log.Info("rbac: agent access denied",
			zap.String("role", cl.Role),
			zap.String("agent_id", agentID),
			zap.String("action", action),
		)
		return m.deny(c, cl.Role, ResourceAgents+":"+action+" agent="+agentID)
	}
}

// RequireAgentFrom enforces object authorization for routes whose agent ID is
// not necessarily a path parameter (chat JSON, query APIs, multipart uploads).
func (m *Manager) RequireAgentFrom(resource, action string, sources ...AgentIDSource) fiber.Handler {
	return func(c *fiber.Ctx) error {
		cl := auth.ClaimsFromCtx(c)
		if cl == nil {
			return c.Next()
		}
		if !cl.Allows(resource, action) {
			return m.deny(c, cl.Role, resource+":"+action)
		}
		agentID := resolveAgentID(c, sources)
		if agentID == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agent_id is required for authorization"})
		}
		var allowed bool
		var err error
		if rs, ok := m.store.(resourceAgentStore); ok {
			allowed, err = rs.CanAccessAgentResource(cl.Role, agentID, resource, action)
		} else {
			allowed, err = m.store.CanAccessAgent(cl.Role, agentID, action)
		}
		if err != nil {
			m.log.Error("rbac: store error", zap.Error(err))
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "rbac store error"})
		}
		if !allowed {
			return m.deny(c, cl.Role, resource+":"+action+" agent="+agentID)
		}
		c.Locals("authorized_agent_id", agentID)
		return c.Next()
	}
}

func resolveAgentID(c *fiber.Ctx, sources []AgentIDSource) string {
	for _, src := range sources {
		if src.PathParam != "" {
			if value := strings.TrimSpace(c.Params(src.PathParam)); value != "" {
				return value
			}
		}
		if src.QueryParam != "" {
			if value := strings.TrimSpace(c.Query(src.QueryParam)); value != "" {
				return value
			}
		}
		if src.FormField != "" {
			if value := strings.TrimSpace(c.FormValue(src.FormField)); value != "" {
				return value
			}
		}
		if src.BodyField != "" && len(c.Body()) > 0 {
			var body map[string]json.RawMessage
			if json.Unmarshal(c.Body(), &body) == nil {
				var value string
				if json.Unmarshal(body[src.BodyField], &value) == nil && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		}
	}
	return ""
}

func (m *Manager) deny(c *fiber.Ctx, role, required string) error {
	return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
		"error":    "insufficient permissions",
		"role":     role,
		"required": required,
	})
}

// ---------------------------------------------------------------------------
// Admin API handlers (mounted under /api/v1/rbac)
// ---------------------------------------------------------------------------

// HandleListGrants returns all per-agent grant rows.
//
//	GET /api/v1/rbac/grants
func (m *Manager) HandleListGrants(c *fiber.Ctx) error {
	grants, err := m.store.ListAgentGrants()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if grants == nil {
		grants = []AgentGrant{}
	}
	return c.JSON(fiber.Map{"grants": grants, "count": len(grants)})
}

// HandleListGrantsForRole returns per-agent grants for a single role.
//
//	GET /api/v1/rbac/grants/:role
func (m *Manager) HandleListGrantsForRole(c *fiber.Ctx) error {
	role := c.Params("role")
	if !IsKnownRole(role) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "unknown role; must be one of: admin, operator, viewer",
		})
	}
	grants, err := m.store.ListAgentGrantsForRole(role)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if grants == nil {
		grants = []AgentGrant{}
	}
	return c.JSON(fiber.Map{"grants": grants, "count": len(grants)})
}

// HandleSetAgentGrant upserts a per-agent grant.
//
//	PUT /api/v1/rbac/grants/:role/:agent_id
//
//	Body: {"actions": ["read","chat"]}
func (m *Manager) HandleSetAgentGrant(c *fiber.Ctx) error {
	role := c.Params("role")
	agentID := c.Params("agent_id")
	if !IsKnownRole(role) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "unknown role; must be one of: admin, operator, viewer",
		})
	}
	var body struct {
		Actions []string `json:"actions"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if len(body.Actions) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "actions must not be empty"})
	}
	g := AgentGrant{Role: role, AgentID: agentID, Actions: body.Actions}
	if err := m.store.SetAgentGrant(g); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusOK).JSON(g)
}

// HandleDeleteAgentGrant removes a per-agent grant row.
//
//	DELETE /api/v1/rbac/grants/:role/:agent_id
func (m *Manager) HandleDeleteAgentGrant(c *fiber.Ctx) error {
	role := c.Params("role")
	agentID := c.Params("agent_id")
	if err := m.store.DeleteAgentGrant(role, agentID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// HandleListPolicy returns the static default policy for all roles.
// Useful for GUI role editors.
//
//	GET /api/v1/rbac/policy
func (m *Manager) HandleListPolicy(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"policy": defaultPolicy})
}
