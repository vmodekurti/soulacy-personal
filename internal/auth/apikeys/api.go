package apikeys

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/requestctx"
	"go.uber.org/zap"
)

// auditLocal carries non-secret credential facts from a handler to the route
// wrapper that writes the admin audit record. The plaintext secret is never
// placed here: only the credential identity, its bindings, and its authority.
const auditLocal = "apikeys_audit"

// AuditSubject returns the credential ID and redaction-safe details recorded by
// the most recent credential handler on this request, if any. Callers use it to
// attribute an audit entry to the specific personal token or service account
// rather than to a generic "API user".
func AuditSubject(c *fiber.Ctx) (string, map[string]any) {
	details, ok := c.Locals(auditLocal).(map[string]any)
	if !ok || len(details) == 0 {
		return "", nil
	}
	id, _ := details["credential_id"].(string)
	return id, details
}

func recordCredentialAudit(c *fiber.Ctx, key APIKey) {
	details := map[string]any{
		"credential_id":   key.ID,
		"credential_kind": key.Kind,
		"credential_name": key.Name,
		"prefix":          key.Prefix,
		"subject_id":      key.SubjectID,
		"organization_id": key.OrganizationID,
		"workspace_ids":   append([]string(nil), key.WorkspaceIDs...),
		"role":            key.Role,
		"scopes":          append([]string(nil), key.Scopes...),
		"issuer":          key.Issuer,
		"status":          key.Status,
	}
	if key.Kind == KindService {
		details["service_account_id"] = key.SubjectID
	}
	if key.ExpiresAt != nil {
		details["expires_at"] = key.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(key.RotatedFromID) != "" {
		details["rotated_from_id"] = key.RotatedFromID
	}
	c.Locals(auditLocal, details)
}

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
	var body CreateRequest
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

	identity, hasIdentity := requestctx.From(c.UserContext())
	if body.Kind == "" {
		body.Kind = KindPersonal
	}
	body.Issuer = "soulacy"
	if hasIdentity {
		body.ActorSubject = identity.Subject()
		if len(body.Scopes) == 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "at least one resource:action scope is required"})
		}
		if body.ExpiresAt == nil {
			expires := time.Now().UTC().Add(90 * 24 * time.Hour)
			body.ExpiresAt = &expires
		}
		if body.Kind == KindPersonal {
			body.SubjectID = identity.Subject()
			body.Role = identity.Role()
		} else if body.Kind == KindService {
			if identity.Role() != "owner" && identity.Role() != "admin" {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "only workspace owners and admins may issue service-account credentials"})
			}
			if !canDelegateRole(identity.Role(), body.Role) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "service-account role exceeds the caller's authority"})
			}
		}
		if body.OrganizationID == "" {
			body.OrganizationID = identity.OrganizationID()
		}
		if len(body.WorkspaceIDs) == 0 {
			body.WorkspaceIDs = []string{identity.WorkspaceID()}
		}
		if _, local := a.store.(*SQLiteStore); local {
			for _, workspaceID := range body.WorkspaceIDs {
				if strings.TrimSpace(workspaceID) != identity.WorkspaceID() {
					return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "local credentials may only bind the active workspace"})
				}
			}
		}
	}
	var plaintext string
	var key APIKey
	var err error
	if scoped, ok := a.store.(ScopedStore); ok && (hasIdentity || body.SubjectID != "" || body.OrganizationID != "" || len(body.WorkspaceIDs) > 0) {
		plaintext, key, err = scoped.CreateScoped(c.UserContext(), body)
	} else {
		plaintext, key, err = a.store.Create(c.UserContext(), body.Name, body.Scopes)
	}
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name, credential kind, subject, organization, workspace, role, issuer, and a future expiry are required"})
		}
		a.log.Error("apikeys: create failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to create API key",
		})
	}

	recordCredentialAudit(c, key)
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":         key.ID,
		"name":       key.Name,
		"prefix":     key.Prefix,
		"key":        plaintext,
		"scopes":     key.Scopes,
		"created_at": key.CreatedAt,
		"kind":       key.Kind, "subject_id": key.SubjectID,
		"organization_id": key.OrganizationID, "workspace_ids": key.WorkspaceIDs,
		"role": key.Role, "issuer": key.Issuer, "status": key.Status,
		"expires_at": key.ExpiresAt,
	})
}

func canDelegateRole(actor, target string) bool {
	rank := map[string]int{"viewer": 1, "operator": 2, "developer": 3, "admin": 4, "owner": 5}
	actorRank, actorOK := rank[strings.ToLower(strings.TrimSpace(actor))]
	targetRank, targetOK := rank[strings.ToLower(strings.TrimSpace(target))]
	return actorOK && targetOK && targetRank <= actorRank
}

