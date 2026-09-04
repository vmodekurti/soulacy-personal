package extstorage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkext "github.com/soulacy/soulacy/sdk/extstorage"
	"github.com/soulacy/soulacy/sdk/memory"
)

// TestHelperStorageSidecar is not a real test: it is re-executed as the
// sidecar subprocess (standard helper-process pattern, mirroring the E3
// adapter tests). GO_EXTSTORAGE_SIDECAR selects the behaviour.
func TestHelperStorageSidecar(t *testing.T) {
	mode := os.Getenv("GO_EXTSTORAGE_SIDECAR")
	if mode == "" {
		t.Skip("helper process only")
	}
	defer os.Exit(0)
	runHelperSidecar(mode)
}

func runHelperSidecar(mode string) {
	out := os.Stdout
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 4096), 4*1024*1024)

	var sharedDir string

	type entry struct {
		ID, AgentID, Content string
	}
	var store []entry

	type storageEntry struct {
		ID, AgentID, SessionID, Scope, Key, Content string
		CreatedAt                                   time.Time
	}
	var storageStore []storageEntry

	subs := map[string]string{} // id → subject
	nextSub := 0

	respond := func(id int64, result any) {
		m, _ := sdkext.NewResponse(id, result)
		_ = sdkext.WriteMessage(out, m)
	}

	if mode == "silent" {
		time.Sleep(10 * time.Second)
		return
	}

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		m, err := sdkext.ParseMessage([]byte(line))
		if err != nil {
			continue
		}
		if !m.IsRequest() {
			continue
		}
		id := *m.ID
		switch m.Method {
		case sdkext.MethodNegotiate:
			var p sdkext.NegotiateParams
			_ = json.Unmarshal(m.Params, &p)
			sharedDir = p.SharedDir
			shared := p.SharedDir
			if mode == "noecho" {
				shared = "/somewhere/else"
			}
			respond(id, sdkext.NegotiateResult{
				Protocol:     1,
				Name:         "go-helper",
				Capabilities: []string{"vector", "queue", "storage"},
				SharedDir:    shared,
			})
			if mode == "crashafterhello" {
				os.Exit(1)
			}
		case sdkext.MethodShutdown:
			respond(id, map[string]bool{"ok": true})
			return
		case sdkext.MethodVectorWrite:
			var p sdkext.VectorWriteParams
			_ = json.Unmarshal(m.Params, &p)
			content := p.Content
			if p.ContentFile != "" {
				b, err := os.ReadFile(filepath.Join(sharedDir, p.ContentFile))
				if err == nil {
					content = string(b)
				}
			}
			store = append(store, entry{ID: p.ID, AgentID: p.AgentID, Content: content})
			respond(id, sdkext.VectorWriteResult{OK: true})
		case sdkext.MethodVectorSearch:
			var p sdkext.VectorSearchParams
			_ = json.Unmarshal(m.Params, &p)
			hits := []sdkext.VectorHit{}
			for _, e := range store {
				if p.AgentID != "" && e.AgentID != p.AgentID {
					continue
				}
				if strings.Contains(e.Content, p.Query) {
					// If the helper stored it, let's also return it. Wait, does the helper support returning ContentFile?
					// Yes, let's optionally return ContentFile or Content. If we want to test that the host reads it,
					// we can return it as ContentFile! Wait, if we return it as ContentFile, we should write it first.
					// But we don't have to for basic roundtrip. Let's just return Content.
					hits = append(hits, sdkext.VectorHit{ID: e.ID, AgentID: e.AgentID, Content: e.Content, Distance: 0.1})
				}
			}
			respond(id, sdkext.VectorSearchResult{Results: hits})
		case sdkext.MethodQueueSubscribe:
			nextSub++
			var p sdkext.QueueSubscribeParams
			_ = json.Unmarshal(m.Params, &p)
			sid := fmt.Sprintf("sub-%d", nextSub)
			subs[sid] = p.Subject
			respond(id, sdkext.QueueSubscribeResult{SubscriptionID: sid})
		case sdkext.MethodQueueUnsubscribe:
			var p sdkext.QueueUnsubscribeParams
			_ = json.Unmarshal(m.Params, &p)
			delete(subs, p.SubscriptionID)
			respond(id, map[string]bool{"ok": true})
		case sdkext.MethodQueuePublish:
			var p sdkext.QueuePublishParams
			_ = json.Unmarshal(m.Params, &p)
			for sid, subject := range subs {
				if subject == p.Subject || subject == ">" {
					_ = sdkext.WriteMessage(out, sdkext.Message{
						JSONRPC: sdkext.Version2,
						Method:  sdkext.NotifyQueueMessage,
						Params: sdkext.MustParams(sdkext.QueueMessageParams{
							SubscriptionID: sid, Subject: p.Subject,
							Data: p.Data, DeliveryID: "d-1",
						}),
					})
				}
			}
			respond(id, sdkext.QueuePublishResult{OK: true})
		case sdkext.MethodQueueAck:
			respond(id, map[string]bool{"ok": true})
		case sdkext.MethodStorageArchive:
			var p sdkext.StorageArchiveParams
			_ = json.Unmarshal(m.Params, &p)
			entry := p.Entry
			content := entry.Content
			if p.ContentFile != "" {
				b, err := os.ReadFile(filepath.Join(sharedDir, p.ContentFile))
				if err == nil {
					content = string(b)
				}
			}
			storageStore = append(storageStore, storageEntry{
				ID:        entry.ID,
				AgentID:   entry.AgentID,
				SessionID: entry.SessionID,
				Scope:     string(entry.Scope),
				Key:       entry.Key,
				Content:   content,
				CreatedAt: entry.CreatedAt,
			})
			respond(id, sdkext.StorageArchiveResult{OK: true})
		case sdkext.MethodStorageSearch:
			var p sdkext.StorageSearchParams
			_ = json.Unmarshal(m.Params, &p)
			var entries []memory.Entry
			for _, e := range storageStore {
				if p.AgentID != "" && e.AgentID != p.AgentID {
					continue
				}
				if strings.Contains(e.Content, p.Query) {
					entries = append(entries, memory.Entry{
						ID:        e.ID,
						AgentID:   e.AgentID,
						SessionID: e.SessionID,
						Scope:     memory.Scope(e.Scope),
						Key:       e.Key,
						Content:   e.Content,
						CreatedAt: e.CreatedAt,
					})
				}
			}
			respond(id, sdkext.StorageSearchResult{Entries: entries})
		case sdkext.MethodStorageReadByScope:
			var p sdkext.StorageReadByScopeParams
			_ = json.Unmarshal(m.Params, &p)
			var entries []memory.Entry
			for _, e := range storageStore {
				if e.AgentID == p.AgentID && e.SessionID == p.SessionID && e.Scope == string(p.Scope) {
					entries = append(entries, memory.Entry{
						ID:        e.ID,
						AgentID:   e.AgentID,
						SessionID: e.SessionID,
						Scope:     memory.Scope(e.Scope),
						Key:       e.Key,
						Content:   e.Content,
						CreatedAt: e.CreatedAt,
					})
				}
			}
			respond(id, sdkext.StorageReadByScopeResult{Entries: entries})
		case sdkext.MethodStorageReadGlobal:
			var p sdkext.StorageReadGlobalParams
			_ = json.Unmarshal(m.Params, &p)
			var entries []memory.Entry
			for _, e := range storageStore {
				if e.AgentID == p.AgentID {
					entries = append(entries, memory.Entry{
						ID:        e.ID,
						AgentID:   e.AgentID,
						SessionID: e.SessionID,
						Scope:     memory.Scope(e.Scope),
						Key:       e.Key,
						Content:   e.Content,
						CreatedAt: e.CreatedAt,
					})
				}
			}
			respond(id, sdkext.StorageReadGlobalResult{Entries: entries})
		case sdkext.MethodStoragePrune:
			var p sdkext.StoragePruneParams
			_ = json.Unmarshal(m.Params, &p)
			var keep []storageEntry
			deleted := int64(0)
			for _, e := range storageStore {
				if e.AgentID == p.AgentID && e.CreatedAt.Before(p.Before) {
					deleted++
				} else {
					keep = append(keep, e)
				}
			}
			storageStore = keep
			respond(id, sdkext.StoragePruneResult{RowsDeleted: deleted})
		default:
			_ = sdkext.WriteMessage(out, sdkext.NewErrorResponse(id, sdkext.CodeMethodNotFound, "method not found"))
		}
	}
}

