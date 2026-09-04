package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/secrets"
)

// handleListSecrets returns the secret catalog (names, categories, env
// fallbacks, and whether each slot is set) for the gateway-global secrets
// store. Secret VALUES are never included in the response.
//
// GET /api/v1/secrets → {"secrets":[{"name","category","env_var","description","set"}]}
func (s *Server) handleListSecrets(c *fiber.Ctx) error {
	mgr := secrets.New(s.CredentialVault())
	catalog := mgr.Catalog(c.Context(), s.cfg)
	if catalog == nil {
		catalog = []secrets.Descriptor{}
	}
	return c.JSON(fiber.Map{"secrets": catalog})
}

// handleSetSecret stores a secret value in the global vault.
//
// PUT /api/v1/secrets/:name  body {"value":"..."} → 204 No Content.
// Returns 503 when the vault is nil/disabled, 400 for an empty value.
func (s *Server) handleSetSecret(c *fiber.Ctx) error {
	mgr := secrets.New(s.CredentialVault())
	if !mgr.Enabled() {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "secrets vault not configured")
	}
	name := c.Params("name")
	var body struct {
		Value string `json:"value"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	if body.Value == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "value is required")
	}
	if err := mgr.Set(c.Context(), name, body.Value); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.recordAdminAudit(c, "secret.set", "secret", name, "ok", nil)
	return c.SendStatus(fiber.StatusNoContent)
}

// handleDeleteSecret removes a secret from the global vault.
//
// DELETE /api/v1/secrets/:name → 204 No Content.
func (s *Server) handleDeleteSecret(c *fiber.Ctx) error {
	mgr := secrets.New(s.CredentialVault())
	if !mgr.Enabled() {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "secrets vault not configured")
	}
	name := c.Params("name")
	if err := mgr.Delete(c.Context(), name); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	s.recordAdminAudit(c, "secret.delete", "secret", name, "ok", nil)
	return c.SendStatus(fiber.StatusNoContent)
}
