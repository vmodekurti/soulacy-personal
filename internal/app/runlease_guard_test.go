// runlease_guard_test.go — claiming a run and holding its lease are one act.
//
// The behaviour tests next door prove holdRunLease renews and releases. They
// all pass on a build where the production path claims a run and never calls
// it: the lease is taken once, lapses sixty seconds later, and the next
// replica to sweep recovers a run that is still executing. That is the
// original bug restored, with every unit test green.
//
// It also passes on a build that claims ANONYMOUSLY. An empty owner cannot
// renew — RenewLease refuses it, deliberately, because two anonymous workers
// are indistinguishable — so the run is abandoned the moment its first lease
// lapses, and it is abandoned *while running*.
//
// Both are call-site properties, so this reads the call site.
package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestEveryRunClaimAlsoHoldsItsLease(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	claimSites := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "beginRun" {
				continue
			}
			claim := findCall(fn.Body, "beginRun")
			if claim == nil {
				continue
			}
			claimSites++

			if findCall(fn.Body, "holdRunLease") == nil {
				t.Errorf("%s in %s claims a run but never holds its lease — the claim expires while "+
					"the run is still executing, and the next replica to sweep recovers work in "+
					"flight", fn.Name.Name, name)
			}
			// The owner is beginRun's fifth argument.
			if len(claim.Args) >= 5 {
				if lit, ok := claim.Args[4].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if strings.Trim(lit.Value, `"`) == "" {
						t.Errorf("%s in %s claims a run anonymously — an empty owner cannot renew, "+
							"so the run is abandoned while running", fn.Name.Name, name)
					}
				}
			}
		}
	}
	if claimSites == 0 {
		t.Fatal("no beginRun call sites found; this guard matches nothing and would pass for any code")
	}
}

func findCall(body *ast.BlockStmt, name string) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(body, func(node ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			found = call
		}
		return true
	})
	return found
}
