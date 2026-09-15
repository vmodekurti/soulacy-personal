package person

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "person.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func entry(owner string, section Section, key, summary, source string) Entry {
	return Entry{Owner: owner, Section: section, Key: key, Summary: summary, Source: source}
}

func TestPrecedenceProtectsWhatThePersonSaid(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)

	// A sense observes where home probably is.
	guess := entry("kai", SectionIdentity, "home", "Home is near Oak Street", "sense:location")
	guess.Confidence = 0.5
	if result, err := store.Put(ctx, guess); err != nil || !result.Applied {
		t.Fatalf("sense write: %+v %v", result, err)
	}

	// The person corrects it. Manual always wins, and is full confidence.
	correction := entry("kai", SectionIdentity, "home", "Home is 14 Oak Street, Chicago", SourceManual)
	result, err := store.Put(ctx, correction)
	if err != nil || !result.Applied {
		t.Fatalf("manual write: %+v %v", result, err)
	}
	if result.Entry.Confidence != 1 {
		t.Fatalf("manual entries are certain, got %v", result.Entry.Confidence)
	}

	// The sense observes again. It must not overwrite the correction.
	again := entry("kai", SectionIdentity, "home", "Home is near Elm Street", "sense:location")
	again.ObservedAt = time.Now().Add(time.Hour)
	result, err = store.Put(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("a sense must not overwrite what the person said")
	}
	if !strings.Contains(result.Reason, "you set this yourself") {
		t.Fatalf("refusal should explain itself, got %q", result.Reason)
	}
	if result.Entry.Summary != "Home is 14 Oak Street, Chicago" {
		t.Fatalf("stored entry changed: %q", result.Entry.Summary)
	}

	// An agent may not overwrite the person either, but does outrank a sense.
	if result, err := store.Put(ctx, entry("kai", SectionIdentity, "home", "Home is somewhere else", "agent:planner")); err != nil || result.Applied {
		t.Fatalf("agent must not outrank manual: %+v %v", result, err)
	}
	if result, err := store.Put(ctx, entry("kai", SectionRoutine, "weekday.leave", "Usually leaves at 8:10", "sense:location")); err != nil || !result.Applied {
		t.Fatalf("sense write to a free key: %+v %v", result, err)
	}
	if result, err := store.Put(ctx, entry("kai", SectionRoutine, "weekday.leave", "Leaves at 8:10, later on Fridays", "agent:planner")); err != nil || !result.Applied {
		t.Fatalf("agent outranks sense: %+v %v", result, err)
	}
}

