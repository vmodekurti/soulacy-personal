// exec_workdir_guard_test.go — every subprocess this package spawns has a
// deliberate working directory.
//
// WHY A SOURCE GUARD AND NOT A BEHAVIOUR TEST. The behaviour test next door
// (TestNoisyNeighbourCannotCollideInAToolSubprocess) asserts that toolWorkDir
// returns two different directories for two tenants. Deleting the cmd.Dir
// assignment leaves that test passing: the helper still answers correctly and
// nothing reads the answer. That is the exact failure shape the handoff
// records — "testing a helper directly rather than the production call site" —
// and the only thing that catches it is reading the call site.
//
// An unset cmd.Dir is not a visible defect. The process starts, the tool
// works, and the file lands in whatever directory the gateway was launched
// from. It becomes a cross-tenant bug only when a second tenant runs a tool
// that touches the same relative path, which is not a scenario anybody
// reproduces while writing a tool.
package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// execWithoutDir names the spawn sites that legitimately have no working
// directory, and why. The reason is the deliverable: "it does not need one" is
// a claim, and a claim somebody has to have made on purpose.
var execWithoutDir = map[string]string{
	// Reads the host user's name for a diagnostic. Runs no user code and
	// touches no path.
	"engine_tools_misc.go:id": "runs `id -un` for a diagnostic; opens no file",
	// Delegates to the sy CLI, which resolves its own destination from the
	// package source and the workspace. A cmd.Dir here would be a second,
	// silently-disagreeing opinion about where an install lands.
	"engine_tools_shell.go:syPath": "sy resolves its own install destination; a Dir here would be a second opinion",
	// The docker preflight version check.
	"privileged_isolation.go:binary": "docker version probe; runs nothing of the caller's",
	// The docker run itself: the container's working directory is set with
	// -w inside the argv, not on the host process, and the host cmd.Dir
	// would have no effect on what the container sees.
	"privileged_isolation.go:binary2": "container working directory is passed as -w in argv, not on the host command",
}

func TestEverySubprocessStartsInADeliberateDirectory(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
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
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			spawns, target := execSpawns(fn.Body)
			if len(spawns) == 0 {
				continue
			}
			checked += len(spawns)
			if assignsDir(fn.Body) {
				continue
			}
			key := name + ":" + target
			if _, allowed := execWithoutDir[key]; allowed {
				continue
			}
			if _, allowed := execWithoutDir[key+"2"]; allowed {
				continue
			}
			t.Errorf("%s in %s spawns a subprocess without setting cmd.Dir — it inherits the gateway's "+
				"working directory, which is one directory for every tenant. Set it from toolWorkDir, or "+
				"add %q to execWithoutDir with the reason it needs none", fn.Name.Name, name, key)
		}
	}
	if checked == 0 {
		t.Fatal("no exec.Command calls found; this guard matches nothing and would pass for any code")
	}
}

// execSpawns returns the exec.Command/CommandContext calls in a body, plus a
// short label drawn from the first meaningful argument so the allowlist keys
// read as something rather than as a line number that moves.
func execSpawns(body *ast.BlockStmt) ([]*ast.CallExpr, string) {
	var found []*ast.CallExpr
	label := ""
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" {
			return true
		}
		if sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" {
			return true
		}
		found = append(found, call)
		if label == "" {
			label = firstArgLabel(call, sel.Sel.Name == "CommandContext")
		}
		return true
	})
	return found, label
}

func firstArgLabel(call *ast.CallExpr, skipCtx bool) string {
	args := call.Args
	if skipCtx && len(args) > 0 {
		args = args[1:]
	}
	if len(args) == 0 {
		return "?"
	}
	switch typed := args[0].(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.BasicLit:
		return strings.Trim(typed.Value, `"`)
	case *ast.IndexExpr:
		if ident, ok := typed.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return "?"
}

// assignsDir reports whether the body assigns to any x.Dir field.
func assignsDir(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Dir" {
				found = true
			}
		}
		return true
	})
	return found
}
