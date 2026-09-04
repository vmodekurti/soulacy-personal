package session

import (
	"context"
	"path/filepath"
	"testing"
)

func newForkTestStore(t *testing.T) *SQLiteHistoryStore {
	t.Helper()
	s, err := NewSQLiteHistoryStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("NewSQLiteHistoryStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedConversation(t *testing.T, s *SQLiteHistoryStore, sessionID string) []ConversationEntry {
	t.Helper()
	ctx := context.Background()
	turns := []ConversationEntry{
		{SessionID: sessionID, AgentID: "bot", Role: "user", Content: "first question"},
		{SessionID: sessionID, AgentID: "bot", Role: "assistant", Content: "first answer"},
		{SessionID: sessionID, AgentID: "bot", Role: "user", Content: "second question"},
		{SessionID: sessionID, AgentID: "bot", Role: "assistant", Content: "second answer"},
	}
	for _, e := range turns {
		if err := s.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	entries, err := s.Load(ctx, sessionID, 0)
	if err != nil || len(entries) != 4 {
		t.Fatalf("Load = %d entries, err=%v; want 4", len(entries), err)
	}
	return entries
}

func TestFork_CopiesUpToCheckpoint(t *testing.T) {
	s := newForkTestStore(t)
	ctx := context.Background()
	entries := seedConversation(t, s, "main")

	// Fork after the first exchange (entry index 1 = "first answer").
	copied, err := s.Fork(ctx, "main", "branch-1", entries[1].ID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if copied != 2 {
		t.Errorf("copied = %d, want 2", copied)
	}

	forked, err := s.Load(ctx, "branch-1", 0)
	if err != nil {
		t.Fatalf("Load fork: %v", err)
	}
	if len(forked) != 2 {
		t.Fatalf("fork has %d entries, want 2", len(forked))
	}
	if forked[0].Role != "user" || forked[0].Content != "first question" {
		t.Errorf("forked[0] = %+v", forked[0])
	}
	if forked[1].Role != "assistant" || forked[1].Content != "first answer" {
		t.Errorf("forked[1] = %+v", forked[1])
	}
	if forked[0].SessionID != "branch-1" || forked[0].AgentID != "bot" {
		t.Errorf("fork identity wrong: %+v", forked[0])
	}

	// Source must be untouched.
	src, _ := s.Load(ctx, "main", 0)
	if len(src) != 4 {
		t.Errorf("source mutated: %d entries, want 4", len(src))
	}
}

func TestFork_BranchesStayIsolated(t *testing.T) {
	s := newForkTestStore(t)
	ctx := context.Background()
	entries := seedConversation(t, s, "main")

	if _, err := s.Fork(ctx, "main", "branch-1", entries[1].ID); err != nil {
		t.Fatalf("Fork: %v", err)
	}

	// New turns on the branch must not appear in main, and vice versa.
	_ = s.Append(ctx, ConversationEntry{SessionID: "branch-1", AgentID: "bot", Role: "user", Content: "branch question"})
	_ = s.Append(ctx, ConversationEntry{SessionID: "main", AgentID: "bot", Role: "user", Content: "main question"})

	branch, _ := s.Load(ctx, "branch-1", 0)
	main, _ := s.Load(ctx, "main", 0)
	for _, e := range branch {
		if e.Content == "main question" {
			t.Error("main entry leaked into branch")
		}
	}
	for _, e := range main {
		if e.Content == "branch question" {
			t.Error("branch entry leaked into main")
		}
	}
	if len(branch) != 3 || len(main) != 5 {
		t.Errorf("branch=%d main=%d, want 3/5", len(branch), len(main))
	}
}

func TestFork_CheckpointBeyondEndCopiesAll(t *testing.T) {
	s := newForkTestStore(t)
	ctx := context.Background()
	entries := seedConversation(t, s, "main")

	copied, err := s.Fork(ctx, "main", "branch-all", entries[3].ID+1000)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if copied != 4 {
		t.Errorf("copied = %d, want 4", copied)
	}
}

func TestFork_UnknownSourceCopiesNothing(t *testing.T) {
	s := newForkTestStore(t)
	copied, err := s.Fork(context.Background(), "never-existed", "branch-x", 999)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if copied != 0 {
		t.Errorf("copied = %d, want 0", copied)
	}
}

func TestFork_RefusesNonEmptyTarget(t *testing.T) {
	s := newForkTestStore(t)
	ctx := context.Background()
	entries := seedConversation(t, s, "main")
	_ = s.Append(ctx, ConversationEntry{SessionID: "busy", AgentID: "bot", Role: "user", Content: "existing"})

	if _, err := s.Fork(ctx, "main", "busy", entries[3].ID); err == nil {
		t.Fatal("Fork into non-empty target should error (branch mixing)")
	}
}

func TestFork_RefusesSameSession(t *testing.T) {
	s := newForkTestStore(t)
	entries := seedConversation(t, s, "main")
	if _, err := s.Fork(context.Background(), "main", "main", entries[0].ID); err == nil {
		t.Fatal("Fork onto itself should error")
	}
}
