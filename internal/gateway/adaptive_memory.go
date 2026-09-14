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
//	PATCH  /memory/facts/:id                           edit content/category/expiry
//	DELETE /memory/facts/:id                           delete one fact
//	GET    /memory/facts/:id/history                   change log of one fact
//	GET    /memory/facts/relations?agent_id=           entity relations
//	DELETE /memory/facts/relations/:id                 delete one relation
//	GET    /memory/facts/export?agent_id=              JSON download
//	DELETE /memory/facts?agent_id=&confirm=true        purge
//	GET    /memory/facts/status                        provider, categories, counts
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
	api.Get("/memory/facts/relations", read, s.handleAdaptiveMemoryRelations)
	api.Delete("/memory/facts/relations/:id", del, s.handleAdaptiveMemoryDeleteRelation)
	api.Get("/memory/facts", read, s.handleAdaptiveMemoryList)
	api.Post("/memory/facts", write, s.handleAdaptiveMemoryAdd)
	api.Get("/memory/facts/:id/history", read, s.handleAdaptiveMemoryHistory)
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
	scope := memory.FactScope{Workspace: s.adaptiveWorkspace(), Owner: owner, AgentID: strings.TrimSpace(c.Query("agent_id")), SessionID: strings.TrimSpace(c.Query("session_id"))}.Normalize()
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "A fact is one short sentence (1-400 characters) with an allowed category; expiry must be a date (YYYY-MM-DD) or RFC 3339 timestamp"})
	case errors.Is(err, memory.ErrInvalidScope):
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Memory requires a stable authenticated identity"})
	case errors.Is(err, memory.ErrUnsupported):
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "The active memory provider does not support this operation"})
	case errors.Is(err, memory.ErrProviderFailed):
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "The memory provider did not respond: " + err.Error()})
	}
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
}

// parseExpiry accepts "", a date, or an RFC 3339 timestamp. An empty value
// means "no expiry".
func parseExpiry(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			if layout == "2006-01-02" {
				t = t.Add(24*time.Hour - time.Second) // end of that day
			}
			t = t.UTC()
			return &t, nil
		}
	}
	return nil, memory.ErrInvalidFact
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
		"graph_enabled":       opts.GraphEnabled,
		"categories":          []string{},
	}
	if engine == nil {
		return c.JSON(out)
	}
	out["provider"] = engine.Provider()
	out["categories"] = engine.Categories()
	_, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return c.JSON(out)
	}
	out["owner"] = scope.Owner
	if local, ok := engine.(*memory.LocalAdaptive); ok && local.Store() != nil {
		ctx, cancel := s.adaptiveCtx(c)
		defer cancel()
		active, superseded, retracted, cerr := local.Store().Count(ctx, scope)
		if cerr == nil {
			out["active"] = active
			out["superseded"] = superseded
			out["retracted"] = retracted
		}
		if rels, rerr := local.Store().Relations(ctx, scope, memory.FactStatusActive, 2000); rerr == nil {
			out["relations"] = len(rels)
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
	if status != "" && !memory.ValidFactStatus(status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "status must be active, superseded or retracted"})
	}
	limit, _ := strconv.Atoi(c.Query("limit", "200"))
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		hits, err := engine.Recall(ctx, scope, q, max(1, min(limit, 50)))
		if err != nil {
			return adaptiveErr(c, err)
		}
		if hits == nil {
			hits = []memory.ScoredFact{}
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

func (s *Server) handleAdaptiveMemoryRelations(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	limit, _ := strconv.Atoi(c.Query("limit", "200"))
	var rels []memory.Relation
	if local, ok := engine.(*memory.LocalAdaptive); ok && strings.TrimSpace(c.Query("q")) == "" {
		status := c.Query("status", memory.FactStatusActive)
		if status == "all" {
			status = ""
		}
		rels, err = local.Store().Relations(ctx, scope, status, limit)
	} else {
		rels, err = engine.Relations(ctx, scope, c.Query("q"), max(1, min(limit, 50)))
	}
	if err != nil {
		return adaptiveErr(c, err)
	}
	if rels == nil {
		rels = []memory.Relation{}
	}
	return c.JSON(fiber.Map{"relations": rels, "provider": engine.Provider(), "owner": scope.Owner})
}

func (s *Server) handleAdaptiveMemoryDeleteRelation(c *fiber.Ctx) error {
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	local, ok := engine.(*memory.LocalAdaptive)
	if !ok {
		return adaptiveErr(c, memory.ErrUnsupported)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	if err := local.Store().DeleteRelation(ctx, scope, strings.Clone(c.Params("id"))); err != nil {
		return adaptiveErr(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

type adaptiveFactBody struct {
	AgentID   string `json:"agent_id"`
	Content   string `json:"content"`
	Category  string `json:"category"`
	ExpiresAt string `json:"expires_at"`
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
	expires, err := parseExpiry(body.ExpiresAt)
	if err != nil {
		return adaptiveErr(c, err)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	f, err := engine.Add(ctx, scope, memory.FactInput{Category: memory.FactCategory(strings.ToLower(body.Category)), Content: body.Content, ExpiresAt: expires})
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
	// Keep the current category and expiry when the client only edits text.
	var current *memory.Fact
	if cur, gerr := engine.List(ctx, scope, "", 2000); gerr == nil {
		for i := range cur {
			if cur[i].ID == id {
				current = &cur[i]
				break
			}
		}
	}
	if body.Category == "" {
		if current != nil {
			body.Category = string(current.Category)
		} else {
			body.Category = string(memory.FactPreference)
		}
	}
	var expires *time.Time
	if body.ExpiresAt == "" && current != nil {
		expires = current.ExpiresAt
	} else if body.ExpiresAt != "" && body.ExpiresAt != "none" {
		if expires, err = parseExpiry(body.ExpiresAt); err != nil {
			return adaptiveErr(c, err)
		}
	}
	f, err := engine.Update(ctx, scope, id, memory.FactInput{Category: memory.FactCategory(strings.ToLower(body.Category)), Content: body.Content, ExpiresAt: expires})
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

func (s *Server) handleAdaptiveMemoryHistory(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	engine, scope, err := s.adaptiveScopeFor(c)
	if err != nil {
		return adaptiveErr(c, err)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	events, err := engine.History(ctx, scope, strings.Clone(c.Params("id")))
	if err != nil {
		return adaptiveErr(c, err)
	}
	return c.JSON(fiber.Map{"events": events})
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
	rels := []memory.Relation{}
	if local, ok := engine.(*memory.LocalAdaptive); ok {
		if r, rerr := local.Store().Relations(ctx, scope, "", 2000); rerr == nil && r != nil {
			rels = r
		}
	} else if r, rerr := engine.Relations(ctx, scope, "", 50); rerr == nil && r != nil {
		rels = r
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
		"categories":  engine.Categories(),
		"facts":       facts,
		"relations":   rels,
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
