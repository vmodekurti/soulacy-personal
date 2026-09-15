package person

import (
	"strings"
	"testing"
	"time"
)

func at(base time.Time, day int, hour, minute int) time.Time {
	return base.AddDate(0, 0, -day).Truncate(24 * time.Hour).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

func observation(kind string, when time.Time, payload map[string]any) Observation {
	return Observation{Owner: "kai", Kind: kind, At: when, Payload: payload}
}

func TestStateObserverOnlyConcludesFromFreshSignals(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	observer := StateObserver{}

	// Nothing at all.
	if entries := observer.Digest("kai", nil, now); len(entries) != 0 {
		t.Fatalf("no signals should conclude nothing: %+v", entries)
	}

	// A Focus from this morning is not evidence about this afternoon.
	stale := []Observation{observation(KindFocus, now.Add(-4*time.Hour), map[string]any{"mode": "Work"})}
	if entries := observer.Digest("kai", stale, now); len(entries) != 0 {
		t.Fatalf("a stale signal must not become 'right now': %+v", entries)
	}

	// Fresh Focus wins, and the conclusion expires.
	fresh := []Observation{observation(KindFocus, now.Add(-5*time.Minute), map[string]any{"mode": "Work"})}
	entries := observer.Digest("kai", fresh, now)
	if len(entries) != 1 || entries[0].Summary != "In Work Focus" {
		t.Fatalf("focus state: %+v", entries)
	}
	if entries[0].ExpiresAt == nil || entries[0].ExpiresAt.After(now.Add(StateFreshness+time.Minute)) {
		t.Fatalf("a state entry must expire soon: %+v", entries[0].ExpiresAt)
	}
	if entries[0].Source != "sense:state" {
		t.Fatalf("source should name the sense: %q", entries[0].Source)
	}

	// Driving outranks Focus: where the person is matters more than a setting.
	driving := append(fresh, observation(KindMotion, now.Add(-time.Minute), map[string]any{"activity": "automotive"}))
	if entries := observer.Digest("kai", driving, now); entries[0].Summary != "Driving" {
		t.Fatalf("motion should win over focus: %+v", entries[0])
	}
}

func TestStateObserverKeepsSleepLongerThanNow(t *testing.T) {
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	entries := StateObserver{}.Digest("kai", []Observation{
		observation(KindFocus, now.Add(-time.Minute), map[string]any{"mode": "Work"}),
		observation(KindSleep, now.Add(-7*time.Hour), map[string]any{"hours": 5.5}),
	}, now)
	if len(entries) != 2 {
		t.Fatalf("expected now + rest: %+v", entries)
	}
	rest := entries[1]
	if !strings.Contains(rest.Summary, "5.5 hours") {
		t.Fatalf("rest summary: %q", rest.Summary)
	}
	if rest.ExpiresAt == nil || rest.ExpiresAt.Before(now.Add(10*time.Hour)) {
		t.Fatalf("last night's sleep should outlive 'now': %+v", rest.ExpiresAt)
	}
}

func TestRoutineObserverNeedsEnoughDaysBeforeClaimingAHabit(t *testing.T) {
	now := time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC) // a Tuesday
	observer := RoutineObserver{}

	// Three Tuesdays is not yet a habit.
	var few []Observation
	for i := 1; i <= 3; i++ {
		few = append(few, observation(KindDeparture, at(now, i*7, 8, 10), map[string]any{"place": "home"}))
	}
	if entries := observer.Digest("kai", few, now); len(entries) != 0 {
		t.Fatalf("three samples must not become a habit: %+v", entries)
	}

	// Five, tightly clustered, is.
	var many []Observation
	for i := 1; i <= 5; i++ {
		many = append(many, observation(KindDeparture, at(now, i*7, 8, 10+i), map[string]any{"place": "home"}))
	}
	entries := observer.Digest("kai", many, now)
	if len(entries) != 1 {
		t.Fatalf("expected one habit: %+v", entries)
	}
	if !strings.Contains(entries[0].Summary, "Tuesday leaves home around 08:1") {
		t.Fatalf("habit summary: %q", entries[0].Summary)
	}
	if entries[0].Confidence < 0.7 {
		t.Fatalf("a tight habit should be confident: %v", entries[0].Confidence)
	}
}

