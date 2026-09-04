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
}

// AllowsResource reports whether these claims may touch the named RBAC
// resource. Scopes only ever NARROW: a credential with none is unrestricted,
// and one with scopes must still satisfy its role on top of this.
func (c *Claims) AllowsResource(resource string) bool {
	if c == nil || len(c.Scopes) == 0 {
		return true
	}
	for _, s := range c.Scopes {
		if strings.EqualFold(strings.TrimSpace(s), resource) {
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
