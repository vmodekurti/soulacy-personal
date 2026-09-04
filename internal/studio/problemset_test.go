package studio

import (
	"strings"
	"testing"
)

// buildProblemSet drives the autonomous repair loop. It must act on real BLOCKERS
// but ignore deep-introspection argument WARNINGS, which are false-positive-prone
// (incomplete published schemas) and would loop the build on phantom errors.
func TestBuildProblemSet_BlockersInWarningsOut(t *testing.T) {
	pf := PreflightResult{
		Blockers: []PreflightIssue{
			{Severity: "block", Kind: "dependency", NodeID: "use", Message: `Required argument "notebook_id" is empty`},
			{Severity: "block", Kind: "schedule", Message: "schedule trigger has no cron"},
		},
		Warnings: []PreflightIssue{
			// The false-positive class: an arg the tool actually accepts but doesn't publish.
			{Severity: "warn", Kind: "dependency", NodeID: "poll", Message: `Argument "max_wait" is not accepted by tool "mcp__notebooklm__research_status"`},
			{Severity: "warn", Kind: "channel", Message: "telegram token missing"},
		},
	}
	got := buildProblemSet(pf, Draft{}, Catalog{})

	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "notebook_id") || !strings.Contains(joined, "cron") {
		t.Errorf("blockers must be in the problem set: %+v", got)
	}
	if strings.Contains(joined, "max_wait") {
		t.Errorf("introspection arg WARNINGS must NOT drive the loop (false positives): %+v", got)
	}
	if strings.Contains(joined, "telegram") {
		t.Errorf("pure-config warnings must not be in the problem set: %+v", got)
	}
	if len(got) != 2 {
		t.Errorf("want exactly the 2 blockers, got %d: %+v", len(got), got)
	}
}
