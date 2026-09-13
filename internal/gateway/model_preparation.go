package gateway

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/rbac"
)

func (s *Server) handleModelPreparation(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	// Enforce scoped credentials even on installations without optional RBAC.
	if claims := auth.ClaimsFromCtx(c); claims != nil {
		if !claims.Allows(rbac.ResourceAgents, rbac.ActionRead) || (s.rbacManager == nil && !rbac.HasPermission(claims.Role, rbac.ResourceAgents, rbac.ActionRead)) {
			return s.errMsg(c, 403, "Agent read permission is required")
		}
	}
	def := s.loader.Get(strings.Clone(c.Params("id")))
	if def == nil {
		return s.errMsg(c, 404, "Agent not found")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), s.httpRequestTimeout())
	defer cancel()
	preparation, err := s.engine.PrepareAgentModel(ctx, def)
	if err != nil {
		return s.errMsg(c, 422, err.Error())
	}
	return c.JSON(preparation)
}
