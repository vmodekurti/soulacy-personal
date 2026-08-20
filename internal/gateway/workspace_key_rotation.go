package gateway

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/rbac"
)

// workspace_key_rotation.go — MU-015 part 2 at the HTTP edge.
//
// TWO ROUTES BECAUSE THEY ARE TWO OPERATIONS, and conflating them would make
// the fast one as dangerous as the slow one:
//
//   - Rotating mints a new data key and retires the old one. It is instant,
//     touches no credential, and changes only what SUBSEQUENT writes use. This
//     is what you do the moment a key is suspected compromised, because it
//     stops the bleeding without a long-running job that could fail halfway.
//   - Re-encrypting rewrites every credential under the current key, which is
//     what actually closes out the old one. It touches every row and can fail
//     partway — and is safe to re-run, because a row already at the current
//     version is rewritten harmlessly.
//
// A single "rotate" endpoint doing both would mean an operator responding to
// an incident cannot take the instant action without also starting the slow
// one, and cannot re-run the slow one without minting yet another key.
//
// Gated on ActionRotate over ResourceSecrets, not on the workspace owner role
// used by export and deletion. Key rotation is a security operation an
// operator performs, not a data-lifecycle action an owner takes: the person
// who should be able to do it is whoever the RBAC matrix already trusts with
// this workspace's secrets.

func (s *Server) registerWorkspaceKeyRoutes(api fiber.Router) {
	api.Post("/workspace/keys/rotate",
		s.rbacMW(rbac.ResourceSecrets, rbac.ActionRotate), s.requireRecentAuth(),
		s.auditing("secret.key_rotate", "workspace", "", s.handleRotateWorkspaceKey))
	api.Post("/workspace/keys/reencrypt",
		s.rbacMW(rbac.ResourceSecrets, rbac.ActionRotate), s.requireRecentAuth(),
		s.auditing("secret.key_reencrypt", "workspace", "", s.handleReencryptWorkspace))
}

// rotatableKeys narrows the credential store to the operations these routes
// need, so a deployment whose implementation predates envelope encryption gets
// a clear 501 rather than a type-assertion panic.
//
// Deliberately NOT named with "Vault" in it: the ownership catalog's discovery
// scan treats a Store/Archive/Vault type declaration as a durable repository
// needing classification, and this is an interface over one rather than a
// second store. A name that trips that scan produces a catalog entry for a
// thing that persists nothing.
type rotatableKeys interface {
	RotateWorkspaceKey(ctx context.Context, workspaceID string) (int, error)
	ReencryptWorkspace(ctx context.Context, workspaceID string) (int, error)
}

func (s *Server) rotatableKeysOr501(c *fiber.Ctx) (rotatableKeys, bool) {
	if s.credVault == nil {
		_ = s.errMsg(c, fiber.StatusServiceUnavailable, "the credential vault is unavailable on this deployment")
		return nil, false
	}
	vault, ok := s.credVault.(rotatableKeys)
	if !ok {
		// 501 and not 500: the deployment is working, it just has a vault
		// implementation with no data key to rotate. An operator reading a 500
		// would go looking for a fault that is not there.
		_ = s.errMsg(c, fiber.StatusNotImplemented,
			"this deployment's credential vault does not use envelope encryption, so there is no data key to rotate")
		return nil, false
	}
	return vault, true
}

func (s *Server) handleRotateWorkspaceKey(c *fiber.Ctx) error {
	identity, ok := requestIdentity(c)
	if !ok {
		return s.errMsg(c, fiber.StatusForbidden, "verified workspace membership is required")
	}
	vault, ok := s.rotatableKeysOr501(c)
	if !ok {
		return nil
	}
	version, err := vault.RotateWorkspaceKey(c.UserContext(), identity.WorkspaceID())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the workspace key could not be rotated")
	}
	return c.JSON(fiber.Map{
		"key_version": version,
		// Said plainly, because the difference between the two operations is
		// exactly what an operator responding to an incident needs to know.
		"effect": "new credential writes use this key version; existing credentials remain readable under the key that wrote them",
		"next":   "POST /api/v1/workspace/keys/reencrypt rewrites existing credentials under the new key",
	})
}

func (s *Server) handleReencryptWorkspace(c *fiber.Ctx) error {
	identity, ok := requestIdentity(c)
	if !ok {
		return s.errMsg(c, fiber.StatusForbidden, "verified workspace membership is required")
	}
	vault, ok := s.rotatableKeysOr501(c)
	if !ok {
		return nil
	}
	moved, err := vault.ReencryptWorkspace(c.UserContext(), identity.WorkspaceID())
	if err != nil {
		// The count is reported WITH the error. A re-encryption that failed
		// partway has done real work, and an operator re-running it needs to
		// know it is resumable rather than starting from a clean slate they do
		// not have.
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error":       "the workspace's credentials could not all be re-encrypted",
			"reencrypted": moved,
			"remedy":      "re-run this endpoint; credentials already moved are rewritten harmlessly",
		})
	}
	return c.JSON(fiber.Map{"reencrypted": moved})
}
