package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/channels/mobile"
)

func TestWaitForNodeCommandReturnsWhenThePhoneAnswers(t *testing.T) {
	store, err := mobile.Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.RegisterNode(ctx, "personal", "admin", mobile.Node{DeviceID: "phone", Name: "Ada", Platform: "ios", Capabilities: []string{"focus.status"}}); err != nil {
		t.Fatal(err)
	}
	cmd, err := store.EnqueueNodeCommand(ctx, "personal", "admin", mobile.NodeCommand{DeviceID: "phone", Command: "focus.status"})
	if err != nil {
		t.Fatal(err)
	}
	// Without waiting the read says queued.
	if got, _ := waitForNodeCommand(ctx, store, "admin", "phone", cmd.ID, 0); got.Status != "queued" {
		t.Fatalf("immediate read: %s", got.Status)
	}
	// The phone answers a second later; the waiting read returns the result.
	go func() {
		time.Sleep(700 * time.Millisecond)
		if _, err := store.ClaimNodeCommand(ctx, "personal", "admin", "phone", cmd.ID); err != nil {
			return
		}
		_, _ = store.FinishNodeCommand(ctx, "personal", "admin", "phone", cmd.ID, "completed", json.RawMessage(`{"focused":true}`), "")
	}()
	start := time.Now()
	got, err := waitForNodeCommand(ctx, store, "admin", "phone", cmd.ID, 10*time.Second)
	if err != nil || got.Status != "completed" || string(got.Result) != `{"focused":true}` {
		t.Fatalf("waited read: %v %+v", err, got)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("should return as soon as the result lands, not at the deadline")
	}
	// A bounded wait on a command nobody answers returns the pending state.
	other, _ := store.EnqueueNodeCommand(ctx, "personal", "admin", mobile.NodeCommand{DeviceID: "phone", Command: "focus.status"})
	start = time.Now()
	if got, _ := waitForNodeCommand(ctx, store, "admin", "phone", other.ID, time.Second); got.Status != "queued" || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("bounded wait wrong: %s after %v", got.Status, time.Since(start))
	}
}
