package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/workspacesettings"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestBootValidationKeepsWorkspaceOwnedProviderAgentEnabled(t *testing.T) {
	providerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"workspace-model"}]}`))
	}))
	t.Cleanup(providerAPI.Close)

	s := newTestGateway(t, "secret")
	store, err := workspacesettings.NewStore(filepath.Join(t.TempDir(), "workspace-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetWorkspaceSettingsStore(store)
	vault := newMemVault()
	s.SetCredentialVault(vault)

	const workspaceID = "ws_workspace_provider"
	if _, err := store.Set(context.Background(), workspaceID, "owner", workspacesettings.Settings{
		LLM: workspacesettings.LLM{Providers: map[string]workspacesettings.Provider{
			"ollama-cloud": {BaseURL: providerAPI.URL, Model: "workspace-model"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(context.Background(), workspaceID, workspacesettings.SecretNamespace,
		workspacesettings.ProviderAPIKey("ollama-cloud"), []byte("workspace-key")); err != nil {
		t.Fatal(err)
	}
	builtins := []string{"web_search"}
	s.loader.RegisterInWorkspace(workspaceID, &agent.Definition{
		ID:           "weather-advice-agent",
		Name:         "Weather Advice Agent",
		Enabled:      true,
		SystemPrompt: "Use tools when needed.",
		LLM:          agent.LLMConfig{Provider: "ollama-cloud", Model: "workspace-model"},
		Builtins:     &builtins,
	})
	settings, err := store.Get(context.Background(), workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	opts := s.agentValidationOptionsForWorkspace(context.Background(), workspaceID, settings)
	if len(opts.ProviderModels["ollama-cloud"]) == 0 {
		t.Fatalf("workspace provider was not available to validation: providers=%v models=%v", opts.RegisteredProviders, opts.ProviderModels)
	}

	s.validateAgentsAtBoot(context.Background())
	got := s.loader.GetInWorkspace(workspaceID, "weather-advice-agent")
	if got == nil || !got.Enabled {
		t.Fatalf("workspace-provider agent was disabled at boot: %+v", got)
	}
}
