package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth/apikeys"
)

// Config holds all auth subsystem parameters.
// Values are parsed from the YAML AuthConfig in internal/config and from
// environment variables before being passed to New().
type Config struct {
	// Mode is "apikey" (default, backwards-compatible) or "jwt".
	Mode string

	// JWTSecret is the HMAC-SHA256 signing key for locally-issued tokens.
	// Required when Mode == "jwt". If empty an ephemeral key is generated —
	// valid only for the current process lifetime (tokens are invalidated on
	// restart). Set to a stable random hex string in production.
	JWTSecret string

	// JWTAccessTTL is the lifetime of access tokens. Default 15m.
	JWTAccessTTL time.Duration

	// JWTRefreshTTL is the lifetime of opaque refresh tokens. Default 168h (7d).
	JWTRefreshTTL time.Duration

	// OIDCIssuer is the base URL of a third-party OIDC provider
	// (e.g. "https://accounts.google.com"). When set, the gateway acts as an
	// OIDC resource server and accepts JWTs issued by this provider in addition
	// to locally-issued tokens.
	OIDCIssuer string

	// OIDCAudience is the expected "aud" claim in OIDC tokens.
	// Defaults to OIDCClientID when empty.
	OIDCAudience string

	// OIDCClientID identifies this application to the OIDC provider.
	// Used as the audience claim fallback when OIDCAudience is empty.
	OIDCClientID string
}

func (c *Config) applyDefaults() {
	if c.Mode == "" {
		c.Mode = "apikey"
	}
	if c.JWTAccessTTL == 0 {
		c.JWTAccessTTL = 15 * time.Minute
	}
	if c.JWTRefreshTTL == 0 {
		c.JWTRefreshTTL = 7 * 24 * time.Hour
	}
	if c.OIDCAudience == "" {
		c.OIDCAudience = c.OIDCClientID
	}
}

// Engine is the Soulacy auth subsystem.
//
//	apikey mode (default): validates requests against the static server.api_key.
//	                       No JWTs, no refresh tokens, identical to Phase 2 behaviour.
//
//	jwt mode:              (1) accepts the static API key as a fallback,
//	                       (2) validates locally-issued access JWTs (HS256),
//	                       (3) validates OIDC-provider JWTs when oidc_issuer is set.
//	                       Tokens carry Claims (sub, email, role) for downstream RBAC.
type Engine struct {
	cfg         Config
	staticKey   string         // server.api_key; always checked, any mode
	issuer      *Issuer        // non-nil when cfg.Mode == "jwt"
	oidc        *OIDCValidator // non-nil when cfg.OIDCIssuer != ""
	log         *zap.Logger
	apiKeyStore apikeys.Store // non-nil when managed API keys are enabled
}

// SetAPIKeyStore wires the managed API key store. When set, tokens with the
// "sk_" prefix are validated against the store before falling through to JWT
// validation. Safe to call once at startup before any traffic.
func (e *Engine) SetAPIKeyStore(s apikeys.Store) {
	e.apiKeyStore = s
}

// New constructs an Engine and performs OIDC discovery synchronously (if
// configured). Discovery failure is fatal when OIDC is the only usable
// authentication method; otherwise the configured fallback remains effective.
func New(cfg Config, staticKey string, log *zap.Logger) (*Engine, error) {
	cfg.applyDefaults()

	e := &Engine{
		cfg:       cfg,
		staticKey: staticKey,
		log:       log,
	}

	if cfg.Mode == "jwt" {
		iss, err := newIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
		if err != nil {
			return nil, fmt.Errorf("auth jwt issuer: %w", err)
		}
		e.issuer = iss
		secretSource := "configured"
		if cfg.JWTSecret == "" {
			secretSource = "ephemeral (not persistent across restarts — set auth.jwt_secret in production)"
		}
		log.Info("auth: JWT mode",
			zap.Duration("access_ttl", cfg.JWTAccessTTL),
			zap.Duration("refresh_ttl", cfg.JWTRefreshTTL),
			zap.String("secret", secretSource),
		)
	}

	if cfg.OIDCIssuer != "" {
		oidcVal, err := newOIDCValidator(cfg.OIDCIssuer, cfg.OIDCAudience)
		if err != nil {
			if e.staticKey == "" && e.issuer == nil {
				return nil, fmt.Errorf("auth OIDC discovery: %w", err)
			}
			log.Warn("auth: OIDC discovery failed — OIDC tokens will be rejected until next restart",
				zap.String("issuer", cfg.OIDCIssuer),
				zap.Error(err),
			)
		} else {
			e.oidc = oidcVal
			log.Info("auth: OIDC validator ready", zap.String("issuer", cfg.OIDCIssuer))
		}
	}

	return e, nil
}

