package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/soulacy/soulacy/internal/config"
)

func scaleReportLines(t *testing.T, mode string) []observer.LoggedEntry {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	cfg := &config.Config{}
	cfg.Deployment.Mode = mode
	reportScaleReadiness(zap.New(core), cfg)
	return logs.All()
}

// A blocker list nobody prints is a hazard nobody knows about. This is the
// whole point of the file: the list must reach the log.
func TestScaleModeAnnouncesEveryReplicationBlocker(t *testing.T) {
	blockers := config.ScaleReplicationBlockers()
	if len(blockers) == 0 {
		t.Skip("no blockers recorded; nothing to announce")
	}
	entries := scaleReportLines(t, config.DeploymentModeScale)
	if len(entries) == 0 {
		t.Fatal("scale mode logged nothing about replication blockers")
	}
	joined := strings.Join(func() []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Message)
			for _, f := range e.Context {
				out = append(out, f.String)
			}
		}
		return out
	}(), "\n")
	for _, blocker := range blockers {
		if !strings.Contains(joined, blocker) {
			t.Errorf("blocker never reached the log: %s", blocker)
		}
	}
	// One summary line naming seven problems is read as one problem. Each
	// blocker gets its own record so the count in the log matches the count in
	// the list.
	warned := 0
	for _, e := range entries {
		if e.Level == zap.WarnLevel {
			warned++
		}
	}
	if warned < len(blockers) {
		t.Errorf("got %d warn records for %d blockers; each blocker needs its own line, "+
			"because a single summary line is skimmed as a single issue", warned, len(blockers))
	}
}

// Personal and team are single-replica by definition. Printing multi-replica
// hazards there trains operators to ignore the message, which is how the
// scale-mode warning stops working.
func TestSingleReplicaModesStaySilent(t *testing.T) {
	for _, mode := range []string{config.DeploymentModePersonal, config.DeploymentModeTeam, ""} {
		if entries := scaleReportLines(t, mode); len(entries) != 0 {
			t.Errorf("mode %q logged %d scale-replication records; it cannot run replicas",
				mode, len(entries))
		}
	}
}

// The behaviour test above passes on a build where nothing calls the reporter.
// That build is the original bug — an enumerated hazard list that no operator
// ever sees — so the call site is checked directly.
func TestBootActuallyReportsScaleReadiness(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wire.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if findCall(fn.Body, "reportScaleReadiness") != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("no function in wire.go calls reportScaleReadiness — the scale replication " +
			"blockers are recorded and never shown to the operator running the thing")
	}
}
