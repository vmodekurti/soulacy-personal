package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/valyala/fasthttp"

	"github.com/soulacy/soulacy/internal/auth"

	"github.com/soulacy/soulacy/internal/config"
)

// A tenant restarting the deployment for every other tenant.
//
// The endpoint calls os.Exit(0) and is gated on config:write, which the
// workspace `owner` and `admin` roles hold. Those are tenant roles — the
// person who runs a workspace, not the person who runs the machine.
func TestATenantCannotRestartTheWholeDeployment(t *testing.T) {
	for _, mode := range []string{config.DeploymentModeTeam, config.DeploymentModeScale} {
		s := &Server{}
		s.setConfig(&config.Config{Deployment: config.DeploymentConfig{Mode: mode}})
		refusal := s.restartRefusal(claimsCtx(t, "cred_alice"))
		if refusal == "" {
			t.Errorf("%s mode allows a workspace admin to call os.Exit(0) on every other "+
				"workspace's runs", mode)
			continue
		}
		if !strings.Contains(strings.ToLower(refusal), "host") {
			t.Errorf("%s refusal must say where to restart instead; got %q", mode, refusal)
		}
	}
}

// Personal is one person restarting their own gateway. Invariant 7: an
// existing single-user installation must not notice any of this.
func TestPersonalInstallationsCanStillRestart(t *testing.T) {
	for _, mode := range []string{config.DeploymentModePersonal, ""} {
		s := &Server{}
		s.setConfig(&config.Config{Deployment: config.DeploymentConfig{Mode: mode}})
		if refusal := s.restartRefusal(claimsCtx(t, "cred_alice")); refusal != "" {
			t.Errorf("personal mode %q lost the restart button: %s", mode, refusal)
		}
	}
}

// The operator must be able to restart their own deployment; otherwise the
// endpoint is dead weight and they learn to ignore the product's own controls.
func TestTheOperatorCanStillRestartTheDeployment(t *testing.T) {
	for _, mode := range []string{config.DeploymentModeTeam, config.DeploymentModeScale} {
		s := &Server{}
		s.setConfig(&config.Config{Deployment: config.DeploymentConfig{Mode: mode}})
		if refusal := s.restartRefusal(claimsCtx(t, staticAPIKeyCredentialID)); refusal != "" {
			t.Errorf("%s: the deployment's own credential was refused a restart: %s", mode, refusal)
		}
	}
}

// The refusal above is worth nothing if the handler does not consult it, and
// the handler cannot be driven to completion in a test because it ends in
// os.Exit(0). So the wiring is read.
func TestTheRestartHandlerConsultsTheRefusal(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "api.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var restart *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "handleRestart" {
			restart = fn
		}
	}
	if restart == nil {
		t.Fatal("handleRestart is gone; this guard vouches for nothing")
	}
	if findCallIn(restart.Body, "restartRefusal") == nil {
		t.Error("handleRestart never asks whether it is allowed to restart, so every mode does")
	}
	if findCallIn(restart.Body, "startRestartChild") == nil {
		t.Error("the personal restart path no longer spawns a replacement process")
	}
	// The refusal must come FIRST. A check after the child is spawned refuses
	// a restart that has already happened.
	refusalPos := findCallIn(restart.Body, "restartRefusal").Pos()
	spawnPos := findCallIn(restart.Body, "startRestartChild").Pos()
	if refusalPos > spawnPos {
		t.Error("the mode check runs after the replacement process is spawned")
	}
}

func findCallIn(body *ast.BlockStmt, name string) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found != nil {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == name {
				found = call
			}
		case *ast.SelectorExpr:
			if fn.Sel.Name == name {
				found = call
			}
		}
		return true
	})
	return found
}

// claimsCtx builds a request context carrying one credential ID, which is all
// platformPrincipal reads.
func claimsCtx(t *testing.T, credentialID string) *fiber.Ctx {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
	t.Cleanup(func() { app.ReleaseCtx(ctx) })
	auth.SetClaims(ctx, &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "usr_alice"},
		Role:             "owner", Kind: "access", PrincipalKind: "user",
		CredentialID: credentialID,
	})
	return ctx
}

// The prober problem, as a test. A bare POST from anything that walks the
// route table used to restart the deployment; in multi-user it now has to say
// so explicitly.
func TestABareRestartPostDoesNothingInMultiUser(t *testing.T) {
	for _, mode := range []string{config.DeploymentModeTeam, config.DeploymentModeScale} {
		s := newTestGateway(t, "secret")
		s.mutateConfig(func(c *config.Config) { c.Deployment.Mode = mode })
		req := httptest.NewRequest("POST", "/api/v1/admin/restart", nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := s.app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("%s: a bare POST got %d; a route prober or security scanner would have "+
				"restarted the deployment", mode, resp.StatusCode)
		}
	}
}
