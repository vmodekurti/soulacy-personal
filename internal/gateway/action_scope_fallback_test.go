package gateway

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// workspaceTailOnlyLog models PostgreSQL's contract: workspace-aware Tail,
// but no optional TailFilteredInWorkspace optimization.
type workspaceTailOnlyLog struct {
	wantWorkspace string
	events        []message.Event
	seenWorkspace string
}

func (l *workspaceTailOnlyLog) Append(message.Event) {}
func (l *workspaceTailOnlyLog) Tail(string, int) ([]message.Event, error) {
	panic("unscoped tail must never be used for a tenant")
}
func (l *workspaceTailOnlyLog) TailInWorkspace(workspaceID, _ string, _ int) ([]message.Event, error) {
	l.seenWorkspace = workspaceID
	if workspaceID != l.wantWorkspace {
		panic("cross-workspace tail")
	}
	return l.events, nil
}
func (l *workspaceTailOnlyLog) EventFilePath(string) string { return "" }
func (l *workspaceTailOnlyLog) IncompleteMessageIns(time.Time) ([][]byte, error) {
	return nil, nil
}
func (l *workspaceTailOnlyLog) CountMessageInAttempts(string, string, time.Time) (int, error) {
	return 0, nil
}
func (l *workspaceTailOnlyLog) CountMessageInAttemptsInWorkspace(string, string, string, time.Time) (int, error) {
	return 0, nil
}
func (l *workspaceTailOnlyLog) MarkDeadLetter(string, string, string) error { return nil }
func (l *workspaceTailOnlyLog) MarkDeadLetterInWorkspace(string, string, string, string) error {
	return nil
}
func (l *workspaceTailOnlyLog) Close() error { return nil }

func TestTailFilteredFallsBackToWorkspaceTailWithoutCrossTenantRead(t *testing.T) {
	backend := &workspaceTailOnlyLog{
		wantWorkspace: "ws_a",
		events: []message.Event{
			{WorkspaceID: "ws_a", AgentID: "analyst", Type: "llm.call"},
			{WorkspaceID: "ws_a", AgentID: "analyst", Type: "llm.result"},
		},
	}
	scope := actionScope{actions: backend, scoped: backend, workspaceID: "ws_a"}
	events, err := scope.TailFiltered("analyst", 100, map[string]bool{"llm.call": true})
	if err != nil {
		t.Fatalf("workspace-safe fallback failed: %v", err)
	}
	if backend.seenWorkspace != "ws_a" {
		t.Fatalf("tail used workspace %q", backend.seenWorkspace)
	}
	// The scoped wrapper returns the safe unfiltered window; handleAgentActions
	// performs the documented post-filter for backends without the optimization.
	if len(events) != 2 {
		t.Fatalf("fallback returned %d events, want the complete scoped window", len(events))
	}
}
