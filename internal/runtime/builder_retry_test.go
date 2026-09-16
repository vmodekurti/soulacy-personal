package runtime

import (
	"strings"
	"testing"
)

// Reported from production, on the very first turn: "Sorry — my answer got cut
// off." The guard was doing its job, but the user was still losing a turn to
// an apology. These pin the two things that stop it happening.
func TestPromptDefersTheExpensiveFieldUntilItIsNeeded(t *testing.T) {
	if !strings.Contains(builderSystemPrompt, "set system_prompt to null") {
		t.Error("while still asking questions the model should not write the agent's full instructions")
	}
	// Matched without the words either side of it: the sentence wraps in the
	// prompt, and an assertion that spans a line break fails for a reason that
	// has nothing to do with the instruction being present.
	if !strings.Contains(builderSystemPrompt, "reaches 0.8") {
		t.Error("the prompt should say when to write them instead")
	}
}

// The retry only makes sense for a reply that was trying to be JSON. Plain
// prose is a valid answer and must not be re-requested.
func TestOnlyAMachineReplyCountsAsTruncated(t *testing.T) {
	if !looksLikeJSON(`{"reply":"half`) {
		t.Error("a cut-off envelope is a truncated machine reply")
	}
	if looksLikeJSON("Which calendar should I read?") {
		t.Error("prose is an answer, not a truncation")
	}
}
