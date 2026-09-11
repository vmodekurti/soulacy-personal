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

