package studio

import (
	"fmt"
	"testing"
)

// TestGenerationRobustnessCorpus scores the built-in corpus through the
// deterministic pipeline and reports the recovery rate. It fails if any
// recoverable draft is NOT repaired (a normalizer regression) or any
// unrecoverable draft slips through as valid (a scorer/validation regression).
// The corpus itself lives in gencorpus.go so `sy eval generation` runs the same
// cases in CI.
func TestGenerationRobustnessCorpus(t *testing.T) {
	rep := RunGenerationCorpus(BuiltinGenerationCorpus())
	for _, f := range rep.Failures {
		if f.Expected {
			t.Errorf("[%s] expected the deterministic layer to make it valid, got errors: %v", f.Name, f.Errors)
		} else {
			t.Errorf("[%s] expected an invalid draft to be flagged, but it passed", f.Name)
		}
	}
	t.Logf("generation robustness: %d/%d recoverable drafts repaired deterministically (%.0f%%)",
		rep.Recovered, rep.RecoverableTotal, rep.Rate())
	if rep.Recovered != rep.RecoverableTotal {
		t.Fatalf("generation robustness regressed: %s", fmt.Sprintf("%d/%d", rep.Recovered, rep.RecoverableTotal))
	}
}
