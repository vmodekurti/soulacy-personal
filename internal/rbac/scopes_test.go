package rbac

// A scope that is displayed but not enforced is worse than no scope at all,
// because the operator is told the credential is limited and believes it.
//
// Managed API keys always STORED scopes — the key store persists them, the
// create endpoint echoes them back, and the pairing flow describes what it
// mints as "a scoped mobile credential". Nothing read them. Every sk_ key
// authenticated as Role "operator", so a phone paired with
// scopes [chat, memory, config] could also write agents, write MCP servers and
// delete knowledge.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
)

func TestScopedCredentialCannotReachAnUnscopedResource(t *testing.T) {
	phone := &auth.Claims{Role: "operator", Scopes: []string{"chat", "memory", "config"}}

	if phone.AllowsResource("agents") {
		t.Error("a phone paired for chat can write agents")
	}
	if phone.AllowsResource("mcp") {
		t.Error("a phone paired for chat can install MCP servers")
	}
	if phone.AllowsResource("knowledge") {
		t.Error("a phone paired for chat can delete the knowledge base")
	}
}

func TestScopedCredentialStillReachesItsOwnScopes(t *testing.T) {
	phone := &auth.Claims{Role: "operator", Scopes: []string{"chat", "memory"}}
	for _, r := range []string{"chat", "memory"} {
		if !phone.AllowsResource(r) {
			t.Errorf("the credential cannot reach %q, which it was minted for", r)
		}
	}
}

func TestLegacyMobileChatScopeCanReadButNotManageAgentDirectory(t *testing.T) {
	phone := &auth.Claims{Role: "operator", Scopes: []string{"chat", "memory", "config"}}

	if !phone.Allows(ResourceAgents, ActionRead) {
		t.Fatal("an already-paired phone cannot read the agent directory needed for chat")
	}
	for _, action := range []string{ActionWrite, ActionDelete, ActionEnable} {
		if phone.Allows(ResourceAgents, action) {
			t.Fatalf("legacy chat scope unexpectedly allows agents:%s", action)
		}
	}
}

// Everything that existed before this change has no scopes, and must keep
// working exactly as it did — a JWT, the static admin key, an older API key.
func TestUnscopedCredentialsAreUnrestricted(t *testing.T) {
	for name, cl := range map[string]*auth.Claims{
		"jwt with no scopes": {Role: "admin"},
		"empty scope list":   {Role: "operator", Scopes: []string{}},
		"nil claims":         nil,
	} {
		if !cl.AllowsResource("agents") {
			t.Errorf("%s was narrowed by a scope it does not have", name)
		}
	}
}

// Scopes narrow; they never widen. A viewer holding a scope for a resource its
// role cannot touch is still denied.
func TestScopesCannotGrantWhatTheRoleDenies(t *testing.T) {
	viewer := &auth.Claims{Role: "viewer", Scopes: []string{"config"}}

	if viewer.AllowsResource("config") != true {
		t.Fatal("precondition: the scope itself permits config")
	}
	// The role is what actually decides — viewer has no config permission.
	if HasPermission(viewer.Role, ResourceConfig, ActionWrite) {
		t.Error("viewer gained config:write; scopes must not widen a role")
	}
}

func TestScopeMatchingIgnoresCaseAndPadding(t *testing.T) {
	cl := &auth.Claims{Role: "operator", Scopes: []string{" Chat ", "MEMORY"}}
	if !cl.AllowsResource("chat") || !cl.AllowsResource("memory") {
		t.Error("a scope written with different case or stray spaces stopped working")
	}
}

// The helper being correct proves nothing if the middleware never calls it.
// Mutation testing caught exactly that: removing AllowsResource from Require
// left every test above still passing.
func TestRequire_ActuallyEnforcesScopes(t *testing.T) {
	m := NewManager(nil, zap.NewNop())

	serve := func(cl *auth.Claims, resource, action string) int {
		app := fiber.New()
		app.Get("/x", func(c *fiber.Ctx) error {
			auth.SetClaims(c, cl)
			return c.Next()
		}, m.Require(resource, action), func(c *fiber.Ctx) error {
			return c.SendString("reached")
		})
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		return resp.StatusCode
	}

	phone := &auth.Claims{Role: "operator", Scopes: []string{"chat", "memory"}}

	if got := serve(phone, ResourceAgents, ActionWrite); got != fiber.StatusForbidden {
		t.Errorf("a chat-scoped credential reached agents:write (status %d) — "+
			"the scope is stored and displayed but not enforced", got)
	}
	if got := serve(phone, ResourceChat, ActionChat); got != fiber.StatusOK {
		t.Errorf("the credential was denied its own scope (status %d)", got)
	}

	// An unscoped credential behaves exactly as before this change.
	admin := &auth.Claims{Role: "admin"}
	if got := serve(admin, ResourceAgents, ActionWrite); got != fiber.StatusOK {
		t.Errorf("an unscoped admin was narrowed (status %d)", got)
	}
}

// The per-agent gate consults the ROLE via the grant store and knows nothing
// about the credential the request arrived on, so it needs the scope check of
// its own that RequireAgent now carries.
func TestRequireAgent_ActuallyEnforcesScopes(t *testing.T) {
	m := NewManager(nil, zap.NewNop())
	app := fiber.New()
	app.Get("/agents/:id", func(c *fiber.Ctx) error {
		auth.SetClaims(c, &auth.Claims{Role: "operator", Scopes: []string{"chat"}})
		return c.Next()
	}, m.RequireAgent("id", ActionWrite), func(c *fiber.Ctx) error {
		return c.SendString("reached")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/agents/billing", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Errorf("a chat-scoped credential reached a per-agent route (status %d)", resp.StatusCode)
	}
}
