package person

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// An Observation is one raw signal from a device, before anything has been
// concluded from it.
//
// The gateway does not pull these. Device commands only run while the app is
// in the foreground, so a scheduled pull would see nothing most of the day.
// The phone pushes instead: region crossings and Focus changes arrive in the
// background, health once a day, the rest whenever the app is open.
type Observation struct {
	Owner string `json:"owner"`
	// Kind names the sense: "arrival", "departure", "focus", "motion",
	// "sleep", "calendar". Unknown kinds are stored and ignored, so an older
	// gateway never rejects a newer phone.
	Kind string `json:"kind"`
	// At is when the signal happened on the device, which may be well before
	// it could be delivered.
	At time.Time `json:"at"`
	// Payload is the sense's own shape, e.g. {"place":"home"} for an arrival.
	Payload map[string]any `json:"payload,omitempty"`
}

// Known observation kinds. Listed so the API can say what it understands.
const (
	KindArrival   = "arrival"
	KindDeparture = "departure"
	KindFocus     = "focus"
	KindMotion    = "motion"
	KindSleep     = "sleep"
	KindCalendar  = "calendar"
	KindReminder  = "reminder"
)

// KnownKinds is every kind an observer currently digests.
var KnownKinds = []string{KindArrival, KindDeparture, KindFocus, KindMotion, KindSleep, KindCalendar, KindReminder}

// ObservationRetention bounds how far back the buffer is kept. Routines need
// weeks of history to have an opinion; nothing needs months, and a device
// signal is the most sensitive data here, so it does not linger.
const ObservationRetention = 35 * 24 * time.Hour

// Normalize fills in defaults and lower-cases the kind.
func (o Observation) Normalize(now time.Time) Observation {
	o.Owner = strings.TrimSpace(o.Owner)
	o.Kind = strings.ToLower(strings.TrimSpace(o.Kind))
	if o.At.IsZero() {
		o.At = now
	}
	o.At = o.At.UTC()
	return o
}

// Validate rejects what cannot be attributed or dated sensibly.
func (o Observation) Validate(now time.Time) error {
	if o.Owner == "" {
		return ErrInvalidOwner
	}
	if o.Kind == "" {
		return fmt.Errorf("%w: observation kind is required", ErrInvalidEntry)
	}
	if o.At.After(now.Add(time.Hour)) {
		// A device clock may be a little off; an hour of tolerance is plenty,
		// and a signal from tomorrow would poison every window it touches.
		return fmt.Errorf("%w: observation is from the future", ErrInvalidEntry)
	}
	return nil
}

