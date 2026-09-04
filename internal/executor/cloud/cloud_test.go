package cloud

import (
	"strings"
	"testing"
)

func TestPreset_RunPod(t *testing.T) {
	r, ok := Preset("runpod", "pod-123", "")
	if !ok {
		t.Fatal("runpod should be a preset")
	}
	j := strings.Join(r, " ")
	if !strings.HasPrefix(j, "runpodctl exec python") || !strings.Contains(j, "--pod-id pod-123") {
		t.Fatalf("runpod runner wrong: %s", j)
	}
}

func TestPreset_DaytonaWithCLIOverride(t *testing.T) {
	r, ok := Preset("daytona", "ws-main", "/opt/daytona")
	if !ok {
		t.Fatal("daytona should be a preset")
	}
	if r[0] != "/opt/daytona" || !contains(r, "ws-main") {
		t.Fatalf("daytona runner wrong: %v", r)
	}
}

func TestPreset_Modal(t *testing.T) {
	r, ok := Preset("modal", "", "")
	if !ok || r[0] != "modal" || r[1] != "shell" {
		t.Fatalf("modal runner wrong: %v", r)
	}
}

func TestPreset_Unknown(t *testing.T) {
	if _, ok := Preset("aws-lambda", "", ""); ok {
		t.Fatal("unknown preset should return ok=false")
	}
	if IsPreset("nope") {
		t.Fatal("IsPreset should be false for unknown")
	}
	if !IsPreset("modal") {
		t.Fatal("IsPreset should be true for modal")
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
