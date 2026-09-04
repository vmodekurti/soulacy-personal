package credentials

import (
	"encoding/base64"
	"errors"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// API exposes Fiber REST handlers for the credential vault.
// Routes are registered by the gateway server (not here) to avoid import cycles.
type API struct {
	vault Vault
	log   *zap.Logger
}

// NewAPI creates an API wrapping the given vault.
func NewAPI(v Vault, log *zap.Logger) *API {
	return &API{vault: v, log: log}
}

// VaultProvider is a narrow interface satisfied by the gateway Server.
// It lets the lazy API resolve the Vault at request time without importing gateway.
type VaultProvider interface {
	CredentialVault() Vault
}

// LazyAPI is an API that resolves its Vault from a VaultProvider on each request.
// This is used when the vault is wired into the server after buildApp() runs.
type LazyAPI struct {
	provider VaultProvider
	log      *zap.Logger
}

// NewLazyAPI creates a LazyAPI backed by provider.
func NewLazyAPI(provider VaultProvider, log *zap.Logger) *LazyAPI {
	return &LazyAPI{provider: provider, log: log}
}

func (a *LazyAPI) api(c *fiber.Ctx) (*API, bool) {
	v := a.provider.CredentialVault()
	if v == nil {
		_ = c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "credential vault not configured"})
		return nil, false
	}
	return &API{vault: v, log: a.log}, true
}

func (a *LazyAPI) HandleSet(c *fiber.Ctx) error {
	api, ok := a.api(c)
	if !ok {
		return nil
	}
	return api.HandleSet(c)
}

func (a *LazyAPI) HandleList(c *fiber.Ctx) error {
	api, ok := a.api(c)
	if !ok {
		return nil
	}
	return api.HandleList(c)
}

func (a *LazyAPI) HandleGet(c *fiber.Ctx) error {
	api, ok := a.api(c)
	if !ok {
		return nil
	}
	return api.HandleGet(c)
}

func (a *LazyAPI) HandleDelete(c *fiber.Ctx) error {
	api, ok := a.api(c)
	if !ok {
		return nil
	}
	return api.HandleDelete(c)
}

// setRequest is the request body for HandleSet.
type setRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"` // base64-encoded
}

// HandleSet handles POST /credentials/:agentID
// Body: {"key":"foo","value":"base64-encoded"}
func (a *API) HandleSet(c *fiber.Ctx) error {
	agentID := c.Params("agentID")
	if agentID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agentID is required"})
	}

	var req setRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if req.Key == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "key is required"})
	}

	value, err := base64.StdEncoding.DecodeString(req.Value)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "value must be base64-encoded"})
	}

	if err := a.vault.Set(c.Context(), agentID, req.Key, value); err != nil {
		a.log.Error("credential vault set failed",
			zap.String("agent_id", agentID),
			zap.String("key", req.Key),
			zap.Error(err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to store credential"})
	}

	return c.Status(fiber.StatusNoContent).Send(nil)
}

// HandleList handles GET /credentials/:agentID
// Returns: {"keys": ["foo","bar"]}
func (a *API) HandleList(c *fiber.Ctx) error {
	agentID := c.Params("agentID")
	if agentID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agentID is required"})
	}

	keys, err := a.vault.List(c.Context(), agentID)
	if err != nil {
		a.log.Error("credential vault list failed",
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to list credentials"})
	}

	// Return an empty array rather than null when there are no keys.
	if keys == nil {
		keys = []string{}
	}
	return c.JSON(fiber.Map{"keys": keys})
}

// HandleGet handles GET /credentials/:agentID/:key
// Returns: {"value":"base64-encoded"}
func (a *API) HandleGet(c *fiber.Ctx) error {
	agentID := c.Params("agentID")
	key := c.Params("key")
	if agentID == "" || key == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agentID and key are required"})
	}

	value, err := a.vault.Get(c.Context(), agentID, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "credential not found"})
		}
		a.log.Error("credential vault get failed",
			zap.String("agent_id", agentID),
			zap.String("key", key),
			zap.Error(err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to retrieve credential"})
	}

	return c.JSON(fiber.Map{"value": base64.StdEncoding.EncodeToString(value)})
}

// HandleDelete handles DELETE /credentials/:agentID/:key
func (a *API) HandleDelete(c *fiber.Ctx) error {
	agentID := c.Params("agentID")
	key := c.Params("key")
	if agentID == "" || key == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "agentID and key are required"})
	}

	if err := a.vault.Delete(c.Context(), agentID, key); err != nil {
		a.log.Error("credential vault delete failed",
			zap.String("agent_id", agentID),
			zap.String("key", key),
			zap.Error(err),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to delete credential"})
	}

	return c.Status(fiber.StatusNoContent).Send(nil)
}
