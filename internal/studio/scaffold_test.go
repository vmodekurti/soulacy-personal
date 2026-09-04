package studio

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/studio/codeclass"
)

// Every framework scaffold must be complete (define run(inputs)) and its
// declared Requires must match what the classifier sees in the code, so the
// consent UX is accurate.
func TestScaffolds_CompleteAndClassified(t *testing.T) {
	got := Scaffolds()
	if len(got) == 0 {
		t.Fatal("no scaffolds")
	}
	for _, s := range got {
		if !strings.Contains(s.Code, "def run(inputs):") {
			t.Fatalf("scaffold %q missing run(inputs)", s.Kind)
		}
		cls := codeclass.Classify(s.Code)
		// Declared Requires must be a subset of what the classifier detects.
		for _, want := range s.Requires {
			found := false
			for _, c := range cls.Requires {
				if c == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("scaffold %q declares %q but classifier didn't detect it (%v)", s.Kind, want, cls.Requires)
			}
		}
		// The transform scaffold must be ReadOnly (no consent).
		if s.Kind == "transform" && cls.Beyond() {
			t.Fatalf("transform scaffold should be ReadOnly, got %v dynamic=%v", cls.Requires, cls.Dynamic)
		}
	}
	if ScaffoldByKind("shell").Kind != "shell" {
		t.Fatal("ScaffoldByKind(shell) not found")
	}
	if ScaffoldByKind("nope").Kind != "" {
		t.Fatal("unknown kind should be zero value")
	}
}
