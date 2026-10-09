package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func readyWebsiteRoutingEngine(t *testing.T) (*Engine, *agent.Definition) {
	t.Helper()
	store, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	vaultKey, err := credentials.NewLocalKMS()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewSQLiteVault(filepath.Join(t.TempDir(), "vault.db"), vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	connection, err := store.Create(t.Context(), authconnections.CreateInput{
		ID: "conn_notebook", WorkspaceID: PersonalWorkspaceID, OwnerSubject: "admin", Scope: authconnections.ScopeUser,
		Kind: authconnections.KindBrowser, Name: "NotebookLM", BaseURL: "https://notebooklm.google.com", AllowedDomains: []string{"notebooklm.google.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.WriteBlob(t.Context(), "authenticated_connection_"+connection.ID, "browser_storage_state", []byte(`{"cookies":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSecret(t.Context(), PersonalWorkspaceID, connection.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgentGrants(t.Context(), PersonalWorkspaceID, connection.ID, []string{"podcast"}); err != nil {
		t.Fatal(err)
	}

	e := newMinimalEngine(t)
	e.builtins = e.buildBuiltins()
	e.SetAuthenticatedConnectionResolver(authconnections.NewResolver(store, vault))
	connections := []string{connection.ID}
	builtins := []string{}
	def := &agent.Definition{ID: "podcast", Name: "Podcast", Connections: connections, Builtins: &builtins}
	return e, def
}

func TestReadyWebsiteAccessAddsSafeManagedBrowserTools(t *testing.T) {
	e, def := readyWebsiteRoutingEngine(t)
	names := toolSchemaNameSet(e.allToolSchemasForContext(t.Context(), def, "internal"))
	for _, name := range []string{"start_website_action", "inspect_website_action", "act_on_website", "close_website_action"} {
		if !names[name] {
			t.Errorf("ready Website Access did not expose %s", name)
		}
	}
	if names["commit_website_action"] {
		t.Fatal("scheduled agent received the final-action commit tool")
	}
}

func TestWebsiteAccessRoutingGuideUsesSecretFreeMetadata(t *testing.T) {
	e, def := readyWebsiteRoutingEngine(t)
	guide := e.websiteAccessRoutingGuide(t.Context(), def)
	for _, want := range []string{"NotebookLM", "conn_notebook", "notebooklm.google.com", "outside model context"} {
		if !strings.Contains(guide, want) {
			t.Fatalf("routing guide missing %q: %s", want, guide)
		}
	}
	if strings.Contains(strings.ToLower(guide), "cookie\"") {
		t.Fatalf("routing guide exposed browser state: %s", guide)
	}
}

func TestUnauthenticatedMCPTriggersWebsiteAccessFallback(t *testing.T) {
	results := []message.ToolResult{{Name: "mcp__notebooklm__get_health", Content: `<external_content>{"authenticated": false}</external_content>`}}
	if got := websiteAccessFallbackNudge(results, true, false); !strings.Contains(got, "Do not retry") || !strings.Contains(got, "start_website_action") {
		t.Fatalf("scheduled fallback = %q", got)
	}
	if got := websiteAccessFallbackNudge(results, false, true); !strings.Contains(got, "prepare_website_access") {
		t.Fatalf("Genie fallback = %q", got)
	}
}
