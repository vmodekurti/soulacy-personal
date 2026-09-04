package gateway

import (
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"

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
		plaintext, key, err := s.apiKeyStore.Create(c.Context(), "mobile-companion", []string{"chat", "memory", "config"})
		if err != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, "paired but could not issue credential: "+err.Error())
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
