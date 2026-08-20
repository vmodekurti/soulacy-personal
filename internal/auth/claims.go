// Package auth implements Soulacy's authentication and authorisation subsystem.
//
// Two auth modes are supported, selected via config.yaml:
//
//	auth:
//	  mode: apikey   # (default) static bearer token, unchanged from Phase 2
//	  mode: jwt      # locally-issued short-lived JWTs + optional OIDC validation
//
// In both modes the claims extracted from a validated token are stored in the
// Fiber request context via SetClaims() and retrieved by downstream middleware
// (RBAC — Task #31) and handlers via ClaimsFromCtx().
package auth

import (
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"strings"
)

const claimsKey = "auth_claims"

// Claims are the JWT payload fields issued by the Soulacy gateway and
// validated from external OIDC providers. The Role field is intentionally
// reserved for Task #31 (RBAC) — it is populated but not enforced here.
type Claims struct {
	jwt.RegisteredClaims

	// Email is the user's email address (populated from OIDC `email` claim
	// or left empty for static-key and local-JWT sessions).
	Email string `json:"email,omitempty"`

	// Role is one of "admin", "operator", "viewer". Enforced by Task #31 RBAC
	// middleware. Local JWT issues "admin" by default; OIDC can supply the role
	// via a custom claim. Static API key always maps to "admin".
	Role string `json:"role,omitempty"`

	// Kind distinguishes access tokens from refresh tokens.
	// Only "access" tokens are accepted by the auth middleware.
	Kind string `json:"kind"`

	// PrincipalKind identifies the actor behind this access token (user,
	// personal_access_token, service_account, or static_api_key). Kind remains
	// the JWT token type and is always "access" for request authentication.
	PrincipalKind string `json:"principal_kind,omitempty"`

	// Scopes narrows what this credential may reach, by RBAC resource name
	// ("chat", "memory", "config", …). Empty means unrestricted — the role
	// alone decides, which is how every JWT and the static key behave.
	//
	// Managed API keys have always STORED scopes: the key store persists them,
	// the create endpoint echoes them back, and the pairing flow mints a
	// credential described in its own comment as "a scoped mobile credential".
	// Nothing ever read them. Every sk_ key authenticated as full operator, so
	// a phone paired with scopes [chat, memory, config] could also write agents,
	// write MCP servers and delete knowledge. A scope that is displayed but not
	// enforced is worse than none, because the operator is told the credential
	// is limited and reasonably believes it.
	Scopes []string `json:"scopes,omitempty"`

	// Tenant IDs are authority only after the gateway resolves the subject's
	// membership. WorkspaceID may select a workspace, but is never trusted by
	// itself. CredentialID identifies the authenticating key independently of
	// the human/service subject.
	// AuthTime is the Unix second at which the human last proved who they are.
	// Named after the OIDC claim of the same meaning so an external identity
	// provider can supply it directly.
	//
	// NOT `iat`. An access token rotates silently every fifteen minutes, so an
	// iat-freshness check is satisfied forever by a session nobody has
	// touched — the hijacked session step-up exists to stop. This value is
	// carried unchanged through rotation and moved only by an explicit
	// re-authentication. Zero means the credential never involved an
	// interactive authentication at all (a service account, or the
	// deployment's static key), which is an answer rather than a gap.
	AuthTime int64 `json:"auth_time,omitempty"`

	OrganizationID string   `json:"organization_id,omitempty"`
	WorkspaceID    string   `json:"workspace_id,omitempty"`
	WorkspaceIDs   []string `json:"workspace_ids,omitempty"`
	MembershipID   string   `json:"membership_id,omitempty"`
	CredentialID   string   `json:"credential_id,omitempty"`
}

// AllowsResource reports whether these claims may touch the named RBAC
// resource. Scopes only ever NARROW: a credential with none is unrestricted,
// and one with scopes must still satisfy its role on top of this.
func (c *Claims) AllowsResource(resource string) bool {
	return c.Allows(resource, "")
}

// Allows evaluates action-aware scopes. A legacy bare resource scope remains
// valid for all actions on that resource; new credentials should use
// resource:action or resource:* so least privilege is visible and enforceable.
func (c *Claims) Allows(resource, action string) bool {
	if c == nil || len(c.Scopes) == 0 {
		return true
	}
	for _, s := range c.Scopes {
		s = strings.TrimSpace(s)
		if strings.EqualFold(s, resource) || strings.EqualFold(s, resource+":*") || (action != "" && strings.EqualFold(s, resource+":"+action)) {
			return true
		}
	}
	return false
}

// SetClaims stores validated claims in the Fiber request context locals so
// downstream handlers and middleware can read them without re-parsing the token.
func SetClaims(c *fiber.Ctx, cl *Claims) {
	c.Locals(claimsKey, cl)
}

// ClaimsFromCtx retrieves the validated claims attached to the current request.
// Returns nil in open (dev) mode, apikey mode without JWT, or when auth is
// bypassed (e.g. health endpoint). Downstream code should treat nil as
// "authenticated with minimal information" rather than "unauthenticated".
func ClaimsFromCtx(c *fiber.Ctx) *Claims {
	if v := c.Locals(claimsKey); v != nil {
		if cl, ok := v.(*Claims); ok {
			return cl
		}
	}
	return nil
}
