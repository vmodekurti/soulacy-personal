package studio

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func preferenceDraft(instructions string) Draft {
	return Draft{
		SystemPrompt: "You are a useful assistant.",
		Policy: &AgentPolicy{Contract: &AgentContract{
			Goal: "Answer the user's question.", Instructions: instructions,
		}},
	}
}

func TestManualPreferenceAdditionsFindsBehavioralDiff(t *testing.T) {
	initial := preferenceDraft("Use the available tools.")
	final := preferenceDraft("Use the available tools.\nAlways confirm before sending an external message.")
	additions := ManualPreferenceAdditions(initial, final)
	if len(additions) != 1 || additions[0] != "Always confirm before sending an external message." {
		t.Fatalf("additions = %#v", additions)
	}
}

func TestPreferenceMinerRequiresDifferentAgentsAndInjectsRules(t *testing.T) {
	store := NewPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"))
	model := fakeLLM{out: `{"rules":["Always confirm with the user before sending external messages."]}`}
	miner := NewPreferenceMiner(store, model)
	initial := preferenceDraft("Use the available tools.")
	final := preferenceDraft("Use the available tools.\nAlways confirm before sending an external message.")
	if err := miner.Mine(context.Background(), "agent-one", initial, final); err != nil {
		t.Fatal(err)
	}
	if got := store.Rules(); len(got) != 0 {
		t.Fatalf("learned from only one agent: %+v", got)
	}
	if err := miner.Mine(context.Background(), "agent-two", initial, final); err != nil {
		t.Fatal(err)
	}
	rules := store.Rules()
	if len(rules) != 1 || rules[0].Evidence != 2 {
		t.Fatalf("rules = %+v", rules)
	}
	for name, prompt := range map[string]string{
		"refine": BuildRefinePromptInstruction("build an assistant", Catalog{GlobalPreferences: rules}),
		"build":  BuildPrompt("build an assistant", Catalog{GlobalPreferences: rules}, nil),
	} {
		if !strings.Contains(prompt, "GLOBAL USER PREFERENCES") || !strings.Contains(prompt, "Always confirm") {
			t.Fatalf("%s prompt omitted global rule", name)
		}
	}
}

func TestPreferenceStoreDoesNotCountRepeatedEditsFromOneAgent(t *testing.T) {
	store := NewPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"))
	for i := 0; i < 3; i++ {
		if err := store.AddObservation(PreferenceObservation{AgentID: "same", Additions: []string{"Never use markdown in the final answer."}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.RepeatedCandidates(2); len(got) != 0 {
		t.Fatalf("same agent met cross-agent threshold: %v", got)
	}
}

func TestPreferenceRulesAreOwnerScoped(t *testing.T) {
	store := NewPreferenceStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err := store.MergeRulesFor("alice", []string{"Always answer concisely and directly."}, 2); err != nil {
		t.Fatal(err)
	}
	if got := store.RulesFor("bob"); len(got) != 0 {
		t.Fatalf("cross-user rules leaked: %+v", got)
	}
	if got := store.RulesFor("alice"); len(got) != 1 {
		t.Fatalf("alice rules=%+v", got)
	}
}
