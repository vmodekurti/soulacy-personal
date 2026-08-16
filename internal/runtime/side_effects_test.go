// side_effects_test.go — MU-021 criterion 6, the engine half: the dispatch
// choke point reports outside-visible calls, and only those.
package runtime

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"
)

type recordingSideEffects struct {
	mu    sync.Mutex
	tools []string
}

func (r *recordingSideEffects) RecordSideEffect(_ context.Context, tool string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, tool)
	return nil
}

func (r *recordingSideEffects) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tools...)
}

// The classification the recorder relies on has to actually separate the two
// partitions. If a read-only tool were reported, every run would look
// unretryable and worker loss would always need a human; if a privileged one
// were missed, a run that shelled out would be re-executed.
func TestOnlyOutsideVisibleToolsCountAsSideEffects(t *testing.T) {
	for _, tool := range []string{"shell_exec", "run_script", "python_eval", "write_file",
		"download_file", "install_library", "package_install", "http_request",
		"mcp__server__do", "plugin__pkg__do"} {
		if !isSideEffectingTool(tool) {
			t.Errorf("%s is not counted as a side effect; a lost worker would repeat it", tool)
		}
	}
	for _, tool := range []string{"read_file", "list_dir", "find_files", "web_search", "kb_search"} {
		if isSideEffectingTool(tool) {
			t.Errorf("%s is counted as a side effect; every run that read a file would need review", tool)
		}
	}
}

// The report happens at the choke point, so a tool added tomorrow is covered
// without its author knowing the rule exists.
func TestDispatchReportsASideEffectingCall(t *testing.T) {
	e := newMinimalEngine(t)
	rec := &recordingSideEffects{}
	e.SetSideEffectRecorder(rec)
	e.recordSideEffect(context.Background(), "shell_exec")
	if got := rec.seen(); len(got) != 1 || got[0] != "shell_exec" {
		t.Fatalf("recorder saw %v", got)
	}
}

// A recording failure must not fail the tool. The conservative reading is
// already safe-ish and a database hiccup that stops all execution is not.
func TestARecorderFailureDoesNotStopTheTool(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSideEffectRecorder(failingSideEffects{})
	// recordSideEffect returns nothing: the contract is that it cannot abort
	// the call. A signature that returned an error would invite a caller to
	// propagate it, which is the failure mode this test pins shut.
	e.recordSideEffect(context.Background(), "shell_exec")
}

type failingSideEffects struct{}

func (failingSideEffects) RecordSideEffect(context.Context, string) error {
	return context.DeadlineExceeded
}

// No recorder is the ordinary personal-deployment state, not a degraded one.
func TestNoRecorderIsInert(t *testing.T) {
	e := newMinimalEngine(t)
	e.recordSideEffect(context.Background(), "shell_exec")
}

// TestDispatchStillReportsSideEffects is the structural half.
//
// The tests above prove the recorder works when it is called. They cannot
// prove dispatch still calls it: deleting the two lines in executeToolCall
// compiles, runs every tool exactly as before, and silently makes every lost
// run look retry-safe. There is no output to assert on — the whole failure is
// an absence — so the guard reads the source.
func TestDispatchStillReportsSideEffects(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "engine_tool_dispatch.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || method.Sel.Name != "recordSideEffect" {
			return true
		}
		found = true
		return false
	})
	if !found {
		t.Fatal("engine_tool_dispatch.go no longer reports side effects; a lost worker will re-execute runs that already acted")
	}
}
