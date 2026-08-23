package approvals

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPostgresStoreSharesDecisionsAcrossConnections(t *testing.T) {
	dsn := os.Getenv("SOULACY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES INTEGRATION SKIPPED LOUDLY: set SOULACY_TEST_POSTGRES_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := "pg-approval-" + time.Now().UTC().Format("20060102150405.000000000")
	_, err = a.Request(ctx, Approval{ID: id, WorkspaceID: "ws-pg-test", Tool: "shell_exec",
		RequesterSubject: "asker", RequiredResource: "approvals", RequiredAction: "write"}, map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.PurgeWorkspace(context.Background(), "ws-pg-test") })
	by := Eligibility{Subject: "owner", WorkspaceID: "ws-pg-test", Permits: func(_, _ string) bool { return true }}
	if _, err = b.Decide(ctx, "ws-pg-test", id, true, by, ""); err != nil {
		t.Fatal(err)
	}
	got, err := a.Get(ctx, "ws-pg-test", id)
	if err != nil || got.Status != StatusApproved {
		t.Fatalf("shared decision=%+v err=%v", got, err)
	}
}