func TestRoutineObserverIgnoresScatteredBehaviour(t *testing.T) {
	now := time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC)
	var scattered []Observation
	for i, minute := range []int{5, 200, 400, 620, 800} {
		scattered = append(scattered, observation(KindArrival, at(now, (i+1)*7, minute/60, minute%60), map[string]any{"place": "gym"}))
	}
	if entries := (RoutineObserver{}).Digest("kai", scattered, now); len(entries) != 0 {
		t.Fatalf("scattered times are not a routine: %+v", entries)
	}
}

func TestRoutineObserverReportsTodaysDeviation(t *testing.T) {
	now := time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC)
	var observations []Observation
	for i := 1; i <= 5; i++ {
		observations = append(observations, observation(KindDeparture, at(now, i*7, 8, 10), map[string]any{"place": "home"}))
	}
	// Today they left ninety minutes late.
	observations = append(observations, observation(KindDeparture, at(now, 0, 9, 40), map[string]any{"place": "home"}))

	entries := (RoutineObserver{}).Digest("kai", observations, now)
	var deviation *Entry
	for i := range entries {
		if entries[i].Key == "today.deviation" {
			deviation = &entries[i]
		}
	}
	if deviation == nil {
		t.Fatalf("a ninety-minute change should be reported: %+v", entries)
	}
	if !strings.Contains(deviation.Summary, "90 minutes later") {
		t.Fatalf("deviation summary: %q", deviation.Summary)
	}
	if deviation.ExpiresAt == nil {
		t.Fatal("today's deviation is about today and must expire")
	}

	// Today must not also be counted as part of the habit it is measured against.
	for _, entry := range entries {
		if entry.Key == "tuesday.departure.home" {
			if minute, _ := entry.Value["typical_minute"].(int); minute != 8*60+10 {
				t.Fatalf("today leaked into the habit: %+v", entry.Value)
			}
		}
	}
}

func TestRoutineObserverSaysNothingAboutASmallChange(t *testing.T) {
	now := time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC)
	var observations []Observation
	for i := 1; i <= 5; i++ {
		observations = append(observations, observation(KindDeparture, at(now, i*7, 8, 10), map[string]any{"place": "home"}))
	}
	observations = append(observations, observation(KindDeparture, at(now, 0, 8, 25), map[string]any{"place": "home"}))
	for _, entry := range (RoutineObserver{}).Digest("kai", observations, now) {
		if entry.Key == "today.deviation" {
			t.Fatalf("fifteen minutes is not worth mentioning: %q", entry.Summary)
		}
	}
}

func TestObservationValidation(t *testing.T) {
	now := time.Now().UTC()
	if err := (Observation{Kind: KindFocus}).Normalize(now).Validate(now); err == nil {
		t.Fatal("an observation without an owner must be rejected")
	}
	if err := (Observation{Owner: "kai"}).Normalize(now).Validate(now); err == nil {
		t.Fatal("an observation without a kind must be rejected")
	}
	future := Observation{Owner: "kai", Kind: KindFocus, At: now.Add(48 * time.Hour)}
	if err := future.Normalize(now).Validate(now); err == nil {
		t.Fatal("a signal from the future would poison every window it touches")
	}
	ok := Observation{Owner: " kai ", Kind: " FOCUS "}.Normalize(now)
	if ok.Owner != "kai" || ok.Kind != "focus" || ok.At.IsZero() {
		t.Fatalf("normalize: %+v", ok)
	}
}

