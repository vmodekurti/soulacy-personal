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
