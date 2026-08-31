package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"strings"
	"sync"
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

	// RefreshStorePath enables durable refresh rotation, replay detection, and
	// logout revocation across gateway restarts. The store contains token
	// hashes only and is written with owner-only permissions.
	RefreshStorePath string

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

	// OIDCClientSecret is optional for providers that support public PKCE
	// clients. Prefer injecting it from the process environment or a secret
	// manager instead of writing it to config.yaml.
	OIDCClientSecret string

	// OIDCRedirectURL optionally overrides the browser-derived GUI callback,
	// for example when Soulacy runs behind a reverse proxy with a public origin.
	OIDCRedirectURL string

	// OIDCScopes defaults to openid, profile, email.
	OIDCScopes []string

	// AllowUnprovisionedOIDC permits a cryptographically verified global OIDC
	// identity with a verified email to receive an onboarding-only session when
	// it has no workspace membership. Gateway routing confines that principal
	// to self-service signup; it is never accepted by workspace middleware.
	AllowUnprovisionedOIDC bool
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
	if len(c.OIDCScopes) == 0 {
		c.OIDCScopes = []string{"openid", "profile", "email"}
	}
}

// IdentityLinker maps a verified provider subject to a local user. The
// provider subject is always authoritative; email may only assist linking when
// the provider explicitly marked it verified.
type IdentityLinker interface {
	LinkOIDCIdentity(context.Context, string, string, string, bool, string) (string, error)
}

type WorkspaceOIDCProvider struct {
	WorkspaceID, ProviderType, Issuer, ClientID, ClientSecret, Audience string
	Scopes                                                              []string
}

type WorkspaceOIDCProviderResolver func(context.Context, string) (WorkspaceOIDCProvider, bool)
type WorkspaceOIDCAvailabilityResolver func(context.Context, string) bool
type WorkspaceTokenIdentityResolver func(context.Context, string, string) (TokenIdentity, bool)
type WorkspaceInvitationAccepter func(context.Context, string, string, string) (TokenIdentity, bool)

// WorkspaceDemoAdmitter is called only after a verified OIDC identity has no
// existing membership in the explicitly requested workspace. Implementations
// must independently verify that workspace is the operator-designated demo.
type WorkspaceDemoAdmitter func(context.Context, string, string) (TokenIdentity, bool)

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
	cfg                         Config
	staticKey                   string         // server.api_key; always checked, any mode
	issuer                      *Issuer        // non-nil when cfg.Mode == "jwt"
	oidc                        *OIDCValidator // non-nil when cfg.OIDCIssuer != ""
	log                         *zap.Logger
	apiKeyStore                 apikeys.Store // non-nil when managed API keys are enabled
	flows                       *oidcFlowStore
	identityLinker              IdentityLinker
	refreshAllowed              func(context.Context, string) bool
	identityResolver            TokenIdentityResolver
	workspaceIdentityResolver   WorkspaceTokenIdentityResolver
	workspaceProviderResolver   WorkspaceOIDCProviderResolver
	workspaceAvailability       WorkspaceOIDCAvailabilityResolver
	workspaceInvitationAccepter WorkspaceInvitationAccepter
	workspaceDemoAdmitter       WorkspaceDemoAdmitter
	providerMu                  sync.Mutex
	providerValidators          map[string]*OIDCValidator
	auditSink                   func(*fiber.Ctx, AuthEvent)
}

// AuthEvent is one authentication-lifecycle occurrence, for the workspace
// audit trail (MU-031 criterion 2, which names authentication first and which
// nothing was recording).
//
// The category was entirely absent: no login, logout, re-authentication or
// failed-attempt record existed anywhere. That is the one class of audit
// record an intrusion investigation starts from, and the trail could not
// answer "when did this credential first appear" at all.
type AuthEvent struct {
	// Action is "auth.login", "auth.logout", "auth.reauthenticate".
	Action string
	// Subject is who, when the attempt got far enough to know. An empty
	// subject on a failure is an answer: the credential did not identify
	// anybody, which is different from a known subject being refused.
	Subject string
	// Outcome is "ok" or "failed". FAILURES ARE RECORDED. A trail of
	// successes cannot show a credential being guessed at, and the pattern of
	// failures is usually the first thing an investigation looks for.
	Outcome string
	// Reason is a short, non-identifying cause on failure — never the
	// credential, never a hash of it, never enough to confirm a guess.
	Reason string
}

