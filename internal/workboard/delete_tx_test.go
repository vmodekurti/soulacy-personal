package workboard

import (
	"context"
	"github.com/soulacy/soulacy/internal/wsroot"
	"path/filepath"
	"testing"
)

// Delete removed the PARENT row first and then ran three independent statements
// for runs, artifacts and comments. An error partway through — or a crash — left
// child rows pointing at a task id that no longer existed: rows nothing lists and
// nothing cleans up. Every other multi-statement write in this package is
// already transactional.
//
// The failure is forced by dropping the last table, which is the cheapest way to
// make the third statement fail after the first has already succeeded.
func TestDelete_RollsBackWhenALaterStatementFails(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "wb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	ctx := context.Background()

	task, err := s.Create(ctx, Task{WorkspaceID: wsroot.PersonalWorkspaceID, Title: "keep me", Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.db.ExecContext(ctx, `DROP TABLE workboard_comments`); err != nil {
		t.Fatalf("precondition: could not drop the table to force a failure: %v", err)
	}

	if err := s.Delete(ctx, wsroot.PersonalWorkspaceID, task.ID); err == nil {
		t.Fatal("Delete reported success even though one of its statements failed")
	}

	// The task must still be there: either the whole delete happened or none of
	// it did.
	if _, err := s.Get(ctx, wsroot.PersonalWorkspaceID, task.ID); err != nil {
		t.Fatalf("the task row was deleted even though the delete failed partway — its children are now orphans: %v", err)
	}
}

func TestDelete_StillRemovesEverythingOnTheHappyPath(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "wb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	ctx := context.Background()

	task, err := s.Create(ctx, Task{WorkspaceID: wsroot.PersonalWorkspaceID, Title: "delete me", Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, wsroot.PersonalWorkspaceID, task.ID); err != nil {
		t.Fatalf("an ordinary delete failed: %v", err)
	}
	if _, err := s.Get(ctx, wsroot.PersonalWorkspaceID, task.ID); err == nil {
		t.Fatal("the task survived its own delete")
	}
	if err := s.Delete(ctx, wsroot.PersonalWorkspaceID, task.ID); err != ErrNotFound {
		t.Fatalf("deleting a missing task returned %v, want ErrNotFound", err)
	}
}
