package runtime

import (
	"strings"
	"testing"
)

// The reported failure: the conversation collected the purpose, the topic and
// the destination, then asked for the purpose again as if it had just started.
// The model's only memory was the raw text of its own previous replies, so one
// truncated turn erased the thread.
func TestEstablishedFactsAreHandedBackEveryTurn(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:    "ai-news-digest",
		Purpose: "Summarise AI news each morning.",
		Trigger: &BuilderTrigger{Type: "cron", Schedule: "0 7 * * *"},
		Outputs: []BuilderOutput{{Channel: "push"}},
		Missing: []string{"exact time"},
	}
	brief := understandingBrief(u)

	for _, want := range []string{"ai-news-digest", "Summarise AI news each morning.", "0 7 * * *", "push", "exact time"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief should carry %q so it is not asked for again:\n%s", want, brief)
		}
	}
	// It must be an instruction, not a note, or the model drops fields it was
	// given and the understanding shrinks turn by turn.
	if !strings.Contains(brief, "Repeat every field") {
		t.Error("the brief should tell the model to carry the fields forward")
	}
	if !strings.Contains(brief, "do not ask about these again") {
		t.Error("the brief should say plainly that these are settled")
	}
}

func TestNothingKnownYieldsNoBrief(t *testing.T) {
	if got := understandingBrief(nil); got != "" {
		t.Errorf("no understanding means nothing to restate, got %q", got)
	}
	if got := understandingBrief(&BuilderUnderstanding{}); got != "" {
		t.Errorf("an empty understanding should produce no brief, got %q", got)
	}
}

func TestBriefOmitsFieldsThatAreStillBlank(t *testing.T) {
	brief := understandingBrief(&BuilderUnderstanding{Purpose: "Watch a page."})
	if strings.Contains(brief, "Agent name") {
		t.Errorf("a blank field must not be presented as established:\n%s", brief)
	}
	if !strings.Contains(brief, "Watch a page.") {
		t.Errorf("the known field should be there:\n%s", brief)
	}
}

// "7am" is an answer. Leaving the field empty and asking again is how the
// builder lost people: they had said it, and it kept asking.
func TestPlainTimesBecomeSchedules(t *testing.T) {
	cases := map[string]string{
		"7am":              "0 7 * * *",
		"at 7 am":          "0 7 * * *",
		"7:30am":           "30 7 * * *",
		"6pm":              "0 18 * * *",
		"12am":             "0 0 * * *",
		"12pm":             "0 12 * * *",
		"send it at 09:15": "15 9 * * *",
		"make it 16:45":    "45 16 * * *",
	}
	for in, want := range cases {
		if got := cronFromPlainTime(in); got != want {
			t.Errorf("cronFromPlainTime(%q) = %q, want %q", in, got, want)
		}
	}
}

// A bare number is not a time. Scheduling an agent for an hour nobody named is
// worse than asking one more question.
func TestAmbiguousTextIsLeftToTheModel(t *testing.T) {
	for _, in := range []string{
		"every morning", "AI", "the top 5 stories", "", "soon", "daily",
	} {
		if got := cronFromPlainTime(in); got != "" {
			t.Errorf("cronFromPlainTime(%q) = %q, want no guess", in, got)
		}
	}
}

func TestImpossibleTimesAreRejected(t *testing.T) {
	for _, in := range []string{"at 99:00", "25:00", "7:99"} {
		if got := cronFromPlainTime(in); got != "" {
			t.Errorf("cronFromPlainTime(%q) = %q, want none", in, got)
		}
	}
}

// Delivery must never be the thing that blocks a build: a first-run user has
// no channels configured, and results appear in Soulacy by default.
func TestPromptDoesNotBlockOnADeliveryChannel(t *testing.T) {
	if !strings.Contains(builderSystemPrompt, "Never\nblock on a delivery channel") &&
		!strings.Contains(builderSystemPrompt, "Never block on a delivery channel") {
		t.Error("the prompt should say delivery is not a blocker")
	}
	if !strings.Contains(builderSystemPrompt, "Results appear in Soulacy itself") {
		t.Error("the prompt should name the default destination")
	}
}
