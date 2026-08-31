package gateway

import (
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/tenancy"
)

func (s *Server) handleWorkspaceLoginConfig(c *fiber.Ctx) error {
	store, ok := s.tenantResolver.(tenancy.WorkspaceIdentityStore)
	if !ok {
		return s.errMsg(c, fiber.StatusNotFound, "workspace login is unavailable")
	}
	config, err := store.WorkspaceLoginConfig(c.UserContext(), c.Params("id"))
	if err != nil {
		return s.errMsg(c, fiber.StatusNotFound, "workspace was not found")
	}
	if config.OrganizationStatus == tenancy.WorkspaceSuspended {
		return c.Status(fiber.StatusLocked).JSON(fiber.Map{"error": "This organization is temporarily unavailable. Contact your organization administrator or Soulacy deployment operator.", "workspace": config})
	}
	if config.WorkspaceStatus == tenancy.WorkspaceSuspended {
		return c.Status(fiber.StatusLocked).JSON(fiber.Map{"error": "This workspace is temporarily unavailable. Contact your workspace owner or Soulacy deployment operator.", "workspace": config})
	}
	if config.WorkspaceStatus == tenancy.WorkspaceDeleting || config.WorkspaceStatus == tenancy.WorkspaceDeleted {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{"error": "This workspace is no longer available.", "workspace": config})
	}
	if config.IdentityStatus == "active" && config.ProviderType == "" {
		config.ProviderType = providerTypeForIssuer(s.config().Auth.OIDCIssuer)
	}
	publicDemo := s.config().PublicDemo.Enabled && strings.TrimSpace(s.config().PublicDemo.WorkspaceID) == strings.TrimSpace(config.WorkspaceID)
	return c.JSON(fiber.Map{"workspace": config, "redirect_url": workspaceOIDCRedirectURL(c, s.config().Auth.OIDCRedirectURL), "public_demo": publicDemo})
}

func workspaceOIDCRedirectURL(c *fiber.Ctx, configured string) string {
	const callbackPath = "/api/v1/auth/oidc/callback"
	requestURL, requestOK := validWorkspaceCallback(c.Protocol() + "://" + string(c.Context().Host()) + callbackPath)
	configuredURL, configuredOK := validWorkspaceCallback(strings.TrimSpace(configured))
	if configuredOK && !workspaceLoopbackHost(configuredURL.Hostname()) {
		return configuredURL.String()
	}
	if requestOK {
		return requestURL.String()
	}
	if configuredOK {
		return configuredURL.String()
	}
	return ""
}

func validWorkspaceCallback(raw string) (*url.URL, bool) {
	const callbackPath = "/api/v1/auth/oidc/callback"
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != callbackPath {
		return nil, false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && workspaceLoopbackHost(u.Hostname())) {
		return nil, false
	}
	return u, true
}

func workspaceLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

func providerTypeForIssuer(issuer string) string {
	issuer = strings.ToLower(issuer)
	switch {
	case strings.Contains(issuer, "google"):
		return "google"
	case strings.Contains(issuer, "microsoftonline"):
		return "microsoft"
	case strings.Contains(issuer, "okta"):
		return "okta"
	case strings.Contains(issuer, "auth0"):
		return "auth0"
	case strings.Contains(issuer, "keycloak"):
		return "keycloak"
	default:
		return "custom"
	}
}

func (s *Server) handleWorkspaceIdentitySetup(c *fiber.Ctx) error {
	store, ok := s.tenantResolver.(tenancy.WorkspaceIdentityStore)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace identity setup is unavailable")
	}
	var request tenancy.WorkspaceIdentitySetupRequest
	if err := c.BodyParser(&request); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid identity provider request")
	}
	// Authenticate the one-time setup capability before following a caller-
	// supplied issuer URL during OIDC discovery.
	if err := store.ValidateWorkspaceSetupToken(c.UserContext(), c.Params("id"), request.SetupToken); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "workspace setup link is invalid or expired")
	}
	allowed := map[string]bool{"google": true, "microsoft": true, "okta": true, "auth0": true, "keycloak": true, "custom": true}
	if !allowed[strings.ToLower(strings.TrimSpace(request.ProviderType))] {
		return s.errMsg(c, fiber.StatusBadRequest, "unsupported identity provider")
	}
	audience := strings.TrimSpace(request.Audience)
	if audience == "" {
		audience = strings.TrimSpace(request.ClientID)
	}
	if err := auth.ValidateOIDCProvider(request.Issuer, audience); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "OIDC discovery or signing keys could not be validated")
	}
	requestID := strings.TrimSpace(c.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	provider, err := store.ActivateWorkspaceIdentity(c.UserContext(), tenancy.Mutation{ActorSubject: "workspace-setup", RequestID: requestID, At: time.Now().UTC()}, c.Params("id"), request)
	if err != nil {
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "logo") {
			return s.errMsg(c, fiber.StatusBadRequest, err.Error())
		}
		return s.errMsg(c, fiber.StatusInternalServerError, "workspace identity provider could not be activated")
	}
	loginRef := provider.WorkspaceID
	if config, configErr := store.WorkspaceLoginConfig(c.UserContext(), provider.WorkspaceID); configErr == nil && config.WorkspaceSlug != "" {
		loginRef = config.WorkspaceSlug
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"provider": provider, "login_url": "/w/" + loginRef})
}
