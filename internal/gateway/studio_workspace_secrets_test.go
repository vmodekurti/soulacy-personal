package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/workspacesettings"
)

func TestStudioPreflightSeesWorkspaceProviderCredential(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.mutateConfig(func(cfg *config.Config) {
		cfg.Deployment.Mode = config.DeploymentModeTeam
		cfg.LLM.Providers = map[string]config.ProviderConfig{}
	})

	store, err := workspacesettings.NewStore(filepath.Join(t.TempDir(), "workspace-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetWorkspaceSettingsStore(store)
	vault := newMemVault()
	s.SetCredentialVault(vault)

	const workspaceID = "ws_preflight_provider"
	if _, err := store.Set(context.Background(), workspaceID, "owner", workspacesettings.Settings{
		LLM: workspacesettings.LLM{Providers: map[string]workspacesettings.Provider{
			"nvidia": {
				BaseURL: "https://integrate.api.nvidia.com/v1",
				Model:   "nvidia/nemotron-3-nano-30b-a3b",
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(context.Background(), workspaceID, workspacesettings.SecretNamespace,
		workspacesettings.ProviderAPIKey("nvidia"), []byte("nvapi-test")); err != nil {
		t.Fatal(err)
	}

	app := appAsWorkspace(t, s, workspaceID, func(app *fiber.App) {
		app.Get("/preflight-secrets", func(c *fiber.Ctx) error {
			in := s.preflightInput(c, studio.Catalog{})
			return c.JSON(in.SecretsSet)
		})
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/preflight-secrets", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	name := workspacesettings.ProviderAPIKey("nvidia")
	if !got[name] {
		t.Fatalf("Studio preflight credential %q = false, want true; inventory=%v", name, got)
	}
}

func TestStudioPreflightReportsMissingWorkspaceProviderCredential(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := workspacesettings.NewStore(filepath.Join(t.TempDir(), "workspace-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetWorkspaceSettingsStore(store)
	s.SetCredentialVault(newMemVault())

	const workspaceID = "ws_missing_provider_key"
	if _, err := store.Set(context.Background(), workspaceID, "owner", workspacesettings.Settings{
		LLM: workspacesettings.LLM{Providers: map[string]workspacesettings.Provider{
			"nvidia": {BaseURL: "https://integrate.api.nvidia.com/v1", Model: "model"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	app := appAsWorkspace(t, s, workspaceID, func(app *fiber.App) {
		app.Get("/preflight-secrets", func(c *fiber.Ctx) error {
			return c.JSON(s.preflightInput(c, studio.Catalog{}).SecretsSet)
		})
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/preflight-secrets", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	name := workspacesettings.ProviderAPIKey("nvidia")
	value, exists := got[name]
	if !exists || value {
		t.Fatalf("Studio preflight credential %q = %v (exists=%v), want explicit false", name, value, exists)
	}
}
