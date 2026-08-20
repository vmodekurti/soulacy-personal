package plugins

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func onlyResolver(workspace, plugin, key, value string) SettingsResolver {
	return SettingsResolverFunc(func(w, p, k string) (string, bool) {
		if w == workspace && p == plugin && k == key {
			return value, true
		}
		return "", false
	})
}

// The leak: an author who puts an API key in `settings:` instead of declaring
// it hands every tenant the operator's key.
func TestACredentialInSharedSettingsDoesNotReachATenant(t *testing.T) {
	section := map[string]any{"api_key": "operator-secret", "endpoint": "https://x"}

	out, withheld := tenantizeSettings("matrix", "ws_a", section, nil)

	if _, present := out["api_key"]; present {
		t.Fatalf("the operator's key reached the tenant: %v", out)
	}
	if out["endpoint"] != "https://x" {
		t.Errorf("ordinary configuration was stripped: %v", out)
	}
	if len(withheld) != 1 || withheld[0].Key != "api_key" || withheld[0].PluginID != "matrix" {
		t.Fatalf("withheld = %+v; the operator is never told why the plugin stopped working", withheld)
	}
}

// A credential nested one level down is the ordinary way these files are
// written. A shallow check would pass it straight through.
func TestANestedCredentialIsFoundToo(t *testing.T) {
	section := map[string]any{
		"auth":    map[string]any{"api_key": "operator-secret", "scheme": "bearer"},
		"timeout": 30,
	}

	out, withheld := tenantizeSettings("matrix", "ws_a", section, nil)

	auth, _ := out["auth"].(map[string]any)
	if _, present := auth["api_key"]; present {
		t.Fatalf("a nested credential reached the tenant: %v", auth)
	}
	if auth["scheme"] != "bearer" || out["timeout"] != 30 {
		t.Errorf("nesting lost ordinary values: %v", out)
	}
	// Reported by its path, so the operator can find it in the file.
	if len(withheld) != 1 || withheld[0].Key != "auth.api_key" {
		t.Fatalf("withheld = %+v, want the dotted path", withheld)
	}
}

// The tenant's own value is used, and it is looked up by the LEAF name — a
// tenant naming their credential should not have to know where in the
// operator's file it happened to be nested.
func TestTheTenantsOwnValueIsUsed(t *testing.T) {
	section := map[string]any{"auth": map[string]any{"api_key": "operator-secret"}}

	out, withheld := tenantizeSettings("matrix", "ws_a", section,
		onlyResolver("ws_a", "matrix", "api_key", "tenant-a-key"))

	auth, _ := out["auth"].(map[string]any)
	if auth["api_key"] != "tenant-a-key" {
		t.Fatalf("api_key = %v, want the tenant's own", auth["api_key"])
	}
	if len(withheld) != 0 {
		t.Errorf("reported as withheld after being supplied: %+v", withheld)
	}
}

// One tenant supplying a value must not supply it for anybody else.
func TestOneTenantsValueDoesNotLeakToAnother(t *testing.T) {
	section := map[string]any{"api_key": "operator-secret"}
	resolver := onlyResolver("ws_a", "matrix", "api_key", "tenant-a-key")

	if out, _ := tenantizeSettings("matrix", "ws_a", section, resolver); out["api_key"] != "tenant-a-key" {
		t.Fatalf("ws_a: %v", out)
	}
	out, withheld := tenantizeSettings("matrix", "ws_b", section, resolver)
	if _, present := out["api_key"]; present {
		t.Fatalf("ws_b received a value: %v", out)
	}
	if len(withheld) != 1 {
		t.Errorf("ws_b was not told what it is missing: %+v", withheld)
	}
}

// Withholding without saying so is the same feature with a worse error
// message, and the remedy has to name the real fix.
func TestTheRemedyNamesTheManifestNotTheVault(t *testing.T) {
	msg := WithheldSettingsMessage([]WithheldSetting{{PluginID: "matrix", Key: "auth.api_key"}})
	if !strings.Contains(msg, "matrix.auth.api_key") {
		t.Errorf("the message does not name the setting: %q", msg)
	}
	if !strings.Contains(msg, "credentials:") {
		t.Errorf("the message does not name the manifest field that fixes it: %q", msg)
	}
	if WithheldSettingsMessage(nil) != "" {
		t.Error("an empty list produced a message")
	}
}

