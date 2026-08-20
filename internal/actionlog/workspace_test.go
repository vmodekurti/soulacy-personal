// workspace_test.go — the cross-tenant isolation contract for the action log.
//
// internal/ownership/catalog.go names this file as the isolation evidence for
// the agent_events table and the action-log repository.
package actionlog

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestWorkspaceExportAndPurgeAreCompleteAndScoped(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	logger = appendAndFlush(t, logger, dir,
		event("ws_delete", "assistant", "s1", "message.in", map[string]any{"text": "delete-only"}),
		event("ws_delete", "assistant", "s1", "message.out", map[string]any{"text": "delete-reply"}),
		event("ws_keep", "assistant", "s1", "message.in", map[string]any{"text": "keep-only"}),
	)

	var archive bytes.Buffer
	count, err := logger.ExportWorkspaceJSONL(context.Background(), "ws_delete", &archive)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("exported %d events, want 2", count)
	}
	if got := archive.String(); !strings.Contains(got, "delete-only") || !strings.Contains(got, "delete-reply") || strings.Contains(got, "keep-only") {
		t.Fatalf("workspace export crossed its boundary: %s", got)
	}

	removed, err := logger.PurgeWorkspace(context.Background(), "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Rows < 3 { // two SQL rows plus at least one mirror file
		t.Fatalf("purge reported %+v, want two rows and a mirror file", removed)
	}
	deleted, err := logger.QueryEventsInWorkspace("ws_delete", "", "", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := logger.QueryEventsInWorkspace("ws_keep", "", "", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 || len(kept) != 1 {
		t.Fatalf("after purge: deleted=%d kept=%d", len(deleted), len(kept))
	}
	if _, err := os.Stat(filepath.Join(dir, wsroot.NamespaceDir, "ws_delete")); !os.IsNotExist(err) {
		t.Fatalf("workspace mirror tree survived purge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, wsroot.NamespaceDir, "ws_keep", "assistant.log")); err != nil {
		t.Fatalf("other workspace mirror was removed: %v", err)
	}
}

func TestWorkspacePurgeRefusesThePersonalActionLogRoot(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	logger = appendAndFlush(t, logger, dir,
		event(wsroot.PersonalWorkspaceID, "assistant", "s1", "message.in", map[string]any{"text": "personal"}),
	)
	if _, err := logger.PurgeWorkspace(context.Background(), wsroot.PersonalWorkspaceID); err == nil {
		t.Fatal("personal workspace purge was not refused")
	}
	events, err := logger.QueryEventsInWorkspace(wsroot.PersonalWorkspaceID, "", "", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("refused purge still deleted %d personal events", 1-len(events))
	}
}

func newWorkspaceLogger(t *testing.T) (*Logger, string) {
	t.Helper()
	dir := t.TempDir()
	logger, err := New(dir, filepath.Join(dir, "events.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger, dir
}

func event(workspaceID, agentID, sessionID, kind string, payload any) message.Event {
	return message.Event{
		WorkspaceID: workspaceID, AgentID: agentID, SessionID: sessionID,
		Type: kind, Payload: payload, Timestamp: time.Now().UTC(),
	}
}

// appendAndFlush writes events and waits for the async writer to land them.
// Close drains the queue, so reopening is the deterministic way to observe a
// completed write without sleeping.
func appendAndFlush(t *testing.T, logger *Logger, dir string, events ...message.Event) *Logger {
	t.Helper()
	for _, ev := range events {
		logger.Append(ev)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := New(dir, filepath.Join(dir, "events.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
}

// The defect this replaces: the log was one file per agent ID, and agent IDs
// are only unique within a workspace. Two tenants each running an agent called
// "assistant" appended to one file, and each tail returned the other's runs.
func TestTailCannotReachAnotherWorkspacesEvents(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	logger = appendAndFlush(t, logger, dir,
		event("ws_a", "assistant", "s1", "message.in", map[string]any{"text": "alpha confidential"}),
		event("ws_b", "assistant", "s1", "message.in", map[string]any{"text": "beta confidential"}),
	)

	for _, tc := range []struct{ workspace, own, foreign string }{
		{"ws_a", "alpha confidential", "beta confidential"},
		{"ws_b", "beta confidential", "alpha confidential"},
	} {
		events, err := logger.TailInWorkspace(tc.workspace, "assistant", 100)
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
		if text == tc.foreign {
			t.Errorf("%s read another workspace's event", tc.workspace)
		}
	}
}

// Product invariant 7: a single-user installation's log stays at the path it
// has always been at, and a tenant's events never land beside it.
func TestPersonalLogPathIsUnchanged(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	logger = appendAndFlush(t, logger, dir,
		event(wsroot.PersonalWorkspaceID, "bot", "s1", "message.in", map[string]any{"text": "personal"}),
		event("", "bot", "s2", "message.in", map[string]any{"text": "unworkspaced"}),
		event("ws_a", "bot", "s3", "message.in", map[string]any{"text": "tenant"}),
	)

	historical := filepath.Join(dir, "bot.log")
	if _, err := os.Stat(historical); err != nil {
		t.Fatalf("personal log is not at its historical path %s: %v", historical, err)
	}
	namespaced := filepath.Join(dir, wsroot.NamespaceDir, "ws_a", "bot.log")
	if _, err := os.Stat(namespaced); err != nil {
		t.Fatalf("a tenant's log was not namespaced: %v", err)
	}

	// An event with no workspace is a single-tenant event, not an unowned one:
	// it belongs to personal, which is what a pre-tenant installation was.
	personal, err := logger.Tail("bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(personal) != 2 {
		t.Fatalf("personal tail = %d events, want the personal and unworkspaced ones: %+v", len(personal), personal)
	}
	for _, ev := range personal {
		if text, _ := ev.Payload.(map[string]any)["text"].(string); text == "tenant" {
			t.Fatal("a personal tail read a tenant's event")
		}
	}
}

// Durable queries carry the tenant predicate inside the query. The "all
// agents" form is the dangerous one: it is a legitimate request within a
// workspace, and must not become a way to ask for every tenant.
func TestDurableQueriesCarryTheWorkspacePredicate(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	logger = appendAndFlush(t, logger, dir,
		event("ws_a", "assistant", "shared-session", "message.in", map[string]any{"text": "alpha"}),
		event("ws_b", "assistant", "shared-session", "message.in", map[string]any{"text": "beta"}),
	)

	for _, workspaceID := range []string{"ws_a", "ws_b"} {
		// Every agent, every session — within one workspace.
		everything, err := logger.QueryEventsInWorkspace(workspaceID, "", "", 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(everything) != 1 {
			t.Fatalf("%s all-agents query returned %d events, want its own 1: %+v", workspaceID, len(everything), everything)
		}
		if got := everything[0].WorkspaceID; got != workspaceID {
			t.Errorf("%s query returned an event owned by %q", workspaceID, got)
		}
		// Naming the shared session ID explicitly must not widen it either.
		bySession, err := logger.QueryEventsInWorkspace(workspaceID, "assistant", "shared-session", 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(bySession) != 1 || bySession[0].WorkspaceID != workspaceID {
			t.Errorf("%s session query = %+v", workspaceID, bySession)
		}
	}
}

// The poison-pill guard counts and quarantines per tenant. Without the
// predicate, two tenants replaying sessions that share an ID sum into one
// count and quarantine each other's healthy run.
func TestPoisonPillGuardIsWorkspaceScoped(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	since := time.Now().Add(-time.Hour)
	logger = appendAndFlush(t, logger, dir,
		event("ws_a", "bot", "shared", "message.in", map[string]any{"text": "a1"}),
		event("ws_a", "bot", "shared", "message.in", map[string]any{"text": "a2"}),
		event("ws_b", "bot", "shared", "message.in", map[string]any{"text": "b1"}),
	)

	a, err := logger.CountMessageInAttemptsInWorkspace("ws_a", "bot", "shared", since)
	if err != nil {
		t.Fatal(err)
	}
	b, err := logger.CountMessageInAttemptsInWorkspace("ws_b", "bot", "shared", since)
	if err != nil {
		t.Fatal(err)
	}
	if a != 2 || b != 1 {
		t.Fatalf("attempt counts crossed the boundary: ws_a=%d ws_b=%d, want 2 and 1", a, b)
	}

	// Quarantining one tenant's session must not quarantine the other's.
	if err := logger.MarkDeadLetterInWorkspace("ws_a", "bot", "shared", "poisoned"); err != nil {
		t.Fatal(err)
	}
	recovered, err := logger.IncompleteMessageIns(since)
	if err != nil {
		t.Fatal(err)
	}
	var sawA, sawB bool
	for _, raw := range recovered {
		var msg message.Message
		if err := decodeMessage(raw, &msg); err != nil {
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

// Boot recovery is deployment-wide by design — every tenant's interrupted run
// must be recovered — so isolation is preserved by stamping each payload with
// the workspace it belongs to. Without the stamp, every recovered run would
// re-enter the engine as personal.
func TestRecoveredRunsCarryTheirOwnWorkspace(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	since := time.Now().Add(-time.Hour)
	logger = appendAndFlush(t, logger, dir,
		event("ws_a", "bot", "s1", "message.in", message.Message{AgentID: "bot", SessionID: "s1", Channel: "telegram"}),
		event("ws_b", "bot", "s1", "message.in", message.Message{AgentID: "bot", SessionID: "s1", Channel: "telegram"}),
	)

	recovered, err := logger.IncompleteMessageIns(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered %d runs, want both tenants' 2", len(recovered))
	}
	seen := map[string]bool{}
	for _, raw := range recovered {
		var msg message.Message
		if err := decodeMessage(raw, &msg); err != nil {
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

// A run completing in one workspace must not mark another workspace's
// same-named session complete, which would silently drop it from recovery.
func TestCompletionInOneWorkspaceDoesNotSuppressAnother(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	since := time.Now().Add(-time.Hour)
	logger = appendAndFlush(t, logger, dir,
		event("ws_a", "bot", "shared", "message.in", message.Message{AgentID: "bot", SessionID: "shared", Channel: "telegram"}),
		event("ws_b", "bot", "shared", "message.in", message.Message{AgentID: "bot", SessionID: "shared", Channel: "telegram"}),
	)
	// Only ws_a completes.
	logger = appendAndFlush(t, logger, dir,
		event("ws_a", "bot", "shared", "message.out", message.Message{AgentID: "bot", SessionID: "shared"}),
	)

	recovered, err := logger.IncompleteMessageIns(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 {
		t.Fatalf("recovered %d runs, want only the incomplete one: %d", len(recovered), len(recovered))
	}
	var msg message.Message
	if err := decodeMessage(recovered[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.WorkspaceID != "ws_b" {
		t.Fatalf("recovery kept the wrong tenant's run: %q", msg.WorkspaceID)
	}
}

// Retention must reach every tenant. A deletion guarantee that silently
// applies to one workspace only is worse than none.
func TestRetentionSweepReachesEveryWorkspace(t *testing.T) {
	logger, dir := newWorkspaceLogger(t)
	rotated := map[string]string{
		"personal": filepath.Join(dir, "bot.log.1.gz"),
		"tenant":   filepath.Join(dir, wsroot.NamespaceDir, "ws_a", "bot.log.1.gz"),
	}
	for _, path := range rotated {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.DeleteBefore(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for name, path := range rotated {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s rotated log survived the retention sweep: %s", name, path)
		}
	}
}

// decodeMessage is a local helper so the test reads a recovered payload the
// same way the recovery pass does.
func decodeMessage(raw []byte, into *message.Message) error {
	return json.Unmarshal(raw, into)
}
