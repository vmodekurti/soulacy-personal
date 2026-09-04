package apikeys

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// API exposes HTTP handlers for API key management.
type API struct {
	store Store
	log   *zap.Logger
}

// NewAPI constructs a new API handler with the given store and logger.
func NewAPI(store Store, log *zap.Logger) *API {
	return &API{store: store, log: log}
}

// HandleCreate creates a new API key.
// POST body: {"name":"...","scopes":["read","write"]}
// Returns 201 with the plaintext key (shown once).
func (a *API) HandleCreate(c *fiber.Ctx) error {
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body",
		})
	}
	if strings.TrimSpace(body.Name) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "name is required",
		})
	}

	plaintext, key, err := a.store.Create(c.Context(), body.Name, body.Scopes)
	if err != nil {
		a.log.Error("apikeys: create failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to create API key",
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":         key.ID,
		"name":       key.Name,
		"prefix":     key.Prefix,
		"key":        plaintext,
		"scopes":     key.Scopes,
		"created_at": key.CreatedAt,
	})
}

// HandleList returns all API keys.
// Query param: ?include_revoked=true to include revoked keys.
func (a *API) HandleList(c *fiber.Ctx) error {
	includeRevoked := c.Query("include_revoked") == "true"

	keys, err := a.store.List(c.Context(), includeRevoked)
	if err != nil {
		a.log.Error("apikeys: list failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to list API keys",
		})
	}

	if keys == nil {
		keys = []APIKey{}
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"keys": keys,
	})
}

// HandleRevoke revokes an API key by ID.
// Path param: :id
func (a *API) HandleRevoke(c *fiber.Ctx) error {
	id := c.Params("id")
	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "id is required",
		})
	}

	err := a.store.Revoke(c.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "API key not found",
			})
		}
		a.log.Error("apikeys: revoke failed", zap.String("id", id), zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to revoke API key",
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status": "revoked",
		"id":     id,
	})
}

// HandleValidate validates a plaintext API key.
// Body: {"key":"sk_..."}
// Returns 200 with the key record or 401 on invalid/revoked key.
func (a *API) HandleValidate(c *fiber.Ctx) error {
	var body struct {
		Key string `json:"key"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body",
		})
	}
	if body.Key == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "key is required",
		})
	}

	key, err := a.store.Validate(c.Context(), body.Key)
	if err != nil {
		if errors.Is(err, ErrInvalidKey) {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid or revoked key",
			})
		}
		a.log.Error("apikeys: validate failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to validate API key",
		})
	}

	return c.Status(fiber.StatusOK).JSON(key)
}
