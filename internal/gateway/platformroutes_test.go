package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
)

func platformServer(t *testing.T, mode string) *Server {
	t.Helper()
	s := &Server{log: zap.NewNop()}
	s.setConfig(&config.Config{Deployment: config.DeploymentConfig{Mode: mode}})
	return s
}

func platformCall(t *testing.T, s *Server, method, path, credentialID string) int {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Use(func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
			Role:             "owner", Kind: "access", PrincipalKind: "user",
			CredentialID: credentialID,
		})
		return c.Next()
	})
	handler := s.platformMW("config", "write")
	reached := func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) }
	switch strings.ToUpper(method) {
	case "GET":
		app.Get(path, handler, reached)
	case "PATCH":
		app.Patch(path, handler, reached)
	case "DELETE":
		app.Delete(path, handler, reached)
	default:
		app.Post(path, handler, reached)
	}
	req := httptest.NewRequest(method, path, nil)
	resp, err := app.Test(req, 5000)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// A workspace owner is a TENANT owner. Their role must not reach the endpoints
// that change the machine every tenant shares.
func TestAWorkspaceOwnerCannotChangeTheDeployment(t *testing.T) {
	for _, mode := range []string{config.DeploymentModeTeam, config.DeploymentModeScale} {
		s := platformServer(t, mode)
		for _, endpoint := range []struct{ method, path string }{
			{"PATCH", "/api/v1/config"},
			{"GET", "/api/v1/deployment/status"},
			{"GET", "/api/v1/system/updates/status"},
			{"POST", "/api/v1/system/updates/check"},
			{"POST", "/api/v1/system/updates/upgrade"},
		} {
			if code := platformCall(t, s, endpoint.method, endpoint.path, "cred_alice"); code != fiber.StatusForbidden {
				t.Errorf("%s: a workspace owner got %d for %s %s", mode, code, endpoint.method, endpoint.path)
			}
		}
	}
}

// The operator must have a way in, or the endpoints are simply broken.
func TestTheBootstrapCredentialOpensDeploymentEndpoints(t *testing.T) {
	for _, mode := range []string{config.DeploymentModeTeam, config.DeploymentModeScale} {
		s := platformServer(t, mode)
		if code := platformCall(t, s, "PATCH", "/api/v1/config", staticAPIKeyCredentialID); code != fiber.StatusNoContent {
			t.Errorf("%s: the deployment's own credential got %d", mode, code)
		}
	}
}

// Invariant 7: a single-user installation must not notice any of this. There,
// the ordinary RBAC path decides, exactly as before.
func TestPersonalInstallationsAreUnchanged(t *testing.T) {
	s := platformServer(t, config.DeploymentModePersonal)
	if code := platformCall(t, s, "PATCH", "/api/v1/config", "cred_alice"); code != fiber.StatusNoContent {
		t.Fatalf("personal mode lost its own config editing: %d", code)
	}
}

// Segment-wise matching, not prefix. A prefix match on "/api/v1/plugins" would
// also claim "/api/v1/plugins/installed", and on "/api/v1/mcp" the MCP list —
// reads the settings page needs.
func TestReadsBesideAPlatformRouteAreNotClaimedByIt(t *testing.T) {
	reads := []struct{ method, path string }{
		{"GET", "/api/v1/plugins/installed"},
		{"GET", "/api/v1/mcp"},
		{"GET", "/api/v1/admin/dlq"},
		{"GET", "/api/v1/admin/audit"},
		// The one that shares a METHOD with a platform template, so the
		// method check cannot rescue a prefix match. POST /api/v1/mcp adds a
		// server to the deployment template; POST /api/v1/mcp/test asks
		// whether a server the caller described is reachable, which is a
		// workspace-level thing to want to know.
		{"POST", "/api/v1/mcp/test"},
	}
	for _, read := range reads {
		if isPlatformRoute(read.method, read.path) {
			t.Errorf("%s %s was claimed as a deployment route; it is a read a tenant needs",
				read.method, read.path)
		}
	}
}

