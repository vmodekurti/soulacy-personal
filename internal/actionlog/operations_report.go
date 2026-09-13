package actionlog

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ActivityCounts deliberately separates conversation sessions from incoming
// requests. A reply is evidence of completion, not proof of answer quality or
// successful external delivery. Only sessions with a start in the window count.
type ActivityCounts struct {
	Events                int64 `json:"events"`
	Requests              int64 `json:"requests"`
	Replies               int64 `json:"replies"`
	ErrorEvents           int64 `json:"error_events"`
	DeadLetters           int64 `json:"dead_letters"`
	ToolCalls             int64 `json:"tool_calls"`
	Sessions              int64 `json:"sessions"`
	ReplyCompleteSessions int64 `json:"reply_complete_sessions"`
	ErrorSessions         int64 `json:"error_sessions"`
	UnresolvedSessions    int64 `json:"unresolved_sessions"`
}

type AgentActivity struct {
	AgentID string `json:"agent_id"`
	ActivityCounts
}

type ReplyLatency struct {
	Samples int   `json:"samples"`
	AvgMS   int64 `json:"avg_ms"`
	P95MS   int64 `json:"p95_ms"`
}

type OperationsActivity struct {
	Totals  ActivityCounts  `json:"totals"`
	ByAgent []AgentActivity `json:"by_agent"`
	Latency *ReplyLatency   `json:"single_request_reply_latency"`
}

// OperationsActivitySQL is shared by SQLite and PostgreSQL. No payloads, error
// text, prompts or tool arguments are read. The half-open interval prevents
// adjacent report windows from counting the same event twice. The extra row
// detects the safety bound; callers must never present a truncated total.
const OperationsActivitySQL = `SELECT agent_id, COALESCE(session_id, ''), COUNT(*),
	SUM(CASE WHEN type = 'message.in' THEN 1 ELSE 0 END),
	SUM(CASE WHEN type = 'message.out' THEN 1 ELSE 0 END),
	SUM(CASE WHEN type = 'error' THEN 1 ELSE 0 END),
	SUM(CASE WHEN type = 'message.dead_letter' THEN 1 ELSE 0 END),
	SUM(CASE WHEN type = 'tool.call' THEN 1 ELSE 0 END),
	MIN(CASE WHEN type = 'message.in' THEN created_at END),
	MAX(CASE WHEN type = 'message.out' THEN created_at END)
	FROM agent_events WHERE created_at >= $1 AND created_at < $2
	GROUP BY agent_id, COALESCE(session_id, '') LIMIT 100001`

// OperationsRows is the read-only subset shared by database/sql and pgx rows.
type OperationsRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

// ReadOperationsActivity gives both durable backends identical counting rules.
// Session identifiers are used for grouping only and never returned.
func ReadOperationsActivity(rows OperationsRows) (OperationsActivity, error) {
	out := OperationsActivity{ByAgent: []AgentActivity{}}
	agents := map[string]*AgentActivity{}
	var durations []int64
	groups := 0
	for rows.Next() {
		groups++
		if groups > 100000 {
			return OperationsActivity{}, fmt.Errorf("operations activity exceeds 100000 session groups; select a shorter window")
		}
		var id, session string
		var counts ActivityCounts
		var first, last any
		if err := rows.Scan(&id, &session, &counts.Events, &counts.Requests, &counts.Replies,
			&counts.ErrorEvents, &counts.DeadLetters, &counts.ToolCalls, &first, &last); err != nil {
			return OperationsActivity{}, err
		}
		if session != "" && counts.Requests > 0 {
			counts.Sessions = 1
			switch {
			case counts.ErrorEvents > 0 || counts.DeadLetters > 0:
				counts.ErrorSessions = 1
			case counts.Replies >= counts.Requests:
				counts.ReplyCompleteSessions = 1
			default:
				counts.UnresolvedSessions = 1
			}
			// Never treat a multi-turn conversation's elapsed time as latency.
			if counts.Requests == 1 && counts.Replies == 1 && counts.ErrorSessions == 0 {
				start, end := parseSQLiteTime(first), parseSQLiteTime(last)
				if !start.IsZero() && !end.IsZero() && !end.Before(start) {
					durations = append(durations, end.Sub(start).Milliseconds())
				}
			}
		}
		row := agents[id]
		if row == nil {
			row = &AgentActivity{AgentID: id}
			agents[id] = row
		}
		row.ActivityCounts.add(counts)
		out.Totals.add(counts)
	}
	if err := rows.Err(); err != nil {
		return OperationsActivity{}, err
	}
	for _, row := range agents {
		out.ByAgent = append(out.ByAgent, *row)
	}
	sort.Slice(out.ByAgent, func(i, j int) bool { return out.ByAgent[i].AgentID < out.ByAgent[j].AgentID })
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var sum int64
		for _, d := range durations {
			sum += d
		}
		out.Latency = &ReplyLatency{Samples: len(durations), AvgMS: sum / int64(len(durations)),
			P95MS: durations[(95*len(durations)+99)/100-1]}
	}
	return out, nil
}

func (c *ActivityCounts) add(v ActivityCounts) {
	c.Events += v.Events
	c.Requests += v.Requests
	c.Replies += v.Replies
	c.ErrorEvents += v.ErrorEvents
	c.DeadLetters += v.DeadLetters
	c.ToolCalls += v.ToolCalls
	c.Sessions += v.Sessions
	c.ReplyCompleteSessions += v.ReplyCompleteSessions
	c.ErrorSessions += v.ErrorSessions
	c.UnresolvedSessions += v.UnresolvedSessions
}

func (l *Logger) OperationsActivity(ctx context.Context, start, end time.Time) (OperationsActivity, error) {
	rows, err := l.db.QueryContext(ctx, OperationsActivitySQL, start.UTC(), end.UTC())
	if err != nil {
		return OperationsActivity{}, err
	}
	defer rows.Close()
	return ReadOperationsActivity(rows)
}
