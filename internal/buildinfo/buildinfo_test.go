package buildinfo

import (
	"regexp"
	"testing"
)

func TestCodenameIsStableAndWellFormed(t *testing.T) {
	a := Codename("3f9a2c1b4e07")
	if a != Codename("3f9a2c1b4e07") {
		t.Fatalf("codename not deterministic")
	}
	if !regexp.MustCompile(`^[a-z]+-[a-z]+$`).MatchString(a) {
		t.Fatalf("codename %q is not adjective-noun", a)
	}
	if Codename("3f9a2c1b4e07") == Codename("409a2c1b4e07") {
		t.Fatalf("different first byte should change the adjective")
	}
	if Codename("") != "unknown-build" || Codename("zz") != "unknown-build" {
		t.Fatalf("invalid ids should map to unknown-build")
	}
}

func TestWordListsHaveNoDuplicates(t *testing.T) {
	for name, list := range map[string][64]string{"adjectives": adjectives, "nouns": nouns} {
		seen := map[string]bool{}
		for _, w := range list {
			if w == "" || seen[w] {
				t.Fatalf("%s: empty or duplicate word %q", name, w)
			}
			seen[w] = true
		}
	}
}

func TestCurrentDescribesThisProcess(t *testing.T) {
	info := Current()
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(info.BuildID) {
		t.Fatalf("BuildID = %q, want 12 hex chars", info.BuildID)
	}
	if info.Label != Codename(info.BuildID) {
		t.Fatalf("Label %q does not match Codename(BuildID)", info.Label)
	}
	if len(info.InstanceID) != 8 || info.StartedAt.IsZero() || info.GoVersion == "" {
		t.Fatalf("incomplete process identity: %+v", info)
	}
	if Current() != info {
		t.Fatalf("Current() changed between calls")
	}
}
