// background_admission_test.go — MU-025 criterion 5, behaviourally and
// structurally.
//
// The behavioural half proves the two background jobs that exist today stop
// writing to a workspace that has entered deletion. The structural half fails
// the build when the NEXT one does not, because this is a rule whose violation
// is invisible: the data lands in the right tenant, nothing errors, and the
// only symptom is a recovery window that quietly did not restore what it
// promised.
package gateway

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/tenancy"
)

type statusLifecycle struct {
	status string
	err    error
	reads  int
}

func (l *statusLifecycle) Workspace(context.Context, string) (tenancy.WorkspaceRecord, error) {
	l.reads++
	if l.err != nil {
		return tenancy.WorkspaceRecord{}, l.err
	}
	return tenancy.WorkspaceRecord{ID: "ws_team", Name: "Acme", Status: l.status}, nil
}

func (l *statusLifecycle) BeginDeletion(context.Context, tenancy.Mutation, string, time.Time) (tenancy.WorkspaceRecord, error) {
	return tenancy.WorkspaceRecord{}, nil
}
func (l *statusLifecycle) CancelDeletion(context.Context, tenancy.Mutation, string) (tenancy.WorkspaceRecord, error) {
	return tenancy.WorkspaceRecord{}, nil
}
func (l *statusLifecycle) CompleteDeletion(context.Context, tenancy.Mutation, string) (tenancy.WorkspaceRecord, error) {
	return tenancy.WorkspaceRecord{}, nil
}
func (l *statusLifecycle) DueForPurge(context.Context, time.Time, int) ([]tenancy.WorkspaceRecord, error) {
	return nil, nil
}
func (l *statusLifecycle) ClaimPurge(context.Context, tenancy.Mutation, string, time.Time) (bool, error) {
	return false, nil
}

func TestABackgroundWriteIsRefusedOnceDeletionBegins(t *testing.T) {
	srv := newTestGateway(t, "secret")

	for _, status := range []string{tenancy.WorkspaceDeleting, tenancy.WorkspaceSuspended, tenancy.WorkspaceDeleted, "a-status-this-build-does-not-know"} {
		srv.SetWorkspaceLifecycle(&statusLifecycle{status: status})
		if srv.backgroundWriteAdmitted(context.Background(), "ws_team") {
			t.Errorf("a background write was admitted to a %q workspace", status)
		}
	}
	srv.SetWorkspaceLifecycle(&statusLifecycle{status: tenancy.WorkspaceActive})
	if !srv.backgroundWriteAdmitted(context.Background(), "ws_team") {
		t.Error("a background write was refused on an active workspace")
	}
	// An empty status is an older row that predates the column, and must read
	// as active — otherwise upgrading stops every background job in every
	// existing workspace at once.
	srv.SetWorkspaceLifecycle(&statusLifecycle{status: ""})
	if !srv.backgroundWriteAdmitted(context.Background(), "ws_team") {
		t.Error("a background write was refused on a workspace with no recorded status")
	}
}

// Fails OPEN, deliberately, and this asserts the decision rather than
// discovering it. Blocking derived-data jobs on a tenancy database outage
// trades a rare stale write for a total, silent stop — and a deployment with
// no workspace lifecycle at all (every Personal one) would never learn again.
func TestABackgroundWriteIsAdmittedWhenTheStatusCannotBeRead(t *testing.T) {
	srv := newTestGateway(t, "secret")
	srv.SetWorkspaceLifecycle(&statusLifecycle{err: context.DeadlineExceeded})
	if !srv.backgroundWriteAdmitted(context.Background(), "ws_team") {
		t.Error("an unreadable status blocked a background write")
	}

	srv.SetWorkspaceLifecycle(nil)
	if !srv.backgroundWriteAdmitted(context.Background(), "ws_team") {
		t.Error("a deployment with no workspace lifecycle blocked a background write")
	}
}

// The replay sweep reads the status ONCE per workspace, not once per event.
// A read per event would be thousands of queries to answer a question whose
// answer is the same every time.
func TestTheLearningReplayChecksStatusOncePerWorkspace(t *testing.T) {
	srv := newTestGateway(t, "secret")
	// The replay returns immediately with no action backend, so a test without
	// one would pass for the wrong reason — it would assert nothing about the
	// status check and everything about a nil guard three lines earlier.
	srv.actions = &fakeTailBackend{}
	lifecycle := &statusLifecycle{status: tenancy.WorkspaceDeleting}
	srv.SetWorkspaceLifecycle(lifecycle)

	srv.replayStudioLearning()

	if lifecycle.reads == 0 {
		t.Fatal("the learning replay never consulted the workspace's status")
	}
	if workspaces := len(srv.loader.AllWorkspaces()); lifecycle.reads > workspaces {
		t.Fatalf("the replay read the status %d times for %d workspaces", lifecycle.reads, workspaces)
	}
}

// backgroundWriters are the functions that commit derived data outside a
// request. Each must consult backgroundWriteAdmitted before writing.
//
// A list rather than a pattern, because "runs outside a request" is not
// something a parser can see. Adding a background writer means adding a line
// here, and the line is the prompt to think about the window.
var backgroundWriters = []string{
	"processPreferenceJob",
	"replayStudioLearning",
}

func TestEveryBackgroundWriterRevalidatesTheWorkspace(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	found := map[string]bool{}
	checked := map[string]bool{}
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
			wanted := false
			for _, writer := range backgroundWriters {
				if fn.Name.Name == writer {
					wanted = true
				}
			}
			if !wanted {
				continue
			}
			found[fn.Name.Name] = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if selector.Sel.Name == "backgroundWriteAdmitted" {
					checked[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	for _, writer := range backgroundWriters {
		if !found[writer] {
			t.Errorf("%s is listed as a background writer but no longer exists — "+
				"if it was renamed, rename it here; if it was deleted, remove the entry", writer)
			continue
		}
		if !checked[writer] {
			t.Errorf("%s commits derived data outside a request without calling "+
				"backgroundWriteAdmitted — a write that lands after a deletion begins "+
				"defeats the recovery window, and after the purge it is data surviving "+
				"a deletion the report said completed", writer)
		}
	}
}
