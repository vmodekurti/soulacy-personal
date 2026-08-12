package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/secrets"
)

// denyGlobalCredentialScope prevents the per-agent credential API from being
// used as an alternate route to gateway-global provider, channel, or server
// secrets. Those values are exclusively managed through /secrets.
func (s *Server) denyGlobalCredentialScope(c *fiber.Ctx) error {
	if c.Params("agentID") == secrets.GlobalScope {
		return s.errMsg(c, fiber.StatusNotFound, "agent credential scope not found")
	}
	return c.Next()
}

// requireCredentialRevealConfirmation makes plaintext disclosure an explicit
// user act even for an administrator. A future identity provider may satisfy
// this with step-up authentication; today clients send the one-shot header.
func (s *Server) requireCredentialRevealConfirmation(c *fiber.Ctx) error {
	if !isTruthy(c.Get("X-Soulacy-Confirm-Credential-Reveal")) {
		return c.Status(fiber.StatusPreconditionRequired).JSON(fiber.Map{
			"error":               "credential reveal requires explicit confirmation",
			"confirmation_header": "X-Soulacy-Confirm-Credential-Reveal: true",
		})
	}
	return c.Next()
}

// credentialAudit records every authenticated change, rotation, deletion, or
// reveal attempt. It never records request/response bodies or secret values.
func (s *Server) credentialAudit(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		err := c.Next()
		status := "ok"
		if err != nil || c.Response().StatusCode() >= 400 {
			status = "denied"
		}
		target := strings.Trim(c.Params("agentID")+"/"+c.Params("key"), "/")
		s.recordAdminAudit(c, action, "credential", target, status, map[string]any{
			"http_status": c.Response().StatusCode(),
		})
		return err
	}
}
