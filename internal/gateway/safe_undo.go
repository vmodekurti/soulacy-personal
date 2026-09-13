package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/safeundo"
)

func (s *Server) SetSafeUndoStore(store *safeundo.Store) { s.undoStore = store }

func (s *Server) registerSafeUndoRoutes(api fiber.Router) {
	read := s.rbacAgentFromMW(rbac.ResourceAgents, rbac.ActionRead, rbac.AgentIDSource{PathParam: "id"})
	write := s.rbacAgentFromMW(rbac.ResourceAgents, rbac.ActionWrite, rbac.AgentIDSource{PathParam: "id"})
	api.Get("/agents/:id/safe-undo/resources", read, s.handleSafeUndo)
	api.Get("/agents/:id/safe-undo/jobs", read, s.handleSafeUndo)
	api.Get("/agents/:id/safe-undo/jobs/:job", read, s.handleSafeUndo)
	api.Post("/agents/:id/safe-undo/jobs", write, s.handleSafeUndo)
	api.Post("/agents/:id/safe-undo/jobs/:job/preview", write, s.handleSafeUndo)
	api.Post("/agents/:id/safe-undo/jobs/:job/execute", write, s.handleSafeUndo)
}

func safeUndoDecode(c *fiber.Ctx, v any) error {
	if len(c.Body()) == 0 || len(c.Body()) > safeundo.MaxJobBytes {
		return safeundo.ErrInvalid
	}
	return safeundo.DecodeRequest(c.Body(), v)
}

func (s *Server) safeUndoAuthority(c *fiber.Ctx, ctx context.Context, owner, agentID, action string) safeundo.Authorize {
	token := strings.Clone(strings.TrimPrefix(c.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.Clone(c.Cookies("soulacy_access"))
	}
	// Deliberately no URL query credentials for this private mutation API.
	return func() error {
		var claims *auth.Claims
		if s.authEngine != nil {
			var err error
			claims, err = s.authEngine.RevalidateCredential(ctx, token)
			if err != nil {
				return safeundo.ErrPermission
			}
		} else {
			if s.cfg.Server.APIKey == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Server.APIKey)) != 1 {
				return safeundo.ErrPermission
			}
			claims = &auth.Claims{Email: "admin", Role: "admin", Kind: "access"}
		}
		currentOwner := strings.TrimSpace(claims.Subject)
		if currentOwner == "" {
			currentOwner = strings.TrimSpace(claims.Email)
		}
		if currentOwner == "" {
			currentOwner = "admin"
		}
		if currentOwner != owner || !claims.Allows(rbac.ResourceAgents, action) || s.loader.Get(agentID) == nil {
			return safeundo.ErrPermission
		}
		if s.rbacManager != nil {
			allowed, err := s.rbacManager.CanAccessAgentResource(claims.Role, agentID, rbac.ResourceAgents, action)
			if err != nil || !allowed {
				return safeundo.ErrPermission
			}
		} else if !rbac.HasPermission(claims.Role, rbac.ResourceAgents, action) {
			return safeundo.ErrPermission
		}
		return ctx.Err()
	}
}

func (s *Server) safeUndoError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, safeundo.ErrInvalid):
		return s.errMsg(c, 400, err.Error())
	case errors.Is(err, safeundo.ErrNotFound):
		return s.errMsg(c, 404, "Safe Undo job not found")
	case errors.Is(err, safeundo.ErrPermission):
		return s.errMsg(c, 403, "Current agent and resource permission is required")
	case errors.Is(err, safeundo.ErrConflict), errors.Is(err, safeundo.ErrUncertain):
		return s.errMsg(c, 409, err.Error())
	case errors.Is(err, safeundo.ErrLimit):
		return s.errMsg(c, 409, "Safe Undo storage or attempt limit reached")
	default:
		return s.errMsg(c, 503, "Safe Undo is unavailable; refresh the receipt before trying again")
	}
}

func (s *Server) handleSafeUndo(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	if s.undoStore == nil || (s.authEngine == nil && s.cfg.Server.APIKey == "") || (s.authEngine != nil && !s.authEngine.Effective()) {
		return s.errMsg(c, 503, "Safe Undo requires authenticated, durable storage")
	}
	agentID := strings.Clone(c.Params("id"))
	_, owner, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), min(s.httpRequestTimeout(), safeundo.MaxOperationDuration))
	defer cancel()
	action := rbac.ActionRead
	if c.Method() == "POST" {
		action = rbac.ActionWrite
	}
	authorize := s.safeUndoAuthority(c, ctx, owner, agentID, action)
	if err := authorize(); err != nil {
		return s.safeUndoError(c, err)
	}
	var result any
	switch {
	case c.Method() == "GET" && c.Params("job") == "" && strings.HasSuffix(c.Path(), "/resources"):
		result = fiber.Map{"resources": s.undoStore.Resources(agentID)}
	case c.Method() == "GET" && c.Params("job") == "":
		var jobs []safeundo.JobSummary
		jobs, err = s.undoStore.List(ctx, owner, agentID)
		result = fiber.Map{"jobs": jobs}
	case c.Method() == "GET":
		result, err = s.undoStore.Get(ctx, owner, agentID, c.Params("job"))
	case c.Params("job") == "":
		var req safeundo.PrepareRequest
		if err = safeUndoDecode(c, &req); err == nil {
			result, err = s.undoStore.Prepare(ctx, owner, agentID, "", req, authorize)
		}
		if err == nil {
			c.Status(201)
		}
	case strings.HasSuffix(c.Path(), "/preview"):
		var req struct {
			Direction string `json:"direction"`
		}
		if err = safeUndoDecode(c, &req); err == nil {
			result, err = s.undoStore.Preview(ctx, owner, agentID, c.Params("job"), req.Direction, authorize)
		}
	default:
		var req struct {
			Token     string `json:"token"`
			Confirmed bool   `json:"confirmed"`
		}
		if err = safeUndoDecode(c, &req); err == nil {
			if !req.Confirmed {
				err = safeundo.ErrInvalid
			} else {
				result, err = s.undoStore.Execute(ctx, owner, agentID, c.Params("job"), req.Token, authorize)
			}
		}
	}
	if err != nil {
		return s.safeUndoError(c, err)
	}
	return c.JSON(result)
}
