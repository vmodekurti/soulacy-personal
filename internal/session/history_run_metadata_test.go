package session

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFinalRunMetadataSurvivesReloadWithoutChangingOtherRuns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewSQLiteHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	for _, run := range []string{"first", "second"} {
		if err := store.Append(ctx, ConversationEntry{SessionID: "s", AgentID: "a", Role: "assistant", Content: run,
			Metadata: map[string]string{"run_id": run, "response.spoken": "preserved"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateRunMetadata(ctx, "s", "a", "first", map[string]string{"autopilot_verification": "unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	reopened, err := NewSQLiteHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entries, err := reopened.Load(ctx, "s", 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if entries[0].Metadata["autopilot_verification"] != "unknown" || entries[0].Metadata["response.spoken"] != "preserved" || entries[1].Metadata["autopilot_verification"] != "" {
		t.Fatalf("wrong final metadata: %+v", entries)
	}
}
