// revocation_test.go — MU-017 criterion 7: revocation prevents new invocations
// and terminates or drains existing processes according to documented policy.
package mcp

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// helperServer is a stdio MCP server that answers initialize/tools/list and
// blocks in tools/call until told otherwise, so a test can hold a call
// in flight while the server is revoked.
func helperServerScript(t *testing.T) (command string, args []string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	script := `
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}\n' "$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')" ;;
    *'"tools/list"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"slow","description":"blocks","inputSchema":{}}]}}\n' "$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')" ;;
    *'"tools/call"'*)
      sleep 30
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"done"}]}}\n' "$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')" ;;
  esac
done
`
	return "sh", []string{"-c", script}
}

// A revoked server is unreachable immediately: the removal takes effect before
// the shutdown, so a caller cannot slip a new invocation in while the process
// is still winding down.
func TestARevokedServerAcceptsNoNewInvocations(t *testing.T) {
	command, args := helperServerScript(t)
	client := New(Config{Servers: map[string]ServerConfig{
		"helper": {Transport: "stdio", Command: command, Args: args},
	}}, zap.NewNop())
	defer client.Close()

	if len(client.AllTools()) == 0 {
		t.Skip("helper server exposed no tools; the harness cannot exercise revocation")
	}

	if err := client.RemoveServer("helper"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Call(ctx, "mcp__helper__slow", nil); err == nil {
		t.Fatal("a revoked server still accepted an invocation")
	}
	if len(client.AllTools()) != 0 {
		t.Fatalf("a revoked server's tools are still advertised: %+v", client.AllTools())
	}
}

// Revocation must not be blocked by a tool that never returns. The drain is
// bounded: an extension that hangs would otherwise make revoking it
// impossible, which is the opposite of what revocation is for.
func TestRevocationCompletesEvenWhileACallIsStuck(t *testing.T) {
	command, args := helperServerScript(t)
	client := New(Config{Servers: map[string]ServerConfig{
		"helper": {Transport: "stdio", Command: command, Args: args},
	}}, zap.NewNop())
	defer client.Close()
	if len(client.AllTools()) == 0 {
		t.Skip("helper server exposed no tools")
	}

	restore := drainGrace
	drainGrace = 200 * time.Millisecond
	defer func() { drainGrace = restore }()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = client.Call(ctx, "mcp__helper__slow", nil)
	}()
	time.Sleep(300 * time.Millisecond) // let the call reach the server

	done := make(chan struct{})
	start := time.Now()
	go func() {
		_ = client.RemoveServer("helper")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("revocation never completed while a call was stuck")
	}
	// Bounded, not merely eventual. An unbounded drain also "completes" — as
	// soon as the stuck call hits its own context deadline — so the assertion
	// has to be on the clock. With a 200ms grace, anything approaching the
	// call's own timeout means revocation is waiting for the extension rather
	// than for its own budget.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("revocation took %s with a %s drain budget — the wait is unbounded", elapsed, drainGrace)
	}
	wg.Wait()
}

// The subprocess is reaped, not merely killed.
//
// close() used to call Kill and return without Wait, so every removed or
// hot-replaced server left a zombie until the gateway exited. Nothing visibly
// broke, which is why it survived — the process table just kept growing on a
// deployment that reconfigures servers.
//
// Driven against the transport rather than through the client so it is exact:
// cmd.ProcessState is non-nil only after a successful Wait, which is precisely
// the step that was missing.
func TestAClosedServerSubprocessIsReaped(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is unavailable")
	}
	tx, err := newStdio(ServerConfig{Command: "sleep", Args: []string{"600"}}, zap.NewNop())
	if err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	pid := tx.processRootPID()
	if pid <= 0 {
		t.Fatal("no pid for the started subprocess")
	}
	if tx.cmd.ProcessState != nil {
		t.Fatal("the subprocess exited before the test began")
	}

	start := time.Now()
	if err := tx.close(); err != nil {
		t.Fatal(err)
	}
	if tx.cmd.ProcessState == nil {
		t.Fatal("close returned without reaping the subprocess — it is a zombie until the gateway exits")
	}
	// A process that ignores stdin EOF is signalled rather than waited on
	// forever; sleep does exactly that, so this must not have taken the full
	// SIGKILL path's worth of grace periods.
	if elapsed := time.Since(start); elapsed > 3*shutdownGrace {
		t.Fatalf("shutdown took %s — the escalation is not bounded", elapsed)
	}
}