// Team and Scale add a tenant boundary around the Personal product; they do
// not replace it with a smaller product. These representative Personal-mode
// capabilities must remain workspace routes so a verified workspace member
// can keep using them through workspace RBAC and workspace-scoped storage.
// Only the explicitly deployment-wide routes in platformRoutes may cross to
// the operator control plane.
func TestPersonalCapabilitiesRemainWorkspaceScopedInTeamAndScale(t *testing.T) {
	capabilities := []struct{ method, path string }{
		{"GET", "/api/v1/agents"},
		{"POST", "/api/v1/agents"},
		{"POST", "/api/v1/chat"},
		{"GET", "/api/v1/workspace/config"},
		{"PATCH", "/api/v1/workspace/config"},
		{"GET", "/api/v1/workspace/llm-selection"},
		{"PATCH", "/api/v1/workspace/llm-selection"},
		{"GET", "/api/v1/workspace/providers"},
		{"POST", "/api/v1/workspace/providers/nvidia/model"},
		{"POST", "/api/v1/workspace/providers/nvidia"},
		{"DELETE", "/api/v1/workspace/providers/nvidia"},
		{"GET", "/api/v1/mcp"},
		{"GET", "/api/v1/mcp/own"},
		{"PUT", "/api/v1/mcp/own/weather"},
		{"DELETE", "/api/v1/mcp/own/weather"},
		{"GET", "/api/v1/plugins/installed"},
		{"POST", "/api/v1/plugins/install"},
		{"GET", "/api/v1/skills"},
		{"GET", "/api/v1/knowledge"},
		{"POST", "/api/v1/knowledge"},
		{"GET", "/api/v1/channels"},
		{"GET", "/api/v1/schedule"},
		{"GET", "/api/v1/templates"},
		{"GET", "/api/v1/runs"},
		{"GET", "/api/v1/browser/status"},
		{"GET", "/api/v1/tool-catalog"},
	}
	for _, capability := range capabilities {
		if isPlatformRoute(capability.method, capability.path) {
			t.Errorf("%s %s is a Personal capability but was moved behind deployment administration",
				capability.method, capability.path)
		}
	}
}

func TestTheWritesThatChangeTheDeploymentAreAllClaimed(t *testing.T) {
	writes := []struct{ method, path string }{
		{"POST", "/api/v1/admin/restart"},
		{"POST", "/api/v1/system/updates/upgrade"},
		{"PATCH", "/api/v1/config"},
		{"POST", "/api/v1/registries"},
		{"POST", "/api/v1/mcp"},
		{"PATCH", "/api/v1/mcp/weather"},
		{"DELETE", "/api/v1/mcp/weather"},
	}
	for _, write := range writes {
		if !isPlatformRoute(write.method, write.path) {
			t.Errorf("%s %s writes deployment-wide state and is reachable by a workspace role",
				write.method, write.path)
		}
	}
}

// The method is part of the identity. GET /config and PATCH /config are
// different decisions, and matching on the path alone would make one of them
// wrong whichever way it went.
func TestTheMethodIsPartOfTheMatch(t *testing.T) {
	if isPlatformRoute("GET", "/api/v1/admin/restart") {
		t.Error("a GET matched a POST-only platform route")
	}
	if !isPlatformRoute("POST", "/api/v1/admin/restart") {
		t.Error("the POST it was written for does not match")
	}
}

// The table is the boundary. A route registered with platformMW and missing
// from it would be admitted by the workspace middleware and then refused for
// everyone, including the operator; a table entry with no registration guards
// nothing while reading as though it does.
func TestTheTableAndTheRegistrationsAgree(t *testing.T) {
	fset := token.NewFileSet()
	registered := map[string]bool{}
	inspectRegistration := func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method := strings.ToUpper(sel.Sel.Name)
		switch method {
		case "GET", "POST", "PATCH", "DELETE", "PUT":
		default:
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		usesPlatform := false
		for _, arg := range call.Args[1:] {
			ast.Inspect(arg, func(inner ast.Node) bool {
				if innerSel, ok := inner.(*ast.SelectorExpr); ok && innerSel.Sel.Name == "platformMW" {
					usesPlatform = true
				}
				return true
			})
		}
		if usesPlatform {
			registered[method+" /api/v1"+strings.Trim(lit.Value, `"`)] = true
		}
		return true
	}
	// Platform routes may be grouped into focused registration helpers. Parse
	// every gateway source file so moving a route out of server.go cannot make
	// this boundary audit silently incomplete.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		ast.Inspect(file, inspectRegistration)
	}

	tabled := map[string]bool{}
	for _, route := range platformRoutes {
		tabled[strings.ToUpper(route.Method)+" "+route.Path] = true
	}
	for key := range registered {
		if !tabled[key] {
			t.Errorf("%s is registered with platformMW and missing from platformRoutes, so the "+
				"workspace middleware still demands a membership for it — the operator's own "+
				"credential is refused before platformMW ever runs", key)
		}
	}
	for key := range tabled {
		if !registered[key] {
			t.Errorf("%s is in platformRoutes and not registered with platformMW; the table says "+
				"it is protected and nothing protects it", key)
		}
	}
}

// Every entry has to say what deployment-wide state it writes. "Why" is what
// makes removing one a decision rather than an edit.
func TestEveryPlatformRouteSaysWhatItChanges(t *testing.T) {
	for _, route := range platformRoutes {
		if len(strings.TrimSpace(route.Why)) < 20 {
			t.Errorf("%s %s has no usable reason: %q", route.Method, route.Path, route.Why)
		}
	}
}
