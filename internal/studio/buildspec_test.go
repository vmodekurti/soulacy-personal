package studio

import (
	"strings"
	"testing"
)

// The intent from the actual podcast workflow — the case every screen shows.
const podcastIntent = `Every weekday at 7:00am, build an "AI articles podcast" as a fixed workflow ` +
	`(not a reasoning agent). Sources: HBR.org, MIT Technology Review, Gartner.com. ` +
	`Summarize the top stories and deliver the daily podcast to Telegram.`

func TestExtractBuildSpec_Podcast(t *testing.T) {
	s := ExtractBuildSpec(podcastIntent)

	if s.Trigger != "schedule" {
		t.Errorf("trigger = %q, want schedule", s.Trigger)
	}
	// The spec must commit to a real cron, not echo the prose back.
	if s.Schedule != "0 7 * * 1-5" {
		t.Errorf("schedule = %q, want \"0 7 * * 1-5\"", s.Schedule)
	}
	if !strings.Contains(s.ScheduleText, "weekday") || !strings.Contains(s.ScheduleText, "07:00") {
		t.Errorf("schedule text should be readable: %q", s.ScheduleText)
	}
	if len(s.Inputs) != 3 {
		t.Errorf("expected the three named sources, got %v", s.Inputs)
	}
	if len(s.Delivery) != 1 || s.Delivery[0] != "Telegram" {
		t.Errorf("delivery = %v, want [Telegram]", s.Delivery)
	}
	// Stages must be recognised, and the multi-source gather marked parallel.
	names := stageNames(s)
	if !containsStr(names, "search sources") || !containsStr(names, "summarize") {
		t.Errorf("stages = %v", names)
	}
	foundParallel := false
	for _, st := range s.Stages {
		if st.Parallel {
			foundParallel = true
			// Evidence, not assertion: the spec shows the phrase it read.
			if st.Detail == "" {
				t.Error("a parallel stage should carry the phrase it came from")
			}
		}
	}
	if !foundParallel {
		t.Error("three named sources should mark the gathering stage parallel")
	}
	// Security must be stated as consequences, not capability tokens.
	joined := strings.Join(s.Security, " ")
	if !strings.Contains(joined, "sends messages on your behalf") {
		t.Errorf("security should describe what it can DO: %v", s.Security)
	}
	if strings.Contains(joined, "system") || strings.Contains(joined, "network") {
		t.Errorf("security must avoid raw capability tokens: %v", s.Security)
	}
	// The original prompt is never lost.
	if s.Intent == "" {
		t.Error("the original intent must be preserved")
	}
}

func TestExtractBuildSpec_TimeParsing(t *testing.T) {
	cases := map[string]string{
		"every weekday at 7:00am summarize the news and email it": "0 7 * * 1-5",
		"every day at 6:30pm write a report and email it":         "30 18 * * *",
		"every monday at 12:00am compile a digest and email it":   "0 0 * * 1",
		"every friday at 12:00pm summarize and email":             "0 12 * * 5",
	}
	for intent, want := range cases {
		if got := ExtractBuildSpec(intent).Schedule; got != want {
			t.Errorf("%q: schedule = %q, want %q", intent, got, want)
		}
	}

	// "top 5 stories" must not be read as a time — the classic false positive.
	s := ExtractBuildSpec("every weekday summarize the top 5 stories and email them")
	if s.Schedule != "" {
		t.Errorf("a bare quantity must not become a schedule: %q", s.Schedule)
	}
	// And that missing time is a blocker, phrased as a question.
	if !hasQuestion(s, "schedule_time") {
		t.Error("a schedule with no time must ask for one")
	}
	if s.Ready() {
		t.Error("a schedule with no time is not build-ready")
	}
}

func TestExtractBuildSpec_QuestionsAndBlockers(t *testing.T) {
	// Naming a channel is not naming a destination — the most common cause of a
	// run that "succeeds" and reaches nobody.
	s := ExtractBuildSpec("every weekday at 8:00am summarize the news and post it to telegram")
	if !hasQuestion(s, "destination") {
		t.Error("a delivery channel with no destination must be asked about")
	}
	if s.Ready() {
		t.Error("a missing destination must block generation")
	}

	// Supplying the chat id resolves it.
	s = ExtractBuildSpec("every weekday at 8:00am summarize the news and post it to telegram chat -100123")
	if hasQuestion(s, "destination") {
		t.Errorf("a supplied destination should resolve the question: %+v", s.Questions)
	}

	// An intent with no discernible work cannot be built.
	s = ExtractBuildSpec("do something useful")
	if s.Ready() || !hasQuestion(s, "stages") {
		t.Errorf("an intent with no work must block with a question: %+v", s)
	}

	// Empty input is handled, not panicked on.
	if ExtractBuildSpec("   ").Ready() {
		t.Error("an empty intent cannot be ready")
	}

	// Not every gap is a blocker — over-blocking turns guidance into an
	// interrogation.
	s = ExtractBuildSpec("every weekday at 9:00am search the web and write a report")
	for _, q := range s.Questions {
		if q.ID == "sources" && q.Blocker {
			t.Error("an unspecified source list should be a suggestion, not a blocker")
		}
	}
	// Every question must explain itself and bind to a field.
	for _, q := range s.Questions {
		if q.Why == "" || q.Field == "" {
			t.Errorf("question %q must carry a reason and a field: %+v", q.ID, q)
		}
	}
}

func TestSpecDiffAndMateriality(t *testing.T) {
	before := ExtractBuildSpec("every weekday summarize the news and post to telegram")
	after := ExtractBuildSpec("every weekday at 7:00am summarize HBR.org and post to telegram chat -100123")

	changes := DiffSpecs(before, after)
	if len(changes) == 0 {
		t.Fatal("a refinement that adds a schedule and a source must show changes")
	}
	var fields []string
	for _, c := range changes {
		fields = append(fields, c.Field)
	}
	if !containsStr(fields, "schedule") || !containsStr(fields, "inputs") {
		t.Errorf("change summary should name schedule and inputs: %v", fields)
	}
	if !MateriallyDifferent(before, after) {
		t.Error("adding a schedule and resolving blockers is material")
	}

	// A pure reword changes nothing structural, and must say so rather than
	// implying progress.
	a := ExtractBuildSpec("every weekday at 7:00am summarize HBR.org and post to telegram chat -100123")
	b := ExtractBuildSpec("Each weekday at 7:00am, summarize HBR.org, then post to telegram chat -100123")
	if MateriallyDifferent(a, b) {
		t.Errorf("a reworded intent with the same structure is not material: %+v", DiffSpecs(a, b))
	}
}

func TestExtractBuildSpec_TriggerKinds(t *testing.T) {
	cases := map[string]string{
		"every weekday at 8:00am email a summary":            "schedule",
		"answers questions about our docs when someone asks": "channel",
		"on webhook, summarize the payload and store it":     "webhook",
		"summarize a document I give it and write a report":  "manual",
	}
	for intent, want := range cases {
		if got := ExtractBuildSpec(intent).Trigger; got != want {
			t.Errorf("%q: trigger = %q, want %q", intent, got, want)
		}
	}
}

func hasQuestion(s BuildSpec, id string) bool {
	for _, q := range s.Questions {
		if q.ID == id {
			return true
		}
	}
	return false
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
