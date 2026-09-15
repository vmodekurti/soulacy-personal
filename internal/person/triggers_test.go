package person

import (
	"strings"
	"testing"
	"time"
)

func model(entries ...Entry) Model {
	return NewModel("kai", entries, time.Now().UTC())
}

func stateEntry(summary string) Entry {
	return Entry{Owner: "kai", Section: SectionState, Key: "now", Summary: summary, Source: "sense:state", Confidence: 0.8}
}

func commitment(key, summary string, due time.Time) Entry {
	e := Entry{Owner: "kai", Section: SectionCommitments, Key: key, Summary: summary, Source: "sense:commitments", Confidence: 0.9}
	if !due.IsZero() {
		e.Value = map[string]any{"due": due.Format(time.RFC3339)}
	}
	return e
}

func TestStateTriggerFiresOnChangeAndNotOnRepetition(t *testing.T) {
	now := time.Now().UTC()
	conditions := []TriggerCondition{{When: "state.changed"}}

	// Appearing for the first time is a change.
	fires := Evaluate(model(), model(stateEntry("Driving")), conditions, now)
	if len(fires) != 1 || !strings.Contains(fires[0].Reason, "Driving") {
		t.Fatalf("first sighting should fire: %+v", fires)
	}

	// The same state observed again is not.
	if fires := Evaluate(model(stateEntry("Driving")), model(stateEntry("Driving")), conditions, now); len(fires) != 0 {
		t.Fatalf("an unchanged state must not fire: %+v", fires)
	}

	// A different state is.
	fires = Evaluate(model(stateEntry("Driving")), model(stateEntry("In Work Focus")), conditions, now)
	if len(fires) != 1 || !strings.Contains(fires[0].Reason, "In Work Focus") {
		t.Fatalf("a changed state should fire: %+v", fires)
	}

	// Losing the state is the absence of news, not news.
	if fires := Evaluate(model(stateEntry("Driving")), model(), conditions, now); len(fires) != 0 {
		t.Fatalf("an expired state must not fire: %+v", fires)
	}
}

func TestCommitmentTriggerFiresOnceAsADeadlineApproaches(t *testing.T) {
	now := time.Now().UTC()
	conditions := []TriggerCondition{{When: "commitment.due", Within: 24 * time.Hour}}

	far := commitment("r1", "Send the proposal", now.Add(72*time.Hour))
	near := commitment("r1", "Send the proposal", now.Add(6*time.Hour))

	// Outside the window: nothing.
	if fires := Evaluate(model(), model(far), conditions, now); len(fires) != 0 {
		t.Fatalf("a distant deadline is not news: %+v", fires)
	}

	// Crossing into the window: fire.
	fires := Evaluate(model(far), model(near), conditions, now)
	if len(fires) != 1 || !strings.Contains(fires[0].Reason, "Send the proposal") {
		t.Fatalf("crossing into the window should fire: %+v", fires)
	}

	// Still inside it next time: silent. This is what stops an assistant
	// becoming an alarm clock.
	if fires := Evaluate(model(near), model(near), conditions, now); len(fires) != 0 {
		t.Fatalf("a commitment already inside the window must not fire again: %+v", fires)
	}

	// A commitment with no due date never fires on this condition.
	undated := commitment("r2", "Renew the passport", time.Time{})
	if fires := Evaluate(model(), model(undated), conditions, now); len(fires) != 0 {
		t.Fatalf("no deadline means no deadline trigger: %+v", fires)
	}
}

func TestCommitmentTriggerRespectsItsWindow(t *testing.T) {
	now := time.Now().UTC()
	soon := commitment("r1", "Pay the bill", now.Add(3*time.Hour))

	tight := []TriggerCondition{{When: "commitment.due", Within: time.Hour}}
	if fires := Evaluate(model(), model(soon), tight, now); len(fires) != 0 {
		t.Fatalf("three hours is outside a one-hour window: %+v", fires)
	}
	wide := []TriggerCondition{{When: "commitment.due", Within: 12 * time.Hour}}
	if fires := Evaluate(model(), model(soon), wide, now); len(fires) != 1 {
		t.Fatalf("three hours is inside a twelve-hour window: %+v", fires)
	}
	// An unset window falls back to a day rather than firing on everything.
	if fires := Evaluate(model(), model(soon), []TriggerCondition{{When: "commitment.due"}}, now); len(fires) != 1 {
		t.Fatalf("default window: %+v", fires)
	}
}

func TestRoutineDeviationFiresOnceUntilItChanges(t *testing.T) {
	now := time.Now().UTC()
	conditions := []TriggerCondition{{When: "routine.deviation"}}
	deviation := func(summary string) Entry {
		return Entry{Owner: "kai", Section: SectionRoutine, Key: "today.deviation", Summary: summary, Source: "sense:routine", Confidence: 0.7}
	}

	fires := Evaluate(model(), model(deviation("Left home 90 minutes later than usual today")), conditions, now)
	if len(fires) != 1 || !strings.Contains(fires[0].Reason, "not going the usual way") {
		t.Fatalf("a new deviation should fire: %+v", fires)
	}
	same := model(deviation("Left home 90 minutes later than usual today"))
	if fires := Evaluate(same, same, conditions, now); len(fires) != 0 {
		t.Fatalf("the same deviation must not fire twice: %+v", fires)
	}
	grew := model(deviation("Left home 140 minutes later than usual today"))
	if fires := Evaluate(same, grew, conditions, now); len(fires) != 1 {
		t.Fatalf("a changed deviation should fire: %+v", fires)
	}
}

func TestAnUnknownConditionFiresNothing(t *testing.T) {
	now := time.Now().UTC()
	fires := Evaluate(model(), model(stateEntry("Driving")), []TriggerCondition{{When: "phase.of.the.moon"}}, now)
	if len(fires) != 0 {
		t.Fatalf("an unknown condition must be inert, not a wildcard: %+v", fires)
	}
}

func TestSeveralConditionsAreEvaluatedTogether(t *testing.T) {
	now := time.Now().UTC()
	after := model(stateEntry("Driving"), commitment("r1", "Pay the bill", now.Add(2*time.Hour)))
	fires := Evaluate(model(), after, []TriggerCondition{
		{When: "state.changed"}, {When: "commitment.due", Within: 24 * time.Hour},
	}, now)
	if len(fires) != 2 {
		t.Fatalf("both conditions should report: %+v", fires)
	}
}

func TestDurationParsingIsStrictAboutNonsense(t *testing.T) {
	if d, err := ParseWithin(""); err != nil || d != DefaultTriggerWithin {
		t.Fatalf("empty within should default: %v %v", d, err)
	}
	if d, err := ParseWithin("2h"); err != nil || d != 2*time.Hour {
		t.Fatalf("within: %v %v", d, err)
	}
	// A person who writes "2 hours" should be told, not quietly given a day.
	if _, err := ParseWithin("2 hours"); err == nil {
		t.Fatal("an unparseable duration must be an error")
	}
	if _, err := ParseWithin("-1h"); err == nil {
		t.Fatal("a negative window is meaningless")
	}
	if d, err := ParseCooldown(""); err != nil || d != DefaultTriggerCooldown {
		t.Fatalf("empty cooldown should default: %v %v", d, err)
	}
	if d, err := ParseCooldown("0s"); err != nil || d != 0 {
		t.Fatalf("an explicit zero cooldown is allowed: %v %v", d, err)
	}
	if _, err := ParseCooldown("soon"); err == nil {
		t.Fatal("an unparseable cooldown must be an error")
	}
}
