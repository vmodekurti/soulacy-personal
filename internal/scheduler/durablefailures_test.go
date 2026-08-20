package scheduler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The store-level behaviour is tested in internal/schedules. What cannot be
// tested there is that the scheduler USES it — and a build where it does not
// is the original bug: each replica counts its own failures, the auto-disable
// limit is reached at best once per N replicas, and a chronically broken agent
// keeps firing forever.
//
// A source guard because reaching the durable path in a behaviour test needs a
// scheduler with a real store, a registered cron entry and a fired occurrence;
// a test that assembles all of that mostly proves the assembly.
func TestTheSchedulerCountsFailuresDurably(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "scheduler.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var record *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "recordFireResult" {
			record = fn
		}
	}
	if record == nil {
		t.Fatal("recordFireResult is gone; this guard vouches for nothing")
	}

	calls := map[string]bool{}
	ast.Inspect(record.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			calls[sel.Sel.Name] = true
		}
		return true
	})
	if !calls["RecordFailure"] {
		t.Error("a failed fire is not recorded durably; with more than one replica the " +
			"auto-disable limit is never reached and a broken agent fires forever")
	}
	if !calls["ClearFailures"] {
		t.Error("a successful fire does not clear the durable count, so an agent that fails " +
			"intermittently is eventually disabled for no good reason")
	}
	if !calls["applyFailureCount"] {
		t.Error("the durable path does not reach the shared quarantine decision, so the two " +
			"paths can disagree about when an agent gets switched off")
	}
}
