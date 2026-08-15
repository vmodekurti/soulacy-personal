package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func newOwnershipStore(t *testing.T) (*SQLiteOwnershipStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := NewSQLiteOwnershipStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteOwnershipStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func claim(t *testing.T, store *SQLiteOwnershipStore, sessionID, workspaceID, agentID, creator string) Ownership {
	t.Helper()
	owner, err := store.Claim(context.Background(), Ownership{
		SessionID: sessionID, WorkspaceID: workspaceID, AgentID: agentID, Creator: creator,
	})
	if err != nil {
		t.Fatalf("claim %s: %v", sessionID, err)
	}
	return owner
}

// The first caller to present a session ID owns it. A second principal
// presenting the same ID is refused rather than silently joined to the
// conversation.
func TestASessionIDCannotBeClaimedTwice(t *testing.T) {
	store, _ := newOwnershipStore(t)
	ctx := context.Background()
	claim(t, store, "sess_1", "ws_a", "bot", "usr_alice")

	for _, tc := range []struct {
		name  string
		want  Ownership
		claim bool
	}{
		{"another principal", Ownership{SessionID: "sess_1", WorkspaceID: "ws_a", AgentID: "bot", Creator: "usr_bob"}, true},
		{"another workspace", Ownership{SessionID: "sess_1", WorkspaceID: "ws_b", AgentID: "bot", Creator: "usr_alice"}, true},
		{"another agent", Ownership{SessionID: "sess_1", WorkspaceID: "ws_a", AgentID: "other-bot", Creator: "usr_alice"}, true},
	} {
		_, err := store.Claim(ctx, tc.want)
		if !errors.Is(err, ErrSessionClaimed) {
			t.Errorf("%s: err = %v, want ErrSessionClaimed", tc.name, err)
		}
	}

	// The rightful owner re-presenting the same session is not an error: every
	// message in a conversation re-claims it.
	if _, err := store.Claim(ctx, Ownership{SessionID: "sess_1", WorkspaceID: "ws_a", AgentID: "bot", Creator: "usr_alice"}); err != nil {
		t.Fatalf("the owner was refused its own session: %v", err)
	}
}

// Two concurrent first-uses of the same ID must not both succeed: "look it up,
// then insert if absent" is a race both callers win, and the loser's messages
// would land in the winner's conversation.
// The loser's error matters as much as the winner's success. A loser that gets
// "database is locked" instead of ErrSessionClaimed is reported to the caller
// as a server fault rather than as a taken ID — and a *co-owner* whose
// idempotent re-claim hits the same lock is refused its own session. That is
// what a deferred transaction produces here: every racer takes a read lock at
// the SELECT and then tries to upgrade at the INSERT, and SQLite fails the
// upgraders immediately rather than letting them wait. See
// sqlitex.Options.ImmediateTx.
//
// Run over several rounds because a single round can pass by scheduling luck.
func TestConcurrentClaimsElectExactlyOneOwner(t *testing.T) {
	store, _ := newOwnershipStore(t)
	const racers = 8
	for round := 0; round < 12; round++ {
		sessionID := fmt.Sprintf("sess_race_%d", round)
		var wg sync.WaitGroup
		results := make([]error, racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				_, results[index] = store.Claim(context.Background(), Ownership{
					SessionID: sessionID, WorkspaceID: "ws_a", AgentID: "bot",
					Creator: []string{"usr_a", "usr_b"}[index%2],
				})
			}(i)
		}
		close(start)
		wg.Wait()

		owner, err := store.Lookup(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		for index, result := range results {
			creator := []string{"usr_a", "usr_b"}[index%2]
			if creator == owner.Creator && result != nil {
				t.Fatalf("round %d: racer %d (the winner's principal) was refused: %v", round, index, result)
			}
			if creator != owner.Creator && !errors.Is(result, ErrSessionClaimed) {
				t.Fatalf("round %d: racer %d (a loser) got %v, want ErrSessionClaimed", round, index, result)
			}
		}
	}
}

// Authorization must survive a restart. This is the whole reason ownership is
// not an in-process map: a restart used to forget who owned every open
// conversation.
func TestOwnershipSurvivesARestart(t *testing.T) {
	store, path := newOwnershipStore(t)
	claim(t, store, "sess_1", "ws_a", "bot", "usr_alice")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteOwnershipStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	owner, err := reopened.Lookup(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("ownership was lost across a restart: %v", err)
	}
	if !owner.Readable("ws_a", "usr_alice", false) {
		t.Fatal("the creator cannot read their own session after a restart")
	}
	if owner.Readable("ws_a", "usr_bob", false) {
		t.Fatal("another principal could read the session after a restart")
	}
	// And a second replica opening the same database reaches the same verdict.
	second, err := NewSQLiteOwnershipStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.Claim(context.Background(), Ownership{
		SessionID: "sess_1", WorkspaceID: "ws_a", AgentID: "bot", Creator: "usr_bob",
	}); !errors.Is(err, ErrSessionClaimed) {
		t.Fatalf("a second replica allowed a claim the first refused: %v", err)
	}
}

// Visibility widens access inside a workspace and never across one.
func TestVisibilityWidensInsideAWorkspaceOnly(t *testing.T) {
	store, _ := newOwnershipStore(t)
	ctx := context.Background()
	claim(t, store, "sess_1", "ws_a", "bot", "usr_alice")

	owner, err := store.Lookup(ctx, "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if owner.Visibility != VisibilityPrivate {
		t.Fatalf("default visibility = %q, want private", owner.Visibility)
	}
	if owner.Readable("ws_a", "usr_bob", false) {
		t.Fatal("a private session was readable by a workspace peer")
	}

	shared, err := store.SetVisibility(ctx, "sess_1", "ws_a", "usr_alice", VisibilityWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if !shared.Readable("ws_a", "usr_bob", false) {
		t.Fatal("a workspace-visible session was not readable by a workspace peer")
	}
	// The critical case: workspace visibility must not cross the tenant line.
	if shared.Readable("ws_b", "usr_bob", false) {
		t.Fatal("a workspace-visible session was readable from another workspace")
	}
	if shared.Readable("ws_b", "usr_alice", true) {
		t.Fatal("an admin in another workspace could read the session")
	}
}

// An admin may read a private conversation for support, but publishing someone
// else's conversation to the whole workspace is a different and larger power.
func TestOnlyTheCreatorMayChangeVisibility(t *testing.T) {
	store, _ := newOwnershipStore(t)
	ctx := context.Background()
	claim(t, store, "sess_1", "ws_a", "bot", "usr_alice")

	if _, err := store.SetVisibility(ctx, "sess_1", "ws_a", "usr_bob", VisibilityWorkspace); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a non-creator changed visibility: %v", err)
	}
	if _, err := store.SetVisibility(ctx, "sess_1", "ws_b", "usr_alice", VisibilityWorkspace); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("visibility was changed from another workspace: %v", err)
	}
	owner, err := store.Lookup(ctx, "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if owner.Visibility != VisibilityPrivate {
		t.Fatalf("visibility changed to %q despite the refusals", owner.Visibility)
	}
}

