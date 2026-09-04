package external

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPoCVoiceSidecarConformance proves the Story-10 voice-bridge PoC
// (docs/VOICE_SPIKE.md) speaks External Channel Protocol v1: handshake
// deadline, version negotiation, unknown-frame tolerance (its proposed v2
// `usage` frame must be ignored), send survival, and shutdown ≤5s.
// Skipped when python3 is unavailable.
func TestPoCVoiceSidecarConformance(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	script, err := filepath.Abs("../../../scripts/poc-voice-sidecar.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := RunConformance(context.Background(), py, script); err != nil {
		t.Fatalf("conformance: %v", err)
	}
}
