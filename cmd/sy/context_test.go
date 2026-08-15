package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempWorkspace points the context file at a scratch directory so tests
// never read or clobber the developer's real contexts.
func withTempWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SOULACY_WORKSPACE", "")
	t.Setenv(EnvContext, "")
	return dir
}

func TestRootExposesContextCommands(t *testing.T) {
	root := buildRoot()
	for _, path := range [][]string{
		{"context", "add"}, {"context", "list"}, {"context", "use"},
		{"context", "show"}, {"context", "delete"},
		{"login"}, {"logout"}, {"whoami"},
		{"workspace", "list"}, {"workspace", "use"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil || cmd == nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("missing sy %s: %v", strings.Join(path, " "), err)
		}
	}
	if root.PersistentFlags().Lookup("workspace") == nil {
		t.Fatal("the global --workspace selector is missing")
	}
}

// The context file is the one piece of CLI state that lands on disk in the
// clear. If a token ever leaks into it, every backup, sync client, and repo
// checkout that touches it becomes a credential store.
func TestContextFileNeverHoldsSecrets(t *testing.T) {
	withTempWorkspace(t)
	file := contextFile{
		Current: "prod",
		Contexts: map[string]syContext{"prod": {
			Name: "prod", Server: "https://soulacy.example.com",
			OrganizationID: "org_acme", WorkspaceID: "ws_prod",
			Subject: "usr_alice", PrincipalKind: "user", Role: "developer",
		}},
	}
	if err := saveContexts(file); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(contextFilePath())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	lowered := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"access_token", "refresh_token", "api_key", "apikey",
		"secret", "password", "bearer", "sk_", "token",
	} {
		if strings.Contains(lowered, forbidden) {
			t.Errorf("context file contains a credential-bearing field %q:\n%s", forbidden, raw)
		}
	}

	// The struct itself must not grow one either, since a future field would
	// only show up in the serialized form once someone populated it.
	encoded, err := json.Marshal(syContext{})
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	for key := range shape {
		lowerKey := strings.ToLower(key)
		for _, forbidden := range []string{"token", "secret", "key", "password"} {
			if strings.Contains(lowerKey, forbidden) {
				t.Errorf("syContext declares a credential-bearing field %q", key)
			}
		}
	}
}

