package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
)

// platformroutes.go — the endpoints that change the DEPLOYMENT, not a workspace.
//
// THE GAP THIS CLOSES. Every /api/v1 route runs through workspaceContextMW,
// which in multi-user mode requires a verified workspace membership and
// refuses the bootstrap server key outright. That refusal is right — the
// static key names no workspace, so it cannot be given one — but its
// consequence was that EVERY authenticated principal in Team and Scale is a
// member of some workspace. There is no "operator of this deployment".
//
// Meanwhile `owner` and `admin` in the RBAC policy hold config:write, and
// those are MEMBERSHIP roles: owner of a workspace, i.e. a customer. So a
// customer could restart the process for every other customer, rewrite the one
// shared config.yaml — provider allowlists and ceilings, security settings,
// worker policy, and the approved extension catalogs. Workspace plugin and MCP
// instances are deliberately not platform routes: their stores are selected
// from verified workspace context.
//
// WHY A TABLE AND NOT A ROLE. Adding a `superadmin` value to the membership
// roles would put deployment power on the same ladder as tenant power, one
// promotion away — and the promotion is performed by another tenant's admin. A
// workspace role answers "what may this member do inside their workspace", and
// no amount of it should add up to "and also the machine". The two are
// different in kind, so the seam is the ROUTE, not the role.
//
// WHY A TABLE AND NOT A PER-HANDLER CHECK. Fiber registers group middleware by
// path prefix, so a second app.Group("/api/v1", …) with different middleware
// would still be wrapped by the first group's. One table, consulted in the one
// middleware every request already passes through, is the closest thing to
// structural available here: a route is platform or it is not, and a guard
// test fails the build when the table and the registrations disagree.

// platformRoute is one deployment-wide endpoint.
type platformRoute struct {
	Method string
	// Path is the route template as registered, with ":param" segments. It is
	// matched segment-wise against the request path.
	Path string
	// Why records what deployment-wide state this route writes. Present so
	// adding an entry requires saying what makes it platform-level, and so
	// removing one requires disagreeing in writing.
	Why string
}

// platformRoutes is the complete set. Workspace-scoped extensions are
// deliberately absent: their stores are selected from the verified workspace
// context and their lifecycle is governed by workspace RBAC.
var platformRoutes = []platformRoute{
	{"GET", "/api/v1/admin/bootstrap",
		"reports whether the deployment-wide tenant catalog needs its first owner"},
	{"POST", "/api/v1/admin/bootstrap",
		"creates the deployment's first organization, workspace, and owner exactly once"},
	{"GET", "/api/v1/admin/platform/overview",
		"reports deployment health and tenant counts without entering a workspace"},
	{"GET", "/api/v1/admin/platform/organizations",
		"lists tenant lifecycle metadata without exposing workspace content"},
	{"GET", "/api/v1/admin/platform/audit",
		"reads deployment-key and tenant lifecycle audit metadata without entering a workspace"},
	{"POST", "/api/v1/admin/platform/organizations",
		"provisions a tenant boundary and assigns its first owner without joining it"},
	{"POST", "/api/v1/admin/platform/organizations/:id/workspaces",
		"provisions a workspace and designated owner without granting the operator membership"},
	{"PATCH", "/api/v1/admin/platform/organizations/:id/status",
		"places or removes a deployment-level hold across every workspace in an organization"},
	{"PATCH", "/api/v1/admin/platform/workspaces/:workspaceID/address",
		"changes a workspace's public sign-in address without entering the workspace"},
	{"PATCH", "/api/v1/admin/platform/workspaces/:workspaceID/status",
		"places or removes an administrative hold on one workspace"},
	{"POST", "/api/v1/admin/restart",
		"calls os.Exit(0) on the process shared by every workspace"},
	{"GET", "/api/v1/logs",
		"tails the shared gateway log, which can contain signals from every workspace"},
	{"GET", "/api/v1/support/bundle",
		"downloads deployment diagnostics and shared gateway logs"},
	{"GET", "/api/v1/deployment/status",
		"reports shared infrastructure launch posture rather than workspace readiness"},
	{"GET", "/api/v1/system/updates/status",
		"reports release state for the gateway process shared by every workspace"},
	{"POST", "/api/v1/system/updates/check",
		"contacts the deployment release source and updates shared release state"},
	{"POST", "/api/v1/system/updates/upgrade",
		"replaces and restarts the gateway process shared by every workspace"},

	{"GET", "/api/v1/config",
		"returns the single deployment-wide config.yaml, including provider and channel settings"},
	{"PATCH", "/api/v1/config",
		"writes the single deployment-wide config.yaml: provider keys, budgets, security, channels"},

	{"GET", "/api/v1/registries",
		"the registry list lives in the deployment-wide config"},
	{"GET", "/api/v1/registries/search",
		"queries the operator's configured registries, including their credentials"},
	{"POST", "/api/v1/registries/probe",
		"makes the deployment fetch an operator-supplied URL"},
	{"POST", "/api/v1/registries",
		"adds a registry to the deployment-wide config"},

	{"POST", "/api/v1/mcp",
		"config.yaml's mcp.servers is the TEMPLATE every workspace instantiates, not one tenant's server set"},
	{"PATCH", "/api/v1/mcp/:id",
		"edits the deployment-wide MCP template"},
	{"DELETE", "/api/v1/mcp/:id",
		"removes a server from the deployment-wide MCP template"},
	{"POST", "/api/v1/mcp/provision-glama",
		"writes a provisioned server into the deployment-wide MCP template"},
	{"POST", "/api/v1/mcp/provision-registry",
		"writes a provisioned server into the deployment-wide MCP template"},
}

// isPlatformRoute reports whether a request targets deployment-wide state.
func isPlatformRoute(method, path string) bool {
	for _, route := range platformRoutes {
		if strings.EqualFold(route.Method, method) && pathMatchesTemplate(path, route.Path) {
			return true
		}
	}
	return false
}

// pathMatchesTemplate compares a request path against a route template
// segment-wise, treating ":name" as a single-segment wildcard.
//
// Segment-wise rather than by prefix, deliberately. A prefix match on
// "/api/v1/plugins" would also claim "/api/v1/plugins/installed", which is a
// read the settings page needs; a prefix match on "/api/v1/mcp" would claim
// the MCP list. Wrong in the direction that breaks the product rather than the
// one that leaks, but wrong, and a partition that is wrong in either direction
// stops being believed.
func pathMatchesTemplate(path, template string) bool {
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	tmplParts := strings.Split(strings.Trim(template, "/"), "/")
	if len(pathParts) != len(tmplParts) {
		return false
	}
	for i, part := range tmplParts {
		if strings.HasPrefix(part, ":") {
			if pathParts[i] == "" {
				return false
			}
			continue
		}
		if part != pathParts[i] {
			return false
		}
	}
	return true
}

// platformPrincipal reports whether this request carries the deployment's own
// credential rather than a workspace member's.
//
// The bootstrap server.api_key is the only thing in the system that already
// means "operator of this deployment": multi-user validation requires it, it
// belongs to no workspace, and auth.Engine stamps it with a distinguishable
// credential ID. Using it rather than inventing an identity keeps the number
// of things that can administer the machine at one.
func platformPrincipal(c *fiber.Ctx) bool {
	claims := auth.ClaimsFromCtx(c)
	return claims != nil && strings.TrimSpace(claims.CredentialID) == staticAPIKeyCredentialID
}

// staticAPIKeyCredentialID is the credential ID auth.Engine stamps on a
// request authenticated with the bootstrap server key.
const staticAPIKeyCredentialID = "static-api-key"