// helperConfig builds a ClientConfig that re-executes this test binary as
// the sidecar in the given mode.
func helperConfig(t *testing.T, mode string) ClientConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv("GO_EXTSTORAGE_SIDECAR", mode)
	return ClientConfig{
		Name:        "helper-" + mode,
		Command:     exe,
		Args:        []string{"-test.run", "TestHelperStorageSidecar"},
		ScratchRoot: t.TempDir(),
	}
}

func TestClient_NegotiateHappyPath(t *testing.T) {
	c := NewClient(helperConfig(t, "happy"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	neg := c.Negotiated()
	if neg.Protocol != 1 || neg.Name != "go-helper" {
		t.Errorf("negotiated = %+v", neg)
	}
	if c.SharedDir() == "" {
		t.Error("SharedDir empty after Start")
	}
	if fi, err := os.Stat(c.SharedDir()); err != nil || !fi.IsDir() {
		t.Errorf("scratch dir missing: %v", err)
	}
	if neg.SharedDir != c.SharedDir() {
		t.Errorf("sidecar echo %q != %q", neg.SharedDir, c.SharedDir())
	}
}

func TestClient_RefusesWrongSharedDirEcho(t *testing.T) {
	c := NewClient(helperConfig(t, "noecho"))
	err := c.Start(context.Background())
	if err == nil {
		c.Close()
		t.Fatal("Start must fail when the sidecar doesn't echo the shared dir")
	}
	if !strings.Contains(err.Error(), "shared dir") {
		t.Errorf("error should mention shared dir: %v", err)
	}
}

func TestClient_HandshakeTimeout(t *testing.T) {
	cfg := helperConfig(t, "silent")
	cfg.HandshakeTimeout = 200 * time.Millisecond
	c := NewClient(cfg)
	start := time.Now()
	if err := c.Start(context.Background()); err == nil {
		c.Close()
		t.Fatal("Start must fail on a silent sidecar")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("handshake timeout not honoured (took %s)", time.Since(start))
	}
}

func TestClient_UnknownMethodSurfacesRPCError(t *testing.T) {
	c := NewClient(helperConfig(t, "happy"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	err := c.Call(context.Background(), "no.such.method", struct{}{}, nil)
	if err == nil {
		t.Fatal("expected method-not-found error")
	}
	var rpcErr *sdkext.Error
	if !errorsAs(err, &rpcErr) || rpcErr.Code != sdkext.CodeMethodNotFound {
		t.Errorf("want wrapped *extstorage.Error code %d, got %v", sdkext.CodeMethodNotFound, err)
	}
}

func TestClient_CallAfterSidecarExit(t *testing.T) {
	c := NewClient(helperConfig(t, "crashafterhello"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after sidecar exit")
	}
	err := c.Call(context.Background(), sdkext.MethodVectorSearch,
		sdkext.VectorSearchParams{Query: "x", TopK: 1}, nil)
	if err == nil {
		t.Fatal("Call after exit must error")
	}
}

func TestClient_CloseRemovesScratchAndIsIdempotent(t *testing.T) {
	c := NewClient(helperConfig(t, "happy"))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	dir := c.SharedDir()
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("scratch dir not removed: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// errorsAs avoids importing errors in two places.
func errorsAs(err error, target *(*sdkext.Error)) bool {
	for err != nil {
		if e, ok := err.(*sdkext.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
