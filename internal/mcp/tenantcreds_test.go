package mcp

import (
	"testing"

	"github.com/soulacy/soulacy/internal/sandbox"
)

func poolWithTemplate(t *testing.T, servers map[string]ServerConfig) *Pool {
	t.Helper()
	confine := ConfinementFunc(func(workspaceID string) (string, sandbox.Limits, string, error) {
		return t.TempDir(), sandbox.Limits{}, "", nil
	})
	return NewPool(Config{Servers: servers}, confine, nil)
}

func effective(t *testing.T, p *Pool, workspaceID string) map[string]ServerConfig {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.configFor(workspaceID).Servers
}

// THE LEAK. The pool gave every workspace its own process and started each one
// with the OPERATOR's token, so tenant A's agent called the upstream service as
// the operator — and saw everything tenant B had put there.
func TestATenantNeverInheritsTheOperatorsCredential(t *testing.T) {
	p := poolWithTemplate(t, map[string]ServerConfig{
		"github": {Command: "gh-mcp", Env: map[string]string{"GITHUB_TOKEN": "operator-secret"}},
	})
	p.RequireTenantCredentials(nil)

	servers := effective(t, p, "ws_tenant")
	if sc, started := servers["github"]; started {
		t.Fatalf("a credentialed server started for a tenant that supplied nothing; its env is %v — "+
			"every call it makes is the operator's identity", sc.Env)
	}
	withheld := p.Withheld("ws_tenant")
	if len(withheld) != 1 || withheld[0].ServerID != "github" {
		t.Fatalf("withheld = %+v; the tenant is shown a missing tool instead of a setup step", withheld)
	}
	if len(withheld[0].Missing) != 1 || withheld[0].Missing[0] != "GITHUB_TOKEN" {
		t.Errorf("missing = %v, want the key they have to supply", withheld[0].Missing)
	}
}

// The tenant's own value is used, and nothing of the operator's survives.
func TestTheTenantsOwnCredentialIsTheOneThatRuns(t *testing.T) {
	p := poolWithTemplate(t, map[string]ServerConfig{
		"github": {Command: "gh-mcp", Env: map[string]string{"GITHUB_TOKEN": "operator-secret"}},
	})
	p.RequireTenantCredentials(CredentialResolverFunc(
		func(workspaceID, serverID, key string) (string, bool) {
			if workspaceID == "ws_a" && serverID == "github" && key == "GITHUB_TOKEN" {
				return "tenant-a-token", true
			}
			return "", false
		}))

	a := effective(t, p, "ws_a")["github"]
	if a.Env["GITHUB_TOKEN"] != "tenant-a-token" {
		t.Fatalf("token = %q, want the tenant's own", a.Env["GITHUB_TOKEN"])
	}
	if b, started := effective(t, p, "ws_b")["github"]; started {
		t.Fatalf("a workspace that supplied nothing still started the server: %v", b.Env)
	}
}

// Configuration the operator meant to share is not a credential. Withholding a
// server because it sets LOG_LEVEL would make the feature unusable and teach
// operators to turn the control off.
func TestNonSecretTemplateSettingsStillTravel(t *testing.T) {
	p := poolWithTemplate(t, map[string]ServerConfig{
		"docs": {Command: "docs-mcp", Env: map[string]string{"LOG_LEVEL": "debug", "DOCS_ROOT": "/srv/docs"}},
	})
	p.RequireTenantCredentials(nil)

	sc, started := effective(t, p, "ws_a")["docs"]
	if !started {
		t.Fatal("a server with no credentials at all was withheld")
	}
	if sc.Env["LOG_LEVEL"] != "debug" || sc.Env["DOCS_ROOT"] != "/srv/docs" {
		t.Errorf("operator configuration was stripped: %v", sc.Env)
	}
}

// An empty template value is a placeholder telling the tenant what to fill in,
// not a secret being shared. Passing it through as "" would start the server
// unauthenticated and produce a confusing upstream error instead of a clear
// setup step.
func TestAnEmptyPlaceholderIsStillRequired(t *testing.T) {
	p := poolWithTemplate(t, map[string]ServerConfig{
		"github": {Command: "gh-mcp", Env: map[string]string{"GITHUB_TOKEN": ""}},
	})
	p.RequireTenantCredentials(nil)
	if _, started := effective(t, p, "ws_a")["github"]; started {
		t.Fatal("a server whose token placeholder is empty started anyway")
	}
}

// HTTP servers carry their identity in a header rather than the environment,
// and the header is the same leak.
func TestAuthorizationHeadersAreTenantScopedToo(t *testing.T) {
	p := poolWithTemplate(t, map[string]ServerConfig{
		"api": {Transport: "http", URL: "https://x", Headers: map[string]string{
			"Authorization": "Bearer operator-key", "Accept": "application/json",
		}},
	})
	p.RequireTenantCredentials(CredentialResolverFunc(
		func(workspaceID, serverID, key string) (string, bool) {
			if workspaceID == "ws_a" {
				return "Bearer tenant-a", true
			}
			return "", false
		}))

	a := effective(t, p, "ws_a")["api"]
	if a.Headers["Authorization"] != "Bearer tenant-a" {
		t.Fatalf("authorization = %q", a.Headers["Authorization"])
	}
	if a.Headers["Accept"] != "application/json" {
		t.Errorf("a non-secret header was stripped: %v", a.Headers)
	}
	if _, started := effective(t, p, "ws_b")["api"]; started {
		t.Fatal("a workspace with no header of its own still got the operator's")
	}
}

