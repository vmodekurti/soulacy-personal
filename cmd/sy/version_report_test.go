package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRenderVersionReportShowsRunningBuild(t *testing.T) {
	now := time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)
	body := []byte(`{"status":"ok","build":{"label":"amber-falcon","build_id":"3f9a2c1b4e07","version":"v1.2.0-dirty",
		"commit":"fc2fe43fe400c289b76e","modified":true,"started_at":"2026-10-18T14:55:00Z","instance_id":"a1b2c3d4"}}`)
	out := renderVersionReport("v1.2.0", "http://127.0.0.1:18789", body, nil, now)
	for _, want := range []string{
		"Running build  amber-falcon  (3f9a2c1b4e07)",
		"fc2fe43fe400 (with uncommitted changes)",
		"(5m ago)",
		"Instance     a1b2c3d4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderVersionReportHandlesOldOrDownGateway(t *testing.T) {
	now := time.Now()
	if out := renderVersionReport("v1", "http://x", nil, errors.New("connection refused"), now); !strings.Contains(out, "Could not read the running build: connection refused") {
		t.Errorf("down gateway: %s", out)
	}
	if out := renderVersionReport("v1", "http://x", []byte(`{"status":"ok","version":"v0.9"}`), nil, now); !strings.Contains(out, "predates build labels") {
		t.Errorf("old gateway: %s", out)
	}
}