// HandleRotate revokes the old credential and returns a single-use replacement
// secret with identical bindings and scopes.
func (a *API) HandleRotate(c *fiber.Ctx) error {
	scoped, ok := a.store.(ScopedStore)
	if !ok {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "credential rotation is not supported by this store"})
	}
	if !a.mayManage(c, c.Params("id")) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "credential not found"})
	}
	plaintext, key, err := scoped.Rotate(c.UserContext(), c.Params("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "credential not found"})
		}
		if errors.Is(err, ErrInvalidKey) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "only active, unexpired credentials can be rotated"})
		}
		a.log.Error("apikeys: rotate failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to rotate credential"})
	}
	recordCredentialAudit(c, key)
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"key": plaintext, "credential": key})
}

// HandleStatus supports immediate suspension and deletion in addition to
// revocation. Reactivation remains explicit and audited by the caller.
func (a *API) HandleStatus(c *fiber.Ctx) error {
	scoped, ok := a.store.(ScopedStore)
	if !ok {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "credential status changes are not supported by this store"})
	}
	if !a.mayManage(c, c.Params("id")) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "credential not found"})
	}
	var body struct {
		Status    string     `json:"status"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if err := scoped.SetStatus(c.UserContext(), c.Params("id"), body.Status); err != nil {
		if errors.Is(err, ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "credential not found"})
		}
		if errors.Is(err, ErrInvalidRequest) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "status must be active, revoked, suspended, or deleted"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to update credential"})
	}
	c.Locals(auditLocal, map[string]any{"credential_id": c.Params("id"), "status": strings.ToLower(strings.TrimSpace(body.Status))})
	return c.JSON(fiber.Map{"id": c.Params("id"), "status": strings.ToLower(strings.TrimSpace(body.Status))})
}

// HandleList returns all API keys.
// Query param: ?include_revoked=true to include revoked keys.
func (a *API) HandleList(c *fiber.Ctx) error {
	includeRevoked := c.Query("include_revoked") == "true"

	keys, err := a.visibleKeys(c, includeRevoked)
	if err != nil {
		a.log.Error("apikeys: list failed", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "failed to list API keys",
		})
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
	if !a.mayManage(c, id) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "API key not found"})
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
	if !credentialVisibleTo(c, key) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "credential not found"})
	}

	return c.Status(fiber.StatusOK).JSON(key)
}

func credentialHasWorkspace(key APIKey, workspaceID string) bool {
	for _, bound := range key.WorkspaceIDs {
		if strings.TrimSpace(bound) == strings.TrimSpace(workspaceID) {
			return true
		}
	}
	return false
}

func credentialVisibleTo(c *fiber.Ctx, key APIKey) bool {
	identity, ok := requestctx.From(c.UserContext())
	if !ok {
		return true
	}
	return key.OrganizationID == identity.OrganizationID() && credentialHasWorkspace(key, identity.WorkspaceID())
}

// visibleKeys returns exactly the credentials the caller's verified workspace
// context authorizes. When the store can scope the query it does so in the
// store; the post-filter remains as a second, independent barrier so a store
// that ignores the scope still cannot leak another tenant's records.
func (a *API) visibleKeys(c *fiber.Ctx, includeRevoked bool) ([]APIKey, error) {
	identity, constrained := requestctx.From(c.UserContext())
	if !constrained {
		keys, err := a.store.List(c.UserContext(), includeRevoked)
		if err != nil {
			return nil, err
		}
		if keys == nil {
			keys = []APIKey{}
		}
		return keys, nil
	}
	var keys []APIKey
	var err error
	if scoped, ok := a.store.(ScopedLister); ok {
		keys, err = scoped.ListForWorkspace(c.UserContext(), identity.OrganizationID(), identity.WorkspaceID(), includeRevoked)
	} else {
		keys, err = a.store.List(c.UserContext(), includeRevoked)
	}
	if err != nil {
		return nil, err
	}
	filtered := make([]APIKey, 0, len(keys))
	for _, key := range keys {
		if key.OrganizationID == identity.OrganizationID() && credentialHasWorkspace(key, identity.WorkspaceID()) {
			filtered = append(filtered, key)
		}
	}
	return filtered, nil
}

func (a *API) mayManage(c *fiber.Ctx, id string) bool {
	if _, constrained := requestctx.From(c.UserContext()); !constrained {
		return true
	}
	keys, err := a.visibleKeys(c, true)
	if err != nil {
		a.log.Error("apikeys: authority lookup failed", zap.Error(err))
		return false
	}
	for _, key := range keys {
		if key.ID == id {
			return true
		}
	}
	return false
}
