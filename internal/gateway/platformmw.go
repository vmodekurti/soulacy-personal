package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/config"
)

// platformMW authorizes a deployment-wide endpoint.
//
// Personal mode delegates to the ordinary RBAC middleware and behaves exactly
// as it always has — invariant 7. There is one tenant there and they own the
// machine, so "may this member change the config" and "may this operator
// change the config" are the same question.
//
// Team and Scale require the deployment's own credential. Not a role: see
// platformroutes.go for why a `superadmin` membership role would be the wrong
// shape. A workspace `owner` reaching one of these gets a refusal that says
// which credential opens it, because the alternative — a bare 403 — reads as a
// bug in the product rather than a boundary.
func (s *Server) platformMW(resource, action string) fiber.Handler {
	workspaceRBAC := s.rbacMW(resource, action)
	return func(c *fiber.Ctx) error {
		if !config.IsMultiUserMode(s.config().DeploymentMode()) {
			return workspaceRBAC(c)
		}
		if !platformPrincipal(c) {
			return s.errMsg(c, fiber.StatusForbidden,
				"this endpoint changes the whole deployment, not your workspace, so workspace roles "+
					"do not grant it. It is opened by the deployment's bootstrap server.api_key, "+
					"presented by whoever operates the host.")
		}
		return c.Next()
	}
}
