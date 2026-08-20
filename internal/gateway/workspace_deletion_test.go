// workspace_deletion_test.go — MU-032 criteria 3 and 6.
//
// The three gates defend against three different things, and the tests are
// organised that way because the temptation with an irreversible operation is
// to add gates until it feels safe rather than until each one covers something
// the others cannot.
package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/ownership"
	"github.com/soulacy/soulacy/internal/tenancy"
)

func goodRequest() DeletionRequest {
	return DeletionRequest{ConfirmWorkspaceName: "Acme Production", Reason: "consolidating"}
}

// Recent authentication answers "is this still the person who signed in",
// which no role check can: an abandoned session carries a genuine owner's
// credential and passes every one of them.
func TestDeletionRequiresRecentAuthentication(t *testing.T) {
	_, refusal := checkDeletionGates(goodRequest(), "Acme Production", tenancy.WorkspaceActive, false)
	if refusal == nil {
		t.Fatal("a stale session deleted a workspace")
	}
	if refusal.code != "reauthentication_required" {
		t.Fatalf("refusal code = %q", refusal.code)
	}
	if !strings.Contains(refusal.remedy, "reauthenticate") {
		t.Fatalf("the refusal does not say how to recover: %q", refusal.remedy)
	}
}

// Typing the name answers "did you mean THIS workspace", which authentication
// cannot: somebody with two workspaces open is exactly the person who can be
// perfectly authenticated and one tab wrong.
func TestDeletionRequiresTheExactWorkspaceName(t *testing.T) {
	for _, wrong := range []string{
		"", "acme production", "Acme  Production", " Acme Production", "Acme Production ",
		"Acme Prod", "Acme Production\n",
	} {
		req := goodRequest()
		req.ConfirmWorkspaceName = wrong
		_, refusal := checkDeletionGates(req, "Acme Production", tenancy.WorkspaceActive, true)
		if refusal == nil {
			t.Errorf("%q was accepted as a confirmation of \"Acme Production\"", wrong)
			continue
		}
		if refusal.code != "workspace_name_mismatch" {
			t.Errorf("%q refused with %q", wrong, refusal.code)
		}
	}
	if _, refusal := checkDeletionGates(goodRequest(), "Acme Production", tenancy.WorkspaceActive, true); refusal != nil {
		t.Fatalf("the exact name was refused: %+v", refusal)
	}
}

// A refusal that supplies the expected name turns the gate into a formality
// the next request satisfies by copying it out of the error.
func TestTheNameMismatchRefusalDoesNotRevealTheName(t *testing.T) {
	req := goodRequest()
	req.ConfirmWorkspaceName = "wrong"
	_, refusal := checkDeletionGates(req, "Acme Production", tenancy.WorkspaceActive, true)
	if refusal == nil {
		t.Fatal("no refusal")
	}
	if strings.Contains(refusal.message+refusal.remedy, "Acme Production") {
		t.Fatalf("the refusal handed back the answer: %q / %q", refusal.message, refusal.remedy)
	}
}

// The window answers "did you mean it at all", which neither of the other two
// can, because both are satisfied at the moment of a mistake.
func TestTheRecoveryWindowHasAFloorAndADefault(t *testing.T) {
	window, refusal := checkDeletionGates(goodRequest(), "Acme Production", tenancy.WorkspaceActive, true)
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	if window != DefaultRecoveryWindow {
		t.Fatalf("default window = %s, want %s", window, DefaultRecoveryWindow)
	}

	// Longer is allowed; shorter is the whole point of a floor.
	req := goodRequest()
	req.RecoveryWindow = "720h"
	if got, refusal := checkDeletionGates(req, "Acme Production", tenancy.WorkspaceActive, true); refusal != nil || got != 720*time.Hour {
		t.Fatalf("a longer window was refused: %s %+v", got, refusal)
	}
	for _, short := range []string{"0s", "1m", "23h"} {
		req.RecoveryWindow = short
		_, refusal := checkDeletionGates(req, "Acme Production", tenancy.WorkspaceActive, true)
		if refusal == nil {
			t.Errorf("a %s recovery window was accepted", short)
			continue
		}
		if refusal.code != "recovery_window_too_short" {
			t.Errorf("%s refused with %q", short, refusal.code)
		}
	}
	req.RecoveryWindow = "not-a-duration"
	if _, refusal := checkDeletionGates(req, "Acme Production", tenancy.WorkspaceActive, true); refusal == nil || refusal.code != "invalid_recovery_window" {
		t.Fatalf("an unparseable window was accepted: %+v", refusal)
	}
}

