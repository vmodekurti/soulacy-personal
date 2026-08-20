// plugin_scope_test.go — a plugin installed in one workspace does not run in
// another.
//
// WHAT MADE THIS SURVIVE. internal/plugins.Stores — a per-workspace registry
// of plugin loaders, with the layered platform/workspace scan order and the
// shadowing rule — was written for MU-017 criterion 1 and then wired to
// nothing. The inventory was scoped; the execution path held one process-wide
// provider. Reviewing either half alone reads as correct.
//
// The tests go through the engine's own resolver and its tool-schema builder,
// not through Stores. Stores was already right. The question is whether the
// component that decides what an agent may call asks it.
package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

type fakePluginProvider struct{ tools []PluginTool }

func (f *fakePluginProvider) AllTools() []PluginTool { return f.tools }

func pluginNamed(name string) PluginTool {
	return PluginTool{Name: name, Description: "d", Parameters: map[string]any{}, Handler: "python:/tmp/x.py::f"}
}

func TestAPluginInstalledInOneWorkspaceIsNotOfferedToAnother(t *testing.T) {
	e := &Engine{}
	e.SetPluginProviders(func(workspaceID string) PluginToolProvider {
		switch workspaceID {
		case "ws-a":
			return &fakePluginProvider{tools: []PluginTool{pluginNamed("plugin__acme__charge")}}
		case "ws-b":
			return &fakePluginProvider{tools: []PluginTool{pluginNamed("plugin__other__ping")}}
		}
		return nil
	})

	for _, tc := range []struct{ ws, want, absent string }{
		{"ws-a", "plugin__acme__charge", "plugin__other__ping"},
		{"ws-b", "plugin__other__ping", "plugin__acme__charge"},
	} {
		provider := e.plugins(inWorkspace(context.Background(), tc.ws))
		if provider == nil {
			t.Fatalf("%s got no provider at all", tc.ws)
		}
		names := map[string]bool{}
		for _, tool := range provider.AllTools() {
			names[tool.Name] = true
		}
		if !names[tc.want] {
			t.Errorf("%s cannot see its own plugin %s", tc.ws, tc.want)
		}
		if names[tc.absent] {
			t.Errorf("%s can call %s, which only the other workspace installed", tc.ws, tc.absent)
		}
	}
}

func TestAWorkspaceWithNoPluginsDoesNotInheritTheProcessWideOnes(t *testing.T) {
	// The fallback that would be natural to write — "no per-workspace
	// provider, so use the shared one" — hands the deployment's whole plugin
	// surface to precisely the workspaces the resolver could not answer for.
	e := &Engine{pluginProvider: &fakePluginProvider{tools: []PluginTool{pluginNamed("plugin__legacy__run")}}}
	e.SetPluginProviders(func(string) PluginToolProvider { return nil })

	if provider := e.plugins(inWorkspace(context.Background(), "ws-new")); provider != nil {
		t.Errorf("an unresolved workspace fell back to the process-wide provider: %d tools", len(provider.AllTools()))
	}
}

func TestTheProcessWideProviderStillServesASingleTenantInstall(t *testing.T) {
	// Invariant 7. A deployment that never installs a resolver keeps exactly
	// the plugins it has always had, in the personal workspace and everywhere
	// else, with no migration.
	e := &Engine{pluginProvider: &fakePluginProvider{tools: []PluginTool{pluginNamed("plugin__legacy__run")}}}
	provider := e.plugins(context.Background())
	if provider == nil || len(provider.AllTools()) != 1 {
		t.Fatal("a single-tenant install lost its plugins")
	}
}

func TestTheToolSchemaOfferedToTheModelIsScoped(t *testing.T) {
	// The resolver being right is not the same as the schema builder using it.
	// This is the call site that decides what the model is even told exists —
	// a tool absent from the schema is one the model cannot name.
	e := &Engine{}
	e.SetPluginProviders(func(workspaceID string) PluginToolProvider {
		if workspaceID == "ws-a" {
			return &fakePluginProvider{tools: []PluginTool{pluginNamed("plugin__acme__charge")}}
		}
		return &fakePluginProvider{}
	})
	allowAll := []string{"*"}
	def := &agent.Definition{ID: "a", PluginTools: &allowAll}

	for _, tc := range []struct {
		ws   string
		want bool
	}{{"ws-a", true}, {"ws-b", false}} {
		schemas := e.allToolSchemasForContext(inWorkspace(context.Background(), tc.ws), def, "")
		found := false
		for _, schema := range schemas {
			if strings.Contains(schema.Name, "plugin__acme__charge") {
				found = true
			}
		}
		if found != tc.want {
			t.Errorf("%s: acme plugin offered=%v, want %v", tc.ws, found, tc.want)
		}
	}
}
