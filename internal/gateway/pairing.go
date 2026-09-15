package gateway

import (
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/pairing"
	"github.com/soulacy/soulacy/internal/rbac"
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
//
// Body (optional): {"name": "Priya", "role": "operator"|"viewer"} pairs a
// phone for someone else in the household; only an admin may do that. With
// no body the phone becomes the caller: same subject, so the phone shares
// memory, approvals and inbox with the person's web login.
func (s *Server) handleCreatePairingToken(c *fiber.Ctx) error {
	var body struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&body); err != nil {
			return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
		}
	}
	principal, _ := requestPrincipal(c)
	// A device paired for oneself never outranks the device that paired it:
	// a viewer's watch is a viewer. Everyone else's second device is an
	// operator, which is what a phone paired from the web already gets.
	meta := pairing.Token{Subject: strings.TrimSpace(principal.Subject), Role: selfPairingRole(principal.Role)}
	if meta.Subject == "" {
		meta.Subject = "admin"
	}
	if name := strings.TrimSpace(body.Name); name != "" {
		if !strings.EqualFold(principal.Role, rbac.RoleAdmin) {
			return s.errMsg(c, fiber.StatusForbidden, "only an admin can pair a phone for someone else")
		}
		subject := householdSubject(name)
		if subject == "" || len(name) > 60 {
			return s.errMsg(c, fiber.StatusBadRequest, "name must contain letters or digits and be at most 60 characters")
		}
		if subject == "admin" || subject == meta.Subject {
			return s.errMsg(c, fiber.StatusBadRequest, "that name is the owner; pair without a name for your own phone")
		}
		role := strings.ToLower(strings.TrimSpace(body.Role))
		if role == "" {
			role = "operator"
		}
		if role != "operator" && role != "viewer" {
			return s.errMsg(c, fiber.StatusBadRequest, "role must be operator or viewer")
		}
		meta = pairing.Token{Subject: subject, DisplayName: name, Role: role}
	}
	tok, err := getPairingStore().CreateFor(0, meta)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	metrics.PairingTokensTotal.WithLabelValues("issued").Inc()
	base := strings.TrimRight(c.BaseURL(), "/")
	return c.JSON(fiber.Map{
		"code":         tok.Code,
		"expires_at":   tok.ExpiresAt,
		"pair_url":     base + "/mobile?pair=" + tok.Code,
		"subject":      tok.Subject,
		"display_name": tok.DisplayName,
		"role":         tok.Role,
	})
}

func selfPairingRole(callerRole string) string {
	if strings.EqualFold(strings.TrimSpace(callerRole), rbac.RoleViewer) {
		return rbac.RoleViewer
	}
	return rbac.RoleOperator
}

// householdSubject turns a person's name into a stable, URL-safe subject:
// "Priya S." → "priya-s". It is the identity every owner-scoped store keys
// on, so it must not change when the display name is edited later.
func householdSubject(name string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// companionKeyName is the managed-key name every paired phone uses; the
// key's subject says whose phone it is.
const companionKeyName = "mobile-companion"

// handleListHouseholdMembers lists who has a paired phone: one row per
// subject, with how many phones and when one was last seen. Admin only.
func (s *Server) handleListHouseholdMembers(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	principal, _ := requestPrincipal(c)
	if !strings.EqualFold(principal.Role, rbac.RoleAdmin) {
		return s.errMsg(c, fiber.StatusForbidden, "only an admin can list the household")
	}
	if s.apiKeyStore == nil {
		return c.JSON(fiber.Map{"members": []any{}})
	}
	keys, err := s.apiKeyStore.List(c.Context(), false)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	type member struct {
		Subject     string     `json:"subject"`
		DisplayName string     `json:"display_name"`
		Role        string     `json:"role"`
		Phones      int        `json:"phones"`
		KeyIDs      []string   `json:"key_ids"`
		LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
		Owner       bool       `json:"owner"`
	}
	bySubject := map[string]*member{}
	var order []string
	for _, k := range keys {
		if k.Name != companionKeyName && !strings.HasPrefix(k.Name, companionKeyName+" ") {
			continue
		}
		cl := auth.ClaimsForAPIKey(k)
		m := bySubject[cl.Subject]
		if m == nil {
			display := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(k.Name, companionKeyName), " "))
			display = strings.Trim(display, "()")
			if display == "" {
				display = cl.Subject
			}
			m = &member{Subject: cl.Subject, DisplayName: display, Role: cl.Role, Owner: cl.Subject == "admin"}
			bySubject[cl.Subject] = m
			order = append(order, cl.Subject)
		}
		m.Phones++
		m.KeyIDs = append(m.KeyIDs, k.ID)
		if k.LastUsedAt != nil && (m.LastUsedAt == nil || k.LastUsedAt.After(*m.LastUsedAt)) {
			t := *k.LastUsedAt
			m.LastUsedAt = &t
		}
	}
	out := make([]member, 0, len(order))
	for _, sub := range order {
		out = append(out, *bySubject[sub])
	}
	return c.JSON(fiber.Map{"members": out})
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
	tok, ok := getPairingStore().RedeemToken(body.Code)
	if !ok {
		metrics.PairingTokensTotal.WithLabelValues("rejected").Inc()
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"paired": false, "reason": "invalid or expired pairing code"})
	}
	metrics.PairingTokensTotal.WithLabelValues("redeemed").Inc()
	// On a verified redeem, mint a scoped mobile credential so the phone can call
	// the gateway (chat, approvals, push). Returned once — the client stores it.
	// The key authenticates as the person the code was minted for, so the
	// phone and that person's web login are one identity everywhere.
	if s.apiKeyStore != nil {
		subject, role, display := tok.Subject, tok.Role, tok.DisplayName
		if subject == "" {
			subject, role = "admin", "operator"
		}
		name := companionKeyName
		if display != "" {
			name = companionKeyName + " (" + display + ")"
		}
		scopes := []string{"chat", "agents:read", "memory", "config"}
		var (
			plaintext string
			key       apikeys.APIKey
			err       error
		)
		if ids, ok := s.apiKeyStore.(apikeys.IdentityStore); ok {
			plaintext, key, err = ids.CreateFor(c.Context(), name, scopes, subject, role)
		} else {
			plaintext, key, err = s.apiKeyStore.Create(c.Context(), name, scopes)
		}
		if err != nil {
			return s.errMsg(c, fiber.StatusInternalServerError, "paired but could not issue credential: "+err.Error())
		}
		return c.JSON(fiber.Map{
			"paired":       true,
			"token":        plaintext,
			"key_id":       key.ID,
			"scopes":       key.Scopes,
			"subject":      subject,
			"role":         role,
			"display_name": display,
			"message":      "Store this token; it is shown only once.",
		})
	}
	// No managed key store (auth disabled): pairing still succeeds, no token needed.
	return c.JSON(fiber.Map{"paired": true, "token": "", "message": "Gateway auth is disabled; no token required."})
}
