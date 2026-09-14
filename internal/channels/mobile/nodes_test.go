package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNodeCommandsOwnershipClaimAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	node := Node{DeviceID: "phone", Platform: "ios", Capabilities: []string{"device.info", "system.notify"}}
	if err := s.RegisterNode(ctx, "w", "alice", node); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterNode(ctx, "w", "bob", node); !errors.Is(err, ErrDeviceOwnership) {
		t.Fatalf("device takeover: %v", err)
	}
	cmd, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{ID: "one", DeviceID: "phone", Command: "system.notify", Params: json.RawMessage(`{"title":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PendingNodeCommands(ctx, "w", "bob", "phone"); !errors.Is(err, ErrDeviceOwnership) {
		t.Fatalf("owner boundary: %v", err)
	}
	if _, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "phone", Command: "location.current"}); !errors.Is(err, ErrNodeInvalid) {
		t.Fatalf("disabled capability: %v", err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ClaimNodeCommand(ctx, "w", "alice", "phone", cmd.ID)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrNodeConflict) {
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("claims=%d", won.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pending, err := s.PendingNodeCommands(ctx, "w", "alice", "phone")
	if err != nil || len(pending) != 0 {
		t.Fatalf("claimed command replayed after restart: %v %v", pending, err)
	}
	for range 2 {
		if _, err := s.FinishNodeCommand(ctx, "w", "alice", "phone", cmd.ID, "completed", json.RawMessage(`{"delivered":true}`), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.FinishNodeCommand(ctx, "w", "alice", "phone", cmd.ID, "failed", nil, "different result"); !errors.Is(err, ErrNodeConflict) {
		t.Fatalf("terminal overwrite: %v", err)
	}
	if _, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{ID: "one", DeviceID: "phone", Command: "device.info"}); !errors.Is(err, ErrNodeConflict) {
		t.Fatalf("idempotency body changed: %v", err)
	}
}

func TestNodeExpiryAndResultBeforeClaim(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.RegisterNode(ctx, "w", "alice", Node{DeviceID: "p", Platform: "ios", Capabilities: []string{"device.info"}}); err != nil {
		t.Fatal(err)
	}
	c, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "p", Command: "device.info"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishNodeCommand(ctx, "w", "alice", "p", c.ID, "completed", nil, ""); !errors.Is(err, ErrNodeConflict) {
		t.Fatalf("unclaimed result: %v", err)
	}
	_, err = s.db.Exec(`UPDATE mobile_node_commands SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNodeCommand(ctx, "w", "alice", "p", c.ID); !errors.Is(err, ErrNodeConflict) {
		t.Fatalf("expired claim: %v", err)
	}
}

func TestNodePermissionRevokedBeforeClaim(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	node := Node{DeviceID: "p", Platform: "ios", Capabilities: []string{"device.info", "location.current"}}
	if err := s.RegisterNode(ctx, "w", "alice", node); err != nil {
		t.Fatal(err)
	}
	c, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "p", Command: "location.current"})
	if err != nil {
		t.Fatal(err)
	}
	node.Capabilities = []string{"device.info"}
	if err := s.RegisterNode(ctx, "w", "alice", node); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNodeCommand(ctx, "w", "alice", "p", c.ID); !errors.Is(err, ErrNodeConflict) {
		t.Fatalf("revoked capability was claimed: %v", err)
	}
}

func TestHealthAndFocusCommandsAreGatedByAdvertisedCapability(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.RegisterNode(ctx, "w", "alice", Node{DeviceID: "phone", Name: "Ada", Platform: "ios", Capabilities: []string{"focus.status"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "phone", Command: "health.summary"}); !errors.Is(err, ErrNodeInvalid) {
		t.Fatalf("health.summary must be refused until the phone enables it: %v", err)
	}
	cmd, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "phone", Command: "focus.status"})
	if err != nil || cmd.Status != "queued" {
		t.Fatalf("focus.status should queue: %v %+v", err, cmd)
	}
	if err := s.RegisterNode(ctx, "w", "alice", Node{DeviceID: "phone", Name: "Ada", Platform: "ios", Capabilities: []string{"focus.status", "health.summary"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "phone", Command: "health.summary", Params: json.RawMessage(`{"window":"today"}`)}); err != nil {
		t.Fatalf("health.summary should queue once enabled: %v", err)
	}
}

func TestCanvasComponentsAreValidatedOnEnqueue(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "mobile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.RegisterNode(ctx, "w", "alice", Node{DeviceID: "phone", Name: "Ada", Platform: "ios", Capabilities: []string{"canvas.present"}}); err != nil {
		t.Fatal(err)
	}
	good := `{"title":"Trip plan","components":[
	  {"type":"text","markdown":"**Two options** for Friday."},
	  {"type":"checklist","title":"Pack","items":[{"label":"Passport","done":false},{"label":"Charger","done":true}]},
	  {"type":"chart","title":"Spend","kind":"bar","labels":["Mon","Tue"],"series":[{"label":"USD","values":[12,30]}]},
	  {"type":"metric","label":"Budget left","value":"420","unit":"USD","trend":"down"},
	  {"type":"form","id":"pick","fields":[{"name":"option","label":"Which one?","kind":"choice","options":["Beach","City"],"required":true},{"name":"notes","label":"Anything else","kind":"text"}],"submit":"Choose"}]}`
	cmd, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "phone", Command: "canvas.present", Params: json.RawMessage(good), ExpiresAt: time.Now().Add(14 * time.Minute)})
	if err != nil || cmd.Status != "queued" {
		t.Fatalf("typed canvas should queue: %v", err)
	}
	for name, bad := range map[string]string{
		"unknown type": `{"components":[{"type":"video"}]}`,
		"two forms":    `{"components":[{"type":"form","fields":[{"name":"a"}]},{"type":"form","fields":[{"name":"b"}]}]}`,
		"empty form":   `{"components":[{"type":"form","fields":[]}]}`,
		"bad kind":     `{"components":[{"type":"form","fields":[{"name":"a","kind":"slider"}]}]}`,
		"not array":    `{"components":{"type":"text"}}`,
		"empty chart":  `{"components":[{"type":"chart","series":[]}]}`,
	} {
		if _, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "phone", Command: "canvas.present", Params: json.RawMessage(bad)}); !errors.Is(err, ErrNodeInvalid) {
			t.Fatalf("%s should be refused, got %v", name, err)
		}
	}
	// Legacy cards without components still work.
	if _, err := s.EnqueueNodeCommand(ctx, "w", "alice", NodeCommand{DeviceID: "phone", Command: "canvas.present", Params: json.RawMessage(`{"title":"Hi","items":["a","b"]}`)}); err != nil {
		t.Fatalf("legacy canvas: %v", err)
	}
}
