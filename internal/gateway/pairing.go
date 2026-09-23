package gateway

import (
	"context"
	"errors"
	"fmt"
	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
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
		Name    string `json:"name"`
		Role    string `json:"role"`
		BaseURL string `json:"base_url"` // the address the phone will use; optional
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
	// One paired device per person (#216): a second phone is refused until
	// the first is unpaired, so a lost or replaced phone is an explicit act.
	if pd, err := s.pairedDeviceFor(c.Context(), meta.Subject); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	} else if pd != nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":  alreadyPairedMessage(pd),
			"paired": pd,
		})
	}
	tok, err := getPairingStore().CreateFor(0, meta)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	metrics.PairingTokensTotal.WithLabelValues("issued").Inc()
	// The QR must carry an address the PHONE can reach — never just the
	// browser's own origin (opening the GUI at localhost baked
	// http://localhost:18789 into the code, which a phone resolves to itself).
	nctx, ncancel := context.WithTimeout(c.Context(), min(s.httpRequestTimeout(), pairProbeBudget))
	tailnet := tailnetNameLookup(nctx)
	ncancel()
	pb, err := resolvePairBase(c.Context(), s.cfg.Server.PublicURL, c.BaseURL(), s.cfg.Server.Port, body.BaseURL, boundedProbe(pairProbeFor(s.tlsFingerprint), min(s.httpRequestTimeout(), pairProbeBudget)), tailnet)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	// Bounded like the reachability probes: a wrong guess must fail fast.
	pctx, cancel := context.WithTimeout(c.Context(), min(s.httpRequestTimeout(), pairProbeBudget))
	defer cancel()
	base, pairURL, fp := s.pinnedPairURL(pctx, pb.URL, tok.Code, body.BaseURL)
	return c.JSON(fiber.Map{
		"code":         tok.Code,
		"expires_at":   tok.ExpiresAt,
		"pair_url":     pairURL,
		"base_url":     base,
		"fingerprint":  fp,
		"reachable":    pb.Reachable,
		"hint":         pb.Hint,
		"candidates":   pb.Candidates,
		"subject":      tok.Subject,
		"display_name": tok.DisplayName,
		"role":         tok.Role,
	})
}

// pinnedPairURL turns a reachable base into the address the phone should
// actually use. When this gateway terminates TLS with its own certificate
// and the base is a direct address (auto-detected, or typed on the Mobile
// page for the phone), the QR carries https:// plus the key fingerprint
// (`fp`), so the phone connects encrypted from the very first request and
// pins the key. Two guards keep that claim honest (#171):
//   - an https:// base gets the pin only when the pinned probe proves the
//     certificate there is ours (an operator re-typing the QR address); a
//     proxy's certificate (nginx, tailscale serve --https) leaves it alone and
//     the phone's system trust is the right check for it (#174);
//   - an http:// base is upgraded only after actually connecting to
//     https://<base>/ping with the pin. tailscale serve --http owns the
//     tailnet address and does not speak TLS; upgrading blindly would point
//     the phone at a port that refuses the handshake.
//
// A base from server.public_url is never rewritten: the operator chose it.
// Returns base, pair URL, fingerprint ("" = none).
func (s *Server) pinnedPairURL(ctx context.Context, base, code, typedOverride string) (string, string, string) {
	pairURL := base + "/mobile?pair=" + code
	fp := s.tlsFingerprint
	if fp == "" {
		return base, pairURL, ""
	}
	direct := strings.TrimSpace(s.cfg.Server.PublicURL) == "" || strings.TrimSpace(typedOverride) != ""
	if !direct {
		return base, pairURL, ""
	}
	switch {
	case strings.HasPrefix(base, "https://"):
		// Already https: ours (typed by the operator, or a re-typed QR
		// address) gets the pin; a proxy's certificate does not.
		if !pairTLSProbe(ctx, base, fp) {
			return base, pairURL, ""
		}
	case strings.HasPrefix(base, "http://"):
		if !pairTLSProbe(ctx, base, fp) {
			return base, pairURL, ""
		}
		base = upgradeToHTTPS(base)
	default:
		return base, pairURL, ""
	}
	return base, base + "/mobile?pair=" + code + "&fp=" + fp, fp
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
		// The code may have been minted before another phone finished
		// pairing; the rule holds at redeem too (#216).
		if pd, err := s.pairedDeviceFor(c.Context(), subject); err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		} else if pd != nil {
			metrics.PairingTokensTotal.WithLabelValues("rejected").Inc()
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"paired": false, "reason": alreadyPairedMessage(pd), "device": pd})
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
			"fingerprint":  s.tlsFingerprint,
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

