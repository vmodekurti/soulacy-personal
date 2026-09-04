package tour

// The tour has to hold two properties that are easy to lose: it must cover
// every screen, and it must actually change with the install. A "tour" that
// says the same thing to a fresh install and a working one is documentation
// with extra steps.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// navIDs reads the screen list out of gui/src/lib/nav.js.
//
// It used to be a hand-typed copy of that list sitting in this test. That is
// the one shape of seam check that cannot fail: rename a screen on the client
// and the copy here goes stale, the test keeps passing against a list nobody
// navigates to any more, and the renamed screen quietly loses its story. This
// codebase has been bitten by a hand-typed mirror three times now — the fix
// action vocabulary, the shared-channel list, the walkthrough anchors — so
// read the real file instead.
func navIDs(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "gui", "src", "lib", "nav.js")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the tour's screen list has nothing to check itself against", path, err)
	}
	re := regexp.MustCompile(`\{\s*id:\s*'([^']+)'`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		out = append(out, m[1])
	}
	if len(out) < 20 {
		t.Fatalf("only parsed %d screens out of %s — the extractor has stopped matching, so this test proves nothing", len(out), path)
	}
	return out
}

func fresh() InstallState { return InstallState{} }
func working() InstallState {
	return InstallState{
		Providers: 2, Agents: 5, EnabledAgents: 3, DeliveryChannels: 2,
		Schedules: 1, Runs: 40, KnowledgeBases: 1, Skills: 3, MCPServers: 2,
		Plugins: 1, LearningPending: 4, OpenTasks: 2,
	}
}

func TestEveryPageHasAStory(t *testing.T) {
	// A screen with no story is a screen where "Show me around" is a button
	// that apologises — which is worse than no button, because the user pressed
	// it expecting help.
	nav := navIDs(t)
	for _, id := range nav {
		if !Has(id) {
			t.Errorf("no tour for %q", id)
		}
	}
	for _, id := range Pages() {
		found := false
		for _, n := range nav {
			if n == id {
				found = true
			}
		}
		if !found {
			t.Errorf("tour for %q, which is not a screen", id)
		}
	}
}

func TestEveryStoryIsWellFormed(t *testing.T) {
	for _, st := range []InstallState{fresh(), working()} {
		for _, id := range Pages() {
			pt, ok := Narrate(id, st)
			if !ok {
				t.Fatalf("%s: no tour", id)
			}
			if pt.Chapter == "" || pt.Position == "" || pt.Outcome == "" {
				t.Errorf("%s: missing framing (chapter=%q position=%q)", id, pt.Chapter, pt.Position)
			}
			if len(pt.Beats) < 2 {
				t.Errorf("%s: %d beats — a story needs at least what it is for and where you are", id, len(pt.Beats))
			}
			for _, b := range pt.Beats {
				if n := len(strings.Fields(b.Text)); n < 12 {
					t.Errorf("%s: a %d-word beat is a label, not a story: %q", id, n, b.Text)
				}
			}
			if pt.NextAction != "" && pt.NextLabel == "" {
				t.Errorf("%s: an action with no label renders no button", id)
			}
		}
	}
}

// The whole point of choosing "adaptive" over "fixed copy".
//
// The first version of this compared full narratives between a fresh and a
// working install, and passed even when a page's two tellings were made
// identical — because on a fresh install almost every page also gains a
// "first, though" beat, so the beat lists differed for an unrelated reason.
// This compares the two tellings directly, which is the actual property.
func TestEachPageTellsEmptyAndUsedDifferently(t *testing.T) {
	for id, p := range pages {
		empty := p.whenEmpty(fresh())
		used := p.whenUsed(working())
		if empty == used {
			t.Errorf("%s says the same thing whether it is empty or in use — that is fixed copy, not an adaptive tour", id)
		}
		if len(strings.Fields(empty)) < 12 || len(strings.Fields(used)) < 12 {
			t.Errorf("%s: one of its tellings is too short to be a story", id)
		}
	}
}

