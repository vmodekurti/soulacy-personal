// resources_workspace_test.go — the isolation and expiry contract for session
// attachments (MU-016).
//
// Attachment IDs are server-generated UUIDs, but an ID alone used to be enough
// to fetch a row. The only thing between one tenant and another's uploads was
// the gateway remembering to call requireSession first — ownership by
// convention rather than by construction.
package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newScopedResourceStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resources.db")
	store, err := NewSQLiteStore(path, DefaultConfig())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func putAttachment(t *testing.T, s *SQLiteStore, workspaceID, id, sessionID string, ttl time.Duration) {
	t.Helper()
	if err := s.PutAttachment(context.Background(), Attachment{
		WorkspaceID: workspaceID, ID: id, SessionID: sessionID, AgentID: "assistant",
		Filename: id + ".txt", MIMEType: "text/plain", Text: "body of " + id,
	}, []byte("body of "+id), ttl); err != nil {
		t.Fatalf("put %s/%s: %v", workspaceID, id, err)
	}
}

// An attachment with no owner is a file every tenant can download.
func TestAttachmentWithoutAWorkspaceIsRefused(t *testing.T) {
	store, _ := newScopedResourceStore(t)
	err := store.PutAttachment(context.Background(), Attachment{
		ID: "a1", SessionID: "s1", AgentID: "assistant", MIMEType: "text/plain",
	}, []byte("orphan"), time.Hour)
	if !errors.Is(err, ErrWorkspaceRequired) {
		t.Fatalf("put without a workspace = %v", err)
	}
	if _, _, err := store.GetAttachment(context.Background(), "", "a1"); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("get without a workspace = %v", err)
	}
	if _, err := store.ListAttachments(context.Background(), "", "assistant", "s1"); !errors.Is(err, ErrWorkspaceRequired) {
		t.Errorf("list without a workspace = %v", err)
	}
}

// Knowing an attachment ID must not be enough. A file another workspace owns
// reads as absent — the same answer an ID that never existed gives.
func TestAnAttachmentIDIsNotEnoughToFetchIt(t *testing.T) {
	store, _ := newScopedResourceStore(t)
	putAttachment(t, store, "ws_a", "confidential", "s1", time.Hour)

	if _, _, err := store.GetAttachment(context.Background(), "ws_b", "confidential"); err == nil {
		t.Fatal("another workspace downloaded an attachment by ID")
	}
	if _, _, err := store.GetAttachment(context.Background(), "ws_b", "never-existed"); err == nil {
		t.Fatal("an unknown ID resolved")
	}
	// The owner still gets it.
	att, data, err := store.GetAttachment(context.Background(), "ws_a", "confidential")
	if err != nil {
		t.Fatalf("the owner lost its own attachment: %v", err)
	}
	if att.WorkspaceID != "ws_a" || string(data) != "body of confidential" {
		t.Fatalf("owner read %+v / %q", att, data)
	}
}

// Listing is scoped even when two tenants use the same agent and session IDs.
func TestAttachmentListingIsScopedToOneWorkspace(t *testing.T) {
	store, _ := newScopedResourceStore(t)
	putAttachment(t, store, "ws_a", "alpha-file", "shared-session", time.Hour)
	putAttachment(t, store, "ws_b", "beta-file", "shared-session", time.Hour)

	for _, tc := range []struct{ workspace, want string }{
		{"ws_a", "alpha-file"},
		{"ws_b", "beta-file"},
	} {
		listed, err := store.ListAttachments(context.Background(), tc.workspace, "assistant", "shared-session")
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].ID != tc.want {
			t.Fatalf("%s listed %+v, want only %s", tc.workspace, listed, tc.want)
		}
	}
}

// MU-016: an expired artifact must stop being downloadable at the moment it
// expires, including through a previously issued link — not whenever the prune
// sweep next happens to run. Expiry is therefore a query predicate, not
// housekeeping.
func TestAnExpiredAttachmentIsImmediatelyUndownloadable(t *testing.T) {
	store, _ := newScopedResourceStore(t)
	// PutAttachment normalizes a non-positive TTL to the default, so an
	// already-expired row is made by backdating CreatedAt: ExpiresAt is
	// CreatedAt+ttl, which lands in the past. The row exists and the sweep has
	// not run.
	if err := store.PutAttachment(context.Background(), Attachment{
		WorkspaceID: "ws_a", ID: "stale", SessionID: "s1", AgentID: "assistant",
		Filename: "stale.txt", MIMEType: "text/plain",
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
	}, []byte("body of stale"), time.Hour); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.GetAttachment(context.Background(), "ws_a", "stale"); err == nil {
		t.Fatal("an expired attachment was still downloadable before the prune sweep")
	}
	listed, err := store.ListAttachments(context.Background(), "ws_a", "assistant", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("an expired attachment was still listed: %+v", listed)
	}
	// A live one alongside it is unaffected.
	putAttachment(t, store, "ws_a", "live", "s1", time.Hour)
	if _, _, err := store.GetAttachment(context.Background(), "ws_a", "live"); err != nil {
		t.Fatalf("a live attachment was refused: %v", err)
	}
}

// A delete must not reach across the boundary.
func TestAttachmentDeleteCannotReachAnotherWorkspace(t *testing.T) {
	store, _ := newScopedResourceStore(t)
	putAttachment(t, store, "ws_a", "owned", "s1", time.Hour)

	if err := store.Delete(context.Background(), "ws_b", "owned"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetAttachment(context.Background(), "ws_a", "owned"); err != nil {
		t.Fatal("another workspace's delete removed the owner's attachment")
	}
	if err := store.Delete(context.Background(), "ws_a", "owned"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetAttachment(context.Background(), "ws_a", "owned"); err == nil {
		t.Fatal("the owner's delete did not take effect")
	}
}

// A resource store written before tenancy keeps working: rows are assigned to
// the personal workspace, which is what a single-user installation's uploads
// were. Getting this wrong would not leak — the files would silently become
// undownloadable while still occupying disk until their TTL expired.
func TestExistingAttachmentsMigrateToPersonal(t *testing.T) {
	store, path := newScopedResourceStore(t)
	putAttachment(t, store, wsroot.PersonalWorkspaceID, "legacy", "s1", time.Hour)
	if _, err := store.db.Exec(`UPDATE session_resources SET workspace_id = ''`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	if _, _, err := reopened.GetAttachment(context.Background(), wsroot.PersonalWorkspaceID, "legacy"); err != nil {
		t.Fatalf("a pre-existing attachment became undownloadable: %v", err)
	}
}
