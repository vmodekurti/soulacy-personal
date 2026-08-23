package gateway

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/tenancy"
)

type selfServiceSignupRequest struct {
	OrganizationName string `json:"organization_name"`
	WorkspaceName    string `json:"workspace_name"`
	WorkspaceSlug    string `json:"workspace_slug,omitempty"`
	DisplayName      string `json:"display_name"`
}

func (s *Server) handleSignupConfig(c *fiber.Ctx) error {
	cfg := s.config()
	enabled := cfg != nil && cfg.Signup.Enabled
	return c.JSON(fiber.Map{
		"enabled":  enabled,
		"provider": "oidc",
	})
}

func (s *Server) handleSignupSession(c *fiber.Ctx) error {
	if cfg := s.config(); cfg == nil || !cfg.Signup.Enabled {
		return s.errMsg(c, fiber.StatusNotFound, "self-service signup is not enabled")
	}
	claims := auth.ClaimsFromCtx(c)
	if !verifiedOnboardingSession(claims) {
		return s.errMsg(c, fiber.StatusForbidden, "a verified signup identity is required")
	}
	return c.JSON(fiber.Map{"eligible": true, "email": claims.Email})
}

func (s *Server) handleSelfServiceSignup(c *fiber.Ctx) error {
	if cfg := s.config(); cfg == nil || !cfg.Signup.Enabled {
		return s.errMsg(c, fiber.StatusNotFound, "self-service signup is not enabled")
	}
	claims := auth.ClaimsFromCtx(c)
	if !verifiedOnboardingSession(claims) {
		return s.errMsg(c, fiber.StatusForbidden, "a verified signup identity is required")
	}
	provisioner, ok := s.tenantResolver.(tenancy.SelfServiceProvisioner)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "self-service tenant provisioning is unavailable")
	}
	var request selfServiceSignupRequest
	if err := c.BodyParser(&request); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid signup request")
	}
	request.OrganizationName = strings.TrimSpace(request.OrganizationName)
	request.WorkspaceName = strings.TrimSpace(request.WorkspaceName)
	request.WorkspaceSlug = strings.ToLower(strings.TrimSpace(request.WorkspaceSlug))
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if request.DisplayName == "" {
		request.DisplayName = strings.TrimSpace(claims.Email)
	}
	if request.OrganizationName == "" || request.WorkspaceName == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "organization name and workspace name are required")
	}
	requestID := strings.TrimSpace(c.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	result, err := provisioner.ProvisionSelfServiceOrganization(c.UserContext(), tenancy.Mutation{
		ActorSubject: claims.Subject,
		RequestID:    requestID,
		At:           time.Now().UTC(),
	}, claims.Subject, tenancy.BootstrapRequest{
		OrganizationName: request.OrganizationName,
		WorkspaceName:    request.WorkspaceName,
		WorkspaceSlug:    request.WorkspaceSlug,
		OwnerEmail:       claims.Email,
		OwnerDisplayName: request.DisplayName,
	})
	if errors.Is(err, tenancy.ErrSelfServiceAlreadyProvisioned) {
		return s.errMsg(c, fiber.StatusConflict, err.Error())
	}
	if err != nil {
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "slug") || strings.Contains(err.Error(), "email") {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
		return s.errMsg(c, fiber.StatusInternalServerError, "organization could not be created")
	}
	address := result.Workspace.Slug
	if address == "" {
		address = result.Workspace.ID
	}
	setupPath := "/w/" + url.PathEscape(address) + "/setup?token=" + url.QueryEscape(result.SetupToken)
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"result": result,
		"next": fiber.Map{
			"refresh_session": true,
			"setup_path":      setupPath,
			"workspace_id":    result.Workspace.ID,
		},
	})
}

// Only Soulacy's locally issued onboarding token proves that the global OIDC
// callback validated email_verified and linked the provider identity. The auth
// engine can also validate provider bearer tokens for ordinary APIs; accepting
// a provider-supplied principal_kind claim here would skip those guarantees.
func verifiedOnboardingSession(claims *auth.Claims) bool {
	return claims != nil && claims.Issuer == "soulacy" && claims.Kind == "access" &&
		strings.TrimSpace(claims.PrincipalKind) == "onboarding" &&
		strings.TrimSpace(claims.Subject) != "" && strings.TrimSpace(claims.Email) != ""
}