// String returns the payload value for key, or "".
func (o Observation) String(key string) string {
	if v, ok := o.Payload[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// Bool returns the payload value for key, or false.
func (o Observation) Bool(key string) bool {
	v, _ := o.Payload[key].(bool)
	return v
}

// Time parses an RFC 3339 payload value, or returns the zero time.
func (o Observation) Time(key string) time.Time {
	raw := o.String(key)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// Number returns the payload value for key as a float, or 0.
func (o Observation) Number(key string) float64 {
	switch v := o.Payload[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

// An Observer turns observations into model entries. Deliberately an
// interface over plain Go rather than an agent: "what time do they usually
// leave" is a median, not a judgement, and a rule cannot hallucinate.
type Observer interface {
	// Sense is the name that appears in sources ("sense:routine") and in the
	// consent switch the person controls.
	Sense() string
	// Kinds lists the observation kinds this observer reads.
	Kinds() []string
	// Digest returns the entries concluded from the observations given. It
	// must be pure: the same input yields the same output, which is what
	// makes it testable without a phone.
	Digest(owner string, observations []Observation, now time.Time) []Entry
}

// Observers is the default set.
func Observers() []Observer {
	return []Observer{StateObserver{}, RoutineObserver{}, CommitmentsObserver{}}
}

// Digest runs every enabled observer over the owner's recent observations and
// writes what they conclude. Refusals (a person's own entry outranking a
// sense) are counted, not errors.
func Digest(ctx context.Context, store Store, buffer ObservationStore, owner string, observers []Observer, enabled func(sense string) bool, now time.Time) (applied int, refused int, err error) {
	for _, observer := range observers {
		if enabled != nil && !enabled(observer.Sense()) {
			continue
		}
		observations, err := buffer.Observations(ctx, owner, observer.Kinds(), now.Add(-ObservationRetention))
		if err != nil {
			return applied, refused, err
		}
		entries := observer.Digest(owner, observations, now)
		if len(entries) == 0 {
			continue
		}
		results, err := store.PutAll(ctx, entries)
		if err != nil {
			return applied, refused, err
		}
		for _, result := range results {
			if result.Applied {
				applied++
			} else {
				refused++
			}
		}
	}
	return applied, refused, nil
}

// ObservationStore buffers raw signals until observers digest them.
type ObservationStore interface {
	Record(ctx context.Context, observations []Observation) (int, error)
	// Observations returns the owner's signals of these kinds since a time,
	// oldest first. An empty kinds slice means every kind.
	Observations(ctx context.Context, owner string, kinds []string, since time.Time) ([]Observation, error)
	// PruneObservations drops anything older than the retention window.
	PruneObservations(ctx context.Context, before time.Time) (int, error)
	// Senses reports which observers the owner has consented to. A sense with
	// no stored preference is off: nothing watches until asked.
	Senses(ctx context.Context, owner string) (map[string]bool, error)
	SetSense(ctx context.Context, owner, sense string, enabled bool) error
}

// --- state -----------------------------------------------------------------

// StateObserver answers "how are they right now" from the most recent Focus,
// motion and sleep signals.
type StateObserver struct{}

func (StateObserver) Sense() string   { return "state" }
func (StateObserver) Kinds() []string { return []string{KindFocus, KindMotion, KindSleep} }

// StateFreshness is how long a conclusion about "now" stays true. Short on
// purpose: a stale "commuting" is worse than saying nothing.
const StateFreshness = 45 * time.Minute

func (o StateObserver) Digest(owner string, observations []Observation, now time.Time) []Entry {
	latest := latestByKind(observations)
	focus, hasFocus := latest[KindFocus]
	motion, hasMotion := latest[KindMotion]

	// Only conclude from signals that are themselves recent. An arrival from
	// this morning says nothing about this afternoon.
	fresh := func(o Observation, ok bool) bool { return ok && now.Sub(o.At) < StateFreshness }

	var summary string
	var confidence float32 = 0.7
	switch {
	case fresh(motion, hasMotion) && motion.String("activity") == "automotive":
		summary = "Driving"
		confidence = 0.8
	case fresh(motion, hasMotion) && (motion.String("activity") == "walking" || motion.String("activity") == "running"):
		summary = "Moving about"
	case fresh(focus, hasFocus) && focus.String("mode") != "" && focus.String("mode") != "none":
		summary = "In " + focus.String("mode") + " Focus"
		confidence = 0.9
	case fresh(motion, hasMotion) && motion.String("activity") == "stationary":
		summary = "Settled somewhere"
		confidence = 0.6
	}
	if summary == "" {
		return nil
	}

	expires := now.Add(StateFreshness)
	entries := []Entry{{
		Owner: owner, Section: SectionState, Key: "now", Summary: summary,
		Source: SourceSensePrefix + o.Sense(), Confidence: confidence,
		ObservedAt: now, ExpiresAt: &expires,
		Value: map[string]any{"focus": focus.String("mode"), "activity": motion.String("activity")},
	}}

	// Sleep is reported once a day and is worth keeping longer than "now".
	if sleep, ok := latest[KindSleep]; ok && now.Sub(sleep.At) < 20*time.Hour {
		if hours := sleep.Number("hours"); hours > 0 {
			expires := sleep.At.Add(20 * time.Hour)
			entries = append(entries, Entry{
				Owner: owner, Section: SectionState, Key: "rest",
				Summary:    fmt.Sprintf("Slept about %.1f hours last night", hours),
				Source:     SourceSensePrefix + o.Sense(),
				Confidence: 0.8, ObservedAt: sleep.At, ExpiresAt: &expires,
				Value: map[string]any{"hours": hours},
			})
		}
	}
	return entries
}

// --- routine ---------------------------------------------------------------

// RoutineObserver learns the shape of a normal day from arrivals and
// departures, per weekday, and says how far today departs from it.
type RoutineObserver struct{}

func (RoutineObserver) Sense() string   { return "routine" }
func (RoutineObserver) Kinds() []string { return []string{KindArrival, KindDeparture} }

// MinRoutineSamples is how many matching days are needed before the observer
// will claim anything is "usual". Two points are a coincidence.
const MinRoutineSamples = 4

// habit keys one repeated behaviour: leaving home on a Tuesday, say.
type habit struct {
	weekday time.Weekday
	kind    string
	place   string
}

func (o RoutineObserver) Digest(owner string, observations []Observation, now time.Time) []Entry {
	minutes := map[habit][]int{}
	var todays []Observation
	today := now.Truncate(24 * time.Hour)

	for _, observation := range observations {
		place := observation.String("place")
		if place == "" {
			continue
		}
		local := observation.At
		if local.After(today) {
			todays = append(todays, observation)
			// Today is what we are comparing against the habit, so it must
			// not also be part of the habit.
			continue
		}
		key := habit{weekday: local.Weekday(), kind: observation.Kind, place: place}
		minutes[key] = append(minutes[key], local.Hour()*60+local.Minute())
	}

	var entries []Entry
	for key, samples := range minutes {
		if len(samples) < MinRoutineSamples {
			continue
		}
		typical := median(samples)
		spread := spreadMinutes(samples, typical)
		// A habit with an hour of scatter is not a habit worth reporting.
		if spread > 60 {
			continue
		}
		entries = append(entries, Entry{
			Owner: owner, Section: SectionRoutine,
			Key:        fmt.Sprintf("%s.%s.%s", strings.ToLower(key.weekday.String()), key.kind, key.place),
			Summary:    fmt.Sprintf("%s %s %s around %s", key.weekday, verbFor(key.kind), key.place, clock(typical)),
			Source:     SourceSensePrefix + o.Sense(),
			Confidence: routineConfidence(len(samples), spread),
			ObservedAt: now,
			Value: map[string]any{
				"weekday": key.weekday.String(), "kind": key.kind, "place": key.place,
				"typical_minute": typical, "spread_minutes": spread, "samples": len(samples),
			},
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })

	// Today's deviation, which is the part an agent actually acts on.
	if deviation := o.deviation(owner, todays, minutes, now); deviation != nil {
		entries = append(entries, *deviation)
	}
	return entries
}

func (o RoutineObserver) deviation(owner string, todays []Observation, habits map[habit][]int, now time.Time) *Entry {
	var worst int
	var worstText string
	for _, observation := range todays {
		place := observation.String("place")
		samples := habits[habit{weekday: observation.At.Weekday(), kind: observation.Kind, place: place}]
		if len(samples) < MinRoutineSamples {
			continue
		}
		typical := median(samples)
		actual := observation.At.Hour()*60 + observation.At.Minute()
		delta := actual - typical
		if abs(delta) < 30 || abs(delta) <= worst {
			continue
		}
		worst = abs(delta)
		when := "earlier"
		if delta > 0 {
			when = "later"
		}
		worstText = fmt.Sprintf("%s %s about %d minutes %s than usual today",
			verbForPast(observation.Kind), place, abs(delta), when)
	}
	if worstText == "" {
		return nil
	}
	expires := now.Add(12 * time.Hour)
	return &Entry{
		Owner: owner, Section: SectionRoutine, Key: "today.deviation",
		Summary: strings.ToUpper(worstText[:1]) + worstText[1:],
		Source:  SourceSensePrefix + o.Sense(), Confidence: 0.7,
		ObservedAt: now, ExpiresAt: &expires,
		Value: map[string]any{"minutes": worst},
	}
}

func verbFor(kind string) string {
	if kind == KindDeparture {
		return "leaves"
	}
	return "arrives at"
}

func verbForPast(kind string) string {
	if kind == KindDeparture {
		return "left"
	}
	return "arrived at"
}

func routineConfidence(samples, spread int) float32 {
	confidence := 0.5 + float32(samples)*0.05
	if spread < 15 {
		confidence += 0.15
	}
	if confidence > 0.95 {
		confidence = 0.95
	}
	return confidence
}

func latestByKind(observations []Observation) map[string]Observation {
	latest := map[string]Observation{}
	for _, observation := range observations {
		if existing, ok := latest[observation.Kind]; !ok || observation.At.After(existing.At) {
			latest[observation.Kind] = observation
		}
	}
	return latest
}

func median(values []int) int {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

func spreadMinutes(values []int, centre int) int {
	worst := 0
	for _, value := range values {
		if d := abs(value - centre); d > worst {
			worst = d
		}
	}
	return worst
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func clock(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

// --- commitments -----------------------------------------------------------

// CommitmentsObserver keeps the model's list of what the person is on the
// hook for, from the reminders their phone already holds.
//
// The value is not that a reminder exists — they have an app for that. It is
// that an agent can see it without the phone being awake and in the
// foreground, which is when device commands work and when almost nothing
// useful happens.
type CommitmentsObserver struct{}

func (CommitmentsObserver) Sense() string   { return "commitments" }
func (CommitmentsObserver) Kinds() []string { return []string{KindReminder} }

// CommitmentGrace is how long a commitment stays in the model after its due
// date. Something overdue is exactly what an assistant should still be
// raising; something a month overdue is noise.
const CommitmentGrace = 14 * 24 * time.Hour

func (o CommitmentsObserver) Digest(owner string, observations []Observation, now time.Time) []Entry {
	// One entry per reminder, from the most recent sighting of it. A phone
	// re-sends its whole list, so older sightings are stale by definition.
	latest := map[string]Observation{}
	for _, observation := range observations {
		id := observation.String("id")
		if id == "" {
			continue
		}
		if existing, ok := latest[id]; !ok || observation.At.After(existing.At) {
			latest[id] = observation
		}
	}

	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		observation := latest[id]
		title := observation.String("title")
		if title == "" {
			continue
		}
		due := observation.Time("due")
		entry := Entry{
			Owner: owner, Section: SectionCommitments, Key: "reminder." + id,
			Summary:    commitmentSummary(title, due, now),
			Source:     SourceSensePrefix + o.Sense(),
			Confidence: 0.9, ObservedAt: observation.At,
			Value: map[string]any{"title": title, "reminder_id": id},
		}
		if !due.IsZero() {
			entry.Value["due"] = due.Format(time.RFC3339)
			expires := due.Add(CommitmentGrace)
			entry.ExpiresAt = &expires
		}
		// A finished reminder is not a commitment. Expiring it rather than
		// leaving it out is what reaches a device as a tombstone, so a phone
		// holding the old copy is told it is done.
		if observation.Bool("done") {
			expired := observation.At
			entry.ExpiresAt = &expired
		}
		entries = append(entries, entry)
	}
	return entries
}

func commitmentSummary(title string, due, now time.Time) string {
	if due.IsZero() {
		return title
	}
	days := int(due.Truncate(24*time.Hour).Sub(now.Truncate(24*time.Hour)).Hours() / 24)
	switch {
	case days < 0:
		return fmt.Sprintf("%s (overdue since %s)", title, due.Format("2 Jan"))
	case days == 0:
		return title + " (due today)"
	case days == 1:
		return title + " (due tomorrow)"
	case days <= 7:
		return fmt.Sprintf("%s (due %s)", title, due.Format("Monday"))
	default:
		return fmt.Sprintf("%s (due %s)", title, due.Format("2 Jan"))
	}
}
