package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/tenancy"
)

const workspaceIdentityLocal = "workspace_identity"

// SetTenantResolver wires membership verification. Team and Scale requests
// fail closed until a resolver is available; Personal installations receive a
// resolver for their implicit tenant during application wiring.
func (s *Server) SetTenantResolver(resolver tenancy.Resolver) {
	s.tenantResolver = resolver
	if members, ok := resolver.(tenancy.MemberManager); ok {
		s.tenantMembers = members
	}
}

func (s *Server) workspaceContextMW() fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims := auth.ClaimsFromCtx(c)
		if claims == nil {
			// Personal's explicit open development mode remains backwards
			// compatible. Multi-user modes never synthesize an identity.
			if s.cfg != nil && s.cfg.DeploymentMode() != config.DeploymentModePersonal {
				return s.errMsg(c, fiber.StatusUnauthorized, "authenticated identity is required")
			}
			claims = &auth.Claims{Role: "admin", Kind: "local", CredentialID: "open-personal"}
			claims.Subject = "local-owner"
		}
		if s.cfg != nil && config.IsMultiUserMode(s.cfg.DeploymentMode()) && claims.CredentialID == "static-api-key" {
			return s.errMsg(c, fiber.StatusForbidden, "the static server key cannot access workspace APIs in multi-user mode")
		}
		subject := strings.TrimSpace(claims.Subject)
		if subject == "" {
			subject = strings.TrimSpace(claims.Email)
		}
		if subject == "" {
			return s.errMsg(c, fiber.StatusUnauthorized, "authenticated subject is required")
		}
		resolver := s.tenantResolver
		if resolver == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace membership resolver is unavailable")
		}

		// A signed workspace claim has precedence. The header is only a selector
		// and gains no authority: ResolveMembership must independently verify it.
		requestedWorkspace := strings.TrimSpace(claims.WorkspaceID)
		if requestedWorkspace == "" {
			requestedWorkspace = strings.TrimSpace(c.Get("X-Soulacy-Workspace"))
		}
		personalMode := s.cfg == nil || s.cfg.DeploymentMode() == config.DeploymentModePersonal
		// Credentials created before Personal tenancy existed carry stable
		// aliases. They remain valid only in Personal mode and are resolved to
		// that installation's single implicit workspace.
		if personalMode && requestedWorkspace == "ws_personal" {
			requestedWorkspace = ""
		}
		if len(claims.WorkspaceIDs) > 0 && !credentialBindsWorkspace(claims.WorkspaceIDs, requestedWorkspace) {
			if !(personalMode && credentialBindsWorkspace(claims.WorkspaceIDs, "ws_personal") && requestedWorkspace == "") {
				return s.errMsg(c, fiber.StatusForbidden, "credential is not bound to the requested workspace")
			}
		}
		membership, err := resolver.ResolveMembership(c.UserContext(), subject, requestedWorkspace)
		if err != nil {
			if errors.Is(err, tenancy.ErrMembershipNotFound) {
				return s.errMsg(c, fiber.StatusForbidden, "workspace membership is required")
			}
			return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace membership could not be verified")
		}
		if claimedOrg := strings.TrimSpace(claims.OrganizationID); claimedOrg != "" && claimedOrg != membership.OrganizationID && !(personalMode && claimedOrg == "org_personal") {
			return s.errMsg(c, fiber.StatusForbidden, "credential organization does not match the workspace")
		}
		// The verified membership is authoritative in Team/Scale mode. A
		// credential's signed role may be broader, stale, or valid in another
		// workspace and must never override the workspace-specific assignment.
		role := strings.TrimSpace(membership.Role)
		if personalMode {
			if claimRole := strings.TrimSpace(claims.Role); claimRole != "" {
				role = claimRole
			}
		}
		identity, err := requestctx.New(requestctx.Input{
			Subject: subject, OrganizationID: membership.OrganizationID,
			WorkspaceID: membership.WorkspaceID, MembershipID: membership.MembershipID,
			Role: role, Scopes: claims.Scopes, CredentialID: credentialID(claims),
			RequestID: localString(c.Locals("request_id")), PrincipalKind: principalKind(claims),
		})
		if err != nil {
			return s.errMsg(c, fiber.StatusForbidden, "verified workspace context is incomplete")
		}
		c.Locals(workspaceIdentityLocal, identity)
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		return c.Next()
	}
}

func credentialBindsWorkspace(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func principalKind(claims *auth.Claims) string {
	if claims == nil {
		return ""
	}
	if kind := strings.TrimSpace(claims.PrincipalKind); kind != "" {
		return kind
	}
	return strings.TrimSpace(claims.Kind)
}

func credentialID(claims *auth.Claims) string {
	if claims == nil {
		return ""
	}
	if id := strings.TrimSpace(claims.CredentialID); id != "" {
		return id
	}
	return strings.TrimSpace(claims.ID)
}

func localString(value any) string {
	s, _ := value.(string)
	return s
}

func requestIdentity(c *fiber.Ctx) (requestctx.Identity, bool) {
	if identity, ok := c.Locals(workspaceIdentityLocal).(requestctx.Identity); ok {
		return identity, true
	}
	return requestctx.From(c.UserContext())
}

func withWorkspaceIdentity(c *fiber.Ctx, ctx context.Context) context.Context {
	if identity, ok := requestIdentity(c); ok {
		return requestctx.With(ctx, identity)
	}
	return ctx
}

// detachedRequestContext preserves verified authority while decoupling work
// from Fiber's reusable request buffer and client disconnect lifecycle.
func detachedRequestContext(c *fiber.Ctx) context.Context {
	return withRequestPrincipal(c, context.WithoutCancel(c.UserContext()))
}

// defaultPersonalResolver keeps directly constructed test/embedded gateways
// backwards compatible. Production application wiring replaces it with the
// stable IDs persisted in data/tenants.db.
func defaultPersonalResolver() tenancy.Resolver {
	return tenancy.NewPersonalResolver(tenancy.PersonalTenant{
		OrganizationID: "org_personal", WorkspaceID: "ws_personal",
		UserID: "usr_local_owner", MembershipID: "mem_personal_owner",
	})
}

func shouldUseDefaultPersonalResolver(cfg *config.Config) bool {
	return cfg == nil || cfg.DeploymentMode() == config.DeploymentModePersonal
}
