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

// spawningTx stands in for an MCP server that starts a long-lived child during
// a tool call — a browser server being the case that matters.
type spawningTx struct{ exited chan struct{} }

func (s *spawningTx) request(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		_ = cmd.Wait()
		close(s.exited)
	}()
	return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
}

func (s *spawningTx) notify(string, any) error { return nil }
func (s *spawningTx) close() error             { return nil }

// The janitor walks down from this PID, so the test process stands in for the
// server process and the sleep above for the browser it launched.
func (s *spawningTx) processRootPID() int { return os.Getpid() }

func callOnce(t *testing.T, keeps bool) *spawningTx {
	t.Helper()
	tx := &spawningTx{exited: make(chan struct{})}
	c := &Client{
		log: zap.NewNop(),
		servers: []*server{{
			id:        "probe",
			cfg:       ServerConfig{Transport: "stdio", KeepsProcesses: keeps},
			tx:        tx,
			connected: true,
		}},
	}
	if _, err := c.Call(context.Background(), "mcp__probe__do_something", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	return tx
}

// The default: a process a tool leaves behind is a leak, and gets cleaned up.
func TestJanitorReapsChildrenByDefault(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the janitor only runs where it can walk the process tree")
	}
	tx := callOnce(t, false)
	select {
	case <-tx.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a child left behind by a tool call should have been cleaned up")
	}
}

// The exception: a server that says its children are its state keeps them.
//
// Without this, a browser server loses its page one second after navigating to
// it, and the next call returns about:blank with nothing in the reply to
// explain where the page went — which reads as the tool being broken.
func TestJanitorLeavesChildrenAloneWhenTheServerKeepsThem(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the janitor only runs where it can walk the process tree")
	}
	tx := callOnce(t, true)
	select {
	case <-tx.exited:
		t.Fatal("the child was killed even though the server keeps its processes")
	case <-time.After(3 * time.Second):
		// Still running, which is the point.
	}
}
