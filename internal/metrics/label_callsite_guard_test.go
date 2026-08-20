// metric_label_guard_test.go — no identifier is passed straight to a metric.
//
// WHY THIS EXISTS ALONGSIDE THE BEHAVIOUR TESTS. metric_leak_test.go proves
// toolMetricLabel classifies correctly; internal/metrics proves ToolLabel
// does. Both pass on a build where a dispatcher writes
//
//	metrics.ToolCallsTotal.WithLabelValues(tc.Name, "error")
//
// which is the original bug verbatim. The classification being right is not
// the same as the call site using it, and only reading the call site tells
// them apart. This is the third time in this milestone that distinction has
// mattered — see also the cmd.Dir and pluginsChanged guards.
//
// The rule is syntactic and blunt on purpose: a metric label argument may not
// be a field read off a message, call, or definition. Those fields are exactly
// where names chosen outside this repository live — tc.Name, msg.AgentID,
// def.ID — and a label built from one is readable by every principal permitted
// to scrape, because the Prometheus exposition is a single global document.
//
// It walks the whole repository rather than one package. The labels belong to
// THIS package and the call sites are everywhere else, so a guard that only
// read its own directory would vouch for the one place with no call sites in
// it. internal/channels was the second package feeding an identifier in, and a
// runtime-only guard would never have looked at it.
package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// identifierFields are struct fields whose values originate outside this
// binary. Passing one to WithLabelValues puts it in the shared exposition.
var identifierFields = map[string]bool{
	"Name":        true, // tc.Name — a tool name, possibly mcp__<tenant server>__…
	"AgentID":     true, // an agent ID, i.e. prose somebody typed
	"ID":          true, // def.ID, run.ID, …
	"SessionID":   true,
	"WorkspaceID": true,
	"Subject":     true,
	"RunID":       true,
	"Channel":     true, // channel IDs are operator-configured, but see below
}

// labelExemptions are call sites where a field in the list above is
// legitimately a label, keyed by file:function, with the reason its
// cardinality is bounded.
//
// It is EMPTY, and that is the finding rather than an oversight: every call
// site that needed one of these values classifies it first — through
// toolMetricLabel in the runtime, through channelMetricLabel in the channels
// package. An empty map is the honest state, and adding a pre-written entry
// "in case" would be the pre-approved slot the metrics label guard next door
// exists to prevent.
var labelExemptions = map[string]string{}

func TestNoIdentifierIsPassedStraightToAMetricLabel(t *testing.T) {
	fset := token.NewFileSet()
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	checked := 0
	walkErr := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "gui", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		name := filepath.ToSlash(rel)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok || !isWithLabelValues(call) {
					return true
				}
				checked++
				for _, arg := range call.Args {
					sel, ok := arg.(*ast.SelectorExpr)
					if !ok || !identifierFields[sel.Sel.Name] {
						continue
					}
					key := name + ":" + fn.Name.Name
					if _, exempt := labelExemptions[key]; exempt {
						continue
					}
					t.Errorf("%s in %s passes .%s straight to a metric label — that value is chosen "+
						"outside this binary, and the Prometheus exposition is one global document "+
						"every permitted scraper reads in full. Classify it first (see "+
						"toolMetricLabel), or add %q to labelExemptions with its bound",
						fn.Name.Name, name, sel.Sel.Name, key)
				}
				return true
			})
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if checked == 0 {
		t.Fatal("no WithLabelValues calls found; the guard matches nothing and would pass for any code")
	}
}

// isWithLabelValues matches x.y.WithLabelValues(...) where the chain mentions
// the metrics package, so an unrelated method of the same name elsewhere does
// not trip the guard.
func isWithLabelValues(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WithLabelValues" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := inner.X.(*ast.Ident)
	return ok && pkg.Name == "metrics"
}
