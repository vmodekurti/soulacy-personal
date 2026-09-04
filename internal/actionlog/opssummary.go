package actionlog

import (
	"sort"
	"strings"
	"time"
)

// OpsSummary is a durable reliability rollup across recent agent runs.
// A run is identified by (agent_id, session_id) and counted when it has a
// message.in event inside the requested window.
type OpsSummary struct {
	GeneratedAt    time.Time             `json:"generated_at"`
	Since          time.Time             `json:"since"`
	Window         string                `json:"window"`
	TotalRuns      int                   `json:"total_runs"`
	SuccessfulRuns int                   `json:"successful_runs"`
	FailedRuns     int                   `json:"failed_runs"`
	IncompleteRuns int                   `json:"incomplete_runs"`
	TotalEvents    int                   `json:"total_events"`
	ToolCalls      int                   `json:"tool_calls"`
	FailureRate    float64               `json:"failure_rate"`
	IncompleteRate float64               `json:"incomplete_rate"`
	AvgDurationMS  int64                 `json:"avg_duration_ms"`
	P95DurationMS  int64                 `json:"p95_duration_ms"`
	MaxDurationMS  int64                 `json:"max_duration_ms"`
	TopFailing     []AgentFailureSummary `json:"top_failing_agents"`
	TopErrors      []ErrorSignature      `json:"top_errors"`
	RecentFailures []RunFailure          `json:"recent_failures"`
}

type AgentFailureSummary struct {
	AgentID     string  `json:"agent_id"`
	Failures    int     `json:"failures"`
	Runs        int     `json:"runs"`
	FailureRate float64 `json:"failure_rate"`
}

type ErrorSignature struct {
	Message string `json:"message"`
	Count   int    `json:"count"`
}

type RunFailure struct {
	AgentID   string    `json:"agent_id"`
	SessionID string    `json:"session_id"`
	Error     string    `json:"error"`
	At        time.Time `json:"at"`
}

type runRollup struct {
	AgentID      string
	SessionID    string
	Events       int
	ToolCalls    int
	Starts       int
	Replies      int
	Errors       int
	DeadLetters  int
	FirstEventAt time.Time
	LastEventAt  time.Time
}

// OpsSummary aggregates recent run reliability from the durable SQLite event
// table. Since zero means all known history.
func (l *Logger) OpsSummary(since time.Time, window string, limit int) (OpsSummary, error) {
	if limit <= 0 {
		limit = 8
	}
	if limit > 50 {
		limit = 50
	}
	summary := OpsSummary{
		GeneratedAt: time.Now().UTC(),
		Since:       since,
		Window:      strings.TrimSpace(window),
	}
	runs, err := l.runRollups(since)
	if err != nil {
		return summary, err
	}

	byAgent := map[string]*AgentFailureSummary{}
	durations := make([]int64, 0, len(runs))
	for _, r := range runs {
		summary.TotalRuns++
		summary.TotalEvents += r.Events
		summary.ToolCalls += r.ToolCalls
		if !r.FirstEventAt.IsZero() && !r.LastEventAt.IsZero() && !r.LastEventAt.Before(r.FirstEventAt) {
			ms := r.LastEventAt.Sub(r.FirstEventAt).Milliseconds()
			durations = append(durations, ms)
			summary.AvgDurationMS += ms
			if ms > summary.MaxDurationMS {
				summary.MaxDurationMS = ms
			}
		}
		row := byAgent[r.AgentID]
		if row == nil {
			row = &AgentFailureSummary{AgentID: r.AgentID}
			byAgent[r.AgentID] = row
		}
		row.Runs++
		switch {
		case r.Errors > 0 || r.DeadLetters > 0:
			summary.FailedRuns++
			row.Failures++
		case r.Replies > 0:
			summary.SuccessfulRuns++
		default:
			summary.IncompleteRuns++
		}
	}
	if summary.TotalRuns > 0 {
		summary.FailureRate = float64(summary.FailedRuns) / float64(summary.TotalRuns)
		summary.IncompleteRate = float64(summary.IncompleteRuns) / float64(summary.TotalRuns)
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		summary.AvgDurationMS = summary.AvgDurationMS / int64(len(durations))
		idx := int(float64(len(durations))*0.95+0.999999) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(durations) {
			idx = len(durations) - 1
		}
		summary.P95DurationMS = durations[idx]
	}
	for _, row := range byAgent {
		if row.Runs > 0 {
			row.FailureRate = float64(row.Failures) / float64(row.Runs)
		}
		if row.Failures > 0 {
			summary.TopFailing = append(summary.TopFailing, *row)
		}
	}
	sort.Slice(summary.TopFailing, func(i, j int) bool {
		if summary.TopFailing[i].Failures == summary.TopFailing[j].Failures {
			return summary.TopFailing[i].AgentID < summary.TopFailing[j].AgentID
		}
		return summary.TopFailing[i].Failures > summary.TopFailing[j].Failures
	})
	if len(summary.TopFailing) > limit {
		summary.TopFailing = summary.TopFailing[:limit]
	}

	failures, err := l.recentFailures(since, limit)
	if err != nil {
		return summary, err
	}
	summary.RecentFailures = failures
	signatures, err := l.errorSignatures(since, limit)
	if err != nil {
		return summary, err
	}
	summary.TopErrors = signatures
	return summary, nil
}

