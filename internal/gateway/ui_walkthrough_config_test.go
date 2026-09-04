package gateway

// ui_walkthrough_config_test.go — the `ui` config block the web UI uses to
// remember, per install, that the platform walkthrough has been seen.
//
// Three things are asserted here because all three have burned us before in
// this codebase:
//
//  1. the patch actually lands in config.yaml on disk;
//  2. a GET in the SAME process reflects it — handlePatchConfig writes the file
//     but does not reload s.cfg, so without the in-memory update the very next
//     page load would read walkthrough_seen=false and re-open the tour the user
//     just dismissed;
//  3. an explicit `false` survives the round trip — pointer fields exist
//     precisely so "off" is distinguishable from "not mentioned".

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func uiBlock(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	ui, ok := body["ui"].(map[string]any)
	if !ok {
		t.Fatalf("config view has no ui block: %v", body)
	}
	return ui
}

func TestPatchConfig_UIWalkthrough_PersistsAndIsReadableImmediately(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"ui":{"walkthrough_seen":true,"walkthrough_step":7,"walkthrough_version":1}}`)
	if status != http.StatusOK {
		t.Fatalf("patch config (ui) status = %d body=%v", status, body)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"walkthrough_seen: true", "walkthrough_step: 7", "walkthrough_version: 1"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config.yaml missing %q:\n%s", want, raw)
		}
	}

	// Same process, no restart: this is what the browser reads on next load.
	status, got := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get config status = %d", status)
	}
	ui := uiBlock(t, got)
	if ui["walkthrough_seen"] != true {
		t.Fatalf("walkthrough_seen did not survive into the live config view: %v", ui)
	}
	if n, _ := ui["walkthrough_step"].(float64); int(n) != 7 {
		t.Fatalf("walkthrough_step = %v, want 7", ui["walkthrough_step"])
	}
}

func TestPatchConfig_UIWalkthrough_ExplicitFalseIsNotDropped(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	if status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"ui":{"walkthrough_seen":true}}`); status != http.StatusOK {
		t.Fatalf("patch (true) status = %d body=%v", status, body)
	}
	if status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"ui":{"walkthrough_seen":false}}`); status != http.StatusOK {
		t.Fatalf("patch (false) status = %d body=%v", status, body)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "walkthrough_seen: false") {
		t.Fatalf("explicit false was dropped from config.yaml:\n%s", raw)
	}

	_, got := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if uiBlock(t, got)["walkthrough_seen"] != false {
		t.Fatalf("explicit false was dropped from the live config view: %v", got["ui"])
	}
}

// A patch that says nothing about the UI must leave the block alone.
func TestPatchConfig_UnrelatedPatchLeavesWalkthroughAlone(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)

	if status, _ := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"ui":{"walkthrough_seen":true,"walkthrough_step":4}}`); status != http.StatusOK {
		t.Fatal("seed patch failed")
	}
	if status, _ := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret",
		`{"log":{"level":"debug"}}`); status != http.StatusOK {
		t.Fatal("log patch failed")
	}

	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "walkthrough_step: 4") {
		t.Fatalf("unrelated patch clobbered the ui block:\n%s", raw)
	}
	_, got := gatewayJSON(t, s, http.MethodGet, "/api/v1/config", "secret", "")
	if uiBlock(t, got)["walkthrough_seen"] != true {
		t.Fatalf("unrelated patch cleared walkthrough_seen: %v", got["ui"])
	}
}