// Personal installations have one tenant whose credentials genuinely are the
// operator's. Invariant 7: without the requirement, nothing changes.
func TestWithoutTheRequirementSettingsAreSharedAsBefore(t *testing.T) {
	stores := NewStores(nil, t.TempDir(), nil)
	stores.SetSettings(map[string]map[string]any{
		"matrix": {"api_key": "operator-secret"},
	})
	stores.mu.Lock()
	shared := stores.settings["matrix"]
	tenantOn := stores.tenantSettings
	stores.mu.Unlock()
	if tenantOn {
		t.Fatal("the requirement is on by default; every personal installation loses its settings")
	}
	if shared["api_key"] != "operator-secret" {
		t.Fatalf("settings were altered without the requirement: %v", shared)
	}
}

// Turning the requirement on after loaders exist has to re-apply to them.
// Otherwise the rule only reaches whichever workspace happened to be built
// afterwards, which is a rule about cache timing rather than about credentials.
//
// Driven through Stores with a real plugin on disk rather than through
// tenantizeSettings directly: the version of this test that only checked the
// flag passed on a build where applySettings ignored it entirely, which is the
// whole bug with an extra step.
func TestTurningTheRequirementOnReachesLoadersAlreadyBuilt(t *testing.T) {
	// A PLATFORM plugin: the operator's shared directory, which is the case
	// plugins_config actually configures. A plugin under a workspace's own
	// directory would only be visible to that workspace and could not show the
	// sharing behaviour at all.
	platform := t.TempDir()
	writePlugin(t, platform, "matrix-suite", `
id: matrix-suite
name: Matrix Suite
version: 1.0.0
`)
	stores := NewStores([]string{platform}, t.TempDir(), zap.NewNop())
	stores.SetSettings(map[string]map[string]any{
		"matrix-suite": {"api_key": "operator-secret", "endpoint": "https://x"},
	})

	// Built BEFORE the requirement, holding the operator's value.
	loader := stores.For("ws_a")
	before := pluginSettings(t, loader, "matrix-suite")
	if before["api_key"] != "operator-secret" {
		t.Fatalf("setup: expected the shared value, got %v", before)
	}

	stores.RequireTenantSettings(nil)

	after := pluginSettings(t, loader, "matrix-suite")
	if _, present := after["api_key"]; present {
		t.Fatalf("a loader built before the requirement kept the operator's credential: %v", after)
	}
	if after["endpoint"] != "https://x" {
		t.Errorf("ordinary configuration was stripped: %v", after)
	}
	items := stores.WithheldSettings("ws_a")
	if len(items) != 1 || items[0].Key != "api_key" {
		t.Fatalf("withheld = %+v", items)
	}
}

// A loader built AFTER the requirement must be correct from the start.
func TestALoaderBuiltAfterTheRequirementNeverHoldsTheOperatorsValue(t *testing.T) {
	// A PLATFORM plugin: the operator's shared directory, which is the case
	// plugins_config actually configures. A plugin under a workspace's own
	// directory would only be visible to that workspace and could not show the
	// sharing behaviour at all.
	platform := t.TempDir()
	writePlugin(t, platform, "matrix-suite", `
id: matrix-suite
name: Matrix Suite
version: 1.0.0
`)
	stores := NewStores([]string{platform}, t.TempDir(), zap.NewNop())
	stores.RequireTenantSettings(onlyResolver("ws_a", "matrix-suite", "api_key", "tenant-a-key"))
	stores.SetSettings(map[string]map[string]any{
		"matrix-suite": {"api_key": "operator-secret"},
	})

	if got := pluginSettings(t, stores.For("ws_a"), "matrix-suite"); got["api_key"] != "tenant-a-key" {
		t.Fatalf("ws_a api_key = %v, want the tenant's own", got["api_key"])
	}
	if got := pluginSettings(t, stores.For("ws_b"), "matrix-suite"); got["api_key"] != nil {
		t.Fatalf("ws_b received a value it never supplied: %v", got)
	}
}

func pluginSettings(t *testing.T, loader *Loader, pluginID string) map[string]any {
	t.Helper()
	for _, lp := range loader.All() {
		if lp != nil && lp.Manifest.ID == pluginID {
			return lp.Settings
		}
	}
	t.Fatalf("plugin %q was not loaded", pluginID)
	return nil
}
