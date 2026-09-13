package gateway

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
)

func (s *Server) registerLearningNotebookRoutes(api fiber.Router) {
	api = api.Group("/agents/:id/learning", s.rbacAgentFromMW(rbac.ResourceAgents, rbac.ActionRead, rbac.AgentIDSource{PathParam: "id"}))
	read := s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "id"})
	write := s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionWrite, rbac.AgentIDSource{PathParam: "id"})
	api.Get("/lessons", read, s.handleLearningNotebook)
	api.Get("/lessons/:lesson", read, s.handleLearningNotebook)
	api.Post("/teach", write, s.handleLearningNotebook)
	api.Post("/lessons/:lesson/review", write, s.handleLearningNotebook)
	api.Post("/lessons/:lesson/feedback", write, s.handleLearningNotebook)
	api.Get("/lessons/:lesson/export", read, s.handleLearningNotebook)
}

func (s *Server) handleLearningNotebook(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	n := s.engine.LearningNotebook()
	if n == nil || (s.authEngine == nil && s.cfg.Server.APIKey == "") || (s.authEngine != nil && !s.authEngine.Effective()) {
		return s.errMsg(c, 503, "Learning requires authenticated, durable storage")
	}
	if c.Get("Authorization") == "" && c.Cookies("soulacy_access") == "" {
		return s.errMsg(c, 403, "Private learning does not accept credentials in URLs")
	}
	agentID := strings.Clone(c.Params("id"))
	def := s.loader.Get(agentID)
	if def == nil {
		return s.errMsg(c, 404, "Agent not found")
	}
	// The legacy optional RBAC middleware is a no-op without a manager. New
	// private learning routes still enforce credential scopes and base roles.
	action := rbac.ActionRead
	if c.Method() == "POST" {
		action = rbac.ActionWrite
	}
	claims := auth.ClaimsFromCtx(c)
	if claims == nil || !claims.Allows(rbac.ResourceMemory, action) || !claims.Allows(rbac.ResourceAgents, rbac.ActionRead) ||
		(s.rbacManager == nil && (!rbac.HasPermission(claims.Role, rbac.ResourceMemory, action) || !rbac.HasPermission(claims.Role, rbac.ResourceAgents, rbac.ActionRead))) {
		return s.errMsg(c, 403, "Agent and memory permission is required")
	}
	principal, ok := requestPrincipal(c)
	if !ok || strings.TrimSpace(principal.Subject) == "" {
		return s.errMsg(c, 403, "Learning requires a stable authenticated identity")
	}
	scope := learning.Scope{Owner: principal.Subject, AgentID: agentID}
	ctx, cancel := context.WithTimeout(c.UserContext(), s.httpRequestTimeout())
	defer cancel()
	var result any
	var err error
	id := strings.Clone(c.Params("lesson"))
	switch {
	case c.Method() == "GET" && id == "":
		status := c.Query("status", "active")
		if status != "" && status != "active" && status != "pending" && status != "archived" && status != "superseded" && status != "rejected" {
			return s.errMsg(c, 400, "Invalid lesson status")
		}
		var lessons []learning.Lesson
		lessons, err = n.List(ctx, scope, status)
		offset, parseErr := strconv.Atoi(c.Query("offset", "0"))
		if parseErr != nil || offset < 0 || offset > 2000 {
			return s.errMsg(c, 400, "Invalid page offset")
		}
		total := len(lessons)
		lessons = lessons[min(offset, total):min(offset+100, total)]
		result = fiber.Map{"lessons": lessons, "enabled": def.Learning.Enabled, "auto_propose": def.Learning.AutoPropose, "total": total, "offset": offset}
	case c.Method() == "GET":
		var l learning.Lesson
		l, err = n.Get(ctx, scope, id)
		result = l
		if err == nil && strings.HasSuffix(c.Path(), "/export") {
			if l.Kind != "skill" || l.Status != "active" {
				err = learning.ErrLessonConflict
				break
			}
			c.Set("Content-Disposition", `attachment; filename="`+l.Key+`-SKILL.md"`)
			c.Set("Content-Type", "text/plain; charset=utf-8")
			return c.SendString(learning.ExportSkill(l))
		}
	case id == "":
		var body struct {
			Note string `json:"note"`
		}
		if err = learning.DecodeLessonRequest(c.Body(), &body); err != nil {
			break
		}
		ctx = runtime.WithPrincipal(ctx, principal)
		var l *learning.Lesson
		var created bool
		l, created, err = s.engine.TeachLearning(ctx, scope, body.Note)
		result = fiber.Map{"lesson": l, "created": created}
	case strings.HasSuffix(c.Path(), "/feedback"):
		var body struct {
			Rating int `json:"rating"`
		}
		if err = learning.DecodeLessonRequest(c.Body(), &body); err == nil {
			result, err = n.Feedback(ctx, scope, id, body.Rating)
		}
	default:
		var body struct {
			Action    string `json:"action"`
			Confirmed bool   `json:"confirmed"`
		}
		if err = learning.DecodeLessonRequest(c.Body(), &body); err != nil {
			break
		}
		if !body.Confirmed {
			err = learning.ErrInvalidLesson
			break
		}
		result, err = n.Review(ctx, scope, id, body.Action)
	}
	if err != nil {
		switch {
		case errors.Is(err, learning.ErrInvalidLesson):
			return s.errMsg(c, 400, err.Error())
		case errors.Is(err, learning.ErrLessonNotFound):
			return s.errMsg(c, 404, "Lesson not found")
		case errors.Is(err, learning.ErrLessonConflict), errors.Is(err, learning.ErrNotebookFull):
			return s.errMsg(c, 409, err.Error())
		default:
			return s.errMsg(c, 503, "Learning is unavailable. No successful update was confirmed; refresh before retrying.")
		}
	}
	return c.JSON(result)
}