func TestStaleObservationsNeverReplaceFresherOnes(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	now := time.Now().UTC()

	fresh := entry("kai", SectionState, "now", "Working", "sense:focus")
	fresh.ObservedAt = now
	if _, err := store.Put(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	stale := entry("kai", SectionState, "now", "Asleep", "sense:focus")
	stale.ObservedAt = now.Add(-2 * time.Hour)
	result, err := store.Put(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Entry.Summary != "Working" {
		t.Fatalf("a late-arriving old sample must not win: %+v", result)
	}
}

func TestExpiredStateIsNotReadAndSyncsAsATombstone(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	now := time.Now().UTC()

	soon := now.Add(30 * time.Minute)
	state := entry("kai", SectionState, "now", "Commuting", "sense:motion")
	state.ExpiresAt = &soon
	if _, err := store.Put(ctx, state); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.List(ctx, "kai", Query{}); err != nil || len(entries) != 1 {
		t.Fatalf("fresh state should be readable: %d %v", len(entries), err)
	}

	// Move the clock past the expiry.
	store.now = func() time.Time { return now.Add(time.Hour) }
	entries, err := store.List(ctx, "kai", Query{})
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired state must not be read: %d %v", len(entries), err)
	}
	if entries, err := store.List(ctx, "kai", Query{IncludeExpired: true}); err != nil || len(entries) != 1 {
		t.Fatalf("the management view still sees it: %d %v", len(entries), err)
	}
	page, err := store.Changes(ctx, "kai", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 1 || !page.Changes[0].Deleted {
		t.Fatalf("an expired entry reaches a device as a tombstone: %+v", page.Changes)
	}
}

func TestChangeFeedCatchesADeviceUpAndResetsAfterPurge(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)

	for _, e := range []Entry{
		entry("kai", SectionCommitments, "proposal", "Send Priya the proposal by Friday", "sense:email"),
		entry("kai", SectionRelationships, "priya-s", "Priya, colleague, weekly", "sense:calendar"),
	} {
		if _, err := store.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Changes(ctx, "kai", 0, 100)
	if err != nil || len(first.Changes) != 2 || first.NextCursor == 0 {
		t.Fatalf("first sync: %+v %v", first, err)
	}

	// Nothing new since.
	if page, err := store.Changes(ctx, "kai", first.NextCursor, 100); err != nil || len(page.Changes) != 0 {
		t.Fatalf("caught-up sync should be empty: %+v %v", page, err)
	}

	// One more change, and a deletion.
	if _, err := store.Put(ctx, entry("kai", SectionPreferences, "seats", "Never a middle seat", SourceManual)); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "kai", SectionCommitments, "proposal"); err != nil {
		t.Fatal(err)
	}
	page, err := store.Changes(ctx, "kai", first.NextCursor, 100)
	if err != nil || len(page.Changes) != 2 {
		t.Fatalf("incremental sync: %+v %v", page, err)
	}
	if page.Changes[1].Key != "proposal" || !page.Changes[1].Deleted {
		t.Fatalf("deletion must arrive as a tombstone: %+v", page.Changes[1])
	}

	// A purge tells the next caller to drop its copy.
	if removed, err := store.Purge(ctx, "kai"); err != nil || removed != 2 {
		t.Fatalf("purge: %d %v", removed, err)
	}
	after, err := store.Changes(ctx, "kai", page.NextCursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Reset {
		t.Fatalf("a cursor ahead of the feed must reset: %+v", after)
	}
}

func TestOwnersNeverSeeEachOther(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	if _, err := store.Put(ctx, entry("kai", SectionIdentity, "home", "Kai's home", SourceManual)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, entry("priya-s", SectionIdentity, "home", "Priya's home", SourceManual)); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(ctx, "kai", Query{})
	if err != nil || len(entries) != 1 || entries[0].Summary != "Kai's home" {
		t.Fatalf("household members must not leak: %+v %v", entries, err)
	}
	if page, err := store.Changes(ctx, "kai", 0, 100); err != nil || len(page.Changes) != 1 {
		t.Fatalf("the feed is owner-scoped too: %+v %v", page, err)
	}
	if _, err := store.Get(ctx, "kai", SectionIdentity, "nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing entry: %v", err)
	}
}

func TestValidationKeepsTheModelReadable(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	for name, bad := range map[string]Entry{
		"no owner":        {Section: SectionIdentity, Key: "home", Summary: "x"},
		"unknown section": entry("kai", Section("astrology"), "sign", "Leo", SourceManual),
		"no key":          entry("kai", SectionIdentity, "", "Home", SourceManual),
		"no summary":      entry("kai", SectionIdentity, "home", "", SourceManual),
		"long summary":    entry("kai", SectionIdentity, "home", strings.Repeat("x", MaxSummaryLength+1), SourceManual),
	} {
		if _, err := store.Put(ctx, bad); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
}

func TestRenderIsProseAnAgentCanRead(t *testing.T) {
	now := time.Now().UTC()
	manual := entry("kai", SectionIdentity, "home", "Home is 14 Oak Street", SourceManual)
	manual.ObservedAt = now
	guess := entry("kai", SectionRoutine, "weekday.leave", "Usually leaves home at 8:10", "sense:location")
	guess.Confidence = 0.5
	guess.ObservedAt = now.Add(-72 * time.Hour)
	inferred := entry("kai", SectionCommitments, "proposal", "Owes Priya the proposal by Friday", "agent:planner")
	inferred.Confidence = 0.9
	inferred.ObservedAt = now

	model := NewModel("kai", []Entry{manual, guess, inferred}, now)
	text := Render(model, now)

	for _, want := range []string{
		"Who they are:", "- Home is 14 Oak Street (they told us)",
		"Their usual day:", "low confidence", "as of ",
		"Open commitments:", "inferred by planner",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered model missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "{") || strings.Contains(text, "\"summary\"") {
		t.Fatalf("the model must reach an agent as prose, not JSON:\n%s", text)
	}
	if empty := Render(NewModel("kai", nil, now), now); !strings.Contains(empty, "Nothing is known") {
		t.Fatalf("empty model should say so: %q", empty)
	}
	if only := RenderSections(model, []Section{SectionCommitments}, now); strings.Contains(only, "Who they are") {
		t.Fatalf("section filter leaked other sections:\n%s", only)
	}
}

func TestSearchMatchesEveryWord(t *testing.T) {
	now := time.Now().UTC()
	model := NewModel("kai", []Entry{
		entry("kai", SectionCommitments, "proposal", "Owes Priya the proposal by Friday", "agent:planner"),
		entry("kai", SectionRelationships, "priya-s", "Priya is a colleague, spoken to weekly", "sense:calendar"),
		entry("kai", SectionPreferences, "seats", "Never a middle seat", SourceManual),
	}, now)

	hits := Search(model, "priya proposal", 10)
	if len(hits) != 1 || hits[0].Key != "proposal" {
		t.Fatalf("both words must match: %+v", hits)
	}
	if hits := Search(model, "priya", 10); len(hits) != 2 {
		t.Fatalf("one word matches both Priya rows: %+v", hits)
	}
	if hits := Search(model, "a", 10); len(hits) != 0 {
		t.Fatal("short noise words match nothing")
	}
	if hits := Search(model, "priya", 1); len(hits) != 1 {
		t.Fatal("limit ignored")
	}
}

func TestModelForAssemblesEverySection(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	if _, err := store.PutAll(ctx, []Entry{
		entry("kai", SectionIdentity, "home", "Home is 14 Oak Street", SourceManual),
		entry("kai", SectionState, "now", "Working", "sense:focus"),
	}); err != nil {
		t.Fatal(err)
	}
	model, err := ModelFor(ctx, store, "kai", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Sections[SectionIdentity]) != 1 || len(model.Sections[SectionState]) != 1 {
		t.Fatalf("model: %+v", model.Sections)
	}
	if model.Empty() {
		t.Fatal("model should not be empty")
	}
	if model.UpdatedAt.IsZero() {
		t.Fatal("model should carry its freshness")
	}
}

var _ Store = (*SQLiteStore)(nil)

var _ = context.Background
