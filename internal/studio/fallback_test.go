package studio

// Do not fall back to something worse.
//
// When the builder model's graph fails its contract, the pipeline swaps in a
// deterministic skeleton. The only reason to make that swap is that the
// replacement is sounder — otherwise a graph shaped like the user's request is
// being thrown away for nothing.
//
// One guard existed: keep the model's graph if the fallback would drop a
// capability the user named. Contract health was never compared at all, and
// that is the axis that bit.
//
// Observed live, building a scheduled market briefing. The prompt named three
// parallel analysts and an editor; the refined intent captured all four
// correctly. The model's graph carried ONE blocker. It was discarded for a
// deterministic skeleton that carried TWO blockers and two warnings — a
// two-node "search then summarize" with no agent nodes at all, whose one tool
// step was wired to a NOTE-WRITING tool to do the searching. The user was shown
// a fallback that was worse on every axis and told nothing had been lost.

import (
	"strings"
	"testing"
)

func TestKeepModelGraph_WhenTheFallbackIsNoHealthier(t *testing.T) {
	// The live case: model 1 blocker, deterministic 2.
	reason, note := KeepModelGraph("", "", 1, 2)
	if reason == "" {
		t.Fatal("fell back from a 1-blocker graph to a 2-blocker one — the swap costs the user their structure and buys nothing")
	}
	if !strings.Contains(reason, "2 blocker") || !strings.Contains(reason, "1") {
		t.Errorf("the reason should say what it compared, got: %s", reason)
	}
	if note == "" {
		t.Error("the draft should carry a note explaining why it still has blockers")
	}
}

// A tie buys nothing either, and the model's graph is the one shaped like the
// request. Keep it.
func TestKeepModelGraph_OnATie(t *testing.T) {
	if reason, _ := KeepModelGraph("", "", 2, 2); reason == "" {
		t.Fatal("an equal-blocker fallback replaces the user's structure for no gain")
	}
}

// The swap is worth making when the fallback genuinely is sounder.
func TestKeepModelGraph_FallsBackWhenTheAlternativeIsActuallyBetter(t *testing.T) {
	if reason, _ := KeepModelGraph("", "", 3, 0); reason != "" {
		t.Fatalf("should have fallen back to a clean graph, but kept the model's: %s", reason)
	}
	if reason, _ := KeepModelGraph("", "", 3, 1); reason != "" {
		t.Fatalf("should have fallen back to a healthier graph, but kept the model's: %s", reason)
	}
}

// The original guard still holds, and still wins even when the fallback has
// fewer blockers: a graph that quietly uses the wrong capability is worse than
// one with visible blockers, because the blockers are on screen with a button
// and the wrong tool is invisible until someone reads the nodes.
func TestKeepModelGraph_StillProtectsANamedCapability(t *testing.T) {
	reason, _ := KeepModelGraph("", "drops the notebooklm MCP server you named", 3, 0)
	if reason == "" {
		t.Fatal("fell back to a graph that drops a capability the user named")
	}
	if !strings.Contains(reason, "capability you asked for") {
		t.Errorf("the reason should name the coverage loss, got: %s", reason)
	}
}

// When BOTH lose the capability, coverage is not a reason to keep either, so
// the decision falls through to contract health.
func TestKeepModelGraph_CoverageTieFallsThroughToBlockers(t *testing.T) {
	if reason, _ := KeepModelGraph("drops it", "drops it", 3, 0); reason != "" {
		t.Fatalf("neither graph covers it, and the fallback is healthier, so it should be taken: %s", reason)
	}
	if reason, _ := KeepModelGraph("drops it", "drops it", 1, 2); reason == "" {
		t.Fatal("neither covers it and the fallback is worse — keep the model's graph")
	}
}