// SetAuditSink installs the callback that records authentication events.
//
// Optional and nil-safe: internal/auth must stay usable without a gateway, and
// a deployment with no action log still authenticates. It takes the Fiber
// context because the trail's actor, workspace and request id are resolved
// from the request, not from this package.
func (e *Engine) SetAuditSink(sink func(*fiber.Ctx, AuthEvent)) { e.auditSink = sink }

func (e *Engine) recordAuth(c *fiber.Ctx, event AuthEvent) {
	if e == nil || e.auditSink == nil {
		return
	}
	e.auditSink(c, event)
}

// SetAPIKeyStore wires the managed API key store. When set, tokens with the
// "sk_" prefix are validated against the store before falling through to JWT
// validation. Safe to call once at startup before any traffic.
func (e *Engine) SetAPIKeyStore(s apikeys.Store) {
	e.apiKeyStore = s
}

func (e *Engine) SetIdentityLinker(linker IdentityLinker) { e.identityLinker = linker }
func (e *Engine) SetWorkspaceOIDCProviderResolver(resolve WorkspaceOIDCProviderResolver) {
	e.workspaceProviderResolver = resolve
	if resolve != nil && e.flows == nil {
		e.flows = newOIDCFlowStore()
	}
}
func (e *Engine) SetWorkspaceOIDCAvailabilityResolver(resolve WorkspaceOIDCAvailabilityResolver) {
	e.workspaceAvailability = resolve
}
func (e *Engine) SetWorkspaceTokenIdentityResolver(resolve WorkspaceTokenIdentityResolver) {
	e.workspaceIdentityResolver = resolve
}
func (e *Engine) SetWorkspaceInvitationAccepter(accept WorkspaceInvitationAccepter) {
	e.workspaceInvitationAccepter = accept
}
func (e *Engine) SetWorkspaceDemoAdmitter(admit WorkspaceDemoAdmitter) {
	e.workspaceDemoAdmitter = admit
}

func (e *Engine) workspaceValidator(provider WorkspaceOIDCProvider) (*OIDCValidator, error) {
	key := provider.Issuer + "\x00" + provider.Audience
	e.providerMu.Lock()
	defer e.providerMu.Unlock()
	if current := e.providerValidators[key]; current != nil {
		return current, nil
	}
	validator, err := newOIDCValidator(provider.Issuer, provider.Audience)
	if err != nil {
		return nil, err
	}
	if e.providerValidators == nil {
		e.providerValidators = map[string]*OIDCValidator{}
	}
	e.providerValidators[key] = validator
	return validator, nil
}

func ValidateOIDCProvider(issuer, audience string) error {
	validator, err := newOIDCValidator(strings.TrimRight(strings.TrimSpace(issuer), "/"), strings.TrimSpace(audience))
	if validator != nil {
		validator.close()
	}
	return err
}

// TokenIdentityResolver reads a subject's current tenancy — the workspace the
// access token should act in, and the membership that grants it.
//
// Optional: a personal deployment has no membership source and issues tokens
// with no tenancy, which resolves to the personal workspace everywhere
// downstream and is exactly the behaviour those deployments have always had.
type TokenIdentityResolver func(ctx context.Context, subject string) (TokenIdentity, bool)

// SetTokenIdentityResolver wires the live membership lookup used when minting
// and refreshing tokens.
//
// Without it, a signed-in member of a workspace receives an access token with
// no WorkspaceID, which authenticates fine and then acts with the personal
// workspace's authority — a valid token for the wrong tenant.
func (e *Engine) SetTokenIdentityResolver(resolve TokenIdentityResolver) {
	e.identityResolver = resolve
}

