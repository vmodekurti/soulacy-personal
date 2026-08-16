// store_test.go — MU-022: an approval is a durable, workspace-owned record;
// only current eligible members may see or decide it; a decision is single-use
// and binds to the exact call.
package approvals

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func member(subject, workspaceID string, allowed ...string) Eligibility {
	permitted := map[string]bool{}
	for _, a := range allowed {
		permitted[a] = true
	}
	return Eligibility{
		Subject: subject, WorkspaceID: workspaceID,
		Permits: func(resource, action string) bool { return permitted[resource+":"+action] },
	}
}

func request(t *testing.T, store *Store, id, workspaceID string, args map[string]any) Approval {
	t.Helper()
	approval, err := store.Request(context.Background(), Approval{
		ID: id, WorkspaceID: workspaceID, RunID: "run_" + id, AgentID: "bot",
		Tool: "shell_exec", Reason: "privileged",
		RequesterSubject: "usr_asker",
		RequiredResource: "chat", RequiredAction: "chat",
	}, args)
	if err != nil {
		t.Fatalf("request %s: %v", id, err)
	}
	return approval
}

// Criterion 1: everything an auditor or an approver needs is on the record,
// and it survives the process that created it.
func TestAnApprovalRecordsEverythingTheDecisionNeeds(t *testing.T) {
	store := newStore(t)
	approval := request(t, store, "apr_1", "ws-a", map[string]any{"command": "rm -rf /data"})

	if approval.WorkspaceID != "ws-a" || approval.RunID != "run_apr_1" || approval.Tool != "shell_exec" {
		t.Fatalf("record is missing its subject matter: %+v", approval)
	}
	if approval.RequesterSubject == "" || approval.RequiredResource == "" || approval.RequiredAction == "" {
		t.Fatal("record does not say who asked or what answering it requires")
	}
	if approval.ExpiresAt.IsZero() || !approval.ExpiresAt.After(approval.CreatedAt) {
		t.Fatal("record has no expiry; a paused action would wait forever")
	}
	if approval.Fingerprint == "" {
		t.Fatal("record has no fingerprint; approval would authorize any later call")
	}
	if approval.Status != StatusPending || approval.DecidedBy != "" || approval.DecidedAt != nil {
		t.Fatalf("a fresh approval already has a decision: %+v", approval)
	}
	// And it is durable: a second Store over the same file sees it.
	stored, err := store.Get(context.Background(), "ws-a", "apr_1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fingerprint != approval.Fingerprint {
		t.Fatal("the stored record does not match what was returned")
	}
}

// Criterion 2, the important half. `admin` used to be a role with no tenant in
// it, so an admin of any workspace could list and decide every other
// workspace's paused calls — and their arguments.
func TestAnotherWorkspacesAdminCanNeitherSeeNorDecide(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	approval := request(t, store, "apr_x", "ws-victim", map[string]any{"command": "cat /etc/shadow"})

	outsider := member("usr_admin", "ws-attacker", "chat:chat")
	if CanView(approval, outsider) {
		t.Error("another workspace's admin can view a paused call")
	}
	_, err := store.Decide(ctx, "ws-victim", "apr_x", true, outsider, "")
	// ErrNotFound, not ErrNotEligible: telling them it exists but is not
	// theirs turns approval IDs into an enumeration oracle.
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// It is untouched.
	stored, err := store.Get(ctx, "ws-victim", "apr_x")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusPending {
		t.Fatalf("status = %s after a foreign decision attempt", stored.Status)
	}
	// And listing is scoped the same way.
	pending, err := store.ListPending(ctx, "ws-attacker")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("attacker's workspace lists %d approvals it does not own", len(pending))
	}
}

