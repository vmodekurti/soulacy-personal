package ratelimit

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// tenancy.go — MU-024 criterion 5: "rate limits are keyed by authenticated
// credential and workspace, not only IP."
//
// THE BUG THIS FIXES IS WORSE THAN A COLLISION. The agent bucket was keyed
// `"agent:" + agentID`, and the agent ID is read from the REQUEST BODY. Agent
// IDs are unique per workspace, not per deployment, so two tenants with a
// "support-bot" already shared one bucket by accident. But because the ID
// comes from the body rather than from anything verified, a member of one
// workspace could also name ANOTHER tenant's agent and burn its rate-limit
// budget on purpose — a cross-tenant denial of service needing no credential
// beyond a valid session of one's own.
//
// The fix is not to validate the body value. It is to prefix every key with a
// workspace nobody can assert: the one on the verified identity. A body field
// then selects a bucket WITHIN the caller's own tenant, where naming your own
// agents is exactly what the limiter is for.

// workspaceOf returns the verified workspace for a request.
//
// Falls back to the personal workspace when there is no identity, which is
// what a single-tenant deployment has always been — so its keys are unchanged
// in shape and its buckets keep their meaning (invariant 7).
func workspaceOf(c *fiber.Ctx) string {
	if c != nil {
		if identity, ok := requestctx.From(c.UserContext()); ok {
			if workspaceID := strings.TrimSpace(identity.WorkspaceID()); workspaceID != "" {
				return wsroot.Normalize(workspaceID)
			}
		}
	}
	return wsroot.PersonalWorkspaceID
}

// credentialOf returns the authenticated credential a limit is keyed by.
//
// The credential, not only the subject: a person with a long-lived API key and
// the same person in a browser session are two things whose traffic an
// operator may legitimately want bounded separately, and revoking one must not
// hand the other a fresh budget. Falls back to the subject, then to "anon".
func credentialOf(c *fiber.Ctx) string {
	if c != nil {
		if identity, ok := requestctx.From(c.UserContext()); ok {
			if credentialID := strings.TrimSpace(identity.CredentialID()); credentialID != "" {
				return credentialID
			}
			if subject := strings.TrimSpace(identity.Subject()); subject != "" {
				return subject
			}
		}
		if claims := auth.ClaimsFromCtx(c); claims != nil && strings.TrimSpace(claims.Subject) != "" {
			return strings.TrimSpace(claims.Subject)
		}
	}
	return "anon"
}

// userKey is the rate-limit key for one credential in one workspace.
func userKey(c *fiber.Ctx) string {
	return bucketUserKey(workspaceOf(c), credentialOf(c))
}

// agentKey is the rate-limit key for one agent in one workspace.
//
// The workspace prefix comes first so a key can never be forged by choosing an
// agent ID: whatever the caller writes in the body lands inside their own
// tenant's namespace.
func agentKey(c *fiber.Ctx, agentID string) string {
	return "ws:" + workspaceOf(c) + "|agent:" + strings.TrimSpace(agentID)
}

// bucketKey is the in-memory token-bucket key for an agent.
func bucketKey(workspaceID, agentID string) string {
	return wsroot.Normalize(workspaceID) + "|" + strings.TrimSpace(agentID)
}

// bucketUserKey is the in-memory token-bucket key for a credential. Exposed so
// the recorder, the middleware, the status endpoint and the tests all name a
// bucket the same way — four hand-rolled key expressions is how a limiter ends
// up checking a bucket nothing fills.
func bucketUserKey(workspaceID, credentialID string) string {
	return "ws:" + wsroot.Normalize(workspaceID) + "|user:" + strings.TrimSpace(credentialID)
}
