package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionDeletesExpiredAuditDay(t *testing.T) {
	root := t.TempDir()
	oldDay := time.Now().Add(-72 * time.Hour).Format("2006-01-02")
	oldDir := filepath.Join(root, oldDay)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "run.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewWithRetention(root, 24*time.Hour)
	l.Log(Entry{Timestamp: time.Now(), SessionID: "new", AgentID: "a", Tool: "x"})
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("expired audit directory still exists: %v", err)
	}
}
