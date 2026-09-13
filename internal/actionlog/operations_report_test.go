package actionlog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOperationsActivityWindowCountingAndPrivacy(t *testing.T) {
	l := newStatsTestLogger(t)
	start := time.Date(2026, 9, 12, 12, 0, 0, 0, time.FixedZone("test", -5*3600))
	end := start.Add(time.Hour)
	insert := func(agent, session, typ string, at time.Time) {
		t.Helper()
		_, err := l.db.Exec(`INSERT INTO agent_events (agent_id,session_id,type,payload,created_at) VALUES (?,?,?,?,?)`, agent, session, typ, `{"secret":"NEVER_INCLUDE_THIS_PAYLOAD"}`, at.UTC())
		if err != nil {
			t.Fatal(err)
		}
	}
	// Same session ID on different agents is not one session. A recovered error
	// remains "with errors", not silently relabelled successful.
	insert("a", "private-session", "message.in", start)
	insert("a", "private-session", "tool.call", start.Add(time.Second))
	insert("a", "private-session", "message.out", start.Add(2*time.Second))
	insert("b", "private-session", "message.in", start)
	insert("b", "private-session", "error", start.Add(time.Second))
	insert("b", "private-session", "message.out", start.Add(3*time.Second))
	// Two requests and one response in a conversation are not two finished runs.
	insert("a", "multi", "message.in", start)
	insert("a", "multi", "message.out", start.Add(time.Second))
	insert("a", "multi", "message.in", start.Add(10*time.Second))
	insert("a", "dead", "message.in", start)
	insert("a", "dead", "message.dead_letter", start.Add(time.Second))
	// Missing IDs count as events/requests, never fabricated sessions.
	insert("", "", "message.in", start)
	// Cross-boundary reply contributes an event, not a newly-started session.
	insert("a", "old", "message.in", start.Add(-time.Second))
	insert("a", "old", "message.out", start.Add(time.Second))
	insert("outside", "future", "message.in", end)
	got, err := l.OperationsActivity(t.Context(), start, end)
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals != (ActivityCounts{Events: 13, Requests: 6, Replies: 4, ErrorEvents: 1, DeadLetters: 1, ToolCalls: 1, Sessions: 4, ReplyCompleteSessions: 1, ErrorSessions: 2, UnresolvedSessions: 1}) {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if got.Latency == nil || got.Latency.Samples != 1 || got.Latency.AvgMS != 2000 || got.Latency.P95MS != 2000 {
		t.Fatalf("latency = %+v", got.Latency)
	}
	if len(got.ByAgent) != 3 || got.ByAgent[0].AgentID != "" {
		t.Fatalf("agent ordering = %+v", got.ByAgent)
	}
	data, _ := json.Marshal(got)
	for _, secret := range []string{"NEVER_INCLUDE_THIS_PAYLOAD", "private-session", "future"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("report leaked %q", secret)
		}
	}
}

func TestOperationsActivityEmptyCancellationAndLatency(t *testing.T) {
	l := newStatsTestLogger(t)
	start := time.Now().UTC().Truncate(time.Second)
	got, err := l.OperationsActivity(t.Context(), start, start.Add(time.Hour))
	if err != nil || got.Latency != nil || got.ByAgent == nil || got.Totals.Events != 0 {
		t.Fatalf("empty = %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := l.OperationsActivity(ctx, start, start.Add(time.Hour)); err == nil {
		t.Fatal("canceled query succeeded")
	}
	for i := 1; i <= 20; i++ {
		for _, event := range []struct {
			typ string
			at  time.Time
		}{{"message.in", start}, {"message.out", start.Add(time.Duration(i) * time.Second)}} {
			if _, err := l.db.Exec(`INSERT INTO agent_events (agent_id,session_id,type,created_at) VALUES (?,?,?,?)`, "a", fmt.Sprint(i), event.typ, event.at); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err = l.OperationsActivity(t.Context(), start, start.Add(time.Hour))
	if err != nil || got.Latency == nil || got.Latency.P95MS != 19000 || got.Latency.AvgMS != 10500 {
		t.Fatalf("p95 = %+v, %v", got.Latency, err)
	}
}

type boundedOperationsRows struct {
	count           int
	scanErr, rowErr error
}

func (r *boundedOperationsRows) Next() bool        { r.count++; return r.count <= 100001 }
func (r *boundedOperationsRows) Scan(...any) error { return r.scanErr }
func (r *boundedOperationsRows) Err() error        { return r.rowErr }
func TestOperationsActivityDoesNotReturnTruncatedTotals(t *testing.T) {
	if _, err := ReadOperationsActivity(&boundedOperationsRows{}); err == nil {
		t.Fatal("group limit silently truncated")
	}
	if _, err := ReadOperationsActivity(&boundedOperationsRows{scanErr: fmt.Errorf("scan failed")}); err == nil {
		t.Fatal("scan failure hidden")
	}
	if _, err := ReadOperationsActivity(&boundedOperationsRows{count: 100001, rowErr: fmt.Errorf("rows failed")}); err == nil {
		t.Fatal("iteration failure hidden")
	}
}
