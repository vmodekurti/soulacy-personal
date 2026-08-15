// workspace_test.go — the cross-tenant isolation contract for the Postgres
// action log. It mirrors internal/actionlog/workspace_test.go so both backends
// are held to the same boundary.
//
// Like the rest of this package's tests it skips loudly when no Postgres is
// reachable, so it is real evidence in the integration lane and compiles and
// vets everywhere else.
package postgres

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

func pgEvent(workspaceID, agentID, sessionID, kind string, payload any) message.Event {
	return message.Event{
		WorkspaceID: workspaceID, AgentID: agentID, SessionID: sessionID,
		Type: kind, Payload: payload, Timestamp: time.Now().UTC(),
	}
}

// flushEvents writes events and waits for the async writer to land them, so a
// read afterwards is deterministic rather than timing-dependent.
func flushEvents(t *testing.T, log *ActionLog, events ...message.Event) {
	t.Helper()
	for _, ev := range events {
		log.Append(ev)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var count int
		if err := log.pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_events`).Scan(&count); err != nil {
			t.Fatalf("count events: %v", err)
		}
		if count >= len(events) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d events landed before the deadline", count, len(events))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The mirror files are keyed by (workspace, agent), because agent IDs are only
// unique within a workspace. Two tenants each running an agent called
// "assistant" previously shared one file and each tail returned the other's
// runs.
func TestPostgresTailCannotReachAnotherWorkspace(t *testing.T) {
	log, _, _ := testStores(t)
	flushEvents(t, log,
		pgEvent("ws_a", "assistant", "s1", "message.in", map[string]any{"text": "alpha confidential"}),
		pgEvent("ws_b", "assistant", "s1", "message.in", map[string]any{"text": "beta confidential"}),
	)

	for _, tc := range []struct{ workspace, own, foreign string }{
		{"ws_a", "alpha confidential", "beta confidential"},
		{"ws_b", "beta confidential", "alpha confidential"},
	} {
		events, err := log.TailInWorkspace(tc.workspace, "assistant", 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("%s tailed %d events, want its own 1: %+v", tc.workspace, len(events), events)
		}
		text, _ := events[0].Payload.(map[string]any)["text"].(string)
		if text != tc.own {
			t.Errorf("%s read %q, want %q", tc.workspace, text, tc.own)
		}
	}
}

// Product invariant 7: the personal mirror stays at its historical path, and a
// tenant's mirror is namespaced beside it rather than merged into it.
func TestPostgresPersonalMirrorPathIsUnchanged(t *testing.T) {
	log, _, _ := testStores(t)
	if got := log.EventFilePath("bot"); got != filepath.Join(log.logDir, "bot.log") {
		t.Fatalf("personal mirror moved to %s", got)
	}
	want := filepath.Join(log.logDir, wsroot.NamespaceDir, "ws_a", "bot.log")
	if got := log.EventFilePathInWorkspace("ws_a", "bot"); got != want {
		t.Fatalf("tenant mirror = %s, want %s", got, want)
	}
	if got := log.EventFilePathInWorkspace(wsroot.PersonalWorkspaceID, "bot"); got != log.EventFilePath("bot") {
		t.Fatalf("the personal workspace resolved to a namespaced path: %s", got)
	}
}

// The poison-pill guard counts and quarantines per tenant, so two tenants
// replaying sessions that share an ID cannot quarantine each other.
func TestPostgresPoisonPillGuardIsWorkspaceScoped(t *testing.T) {
	log, _, _ := testStores(t)
	since := time.Now().Add(-time.Hour)
	flushEvents(t, log,
		pgEvent("ws_a", "bot", "shared", "message.in", message.Message{AgentID: "bot", SessionID: "shared", Channel: "telegram"}),
		pgEvent("ws_a", "bot", "shared", "message.in", message.Message{AgentID: "bot", SessionID: "shared", Channel: "telegram"}),
		pgEvent("ws_b", "bot", "shared", "message.in", message.Message{AgentID: "bot", SessionID: "shared", Channel: "telegram"}),
	)

	a, err := log.CountMessageInAttemptsInWorkspace("ws_a", "bot", "shared", since)
	if err != nil {
		t.Fatal(err)
	}
	b, err := log.CountMessageInAttemptsInWorkspace("ws_b", "bot", "shared", since)
	if err != nil {
		t.Fatal(err)
	}
	if a != 2 || b != 1 {
		t.Fatalf("attempt counts crossed the boundary: ws_a=%d ws_b=%d, want 2 and 1", a, b)
	}

	if err := log.MarkDeadLetterInWorkspace("ws_a", "bot", "shared", "poisoned"); err != nil {
		t.Fatal(err)
	}
	recovered, err := log.IncompleteMessageIns(since)
	if err != nil {
		t.Fatal(err)
	}
	var sawA, sawB bool
	for _, raw := range recovered {
		var msg message.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		switch msg.WorkspaceID {
		case "ws_a":
			sawA = true
		case "ws_b":
			sawB = true
		}
	}
	if sawA {
		t.Error("a quarantined workspace's run was still offered for recovery")
	}
	if !sawB {
		t.Error("another workspace's dead letter suppressed a healthy run's recovery")
	}
}

// Boot recovery is deployment-wide, so isolation depends on each payload
// carrying its own workspace. Without the stamp every recovered run would
// replay as personal.
func TestPostgresRecoveredRunsCarryTheirOwnWorkspace(t *testing.T) {
	log, _, _ := testStores(t)
	since := time.Now().Add(-time.Hour)
	flushEvents(t, log,
		pgEvent("ws_a", "bot", "s1", "message.in", message.Message{AgentID: "bot", SessionID: "s1", Channel: "telegram"}),
		pgEvent("ws_b", "bot", "s2", "message.in", message.Message{AgentID: "bot", SessionID: "s2", Channel: "telegram"}),
	)

	recovered, err := log.IncompleteMessageIns(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered %d runs, want both tenants' 2", len(recovered))
	}
	seen := map[string]bool{}
	for _, raw := range recovered {
		var msg message.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.WorkspaceID == "" {
			t.Fatalf("a recovered run carries no workspace and would replay as personal: %s", raw)
		}
		if msg.Channel != "telegram" {
			t.Errorf("stamping the workspace damaged the payload: %s", raw)
		}
		seen[msg.WorkspaceID] = true
	}
	if !seen["ws_a"] || !seen["ws_b"] {
		t.Fatalf("recovered runs did not keep their own tenants: %v", seen)
	}
}