// Middleware returns a Fiber middleware that enforces authentication.
//
// Validation order:
//  1. Static API key (Bearer or ?api_key= query param) — always checked first.
//  2. Locally-issued JWT — only in jwt mode.
//  3. OIDC JWT — only when oidc_issuer is configured.
//
// Validated claims are stored via SetClaims() for downstream use.
// Returns 401 if no credential matches.
func (e *Engine) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		if token == "" {
			token = c.Cookies("soulacy_access")
		}
		// WebSocket connections cannot set headers; accept ?api_key= as fallback.
		if token == "" {
			token = c.Query("api_key")
		}

		// 1. Static API key
		if e.staticKey != "" && secretEqual(token, e.staticKey) {
			SetClaims(c, &Claims{Email: "admin", Role: "admin", Kind: "access"})
			return c.Next()
		}

		// 1.5. Managed API key (sk_ prefix) — validated against the key store.
		// Role defaults to "operator" (same as a regular authenticated user).
		if e.apiKeyStore != nil && strings.HasPrefix(token, "sk_") {
			if ak, err := e.apiKeyStore.Validate(c.Context(), token); err == nil {
				SetClaims(c, &Claims{
					RegisteredClaims: jwt.RegisteredClaims{Subject: ak.ID},
					Email:            ak.Name,
					Role:             "operator",
					Kind:             "access",
					// Carry the key's stored scopes so RBAC can honour them.
					// They were persisted and echoed back but never enforced, so
					// every sk_ key was a full operator whatever it was minted
					// with.
					Scopes: ak.Scopes,
				})
				return c.Next()
			}
		}

		// 2. Local JWT
		if e.issuer != nil {
			if cl, err := e.issuer.VerifyAccess(token); err == nil {
				SetClaims(c, cl)
				return c.Next()
			}
		}

		// 3. OIDC JWT
		if e.oidc != nil {
			if cl, err := e.oidc.Validate(token); err == nil {
				SetClaims(c, cl)
				return c.Next()
			}
		}

		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid or missing credentials",
		})
	}
}

// Effective reports whether this engine has at least one usable credential
// verifier. It deliberately describes capability, not object construction:
// an allocated apikey engine with no key is not effective authentication.
func (e *Engine) Effective() bool {
	return e != nil && (e.staticKey != "" || e.issuer != nil || e.oidc != nil || e.apiKeyStore != nil)
}

// HandleTokenRequest handles POST /api/v1/auth/token.
//
// Request body:
//
//	{"api_key": "<static-key>"}
//
// Response (jwt mode only):
//
//	{"access_token":"eyJ…","refresh_token":"<opaque>","expires_in":900,"token_type":"Bearer"}
func (e *Engine) HandleTokenRequest(c *fiber.Ctx) error {
	if e.issuer == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "JWT auth mode is not enabled — set auth.mode=jwt in config.yaml",
		})
	}
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if !secretEqual(req.APIKey, e.staticKey) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid api_key"})
	}
	access, refresh, expiresIn, err := e.issuer.Issue("admin", "", "admin")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	e.setAuthCookies(c, access, refresh, expiresIn)
	return c.JSON(fiber.Map{
		"access_token":  access,
		"refresh_token": refresh,
		"expires_in":    expiresIn,
		"token_type":    "Bearer",
	})
}

