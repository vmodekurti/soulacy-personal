// Package pool implements executor.Backend with a pre-forked Python worker pool.
//
// Instead of paying the Python interpreter cold-start cost (~100–300 ms) on
// every tool call, the pool starts N long-lived Python processes at startup.
// Each worker runs an embedded IPC loop that reads newline-delimited JSON
// requests from stdin and writes JSON responses to stdout. Workers are held
// in a buffered channel; a caller acquires one, sends its request, reads the
// response, and returns the worker to the pool. If a worker crashes (non-zero
// exit) it is automatically replaced by a fresh process so the pool never
// shrinks below the configured size.
//
// Protocol (newline-delimited JSON):
//
//	Request  → {"pyFile":"…","funcName":"…","inline":"…","argsJSON":"…"}
//	Response → {"ok":true,"output":"…"}
//	         | {"ok":false,"error":"…"}
//
// The argsJSON field is a JSON-encoded string (double-encoded) so the worker
// can forward it directly to json.loads() without re-serialising it. "inline"
// takes precedence over pyFile when both are set.
//
// Latency characteristics (measured on an M2 MacBook):
//
//	process backend: ~180 ms first call (interpreter start)
//	pool backend:     ~3 ms first call (IPC round-trip only)
package pool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/message"
)

// compile-time interface check
var _ executor.Backend = (*Pool)(nil)

// ---------------------------------------------------------------------------
// Embedded Python IPC shim
// ---------------------------------------------------------------------------

// workerShim is the Python source code injected into each worker process.
// It reads newline-delimited JSON requests from stdin and writes JSON
// responses to stdout. Each request is handled synchronously — the Go side
// serialises calls per-worker so there's no concurrent access to a single
// Python interpreter.
const workerShim = `
import sys, json, importlib.util, traceback, os, inspect

# Silence normal print() so tool code doesn't pollute the IPC channel.
# Tool code that explicitly writes to sys.stdout still works because we
# restore it per-request around the user function call.
_real_stdout = sys.stdout
sys.stdout = sys.stderr

def _invoke(fn, args):
    # Call convention MUST match the process backend and the documented
    # signature 'def run(inputs):' — pass the whole dict as ONE positional arg.
    # Fall back to keyword spread ONLY for functions that declare **kwargs (the
    # deployed-file 'def run(**args):' style), so both conventions work under
    # either executor. Previously this backend always spread (**args), which
    # raised TypeError for the standard 'def run(inputs):' Studio node.
    try:
        params = list(inspect.signature(fn).parameters.values())
    except (TypeError, ValueError):
        params = []
    positional = [p for p in params
                  if p.kind in (p.POSITIONAL_ONLY, p.POSITIONAL_OR_KEYWORD)]
    has_var_positional = any(p.kind == p.VAR_POSITIONAL for p in params)
    has_var_keyword = any(p.kind == p.VAR_KEYWORD for p in params)
    if positional or has_var_positional:
        return fn(args)
    if isinstance(args, dict) and has_var_keyword:
        return fn(**args)
    return fn(args)

def _run_request(req):
    inline   = req.get("inline", "")
    py_file  = req.get("pyFile",  "")
    func_name = req.get("funcName", "")
    args_json = req.get("argsJSON", "{}")
    try:
        args = json.loads(args_json)
    except Exception:
        # A non-JSON rendered input (e.g. a plain templated string) is delivered
        # under 'input' rather than silently dropped (mirrors the process backend).
        args = {"input": args_json}

    if inline:
        # Execute inline source in a fresh namespace.
        ns = {}
        exec(compile(inline, "<inline>", "exec"), ns)
        if func_name and func_name in ns:
            result = _invoke(ns[func_name], args)
        else:
            result = ns.get("__result__", "")
    elif py_file:
        if py_file.startswith("~/"):
            py_file = os.path.expanduser(py_file)
        spec = importlib.util.spec_from_file_location("tool", py_file)
        mod  = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        if func_name:
            fn     = getattr(mod, func_name)
            result = _invoke(fn, args)
        else:
            result = ""
    else:
        return {"ok": False, "error": "neither pyFile nor inline provided"}

    # Restore stdout just long enough to write the result.
    output = result if isinstance(result, str) else json.dumps(result)
    return {"ok": True, "output": output}

for raw_line in sys.stdin:
    raw_line = raw_line.strip()
    if not raw_line:
        continue
    try:
        req  = json.loads(raw_line)
        resp = _run_request(req)
    except Exception:
        resp = {"ok": False, "error": traceback.format_exc()}
    _real_stdout.write(json.dumps(resp) + "\n")
    _real_stdout.flush()
`

