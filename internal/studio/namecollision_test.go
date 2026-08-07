package studio

// A new draft must not quietly take over an agent that already exists.
//
// ToAgentDefinition derives a new draft's id from slug(Name), and the save path
// then updates in place when that id is already on disk. Correct for a re-save;
// silent replacement of a working agent for a draft that has never been saved.
//
// Seen live: describing a scheduled stock briefing produced a draft the builder
// named "Stock Advisor" — a name already deployed on that install. Saving would
// have overwritten it. Nothing on the Save step mentioned it; the dialog listed
// tool-argument blockers and said nothing about the agent about to be replaced.

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func collisionDraft(name, id string) Draft {
	return Draft{
		ID: id, Name: name, Trigger: Trigger{Type: "manual"},
		Flow: Flow{Entry: "n", Nodes: []sdkr.FlowNode{{ID: "n", Kind: "llm", Input: "hi", Output: "out"}}},
	}
}

func checkFor(r ContractResult, id string) (ContractCheck, bool) {
	for _, c := range r.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return ContractCheck{}, false
}

func TestContract_BlocksANewDraftThatWouldReplaceAnExistingAgent(t *testing.T) {
	cat := Catalog{Agents: []string{"summarizer", "stock-advisor", "notifier"}}
	r := AssessContract(collisionDraft("Stock Advisor", ""), cat, PreflightInput{Catalog: cat})

	c, ok := checkFor(r, "identity.collision")
	if !ok || c.Status != "block" {
		t.Fatalf("saving this would overwrite the deployed \"stock-advisor\" agent and nothing blocked it; checks: %+v", r.Checks)
	}
	if c.Action != FixRenameAgent {
		t.Errorf("a blocker the user cannot act on is half a warning; action=%q", c.Action)
	}
	if c.ActionLabel == "" {
		t.Error("the button would render with no text on it")
	}
	// The remedy has to say the existing agent survives if they rename. Without
	// that, the honest reading is "you have already broken something".
	if !strings.Contains(strings.ToLower(c.Fix), "keeps running") {
		t.Errorf("the fix should say the existing agent is untouched, got: %s", c.Fix)
	}
}

// The id is derived by slug(), so the collision is about the ID, not the exact
// spelling of the name.
func TestContract_CollisionIsJudgedOnTheDerivedID(t *testing.T) {
	cat := Catalog{Agents: []string{"stock-advisor"}}
	for _, name := range []string{"Stock Advisor", "stock advisor", "  Stock   Advisor  ", "STOCK-ADVISOR"} {
		r := AssessContract(collisionDraft(name, ""), cat, PreflightInput{Catalog: cat})
		if c, ok := checkFor(r, "identity.collision"); !ok || c.Status != "block" {
			t.Errorf("%q slugs onto the existing agent id but did not block", name)
		}
	}
}

// Re-saving an agent you opened for editing is the normal case and must stay
// silent. A false blocker here would make every edit unsaveable.
func TestContract_DoesNotBlockResavingTheAgentYouOpened(t *testing.T) {
	cat := Catalog{Agents: []string{"stock-advisor"}}
	r := AssessContract(collisionDraft("Stock Advisor", "stock-advisor"), cat, PreflightInput{Catalog: cat})

	if c, ok := checkFor(r, "identity.collision"); ok && c.Status == "block" {
		t.Fatal("editing an existing agent was reported as a collision — every re-save would be blocked")
	}
}

// A name nobody is using passes, visibly. A check that only ever speaks up when
// something is wrong leaves the user unsure it ran at all.
func TestContract_ReportsAClearNameAsAPass(t *testing.T) {
	cat := Catalog{Agents: []string{"stock-advisor"}}
	r := AssessContract(collisionDraft("Market Digest Workflow", ""), cat, PreflightInput{Catalog: cat})

	c, ok := checkFor(r, "identity.collision")
	if !ok || c.Status != "pass" {
		t.Fatalf("expected a passing name-collision check, got %+v", c)
	}
}

// An empty catalog means we do not know what exists — it is not evidence that
// the name is free, but it must not invent a blocker either.
func TestContract_StaysQuietWhenNoAgentsAreKnown(t *testing.T) {
	r := AssessContract(collisionDraft("Stock Advisor", ""), Catalog{}, PreflightInput{})
	if c, ok := checkFor(r, "identity.collision"); ok && c.Status == "block" {
		t.Fatal("blocked on a collision with an agent list we never had")
	}
}