// Criterion 2, the other half: a member of the right workspace who lacks the
// permission the request names is not an approver either.
func TestAMemberWithoutTheRequiredPermissionCannotDecide(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	request(t, store, "apr_p", "ws-a", map[string]any{"command": "deploy"})

	viewer := member("usr_viewer", "ws-a") // permits nothing
	if _, err := store.Decide(ctx, "ws-a", "apr_p", true, viewer, ""); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("err = %v, want ErrNotEligible", err)
	}
	approver := member("usr_ops", "ws-a", "chat:chat")
	decided, err := store.Decide(ctx, "ws-a", "apr_p", true, approver, "looks right")
	if err != nil {
		t.Fatalf("an eligible member was refused: %v", err)
	}
	// The actor is recorded, and it is the DECIDER, not the requester.
	if decided.DecidedBy != "usr_ops" {
		t.Fatalf("decided_by = %q, want the deciding actor", decided.DecidedBy)
	}
	if decided.DecidedBy == decided.RequesterSubject {
		t.Fatal("the audit trail credits the request to the person it was taken from")
	}
	if decided.DecidedAt == nil || decided.DecisionReason != "looks right" {
		t.Fatalf("decision metadata is incomplete: %+v", decided)
	}
}

// Criterion 4. Two approvers racing is what a team notification produces.
func TestADecisionIsSingleUseUnderConcurrentApprovers(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	request(t, store, "apr_race", "ws-a", map[string]any{"command": "ship"})

	const approvers = 8
	var wg sync.WaitGroup
	results := make([]error, approvers)
	start := make(chan struct{})
	for i := 0; i < approvers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Half approve, half deny: the winner must be ONE of them, and
			// the record must not end up describing both.
			_, err := store.Decide(ctx, "ws-a", "apr_race", i%2 == 0,
				member("usr_"+string(rune('a'+i)), "ws-a", "chat:chat"), "")
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrNotPending):
		default:
			t.Fatalf("approver %d got an unexpected error: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d approvers succeeded, want exactly 1", winners)
	}
	stored, err := store.Get(ctx, "ws-a", "apr_race")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusApproved && stored.Status != StatusDenied {
		t.Fatalf("status = %s", stored.Status)
	}
	if stored.DecidedBy == "" {
		t.Fatal("a decided approval has no actor")
	}
	// A second decision after the race is still refused.
	if _, err := store.Decide(ctx, "ws-a", "apr_race", true, member("usr_late", "ws-a", "chat:chat"), ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("a late decision was accepted: %v", err)
	}
}

