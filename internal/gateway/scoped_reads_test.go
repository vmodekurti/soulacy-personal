// scoped_reads_test.go — the structural half of MU-019's last criterion:
// "pagination, filters, exports, and aggregate queries cannot omit the
// workspace predicate."
//
// Per-store isolation tests prove the queries that exist today are scoped.
// They cannot prove the *next* one will be. This guard works the other way
// round: it fails the build when a handler reaches a tenant-scoped store
// directly instead of through the request-scoped accessor, which is the only
// way a new read can quietly omit the predicate.
package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// tenantNeutralMethods are the operations on those stores that legitimately
// take no workspace. Append is a write and carries the tenant on the event
// itself; EventFilePath is a path helper; Close is lifecycle. Everything else
// is a read, and every read must name a tenant.
var tenantNeutralMethods = map[string]bool{
	"Append": true, "EventFilePath": true, "Close": true,
}

// directStoreFields are the server fields whose stores hold tenant data and
// whose scoped accessor takes the workspace for you.
var directStoreFields = map[string]string{
	"actions":   "s.actionLog(c) / s.actionLogForWorkspace(...)",
	"costStore": "s.costs(c) / s.costsForWorkspace(...)",
}

// scopedAccessors are the functions that legitimately touch those fields: the
// accessors themselves and the wiring that installs them.
var scopedAccessors = map[string]bool{
	"actionLog": true, "actionLogForWorkspace": true, "Available": true,
	"costs": true, "costsForWorkspace": true, "costWorkspace": true,
	"New": true, "NewServer": true, "buildApp": true,
	"SetActionLog": true, "SetCostStore": true, "SetHistoryStore": true,
}

func TestTenantStoresAreReachedThroughAScopedAccessor(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || scopedAccessors[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || tenantNeutralMethods[method.Sel.Name] {
					return true
				}
				field, ok := method.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				receiver, ok := field.X.(*ast.Ident)
				if !ok || receiver.Name != "s" {
					return true
				}
				hint, watched := directStoreFields[field.Sel.Name]
				if !watched {
					return true
				}
				t.Errorf("%s: %s calls s.%s.%s directly — use %s so the query cannot omit the workspace predicate",
					fileSet.Position(call.Pos()), fn.Name.Name, field.Sel.Name, method.Sel.Name, hint)
				return true
			})
		}
	}
}

// scopeResolvers are the helpers that turn a request into a tenant. A call on
// a shared, deployment-wide store must pass one of these rather than a literal
// or a value threaded in from somewhere the reader cannot see.
var scopeResolvers = map[string]bool{
	"dlqScope": true, "costWorkspace": true, "historyScope": true, "shareScope": true,
	"snapWorkspace": true,
}

// sharedStoreReads are methods on stores that stay a single deployment-wide
// handle — the tenant is an argument, not a different store — mapped to the
// resolver a handler should be passing.
var sharedStoreReads = map[string]map[string]bool{
	"dlqStore": {"List": true, "Get": true, "Delete": true},
}

// TestSharedStoreReadsNameATenant is the other half of the guard above.
//
// For the action log and the cost store the scoped accessor *is* the store, so
// touching the field at all is the violation. The dead-letter queue is one
// database for the whole deployment — there is no per-workspace handle to hand
// back — so the field access is legitimate and the tenant travels as an
// argument. That makes "did you pass a workspace" the thing to check, and a
// literal `""` in the workspace position is exactly the mistake this catches:
// it compiles, it reads plausibly, and it turns a scoped listing back into a
// deployment-wide one.
func TestSharedStoreReadsNameATenant(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || scopeResolvers[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				field, ok := method.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				receiver, ok := field.X.(*ast.Ident)
				if !ok || receiver.Name != "s" {
					return true
				}
				watched, ok := sharedStoreReads[field.Sel.Name]
				if !ok || !watched[method.Sel.Name] {
					return true
				}
				if !callPassesAScope(call) {
					t.Errorf("%s: %s calls s.%s.%s without a request scope — pass s.dlqScope(c) so the read cannot span tenants",
						fileSet.Position(call.Pos()), fn.Name.Name, field.Sel.Name, method.Sel.Name)
				}
				return true
			})
		}
	}
}

// callPassesAScope reports whether any argument is a call to one of the
// recognised scope resolvers.
func callPassesAScope(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		inner, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := inner.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if scopeResolvers[selector.Sel.Name] {
			return true
		}
	}
	return false
}
