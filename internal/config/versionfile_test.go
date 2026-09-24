package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The VERSION file is what a source build reports when nobody passes a build
// arg. Railway, Render and Coolify cannot pass one, so without it every
// managed-platform image called itself "dev", the update checker skipped it as
// an incomparable dev build, and a redeploy that worked was indistinguishable
// from one that never happened (#227).
func TestVersionFileIsPresentAndTagShaped(t *testing.T) {
	raw, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatalf("VERSION file must exist at the repo root: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	if got == "" {
		t.Fatal("VERSION file is empty; a source build would report \"dev\"")
	}
	if got == "dev" {
		t.Fatal("VERSION file says \"dev\"; that is the fallback it exists to avoid")
	}
	if strings.ContainsAny(got, " \t\n") {
		t.Fatalf("VERSION must be a single token, got %q", got)
	}
	// Same shape scripts/create-release.sh accepts, so the two cannot disagree.
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`).MatchString(got) {
		t.Fatalf("VERSION %q is not a release tag like v1.2.3", got)
	}
}

// The compiled-in default stays "dev" — that is how an unstamped local `go
// build` identifies itself, and the updater relies on it to decline comparing.
func TestVersionDefaultsToDev(t *testing.T) {
	if Version != "dev" && !regexp.MustCompile(`^v[0-9]`).MatchString(Version) {
		t.Fatalf("Version = %q, want \"dev\" or a v-prefixed release", Version)
	}
}
