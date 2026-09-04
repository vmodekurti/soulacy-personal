package plugins

// Story E22 (2)+(3): SDK-major rejection at load, and chaos-style proof
// that one broken plugin never takes down the rest — refused plugins are
// skipped with a recorded diagnostic the host can surface in the Logs GUI.

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestLoader_RejectsIncompatibleSDKMajor(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "future", "id: future\nname: Future\nsdk_major: 99\n")

	l := New([]string{root}, zap.NewNop())
	if l.Count() != 0 {
		t.Fatalf("plugin with sdk_major 99 must not load (count=%d)", l.Count())
	}
	diags := l.Diagnostics()
	if len(diags) != 1 || !strings.Contains(diags[0].Reason, "sdk_major") {
		t.Errorf("diagnostics = %+v, want one sdk_major rejection", diags)
	}
}

func TestLoader_CurrentSDKMajorLoads(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "ok", "id: ok\nname: OK\nsdk_major: 1\n")
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("sdk_major 1 must load (count=%d, diags=%+v)", l.Count(), l.Diagnostics())
	}
}

// The chaos test: a broken-yaml plugin, a future-schema plugin, and a
// future-sdk plugin sit next to a healthy one. The healthy plugin loads,
// every casualty is individually diagnosed, and nothing panics.
func TestLoader_ChaosOneBadPluginNeverBlocksTheRest(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "broken", "id: [unclosed\n  yaml: {{{\n")
	writePlugin(t, root, "future-schema", "id: fs\nname: FS\nmanifest_schema: 99\n")
	writePlugin(t, root, "future-sdk", "id: fsdk\nname: FSDK\nsdk_major: 99\n")
	writePlugin(t, root, "healthy", "id: healthy\nname: Healthy\n")

	l := New([]string{root}, zap.NewNop())

	if l.Count() != 1 {
		t.Fatalf("healthy plugin count = %d, want 1 (diags=%+v)", l.Count(), l.Diagnostics())
	}
	if l.All()[0].Manifest.ID != "healthy" {
		t.Errorf("loaded plugin = %q, want healthy", l.All()[0].Manifest.ID)
	}

	diags := l.Diagnostics()
	if len(diags) != 3 {
		t.Fatalf("diagnostics = %d, want 3: %+v", len(diags), diags)
	}
	var sawBroken, sawSchema, sawSDK bool
	for _, d := range diags {
		if d.Dir == "" || d.Reason == "" {
			t.Errorf("diagnostic missing dir/reason: %+v", d)
		}
		switch {
		case strings.Contains(d.Dir, "broken"):
			sawBroken = true
		case strings.Contains(d.Reason, "manifest_schema"):
			sawSchema = true
		case strings.Contains(d.Reason, "sdk_major"):
			sawSDK = true
		}
	}
	if !sawBroken || !sawSchema || !sawSDK {
		t.Errorf("missing diagnostics: broken=%v schema=%v sdk=%v — %+v", sawBroken, sawSchema, sawSDK, diags)
	}
}