// tokenIdentityFor resolves the tenancy a new token should carry, falling back
// to the un-tenanted identity when no resolver is wired.
//
// A resolver that is present and says "no" is a refusal, not a fallback: the
// subject authenticated, but has no active membership to act under, and
// issuing a personal-workspace token for them would hand an outsider the
// deployment's own workspace.
func (e *Engine) tokenIdentityFor(ctx context.Context, base TokenIdentity) (TokenIdentity, bool) {
	if e.identityResolver == nil {
		return base, true
	}
	resolved, ok := e.identityResolver(ctx, base.Subject)
	if !ok {
		return TokenIdentity{}, false
	}
	resolved.Subject = base.Subject
	if strings.TrimSpace(resolved.Email) == "" {
		resolved.Email = base.Email
	}
	if strings.TrimSpace(resolved.Role) == "" {
		resolved.Role = base.Role
	}
	if strings.TrimSpace(resolved.PrincipalKind) == "" {
		resolved.PrincipalKind = base.PrincipalKind
	}
	return resolved, true
}

// SetRefreshAuthorizer installs the live account/membership eligibility check
// used before a refresh token is rotated. The callback receives the locally
// linked user subject, never an unverified external claim.
func (e *Engine) SetRefreshAuthorizer(authorizer func(context.Context, string) bool) {
	e.refreshAllowed = authorizer
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
		iss, err := newIssuerWithStorePath(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL, cfg.RefreshStorePath)
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
			e.flows = newOIDCFlowStore()
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
			SetClaims(c, &Claims{
				RegisteredClaims: jwt.RegisteredClaims{Subject: "api-key"},
				Email:            "admin", Role: "admin", Kind: "access", PrincipalKind: "static_api_key", CredentialID: "static-api-key",
			})
			return c.Next()
		}

		// 1.5. Managed API key (sk_ prefix) — validated against the key store.
		// Role defaults to "operator" (same as a regular authenticated user).
		if e.apiKeyStore != nil && strings.HasPrefix(token, "sk_") {
			if ak, err := e.apiKeyStore.Validate(c.Context(), token); err == nil {
				workspaceID := ""
				if len(ak.WorkspaceIDs) == 1 {
					workspaceID = ak.WorkspaceIDs[0]
				}
				var expiresAt *jwt.NumericDate
				if ak.ExpiresAt != nil {
					expiresAt = jwt.NewNumericDate(*ak.ExpiresAt)
				}
				SetClaims(c, &Claims{
					RegisteredClaims: jwt.RegisteredClaims{Subject: ak.SubjectID, Issuer: ak.Issuer, ExpiresAt: expiresAt},
					Email:            ak.Name,
					Role:             ak.Role,
					Kind:             "access",
					PrincipalKind:    ak.Kind,
					// Carry the key's stored scopes so RBAC can honour them.
					// They were persisted and echoed back but never enforced, so
					// every sk_ key was a full operator whatever it was minted
					// with.
					Scopes: ak.Scopes, OrganizationID: ak.OrganizationID,
					WorkspaceID: workspaceID, WorkspaceIDs: append([]string(nil), ak.WorkspaceIDs...),
					CredentialID: ak.ID,
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

// Reachable reports whether a caller can actually OBTAIN a credential this
// engine will accept.
//
// It differs from Effective in exactly one case, and that case is a real
// configuration: jwt mode with no jwt_secret, no OIDC issuer and no static API
// key. Effective is true there because an ephemeral issuer exists, so the
// middleware is armed — but the only way to mint one of its tokens is the
// token exchange, which authenticates with the static key, and secretEqual
// refuses an empty expected secret. The engine will therefore reject every
// request forever while reporting itself as effective.
//
// A local JWT issuer is deliberately NOT counted as a credential source for
// that reason: it can verify a token, it cannot let anybody get one.
func (e *Engine) Reachable() bool {
	return e != nil && (e.staticKey != "" || e.oidc != nil || e.apiKeyStore != nil)
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
	}
	if !secretEqual(req.APIKey, e.staticKey) {
		// Recorded before the refusal is returned. A failed sign-in that
		// leaves no trace is the one an attacker most wants.
		e.recordAuth(c, AuthEvent{Action: "auth.login", Outcome: "failed", Reason: "credential rejected"})
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication failed"})
	}
	// The static API key is a *platform* credential, not a workspace member's
	// (invariant 10): it is the deployment operator's key from config.yaml, and
	// there is no membership behind it to resolve. It therefore issues with no
	// tenancy, which resolves to the personal workspace — the deployment's own.
	// A successful key exchange IS the interactive authentication, so this is
	// where the step-up clock starts. Refreshes carry it forward unchanged.
	access, refresh, expiresIn, err := e.issuer.IssueFor(TokenIdentity{
		Subject: "admin", Role: "admin", AuthTime: time.Now().UTC(),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "authentication failed"})
	}
	e.recordAuth(c, AuthEvent{Action: "auth.login", Subject: "admin", Outcome: "ok"})
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
	if err := c.BodyParser(&req); err != nil {
		if len(c.Body()) > 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
		}
	}
	if req.RefreshToken == "" {
		req.RefreshToken = c.Cookies("soulacy_refresh")
	}
	if req.RefreshToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "refresh_token is required"})
	}
	access, newRefresh, expiresIn, err := e.issuer.RefreshAuthorized(req.RefreshToken, func(subject string) (TokenIdentity, bool) {
		if e.refreshAllowed != nil && !e.refreshAllowed(c.UserContext(), subject) {
			return TokenIdentity{}, false
		}
		// Re-read the membership rather than replaying the tenancy minted days
		// ago: a member moved to another workspace, or demoted, must not keep
		// acting under the authority they had when they signed in.
		return e.tokenIdentityFor(c.UserContext(), TokenIdentity{Subject: subject})
	})
	if err != nil {
		e.clearAuthCookies(c)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication failed"})
	}
	e.setAuthCookies(c, access, newRefresh, expiresIn)
	return c.JSON(fiber.Map{
		"access_token":  access,
		"refresh_token": newRefresh,
		"expires_in":    expiresIn,
		"token_type":    "Bearer",
	})
}

