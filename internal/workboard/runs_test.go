package workboard

import (
	"context"
	"errors"
	"testing"
)

func mustTask(t *testing.T, s *Store, title, agentID string) Task {
	t.Helper()
	task, err := s.Create(context.Background(), Task{Title: title, AgentID: agentID})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	return task
}

func TestStartRun_FirstAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := mustTask(t, s, "run me", "bot-1")

	run, err := s.StartRun(ctx, task.ID, "bot-1", "wb-1-123", "/logs/bot-1.log")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run.ID == 0 {
		t.Error("run should get a non-zero ID")
	}
	if run.TaskID != task.ID {
		t.Errorf("TaskID = %d, want %d", run.TaskID, task.ID)
	}
	if run.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", run.Attempt)
	}
	if run.Status != RunStatusRunning {
		t.Errorf("Status = %q, want %q", run.Status, RunStatusRunning)
	}
	if run.SessionID != "wb-1-123" || run.AgentID != "bot-1" || run.ActionLogPath != "/logs/bot-1.log" {
		t.Errorf("run fields = %+v", run)
	}
	if run.StartedAt.IsZero() {
		t.Error("StartedAt should be set")
	}
	if run.EndedAt != nil {
		t.Error("EndedAt should be nil while running")
	}
}

func TestStartRun_MissingTask(t *testing.T) {
	s := newTestStore(t)
	_, err := s.StartRun(context.Background(), 9999, "bot-1", "sess", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("StartRun(missing task) err = %v, want ErrNotFound", err)
	}
}

func TestStartRun_DuplicateActiveRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := mustTask(t, s, "busy", "bot-1")

	if _, err := s.StartRun(ctx, task.ID, "bot-1", "sess-1", ""); err != nil {
		t.Fatalf("first StartRun: %v", err)
	}
	_, err := s.StartRun(ctx, task.ID, "bot-1", "sess-2", "")
	if !errors.Is(err, ErrRunActive) {
		t.Fatalf("second StartRun err = %v, want ErrRunActive", err)
	}
}

func TestFinishRun_Done(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := mustTask(t, s, "x", "bot-1")
	run, _ := s.StartRun(ctx, task.ID, "bot-1", "sess", "")

	finished, err := s.FinishRun(ctx, run.ID, RunStatusDone, "it worked", "")
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if finished.Status != RunStatusDone || finished.Result != "it worked" {
		t.Errorf("finished = %+v", finished)
	}
	if finished.EndedAt == nil || finished.EndedAt.IsZero() {
		t.Error("EndedAt should be set after finish")
	}
}

func TestFinishRun_Failed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := mustTask(t, s, "x", "bot-1")
	run, _ := s.StartRun(ctx, task.ID, "bot-1", "sess", "")

	finished, err := s.FinishRun(ctx, run.ID, RunStatusFailed, "", "agent exploded")
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if finished.Status != RunStatusFailed || finished.FailureReason != "agent exploded" {
		t.Errorf("finished = %+v", finished)
	}
}

func TestFinishRun_Errors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := mustTask(t, s, "x", "bot-1")
	run, _ := s.StartRun(ctx, task.ID, "bot-1", "sess", "")

	if _, err := s.FinishRun(ctx, run.ID, "bogus", "", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("FinishRun(bogus status) err = %v, want ErrInvalid", err)
	}
	if _, err := s.FinishRun(ctx, run.ID, RunStatusRunning, "", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("FinishRun(running) err = %v, want ErrInvalid (must be terminal)", err)
	}
	if _, err := s.FinishRun(ctx, 9999, RunStatusDone, "", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("FinishRun(missing) err = %v, want ErrNotFound", err)
	}

	// Double-finish is rejected.
	if _, err := s.FinishRun(ctx, run.ID, RunStatusDone, "ok", ""); err != nil {
		t.Fatalf("first finish: %v", err)
	}
	if _, err := s.FinishRun(ctx, run.ID, RunStatusDone, "again", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("double FinishRun err = %v, want ErrInvalid", err)
	}
}

func TestRetry_PreservesPriorAttempts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := mustTask(t, s, "retry me", "bot-1")

	r1, _ := s.StartRun(ctx, task.ID, "bot-1", "sess-1", "")
	if _, err := s.FinishRun(ctx, r1.ID, RunStatusFailed, "", "boom"); err != nil {
		t.Fatalf("finish r1: %v", err)
	}

	r2, err := s.StartRun(ctx, task.ID, "bot-1", "sess-2", "")
	if err != nil {
		t.Fatalf("retry StartRun: %v", err)
	}
	if r2.Attempt != 2 {
		t.Errorf("retry Attempt = %d, want 2", r2.Attempt)
	}

	runs, err := s.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("ListRuns = %d runs, want 2", len(runs))
	}
	// Newest first; prior attempt intact.
	if runs[0].Attempt != 2 || runs[1].Attempt != 1 {
		t.Errorf("order = attempts [%d, %d], want [2, 1]", runs[0].Attempt, runs[1].Attempt)
	}
	if runs[1].FailureReason != "boom" || runs[1].SessionID != "sess-1" {
		t.Errorf("prior attempt mutated: %+v", runs[1])
	}
}

func TestListRuns_EmptyReturnsEmptySlice(t *testing.T) {
	s := newTestStore(t)
	task := mustTask(t, s, "no runs", "")
	runs, err := s.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs == nil {
		t.Fatal("ListRuns should return empty slice, not nil")
	}
	if len(runs) != 0 {
		t.Fatalf("ListRuns = %d, want 0", len(runs))
	}
}

func TestDeleteTask_CascadesRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := mustTask(t, s, "doomed", "bot-1")
	run, _ := s.StartRun(ctx, task.ID, "bot-1", "sess", "")
	if _, err := s.FinishRun(ctx, run.ID, RunStatusDone, "ok", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if err := s.Delete(ctx, task.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	runs, err := s.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after delete: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs not cascaded: %d remain", len(runs))
	}
}
