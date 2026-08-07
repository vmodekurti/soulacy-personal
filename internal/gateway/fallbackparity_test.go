package gateway

// The two graph-design paths must take the SAME fallback decision.
//
// There are two entry points that build a draft and may discard the builder
// model's graph for a deterministic skeleton:
//
//	internal/studio/generatepipeline.go   the streamed pipeline (Generate)
//	internal/gateway/studio.go            studioDesignGraph (/studio/compile)
//
// Each carried its own copy of the rule. Fixing one left the other, and the
// one left behind was the path the Workflow-mode button actually uses. Live,
// within a minute of each other: the streamed run reported "Keeping the
// model's graph despite its blockers: the deterministic alternative has 2
// blocker(s) of its own against this graph's 1", and the Workflow-mode run
// through /compile threw an equivalent graph away and produced the canned
// two-node skeleton.
//
// Both now call studio.KeepModelGraph. This test reads the handler source and
// fails if a second copy of the decision reappears — a duplicated rule is the
// defect, and it is invisible to any test that only exercises one path.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStudioDesignGraph_UsesTheSharedFallbackDecision(t *testing.T) {
	src, err := os.ReadFile("studio.go")
	if err != nil {
		t.Fatal(err)
	}
	body := designGraphBody(t, string(src))

	if !strings.Contains(body, "studio.KeepModelGraph(") {
		t.Error("studioDesignGraph no longer defers to studio.KeepModelGraph, so /compile can drift from the streamed pipeline again")
	}

	// The tell-tale of a re-grown local copy: deciding straight off
	// CoverageShortfall comparisons in an if, rather than passing them in.
	local := regexp.MustCompile(`if\s+detOK\s*&&\s*\n?\s*studio\.CoverageShortfall`)
	if local.MatchString(body) {
		t.Error("studioDesignGraph has grown its own coverage-only fallback rule again; pass the shortfalls to studio.KeepModelGraph instead")
	}
}

// designGraphBody returns just the studioDesignGraph function, so the assertions
// above cannot be satisfied (or tripped) by unrelated code elsewhere in a 4k-line
// file.
func designGraphBody(t *testing.T, src string) string {
	t.Helper()
	start := strings.Index(src, "func (s *Server) studioDesignGraph(")
	if start < 0 {
		t.Fatal("studioDesignGraph not found — this guard has stopped guarding anything")
	}
	rest := src[start:]
	if end := strings.Index(rest, "\nfunc "); end > 0 {
		// skip past this function's own signature line before finding the next
		if next := strings.Index(rest[1:], "\nfunc "); next > 0 {
			return rest[:next+1]
		}
		return rest[:end]
	}
	return rest
}