// HandleRefresh handles POST /api/v1/auth/refresh.
//
// Request body:
//
//	{"refresh_token": "<opaque>"}
//
// Response (new access token only — refresh token is rotated on each use):
//
//	{"access_token":"eyJ…","expires_in":900,"token_type":"Bearer"}
func (e *Engine) HandleRefresh(c *fiber.Ctx) error {
	if e.issuer == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "JWT auth mode is not enabled",
		})
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.BodyParser(&req); err != nil && len(c.Body()) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if req.RefreshToken == "" {
		req.RefreshToken = c.Cookies("soulacy_refresh")
	}
	if req.RefreshToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "refresh_token is required"})
	}
	access, newRefresh, expiresIn, err := e.issuer.Refresh(req.RefreshToken)
	if err != nil {
		e.clearAuthCookies(c)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
	}
	e.setAuthCookies(c, access, newRefresh, expiresIn)
	return c.JSON(fiber.Map{
		"access_token":  access,
		"refresh_token": newRefresh,
		"expires_in":    expiresIn,
		"token_type":    "Bearer",
	})
}

// HandleMe handles GET /api/v1/auth/me.
// Returns the identity and role from the current request's validated token.
func (e *Engine) HandleMe(c *fiber.Ctx) error {
	out := fiber.Map{"mode": e.cfg.Mode}
	if cl := ClaimsFromCtx(c); cl != nil {
		out["sub"] = cl.Subject
		out["email"] = cl.Email
		out["role"] = cl.Role
		if cl.ExpiresAt != nil {
			out["exp"] = cl.ExpiresAt.Time.Unix()
		}
		if cl.IssuedAt != nil {
			out["iat"] = cl.IssuedAt.Time.Unix()
		}
	} else {
		// apikey mode — no claims object, return minimal info
		out["sub"] = "admin"
		out["role"] = "admin"
	}
	return c.JSON(out)
}

// Mode returns the configured auth mode ("apikey" or "jwt").
func (e *Engine) Mode() string { return e.cfg.Mode }

// Close shuts down background goroutines (JWKS refresh, refresh token cleanup).
// Idempotent.
func (e *Engine) Close() {
	if e.issuer != nil {
		e.issuer.Close()
	}
	if e.oidc != nil {
		e.oidc.close()
	}
}

func (e *Engine) setAuthCookies(c *fiber.Ctx, access, refresh string, expiresIn int) {
	secure := strings.EqualFold(c.Protocol(), "https") || strings.EqualFold(c.Get("X-Forwarded-Proto"), "https")
	c.Cookie(&fiber.Cookie{Name: "soulacy_access", Value: access, HTTPOnly: true, Secure: secure, SameSite: "Lax", MaxAge: expiresIn, Path: "/"})
	c.Cookie(&fiber.Cookie{Name: "soulacy_refresh", Value: refresh, HTTPOnly: true, Secure: secure, SameSite: "Strict", MaxAge: int(e.cfg.JWTRefreshTTL.Seconds()), Path: "/api/v1/auth"})
}

func (e *Engine) clearAuthCookies(c *fiber.Ctx) {
	secure := strings.EqualFold(c.Protocol(), "https") || strings.EqualFold(c.Get("X-Forwarded-Proto"), "https")
	for _, item := range []struct{ name, path string }{{"soulacy_access", "/"}, {"soulacy_refresh", "/api/v1/auth"}} {
		c.Cookie(&fiber.Cookie{Name: item.name, Value: "", HTTPOnly: true, Secure: secure, SameSite: "Strict", MaxAge: -1, Path: item.path})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// secretEqual is a length-independent constant-time string comparison.
// Both inputs are hashed to 32 bytes first to eliminate a timing channel
// when they differ in length (subtle.ConstantTimeCompare is only
// constant-time when both slices have the same length).
func secretEqual(got, want string) bool {
	if want == "" {
		return false
	}
	gh := sha256.Sum256([]byte(got))
	wh := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gh[:], wh[:]) == 1
}
