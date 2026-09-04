package workboard

// Story 14: collaboration primitives — owner, priority, tags, due date on
// tasks; comments and reviewer notes per task.

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func collabStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "wb.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// Task fields: owner, priority, tags, due date
// ---------------------------------------------------------------------------

func TestCreate_CollabFields(t *testing.T) {
	s := collabStore(t)
	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	created, err := s.Create(context.Background(), Task{
		Title:    "review report",
		Owner:    "vasu",
		Priority: PriorityHigh,
		Tags:     []string{"q4", "finance"},
		DueAt:    &due,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "vasu" || got.Priority != PriorityHigh {
		t.Fatalf("task = %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "q4" || got.Tags[1] != "finance" {
		t.Fatalf("tags = %v", got.Tags)
	}
	if got.DueAt == nil || !got.DueAt.Equal(due) {
		t.Fatalf("due = %v, want %v", got.DueAt, due)
	}
}

func TestCreate_DefaultPriorityNormal(t *testing.T) {
	s := collabStore(t)
	created, err := s.Create(context.Background(), Task{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Priority != PriorityNormal {
		t.Fatalf("priority = %q, want normal", created.Priority)
	}
}

func TestCreate_InvalidPriorityRejected(t *testing.T) {
	s := collabStore(t)
	if _, err := s.Create(context.Background(), Task{Title: "t", Priority: "ludicrous"}); err == nil {
		t.Fatal("invalid priority accepted")
	}
}

func TestUpdate_CollabFields(t *testing.T) {
	s := collabStore(t)
	created, _ := s.Create(context.Background(), Task{Title: "t"})
	owner := "reviewer-1"
	prio := PriorityUrgent
	tags := []string{"ops"}
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	got, err := s.Update(context.Background(), created.ID, Update{
		Owner: &owner, Priority: &prio, Tags: &tags, DueAt: &due,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Owner != owner || got.Priority != prio || len(got.Tags) != 1 || got.DueAt == nil {
		t.Fatalf("task = %+v", got)
	}
}

func TestUpdate_ClearDueDate(t *testing.T) {
	s := collabStore(t)
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	created, _ := s.Create(context.Background(), Task{Title: "t", DueAt: &due})
	got, err := s.Update(context.Background(), created.ID, Update{ClearDueAt: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.DueAt != nil {
		t.Fatalf("DueAt = %v, want cleared", got.DueAt)
	}
}

func TestUpdate_InvalidPriorityRejected(t *testing.T) {
	s := collabStore(t)
	created, _ := s.Create(context.Background(), Task{Title: "t"})
	bad := "asap"
	if _, err := s.Update(context.Background(), created.ID, Update{Priority: &bad}); err == nil {
		t.Fatal("invalid priority accepted on update")
	}
}

func TestTags_NormalisedOnSave(t *testing.T) {
	s := collabStore(t)
	created, err := s.Create(context.Background(), Task{
		Title: "t", Tags: []string{"  Q4 ", "", "finance", "q4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// trimmed, lowercased, empties dropped, deduped, order preserved
	if len(created.Tags) != 2 || created.Tags[0] != "q4" || created.Tags[1] != "finance" {
		t.Fatalf("tags = %v", created.Tags)
	}
}

// ---------------------------------------------------------------------------
// Comments & reviewer notes
// ---------------------------------------------------------------------------

func TestComments_AddListDelete(t *testing.T) {
	s := collabStore(t)
	task, _ := s.Create(context.Background(), Task{Title: "t"})

	c1, err := s.AddComment(context.Background(), task.ID, Comment{
		Author: "vasu", Body: "looks good", Kind: CommentKindComment,
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	c2, err := s.AddComment(context.Background(), task.ID, Comment{
		Author: "reviewer", Body: "needs a chart", Kind: CommentKindReview,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ListComments(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("comments = %+v", got)
	}
	// Oldest first (conversation order).
	if got[0].ID != c1.ID || got[1].ID != c2.ID {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[1].Kind != CommentKindReview {
		t.Fatalf("kind = %q", got[1].Kind)
	}

	if err := s.DeleteComment(context.Background(), c1.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListComments(context.Background(), task.ID)
	if len(got) != 1 {
		t.Fatalf("after delete = %+v", got)
	}
	if err := s.DeleteComment(context.Background(), 99999); err != ErrNotFound {
		t.Fatalf("delete missing = %v, want ErrNotFound", err)
	}
}

func TestComments_Validation(t *testing.T) {
	s := collabStore(t)
	task, _ := s.Create(context.Background(), Task{Title: "t"})
	if _, err := s.AddComment(context.Background(), task.ID, Comment{Body: "   "}); err == nil {
		t.Fatal("blank comment accepted")
	}
	if _, err := s.AddComment(context.Background(), task.ID, Comment{Body: "x", Kind: "shout"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if _, err := s.AddComment(context.Background(), 99999, Comment{Body: "x"}); err == nil {
		t.Fatal("comment on missing task accepted")
	}
}

func TestComments_DefaultsKindComment(t *testing.T) {
	s := collabStore(t)
	task, _ := s.Create(context.Background(), Task{Title: "t"})
	c, err := s.AddComment(context.Background(), task.ID, Comment{Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != CommentKindComment || c.Author != "user" {
		t.Fatalf("comment = %+v", c)
	}
}

func TestDeleteTask_CascadesComments(t *testing.T) {
	s := collabStore(t)
	task, _ := s.Create(context.Background(), Task{Title: "t"})
	_, _ = s.AddComment(context.Background(), task.ID, Comment{Body: "x"})
	if err := s.Delete(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListComments(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("comments survived delete: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Migration: pre-Story-14 databases gain the new columns
// ---------------------------------------------------------------------------

func TestMigration_OldDatabaseStillOpens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wb.db")
	// First open creates the current schema; close and reopen to exercise
	// the idempotent migration path (duplicate ALTERs must be tolerated).
	s1, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Create(context.Background(), Task{Title: "pre"}); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	tasks, err := s2.List(context.Background(), Filter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %+v err=%v", tasks, err)
	}
	if tasks[0].Priority != PriorityNormal {
		t.Fatalf("migrated priority = %q", tasks[0].Priority)
	}
}