// pairedDevice is the one phone a person has paired (#216).
type pairedDevice struct {
	Subject     string     `json:"subject"`
	DisplayName string     `json:"display_name"`
	DeviceName  string     `json:"device_name,omitempty"`
	PairedAt    time.Time  `json:"paired_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	KeyIDs      []string   `json:"key_ids"`
	Owner       bool       `json:"owner"`
}

func alreadyPairedMessage(pd *pairedDevice) string {
	who := "you"
	if !pd.Owner {
		who = pd.DisplayName
	}
	device := pd.DeviceName
	if device == "" {
		device = "a phone"
	}
	return fmt.Sprintf("%s already paired on %s. Unpair it first to pair a different device.", device, pd.PairedAt.Format("Jan 2")) +
		func() string {
			if who == "you" {
				return ""
			}
			return " (" + who + ")"
		}()
}

// pairedDeviceFor reports the phone paired for subject, or nil. A phone is
// a live companion credential minted for that person; the device name comes
// from the phone's push registration when it made one.
func (s *Server) pairedDeviceFor(ctx context.Context, subject string) (*pairedDevice, error) {
	if s.apiKeyStore == nil {
		return nil, nil
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = "admin"
	}
	keys, err := s.apiKeyStore.List(ctx, false)
	if err != nil {
		return nil, err
	}
	var pd *pairedDevice
	for _, k := range keys {
		if k.Name != companionKeyName && !strings.HasPrefix(k.Name, companionKeyName+" ") {
			continue
		}
		cl := auth.ClaimsForAPIKey(k)
		if cl.Subject != subject {
			continue
		}
		if pd == nil {
			display := strings.Trim(strings.TrimSpace(strings.TrimPrefix(k.Name, companionKeyName)), " ()")
			if display == "" {
				display = subject
			}
			pd = &pairedDevice{Subject: subject, DisplayName: display, PairedAt: k.CreatedAt, Owner: subject == "admin"}
		}
		pd.KeyIDs = append(pd.KeyIDs, k.ID)
		if k.CreatedAt.Before(pd.PairedAt) {
			pd.PairedAt = k.CreatedAt
		}
		if k.LastUsedAt != nil && (pd.LastSeenAt == nil || k.LastUsedAt.After(*pd.LastSeenAt)) {
			t := *k.LastUsedAt
			pd.LastSeenAt = &t
		}
	}
	if pd == nil {
		return nil, nil
	}
	if store := mobilechan.DefaultStore(); store != nil {
		if devices, err := store.DevicesForUser(ctx, "", subject); err == nil && len(devices) > 0 {
			pd.DeviceName = devices[0].Name
		}
	}
	return pd, nil
}

// handlePairingStatus: is a phone paired for me? Admins may ask about
// anyone with ?subject=.
func (s *Server) handlePairingStatus(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	principal, _ := requestPrincipal(c)
	subject := strings.TrimSpace(principal.Subject)
	if subject == "" {
		subject = "admin"
	}
	if other := strings.TrimSpace(c.Query("subject")); other != "" && other != subject {
		if !strings.EqualFold(principal.Role, rbac.RoleAdmin) {
			return s.errMsg(c, fiber.StatusForbidden, "only an admin can look up someone else's phone")
		}
		subject = other
	}
	pd, err := s.pairedDeviceFor(c.Context(), subject)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if pd == nil {
		return c.JSON(fiber.Map{"paired": false, "subject": subject})
	}
	return c.JSON(fiber.Map{"paired": true, "subject": subject, "device": pd})
}

// handleUnpair clears a person's paired phone: every companion credential
// for them is revoked and their push registrations are forgotten, so a
// different device can pair. Oneself, or anyone for an admin (#216).
func (s *Server) handleUnpair(c *fiber.Ctx) error {
	var body struct {
		Subject string `json:"subject"`
	}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&body); err != nil {
			return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
		}
	}
	principal, _ := requestPrincipal(c)
	subject := strings.TrimSpace(principal.Subject)
	if subject == "" {
		subject = "admin"
	}
	if other := strings.TrimSpace(body.Subject); other != "" && other != subject {
		if !strings.EqualFold(principal.Role, rbac.RoleAdmin) {
			return s.errMsg(c, fiber.StatusForbidden, "only an admin can unpair someone else's phone")
		}
		subject = other
	}
	pd, err := s.pairedDeviceFor(c.Context(), subject)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	revoked := 0
	if pd != nil {
		for _, id := range pd.KeyIDs {
			if err := s.apiKeyStore.Revoke(c.Context(), id); err != nil && !errors.Is(err, apikeys.ErrNotFound) {
				return s.errJSON(c, fiber.StatusInternalServerError, err)
			}
			revoked++
		}
	}
	devices := 0
	if store := mobilechan.DefaultStore(); store != nil {
		if n, err := store.ClearDevicesForUser(c.Context(), "", subject); err == nil {
			devices = n
		}
	}
	return c.JSON(fiber.Map{"unpaired": true, "subject": subject, "revoked": revoked, "devices": devices})
}
