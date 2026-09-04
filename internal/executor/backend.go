// Package executor defines the provider-agnostic interface for running Python
// tools from within agent turns.
//
// Two implementations ship out of the box:
//
//	internal/executor/process — one python3 child process per call (simple, default)
//	internal/executor/pool    — N pre-forked persistent Python workers (low latency)
//
// Active backend is selected at startup from config.yaml:
//
//	runtime:
//	  executor: process   # or "pool"
//	  executor_workers: 4 # pool only — number of pre-forked workers
//
// The engine and all built-in tool handlers import only this package; they
// never reference a concrete implementation directly.
package executor

import "context"

// Backend is the interface satisfied by every Python executor implementation.
type Backend interface {
	// Run executes a Python function and returns its stdout as a string.
	//
	// Exactly one of pyFile or inline must be non-empty:
	//   - pyFile   is a path to a .py file to execute
	//   - inline   is a Python source string to execute (written to a temp file)
	//
	// funcName is the top-level function to call (may be empty for script-style
	// execution where the file has a top-level main() or __main__ block).
	//
	// argsJSON is a JSON-encoded positional argument array passed to funcName
	// (e.g. []byte(`["hello", 42]`)). Pass nil for no arguments.
	//
	// Returns the combined stdout of the Python invocation, or an error if the
	// process exits with a non-zero status or the context deadline is exceeded.
	Run(ctx context.Context, pyFile, funcName, inline string, argsJSON []byte) (string, error)

	// Close shuts down the backend gracefully (drains the worker pool, etc.).
	// Idempotent.
	Close() error
}
