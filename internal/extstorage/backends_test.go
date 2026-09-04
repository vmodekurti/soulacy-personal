package extstorage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/sdk/memory"
	"github.com/soulacy/soulacy/sdk/queue"
)

func TestVectorBackend_WriteSearchRoundTrip(t *testing.T) {
	b, err := NewVectorBackend(context.Background(), helperConfig(t, "happy"))
	if err != nil {
		t.Fatalf("NewVectorBackend: %v", err)
	}
	defer b.Close()

	err = b.Write(context.Background(), memory.Entry{
		ID: "e1", AgentID: "a1", Scope: memory.ScopeAgent,
		Content: "the quick brown fox", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	hits, err := b.Search(context.Background(), "a1", "quick", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.ID != "e1" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Entry.Content != "the quick brown fox" {
		t.Errorf("content = %q", hits[0].Entry.Content)
	}

	// Agent scoping: other agents see nothing.
	hits, err = b.Search(context.Background(), "other", "quick", 5)
	if err != nil {
		t.Fatalf("Search other: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("cross-agent hits = %+v", hits)
	}
}

func TestStorageBackend_ArchiveSearchPruneRoundTrip(t *testing.T) {
	b, err := NewStorageBackend(context.Background(), helperConfig(t, "happy"))
	if err != nil {
		t.Fatalf("NewStorageBackend: %v", err)
	}
	defer b.Close()

	now := time.Now().Truncate(time.Second)
	err = b.Archive(memory.Entry{
		ID: "m1", AgentID: "a1", SessionID: "s1", Scope: memory.ScopeAgent,
		Content: "archived memory content", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	entries, err := b.Search("a1", "memory", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "m1" {
		t.Fatalf("Search entries = %+v", entries)
	}
	if entries[0].Content != "archived memory content" {
		t.Errorf("content = %q", entries[0].Content)
	}

	// Read by scope
	entries, err = b.ReadByScope("a1", "s1", memory.ScopeAgent, 5)
	if err != nil {
		t.Fatalf("ReadByScope: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "m1" {
		t.Fatalf("ReadByScope entries = %+v", entries)
	}

	// Read global
	entries, err = b.ReadGlobal("a1", 5)
	if err != nil {
		t.Fatalf("ReadGlobal: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "m1" {
		t.Fatalf("ReadGlobal entries = %+v", entries)
	}

	// Prune
	deleted, err := b.Prune("a1", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 row deleted, got %d", deleted)
	}

	entries, err = b.ReadGlobal("a1", 5)
	if err != nil {
		t.Fatalf("ReadGlobal after prune: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after prune, got %+v", entries)
	}
}

func TestQueueBackend_PublishSubscribeAck(t *testing.T) {
	b, err := NewQueueBackend(context.Background(), helperConfig(t, "happy"))
	if err != nil {
		t.Fatalf("NewQueueBackend: %v", err)
	}
	defer b.Close()

	got := make(chan *queue.Message, 1)
	sub, err := b.Subscribe(context.Background(), "events.test", "", func(m *queue.Message) {
		got <- m
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := b.Publish(context.Background(), "events.test", []byte("payload")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case m := <-got:
		if m.Subject != "events.test" || string(m.Data) != "payload" {
			t.Errorf("delivered = %q %q", m.Subject, m.Data)
		}
		if err := m.Ack(); err != nil {
			t.Errorf("Ack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery within 5s")
	}

	// After Unsubscribe, deliveries stop reaching the handler.
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if err := b.Publish(context.Background(), "events.test", []byte("late")); err != nil {
		t.Fatalf("Publish 2: %v", err)
	}
	select {
	case m := <-got:
		t.Errorf("delivery after unsubscribe: %q", m.Data)
	case <-time.After(300 * time.Millisecond):
	}

	if err := sub.Unsubscribe(); err != nil {
		t.Errorf("second Unsubscribe: %v", err)
	}
}

func TestNewScratchDir(t *testing.T) {
	root := t.TempDir()
	dir, cleanup, err := NewScratchDir(root, "my id/!x")
	if err != nil {
		t.Fatalf("NewScratchDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("dir not absolute: %q", dir)
	}
	if !strings.HasPrefix(filepath.Base(dir), "my_id__x-") {
		t.Errorf("sanitised name wrong: %q", filepath.Base(dir))
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("perm = %v, want 0700", fi.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup left dir behind")
	}
}

func TestRegistryFactories(t *testing.T) {
	// Missing command must error without spawning anything.
	if _, err := clientConfigFrom(map[string]any{}, "vector-external"); err == nil {
		t.Error("empty command must error")
	}
	cc, err := clientConfigFrom(map[string]any{
		"command":      "/bin/echo",
		"args":         []any{"a", "b"},
		"id":           "custom",
		"scratch_root": "/tmp/x",
	}, "vector-external")
	if err != nil {
		t.Fatalf("clientConfigFrom: %v", err)
	}
	if cc.Name != "custom" || cc.Command != "/bin/echo" || len(cc.Args) != 2 || cc.ScratchRoot != "/tmp/x" {
		t.Errorf("cc = %+v", cc)
	}
}

func TestBackends_FileSpilling(t *testing.T) {
	// 1. Test Vector Spilling
	vBack, err := NewVectorBackend(context.Background(), helperConfig(t, "happy"))
	if err != nil {
		t.Fatalf("NewVectorBackend: %v", err)
	}
	defer vBack.Close()

	// Content >= 1024 bytes
	largeContent := strings.Repeat("a", 1500)
	err = vBack.Write(context.Background(), memory.Entry{
		ID: "e-large", AgentID: "a1", Scope: memory.ScopeAgent,
		Content: largeContent, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Vector Write: %v", err)
	}

	// Verify that a file was indeed written to the scratch directory
	files, err := os.ReadDir(vBack.Client().SharedDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	foundVectorFile := false
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "vector-") {
			foundVectorFile = true
			data, err := os.ReadFile(filepath.Join(vBack.Client().SharedDir(), f.Name()))
			if err != nil {
				t.Fatalf("ReadFile %s: %v", f.Name(), err)
			}
			if string(data) != largeContent {
				t.Errorf("vector file content mismatch, got len %d, want %d", len(data), len(largeContent))
			}
		}
	}
	if !foundVectorFile {
		t.Errorf("expected to find spilled vector file in scratch dir, but got files: %v", files)
	}

	// Verify that we can search and read it back
	hits, err := vBack.Search(context.Background(), "a1", "aaaa", 5)
	if err != nil {
		t.Fatalf("Vector Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.Content != largeContent {
		t.Fatalf("expected to read back large content, got len %d", len(hits))
	}

	// 2. Test Storage Spilling
	sBack, err := NewStorageBackend(context.Background(), helperConfig(t, "happy"))
	if err != nil {
		t.Fatalf("NewStorageBackend: %v", err)
	}
	defer sBack.Close()

	largeStorageContent := strings.Repeat("b", 2000)
	err = sBack.Archive(memory.Entry{
		ID: "m-large", AgentID: "a1", Scope: memory.ScopeAgent,
		Content: largeStorageContent, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Storage Archive: %v", err)
	}

	// Verify that a storage file was written to scratch directory
	files, err = os.ReadDir(sBack.Client().SharedDir())
	if err != nil {
		t.Fatalf("ReadDir storage: %v", err)
	}
	foundStorageFile := false
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "storage-") {
			foundStorageFile = true
			data, err := os.ReadFile(filepath.Join(sBack.Client().SharedDir(), f.Name()))
			if err != nil {
				t.Fatalf("ReadFile %s: %v", f.Name(), err)
			}
			if string(data) != largeStorageContent {
				t.Errorf("storage file content mismatch, got len %d, want %d", len(data), len(largeStorageContent))
			}
		}
	}
	if !foundStorageFile {
		t.Errorf("expected to find spilled storage file in scratch dir")
	}

	// Verify roundtrip search
	entries, err := sBack.Search("a1", "bbbb", 5)
	if err != nil {
		t.Fatalf("Storage Search: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != largeStorageContent {
		t.Fatalf("expected to read back large archived content, got entries %+v", entries)
	}
}
