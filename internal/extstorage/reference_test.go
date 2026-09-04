package extstorage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/sdk/extstorage/storagetest"
	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/queue"
)

func referenceSidecar(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script, err := filepath.Abs("../../scripts/reference-storage-sidecar.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("reference sidecar missing: %v", err)
	}
	return script
}

// TestReferenceSidecarConformance proves the contract is implementable
// outside Go: the exported E24 kit runs against the real python reference
// sidecar (mirrors the E3/E11 cross-language proof).
func TestReferenceSidecarConformance(t *testing.T) {
	script := referenceSidecar(t)
	if err := storagetest.RunConformance(context.Background(), t.TempDir(),
		"python3", script); err != nil {
		t.Fatal(err)
	}
}

// TestReferenceSidecarEndToEnd drives the python reference through the
// real host adapters: vector write/search and queue pub/sub.
func TestReferenceSidecarEndToEnd(t *testing.T) {
	script := referenceSidecar(t)
	cfg := ClientConfig{
		Name:        "py-reference",
		Command:     "python3",
		Args:        []string{script},
		ScratchRoot: t.TempDir(),
	}

	vb, err := NewVectorBackend(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewVectorBackend: %v", err)
	}
	defer vb.Close()
	err = vb.Write(context.Background(), memory.Entry{
		ID: "p1", AgentID: "a1", Content: "soulacy external storage protocol",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	hits, err := vb.Search(context.Background(), "a1", "storage protocol", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.ID != "p1" {
		t.Fatalf("hits = %+v", hits)
	}

	qb, err := NewQueueBackend(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewQueueBackend: %v", err)
	}
	defer qb.Close()
	got := make(chan *queue.Message, 1)
	if _, err := qb.Subscribe(context.Background(), "soulacy.events.>", "", func(m *queue.Message) {
		got <- m
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := qb.Publish(context.Background(), "soulacy.events.test", []byte("hi")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case m := <-got:
		if string(m.Data) != "hi" || m.Subject != "soulacy.events.test" {
			t.Errorf("delivered = %q %q", m.Subject, m.Data)
		}
		if err := m.Ack(); err != nil {
			t.Errorf("Ack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery from python reference within 5s")
	}
}

// TestReferenceSidecarLargeContentSpillsToSharedDir pins the shared-mount
// transport: ≥1KiB content travels as a content_file under the scratch
// dir, and the sidecar resolves it back to the full text.
func TestReferenceSidecarLargeContentSpillsToSharedDir(t *testing.T) {
	script := referenceSidecar(t)
	vb, err := NewVectorBackend(context.Background(), ClientConfig{
		Name:        "py-spill",
		Command:     "python3",
		Args:        []string{script},
		ScratchRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewVectorBackend: %v", err)
	}
	defer vb.Close()

	big := "soulacy spill marker " + strings.Repeat("x", 2048)
	err = vb.Write(context.Background(), memory.Entry{
		ID: "big1", AgentID: "a1", Content: big, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	hits, err := vb.Search(context.Background(), "a1", "spill marker", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d", len(hits))
	}
	if hits[0].Entry.Content != big {
		t.Errorf("content round-trip lost: got %d bytes, want %d",
			len(hits[0].Entry.Content), len(big))
	}
}

// TestReferenceSidecarStorageBackend drives the storage.* method family
// (memory archive) through the host adapter against the python reference.
func TestReferenceSidecarStorageBackend(t *testing.T) {
	script := referenceSidecar(t)
	sb, err := NewStorageBackend(context.Background(), ClientConfig{
		Name:        "py-storage",
		Command:     "python3",
		Args:        []string{script},
		ScratchRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewStorageBackend: %v", err)
	}
	defer sb.Close()

	old := memory.Entry{
		ID: "s1", AgentID: "a1", SessionID: "sess-1", Scope: memory.ScopeSession,
		Content: "remember the milk", CreatedAt: time.Now().Add(-48 * time.Hour),
	}
	fresh := memory.Entry{
		ID: "s2", AgentID: "a1", SessionID: "sess-1", Scope: memory.ScopeSession,
		Content: "soulacy protocol notes", CreatedAt: time.Now(),
	}
	for _, e := range []memory.Entry{old, fresh} {
		if err := sb.Archive(e); err != nil {
			t.Fatalf("Archive %s: %v", e.ID, err)
		}
	}
	// Duplicate IDs are silently ignored (MemoryBackend contract).
	if err := sb.Archive(fresh); err != nil {
		t.Fatalf("Archive dup: %v", err)
	}

	entries, err := sb.Search("a1", "protocol", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) == 0 || entries[0].ID != "s2" {
		t.Fatalf("Search hits = %+v", entries)
	}

	entries, err = sb.ReadByScope("a1", "sess-1", memory.ScopeSession, 10)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadByScope = %d entries, want 2", len(entries))
	}

	entries, err = sb.ReadGlobal("a1", 10)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadGlobal = %d entries, want 2", len(entries))
	}

	deleted, err := sb.Prune("a1", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Prune deleted %d, want 1 (the 48h-old entry)", deleted)
	}
	entries, _ = sb.ReadGlobal("a1", 10)
	if len(entries) != 1 || entries[0].ID != "s2" {
		t.Errorf("post-prune entries = %+v", entries)
	}
}