// ---------------------------------------------------------------------------
// worker
// ---------------------------------------------------------------------------

// worker is one long-lived Python process connected via stdin/stdout pipes.
type worker struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	ioMu     sync.Mutex // held while Scanner may be reading an os/exec pipe
	killOnce sync.Once
}

// send serialises req, writes it to the worker, and waits for a response.
// Returns an error on I/O failure or when the Python code reports an error.
func (w *worker) send(req map[string]string) (string, error) {
	w.ioMu.Lock()
	defer w.ioMu.Unlock()
	line, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(w.stdin, "%s\n", line); err != nil {
		return "", fmt.Errorf("worker write: %w", err)
	}

	if !w.stdout.Scan() {
		if err := w.stdout.Err(); err != nil {
			return "", fmt.Errorf("worker read: %w", err)
		}
		return "", fmt.Errorf("worker exited unexpectedly")
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(w.stdout.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("worker response decode: %w", err)
	}
	if !resp.OK {
		return "", fmt.Errorf("python error: %s", resp.Error)
	}
	return resp.Output, nil
}

// kill terminates the worker process.
func (w *worker) kill() {
	w.killOnce.Do(func() {
		_ = w.stdin.Close()
		if w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
		}
		// os/exec closes StdoutPipe inside Wait. Wait must therefore happen
		// only after the goroutine in send has returned from Scanner.Scan;
		// otherwise a timeout can turn a normal cancellation into a spurious
		// "file already closed" worker failure. Killing the process above
		// unblocks Scan, and ioMu is the explicit reader-completion barrier.
		w.ioMu.Lock()
		w.ioMu.Unlock()
		_ = w.cmd.Wait()
	})
}

// ---------------------------------------------------------------------------
// Pool
// ---------------------------------------------------------------------------

// Pool manages a fixed set of pre-forked Python worker processes.
type Pool struct {
	pythonBin  string
	size       int
	workers    chan *worker // buffered channel of idle workers
	onProgress func(message.ProgressEvent)

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeCh   chan struct{} // closed by Close(); interrupts backoff sleeps
}

// New creates a Pool with `size` pre-forked Python workers.
// Workers are started eagerly; the pool is ready to serve calls immediately.
// pythonBin defaults to "python3" when empty.
func New(pythonBin string, size int) (*Pool, error) {
	if pythonBin == "" {
		pythonBin = "python3"
	}
	if size <= 0 {
		size = 4
	}
	p := &Pool{
		pythonBin: pythonBin,
		size:      size,
		workers:   make(chan *worker, size),
		closeCh:   make(chan struct{}),
	}
	for i := 0; i < size; i++ {
		w, err := p.spawnWorker()
		if err != nil {
			// Best-effort cleanup of any already-started workers.
			close(p.workers)
			for w := range p.workers {
				w.kill()
			}
			return nil, fmt.Errorf("executor/pool: spawn worker %d: %w", i, err)
		}
		p.workers <- w
	}
	return p, nil
}

