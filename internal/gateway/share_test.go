package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestShareTokenRegexRejectsTraversal(t *testing.T) {
	valid := uuid.New().String()
	if !shareTokenRe.MatchString(valid) {
		t.Fatalf("a real uuid should be accepted: %s", valid)
	}
	for _, bad := range []string{
		"", "..", "../../etc/passwd", "abc", valid + "/x",
		"../" + valid, valid + ".json", "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz",
	} {
		if shareTokenRe.MatchString(bad) {
			t.Errorf("token %q should be rejected", bad)
		}
	}
}

func TestSharedSessionRoundTrip(t *testing.T) {
	// Simulate what handleCreateShare writes and handleShareView reads: a JSON
	// snapshot on disk, keyed by token, with the token-shaped filename.
	dir := t.TempDir()
	snap := sharedSession{
		Token:    uuid.New().String(),
		Version:  1,
		Title:    "My chat",
		Messages: []shareMessage{{Role: "user", Text: "hi"}, {Role: "assistant", Text: "hello"}},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, snap.Token+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var back sharedSession
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Token != snap.Token || back.Title != "My chat" || len(back.Messages) != 2 {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if back.Messages[1].Role != "assistant" || back.Messages[1].Text != "hello" {
		t.Errorf("message content lost: %+v", back.Messages)
	}
}
