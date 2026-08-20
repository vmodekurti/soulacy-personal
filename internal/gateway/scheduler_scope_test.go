// scheduler_scope_test.go — the structural guard for the scheduler boundary.
//
// MU-023 made every map in internal/scheduler composite-keyed and kept the
// workspace-free methods for single-tenant callers, with a comment saying they
// are the ones a multi-user caller should not reach for. All twenty-two of the
// gateway's scheduler calls reached for exactly those, so the fix inside the
// package was undone at its boundary and the collision it removed came back:
// one cron entry, one run lock, one failure counter shared by two tenants'
// same-named agents, with the later registration silently replacing the
// earlier one.
//
// Per-handler tests prove the calls that exist today are scoped. This fails the
// build when the next one is not.
package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// workspaceFreeSchedulerCalls are the Scheduler methods that resolve the
// workspace themselves, from the scheduler's single process-wide principal.
// Each maps to the scoped call that names the request's tenant instead.
var workspaceFreeSchedulerCalls = map[string]string{
	"RegisterAgent":         "s.schedules(c).Register(def)",
	"DeregisterAgent":       "s.schedules(c).Deregister(id)",
	"TryStartRun":           "s.schedules(c).TryStartRun(id)",
	"FinishRun":             "s.schedules(c).FinishRun(id)",
	"IsRunning":             "s.schedules(c).IsRunning(id)",
	"Entries":               "s.schedules(c).Entries()",
	"RunningSnapshot":       "s.schedules(c).RunningSnapshot()",
	"LastBackfillsSnapshot": "s.schedules(c).LastBackfills()",
	"LastBackfill":          "s.schedules(c).LastBackfills()",
	"BlocksSnapshot":        "s.schedules(c).Entries(), which carries the block on each entry",
	"LastBlock":             "s.schedules(c).Entries(), which carries the block on each entry",
}

// schedulerScopeAccessors are the functions allowed to touch s.scheduler
// directly: the accessors themselves, and wiring that installs or drives the
// scheduler as a whole rather than one tenant's schedule.
var schedulerScopeAccessors = map[string]string{
	"schedules":             "the accessor",
	"schedulesForWorkspace": "the accessor",
	"New":                   "wiring",
	"NewServer":             "wiring",
	"buildApp":              "wiring",
	"Start":                 "process lifecycle, not one tenant's schedule",
	"Stop":                  "process lifecycle, not one tenant's schedule",
}

// deploymentWideSchedulerMethods take no workspace because they are about the
// process rather than about a tenant's schedule.
var deploymentWideSchedulerMethods = map[string]bool{
	"Start": true, "Stop": true, "SetEventSink": true, "SetPrincipal": true,
	"SetStatePath": true, "SetChannelRegistry": true, "SetReadinessGate": true,
	"SetDefaultOutputs": true, "SetConsecutiveFailLimit": true,
	"SetScheduleStore": true, "RequirePrincipal": true, "PrincipalWorkspace": true,
	// These take a *Definition and deliver its configured output; they read
	// nothing keyed by agent ID.
	"DeliverScheduledOutput": true, "DeliverScheduledReply": true,
	"HasScheduledOutputTarget": true,
}

func TestTheSchedulerIsReachedThroughAScopedAccessor(t *testing.T) {
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
			if _, allowed := schedulerScopeAccessors[fn.Name.Name]; allowed {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || deploymentWideSchedulerMethods[method.Sel.Name] {
					return true
				}
				field, ok := method.X.(*ast.SelectorExpr)
				if !ok || field.Sel.Name != "scheduler" {
					return true
				}
				receiver, ok := field.X.(*ast.Ident)
				if !ok || receiver.Name != "s" {
					return true
				}
				hint, watched := workspaceFreeSchedulerCalls[method.Sel.Name]
				if !watched {
					// An unrecognised method on s.scheduler is not obviously
					// safe. Naming it here is a decision; inheriting silence is
					// how the last twenty-two got written.
					t.Errorf("%s: %s calls s.scheduler.%s, which this guard does not classify — "+
						"add it to deploymentWideSchedulerMethods if it takes no workspace, or to "+
						"workspaceFreeSchedulerCalls with the scoped call that replaces it",
						fileSet.Position(call.Pos()), fn.Name.Name, method.Sel.Name)
					return true
				}
				t.Errorf("%s: %s calls s.scheduler.%s directly — use %s so the schedule cannot land in the scheduler's own workspace instead of the caller's",
					fileSet.Position(call.Pos()), fn.Name.Name, method.Sel.Name, hint)
				return true
			})
		}
	}
}