// Criterion 5. An approval that names only a tool authorizes every future use
// of it; this authorizes one.
func TestAnApprovalCoversOnlyTheExactCall(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	args := map[string]any{"command": "rm /tmp/scratch", "timeout_seconds": 30}
	request(t, store, "apr_fp", "ws-a", args)
	approved, err := store.Decide(ctx, "ws-a", "apr_fp", true, member("usr_ops", "ws-a", "chat:chat"), "")
	if err != nil {
		t.Fatal(err)
	}

	if err := approved.VerifyFingerprint("shell_exec", args); err != nil {
		t.Fatalf("the approved call does not verify against itself: %v", err)
	}
	for name, altered := range map[string]map[string]any{
		"changed argument": {"command": "rm -rf /", "timeout_seconds": 30},
		"added argument":   {"command": "rm /tmp/scratch", "timeout_seconds": 30, "working_dir": "/"},
		"dropped argument": {"command": "rm /tmp/scratch"},
	} {
		if err := approved.VerifyFingerprint("shell_exec", altered); !errors.Is(err, ErrFingerprintMismatch) {
			t.Errorf("%s verified against the approval: %v", name, err)
		}
	}
	if err := approved.VerifyFingerprint("run_script", args); !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("a different tool verified against the approval: %v", err)
	}
	// A denied decision authorizes nothing, whatever the arguments are.
	request(t, store, "apr_no", "ws-a", args)
	denied, err := store.Decide(ctx, "ws-a", "apr_no", false, member("usr_ops", "ws-a", "chat:chat"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := denied.VerifyFingerprint("shell_exec", args); err == nil {
		t.Fatal("a denied approval verified an execution")
	}
}

// Fingerprints must not collide across the tool/args boundary.
func TestFingerprintsDoNotCollideAcrossTheToolBoundary(t *testing.T) {
	if Fingerprint("a", map[string]any{"b": 1}) == Fingerprint("ab", map[string]any{"": 1}) {
		t.Fatal("tool name and arguments run together in the digest")
	}
	// Key order is not part of the call.
	one := map[string]any{"x": 1, "y": 2}
	two := map[string]any{"y": 2, "x": 1}
	if Fingerprint("t", one) != Fingerprint("t", two) {
		t.Fatal("the same call fingerprints differently depending on map iteration order")
	}
}

// Criterion 3, all four triggers.
func TestPendingDecisionsAreInvalidatedWhenTheirPremiseChanges(t *testing.T) {
	ctx := context.Background()

	t.Run("run cancelled", func(t *testing.T) {
		store := newStore(t)
		request(t, store, "apr_r", "ws-a", nil)
		n, err := store.InvalidateRun(ctx, "ws-a", "run_apr_r", ReasonRunEnded)
		if err != nil || n != 1 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		assertClosed(t, store, "ws-a", "apr_r", ReasonRunEnded)
	})

	t.Run("requester's membership revoked", func(t *testing.T) {
		store := newStore(t)
		request(t, store, "apr_m", "ws-a", nil)
		if _, err := store.InvalidateSubject(ctx, "ws-a", "usr_asker", ReasonMembershipRevoked); err != nil {
			t.Fatal(err)
		}
		assertClosed(t, store, "ws-a", "apr_m", ReasonMembershipRevoked)
	})

	t.Run("policy changed", func(t *testing.T) {
		store := newStore(t)
		request(t, store, "apr_c", "ws-a", nil)
		request(t, store, "apr_d", "ws-b", nil)
		if _, err := store.InvalidateWorkspace(ctx, "ws-a", ReasonPolicyChanged); err != nil {
			t.Fatal(err)
		}
		assertClosed(t, store, "ws-a", "apr_c", ReasonPolicyChanged)
		// A workspace-wide invalidation is workspace-wide.
		other, err := store.Get(ctx, "ws-b", "apr_d")
		if err != nil {
			t.Fatal(err)
		}
		if other.Status != StatusPending {
			t.Fatalf("one workspace's policy change closed another's approvals: %s", other.Status)
		}
	})

	t.Run("expired", func(t *testing.T) {
		store := newStore(t)
		expired, err := store.Request(ctx, Approval{
			ID: "apr_e", WorkspaceID: "ws-a", Tool: "shell_exec",
			RequiredResource: "chat", RequiredAction: "chat",
			ExpiresAt: time.Now().UTC().Add(-time.Second),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Refused on READ before any sweep runs: the guarantee cannot depend
		// on a timer having fired.
		if expired.Pending(time.Now().UTC()) {
			t.Fatal("an expired approval reads as pending")
		}
		if _, err := store.Decide(ctx, "ws-a", "apr_e", true, member("usr_ops", "ws-a", "chat:chat"), ""); !errors.Is(err, ErrNotPending) {
			t.Fatalf("an expired approval was decided: %v", err)
		}
		pending, err := store.ListPending(ctx, "ws-a")
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 0 {
			t.Fatal("an expired approval is still listed as awaiting a decision")
		}
		// Refusing on read also relabels, so the row a later reader finds
		// says what every reader already computed rather than needing the
		// sweep to have caught up.
		stored, err := store.Get(ctx, "ws-a", "apr_e")
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != StatusExpired {
			t.Fatalf("status = %s after a refused decision, want expired", stored.Status)
		}

		// And the sweep catches the ones nobody touched at all. This is the
		// common case: an approval that expires is one nobody came back to.
		untouched, err := store.Request(ctx, Approval{
			ID: "apr_e2", WorkspaceID: "ws-a", Tool: "shell_exec",
			RequiredResource: "chat", RequiredAction: "chat",
			ExpiresAt: time.Now().UTC().Add(-time.Second),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := store.SweepExpired(ctx); err != nil || n != 1 {
			t.Fatalf("sweep n=%d err=%v", n, err)
		}
		swept, err := store.Get(ctx, untouched.WorkspaceID, untouched.ID)
		if err != nil {
			t.Fatal(err)
		}
		if swept.Status != StatusExpired || swept.DecisionReason != ReasonExpired {
			t.Fatalf("swept record does not say why: %+v", swept)
		}
		// Expired, not denied: "nobody looked" and "somebody said no" are
		// different facts, and an audit that conflates them cannot tell
		// whether a control was exercised or merely present.
		if swept.Status == StatusDenied {
			t.Fatal("expiry was recorded as a denial")
		}
	})
}

// An invalidation racing a real decision must lose. A decision somebody
// actually made is a better record than the system's guess that it was moot.
func TestAnInvalidationDoesNotOverwriteADecisionAlreadyMade(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	request(t, store, "apr_w", "ws-a", nil)
	if _, err := store.Decide(ctx, "ws-a", "apr_w", true, member("usr_ops", "ws-a", "chat:chat"), "yes"); err != nil {
		t.Fatal(err)
	}
	n, err := store.InvalidateRun(ctx, "ws-a", "run_apr_w", ReasonRunEnded)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("invalidation touched %d already-decided approvals", n)
	}
	stored, err := store.Get(ctx, "ws-a", "apr_w")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusApproved || stored.DecidedBy != "usr_ops" || stored.DecisionReason != "yes" {
		t.Fatalf("a human decision was overwritten: %+v", stored)
	}
}

// A restart severs every channel a blocked run was waiting on, so a record
// still saying pending describes a question nobody is listening for.
func TestARestartClosesEveryPendingApprovalAcrossWorkspaces(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	request(t, store, "apr_a", "ws-a", nil)
	request(t, store, "apr_b", "ws-b", nil)

	n, err := store.InvalidateAllPending(ctx, ReasonWorkspaceRestarting)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// Each row keeps its own workspace; nothing is re-homed by a
	// deployment-wide sweep.
	for _, pair := range [][2]string{{"ws-a", "apr_a"}, {"ws-b", "apr_b"}} {
		stored, err := store.Get(ctx, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if stored.WorkspaceID != pair[0] || stored.Status != StatusInvalidated {
			t.Fatalf("%+v", stored)
		}
	}
}

func assertClosed(t *testing.T, store *Store, workspaceID, id, reason string) {
	t.Helper()
	stored, err := store.Get(context.Background(), workspaceID, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusInvalidated {
		t.Fatalf("status = %s, want invalidated", stored.Status)
	}
	if !strings.Contains(stored.DecisionReason, reason) {
		t.Fatalf("reason = %q, want %q — an approver coming back needs to know why", stored.DecisionReason, reason)
	}
	if _, err := store.Decide(context.Background(), workspaceID, id, true,
		member("usr_ops", workspaceID, "chat:chat"), ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("an invalidated approval was decided: %v", err)
	}
}

// The read-then-check in Decide refuses most second decisions, which is why it
// hides the guard that matters. This exercises the conditional write alone: two
// approvers who both passed the read must not both land, or the second silently
// overwrites the first's answer and their name on it.
func TestTheConditionalWriteIsWhatMakesADecisionSingleUse(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	request(t, store, "apr_toctou", "ws-a", nil)
	now := time.Now().UTC()

	if err := store.recordDecision(ctx, "ws-a", "apr_toctou", StatusApproved, "usr_first", "yes", now); err != nil {
		t.Fatalf("first decision: %v", err)
	}
	// The second approver reached the write having also seen `pending`.
	err := store.recordDecision(ctx, "ws-a", "apr_toctou", StatusDenied, "usr_second", "no", now)
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("the second decision landed: %v", err)
	}
	stored, err := store.Get(ctx, "ws-a", "apr_toctou")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusApproved || stored.DecidedBy != "usr_first" || stored.DecisionReason != "yes" {
		t.Fatalf("the first approver's decision was overwritten: %+v", stored)
	}
}
