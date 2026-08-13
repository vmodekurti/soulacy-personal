package learning

import (
	"path/filepath"
	"testing"
)

func TestFeedbackUpsertDoesNotDoubleCount(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "learning.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AddFeedback(Feedback{AgentID: "a", SessionID: "s", RunID: "r", ResponseID: "m", UserID: "u", Rating: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddFeedback(Feedback{AgentID: "a", SessionID: "s", RunID: "r", ResponseID: "m", UserID: "u", Rating: -1, Comment: "incorrect"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Rating != -1 {
		t.Fatalf("upsert = %+v", second)
	}
	all, err := store.ListFeedback("a", 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("feedback=%+v err=%v", all, err)
	}
}

func TestFeedbackValidatesRating(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "learning.jsonl"))
	if _, err := store.AddFeedback(Feedback{AgentID: "a", SessionID: "s", RunID: "r", Rating: 0}); err == nil {
		t.Fatal("expected invalid rating error")
	}
}
