package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The behaviour lives in internal/runs and internal/approvals and is tested
// there. What cannot be tested there is that boot WIRES the two together — and
// a build where it does not is exactly the original bug: both sweeps run, both
// succeed, and runs are left paused on approvals that no longer exist.
//
// Order matters as much as presence. The pending runs must be read BEFORE the
// invalidation, because afterwards nothing records which runs those were.
func TestBootReadsTheBlockedRunsBeforeItInvalidatesTheirApprovals(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wire.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var listPos, invalidatePos, resolvePos token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "PendingRunRefs":
			listPos = call.Pos()
		case "InvalidateAllPending":
			invalidatePos = call.Pos()
		case "resolveOrphanedPauses":
			resolvePos = call.Pos()
		}
		return true
	})

	if invalidatePos == token.NoPos {
		t.Fatal("boot no longer invalidates stale approvals; this guard vouches for nothing")
	}
	if listPos == token.NoPos {
		t.Fatal("boot invalidates every pending approval and never lists the runs they were " +
			"blocking, so the runs stay paused on decisions nobody can make")
	}
	if resolvePos == token.NoPos {
		t.Fatal("boot lists the blocked runs and never resolves them")
	}
	if listPos > invalidatePos {
		t.Fatal("the blocked runs are read after the invalidation, by which point every approval " +
			"is already closed and the list is empty")
	}
	if resolvePos < invalidatePos {
		t.Fatal("the runs are resolved before their approvals are invalidated, so a run released " +
			"in between is moved out from under a decision somebody actually made")
	}
}