// ORDER MATTERS. A caller who typed the wrong workspace must be told THAT,
// not that their window is too short — the second message sends them to fix
// the wrong thing, and they then successfully delete the wrong workspace.
func TestTheWrongWorkspaceIsReportedBeforeTheWindow(t *testing.T) {
	req := DeletionRequest{ConfirmWorkspaceName: "Staging", RecoveryWindow: "1m"}
	_, refusal := checkDeletionGates(req, "Acme Production", tenancy.WorkspaceActive, true)
	if refusal == nil {
		t.Fatal("no refusal")
	}
	if refusal.code != "workspace_name_mismatch" {
		t.Fatalf("refused with %q — the caller is being sent to fix the window on a request that names the wrong workspace", refusal.code)
	}
}

// A second deletion request against a workspace already being deleted must not
// restart the clock, which is what a naive re-request would do.
func TestAWorkspaceAlreadyBeingDeletedIsRefused(t *testing.T) {
	for _, status := range []string{tenancy.WorkspaceDeleting, tenancy.WorkspaceDeleted, tenancy.WorkspaceSuspended} {
		_, refusal := checkDeletionGates(goodRequest(), "Acme Production", status, true)
		if refusal == nil {
			t.Errorf("a deletion was accepted for a workspace in state %q", status)
			continue
		}
		if refusal.code != "workspace_not_active" {
			t.Errorf("state %q refused with %q", status, refusal.code)
		}
	}
	// And the status check comes FIRST: a re-request against a deleting
	// workspace must not be answered "re-authenticate", which would send an
	// owner through a step-up for an operation that is going to be refused.
	_, refusal := checkDeletionGates(goodRequest(), "Acme Production", tenancy.WorkspaceDeleting, false)
	if refusal == nil || refusal.code != "workspace_not_active" {
		t.Fatalf("refused with %+v", refusal)
	}
}

// Criterion 6. The report enumerates the same resource classes the export
// does, because both are the ownership catalog's projection — a report
// assembled from its own list would be the third place that inventory lives,
// and the first to drift.
func TestTheDeletionReportEnumeratesTheCatalog(t *testing.T) {
	at := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	report := newDeletionReport("ws_a", "Acme Production", "usr_alice", "consolidating", at, DefaultRecoveryWindow)

	if report.Status != tenancy.WorkspaceDeleting {
		t.Fatalf("report status = %q", report.Status)
	}
	if !report.RecoverUntil.Equal(at.Add(DefaultRecoveryWindow)) {
		t.Fatalf("recoverable_until = %s", report.RecoverUntil)
	}
	if len(report.Resources) != len(ownership.WorkspaceExportPlan()) {
		t.Fatalf("the report lists %d resource classes and the export lists %d",
			len(report.Resources), len(ownership.WorkspaceExportPlan()))
	}
	// Every class carries a disposition a machine can check, rather than prose
	// a person has to read and believe.
	for _, entry := range report.Resources {
		if entry.Disposition == "" {
			t.Errorf("%s appears in the deletion report with no disposition", entry.Resource)
		}
	}
}

// "Without secret values" — by construction rather than by filtering. Nothing
// in the report path reads a secret, so this asserts the shape stays that way.
func TestTheDeletionReportCarriesNoSecretMaterial(t *testing.T) {
	report := newDeletionReport("ws_a", "Acme Production", "usr_alice", "consolidating", time.Now(), DefaultRecoveryWindow)
	marked := map[string]bool{}
	for _, entry := range report.Resources {
		marked[entry.Resource] = entry.SecretsExcluded
	}
	for _, name := range []string{"secrets", "credentials", "api-keys"} {
		if !marked[name] {
			t.Errorf("the report does not record that %s excludes secret material", name)
		}
	}
}