func (l *Logger) runRollups(since time.Time) ([]runRollup, error) {
	where := "session_id <> ''"
	args := []any{}
	if !since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, since.UTC())
	}
	rows, err := l.db.Query(`
		SELECT agent_id,
		       session_id,
		       COUNT(*) AS events,
		       COALESCE(SUM(CASE WHEN type = 'tool.call' THEN 1 ELSE 0 END), 0) AS tool_calls,
		       COALESCE(SUM(CASE WHEN type = 'message.in' THEN 1 ELSE 0 END), 0) AS starts,
		       COALESCE(SUM(CASE WHEN type = 'message.out' THEN 1 ELSE 0 END), 0) AS replies,
		       COALESCE(SUM(CASE WHEN type = 'error' THEN 1 ELSE 0 END), 0) AS errors,
		       COALESCE(SUM(CASE WHEN type = 'message.dead_letter' THEN 1 ELSE 0 END), 0) AS dead_letters,
		       MIN(created_at) AS first_event_at,
		       MAX(created_at) AS last_event_at
		  FROM agent_events
		 WHERE `+where+`
		 GROUP BY agent_id, session_id
		HAVING starts > 0
		 ORDER BY last_event_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []runRollup
	for rows.Next() {
		var r runRollup
		var firstRaw, lastRaw any
		if err := rows.Scan(&r.AgentID, &r.SessionID, &r.Events, &r.ToolCalls, &r.Starts, &r.Replies, &r.Errors, &r.DeadLetters, &firstRaw, &lastRaw); err != nil {
			return nil, err
		}
		r.FirstEventAt = parseSQLiteTime(firstRaw)
		r.LastEventAt = parseSQLiteTime(lastRaw)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (l *Logger) recentFailures(since time.Time, limit int) ([]RunFailure, error) {
	where := "type = 'error' AND session_id <> ''"
	args := []any{}
	if !since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, since.UTC())
	}
	args = append(args, limit)
	rows, err := l.db.Query(`
		SELECT agent_id, session_id, COALESCE(payload, ''), created_at
		  FROM agent_events
		 WHERE `+where+`
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RunFailure, 0, limit)
	seen := map[string]bool{}
	for rows.Next() {
		var agentID, sessionID, payload string
		var atRaw any
		if err := rows.Scan(&agentID, &sessionID, &payload, &atRaw); err != nil {
			return nil, err
		}
		key := agentID + "\x00" + sessionID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, RunFailure{
			AgentID:   agentID,
			SessionID: sessionID,
			Error:     compactErrorSignature(extractErrorText(payload)),
			At:        parseSQLiteTime(atRaw),
		})
	}
	return out, rows.Err()
}

func (l *Logger) errorSignatures(since time.Time, limit int) ([]ErrorSignature, error) {
	where := "type = 'error'"
	args := []any{}
	if !since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, since.UTC())
	}
	rows, err := l.db.Query(`
		SELECT COALESCE(payload, '')
		  FROM agent_events
		 WHERE `+where+`
		 ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		msg := compactErrorSignature(extractErrorText(payload))
		if msg == "" {
			msg = "unknown error"
		}
		counts[msg]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ErrorSignature, 0, len(counts))
	for msg, count := range counts {
		out = append(out, ErrorSignature{Message: msg, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Message < out[j].Message
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > limit {
		return out[:limit], nil
	}
	return out, nil
}

func compactErrorSignature(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	const max = 180
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

func parseSQLiteTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x.UTC()
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t.UTC()
			}
		}
	case []byte:
		return parseSQLiteTime(string(x))
	}
	return time.Time{}
}
