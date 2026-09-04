package actionlog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SessionStats summarises the event trail of one session (run): how many
// events and tool calls were recorded, the time span, and the most recent
// error payload if the run failed. Zero-valued when the session is unknown.
type SessionStats struct {
	Events     int       `json:"events"`
	ToolCalls  int       `json:"tool_calls"`
	FirstEvent time.Time `json:"first_event"`
	LastEvent  time.Time `json:"last_event"`
	LastError  string    `json:"last_error,omitempty"`
}

// SessionStats aggregates agent_events for (agentID, sessionID). An empty
// agentID matches events from any agent sharing the session.
func (l *Logger) SessionStats(agentID, sessionID string) (SessionStats, error) {
	var st SessionStats

	where := `session_id = ?`
	args := []any{sessionID}
	if agentID != "" {
		where = `agent_id = ? AND session_id = ?`
		args = []any{agentID, sessionID}
	}

	err := l.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN type = 'tool.call' THEN 1 ELSE 0 END), 0)
		FROM agent_events WHERE `+where, args...).
		Scan(&st.Events, &st.ToolCalls)
	if err != nil {
		return SessionStats{}, err
	}
	if st.Events == 0 {
		return st, nil
	}

	if err := l.db.QueryRow(`
		SELECT created_at FROM agent_events WHERE `+where+`
		ORDER BY created_at ASC, id ASC LIMIT 1`, args...).
		Scan(&st.FirstEvent); err != nil {
		return SessionStats{}, err
	}
	if err := l.db.QueryRow(`
		SELECT created_at FROM agent_events WHERE `+where+`
		ORDER BY created_at DESC, id DESC LIMIT 1`, args...).
		Scan(&st.LastEvent); err != nil {
		return SessionStats{}, err
	}

	// Most recent error payload, if any.
	var payload string
	err = l.db.QueryRow(`
		SELECT COALESCE(payload, '') FROM agent_events WHERE `+where+`
		AND type = 'error' ORDER BY created_at DESC, id DESC LIMIT 1`, args...).
		Scan(&payload)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// no error events — fine
	case err != nil:
		return SessionStats{}, err
	default:
		st.LastError = extractErrorText(payload)
	}
	return st, nil
}

// extractErrorText pulls a human-readable message out of an error event
// payload. Payloads are JSON objects (usually {"error": "..."}); fall back
// to the raw payload when the shape is unexpected.
func extractErrorText(payload string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err == nil {
		for _, key := range []string{"error", "detail", "message", "reason"} {
			if v, ok := obj[key].(string); ok && v != "" {
				return v
			}
		}
	}
	return payload
}
