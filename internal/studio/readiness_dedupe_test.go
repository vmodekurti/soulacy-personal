package studio

// One problem, counted once.
//
// AssessContract runs preflight itself and republishes each issue as a
// "runtime.*" check, so that assessing the contract alone still tells you about
// runtime problems. Readiness runs BOTH — preflight as its own section, and the
// contract — so every runtime finding landed in rep.Blockers twice.
//
// Live, on a generated workflow: the Save dialog's header read "Blockers (2)"
// and "Warnings (2)" from the contract, while the readiness summary rendered
// directly beneath it said "4 blockers, 4 warnings" about the same two
// problems. Whichever number the user believed, one of them was lying, and the
// gate ("4 blockers to resolve") used the inflated one.

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// A draft whose one tool node is wired to a tool that does not exist, which
// preflight reports and the contract then echoes.
// The live reproduction, reduced: a workflow node wired to an MCP tool whose
// required arguments were never filled, with the server connected and its
// schema in the catalog so preflight can actually judge it.
func doubleCountedDraft() Draft {
	return Draft{
		Name:    "Digest",
		Trigger: Trigger{Type: "manual"},
		Flow: Flow{
			Entry: "fetch",
			Nodes: []sdkr.FlowNode{{
				ID: "fetch", Kind: "tool", Tool: "mcp__notebooklm__note",
				Input: `{"query":"anything","num_results":5}`, Output: "raw",
			}},
		},
	}
}

// The environment the draft is judged against: the server IS connected and the
// tool's schema IS known, so "action" and "notebook_id" are genuinely missing
// rather than merely unverifiable.
func doubleCountedEnv() (Catalog, PreflightInput) {
	cat := Catalog{
		MCP: []CatalogMCPServer{{
			Server: "notebooklm",
			Tools: []CatalogMCPTool{{
				Name:   "mcp__notebooklm__note",
				Params: "action*:string, notebook_id*:string, title:string, content:string",
			}},
		}},
	}
	return cat, PreflightInput{Catalog: cat, ConnectedMCP: map[string]bool{"notebooklm": true}}
}

func countByMessage(items []ReadinessItem) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Message]++
	}
	return out
}

func TestReadiness_CountsEachRuntimeFindingOnce(t *testing.T) {
	rep := readinessForFixture()

	for msg, n := range countByMessage(rep.Blockers) {
		if n > 1 {
			t.Errorf("this blocker is reported %d times, so the save gate counts one problem as %d:\n    %s", n, n, msg)
		}
	}
	for msg, n := range countByMessage(rep.Warnings) {
		if n > 1 {
			t.Errorf("this warning is reported %d times:\n    %s", n, msg)
		}
	}
}

// The number in the summary is the number the Save step shows. It has to match
// the list a user can actually see and work through.
func TestReadinessSummary_MatchesTheFindingsItLists(t *testing.T) {
	rep := readinessForFixture()
	if rep.OK {
		t.Skip("this draft was expected to block; the fixture no longer bites")
	}

	distinct := len(countByMessage(rep.Blockers))
	if distinct != len(rep.Blockers) {
		t.Fatalf("summary %q counts %d blockers but only %d are distinct problems",
			rep.Summary, len(rep.Blockers), distinct)
	}
}

// The contract's runtime echoes are the copies to drop, not preflight's: the
// preflight section owns these findings and carries their fix actions. If the
// dedupe ever removes the wrong side, runtime problems would vanish from the
// section a user opens to fix them.
func TestReadiness_KeepsRuntimeFindingsInThePreflightSection(t *testing.T) {
	rep := readinessForFixture()

	found := false
	for _, it := range rep.Blockers {
		if it.Section == ReadinessSectionPreflight {
			found = true
		}
		if it.Section == ReadinessSectionContract && strings.HasPrefix(it.Kind, "runtime.") {
			t.Errorf("a runtime finding is still being reported by the contract section as well: %s", it.Message)
		}
	}
	if !found && len(rep.Blockers) > 0 {
		t.Fatal("runtime blockers were dropped from the preflight section — the dedupe removed the wrong copy")
	}
}

// Assessed on its own, the contract must still report runtime problems. The
// dedupe is a Readiness-level concern; stripping them in AssessContract would
// blind every caller that uses it directly.
func TestAssessContract_StillReportsRuntimeIssuesOnItsOwn(t *testing.T) {
	cat, pin := doubleCountedEnv()
	cr := AssessContract(doubleCountedDraft(), cat, pin)
	for _, c := range cr.Checks {
		if strings.HasPrefix(c.ID, "runtime.") {
			return
		}
	}
	t.Fatal("AssessContract no longer reports runtime checks, so a caller using it alone is blind to them")
}

func readinessForFixture() ReadinessReport {
	cat, pin := doubleCountedEnv()
	return Readiness(ReadinessInput{Draft: doubleCountedDraft(), Catalog: cat, Preflight: pin})
}