// spawnWorker starts one Python process running the embedded IPC shim.
func (p *Pool) spawnWorker() (*worker, error) {
	// Pass the shim as a -c argument — no temp file required, no file-system
	// race between writing and the interpreter opening it.
	cmd := exec.Command(p.pythonBin, "-c", workerShim)
	// SEC-5: workers must not inherit gateway secrets. Scrub to base allowlist.
	// (Pool workers are agent-agnostic — per-agent env passthrough is applied
	// on the engine's direct-exec path, not the shared worker pool.)
	cmd.Env = sandbox.FilteredEnviron(nil)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		return nil, err
	}
	// Stderr goes to the Go process's stderr so crash traces are visible.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		stdoutPipe.Close()
		return nil, err
	}

	sc := bufio.NewScanner(stdoutPipe)
	sc.Buffer(make([]byte, 1<<20), 1<<20) // 1 MiB per line

	return &worker{cmd: cmd, stdin: stdinPipe, stdout: sc}, nil
}

// SetOnProgress installs a callback invoked for each progress event parsed from
// the tool output. Pass nil to disable. Safe to call before any Run calls.
func (p *Pool) SetOnProgress(fn func(message.ProgressEvent)) {
	p.onProgress = fn
}

// Run acquires an idle worker, sends the request, and returns the response.
// If the context deadline is exceeded while waiting for a worker, or during
// the IPC round-trip, an error is returned immediately.
//
// When a worker crashes (IPC error), it is discarded and replaced by a fresh
// process so the pool self-heals without operator intervention.
func (p *Pool) Run(ctx context.Context, pyFile, funcName, inline string, argsJSON []byte) (string, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", fmt.Errorf("executor/pool: pool is closed")
	}
	p.mu.Unlock()

	runID := uuid.New().String()

	// Acquire an idle worker (or wait for one to become available).
	var w *worker
	select {
	case w = <-p.workers:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	if argsJSON == nil {
		argsJSON = []byte("{}")
	}

	req := map[string]string{
		"pyFile":   pyFile,
		"funcName": funcName,
		"inline":   inline,
		"argsJSON": string(argsJSON),
	}

	// Apply context deadline to the IPC call via a timeout goroutine.
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := w.send(req)
		done <- result{out, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			// Worker may be in a bad state — replace it.
			w.kill()
			p.replaceWorker()
			return "", res.err
		}
		// Return healthy worker to the pool.
		p.workers <- w
		// Parse and strip progress lines from the output before returning.
		out := p.extractProgress(res.out, runID)
		return out, nil
	case <-ctx.Done():
		// Kill the worker so it doesn't process stale data.
		w.kill()
		p.replaceWorker()
		return "", ctx.Err()
	}
}

// extractProgress scans multi-line output for progress events, fires the
// onProgress callback for each one found, and returns the output with those
// lines removed.
func (p *Pool) extractProgress(out, runID string) string {
	if p.onProgress == nil || !strings.Contains(out, `"progress"`) {
		return out
	}
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if ev, ok := executor.ParseProgressLine(line, runID); ok {
			p.onProgress(ev)
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// replaceWorker spawns a fresh worker and returns it to the pool in a goroutine,
// so the calling goroutine isn't blocked waiting for the interpreter to start.
func (p *Pool) replaceWorker() {
	go func() {
		// Retry a few times in case Python temporarily isn't available (e.g.
		// the interpreter is being updated by a package manager).
		for attempt := 0; attempt < 5; attempt++ {
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				return
			}
			p.mu.Unlock()

			w, err := p.spawnWorker()
			if err == nil {
				p.workers <- w
				return
			}
			// Interruptible backoff: bail immediately if the pool is closing.
			select {
			case <-p.closeCh:
				return
			case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
			}
		}
	}()
}

// Close shuts down all workers and marks the pool as closed. Idempotent.
func (p *Pool) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		close(p.closeCh) // wake any in-flight replaceWorker backoff

		// Drain and kill all idle workers.
		for {
			select {
			case w := <-p.workers:
				w.kill()
			default:
				return
			}
		}
	})
	return nil
}