func TestDigestHonoursConsentAndPrecedence(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	now := time.Now().UTC()

	if _, err := store.Record(ctx, []Observation{
		observation(KindFocus, now.Add(-time.Minute), map[string]any{"mode": "Work"}),
	}); err != nil {
		t.Fatal(err)
	}

	// Consent off: nothing is concluded.
	applied, _, err := Digest(ctx, store, store, "kai", Observers(), func(string) bool { return false }, now)
	if err != nil || applied != 0 {
		t.Fatalf("an observer without consent must not run: %d %v", applied, err)
	}
	if entries, _ := store.List(ctx, "kai", Query{}); len(entries) != 0 {
		t.Fatalf("nothing should be written: %+v", entries)
	}

	// Consent on: the state observer writes.
	applied, _, err = Digest(ctx, store, store, "kai", Observers(), func(string) bool { return true }, now)
	if err != nil || applied != 1 {
		t.Fatalf("digest: %d %v", applied, err)
	}
	entry, err := store.Get(ctx, "kai", SectionState, "now")
	if err != nil || entry.Summary != "In Work Focus" {
		t.Fatalf("state entry: %+v %v", entry, err)
	}

	// The person overrides it; the next pass is refused, not an error.
	if _, err := store.Put(ctx, Entry{
		Owner: "kai", Section: SectionState, Key: "now",
		Summary: "On holiday", Source: SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	applied, refused, err := Digest(ctx, store, store, "kai", Observers(), func(string) bool { return true }, now)
	if err != nil || applied != 0 || refused != 1 {
		t.Fatalf("a sense must be refused, not fail: applied=%d refused=%d err=%v", applied, refused, err)
	}
}

func TestSenseConsentDefaultsOffAndForgettingRemovesWhatItConcluded(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	now := time.Now().UTC()

	senses, err := store.Senses(ctx, "kai")
	if err != nil || len(senses) != 0 {
		t.Fatalf("nothing is watched until asked: %+v %v", senses, err)
	}
	if err := store.SetSense(ctx, "kai", "state", true); err != nil {
		t.Fatal(err)
	}
	if senses, _ := store.Senses(ctx, "kai"); !senses["state"] {
		t.Fatalf("consent not stored: %+v", senses)
	}

	if _, err := store.Record(ctx, []Observation{
		observation(KindFocus, now.Add(-time.Minute), map[string]any{"mode": "Work"}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Digest(ctx, store, store, "kai", Observers(), func(s string) bool { return s == "state" }, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "kai", SectionState, "now"); err != nil {
		t.Fatalf("state should be written: %v", err)
	}

	// Withdrawing consent removes the conclusion and the raw signals.
	removed, err := store.ForgetSense(ctx, "kai", "state")
	if err != nil || removed != 1 {
		t.Fatalf("forget: %d %v", removed, err)
	}
	if _, err := store.Get(ctx, "kai", SectionState, "now"); err == nil {
		t.Fatal("what a sense concluded must go with it")
	}
	observations, err := store.Observations(ctx, "kai", nil, now.Add(-time.Hour))
	if err != nil || len(observations) != 0 {
		t.Fatalf("raw signals must go too: %+v %v", observations, err)
	}
}

func TestObservationBufferPrunesOldSignals(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	now := time.Now().UTC()

	if _, err := store.Record(ctx, []Observation{
		observation(KindArrival, now.Add(-2*time.Hour), map[string]any{"place": "home"}),
		observation(KindArrival, now.Add(-60*24*time.Hour), map[string]any{"place": "home"}),
	}); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.Observations(ctx, "kai", nil, now.Add(-90*24*time.Hour)); len(all) != 2 {
		t.Fatalf("both stored: %d", len(all))
	}
	removed, err := store.PruneObservations(ctx, now.Add(-ObservationRetention))
	if err != nil || removed != 1 {
		t.Fatalf("prune: %d %v", removed, err)
	}
	// Filtering by kind and window works.
	recent, err := store.Observations(ctx, "kai", []string{KindArrival}, now.Add(-24*time.Hour))
	if err != nil || len(recent) != 1 {
		t.Fatalf("filtered read: %+v %v", recent, err)
	}
	if none, _ := store.Observations(ctx, "kai", []string{KindFocus}, now.Add(-24*time.Hour)); len(none) != 0 {
		t.Fatalf("kind filter: %+v", none)
	}
}
