package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServicePATHIncludesInteractiveAndStandardLocations(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "custom-bin")
	t.Setenv("PATH", custom)
	got := servicePATH()
	for _, want := range []string{custom, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		if !strings.Contains(got, want) {
			t.Errorf("servicePATH() = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "/usr/bin") != 1 {
		t.Errorf("servicePATH should de-duplicate entries: %q", got)
	}
}
