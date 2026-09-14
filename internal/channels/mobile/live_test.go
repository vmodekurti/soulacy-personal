package mobile

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveActivityStoreRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	tok := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	start := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	dev := Device{ID: "p1", UserID: "ada", Name: "Ada", PushToken: tok, NotificationsEnabled: true, LiveStartToken: start}
	if err := store.UpsertDevice(ctx, "personal", "ada", dev); err != nil {
		t.Fatal(err)
	}
	// Re-registering without a start token keeps the one we have.
	dev.LiveStartToken = ""
	if err := store.UpsertDevice(ctx, "personal", "ada", dev); err != nil {
		t.Fatal(err)
	}
	devices, err := store.LiveStartDevices(ctx, "personal", "user:ada")
	if err != nil || len(devices) != 1 || devices[0].LiveStartToken != start {
		t.Fatalf("start token lost: %v %+v", err, devices)
	}
	if got, _ := store.LiveStartDevices(ctx, "personal", "user:someone-else"); len(got) != 0 {
		t.Fatal("start tokens must be scoped to the user")
	}
	if err := store.SetLiveStartToken(ctx, "personal", "bob", "p1", start); err != ErrDeviceOwnership {
		t.Fatalf("another user must not be able to set the token: %v", err)
	}

	a := LiveActivity{RunKey: "brief/s1", DeviceID: "p1", UserID: "ada", Token: tok}
	if err := store.UpsertLiveActivity(ctx, "personal", a); err != nil {
		t.Fatal(err)
	}
	a.UserID = "bob"
	if err := store.UpsertLiveActivity(ctx, "personal", a); err != ErrDeviceOwnership {
		t.Fatalf("expected ownership error, got %v", err)
	}
	got, err := store.LiveActivities(ctx, "personal", "brief/s1")
	if err != nil || len(got) != 1 || got[0].Token != tok || got[0].Environment != "production" {
		t.Fatalf("list: %v %+v", err, got)
	}
	if err := store.PruneLiveActivities(ctx, "personal", time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.LiveActivities(ctx, "personal", "brief/s1"); len(got) != 1 {
		t.Fatal("fresh registration must survive pruning")
	}
	if err := store.DeleteLiveActivities(ctx, "personal", "brief/s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.LiveActivities(ctx, "personal", "brief/s1"); len(got) != 0 {
		t.Fatal("delete did not clear the run")
	}
}
