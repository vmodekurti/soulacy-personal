// Package process implements executor.Backend by spawning one python3 child
// process per tool call. This is the default backend — zero setup, simple,
// and compatible with any Python environment.
//
// Each Run() call:
//  1. Writes inline source to a temp file (or resolves pyFile on disk).
//  2. Builds a bootstrap script that imports the tool and calls funcName.
//  3. Launches python3 with argsJSON on stdin and collects stdout.
//
// The per-call overhead is the Python interpreter cold-start (~100–300 ms).
// For latency-sensitive workloads, see internal/executor/pool which eliminates
// this by keeping N Python workers alive across calls.
package process

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/message"
)

// compile-time interface check
var _ executor.Backend = (*Executor)(nil)

// Executor is the process-per-call backend.
type Executor struct {
	pythonBin  string // path to python3 interpreter (e.g. "python3")
	onProgress func(message.ProgressEvent)
}

// New creates an Executor using the given python interpreter binary.
// pythonBin defaults to "python3" when empty.
func New(pythonBin string) *Executor {
	if pythonBin == "" {
		pythonBin = "python3"
	}
	return &Executor{pythonBin: pythonBin}
}

// SetOnProgress installs a callback that is invoked for each progress event
// parsed from stdout during a Run. Pass nil to disable. Safe to call before
// any Run calls.
func (e *Executor) SetOnProgress(fn func(message.ProgressEvent)) {
	e.onProgress = fn
}

// Run executes a Python function and returns its stdout.
//
// If inline is non-empty it is used as the Python source directly.
// Otherwise pyFile is loaded via importlib and funcName is called with the
// keyword arguments decoded from argsJSON.
//
// The bootstrap redirects print() to stderr so any debug output from tool
// code does not corrupt the JSON result written to stdout at the end.
func (e *Executor) Run(ctx context.Context, pyFile, funcName, inline string, argsJSON []byte) (string, error) {
	script, err := BuildScript(pyFile, funcName, inline)
	if err != nil {
		return "", err
	}

	if argsJSON == nil {
		argsJSON = []byte("{}")
	}

	runID := uuid.New().String()

	cmd := exec.CommandContext(ctx, e.pythonBin, "-c", script)
	cmd.Stdin = bytes.NewReader(argsJSON)
	// SEC-5: scrub the environment to the base allowlist so tool code spawned
	// via this backend cannot read gateway secrets (ANTHROPIC_API_KEY, …).
	cmd.Env = sandbox.FilteredEnviron(nil)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("python tool: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("python tool: start: %w", err)
	}

	// Scan stdout line-by-line: progress lines are parsed and forwarded to the
	// optional callback; all other lines are accumulated as the tool output.
	var outputLines []string
	sc := bufio.NewScanner(stdoutPipe)
	// Inline Python commonly returns a single JSON line. The Scanner default is
	// only 64 KiB, which used to discard a perfectly valid large result (for
	// example a curated article pack) and quietly return an empty string. Keep a
	// bounded but practical ceiling and surface overflow as an execution error.
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if ev, ok := executor.ParseProgressLine(line, runID); ok {
			if e.onProgress != nil {
				e.onProgress(ev)
			}
			continue
		}
		outputLines = append(outputLines, line)
	}
	if scanErr := sc.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("python tool: read stdout: %w", scanErr)
	}

	if runErr := cmd.Wait(); runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = runErr.Error()
		}
		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}
		return "", fmt.Errorf("python tool: %w\n%s", runErr, msg)
	}
	return strings.TrimSpace(strings.Join(outputLines, "\n")), nil
}

// Close is a no-op (no persistent state to clean up).
func (e *Executor) Close() error { return nil }

// ---------------------------------------------------------------------------
// Script builder
// ---------------------------------------------------------------------------

// buildScript constructs the Python bootstrap string that wraps the tool
// function. It matches the logic in engine.go's runTool so callers get
// identical behaviour regardless of which executor backend is active.
func BuildScript(pyFile, funcName, inline string) (string, error) {
	if inline != "" {
		// Studio "Custom Python" node: the inline source defines `funcName`
		// (e.g. `run`). Wrap it with a harness that reads the JSON args from
		// stdin, passes them as a single positional arg, and prints the
		// JSON-encoded return value. stdout is quarantined to stderr while the
		// user code loads so stray prints don't corrupt the result (mirrors the
		// file harness below). With no funcName the inline code runs as-is.
		if funcName == "" {
			return inline, nil
		}
		return fmt.Sprintf(`
import sys as _sys, json
_orig_stdout = _sys.stdout
_sys.stdout = _sys.stderr
# Tolerate JSON/JS literals that models sometimes emit into Python code
# (null/true/false) by aliasing them to their Python equivalents. Correct code
# using None/True/False is unaffected; user reassignment still wins.
null = None
true = True
false = False
%s
_raw = _sys.stdin.read()
try:
    _args = json.loads(_raw) if _raw.strip() else {}
except Exception:
    _args = {"input": _raw}
_result = %s(_args)
_sys.stdout = _orig_stdout
print(_result if isinstance(_result, str) else json.dumps(_result))
`, inline, funcName), nil
	}
	if pyFile == "" {
		return "", fmt.Errorf("executor/process: both pyFile and inline are empty")
	}

	// Expand "~/" so importlib can find the file.
	if strings.HasPrefix(pyFile, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			pyFile = home + pyFile[1:]
		}
	}

	if funcName == "" {
		// Script-style: just exec the file without calling a named function.
		return fmt.Sprintf(`
import sys as _sys, json, importlib.util
_sys.stdout = _sys.stderr
exec(open(%q).read())
`, pyFile), nil
	}

	return fmt.Sprintf(`
import sys as _sys, json, importlib.util
_orig_stdout = _sys.stdout
_sys.stdout = _sys.stderr
args = json.loads(_sys.stdin.read())
spec = importlib.util.spec_from_file_location("tool", %q)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
result = getattr(mod, %q)(**args if isinstance(args, dict) else {})
_sys.stdout = _orig_stdout
print(result if isinstance(result, str) else json.dumps(result))
`, pyFile, funcName), nil
}
