// agent_write_guard_test.go — the structural half of MU-028 criterion 2:
// "updates with stale versions return 409 plus current metadata; they never
// silently overwrite."
//
// Per-handler tests prove the handlers that exist today are guarded. They
// cannot prove the next one will be, and the next one is the risk: a new
// agent-mutating handler is a dozen lines that compile, pass review, and lose
// somebody's work only when two people happen to edit at once. This guard
// fails the build when a function performs a durable agent write without
// either checking a precondition or being listed below with a reason.
package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// durableAgentWrites are the agentScope methods that change what is on disk.
//
// Register, Unregister and SetEnabledInMemory are deliberately absent: they
// touch only the in-memory registry for the lifetime of one run (a Studio "try
// this"), so there is no stored definition for them to overwrite.
var durableAgentWrites = map[string]bool{
	"Upsert": true, "Delete": true, "RestoreAgentVersion": true,
}

// concurrencyGuards are the calls that constitute checking. checkIfMatch is
// the shared policy; the two guards are the create/update-aware shells over
// it.
var concurrencyGuards = map[string]bool{
	"checkIfMatch": true, "guardAgentUpdate": true, "guardAgentCreate": true,
}

// unguardedAgentWrites are the functions that write an agent without a
// precondition, each with the reason it cannot lose someone's work. A new
// entry here is a claim a reviewer can check; the absence of an entry is the
// build failing.
var unguardedAgentWrites = map[string]string{
	// Allocates a free id before writing (uniqueAgentID), so the write can
	// never land on an existing definition.
	"handleCloneAgent": "writes only to an id it just proved was free",
	// Same shape: templates.Instantiate takes an availability predicate and
	// keeps incrementing until it finds an unused id. Its rollback Delete
	// removes the agent this same request created moments earlier.
	"handleInstantiateTemplate": "writes only to an id it just proved was free, and rolls back only its own create",
	// Skips every peer that already resolves, so it only ever materialises
	// agents that do not exist.
	"ensurePeerAgents": "creates only peers that do not already exist",
	// Read-modify-write of the LIVE definition rather than of a form the
	// client is holding: it loads the current agent and changes one boolean,
	// so there is no stale copy for it to write back. A concurrent edit
	// survives it.
	"setAgentEnabled": "mutates one field of the freshly loaded definition, never a client-supplied copy",
}

// agentScopeExpr reports whether an expression evaluates to an agentScope.
//
// Two shapes reach a scope: the request accessor (s.agents(c) and its
// deliberate siblings) and a parameter of type agentScope threaded into a
// helper. Both are matched syntactically because a type-checked pass would
// need the whole package built, and this guard has to run in the same cheap
// place the other AST guards on this branch do.
func agentScopeExpr(expr ast.Expr, scopeParams map[string]bool) bool {
	switch node := expr.(type) {
	case *ast.Ident:
		return scopeParams[node.Name]
	case *ast.CallExpr:
		sel, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		receiver, ok := sel.X.(*ast.Ident)
		if !ok || receiver.Name != "s" {
			return false
		}
		switch sel.Sel.Name {
		case "agents", "agentsForWorkspace", "agentsAcrossWorkspaces":
			return true
		}
	}
	return false
}

func TestEveryDurableAgentWriteChecksAPrecondition(t *testing.T) {
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
			if !ok || fn.Body == nil {
				continue
			}
			// A parameter typed agentScope is a scope by another name.
			scopeParams := map[string]bool{}
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "agentScope" {
						for _, paramName := range field.Names {
							scopeParams[paramName.Name] = true
						}
					}
				}
			}

			var writes []string
			guarded := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if concurrencyGuards[sel.Sel.Name] {
					guarded = true
					return true
				}
				if durableAgentWrites[sel.Sel.Name] && agentScopeExpr(sel.X, scopeParams) {
					writes = append(writes, fileSet.Position(call.Pos()).String()+" ."+sel.Sel.Name)
				}
				return true
			})

			if len(writes) == 0 || guarded {
				continue
			}
			if reason, allowed := unguardedAgentWrites[fn.Name.Name]; allowed {
				if strings.TrimSpace(reason) == "" {
					t.Errorf("%s is allowed to write an agent unguarded but gives no reason", fn.Name.Name)
				}
				continue
			}
			t.Errorf("%s writes an agent durably (%s) without checking a precondition — "+
				"call s.guardAgentUpdate/s.guardAgentCreate, or add it to unguardedAgentWrites with the reason it cannot lose work",
				fn.Name.Name, strings.Join(writes, ", "))
		}
	}
}

// TestTheUnguardedListDoesNotOutliveItsCallers keeps the allowlist from
// becoming a place names go to die. An entry naming a function that no longer
// writes an agent is a stale exemption, and the next function to take that
// name inherits it silently.
func TestTheUnguardedListDoesNotOutliveItsCallers(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	writers := map[string]bool{}
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
			if !ok || fn.Body == nil {
				continue
			}
			scopeParams := map[string]bool{}
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "agentScope" {
						for _, paramName := range field.Names {
							scopeParams[paramName.Name] = true
						}
					}
				}
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if durableAgentWrites[sel.Sel.Name] && agentScopeExpr(sel.X, scopeParams) {
					writers[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	for name := range unguardedAgentWrites {
		if !writers[name] {
			t.Errorf("unguardedAgentWrites names %s, which no longer performs a durable agent write — remove the exemption", name)
		}
	}
}