// The escape hatch, and the fact that it has to be asked for.
func TestSharedCredentialsAreOnlyEverAnExplicitChoice(t *testing.T) {
	p := poolWithTemplate(t, map[string]ServerConfig{
		"licence": {Command: "lic-mcp", SharedCredentials: true,
			Env: map[string]string{"LICENCE_KEY": "site-wide"}},
	})
	p.RequireTenantCredentials(nil)

	sc, started := effective(t, p, "ws_a")["licence"]
	if !started || sc.Env["LICENCE_KEY"] != "site-wide" {
		t.Fatalf("the opt-in did not share: started=%v env=%v", started, sc.Env)
	}
	if len(p.Withheld("ws_a")) != 0 {
		t.Error("an opted-in server was reported as withheld")
	}
}

// Personal installations have one tenant whose credentials genuinely are the
// operator's. Invariant 7: without the requirement turned on, nothing changes.
func TestWithoutTheRequirementNothingChanges(t *testing.T) {
	p := poolWithTemplate(t, map[string]ServerConfig{
		"github": {Command: "gh-mcp", Env: map[string]string{"GITHUB_TOKEN": "operator-secret"}},
	})
	sc, started := effective(t, p, "ws_personal")["github"]
	if !started || sc.Env["GITHUB_TOKEN"] != "operator-secret" {
		t.Fatalf("a single-user installation lost its own MCP credentials: started=%v env=%v",
			started, sc.Env)
	}
}

// A workspace's own server carries the workspace's own values already.
// Demanding it supply credentials to itself would make the feature circular.
func TestAWorkspacesOwnServerIsNotAskedToAuthenticateToItself(t *testing.T) {
	p := poolWithTemplate(t, nil)
	p.RequireTenantCredentials(nil)
	if err := p.AddServer("ws_a", "mine", ServerConfig{
		Command: "mine-mcp", Env: map[string]string{"API_KEY": "tenant-a-own"},
	}); err != nil {
		t.Fatal(err)
	}
	sc, started := effective(t, p, "ws_a")["mine"]
	if !started || sc.Env["API_KEY"] != "tenant-a-own" {
		t.Fatalf("the workspace's own server was withheld: started=%v env=%v", started, sc.Env)
	}
}

// Supplying the missing credential has to take effect. Without invalidation
// the tenant sets their token, sees nothing change, and concludes it is broken.
func TestSupplyingTheCredentialTakesEffect(t *testing.T) {
	supplied := false
	p := poolWithTemplate(t, map[string]ServerConfig{
		"github": {Command: "gh-mcp", Env: map[string]string{"GITHUB_TOKEN": "operator-secret"}},
	})
	p.RequireTenantCredentials(CredentialResolverFunc(
		func(workspaceID, serverID, key string) (string, bool) {
			if supplied {
				return "tenant-a-token", true
			}
			return "", false
		}))

	if _, started := effective(t, p, "ws_a")["github"]; started {
		t.Fatal("started before the credential was supplied")
	}
	supplied = true
	p.InvalidateWorkspace("ws_a")
	sc, started := effective(t, p, "ws_a")["github"]
	if !started || sc.Env["GITHUB_TOKEN"] != "tenant-a-token" {
		t.Fatalf("after supplying the credential: started=%v env=%v", started, sc.Env)
	}
	if len(p.Withheld("ws_a")) != 0 {
		t.Error("still reported as withheld after the credential was supplied")
	}
}

// A workspace's own servers come from a durable store now, not only from the
// in-memory overrides that were lost on the next restart.
func TestAWorkspacesStoredServersAreStarted(t *testing.T) {
	p := poolWithTemplate(t, nil)
	p.RequireTenantCredentials(nil)
	p.SetServerStore(ServerStoreFunc(func(workspaceID string) map[string]ServerConfig {
		if workspaceID != "ws_a" {
			return nil
		}
		return map[string]ServerConfig{"mine": {Command: "mine-mcp",
			Env: map[string]string{"API_KEY": "tenant-a-own"}}}
	}))

	sc, started := effective(t, p, "ws_a")["mine"]
	if !started {
		t.Fatal("a workspace's stored server was not started")
	}
	if sc.Env["API_KEY"] != "tenant-a-own" {
		t.Errorf("the workspace's own value was replaced: %v", sc.Env)
	}
	if _, leaked := effective(t, p, "ws_b")["mine"]; leaked {
		t.Fatal("one workspace's stored server was started for another")
	}
}

// A server added through the API in this run wins over the row it was written
// from. They are normally identical; when they differ it is because the write
// failed, and the tenant's most recent intent beats a stale row.
func TestAnInProcessAdditionWinsOverAStaleRow(t *testing.T) {
	p := poolWithTemplate(t, nil)
	p.SetServerStore(ServerStoreFunc(func(string) map[string]ServerConfig {
		return map[string]ServerConfig{"mine": {Command: "stale"}}
	}))
	if err := p.AddServer("ws_a", "mine", ServerConfig{Command: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if sc := effective(t, p, "ws_a")["mine"]; sc.Command != "fresh" {
		t.Fatalf("command = %q, want the value just added", sc.Command)
	}
}
