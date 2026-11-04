package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Effect compares runs that loaded an active lesson with similar runs that did
// not. It is the cheap half of harness refinement: instead of re-running a
// task with and without a change (one model call per probe), it reads the
// contrasts that ordinary use already produced. No model is called.
//
// The comparison is observational. Runs differ in more than the lesson, so a
// verdict is a prompt for human review, never an automatic disable.
type Effect struct {
	LessonID   string     `json:"lesson_id"`
	Key        string     `json:"key"`
	Title      string     `json:"title"`
	Kind       string     `json:"kind"`
	ApprovedAt time.Time  `json:"approved_at"`
	With       RunOutcome `json:"with"`
	Without    RunOutcome `json:"without"`
	Verdict    string     `json:"verdict"` // better, worse, no_clear_difference, too_early, unused
	Reason     string     `json:"reason"`
}

// RunOutcome counts recorded episodes. A run has a problem when it failed or
// any of its tool calls returned an error.
type RunOutcome struct {
	Runs     int `json:"runs"`
	Problems int `json:"problems"`
}

func (o RunOutcome) rate() float64 {
	if o.Runs == 0 {
		return 0
	}
	return float64(o.Problems) / float64(o.Runs)
}

const (
	effectMinRuns    = 5
	effectMargin     = 0.2
	effectUnusedTime = 14 * 24 * time.Hour
)

var verdictOrder = map[string]int{"worse": 0, "unused": 1, "no_clear_difference": 2, "better": 3, "too_early": 4}

// Effects reports every active lesson, those most worth reviewing first.
func (n *Notebook) Effects(ctx context.Context, scope Scope) ([]Effect, error) {
	active, err := n.List(ctx, scope, "active")
	if err != nil {
		return nil, err
	}
	episodes, err := n.effectEpisodes(ctx, scope)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]Effect, 0, len(active))
	for _, l := range active {
		runs, err := n.lessonRuns(ctx, scope, l.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, lessonEffect(l, episodes, runs, now))
	}
	sort.SliceStable(out, func(i, j int) bool { return verdictOrder[out[i].Verdict] < verdictOrder[out[j].Verdict] })
	return out, nil
}

// Effect reports one active lesson.
func (n *Notebook) Effect(ctx context.Context, scope Scope, id string) (Effect, error) {
	l, err := n.Get(ctx, scope, id)
	if err != nil {
		return Effect{}, err
	}
	if l.Status != "active" {
		return Effect{}, ErrLessonConflict
	}
	episodes, err := n.effectEpisodes(ctx, scope)
	if err != nil {
		return Effect{}, err
	}
	runs, err := n.lessonRuns(ctx, scope, l.ID)
	if err != nil {
		return Effect{}, err
	}
	return lessonEffect(l, episodes, runs, time.Now().UTC()), nil
}

func (n *Notebook) effectEpisodes(ctx context.Context, scope Scope) ([]Episode, error) {
	if !validScope(scope) {
		return nil, ErrInvalidLesson
	}
	rows, err := n.db.QueryContext(ctx, `SELECT payload FROM learned_episodes WHERE owner=? AND agent_id=? ORDER BY created_at DESC LIMIT 500`, scope.Owner, scope.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Episode{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ep Episode
		if err := json.Unmarshal(raw, &ep); err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

func (n *Notebook) lessonRuns(ctx context.Context, scope Scope, id string) (map[string]bool, error) {
	rows, err := n.db.QueryContext(ctx, `SELECT run_id FROM learned_uses WHERE owner=? AND agent_id=? AND lesson_id=?`, scope.Owner, scope.AgentID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, err
		}
		out[runID] = true
	}
	return out, rows.Err()
}

func lessonEffect(l Lesson, episodes []Episode, loaded map[string]bool, now time.Time) Effect {
	e := Effect{LessonID: l.ID, Key: l.Key, Title: l.Title, Kind: l.Kind, ApprovedAt: l.UpdatedAt}
	if l.ApprovedAt != nil {
		e.ApprovedAt = *l.ApprovedAt
	}
	// The baseline is runs on the same kind of task: requests that match the
	// lesson's title and trigger. Preferences apply to every request, and are
	// loaded into every run once approved, so their baseline is earlier runs.
	terms := lessonTerms(l.Title + " " + l.Trigger)
	need := min(2, len(terms))
	sinceApproval := 0
	for _, ep := range episodes {
		problem := !ep.Success || ep.ToolFailures > 0
		if !ep.CreatedAt.Before(e.ApprovedAt) {
			sinceApproval++
		}
		switch {
		case loaded[ep.RunID]:
			e.With.Runs++
			if problem {
				e.With.Problems++
			}
		case l.Kind == "preference" || (need > 0 && termScore(terms, ep.Request) >= need):
			e.Without.Runs++
			if problem {
				e.Without.Problems++
			}
		}
	}
	diff := e.With.rate() - e.Without.rate()
	switch {
	case e.With.Runs == 0 && now.Sub(e.ApprovedAt) >= effectUnusedTime:
		e.Verdict = "unused"
		e.Reason = fmt.Sprintf("Not loaded in any of the %d recorded runs since approval. Consider disabling it or rewording when to use it.", sinceApproval)
	case e.With.Runs < effectMinRuns || e.Without.Runs < effectMinRuns:
		e.Verdict = "too_early"
		e.Reason = fmt.Sprintf("Needs at least %d runs with and %d similar runs without this lesson to compare.", effectMinRuns, effectMinRuns)
	case diff >= effectMargin:
		e.Verdict = "worse"
		e.Reason = fmt.Sprintf("Runs that loaded it had problems %s of the time, against %s for similar runs without it. Review or disable it.", percent(e.With.rate()), percent(e.Without.rate()))
	case diff <= -effectMargin:
		e.Verdict = "better"
		e.Reason = fmt.Sprintf("Runs that loaded it had problems %s of the time, against %s for similar runs without it.", percent(e.With.rate()), percent(e.Without.rate()))
	default:
		e.Verdict = "no_clear_difference"
		e.Reason = fmt.Sprintf("Problem rates are close: %s with it, %s without.", percent(e.With.rate()), percent(e.Without.rate()))
	}
	if l.Unhelpful > 0 {
		e.Reason += " You also marked it as needing improvement."
	}
	return e
}

func percent(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }
