package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// #162 — a server that answers the handshake without any credential (Equibles
// does) must not be reported as having verified the key.
func TestProbeHTTP_CredentialUncheckedWhenHandshakeIsAnonymous(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cfg := ServerConfig{Transport: "http", URL: srv.URL, Auth: AuthConfig{Type: "bearer", SecretRef: "mcp.x.credential"}}
	res, err := ProbeHTTP(context.Background(), cfg, func(context.Context, string) (string, error) { return "eq_key", nil })
	if err != nil {
		t.Fatalf("ProbeHTTP: %v", err)
	}
	if res.Tools != 1 {
		t.Fatalf("tools = %d, want 1", res.Tools)
	}
	if res.CredentialChecked {
		t.Fatal("server accepted the anonymous handshake; the credential must be reported as unchecked")
	}
}

// The good case: the server refuses the anonymous handshake, so the credential
// on the first one is what made the difference.
func TestProbeHTTP_CredentialCheckedWhenAnonymousHandshakeIsRefused(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	gate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer eq_key" {
			http.Error(w, `{"Error":"Missing API key"}`, http.StatusUnauthorized)
			return
		}
		fake.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(gate)
	defer srv.Close()

	cfg := ServerConfig{Transport: "http", URL: srv.URL, Auth: AuthConfig{Type: "bearer", SecretRef: "mcp.x.credential"}}
	res, err := ProbeHTTP(context.Background(), cfg, func(context.Context, string) (string, error) { return "eq_key", nil })
	if err != nil {
		t.Fatalf("ProbeHTTP: %v", err)
	}
	if !res.CredentialChecked {
		t.Fatal("server refused the anonymous handshake; the credential must be reported as checked")
	}

	// And with no auth configured at all, nothing is claimed.
	res, err = ProbeHTTP(context.Background(), ServerConfig{Transport: "http", URL: srv.URL}, nil)
	if err == nil && res.CredentialChecked {
		t.Fatal("no credential configured, yet reported as checked")
	}
}

func snapshotDetail(t *testing.T, c *Client, id string) string {
	t.Helper()
	for _, s := range c.ServersSnapshot() {
		if s.ID == id {
			return s.Detail
		}
	}
	t.Fatalf("server %q not in snapshot", id)
	return ""
}

// #162 — once a tool call is refused for want of a valid credential, the
// server's status line says so instead of "N tool(s)"; the next successful
// call clears it.
func TestCall_AuthFailureFlagsCredentialUntilNextSuccess(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	var reject bool
	var mu sync.Mutex
	fake.handle("tools/call", func(json.RawMessage) (json.RawMessage, *rpcError) {
		mu.Lock()
		defer mu.Unlock()
		if reject {
			return json.RawMessage(`{"content":[{"type":"text","text":"Missing API key. Use Authorization: Bearer eq_... header"}],"isError":true}`), nil
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := newTestClient(t, "quotes", srv)
	name := FullNamePrefix + "quotes__echo"

	mu.Lock()
	reject = true
	mu.Unlock()
	if _, err := c.Call(context.Background(), name, nil); err == nil {
		t.Fatal("expected the rejected call to fail")
	}
	if d := snapshotDetail(t, c, "quotes"); !strings.HasPrefix(d, credentialRejectedPrefix) {
		t.Fatalf("detail after auth failure = %q, want prefix %q", d, credentialRejectedPrefix)
	}

	mu.Lock()
	reject = false
	mu.Unlock()
	if _, err := c.Call(context.Background(), name, nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	if d := snapshotDetail(t, c, "quotes"); d != "1 tool(s)" {
		t.Fatalf("detail after success = %q, want %q", d, "1 tool(s)")
	}
}

// A failure that is not about credentials leaves the status line alone.
func TestCall_OtherFailuresDoNotFlagCredential(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	fake.handle("tools/call", func(json.RawMessage) (json.RawMessage, *rpcError) {
		return json.RawMessage(`{"content":[{"type":"text","text":"ticker XYZ not found"}],"isError":true}`), nil
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := newTestClient(t, "quotes", srv)
	_, _ = c.Call(context.Background(), FullNamePrefix+"quotes__echo", nil)
	if d := snapshotDetail(t, c, "quotes"); d != "1 tool(s)" {
		t.Fatalf("detail = %q, want unchanged", d)
	}
}

func TestLooksLikeAuthFailure(t *testing.T) {
	for _, yes := range []string{
		`http 401: {"Error":"Missing API key"}`, "HTTP 403 Forbidden", "Unauthorized", "unauthorised",
		"Missing API key. Use Authorization: Bearer", "Invalid or expired access token.", "authentication failed",
	} {
		if !looksLikeAuthFailure(yes) {
			t.Errorf("%q should look like an auth failure", yes)
		}
	}
	for _, no := range []string{"ticker XYZ not found", "http 500: boom", "rate limited", "http 404: not found"} {
		if looksLikeAuthFailure(no) {
			t.Errorf("%q should not look like an auth failure", no)
		}
	}
}

// #164 — overlapping AddServer calls for the same id must leave exactly one
// entry. The slow initialize is what let the old code interleave.
func TestAddServer_ConcurrentSameIDYieldsOneEntry(t *testing.T) {
	fake := newFakeServer(t)
	fake.standardTools()
	fake.handle("initialize", func(json.RawMessage) (json.RawMessage, *rpcError) {
		time.Sleep(60 * time.Millisecond)
		return json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"1.0"}}`), nil
	})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c := New(Config{}, zap.NewNop())
	t.Cleanup(func() { _ = c.Close() })
	cfg := ServerConfig{Transport: "http", URL: srv.URL}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.AddServer("fetch", cfg)
		}()
	}
	wg.Wait()

	n := 0
	for _, s := range c.ServersSnapshot() {
		if s.ID == "fetch" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("server %q listed %d times, want exactly 1", "fetch", n)
	}
}

// put() is the last line of defence: even if an entry with the id is already
// present, a second one is never appended.
func TestPut_ReplacesExistingID(t *testing.T) {
	c := New(Config{}, zap.NewNop())
	c.put(&server{id: "a", detail: "old"})
	c.put(&server{id: "a", detail: "new"})
	snap := c.ServersSnapshot()
	if len(snap) != 1 || snap[0].Detail != "new" {
		t.Fatalf("snapshot = %+v, want one entry with detail=new", snap)
	}
}

var _ = io.Discard