// A listing carries the workspace predicate in the query itself, and shows a
// principal only what they may read.
func TestListingIsScopedAndRespectsVisibility(t *testing.T) {
	store, _ := newOwnershipStore(t)
	ctx := context.Background()
	claim(t, store, "sess_alice_private", "ws_a", "bot", "usr_alice")
	claim(t, store, "sess_alice_shared", "ws_a", "bot", "usr_alice")
	claim(t, store, "sess_bob", "ws_a", "bot", "usr_bob")
	claim(t, store, "sess_other_tenant", "ws_b", "bot", "usr_alice")
	if _, err := store.SetVisibility(ctx, "sess_alice_shared", "ws_a", "usr_alice", VisibilityWorkspace); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListForPrincipal(ctx, "ws_a", "usr_bob", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, owner := range listed {
		seen[owner.SessionID] = true
	}
	if !seen["sess_bob"] || !seen["sess_alice_shared"] {
		t.Fatalf("bob cannot see his own or the shared session: %v", seen)
	}
	if seen["sess_alice_private"] {
		t.Fatal("a peer's private session appeared in the listing")
	}
	if seen["sess_other_tenant"] {
		t.Fatal("another workspace's session appeared in the listing")
	}

	// An admin sees the workspace, and still only that workspace.
	adminListed, err := store.ListForPrincipal(ctx, "ws_a", "usr_admin", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range adminListed {
		if owner.WorkspaceID != "ws_a" {
			t.Fatalf("an admin listing crossed the workspace boundary: %+v", owner)
		}
	}
	if len(adminListed) != 3 {
		t.Fatalf("admin listing returned %d sessions, want the workspace's 3", len(adminListed))
	}
}

// A missing session and a session the caller may not see are indistinguishable,
// so session IDs cannot be probed.
func TestUnknownAndUnauthorizedAreIndistinguishable(t *testing.T) {
	store, _ := newOwnershipStore(t)
	if _, err := store.Lookup(context.Background(), "sess_does_not_exist"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown session err = %v", err)
	}
	claim(t, store, "sess_1", "ws_a", "bot", "usr_alice")
	owner, err := store.Lookup(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	// Lookup is the storage read; the policy answer is Readable, and for a
	// foreign caller it is the same "no" as a session that does not exist.
	if owner.Readable("ws_b", "usr_bob", false) || owner.Readable("ws_a", "usr_bob", false) {
		t.Fatal("an unauthorized caller was granted read access")
	}
}

func TestClaimRequiresCompleteIdentity(t *testing.T) {
	store, _ := newOwnershipStore(t)
	for _, want := range []Ownership{
		{WorkspaceID: "ws_a", Creator: "usr_a"},
		{SessionID: "sess_1", Creator: "usr_a"},
		{SessionID: "sess_1", WorkspaceID: "ws_a"},
		{SessionID: "  ", WorkspaceID: "ws_a", Creator: "usr_a"},
	} {
		if _, err := store.Claim(context.Background(), want); err == nil {
			t.Errorf("an incomplete ownership claim was accepted: %+v", want)
		}
	}
}

func TestVisibilityIsNormalizedAndConstrained(t *testing.T) {
	store, _ := newOwnershipStore(t)
	ctx := context.Background()
	owner, err := store.Claim(ctx, Ownership{
		SessionID: "sess_1", WorkspaceID: "ws_a", Creator: "usr_a", Visibility: "PUBLIC",
	})
	if err != nil {
		t.Fatal(err)
	}
	// An unrecognized visibility must fall back to the closed value, never the
	// open one.
	if owner.Visibility != VisibilityPrivate {
		t.Fatalf("unknown visibility resolved to %q, want private", owner.Visibility)
	}
	updated, err := store.SetVisibility(ctx, "sess_1", "ws_a", "usr_a", "  WORKSPACE  ")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Visibility != VisibilityWorkspace {
		t.Fatalf("visibility = %q", updated.Visibility)
	}
}
