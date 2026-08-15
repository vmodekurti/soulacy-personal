// history_workspace_test.go — the cross-tenant isolation contract for
// conversation history.
//
// This is the most revealing store in the system: it is the message content
// itself. Its ownership class is user-private, so it has two boundaries — the
// tenant, and the person inside that tenant.
package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newHistory(t *testing.T) (*SQLiteHistoryStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewSQLiteHistoryStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteHistoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func turn(t *testing.T, s *SQLiteHistoryStore, workspaceID, subject, sessionID, content string) {
	t.Helper()
	if err := s.Append(context.Background(), ConversationEntry{
		WorkspaceID: workspaceID, Subject: subject, SessionID: sessionID,
		AgentID: "assistant", Role: "user", Content: content,
	}); err != nil {
		t.Fatalf("append %s/%s: %v", workspaceID, subject, err)
	}
}

// A turn with no owner is a turn every tenant can search, so a write without a
// workspace is refused rather than defaulted.
func TestAppendWithoutAWorkspaceIsRefused(t *testing.T) {
	store, _ := newHistory(t)
	err := store.Append(context.Background(), ConversationEntry{
		SessionID: "s1", AgentID: "assistant", Role: "user", Content: "orphan",
	})
	if !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("append without a workspace = %v, want ErrWorkspaceRequired", err)
	}
	if _, err := store.Load(context.Background(), "", "s1", 10); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("load without a workspace = %v", err)
	}
	if _, err := store.Search(context.Background(), "", "", "", "anything", 10); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("search without a workspace = %v", err)
	}
}

// The same session ID in two workspaces is two conversations.
func TestLoadCannotReachAnotherWorkspace(t *testing.T) {
	store, _ := newHistory(t)
	turn(t, store, "ws_a", "usr_alice", "shared-session", "alpha confidential")
	turn(t, store, "ws_b", "usr_bob", "shared-session", "beta confidential")

	for _, tc := range []struct{ workspace, want string }{
		{"ws_a", "alpha confidential"},
		{"ws_b", "beta confidential"},
	} {
		entries, err := store.Load(context.Background(), tc.workspace, "shared-session", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Content != tc.want {
			t.Fatalf("%s loaded %+v, want only %q", tc.workspace, entries, tc.want)
		}
	}
}

// Search was the sharpest edge: agent_id is optional, so a blank one meant
// "every agent" — and without the tenant predicate, every tenant.
func TestSearchCannotReachAnotherWorkspaceOrPerson(t *testing.T) {
	store, _ := newHistory(t)
	turn(t, store, "ws_a", "usr_alice", "s-alice", "the acquisition target is Northwind")
	turn(t, store, "ws_a", "usr_bob", "s-bob", "the acquisition target is Contoso")
	turn(t, store, "ws_b", "usr_carol", "s-carol", "the acquisition target is Fabrikam")

	// Blank agent ID: the widest query the API allows.
	hits, err := store.Search(context.Background(), "ws_a", "usr_alice", "", "acquisition", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("alice's widest search returned %d hits, want her own 1: %+v", len(hits), hits)
	}
	if hits[0].Content != "the acquisition target is Northwind" {
		t.Fatalf("alice read %q", hits[0].Content)
	}

	// A workspace peer must not find it, and neither must another tenant.
	for _, tc := range []struct{ workspace, subject string }{
		{"ws_a", "usr_bob"},
		{"ws_b", "usr_carol"},
		{"ws_b", "usr_alice"},
	} {
		got, err := store.Search(context.Background(), tc.workspace, tc.subject, "", "Northwind", 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("%s/%s read another person's conversation: %+v", tc.workspace, tc.subject, got)
		}
	}
}

// LoadForAgent reads across sessions, so it carries the person as well.
func TestLoadForAgentIsScopedToOnePerson(t *testing.T) {
	store, _ := newHistory(t)
	turn(t, store, "ws_a", "usr_alice", "s-alice", "alice's turn")
	turn(t, store, "ws_a", "usr_bob", "s-bob", "bob's turn")

	entries, err := store.LoadForAgent(context.Background(), "ws_a", "usr_alice", "assistant", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "alice's turn" {
		t.Fatalf("alice loaded %+v, want only her own turn", entries)
	}
}

// Product invariant 7, and the migration trap it protects against: rows
// written before tenancy carry an empty subject, while a reader today resolves
// to whatever local owner ID the deployment assigned. Partitioning the
// personal workspace by subject would make a single user's own history vanish
// from search on upgrade.
func TestThePersonalWorkspaceIsNotPartitionedBySubject(t *testing.T) {
	store, path := newHistory(t)
	// A turn as it was written before tenancy: no subject at all.
	turn(t, store, wsroot.PersonalWorkspaceID, "", "s1", "written before tenants")
	// And one written today, by a resolved local owner.
	turn(t, store, wsroot.PersonalWorkspaceID, "usr_local_owner", "s1", "written today")

	hits, err := store.Search(context.Background(), wsroot.PersonalWorkspaceID, "usr_local_owner", "", "written", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("the personal user found %d of their own turns, want both: %+v", len(hits), hits)
	}
	entries, err := store.LoadForAgent(context.Background(), wsroot.PersonalWorkspaceID, "usr_local_owner", "assistant", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("LoadForAgent returned %d, want both: %+v", len(entries), entries)
	}
	_ = path
}

// A history database written before tenants keeps working: the columns are
// added in place and rows are assigned to the personal workspace.
func TestExistingHistoryMigratesToPersonal(t *testing.T) {
	store, path := newHistory(t)
	turn(t, store, wsroot.PersonalWorkspaceID, "", "s1", "written before tenants")
	// Simulate a pre-migration row.
	if _, err := store.db.Exec(`UPDATE conversation_history SET workspace_id = ''`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	entries, err := reopened.Load(context.Background(), wsroot.PersonalWorkspaceID, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "written before tenants" {
		t.Fatalf("a pre-existing conversation was lost by the migration: %+v", entries)
	}
}

// A fork is a branch of the same conversation. It must not reach across the
// tenant boundary, and it must not re-own someone else's turns to the forker.
func TestForkStaysInsideTheWorkspaceAndKeepsTurnOwners(t *testing.T) {
	store, _ := newHistory(t)
	ctx := context.Background()
	turn(t, store, "ws_a", "usr_alice", "main", "first question")
	turn(t, store, "ws_a", "usr_alice", "main", "second question")
	entries, err := store.Load(ctx, "ws_a", "main", 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("seed: %+v %v", entries, err)
	}

	// Another tenant naming the same session must copy nothing.
	copied, err := store.Fork(ctx, "ws_b", "main", "stolen-branch", entries[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if copied != 0 {
		t.Fatalf("a fork copied %d entries out of another workspace", copied)
	}
	if got, err := store.Load(ctx, "ws_b", "stolen-branch", 0); err != nil || len(got) != 0 {
		t.Fatalf("another workspace's fork produced readable entries: %+v %v", got, err)
	}

	// The owner's fork works and keeps each turn's original owner.
	copied, err = store.Fork(ctx, "ws_a", "main", "branch", entries[1].ID)
	if err != nil || copied != 2 {
		t.Fatalf("the owner's fork copied %d: %v", copied, err)
	}
	forked, err := store.Load(ctx, "ws_a", "branch", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range forked {
		if e.WorkspaceID != "ws_a" || e.Subject != "usr_alice" {
			t.Fatalf("a forked turn changed owner: %+v", e)
		}
	}
}
