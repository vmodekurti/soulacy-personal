package session

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresHistoryWorkspaceIsolation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SOULACY_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES INTEGRATION SKIPPED: set SOULACY_TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	store, err := NewPostgresHistoryStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	wsA, wsB, sessionID := "ws_pg_a_"+suffix, "ws_pg_b_"+suffix, "same-session-"+suffix
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM conversation_history WHERE workspace_id=$1 OR workspace_id=$2`, wsA, wsB)
	})
	for workspace, content := range map[string]string{wsA: "tenant A secret", wsB: "tenant B secret"} {
		if err := store.Append(ctx, ConversationEntry{WorkspaceID: workspace, Subject: "user", SessionID: sessionID, AgentID: "assistant", Role: "user", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := store.Load(ctx, wsA, sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Load(ctx, wsB, sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].Content != "tenant A secret" || len(b) != 1 || b[0].Content != "tenant B secret" {
		t.Fatalf("cross-tenant history: A=%+v B=%+v", a, b)
	}
}

func TestPostgresConversationLockSerializesReplicas(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SOULACY_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES INTEGRATION SKIPPED: set SOULACY_TEST_POSTGRES_DSN")
	}
	store, err := NewPostgresHistoryStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	release, err := store.LockConversation(context.Background(), "ws_lock_test", "shared-session")
	if err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err = store.LockConversation(blockedCtx, "ws_lock_test", "shared-session"); err == nil {
		release()
		t.Fatal("a second replica acquired the same conversation lock concurrently")
	}
	release()
	secondRelease, err := store.LockConversation(context.Background(), "ws_lock_test", "shared-session")
	if err != nil {
		t.Fatalf("lock remained held after release: %v", err)
	}
	secondRelease()
}
