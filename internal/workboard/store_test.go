package workboard

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "workboard.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func strPtr(s string) *string { return &s }

func TestValidStatus(t *testing.T) {
	for _, st := range []string{StatusTodo, StatusRunning, StatusNeedsReview, StatusDone, StatusFailed} {
		if !ValidStatus(st) {
			t.Errorf("ValidStatus(%q) = false, want true", st)
		}
	}
	for _, st := range []string{"", "pending", "TODO", "in_progress"} {
		if ValidStatus(st) {
			t.Errorf("ValidStatus(%q) = true, want false", st)
		}
	}
}

func TestCreate_DefaultsToTodo(t *testing.T) {
	s := newTestStore(t)
	task, err := s.Create(context.Background(), Task{Title: "write tests"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.ID == 0 {
		t.Error("Create should assign a non-zero ID")
	}
	if task.Status != StatusTodo {
		t.Errorf("default status = %q, want %q", task.Status, StatusTodo)
	}
	if task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
		t.Error("Create should set CreatedAt and UpdatedAt")
	}
}

func TestCreate_EmptyTitleRejected(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Task{Title: "  "})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create(empty title) err = %v, want ErrInvalid", err)
	}
}

func TestCreate_InvalidStatusRejected(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Task{Title: "x", Status: "bogus"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create(bogus status) err = %v, want ErrInvalid", err)
	}
}

func TestCreate_ExplicitStatusKept(t *testing.T) {
	s := newTestStore(t)
	task, err := s.Create(context.Background(), Task{Title: "x", Status: StatusRunning})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.Status != StatusRunning {
		t.Errorf("status = %q, want %q", task.Status, StatusRunning)
	}
}

func TestGet_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(context.Background(), Task{
		Title:       "review PR",
		Description: "look at the diff",
		AgentID:     "agent-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "review PR" || got.Description != "look at the diff" || got.AgentID != "agent-1" {
		t.Errorf("Get returned %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("Get should return a parsed CreatedAt")
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), 9999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
}

func TestList_EmptyReturnsEmptySlice(t *testing.T) {
	s := newTestStore(t)
	tasks, err := s.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if tasks == nil {
		t.Fatal("List should return an empty slice, not nil")
	}
	if len(tasks) != 0 {
		t.Fatalf("List = %d tasks, want 0", len(tasks))
	}
}

func TestList_FilterByStatusAndAgent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mustCreate := func(title, agent, status string) {
		t.Helper()
		if _, err := s.Create(ctx, Task{Title: title, AgentID: agent, Status: status}); err != nil {
			t.Fatalf("Create(%s): %v", title, err)
		}
	}
	mustCreate("a", "bot-1", StatusTodo)
	mustCreate("b", "bot-1", StatusDone)
	mustCreate("c", "bot-2", StatusTodo)

	all, err := s.List(ctx, Filter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("List(all) = %d tasks, err=%v; want 3", len(all), err)
	}
	todos, err := s.List(ctx, Filter{Status: StatusTodo})
	if err != nil || len(todos) != 2 {
		t.Fatalf("List(todo) = %d tasks, err=%v; want 2", len(todos), err)
	}
	bot1Todo, err := s.List(ctx, Filter{Status: StatusTodo, AgentID: "bot-1"})
	if err != nil || len(bot1Todo) != 1 || bot1Todo[0].Title != "a" {
		t.Fatalf("List(todo,bot-1) = %+v, err=%v; want [a]", bot1Todo, err)
	}
}

func TestList_InvalidStatusFilterRejected(t *testing.T) {
	s := newTestStore(t)
	_, err := s.List(context.Background(), Filter{Status: "bogus"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("List(bogus) err = %v, want ErrInvalid", err)
	}
}

func TestUpdate_Status(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created, _ := s.Create(ctx, Task{Title: "x"})

	updated, err := s.Update(ctx, created.ID, Update{Status: strPtr(StatusRunning)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != StatusRunning {
		t.Errorf("status = %q, want %q", updated.Status, StatusRunning)
	}
	if updated.Title != "x" {
		t.Errorf("title clobbered: %q", updated.Title)
	}
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Error("UpdatedAt should not go backwards")
	}
}

func TestUpdate_TitleAndDescription(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created, _ := s.Create(ctx, Task{Title: "old", Description: "d1"})

	updated, err := s.Update(ctx, created.ID, Update{
		Title:       strPtr("new"),
		Description: strPtr("d2"),
		AgentID:     strPtr("bot-9"),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "new" || updated.Description != "d2" || updated.AgentID != "bot-9" {
		t.Errorf("Update returned %+v", updated)
	}
	if updated.Status != StatusTodo {
		t.Errorf("status changed unexpectedly: %q", updated.Status)
	}
}

func TestUpdate_InvalidStatusRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created, _ := s.Create(ctx, Task{Title: "x"})
	_, err := s.Update(ctx, created.ID, Update{Status: strPtr("bogus")})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Update(bogus) err = %v, want ErrInvalid", err)
	}
}

func TestUpdate_EmptyTitleRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created, _ := s.Create(ctx, Task{Title: "x"})
	_, err := s.Update(ctx, created.ID, Update{Title: strPtr("")})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Update(empty title) err = %v, want ErrInvalid", err)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(context.Background(), 9999, Update{Status: strPtr(StatusDone)})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(missing) err = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created, _ := s.Create(ctx, Task{Title: "x"})

	if err := s.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) err = %v, want ErrNotFound", err)
	}
}
