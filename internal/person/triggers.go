package person

import (
	"fmt"
	"strings"
	"time"
)

// Trigger evaluation: deciding whether a change to the model is worth
// starting an agent for.
//
// The whole value is in *not* firing. A scheduled briefing runs whether or
// not anything happened, and a person learns to ignore it. A trigger that
// fires on every observation is worse, because a phone catching up after an
// hour offline can deliver forty of them at once.

// TriggerCondition is one thing worth waking an agent for.
type TriggerCondition struct {
	// When is one of the agent package's condition names.
	When string
	// Within bounds "soon" for a due-date condition.
	Within time.Duration
}

// DefaultTriggerWithin is how close a commitment must be to count as due
// soon, when an agent does not say.
const DefaultTriggerWithin = 24 * time.Hour

// DefaultTriggerCooldown is the shortest gap between two runs for one person.
// An hour, because a phone that has been offline delivers a burst of
// observations and every one of them changes the model.
const DefaultTriggerCooldown = time.Hour

// Fire describes why a trigger matched, in words the agent can be told.
type Fire struct {
	When string
	// Reason is one sentence naming the change, which becomes the agent's
	// inbound message: an agent should know why it was woken.
	Reason string
	// Entry is the line that caused it.
	Entry Entry
}

// Evaluate reports which conditions the change from `before` to `after`
// satisfies. Both are the same owner's model, read either side of a digest.
//
// Comparing two snapshots rather than watching writes is deliberate: an
// observer that rewrites an identical line every minute must not look like a
// change, and the model is small enough that a diff is cheap.
func Evaluate(before, after Model, conditions []TriggerCondition, now time.Time) []Fire {
	var fires []Fire
	for _, condition := range conditions {
		switch condition.When {
		case "state.changed":
			if fire := stateChanged(before, after); fire != nil {
				fires = append(fires, *fire)
			}
		case "commitment.due":
			within := condition.Within
			if within <= 0 {
				within = DefaultTriggerWithin
			}
			fires = append(fires, commitmentsDue(before, after, within, now)...)
		case "routine.deviation":
			if fire := routineDeviation(before, after); fire != nil {
				fires = append(fires, *fire)
			}
		}
	}
	return fires
}

func entryFor(model Model, section Section, key string) (Entry, bool) {
	for _, entry := range model.Sections[section] {
		if entry.Key == key {
			return entry, true
		}
	}
	return Entry{}, false
}

// stateChanged fires when "right now" became something else. Appearing for
// the first time counts; disappearing does not, because an expired state is
// the absence of news rather than news.
func stateChanged(before, after Model) *Fire {
	now, ok := entryFor(after, SectionState, "now")
	if !ok {
		return nil
	}
	if was, existed := entryFor(before, SectionState, "now"); existed && was.Summary == now.Summary {
		return nil
	}
	return &Fire{
		When:   "state.changed",
		Reason: "Something changed about how they are right now: " + now.Summary + ".",
		Entry:  now,
	}
}

// commitmentsDue fires for each commitment that has just come inside the
// window. A commitment already inside it last time is not news: it fired
// then, and firing again every few minutes until the deadline is how an
// assistant becomes an alarm clock nobody trusts.
func commitmentsDue(before, after Model, within time.Duration, now time.Time) []Fire {
	var fires []Fire
	for _, entry := range after.Sections[SectionCommitments] {
		due, ok := commitmentDue(entry)
		if !ok || due.After(now.Add(within)) {
			continue
		}
		if was, existed := entryFor(before, SectionCommitments, entry.Key); existed {
			if wasDue, ok := commitmentDue(was); ok && !wasDue.After(now.Add(within)) {
				continue // already inside the window last time
			}
		}
		fires = append(fires, Fire{
			When:   "commitment.due",
			Reason: "A commitment is coming up: " + entry.Summary + ".",
			Entry:  entry,
		})
	}
	return fires
}

func commitmentDue(entry Entry) (time.Time, bool) {
	raw, ok := entry.Value["due"].(string)
	if !ok || raw == "" {
		return time.Time{}, false
	}
	due, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return due.UTC(), true
}

// routineDeviation fires when today's departure from the usual shape appears
// or grows. It does not fire again for the same deviation.
func routineDeviation(before, after Model) *Fire {
	deviation, ok := entryFor(after, SectionRoutine, "today.deviation")
	if !ok {
		return nil
	}
	if was, existed := entryFor(before, SectionRoutine, "today.deviation"); existed && was.Summary == deviation.Summary {
		return nil
	}
	return &Fire{
		When:   "routine.deviation",
		Reason: "Today is not going the usual way: " + deviation.Summary + ".",
		Entry:  deviation,
	}
}

// ParseWithin reads a duration from an agent definition, tolerating an empty
// value. An unparseable one is an error rather than a silent default: a
// person who wrote "2 hours" should be told, not quietly given 24.
func ParseWithin(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultTriggerWithin, nil
	}
	within, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("person trigger: within must be a duration like \"2h\": %w", err)
	}
	if within <= 0 {
		return 0, fmt.Errorf("person trigger: within must be positive")
	}
	return within, nil
}

// ParseCooldown is the same for the gap between runs.
func ParseCooldown(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultTriggerCooldown, nil
	}
	cooldown, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("person trigger: cooldown must be a duration like \"4h\": %w", err)
	}
	if cooldown < 0 {
		return 0, fmt.Errorf("person trigger: cooldown must not be negative")
	}
	return cooldown, nil
}
