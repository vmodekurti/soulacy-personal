// settings_test.go — invalidating a workspace must not strip its settings.
//
// This is the bug the hot-reload work exposed rather than created. Settings
// were attached once, by plugins.Wire, to the loaders that existed at boot.
// Invalidate then started discarding loaders so a lifecycle change could take
// effect — and the replacement For built was a fresh scan with nothing attached.
//
// So revoking ONE plugin silently stripped every OTHER plugin's configuration
// in that workspace. The symptom is the worst kind: a plugin stops working for
// a reason unrelated to anything anybody changed, and comes back after a
// restart, which makes it look intermittent.
package plugins

import (
	"testing"

	"go.uber.org/zap"
)

func settingsStore(t *testing.T) (*Stores, string) {
	t.Helper()
	base := t.TempDir()
	stores := NewStores(nil, base, zap.NewNop())
	dir, err := stores.EnsureDir("ws-a")
	if err != nil {
		t.Fatal(err)
	}
	writePlugin(t, dir, "acme", "id: acme\nname: acme\nversion: 1.0.0\n")
	writePlugin(t, dir, "beta", "id: beta\nname: beta\nversion: 1.0.0\n")
	stores.SetSettings(map[string]map[string]any{
		"acme": {"units": "metric"},
		"beta": {"units": "imperial"},
	})
	return stores, dir
}

func settingOf(t *testing.T, stores *Stores, workspaceID, pluginID string) any {
	t.Helper()
	for _, lp := range stores.For(workspaceID).All() {
		if lp.Manifest.ID == pluginID {
			return lp.Settings["units"]
		}
	}
	return nil
}

func TestSettingsSurviveInvalidation(t *testing.T) {
	stores, _ := settingsStore(t)
	if got := settingOf(t, stores, "ws-a", "acme"); got != "metric" {
		t.Fatalf("settings were not applied at all (%v); this test proves nothing", got)
	}

	// Exactly what revoking, enabling or re-approving any plugin does.
	stores.Invalidate("ws-a")

	if got := settingOf(t, stores, "ws-a", "acme"); got != "metric" {
		t.Errorf("acme's settings = %v after an unrelated invalidation, want metric — one plugin's "+
			"lifecycle change wiped another plugin's configuration", got)
	}
	if got := settingOf(t, stores, "ws-a", "beta"); got != "imperial" {
		t.Errorf("beta's settings = %v after invalidation, want imperial", got)
	}
}

func TestChangingSettingsReachesLoadersThatAlreadyExist(t *testing.T) {
	// Storing the new settings for FUTURE loaders is half the job. Without
	// re-applying to the current ones, a plugins_config edit would only reach
	// a workspace whose loader happened to be rebuilt afterwards — a rule
	// about cache timing rather than about configuration.
	stores, _ := settingsStore(t)
	_ = stores.For("ws-a") // build it, so there is a live loader to update

	stores.SetSettings(map[string]map[string]any{"acme": {"units": "furlongs"}})

	if got := settingOf(t, stores, "ws-a", "acme"); got != "furlongs" {
		t.Errorf("settings = %v after an edit, want furlongs — the change reached the registry and "+
			"not the loader already serving requests", got)
	}
}

func TestSettingsAreAppliedToAWorkspaceBuiltAfterTheyWereSet(t *testing.T) {
	// The other order: settings first, workspace touched later. This is the
	// ordinary boot sequence, where SetSettings runs during wiring and most
	// workspaces have no loader yet.
	base := t.TempDir()
	stores := NewStores(nil, base, zap.NewNop())
	stores.SetSettings(map[string]map[string]any{"acme": {"units": "metric"}})

	dir, err := stores.EnsureDir("ws-late")
	if err != nil {
		t.Fatal(err)
	}
	writePlugin(t, dir, "acme", "id: acme\nname: acme\nversion: 1.0.0\n")

	if got := settingOf(t, stores, "ws-late", "acme"); got != "metric" {
		t.Errorf("settings = %v for a workspace first built after SetSettings, want metric", got)
	}
}
