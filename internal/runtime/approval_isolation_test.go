// approval_isolation_test.go — MU-022 with the durable record wired in: the
// broker's guarantees must hold through the store, not only through the map.
package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/pkg/message"
)

func brokerWithStore(t *testing.T) (*ConfirmBroker, *approvals.Store) {
	t.Helper()
	store, err := approvals.Open(filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b := newConfirmBroker()
	b.SetStore(store, zap.NewNop())
	return b, store
}

// The record is what survives the process. A second broker over the same store
// — which is what a restarted gateway, or a second one, actually is — sees the
// same pending approvals.
func TestAPausedCallOutlivesTheProcessThatPausedIt(t *testing.T) {
	b, store := brokerWithStore(t)
	ctx := inWorkspace(context.Background(), "ws-a")
	b.Register(ctx, ApprovalRequest{CallID: "c1", Tool: "shell_exec", Reason: "risky", AgentID: "bot"})

	successor := newConfirmBroker()
	successor.SetStore(store, zap.NewNop())
	list := successor.List(ctx, "ws-a")
	if len(list) != 1 || list[0].CallID != "c1" || list[0].Tool != "shell_exec" {
		t.Fatalf("a restarted gateway cannot see the paused call: %+v", list)
	}
	if list[0].ExpiresAt.IsZero() {
		t.Fatal("the surviving record has no expiry")
	}
}

func TestDecisionOnAnotherBrokerReleasesTheBlockedBroker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.db")
	storeA, err := approvals.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := approvals.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	blocked, decider := newConfirmBroker(), newConfirmBroker()
	blocked.SetStore(storeA, zap.NewNop())
	decider.SetStore(storeB, zap.NewNop())
	ctx, cancel := context.WithTimeout(inWorkspace(context.Background(), "ws-a"), 3*time.Second)
	defer cancel()
	local := blocked.Register(ctx, ApprovalRequest{CallID: "cross-replica", Tool: "shell_exec"})
	done := make(chan error, 1)
	go func() {
		approved, waitErr := blocked.Await(ctx, "ws-a", "cross-replica", local)
		if waitErr == nil && !approved {
			waitErr = errors.New("approval was not released")
		}
		done <- waitErr
	}()
	if err := decider.Resolve(ctx, "ws-a", "cross-replica", true, approverIn("ws-a"), ""); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Through the store, the workspace boundary is the store's — and it answers
// ErrNotFound rather than admitting the approval exists.
func TestTheDurableRecordEnforcesTheWorkspaceBoundary(t *testing.T) {
	b, _ := brokerWithStore(t)
	victim := inWorkspace(context.Background(), "ws-victim")
	b.Register(victim, ApprovalRequest{CallID: "victim-call", Tool: "shell_exec",
		Args: map[string]any{"command": "cat /etc/shadow"}})

	attacker := inWorkspace(context.Background(), "ws-attacker")
	if got := b.List(attacker, "ws-attacker"); len(got) != 0 {
		t.Fatalf("another workspace lists %d approvals it does not own", len(got))
	}
	err := b.Resolve(attacker, "ws-victim", "victim-call", true, approverIn("ws-attacker"), "")
	if !errors.Is(err, approvals.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Criterion 5, end to end: the decision wakes the run, and the engine's check
// refuses a call that is not the one that was approved.
func TestAnApprovalReleasesOnlyTheCallItWasShownFor(t *testing.T) {
	e := newMinimalEngine(t)
	store, err := approvals.Open(filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.Broker().SetStore(store, zap.NewNop())

	ctx := inWorkspace(context.Background(), "ws-a")
	approved := map[string]any{"command": "rm /tmp/scratch"}
	e.Broker().Register(ctx, ApprovalRequest{CallID: "c1", Tool: "shell_exec", Args: approved})
	if err := e.Broker().Resolve(ctx, "ws-a", "c1", true, approverIn("ws-a"), ""); err != nil {
		t.Fatal(err)
	}

	// The call actually in hand matches what the human saw.
	if err := e.verifyApproved(ctx, "c1", message.ToolCall{Name: "shell_exec", Arguments: approved}); err != nil {
		t.Fatalf("the approved call was refused: %v", err)
	}
	// Substituted arguments do not.
	err = e.verifyApproved(ctx, "c1", message.ToolCall{Name: "shell_exec", Arguments: map[string]any{"command": "rm -rf /"}})
	if !errors.Is(err, approvals.ErrFingerprintMismatch) {
		t.Fatalf("a substituted call was released: %v", err)
	}
	// Nor does a different tool with the same arguments.
	err = e.verifyApproved(ctx, "c1", message.ToolCall{Name: "run_script", Arguments: approved})
	if !errors.Is(err, approvals.ErrFingerprintMismatch) {
		t.Fatalf("a substituted tool was released: %v", err)
	}
}

// A personal deployment has no durable record and one watcher, so the check is
// silent rather than fail-closed. Breaking every personal install to guard
// against a substitution only a second actor could perform is the wrong trade.
func TestFingerprintVerificationIsSilentWithNoStore(t *testing.T) {
	e := newMinimalEngine(t)
	ctx := inWorkspace(context.Background(), "ws-a")
	if err := e.verifyApproved(ctx, "never-registered", message.ToolCall{Name: "shell_exec"}); err != nil {
		t.Fatalf("the no-store path is not silent: %v", err)
	}
}

// A run that ends without an answer must close its record, or the approvals
// page keeps offering a decision that would release nothing — and the full
// question outlives both the run and the process.
func TestAnAbandonedRunClosesItsDurableRecord(t *testing.T) {
	b, store := brokerWithStore(t)
	ctx := inWorkspace(context.Background(), "ws-a")
	b.Register(ctx, ApprovalRequest{CallID: "abandoned", Tool: "shell_exec"})

	b.Forget(ctx, "abandoned")

	if got := b.List(ctx, "ws-a"); len(got) != 0 {
		t.Fatalf("the abandoned approval is still offered: %+v", got)
	}
	stored, err := store.Get(ctx, "ws-a", "abandoned")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != approvals.StatusInvalidated {
		t.Fatalf("status = %s, want invalidated", stored.Status)
	}
	if !strings.Contains(stored.DecisionReason, "no longer active") {
		t.Fatalf("the record does not say why it closed: %q", stored.DecisionReason)
	}
}

// The durable record holds the redacted arguments; the full ones stay in the
// blocked goroutine.
func TestTheDurableRecordNeverHoldsTheFullArguments(t *testing.T) {
	b, store := brokerWithStore(t)
	ctx := inWorkspace(context.Background(), "ws-a")
	b.Register(ctx, ApprovalRequest{CallID: "c", Tool: "http_request", Args: map[string]any{
		"url": "https://api.example.com/charge", "api_key": "sk-live-REAL",
	}})
	stored, err := store.Get(ctx, "ws-a", "c")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Args["api_key"] != "[redacted]" {
		t.Fatalf("the durable record holds a live credential: %v", stored.Args["api_key"])
	}
	// And the fingerprint still binds the real call, so redaction has not
	// weakened criterion 5.
	if stored.Fingerprint != approvals.Fingerprint("http_request", map[string]any{
		"url": "https://api.example.com/charge", "api_key": "sk-live-REAL",
	}) {
		t.Fatal("the fingerprint was taken over the redacted arguments")
	}
}
