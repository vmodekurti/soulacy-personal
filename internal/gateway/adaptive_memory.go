package gateway

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/rbac"
)

// AdaptiveMemoryRebuilder re-creates the adaptive memory engine from a config
// section. The app installs one so a PATCH /config that changes
// memory.adaptive takes effect without a restart.
type AdaptiveMemoryRebuilder func(config.AdaptiveMemoryConfig) error

// SetAdaptiveMemoryRebuilder installs the hot-reload hook.
func (s *Server) SetAdaptiveMemoryRebuilder(fn AdaptiveMemoryRebuilder) { s.adaptiveRebuild = fn }

// registerAdaptiveMemoryRoutes mounts the fact management API (Story E30).
//
//	GET    /memory/facts?agent_id=&status=&q=&owner=   list or search
//	POST   /memory/facts                               add a fact by hand
//	PATCH  /memory/facts/:id                           edit content/category
//	DELETE /memory/facts/:id                           delete one fact
//	GET    /memory/facts/export?agent_id=              JSON download
//	DELETE /memory/facts?agent_id=&confirm=true        purge
//	GET    /memory/facts/status                        provider + counts
//
// Every route is scoped to the caller's own identity. Admins may pass
// ?owner= to inspect another user's workspace-scoped memory; everyone else
// gets 403 for any owner but themselves.
func (s *Server) registerAdaptiveMemoryRoutes(api fiber.Router) {
	read := s.rbacMW(rbac.ResourceMemory, rbac.ActionRead)
	write := s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite)
	del := s.rbacMW(rbac.ResourceMemory, rbac.ActionDelete)
	api.Get("/memory/facts/status", read, s.handleAdaptiveMemoryStatus)
	api.Get("/memory/facts/export", read, s.handleAdaptiveMemoryExport)
	api.Get("/memory/facts", read, s.handleAdaptiveMemoryList)
	api.Post("/memory/facts", write, s.handleAdaptiveMemoryAdd)
	api.Patch("/memory/facts/:id", write, s.handleAdaptiveMemoryUpdate)
	api.Delete("/memory/facts/:id", del, s.handleAdaptiveMemoryDelete)
	api.Delete("/memory/facts", del, s.handleAdaptiveMemoryPurge)
}

// adaptiveScopeFor resolves the (workspace, owner, agent) scope for a request
// and enforces the owner boundary. It also rejects credentials passed in the
// URL, matching the private-learning routes.
func (s *Server) adaptiveScopeFor(c *fiber.Ctx) (memory.Adaptive, memory.FactScope, error) {
	engine := s.engine.AdaptiveMemory()
	if engine == nil || !s.engine.AdaptiveMemoryOptions().Enabled {
		return nil, memory.FactScope{}, fiber.NewError(fiber.StatusServiceUnavailable, "Adaptive memory is disabled (memory.adaptive.enabled)")
	}
	if c.Get("Authorization") == "" && c.Cookies("soulacy_access") == "" {
		return nil, memory.FactScope{}, fiber.NewError(fiber.StatusForbidden, "Memory does not accept credentials in URLs")
	}
	principal, ok := requestPrincipal(c)
	if !ok || strings.TrimSpace(principal.Subject) == "" {
		return nil, memory.FactScope{}, fiber.NewError(fiber.StatusForbidden, "Memory requires a stable authenticated identity")
	}
	claims := auth.ClaimsFromCtx(c)
	owner := principal.Subject
	if requested := strings.TrimSpace(c.Query("owner")); requested != "" && requested != owner {
		if claims == nil || !strings.EqualFold(claims.Role, rbac.RoleAdmin) {
			return nil, memory.FactScope{}, fiber.NewError(fiber.StatusForbidden, "Only workspace admins can view another member's memory")
		}
		owner = requested
	}
	scope := memory.FactScope{Workspace: s.adaptiveWorkspace(), Owner: owner, AgentID: strings.TrimSpace(c.Query("agent_id"))}.Normalize()
	if scope.AgentID != "" && s.loader != nil && s.loader.Get(scope.AgentID) == nil {
		return nil, memory.FactScope{}, fiber.NewError(fiber.StatusNotFound, "Agent not found")
	}
	return engine, scope, nil
}

// adaptiveWorkspace names the tenancy workspace. Personal is single-tenant,
// so this is the default workspace; a multi-tenant edition overrides it.
func (s *Server) adaptiveWorkspace() string { return memory.DefaultWorkspace }

func (s *Server) adaptiveCtx(c *fiber.Ctx) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.UserContext(), s.httpRequestTimeout())
}

func adaptiveErr(c *fiber.Ctx, err error) error {
	var fe *fiber.Error
	switch {
	case errors.As(err, &fe):
		return c.Status(fe.Code).JSON(fiber.Map{"error": fe.Message})
	case errors.Is(err, memory.ErrFactNotFound):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Fact not found"})
	case errors.Is(err, memory.ErrInvalidFact):
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "A fact is one short sentence (1-400 characters) with a category of preference, identity, constraint, or entity"})
	case errors.Is(err, memory.ErrInvalidScope):
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Memory requires a stable authenticated identity"})
	case errors.Is(err, memory.ErrProviderFailed):
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "The memory provider did not respond: " + err.Error()})
	}
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
}

