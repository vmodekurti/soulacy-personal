package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/soulacy/soulacy/internal/mcp"
)

// The behaviour lives in internal/mcp and is tested there. What cannot be
// tested there is that boot turns the requirement ON in multi-user mode — and
// a build where it does not is the original leak: every tenant's MCP server
// started with the operator's token.
//
// Order matters as much as presence. The requirement has to be installed
// before the engine can hand the pool to anyone, or whichever workspace makes
// the first MCP call gets a client built under the old rules and keeps it.
func TestBootRequiresTenantCredentialsBeforeThePoolIsReachable(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wire_subsystems.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var newPool, require, publish token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "NewPool":
			if newPool == token.NoPos {
				newPool = call.Pos()
			}
		case "RequireTenantCredentials":
			if require == token.NoPos {
				require = call.Pos()
			}
		case "SetMCPPool":
			if publish == token.NoPos {
				publish = call.Pos()
			}
		}
		return true
	})
	if newPool == token.NoPos {
		t.Fatal("wire_subsystems.go no longer builds an MCP pool; this guard vouches for nothing")
	}
	if require == token.NoPos {
		t.Fatal("boot never calls RequireTenantCredentials, so every tenant's MCP server runs on " +
			"the operator's token and sees whatever that token sees")
	}
	if require < newPool {
		t.Fatal("the requirement is installed before the pool exists")
	}
	if publish != token.NoPos && require > publish {
		t.Fatal("the pool is published to the engine before the credential requirement is on; the " +
			"first workspace to call an MCP tool gets a client built under the old rules")
	}
}

// The resolver must never answer with the operator's value. A vault that is
// missing or broken has to mean "not supplied", which withholds the server.
func TestAnAbsentVaultWithholdsRatherThanFallsBack(t *testing.T) {
	resolver := vaultMCPCredentials(nil)
	if value, ok := resolver.ServerSecret("ws_a", "github", "GITHUB_TOKEN"); ok || value != "" {
		t.Fatalf("an absent vault answered %q/%v; the server would start as the operator", value, ok)
	}
}

// The writer and the reader must agree on where a tenant's token is filed. Two
// spellings is a token that saves successfully and is never found.
func TestTheCredentialNamespaceCannotCollideWithAnAgent(t *testing.T) {
	ns := mcp.CredentialNamespace("github")
	if ns == "github" {
		t.Fatal("an MCP server shares a vault namespace with an agent of the same name")
	}
	if got := mcp.CredentialNamespace(" github "); got != ns {
		t.Errorf("whitespace produced a second namespace: %q vs %q", got, ns)
	}
}

// Same guard as the MCP one, for the plugin half. A build where boot never
// turns the requirement on is the original leak: a credential in the shared
// plugins_config reaching every tenant.
//
// Order matters. SetSettings and the first For build loaders, and a loader
// built before the requirement is on holds the operator's value.
func TestBootRequiresTenantSettingsBeforeAnyLoaderIsBuilt(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wire_subsystems.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var newStores, require, setSettings token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "NewStores":
			if newStores == token.NoPos {
				newStores = call.Pos()
			}
		case "RequireTenantSettings":
			if require == token.NoPos {
				require = call.Pos()
			}
		case "SetSettings":
			if setSettings == token.NoPos {
				setSettings = call.Pos()
			}
		}
		return true
	})
	if newStores == token.NoPos || setSettings == token.NoPos {
		t.Fatal("wire_subsystems.go no longer builds the plugin stores; this guard vouches for nothing")
	}
	if require == token.NoPos {
		t.Fatal("boot never calls RequireTenantSettings, so a credential in the shared " +
			"plugins_config reaches every workspace")
	}
	if require < newStores {
		t.Fatal("the requirement is installed before the stores exist")
	}
	if require > setSettings {
		t.Fatal("settings are installed before the requirement is on, so the loaders built by it " +
			"hold the operator's values")
	}
}

// The resolver must never answer with the operator's value.
func TestAnAbsentVaultWithholdsPluginSettingsToo(t *testing.T) {
	resolver := vaultPluginSettings(nil)
	if value, ok := resolver.PluginSecret("ws_a", "matrix", "api_key"); ok || value != "" {
		t.Fatalf("an absent vault answered %q/%v; the plugin would run as the operator", value, ok)
	}
}
