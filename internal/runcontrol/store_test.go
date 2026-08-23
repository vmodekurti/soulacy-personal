package runcontrol

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPostgresCancellationIsWorkspaceScopedAndCrossConnection(t *testing.T) {
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
	runID := "cancel-" + time.Now().UTC().Format("150405.000000000")
	if err := a.Register(ctx, "ws-a", runID, time.Minute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.PurgeWorkspace(context.Background(), "ws-a") })
	if ok, err := b.RequestCancel(ctx, "ws-b", runID); err != nil || ok {
		t.Fatalf("foreign cancellation ok=%v err=%v", ok, err)
	}
	if ok, err := b.RequestCancel(ctx, "ws-a", runID); err != nil || !ok {
		t.Fatalf("shared cancellation ok=%v err=%v", ok, err)
	}
	if ok, err := a.CancelRequested(ctx, "ws-a", runID); err != nil || !ok {
		t.Fatalf("owner did not observe cancellation ok=%v err=%v", ok, err)
	}
}
