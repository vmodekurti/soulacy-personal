// plugin_lifecycle_guard_test.go — a lifecycle decision must reach the loader.
//
// The behaviour tests for this live in internal/plugins and assert that
// Invalidate drops the cached loader. Every one of them passes on a build
// where the HANDLERS stopped calling it: the store still invalidates
// correctly, and nothing asks it to. That is the same failure shape as a
// helper that returns the right working directory nobody assigns.
//
// The consequence is specifically bad here because it is silent in the
// direction of MORE access. A revoke that does not reach the loader returns
// ok:true, deletes the files, and leaves the tool callable — the operator has
// every reason to believe the extension is gone.
package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// mutatingInstallerCalls change what a workspace has installed, so each must
// be followed by an invalidation. Discard is absent deliberately: it removes a
// STAGED directory, which the loader never scans, so there is nothing cached
// to drop.
var mutatingInstallerCalls = map[string]bool{
	"Approve":    true,
	"SetEnabled": true,
	"Reapprove":  true,
	"Remove":     true,
}

func TestEveryPluginLifecycleChangeInvalidatesTheLoader(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			mutator := installerMutation(fn.Body)
			if mutator == "" {
				return true
			}
			checked++
			if callsPluginsChanged(fn.Body) {
				return true
			}
			t.Errorf("%s in %s calls ins.%s but never s.pluginsChanged — the files change and the "+
				"loaded tool set does not, so the handler returns ok:true for a revocation that did "+
				"not revoke anything until the next restart", fn.Name.Name, name, mutator)
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no installer mutations found; the guard matches nothing and would pass for any code")
	}
}

func installerMutation(body *ast.BlockStmt) string {
	found := ""
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := sel.X.(*ast.Ident)
		// Matched on the receiver NAME rather than on its type: this is a
		// syntactic guard, and `ins` is the one name every handler here uses
		// for the installer. A handler that named it something else would slip
		// through, which is why the zero-match check above exists.
		if !ok || receiver.Name != "ins" {
			return true
		}
		if mutatingInstallerCalls[sel.Sel.Name] && found == "" {
			found = sel.Sel.Name
		}
		return true
	})
	return found
}

func callsPluginsChanged(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "pluginsChanged" {
			found = true
		}
		return true
	})
	return found
}
