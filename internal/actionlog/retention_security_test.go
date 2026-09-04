package actionlog

import (
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestDeleteBeforeRemovesExpiredDurableEvents(t *testing.T) {
	l, err := New(filepath.Join(t.TempDir(), "logs"), filepath.Join(t.TempDir(), "actions.db"), zap.NewNop(), WithRetention(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	current := time.Now()
	for _, at := range []time.Time{old, current} {
		if _, err := l.db.Exec(`INSERT INTO agent_events(agent_id, session_id, type, payload, created_at) VALUES('a','s','test','{}',?)`, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.DeleteBefore(time.Now().Add(-24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM agent_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retention left %d events, want 1", count)
	}
	_ = l.Close()
}
