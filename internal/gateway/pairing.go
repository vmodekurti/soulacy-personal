package gateway

import (
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/pairing"
)

// pairingStore is a process-wide store of short-lived mobile pairing tokens.
// Lazily initialized so it costs nothing until the first pairing request.
var (
	pairingOnce  sync.Once
	pairingStore *pairing.Store
)

func getPairingStore() *pairing.Store {
	pairingOnce.Do(func() { pairingStore = pairing.NewStore() })
	return pairingStore
}

// handleCreatePairingToken mints a single-use, short-lived pairing token. The
// desktop renders {code} plus the gateway URL as a QR code; a phone scans it and
// calls the redeem endpoint. Returns a ready-to-encode pair URL for convenience.
func (s *Server) handleCreatePairingToken(c *fiber.Ctx) error {
	tok, err := getPairingStore().Create(0)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	metrics.PairingTokensTotal.WithLabelValues("issued").Inc()
	base := strings.TrimRight(c.BaseURL(), "/")
	return c.JSON(fiber.Map{
		"code":       tok.Code,
		"expires_at": tok.ExpiresAt,
		"pair_url":   base + "/mobile?pair=" + tok.Code,
	})
}

// handleRedeemPairingToken consumes a pairing token. A successful redeem proves
// the device physically scanned a QR shown on the desktop. Token issuance of a
// scoped mobile credential on success is the remaining integration step; this
// endpoint provides the verified one-time handshake it will build on.
func (s *Server) handleRedeemPairingToken(c *fiber.Ctx) error {
	var body struct {
		Code string `json:"code"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	if strings.TrimSpace(body.Code) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "code is required")
	}
	ok := getPairingStore().Redeem(body.Code)
	if !ok {
		metrics.PairingTokensTotal.WithLabelValues("rejected").Inc()
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"paired": false, "reason": "invalid or expired pairing code"})
	}
	metrics.PairingTokensTotal.WithLabelValues("redeemed").Inc()
	// On a verified redeem, mint a scoped mobile credential so the phone can call
	// the gateway (chat, approvals, push). Returned once — the client stores it.
	if s.apiKeyStore != nil {
		plaintext, key, err := s.mintPairedCredential(c)
		if err != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, "paired but could not issue credential: "+err.Error())
		}
		if plaintext == "" {
			// mintPairedCredential already wrote the refusal.
			return nil
		}
		return c.JSON(fiber.Map{
			"paired":  true,
			"token":   plaintext,
			"key_id":  key.ID,
			"scopes":  key.Scopes,
			"message": "Store this token; it is shown only once.",
		})
	}
	// No managed key store (auth disabled): pairing still succeeds, no token needed.
	return c.JSON(fiber.Map{"paired": true, "token": "", "message": "Gateway auth is disabled; no token required."})
}

// mintPairedCredential issues the paired device's credential, bound to the
// workspace the redeeming request was verified into.
//
// THE HOLE THIS CLOSES. The unscoped store.Create(ctx, name, scopes) hard-codes
// org_personal / ws_personal / role operator — see internal/auth/apikeys.
// In a Team deployment that means redeeming a pairing code minted an operator
// credential in the DEPLOYMENT's own workspace, bypassing the whole scoped
// credential API that MU-015 and MU-017 built, including its refusal to issue
// anything at all without a verified workspace identity. The route also has no
// authorization middleware — the pairing code is the capability, deliberately —
// so the one control on credential issuance was the code itself.
//
// Personal is unchanged: one workspace, one operator, and the same call it has
// always made (product invariant 7).
//
// Returns an empty plaintext when it has already written the response.
func (s *Server) mintPairedCredential(c *fiber.Ctx) (string, apikeys.APIKey, error) {
	scopes := []string{"chat", "memory", "config"}
	if !s.authorizationRequired() {
		plaintext, key, err := s.apiKeyStore.Create(c.Context(), "mobile-companion", scopes)
		return plaintext, key, err
	}

	scoped, ok := s.apiKeyStore.(apikeys.ScopedStore)
	if !ok {
		// Fail closed. Falling back to the unscoped call here would be the
		// exact escalation this function exists to prevent, and it would do it
		// silently on the deployments least able to notice.
		return "", apikeys.APIKey{}, nil
	}
	identity, present := requestIdentity(c)
	if !present {
		_ = s.errMsg(c, fiber.StatusForbidden, "a verified workspace identity is required to pair a device")
		return "", apikeys.APIKey{}, nil
	}
	// A paired phone acts AS the person who paired it, in the workspace they
	// were in when they scanned. Binding it to the redeemer rather than to a
	// synthetic subject means revoking their membership revokes the phone, and
	// the credential shows up in their own credential list rather than in an
	// unattributed pool nobody owns.
	expires := time.Now().UTC().Add(pairedCredentialTTL)
	plaintext, key, err := scoped.CreateScoped(c.Context(), apikeys.CreateRequest{
		Name:           "mobile-companion",
		Kind:           apikeys.KindPersonal,
		SubjectID:      identity.Subject(),
		OrganizationID: identity.OrganizationID(),
		WorkspaceIDs:   []string{identity.WorkspaceID()},
		Role:           identity.Role(),
		Scopes:         scopes,
		Issuer:         "soulacy-pairing",
		ExpiresAt:      &expires,
	})
	return plaintext, key, err
}

// pairedCredentialTTL bounds a paired device's credential.
//
// The unscoped path issues no expiry at all, which is defensible for a
// single-user machine and not for a team: a phone paired once and lost keeps
// working until somebody thinks to revoke a credential they never knew was
// created. Ninety days is long enough not to be a nuisance and short enough
// that an unnoticed loss expires.
const pairedCredentialTTL = 90 * 24 * time.Hour
