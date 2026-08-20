package gateway

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/ownership"
	"github.com/soulacy/soulacy/internal/tenancy"
)

// workspace_deletion.go — MU-032 criteria 3 and 6.
//
// "Deletion requires recent authentication, explicit workspace-name
// confirmation, and a configurable recovery window", and "completion produces
// a deletion report without secret values."
//
// THE THREE GATES DEFEND AGAINST THREE DIFFERENT THINGS, which is why all
// three are present and none substitutes for another:
//
//   - Recent authentication (MU-030's step-up) answers "is this still the
//     person who signed in", which role checks cannot: an abandoned session
//     carries a genuine owner's credential and passes every one of them.
//   - Typing the workspace NAME answers "did you mean this workspace", which
//     authentication cannot. Somebody with two workspaces open is exactly the
//     person who can be perfectly authenticated and one tab wrong. It is also
//     the only gate that survives a scripted client: a confirm dialog is a
//     click, and a name is a value that has to be fetched from the thing being
//     destroyed.
//   - The recovery window answers "did you mean it at all", which neither of
//     the others can, because both are satisfied at the moment of a mistake.
//
// The name comparison is deliberately EXACT, not case-insensitive and not
// trimmed of internal whitespace. A confirmation that accepts an approximation
// is a confirmation that accepts a guess, and the entire value of this gate is
// that the caller had to go and read the real value.

// DeletionRequest is what an owner sends to begin deleting a workspace.
type DeletionRequest struct {
	// ConfirmWorkspaceName must equal the workspace's name exactly.
	ConfirmWorkspaceName string `json:"confirm_workspace_name"`
	// RecoveryWindow overrides the deployment default, bounded below by
	// MinRecoveryWindow. A caller may ask for LONGER, never shorter: the floor
	// is what makes the window a safeguard rather than a formality, and a
	// client that could pass "0" would have turned an irreversible operation
	// into a single request again.
	RecoveryWindow string `json:"recovery_window,omitempty"`
	// Reason is recorded in the audit trail and the deletion report.
	Reason string `json:"reason,omitempty"`
}

// MinRecoveryWindow is the floor on how long a deletion can be undone.
//
// Twenty-four hours because the mistake this protects against is usually
// noticed by somebody OTHER than the person who made it, and that person has
// to be awake, in another timezone, and looking.
const MinRecoveryWindow = 24 * time.Hour

// DefaultRecoveryWindow is what a request that names none gets.
const DefaultRecoveryWindow = 7 * 24 * time.Hour

// DeletionReport is what completion produces (criterion 6).
//
// It carries the PLAN, projected from the ownership catalog, so the report
// enumerates the same resource classes the export does. A report assembled
// from its own list would be the third place that inventory lives, and the
// first to drift.
type DeletionReport struct {
	WorkspaceID   string                `json:"workspace_id"`
	WorkspaceName string                `json:"workspace_name"`
	RequestedBy   string                `json:"requested_by"`
	RequestedAt   time.Time             `json:"requested_at"`
	RecoverUntil  time.Time             `json:"recoverable_until"`
	Reason        string                `json:"reason,omitempty"`
	Status        string                `json:"status"`
	Resources     []ownership.PlanEntry `json:"resources"`
}

// deletionRefusal is a gate's answer, kept separate from the HTTP shell so the
// awkward cases are testable without a request.
type deletionRefusal struct {
	status  int
	code    string
	message string
	remedy  string
}

// checkDeletionGates evaluates everything that must be true before a workspace
// deletion may begin.
//
// Order matters and is not arbitrary: the NAME is checked before the recovery
// window, so a caller who typed the wrong workspace is told that rather than
// being told their window is too short — the second message would send them to
// fix the wrong thing, and they would then successfully delete the wrong
// workspace.
func checkDeletionGates(req DeletionRequest, workspaceName string, status string, recentAuth bool) (time.Duration, *deletionRefusal) {
	if !tenancy.WorkspaceAcceptsWrites(status) {
		return 0, &deletionRefusal{
			status:  fiber.StatusConflict,
			code:    "workspace_not_active",
			message: "this workspace is already being deleted or is not accepting changes",
			remedy:  "check the workspace's status before requesting deletion again",
		}
	}
	if !recentAuth {
		return 0, &deletionRefusal{
			status:  fiber.StatusUnauthorized,
			code:    "reauthentication_required",
			message: "deleting a workspace needs you to confirm it is still you",
			remedy:  "POST /api/v1/auth/reauthenticate with your credential, then retry",
		}
	}
	// Exact. A confirmation that accepts an approximation accepts a guess.
	if req.ConfirmWorkspaceName != workspaceName {
		return 0, &deletionRefusal{
			status: fiber.StatusBadRequest,
			code:   "workspace_name_mismatch",
			// The expected name is NOT echoed. The caller is deleting a
			// workspace they can read, so they can look it up — and a refusal
			// that supplies the answer turns the gate into a formality the
			// next request satisfies by copying.
			message: "confirm_workspace_name does not match this workspace's name",
			remedy:  "type the workspace's name exactly as it appears in the workspace settings",
		}
	}
	window := DefaultRecoveryWindow
	if raw := strings.TrimSpace(req.RecoveryWindow); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, &deletionRefusal{
				status:  fiber.StatusBadRequest,
				code:    "invalid_recovery_window",
				message: "recovery_window is not a valid duration",
				remedy:  "use Go duration syntax, for example \"72h\"",
			}
		}
		if parsed < MinRecoveryWindow {
			return 0, &deletionRefusal{
				status: fiber.StatusBadRequest,
				code:   "recovery_window_too_short",
				message: "recovery_window is below the " + MinRecoveryWindow.String() +
					" minimum; a deletion nobody can undo is not a safer deletion",
				remedy: "request a longer window, or none to use the default",
			}
		}
		window = parsed
	}
	return window, nil
}

// newDeletionReport assembles the report for a requested deletion.
//
// The resource list is the ownership catalog's projection, so the report
// enumerates exactly what the export does — and a resource class added later
// appears in both or the build fails, rather than in one.
//
// No secret values, by construction rather than by filtering: nothing in this
// function reads a secret. The plan carries the catalog's own "never secrets"
// marks so a reader can see which classes promised that, without the report
// having to have touched one to say so.
func newDeletionReport(workspaceID, workspaceName, requestedBy, reason string, at time.Time, window time.Duration) DeletionReport {
	return DeletionReport{
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
		RequestedBy:   requestedBy,
		RequestedAt:   at.UTC(),
		RecoverUntil:  at.UTC().Add(window),
		Reason:        strings.TrimSpace(reason),
		Status:        tenancy.WorkspaceDeleting,
		Resources:     ownership.WorkspaceDeletionPlan(),
	}
}