// HandleReauthenticate handles POST /api/v1/auth/reauthenticate (MU-030
// criterion 5).
//
// It is the ONLY thing that moves `auth_time`. A caller re-presents the
// credential they signed in with; on success they get a fresh token pair in a
// NEW refresh family, stamped with the moment they proved themselves.
//
// Three properties are load-bearing:
//
//   - It requires an already-authenticated request. Step-up elevates an
//     existing session; treating it as a login path would make it a second,
//     less-examined way in.
//   - The re-presented credential must belong to the SAME subject. Otherwise
//     "re-authenticate" is an account-switch that keeps the previous session's
//     workspace context — a confused-deputy shape rather than an elevation.
//   - It answers the same 401 for a wrong credential as for a subject
//     mismatch, so it cannot be used to test whether a given key belongs to
//     somebody else.
func (e *Engine) HandleReauthenticate(c *fiber.Ctx) error {
	if e.issuer == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "JWT auth mode is not enabled",
		})
	}
	current := ClaimsFromCtx(c)
	if current == nil || strings.TrimSpace(current.Subject) == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication is required"})
	}
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
	}
	if !secretEqual(req.APIKey, e.staticKey) {
		e.recordAuth(c, AuthEvent{Action: "auth.reauthenticate", Subject: current.Subject, Outcome: "failed", Reason: "credential rejected"})
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication failed"})
	}
	// The static key's subject is "admin" — see HandleTokenRequest. Re-proving
	// it only elevates the session that key signed in.
	if current.Subject != "admin" {
		// A correct credential presented by the wrong session. Recorded with
		// its own reason because it is a different event from a bad
		// credential — somebody proving a key that is not theirs to elevate a
		// session that is.
		e.recordAuth(c, AuthEvent{Action: "auth.reauthenticate", Subject: current.Subject, Outcome: "failed", Reason: "subject mismatch"})
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication failed"})
	}
	identity, ok := e.tokenIdentityFor(c.UserContext(), TokenIdentity{
		Subject: current.Subject, Email: current.Email, Role: current.Role,
		OrganizationID: current.OrganizationID, WorkspaceID: current.WorkspaceID,
		MembershipID: current.MembershipID,
	})
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication failed"})
	}
	access, refresh, expiresIn, err := e.issuer.Reauthenticate(identity)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "authentication failed"})
	}
	e.recordAuth(c, AuthEvent{Action: "auth.reauthenticate", Subject: current.Subject, Outcome: "ok"})
	e.setAuthCookies(c, access, refresh, expiresIn)
	return c.JSON(fiber.Map{
		"access_token":  access,
		"refresh_token": refresh,
		"expires_in":    expiresIn,
		"token_type":    "Bearer",
	})
}