// And end to end: the assembled narrative differs too, for a page whose telling
// quotes real numbers.
func TestAssembledNarrativeReflectsTheNumbers(t *testing.T) {
	few := InstallState{Providers: 1, Agents: 1, DeliveryChannels: 1, Schedules: 1, Runs: 1, MCPServers: 1}
	many := working()
	a, _ := Narrate("agents", few)
	b, _ := Narrate("agents", many)
	if beatsEqual(a.Beats, b.Beats) {
		t.Fatal("the Deployed tour reads identically with 1 agent and with 5")
	}
	joined := ""
	for _, x := range b.Beats {
		joined += " " + x.Text
	}
	if !strings.Contains(joined, "5 agents") {
		t.Fatalf("the telling should quote what is actually there: %q", joined)
	}
}

func beatsEqual(a, b []Beat) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

// Standing on a screen that cannot work yet, the tour should say so and hand
// over the action that unblocks it — not explain a screen you cannot use.
func TestABlockedPageSaysWhatIsMissingFirst(t *testing.T) {
	pt, _ := Narrate("schedule", fresh())
	if pt.Blocked != "brain" {
		t.Fatalf("with no provider, every later screen is blocked on the brain; got %q", pt.Blocked)
	}
	if pt.NextAction != "open_providers" {
		t.Fatalf("the call to action should unblock the chain, not describe this page; got %q", pt.NextAction)
	}
	joined := ""
	for _, b := range pt.Beats {
		joined += " " + b.Text
	}
	if !strings.Contains(strings.ToLower(joined), "no model is connected") {
		t.Fatalf("the blocker should be named in plain words: %q", joined)
	}
}

// And the earliest screen in the chain is never "blocked" by something later.
func TestTheFirstLinkIsNeverBlocked(t *testing.T) {
	if pt, _ := Narrate("providers", fresh()); pt.Blocked != "" {
		t.Fatalf("providers is the first link; nothing can precede it, got %q", pt.Blocked)
	}
}

// A fully-working install should not be told what it is missing.
func TestAWorkingInstallGetsTheImprovementStory(t *testing.T) {
	for _, id := range Pages() {
		pt, _ := Narrate(id, working())
		if pt.Blocked != "" {
			t.Errorf("%s: nothing is missing on a working install, but the tour claims %q is", id, pt.Blocked)
		}
		if strings.Contains(pt.Position, "missing") {
			t.Errorf("%s: position still talks about a missing link: %q", id, pt.Position)
		}
	}
}

// Pages that share a stage must still tell the truth about themselves: an
// install with skills but no knowledge bases has StageMaterial satisfied, and
// the Knowledge page must still say it is empty.
func TestAPageSpeaksAboutItselfNotItsStage(t *testing.T) {
	st := InstallState{Providers: 1, Agents: 1, Skills: 3, DeliveryChannels: 1, Schedules: 1, Runs: 5}
	pt, _ := Narrate("knowledge", st)
	joined := ""
	for _, b := range pt.Beats {
		joined += " " + strings.ToLower(b.Text)
	}
	if !strings.Contains(joined, "nothing here yet") {
		t.Fatalf("knowledge is empty but the tour speaks as though it is populated: %q", joined)
	}
}

// The call to action must come from the shared fix-action vocabulary, so the
// tour's button behaves exactly like the ones on findings.
func TestNextActionsUseTheSharedVocabulary(t *testing.T) {
	known := map[string]bool{
		"open_providers": true, "open_mcp": true, "open_delivery": true, "open_secrets": true,
		"choose_model": true, "add_assertions": true, "run_live": true,
		"open_studio": true, "open_preflight": true, "reveal_node": true,
		"restrict_to_internal_channels": true, "set_intent_gate_deny": true,
		"write_helper_prompt": true,
	}
	for _, st := range []InstallState{fresh(), working()} {
		for _, id := range Pages() {
			pt, _ := Narrate(id, st)
			if pt.NextAction != "" && !known[pt.NextAction] {
				t.Errorf("%s offers action %q, which is not in the shared vocabulary", id, pt.NextAction)
			}
		}
	}
}
