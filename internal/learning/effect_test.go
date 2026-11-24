package learning

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func effectEpisodes(prefix, request string, n, problems int, at time.Time) []Episode {
	out := make([]Episode, n)
	for i := range out {
		out[i] = Episode{RunID: fmt.Sprintf("%s-%d", prefix, i), Request: request, Success: true, CreatedAt: at}
		if i < problems {
			if i%2 == 0 {
				out[i].Success = false
			} else {
				out[i].ToolFailures = 1
			}
		}
	}
	return out
}

func TestLessonEffectVerdicts(t *testing.T) {
	approved := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := approved.Add(30 * 24 * time.Hour)
	skill := Lesson{Draft: Draft{Key: "release-checks", Kind: "skill", Title: "Release checklist", Trigger: "Preparing a staging release"}, ID: "l1", UpdatedAt: approved}
	related, unrelated := "Prepare the staging release for Friday", "What is the weather in Paris"
	loaded := func(eps []Episode) map[string]bool {
		m := map[string]bool{}
		for _, ep := range eps {
			m[ep.RunID] = true
		}
		return m
	}
	cases := []struct {
		name          string
		with, without []Episode
		lesson        Lesson
		want          string
	}{
		{"fewer problems with it", effectEpisodes("w", related, 6, 0, now), effectEpisodes("o", related, 6, 3, approved.Add(-time.Hour)), skill, "better"},
		{"more problems with it", effectEpisodes("w", related, 6, 4, now), effectEpisodes("o", related, 6, 0, now), skill, "worse"},
		{"close rates", effectEpisodes("w", related, 6, 1, now), effectEpisodes("o", related, 6, 1, now), skill, "no_clear_difference"},
		{"small sample", effectEpisodes("w", related, 4, 4, now), effectEpisodes("o", related, 6, 0, now), skill, "too_early"},
		{"unrelated runs are no baseline", effectEpisodes("w", related, 6, 4, now), effectEpisodes("o", unrelated, 9, 0, now), skill, "too_early"},
		{"never loaded", nil, effectEpisodes("o", related, 6, 0, now), skill, "unused"},
		{"never loaded but new", nil, nil, Lesson{Draft: skill.Draft, ID: "l1", UpdatedAt: now.Add(-time.Hour)}, "too_early"},
		{"preference compares all runs", effectEpisodes("w", related, 6, 4, now), effectEpisodes("o", unrelated, 6, 0, approved.Add(-time.Hour)), Lesson{Draft: Draft{Kind: "preference", Title: "Units", Trigger: "Writing measurements"}, ID: "p", UpdatedAt: approved}, "worse"},
	}
	for _, tc := range cases {
		e := lessonEffect(tc.lesson, append(tc.with, tc.without...), loaded(tc.with), now)
		if e.Verdict != tc.want || e.Reason == "" {
			t.Errorf("%s: got %s (%+v), want %s", tc.name, e.Verdict, e, tc.want)
		}
	}
}

func TestNotebookEffectsReadRecordedRuns(t *testing.T) {
	ctx := context.Background()
	n := openTestNotebook(t)
	l := proposeFixture(t, n)
	if _, err := n.Effect(ctx, notebookScope, l.ID); !errors.Is(err, ErrLessonConflict) {
		t.Fatal("pending lesson reported an effect", err)
	}
	active, err := n.Review(ctx, notebookScope, l.ID, "approve")
	if err != nil || active.ApprovedAt == nil {
		t.Fatal(active, err)
	}
	for i := 0; i < 6; i++ {
		run := fmt.Sprintf("with-%d", i)
		if err := n.RecordUse(ctx, notebookScope, l.ID, run); err != nil {
			t.Fatal(err)
		}
		if err := n.RecordEpisode(ctx, notebookScope, Episode{RunID: run, SessionID: "s", Request: "Convert this recipe", Success: i > 0, ToolCalls: 2}); err != nil {
			t.Fatal(err)
		}
		if err := n.RecordEpisode(ctx, notebookScope, Episode{RunID: fmt.Sprintf("without-%d", i), SessionID: "s", Request: "Convert this recipe", Success: i > 3}); err != nil {
			t.Fatal(err)
		}
	}
	effects, err := n.Effects(ctx, notebookScope)
	if err != nil || len(effects) != 1 {
		t.Fatal(effects, err)
	}
	e := effects[0]
	if e.With != (RunOutcome{Runs: 6, Problems: 1}) || e.Without != (RunOutcome{Runs: 6, Problems: 4}) || e.Verdict != "better" {
		t.Fatalf("%+v", e)
	}
	if other, err := n.Effects(ctx, Scope{Owner: "mallory", AgentID: "research"}); err != nil || len(other) != 0 {
		t.Fatal("effects crossed owners", other, err)
	}
}
