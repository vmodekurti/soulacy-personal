package runtime

import (
	"strings"
	"testing"
)

// The case that made the product contradict itself: the conversation declared
// an agent ready while leaving the cron expression blank, and deploy then
// refused it. To a user that is the assistant saying yes and the product
// saying no, with nothing to do about it.
func TestCronAgentWithoutAScheduleIsNotReady(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:         "week-notes",
		SystemPrompt: "Summarise the week.",
		Confidence:   0.95,
		Trigger:      &BuilderTrigger{Type: "cron"},
	}
	gaps := buildableGaps(u)
	if len(gaps) != 1 {
		t.Fatalf("want exactly the schedule gap, got %v", gaps)
	}
	// Worded as an instruction with the field name and an example, because a
	// smaller model writes "every Friday at 4pm" in prose and leaves the field
	// empty. Naming the field is what gets it filled in.
	if !strings.Contains(gaps[0], "trigger.schedule") {
		t.Errorf("the gap should name the field to fill; got %q", gaps[0])
	}
	if !strings.Contains(gaps[0], "* * 5") {
		t.Errorf("the gap should show the shape of a cron expression; got %q", gaps[0])
	}
}

func TestCronAgentWithAScheduleHasNoGaps(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:         "week-notes",
		SystemPrompt: "Summarise the week.",
		Trigger:      &BuilderTrigger{Type: "cron", Schedule: "0 16 * * 5"},
	}
	if gaps := buildableGaps(u); len(gaps) != 0 {
		t.Fatalf("a complete cron agent should have no gaps, got %v", gaps)
	}
}

// A chat agent needs no schedule, so demanding one would block the commonest
// kind of agent there is.
func TestChannelAgentNeedsNoSchedule(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:         "helper",
		SystemPrompt: "Answer questions.",
		Trigger:      &BuilderTrigger{Type: "channel"},
	}
	if gaps := buildableGaps(u); len(gaps) != 0 {
		t.Fatalf("a channel agent needs no schedule, got %v", gaps)
	}
}

func TestAnAgentNeedsANameAndInstructions(t *testing.T) {
	gaps := buildableGaps(&BuilderUnderstanding{Trigger: &BuilderTrigger{Type: "channel"}})
	joined := strings.Join(gaps, "|")
	if !strings.Contains(joined, "name") {
		t.Errorf("a nameless agent cannot be written to disk; gaps = %v", gaps)
	}
	if !strings.Contains(joined, "actually do") {
		t.Errorf("an agent with no instructions does nothing; gaps = %v", gaps)
	}
}

func TestNoUnderstandingIsItsOwnGap(t *testing.T) {
	if gaps := buildableGaps(nil); len(gaps) == 0 {
		t.Fatal("nothing understood yet is not ready")
	}
}
