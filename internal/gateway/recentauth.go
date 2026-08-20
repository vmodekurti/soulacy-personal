package gateway

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/apiversion"
	"github.com/soulacy/soulacy/internal/auth"
)

// recentauth.go — MU-030 criterion 5: "high-impact actions require
// reauthentication or an equivalent recent-auth check."
//
// WHAT THIS PROTECTS AGAINST is a session that is still valid but no longer
// attended: a laptop left open, a token lifted from a machine, a tab an
// ex-colleague still has. Role-based authorization cannot see any of that —
// the request carries a genuine owner's credential and every check passes.
// The only question that separates the owner from whoever has their session is
// "did a human prove themselves recently", and nothing in this deployment was
// asking it.
//
// THE TRAP, AND WHY THIS IS NOT AN `iat` CHECK. An access token here rotates
// silently every fifteen minutes for as long as a browser is open, so `iat` is
// always fresh and an iat-based check passes forever on a session nobody has
// touched. It would be a check that never fails, which is worse than no check
// because it reads like protection in every review afterwards. The token
// therefore carries `auth_time`, which only an explicit re-authentication
// moves — see auth.TokenIdentity.AuthTime.

// recentAuthWindow is how long a human's proof stays good.
//
// Long enough that an administrator working through a batch of changes is not
// re-prompted between each one; short enough that a session abandoned over
// lunch does not still hold elevated authority. Deliberately not configurable
// yet: a deployment that can set it to 24h has a check that never fires, and
// the request to make it settable should arrive with the reason.
const recentAuthWindow = 10 * time.Minute

// requireRecentAuth gates a route on a recent interactive authentication.
//
// It returns 401 with a distinct code rather than 403, because the remedy is
// something the caller can do: re-present a credential. A 403 tells a client
// it lacks permission, and it does not — it has permission and stale proof.
func (s *Server) requireRecentAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !s.recentAuthRequired(c) {
			return c.Next()
		}
		claims := auth.ClaimsFromCtx(c)
		if recentlyAuthenticated(claims, time.Now()) {
			return c.Next()
		}
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error":           "this action needs you to confirm it is still you",
			"code":            apiversion.CodeReauthRequired,
			"remedy":          "POST /api/v1/auth/reauthenticate with your credential, then retry",
			"max_age_seconds": int(recentAuthWindow.Seconds()),
		})
	}
}

// recentAuthRequired decides whether this request is one that can be asked.
//
// Two deliberate exemptions, both of which are about who is on the other end
// rather than about how sensitive the action is:
//
//   - A single-tenant deployment has one operator and no session-hijack story
//     that step-up improves; prompting there is friction with no property
//     behind it, and product invariant 7 says Personal must not notice that
//     the deployment became tenant-aware.
//   - A NON-HUMAN principal cannot be prompted. A service account or a machine
//     credential presenting a valid token has no browser to bounce to, and
//     demanding step-up would simply break automation at 03:00. That is a real
//     residual: a stolen service credential is not slowed down by this. It is
//     the wrong control for that threat — rotation and revocation are — and
//     saying so is better than a check that appears to cover it.
func (s *Server) recentAuthRequired(c *fiber.Ctx) bool {
	if !s.authorizationRequired() {
		return false
	}
	claims := auth.ClaimsFromCtx(c)
	if claims == nil {
		// No claims means the request did not arrive through the JWT path at
		// all. Something earlier in the chain has already decided whether it
		// may proceed; refusing here would break the static-key and open-mode
		// deployments that never had a human session to age.
		return false
	}
	return humanPrincipal(claims)
}

// humanPrincipal reports whether there is a person who could be re-prompted.
//
// An empty PrincipalKind is treated as human. Older tokens predate the field,
// and the safe default for a check that adds friction is the one that ASKS:
// wrongly prompting a service account is a visible, reported failure, while
// wrongly exempting a person is a silent hole that looks exactly like working
// software.
func humanPrincipal(claims *auth.Claims) bool {
	switch strings.TrimSpace(claims.PrincipalKind) {
	case "", "user":
		return true
	default:
		return false
	}
}

// recentlyAuthenticated is the decision, separated from the HTTP shell so the
// awkward cases are testable without a request.
func recentlyAuthenticated(claims *auth.Claims, now time.Time) bool {
	if claims == nil || claims.AuthTime <= 0 {
		// Zero is "no interactive authentication ever happened on this
		// credential". It is NOT "authenticated at the epoch, so very stale" —
		// though both refuse here. The distinction matters at the call site
		// above, which decides whether such a principal should be asked at all.
		return false
	}
	// Both sides truncated to whole seconds. The claim is a Unix second, so a
	// client that re-authenticated at exactly the window boundary is otherwise
	// refused by up to 999ms of quantisation it cannot see or control — and
	// the refusal is indistinguishable from a real timeout, so the user
	// re-authenticates and is refused again.
	return now.UTC().Truncate(time.Second).Sub(time.Unix(claims.AuthTime, 0).UTC()) <= recentAuthWindow
}