// HandleLogout revokes both the presented access token and refresh-token
// family. It always returns 204 so callers cannot use it as an account oracle.
func (e *Engine) HandleLogout(c *fiber.Ctx) error {
	if e.issuer != nil {
		access := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		if access == "" {
			access = c.Cookies("soulacy_access")
		}
		var req struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = c.BodyParser(&req)
		if req.RefreshToken == "" {
			req.RefreshToken = c.Cookies("soulacy_refresh")
		}
		e.issuer.Revoke(access, req.RefreshToken)
	}
	subject := ""
	if claims := ClaimsFromCtx(c); claims != nil {
		subject = claims.Subject
	}
	// Always "ok": logout answers 204 unconditionally so it cannot be used as
	// an account oracle, and the audit record must not become the oracle the
	// status code refuses to be.
	e.recordAuth(c, AuthEvent{Action: "auth.logout", Subject: subject, Outcome: "ok"})
	e.clearAuthCookies(c)
	return c.SendStatus(fiber.StatusNoContent)
}

// HandleMe handles GET /api/v1/auth/me.
// Returns the identity and role from the current request's validated token.
func (e *Engine) HandleMe(c *fiber.Ctx) error {
	out := fiber.Map{"mode": e.cfg.Mode}
	if cl := ClaimsFromCtx(c); cl != nil {
		out["sub"] = cl.Subject
		out["email"] = cl.Email
		out["role"] = cl.Role
		out["principal_kind"] = principalKindOrTokenKind(cl)
		out["organization_id"] = cl.OrganizationID
		out["workspace_id"] = cl.WorkspaceID
		out["workspace_ids"] = cl.WorkspaceIDs
		out["credential_id"] = cl.CredentialID
		out["issuer"] = cl.Issuer
		out["scopes"] = cl.Scopes
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

func principalKindOrTokenKind(cl *Claims) string {
	if cl == nil {
		return ""
	}
	if strings.TrimSpace(cl.PrincipalKind) != "" {
		return cl.PrincipalKind
	}
	return cl.Kind
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
	if e.flows != nil {
		e.flows.close()
	}
	e.providerMu.Lock()
	for _, validator := range e.providerValidators {
		validator.close()
	}
	e.providerValidators = nil
	e.providerMu.Unlock()
}

func (e *Engine) setAuthCookies(c *fiber.Ctx, access, refresh string, expiresIn int) {
	secure := e.secureCookies(c)
	c.Cookie(&fiber.Cookie{Name: "soulacy_access", Value: access, HTTPOnly: true, Secure: secure, SameSite: "Lax", MaxAge: expiresIn, Path: "/"})
	c.Cookie(&fiber.Cookie{Name: "soulacy_refresh", Value: refresh, HTTPOnly: true, Secure: secure, SameSite: "Strict", MaxAge: int(e.cfg.JWTRefreshTTL.Seconds()), Path: "/api/v1/auth"})
}

func (e *Engine) clearAuthCookies(c *fiber.Ctx) {
	for _, item := range []struct{ name, path string }{{"soulacy_access", "/"}, {"soulacy_refresh", "/api/v1/auth"}} {
		c.Cookie(&fiber.Cookie{Name: item.name, Value: "", HTTPOnly: true, Secure: e.secureCookies(c), SameSite: "Strict", MaxAge: -1, Path: item.path})
	}
}

func (e *Engine) secureCookies(c *fiber.Ctx) bool {
	// Fiber may observe HTTP when TLS terminates at a trusted reverse proxy.
	// The registered public callback is authoritative for hosted deployments.
	return strings.EqualFold(c.Protocol(), "https") || strings.HasPrefix(strings.ToLower(e.cfg.OIDCRedirectURL), "https://")
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
