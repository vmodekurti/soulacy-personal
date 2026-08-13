package pool

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	bin, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	p, err := New(bin, 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestWorkerKillWaitsForPipeReader(t *testing.T) {
	p := newTestPool(t)
	w := <-p.workers
	result := make(chan error, 1)
	go func() {
		_, err := w.send(map[string]string{
			"inline":   "import time\ndef run(inputs):\n    time.sleep(0.05)\n    return 'x' * 900000\n",
			"funcName": "run",
			"argsJSON": "{}",
		})
		result <- err
	}()
	// Ensure send entered its pipe read before cancellation.
	time.Sleep(10 * time.Millisecond)
	killed := make(chan struct{})
	go func() {
		w.kill()
		close(killed)
	}()
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("worker kill deadlocked waiting for stdout reader")
	}
	select {
	case err := <-result:
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "file already closed") {
			t.Fatalf("Wait closed stdout while reader was active: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stdout reader did not finish before kill returned")
	}
}

// Regression: the documented inline signature `def run(inputs):` (a single
// positional dict) must work under the POOL backend, which previously called
// run(**args) and raised TypeError. This is the pool-vs-process mismatch fix.
func TestPool_InlineRunInputsPositional(t *testing.T) {
	p := newTestPool(t)
	code := "def run(inputs):\n    return {'echoed': inputs.get('name', '')}\n"
	out, err := p.Run(context.Background(), "", "run", code, []byte(`{"name":"vasu"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, `"echoed":"vasu"`) && !strings.Contains(out, `"echoed": "vasu"`) {
		t.Fatalf("unexpected output: %q", out)
	}
}

// The keyword-spread style `def run(**args):` must also still work (deployed
// file/tool convention) — the adaptive dispatch handles both.
func TestPool_InlineRunKwargs(t *testing.T) {
	p := newTestPool(t)
	code := "def run(**args):\n    return {'n': args.get('name', '')}\n"
	out, err := p.Run(context.Background(), "", "run", code, []byte(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, `"n":"x"`) && !strings.Contains(out, `"n": "x"`) {
		t.Fatalf("unexpected output: %q", out)
	}
}

// A non-JSON rendered input is delivered under "input" rather than dropped.
func TestPool_NonJSONInputUnderInputKey(t *testing.T) {
	p := newTestPool(t)
	code := "def run(inputs):\n    return inputs.get('input', 'MISSING')\n"
	out, err := p.Run(context.Background(), "", "run", code, []byte(`hello world`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("non-JSON input not delivered under 'input': %q", out)
	}
}
