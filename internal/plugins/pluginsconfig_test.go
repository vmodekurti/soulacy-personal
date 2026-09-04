package plugins

// Story E17: Wire attaches each plugin's plugins_config section to its
// LoadedPlugin — for v1 (tools-only) and v2 plugins alike.

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestWire_AttachesPluginsConfigSettings(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "legacy", `
id: legacy
channels: [telegram]
`)
	l := New([]string{root}, zap.NewNop())
	if errs := Wire(context.Background(), l, WireDeps{
		Log: zap.NewNop(),
		PluginsConfig: map[string]map[string]any{
			"legacy":    {"units": "metric", "retries": 3},
			"unrelated": {"x": 1},
		},
	}); len(errs) != 0 {
		t.Fatalf("Wire errors: %v", errs)
	}

	var lp *LoadedPlugin
	for _, p := range l.All() {
		if p.Manifest.ID == "legacy" {
			lp = p
		}
	}
	if lp == nil {
		t.Fatal("legacy plugin not loaded")
	}
	if lp.Settings == nil || lp.Settings["units"] != "metric" {
		t.Fatalf("Settings not attached: %v", lp.Settings)
	}
}

func TestWire_NoSectionLeavesSettingsNil(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "bare", `
id: bare
`)
	l := New([]string{root}, zap.NewNop())
	if errs := Wire(context.Background(), l, WireDeps{Log: zap.NewNop()}); len(errs) != 0 {
		t.Fatalf("Wire errors: %v", errs)
	}
	for _, p := range l.All() {
		if p.Manifest.ID == "bare" && p.Settings != nil {
			t.Fatalf("Settings = %v, want nil", p.Settings)
		}
	}
}