func TestContextFileIsNotWorldReadable(t *testing.T) {
	withTempWorkspace(t)
	if err := saveContexts(contextFile{Current: "local", Contexts: map[string]syContext{
		"local": {Name: "local", Server: "http://localhost:18789"},
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(contextFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&fs.FileMode(0o077) != 0 {
		t.Fatalf("context file mode is %o, want no group or world access", mode)
	}
}

func TestContextRoundTripAndCurrentSelection(t *testing.T) {
	withTempWorkspace(t)
	first := contextFile{Contexts: map[string]syContext{}}
	first.Contexts["staging"] = syContext{Name: "staging", Server: "https://staging.example.com"}
	first.Current = "staging"
	if err := saveContexts(first); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadContexts()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Current != "staging" || loaded.Contexts["staging"].Server != "https://staging.example.com" {
		t.Fatalf("context did not round-trip: %+v", loaded)
	}
}

// A missing context file is an ordinary state — Personal mode never creates
// one — so it must load as empty rather than as an error.
func TestLoadContextsTreatsAMissingFileAsEmpty(t *testing.T) {
	withTempWorkspace(t)
	loaded, err := loadContexts()
	if err != nil {
		t.Fatalf("missing context file reported an error: %v", err)
	}
	if len(loaded.Contexts) != 0 || loaded.Current != "" {
		t.Fatalf("missing context file produced %+v", loaded)
	}
}

// CI selects a target through the environment so that concurrent jobs sharing
// a checkout cannot race each other by writing the current context.
func TestEnvironmentContextOverridesStoredCurrent(t *testing.T) {
	withTempWorkspace(t)
	if err := saveContexts(contextFile{
		Current: "staging",
		Contexts: map[string]syContext{
			"staging": {Name: "staging", Server: "https://staging.example.com", WorkspaceID: "ws_staging"},
			"prod":    {Name: "prod", Server: "https://prod.example.com", WorkspaceID: "ws_prod"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if ctx, ok := activeContext(); !ok || ctx.Name != "staging" {
		t.Fatalf("stored current context not honoured: %+v", ctx)
	}
	t.Setenv(EnvContext, "prod")
	ctx, ok := activeContext()
	if !ok || ctx.Name != "prod" || ctx.WorkspaceID != "ws_prod" {
		t.Fatalf("%s did not override the stored current context: %+v", EnvContext, ctx)
	}
	// Selecting a context must not mutate shared state on disk.
	stored, err := loadContexts()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Current != "staging" {
		t.Fatalf("environment selection rewrote the stored current context to %q", stored.Current)
	}
}

// An explicit flag is the operator's direct instruction and outranks any
// stored default; the stored default outranks the loopback fallback.
func TestApplyActiveContextDoesNotOverrideExplicitFlags(t *testing.T) {
	withTempWorkspace(t)
	if err := saveContexts(contextFile{
		Current:  "prod",
		Contexts: map[string]syContext{"prod": {Name: "prod", Server: "https://prod.example.com", WorkspaceID: "ws_prod"}},
	}); err != nil {
		t.Fatal(err)
	}
	originalGateway, originalWorkspace := gatewayURL, activeWorkspaceID
	t.Cleanup(func() { gatewayURL, activeWorkspaceID = originalGateway, originalWorkspace })

	gatewayURL, activeWorkspaceID = "", ""
	applyActiveContext()
	if gatewayURL != "https://prod.example.com" || activeWorkspaceID != "ws_prod" {
		t.Fatalf("stored context was not applied: %q %q", gatewayURL, activeWorkspaceID)
	}

	gatewayURL, activeWorkspaceID = "http://localhost:18789", "ws_explicit"
	applyActiveContext()
	if gatewayURL != "http://localhost:18789" || activeWorkspaceID != "ws_explicit" {
		t.Fatalf("stored context overrode explicit flags: %q %q", gatewayURL, activeWorkspaceID)
	}
}

// Local mode must keep working with no context, no login, and no keychain.
func TestNoContextLeavesLoopbackDefaultsIntact(t *testing.T) {
	withTempWorkspace(t)
	originalGateway, originalWorkspace := gatewayURL, activeWorkspaceID
	t.Cleanup(func() { gatewayURL, activeWorkspaceID = originalGateway, originalWorkspace })
	gatewayURL, activeWorkspaceID = "", ""
	applyActiveContext()
	if gatewayURL != "" || activeWorkspaceID != "" {
		t.Fatalf("an absent context invented a target: %q %q", gatewayURL, activeWorkspaceID)
	}
}

func TestDescribePrincipalNamesTheActingIdentity(t *testing.T) {
	cases := map[string]struct {
		identity identityView
		contains []string
	}{
		"service account":       {identityView{Subject: "svc_ci", PrincipalKind: "service_account"}, []string{"service-account", "svc_ci"}},
		"personal access token": {identityView{Subject: "usr_a", PrincipalKind: "personal_access_token"}, []string{"usr_a", "personal access token"}},
		"static key":            {identityView{Subject: "api-key", PrincipalKind: "static_api_key"}, []string{"static server key"}},
		"unauthenticated":       {identityView{}, []string{"unauthenticated"}},
	}
	for name, tc := range cases {
		got := describePrincipal(tc.identity)
		for _, want := range tc.contains {
			if !strings.Contains(got, want) {
				t.Errorf("%s: describePrincipal = %q, want it to contain %q", name, got, want)
			}
		}
	}
}

func TestContextDirectoryIsCreatedPrivately(t *testing.T) {
	withTempWorkspace(t)
	if err := saveContexts(contextFile{Contexts: map[string]syContext{}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(contextFilePath()))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&fs.FileMode(0o077) != 0 {
		t.Fatalf("context directory mode is %o, want no group or world access", mode)
	}
}
