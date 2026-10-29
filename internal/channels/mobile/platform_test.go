package mobile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

func TestDevicePlatformMigrationAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile.db")
	// A database written before live activities or Android existed.
	legacy, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := legacy.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO mobile_devices
    (workspace_id,id,user_id,name,push_token,push_environment,bundle_id,notifications_enabled,created_at,updated_at)
    VALUES('personal','old-iphone','ada','iPhone',?,'production','dev.soulacy.ios',1,?,?)`,
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", now, now); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open migrates legacy database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	devices, err := store.DevicesForUser(ctx, "personal", "ada")
	if err != nil || len(devices) != 1 || devices[0].Platform != PlatformIOS {
		t.Fatalf("legacy device = %#v, err = %v; want platform ios", devices, err)
	}
	// Opening again is idempotent.
	if again, err := Open(path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else {
		_ = again.Close()
	}

	start := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := store.UpsertDevice(ctx, "personal", "ada", Device{ID: "pixel", Platform: PlatformAndroid,
		PushToken: "Mixed-Case:fcm_TOKEN", PushEnvironment: "development", LiveStartToken: start,
		NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	targets, err := store.TargetDevices(ctx, "personal", "device:pixel")
	if err != nil || len(targets) != 1 {
		t.Fatalf("android target = %#v, err = %v", targets, err)
	}
	got := targets[0]
	if got.Platform != PlatformAndroid || got.PushToken != "Mixed-Case:fcm_TOKEN" || got.BundleID != BundleAndroid ||
		got.PushEnvironment != "" || got.Name != "Android phone" {
		t.Fatalf("android device = %#v", got)
	}
	if live, _ := store.LiveStartDevices(ctx, "personal", "user:ada"); len(live) != 0 {
		t.Fatalf("android device must never be a Live Activity target: %#v", live)
	}
	err = store.UpsertLiveActivity(ctx, "personal", LiveActivity{RunKey: "r", DeviceID: "pixel", UserID: "ada", Token: start})
	if err != ErrLiveActivityUnsupported {
		t.Fatalf("android live activity registration error = %v", err)
	}

	// An iOS phone re-registered as Android drops its ActivityKit token.
	if err := store.UpsertDevice(ctx, "personal", "ada", Device{ID: "old-iphone", PushToken: strings.Repeat("ab", 32),
		LiveStartToken: start, NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if live, _ := store.LiveStartDevices(ctx, "personal", "user:ada"); len(live) != 1 {
		t.Fatalf("iOS start token missing: %#v", live)
	}
	if err := store.UpsertDevice(ctx, "personal", "ada", Device{ID: "old-iphone", Platform: PlatformAndroid,
		PushToken: "t", NotificationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if live, _ := store.LiveStartDevices(ctx, "personal", "user:ada"); len(live) != 0 {
		t.Fatalf("start token survived switch to android: %#v", live)
	}
	var token string
	if err := store.db.QueryRow(`SELECT live_start_token FROM mobile_devices WHERE id='old-iphone'`).Scan(&token); err != nil || token != "" {
		t.Fatalf("live_start_token = %q, err = %v", token, err)
	}

	if err := store.ClearPushToken(ctx, "personal", "pixel", "stale-token"); err != nil {
		t.Fatal(err)
	}
	if targets, _ = store.TargetDevices(ctx, "personal", "device:pixel"); len(targets) != 1 {
		t.Fatal("clearing a stale token must keep a freshly registered one")
	}
	if err := store.ClearPushToken(ctx, "personal", "pixel", "Mixed-Case:fcm_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if targets, _ = store.TargetDevices(ctx, "personal", "device:pixel"); len(targets) != 0 {
		t.Fatalf("token not cleared: %#v", targets)
	}
}
