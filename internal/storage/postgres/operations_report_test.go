package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPostgresOperationsReport(t *testing.T) {
	al, _, pool := testStores(t)
	start := time.Date(2026, 9, 12, 12, 0, 0, 0, time.FixedZone("test", -5*3600))
	end := start.Add(time.Hour)
	for _, row := range []struct {
		agent, session, typ string
		at                  time.Time
	}{
		{"a", "same", "message.in", start}, {"a", "same", "message.out", start.Add(2 * time.Second)},
		{"b", "same", "message.in", start}, {"b", "same", "error", start.Add(time.Second)},
		{"b", "same", "message.out", start.Add(3 * time.Second)}, {"a", "multi", "message.in", start},
		{"a", "multi", "message.out", start.Add(time.Second)}, {"a", "multi", "message.in", start.Add(4 * time.Second)},
		{"a", "dead", "message.in", start}, {"a", "dead", "message.dead_letter", start.Add(time.Second)},
		{"outside", "old", "message.in", start.Add(-time.Second)}, {"outside", "future", "message.in", end},
		{"", "", "tool.call", start},
	} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO agent_events(agent_id,session_id,type,payload,created_at) VALUES ($1,$2,$3,$4,$5)`, row.agent, row.session, row.typ, `{"text":"private-payload"}`, row.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := al.OperationsActivity(t.Context(), start, end)
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals.Events != 11 || got.Totals.Requests != 5 || got.Totals.Sessions != 4 || got.Totals.ReplyCompleteSessions != 1 || got.Totals.ErrorSessions != 2 || got.Totals.UnresolvedSessions != 1 {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if got.Latency == nil || got.Latency.AvgMS != 2000 || got.Latency.Samples != 1 {
		t.Fatalf("latency = %+v", got.Latency)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "private-payload") || strings.Contains(string(data), "outside") {
		t.Fatal("unexpected data in report")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := al.OperationsActivity(ctx, start, end); err == nil {
		t.Fatal("canceled query succeeded")
	}
}
