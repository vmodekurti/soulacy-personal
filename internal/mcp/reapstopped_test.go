package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"go.uber.org/zap"
)

// stoppableTx stands in for a server that launched something long-lived —
// a browser — and is then stopped.
type stoppableTx struct {
	cmd    *exec.Cmd
	exited chan struct{}
	closed bool
}

func (s *stoppableTx) spawn() {
	s.exited = make(chan struct{})
	s.cmd = exec.Command("sleep", "30")
	_ = s.cmd.Start()
	go func() {
		_ = s.cmd.Wait()
		close(s.exited)
	}()
}

func (s *stoppableTx) request(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(`{"content":[]}`), nil
}
func (s *stoppableTx) notify(string, any) error { return nil }

// close stops the server but not what it started — which is exactly what
// killing an MCP server process does to the browser it launched.
func (s *stoppableTx) close() error { s.closed = true; return nil }

func (s *stoppableTx) processRootPID() int { return os.Getpid() }

func clientWith(tx transport, keeps bool) *Client {
	return &Client{
		log: zap.NewNop(),
		servers: []*server{{
			id:        "browser-ish",
			cfg:       ServerConfig{Transport: "stdio", KeepsProcesses: keeps},
			tx:        tx,
			connected: true,
		}},
	}
}

func skipWithoutProcessTree(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the reaper only runs where it can walk the process tree")
	}
}

// keeps_processes means "do not reap between calls" — it cannot mean "leak
// forever". When the server stops, nothing owns those processes and nothing
// will ever talk to them again.
func TestStoppingAServerReapsWhatItLeftRunning(t *testing.T) {
	skipWithoutProcessTree(t)
	tx := &stoppableTx{}
	tx.spawn()
	c := clientWith(tx, true)

	if err := c.RemoveServer("browser-ish"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	select {
	case <-tx.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the process outlived the server that started it")
	}
}

// Shutdown has the same problem, and it is the one that bit production: a
// gateway restart left browsers holding the runtime directory their
// replacements wanted, so the next start failed with "Target page, context or
// browser has been closed" on a fresh process.
func TestClosingTheClientReapsToo(t *testing.T) {
	skipWithoutProcessTree(t)
	tx := &stoppableTx{}
	tx.spawn()
	c := clientWith(tx, true)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-tx.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a browser survived the gateway shutting down")
	}
}

// The server is still stopped and removed even when it left nothing behind —
// the reaper must not become a condition of stopping.
func TestStoppingWorksWithNothingToReap(t *testing.T) {
	skipWithoutProcessTree(t)
	tx := &stoppableTx{exited: make(chan struct{})}
	c := clientWith(tx, false)

	if err := c.RemoveServer("browser-ish"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if !tx.closed {
		t.Error("the transport should have been closed")
	}
	if len(c.servers) != 0 {
		t.Error("the server should have been removed")
	}
}