func (s *Server) handleAdaptiveMemoryStatus(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	engine := s.engine.AdaptiveMemory()
	opts := s.engine.AdaptiveMemoryOptions()
	out := fiber.Map{
		"enabled":             engine != nil && opts.Enabled,
		"provider":            "",
		"max_prompt_facts":    opts.MaxPromptFacts,
		"prompt_token_budget": opts.PromptTokenBudget,
		"model_provider":      opts.ModelProvider,
		"model":               opts.Model,
	}
	if engine == nil {
		return c.JSON(out)
	}
	out["provider"] = engine.Provider()
	_, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return c.JSON(out)
	}
	out["owner"] = scope.Owner
	if local, ok := engine.(*memory.LocalAdaptive); ok && local.Store() != nil {
		ctx, cancel := s.adaptiveCtx(c)
		defer cancel()
		active, superseded, cerr := local.Store().Count(ctx, scope)
		if cerr == nil {
			out["active"] = active
			out["superseded"] = superseded
		}
	}
	return c.JSON(out)
}

func (s *Server) handleAdaptiveMemoryList(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	status := c.Query("status", "")
	if status != "" && status != memory.FactStatusActive && status != memory.FactStatusSuperseded {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "status must be active or superseded"})
	}
	limit, _ := strconv.Atoi(c.Query("limit", "200"))
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		hits, err := engine.Recall(ctx, scope, q, max(1, min(limit, 50)))
		if err != nil {
			return adaptiveErr(c, err)
		}
		return c.JSON(fiber.Map{"facts": hits, "provider": engine.Provider(), "owner": scope.Owner, "query": q})
	}
	facts, err := engine.List(ctx, scope, status, limit)
	if err != nil {
		return adaptiveErr(c, err)
	}
	if facts == nil {
		facts = []memory.Fact{}
	}
	return c.JSON(fiber.Map{"facts": facts, "provider": engine.Provider(), "owner": scope.Owner})
}

type adaptiveFactBody struct {
	AgentID  string `json:"agent_id"`
	Content  string `json:"content"`
	Category string `json:"category"`
}

func (s *Server) handleAdaptiveMemoryAdd(c *fiber.Ctx) error {
	var body adaptiveFactBody
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if body.AgentID != "" {
		c.Request().URI().QueryArgs().Set("agent_id", body.AgentID)
	}
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	if body.Category == "" {
		body.Category = string(memory.FactPreference)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	f, err := engine.Add(ctx, scope, memory.FactCategory(strings.ToLower(body.Category)), body.Content)
	if err != nil {
		return adaptiveErr(c, err)
	}
	s.recordAdminAudit(c, "memory.fact.add", "memory", f.ID, "ok", map[string]any{"agent_id": scope.AgentID, "category": f.Category})
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"fact": f})
}

func (s *Server) handleAdaptiveMemoryUpdate(c *fiber.Ctx) error {
	var body adaptiveFactBody
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if body.AgentID != "" {
		c.Request().URI().QueryArgs().Set("agent_id", body.AgentID)
	}
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	id := strings.Clone(c.Params("id"))
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	if body.Category == "" {
		// Keep the current category when the client only edits text.
		if cur, gerr := engine.List(ctx, scope, "", 2000); gerr == nil {
			for _, f := range cur {
				if f.ID == id {
					body.Category = string(f.Category)
					break
				}
			}
		}
		if body.Category == "" {
			body.Category = string(memory.FactPreference)
		}
	}
	f, err := engine.Update(ctx, scope, id, body.Content, memory.FactCategory(strings.ToLower(body.Category)))
	if err != nil {
		return adaptiveErr(c, err)
	}
	s.recordAdminAudit(c, "memory.fact.update", "memory", id, "ok", nil)
	return c.JSON(fiber.Map{"fact": f})
}

func (s *Server) handleAdaptiveMemoryDelete(c *fiber.Ctx) error {
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	id := strings.Clone(c.Params("id"))
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	if err := engine.Delete(ctx, scope, id); err != nil {
		return adaptiveErr(c, err)
	}
	s.recordAdminAudit(c, "memory.fact.delete", "memory", id, "ok", nil)
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) handleAdaptiveMemoryPurge(c *fiber.Ctx) error {
	if !isTruthy(c.Query("confirm")) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Purge requires confirm=true"})
	}
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	n, err := engine.Purge(ctx, scope)
	if err != nil {
		return adaptiveErr(c, err)
	}
	s.recordAdminAudit(c, "memory.purge", "memory", scope.Owner, "ok", map[string]any{"agent_id": scope.AgentID, "removed": n})
	return c.JSON(fiber.Map{"ok": true, "removed": n})
}

func (s *Server) handleAdaptiveMemoryExport(c *fiber.Ctx) error {
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	facts, err := engine.List(ctx, scope, "", 2000)
	if err != nil {
		return adaptiveErr(c, err)
	}
	if facts == nil {
		facts = []memory.Fact{}
	}
	name := "soulacy-memory"
	if scope.AgentID != "" {
		name += "-" + scope.AgentID
	}
	c.Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(fiber.Map{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"provider":    engine.Provider(),
		"owner":       scope.Owner,
		"workspace":   scope.Workspace,
		"agent_id":    scope.AgentID,
		"facts":       facts,
	})
}

// maskSecret hides a configured secret from config views while still telling
// the client one is set.
func maskSecret(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return "***"
}
