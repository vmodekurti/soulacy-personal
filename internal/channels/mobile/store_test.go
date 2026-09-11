package mobile

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDeliveryVisibilityAndReceiptsArePerDevice(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, device := range []Device{{ID: "phone-a"}, {ID: "phone-b"}} {
		user := "alex"
		if device.ID == "phone-b" {
			user = "blair"
		}
		if err := store.UpsertDevice(ctx, "workspace", user, device); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []Delivery{
		{ID: "all", Destination: "all", AgentID: "agent", Body: "everyone", CreatedAt: time.Now()},
		{ID: "device", Destination: "device:phone-a", AgentID: "agent", Body: "one device", CreatedAt: time.Now()},
		{ID: "user", Destination: "user:alex", AgentID: "agent", Body: "one user", CreatedAt: time.Now()},
	} {
		if err := store.Add(ctx, "workspace", d); err != nil {
			t.Fatal(err)
		}
	}
	a, err := store.List(ctx, "workspace", "phone-a", "alex", 20)
	if err != nil || len(a) != 3 {
		t.Fatalf("phone-a deliveries = %d, err=%v", len(a), err)
	}
	b, err := store.List(ctx, "workspace", "phone-b", "blair", 20)
	if err != nil || len(b) != 1 {
		t.Fatalf("phone-b deliveries = %d, err=%v", len(b), err)
	}
	if err := store.MarkRead(ctx, "workspace", "all", "phone-a", "alex"); err != nil {
		t.Fatal(err)
	}
	a, _ = store.List(ctx, "workspace", "phone-a", "alex", 20)
	b, _ = store.List(ctx, "workspace", "phone-b", "blair", 20)
	if a[len(a)-1].ReadAt == nil {
		t.Fatal("phone-a receipt was not stored")
	}
	if b[0].ReadAt != nil {
		t.Fatal("phone-a receipt leaked to phone-b")
	}
}

func TestUnqualifiedDeliveryDestinationsRemainFetchable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.UpsertDevice(ctx, "personal", "admin", Device{ID: "phone"}); err != nil {
		t.Fatal(err)
	}

	// Scheduler and legacy channel routes may use an unqualified thread name.
	// New deliveries must normalize to broadcast so the same phone that receives
	// the APNs notification can fetch the result.
	if err := store.Add(ctx, "personal", Delivery{
		ID: "new-unqualified", Destination: "scheduler", AgentID: "reporter", Body: "new result",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "personal", "new-unqualified", "phone", "admin")
	if err != nil {
		t.Fatalf("fetch new unqualified delivery: %v", err)
	}
	if got.Destination != "all" {
		t.Fatalf("normalized destination = %q, want all", got.Destination)
	}

	// Existing databases can already contain an unqualified destination written
	// by an older gateway. Keep those rows readable after upgrading.
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO mobile_deliveries
    (workspace_id,id,destination,agent_id,session_id,title,body,parts_json,metadata_json,created_at)
    VALUES(?,?,?,?,?,?,?,?,?,?)`, "personal", "legacy-unqualified", "mobile", "reporter", "", "Legacy", "old result", "[]", "{}", createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "personal", "legacy-unqualified", "phone", "admin"); err != nil {
		t.Fatalf("fetch legacy unqualified delivery: %v", err)
	}
	if err := store.MarkRead(ctx, "personal", "legacy-unqualified", "phone", "admin"); err != nil {
		t.Fatalf("mark legacy unqualified delivery read: %v", err)
	}
}
