package gateway

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/person"
	"github.com/soulacy/soulacy/internal/rbac"
)

// SetPersonModel installs the person model store. Nil leaves the routes
// answering 503, which is what a gateway without it should say.
func (s *Server) SetPersonModel(store person.Store) { s.personModel = store }

// registerPersonRoutes mounts the person model API.
//
//	GET    /person/model                      the whole model: prose + entries
//	PUT    /person/model/entries              add or correct one entry
//	DELETE /person/model/entries/:section/:key
//	GET    /person/model/sync?since=&limit=   change feed for devices
//	DELETE /person/model?confirm=true         forget everything
//
// Scoped to the caller's own identity, like adaptive memory: a household
// shares one gateway and must not share one model. Admins may inspect
// another member with ?owner=, which the audit log records.
func (s *Server) registerPersonRoutes(api fiber.Router) {
	read := s.rbacMW(rbac.ResourceMemory, rbac.ActionRead)
	write := s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite)
	del := s.rbacMW(rbac.ResourceMemory, rbac.ActionDelete)
	api.Get("/person/model/sync", read, s.handlePersonSync)
	api.Get("/person/model", read, s.handlePersonModel)
	api.Put("/person/model/entries", write, s.handlePersonPut)
	api.Delete("/person/model/entries/:section/:key", del, s.handlePersonDelete)
	api.Delete("/person/model", del, s.handlePersonPurge)
}

// personOwnerFor resolves whose model this request is about and enforces the
// boundary between household members.
func (s *Server) personOwnerFor(c *fiber.Ctx) (person.Store, string, error) {
	if s.personModel == nil {
		return nil, "", fiber.NewError(fiber.StatusServiceUnavailable, "The person model is not enabled on this gateway")
	}
	if c.Get("Authorization") == "" && c.Cookies("soulacy_access") == "" {
		return nil, "", fiber.NewError(fiber.StatusForbidden, "The person model does not accept credentials in URLs")
	}
	principal, ok := requestPrincipal(c)
	if !ok || strings.TrimSpace(principal.Subject) == "" {
		return nil, "", fiber.NewError(fiber.StatusForbidden, "The person model requires a stable authenticated identity")
	}
	owner := principal.Subject
	if requested := strings.TrimSpace(c.Query("owner")); requested != "" && requested != owner {
		claims := auth.ClaimsFromCtx(c)
		if claims == nil || !strings.EqualFold(claims.Role, rbac.RoleAdmin) {
			return nil, "", fiber.NewError(fiber.StatusForbidden, "Only an admin can read another member's person model")
		}
		owner = requested
	}
	return s.personModel, owner, nil
}

func (s *Server) handlePersonModel(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	store, owner, err := s.personOwnerFor(c)
	if err != nil {
		return personErr(c, err)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	entries, err := store.List(ctx, owner, person.Query{IncludeExpired: c.QueryBool("include_expired", false)})
	if err != nil {
		return personErr(c, err)
	}
	now := time.Now().UTC()
	model := person.NewModel(owner, entries, now)
	return c.JSON(fiber.Map{
		"owner":    owner,
		"sections": model.Sections,
		"entries":  entries,
		// The same prose an agent sees, so the app and the web can show
		// exactly what Soulacy believes rather than a prettier paraphrase.
		"summary":    person.Render(model, now),
		"updated_at": model.UpdatedAt,
	})
}

func (s *Server) handlePersonPut(c *fiber.Ctx) error {
	store, owner, err := s.personOwnerFor(c)
	if err != nil {
		return personErr(c, err)
	}
	var body struct {
		Section    string         `json:"section"`
		Key        string         `json:"key"`
		Summary    string         `json:"summary"`
		Value      map[string]any `json:"value"`
		Confidence float32        `json:"confidence"`
		ExpiresAt  *time.Time     `json:"expires_at"`
		// Source is accepted so an observer running as this user can say which
		// sense it is; anything a person types through the UI is manual.
		Source string `json:"source"`
	}
	if err := c.BodyParser(&body); err != nil {
		return personErr(c, fiber.NewError(fiber.StatusBadRequest, "invalid JSON body"))
	}
	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = person.SourceManual
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	result, err := store.Put(ctx, person.Entry{
		Owner: owner, Section: person.Section(strings.TrimSpace(body.Section)), Key: body.Key,
		Summary: body.Summary, Value: body.Value, Source: source,
		Confidence: body.Confidence, ExpiresAt: body.ExpiresAt,
	})
	if err != nil {
		return personErr(c, err)
	}
	status := fiber.StatusOK
	if !result.Applied {
		// Not an error: being overruled by a higher source is normal, and the
		// caller needs to see what stands.
		status = fiber.StatusConflict
	}
	return c.Status(status).JSON(fiber.Map{"entry": result.Entry, "applied": result.Applied, "reason": result.Reason})
}

func (s *Server) handlePersonDelete(c *fiber.Ctx) error {
	store, owner, err := s.personOwnerFor(c)
	if err != nil {
		return personErr(c, err)
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	section := person.Section(strings.TrimSpace(c.Params("section")))
	if err := store.Delete(ctx, owner, section, c.Params("key")); err != nil {
		return personErr(c, err)
	}
	return c.JSON(fiber.Map{"deleted": true})
}

func (s *Server) handlePersonPurge(c *fiber.Ctx) error {
	store, owner, err := s.personOwnerFor(c)
	if err != nil {
		return personErr(c, err)
	}
	if !c.QueryBool("confirm", false) {
		return personErr(c, fiber.NewError(fiber.StatusBadRequest, "Pass confirm=true to forget everything Soulacy understands about you"))
	}
	var sections []person.Section
	for _, name := range strings.Split(c.Query("sections"), ",") {
		if section := person.Section(strings.TrimSpace(name)); section.Valid() {
			sections = append(sections, section)
		}
	}
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	removed, err := store.Purge(ctx, owner, sections...)
	if err != nil {
		return personErr(c, err)
	}
	return c.JSON(fiber.Map{"removed": removed})
}

func (s *Server) handlePersonSync(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	store, owner, err := s.personOwnerFor(c)
	if err != nil {
		return personErr(c, err)
	}
	since, _ := strconv.ParseInt(c.Query("since", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit", "200"))
	ctx, cancel := s.adaptiveCtx(c)
	defer cancel()
	page, err := store.Changes(ctx, owner, since, limit)
	if err != nil {
		return personErr(c, err)
	}
	return c.JSON(fiber.Map{
		"changes": page.Changes, "next_cursor": page.NextCursor,
		"has_more": page.HasMore, "reset": page.Reset, "owner": owner,
	})
}

func personErr(c *fiber.Ctx, err error) error {
	var fe *fiber.Error
	switch {
	case errors.As(err, &fe):
		return c.Status(fe.Code).JSON(fiber.Map{"error": fe.Message})
	case errors.Is(err, person.ErrNotFound):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "No such entry"})
	case errors.Is(err, person.ErrInvalidOwner):
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "The person model requires a stable authenticated identity"})
	case errors.Is(err, person.ErrInvalidEntry):
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	default:
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
}
