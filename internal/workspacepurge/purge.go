// Package workspacepurge executes a workspace deletion and reports honestly on
// what survived it.
//
// MU-032 criteria 4 and 6: "database rows, objects, vectors, secrets, jobs,
// caches, and derived learning data are deleted or tombstoned according to
// documented retention rules", and "completion produces a deletion report".
//
// THE REPORT IS THE PRODUCT, not a receipt attached to it. A purge that runs
// across twenty-seven resource classes and covers nineteen of them is a normal
// state for a system this size at this stage; what is NOT acceptable is that
// state being indistinguishable from full coverage. So this package is built
// the same way internal/workspaceexport is: the plan is projected from the
// ownership catalog, every entry starts as `not-purged`, and a class with no
// registered Purger appears in the report by name saying its data survived.
//
// The two are deliberately the same shape because they are the same claim seen
// from two sides. An export says "here is everything you had"; a deletion says
// "none of that is left". If those two lists could disagree, one of them is
// lying, and the customer has no way to tell which.
//
// WHAT MAKES THIS DIFFERENT FROM THE EXPORT is that an incomplete deletion has
// no benign reading. An export missing a class is a customer who did not get
// all their data — annoying, recoverable, and visible to them. A deletion
// missing a class is data that outlives a deletion somebody was told
// completed, which is the failure that ends up in a regulator's letter. So
// `Report.Complete` is false whenever anything is unpurged, the API must never
// describe such a run as finished, and `Survivors()` names the classes.
package workspacepurge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/ownership"
)

// Outcome is what happened to one resource class.
//
// Five values, and the distinction between the last two is the one that
// matters most to whoever reads the report:
//
//   - not-purged: this build has no purger for the class. The data is still
//     there and nobody tried. Deterministic, and fixed by writing code.
//   - failed: a purger ran and could not finish. Retrying may help.
//
// Collapsing them into "incomplete" would make a permanent gap look like a
// transient one, which is how a known gap gets retried forever instead of
// fixed.
type Outcome string

const (
	OutcomePurged     Outcome = "purged"
	OutcomeTombstoned Outcome = "tombstoned"
	OutcomeRetained   Outcome = "retained"
	OutcomeNotPurged  Outcome = "not-purged"
	OutcomeFailed     Outcome = "failed"
)

// Settled reports whether this outcome discharges the deletion obligation for
// its class. Retained counts: the catalog says the data outlives the workspace
// and says why, so it is a decision rather than a gap.
func (o Outcome) Settled() bool {
	return o == OutcomePurged || o == OutcomeTombstoned || o == OutcomeRetained
}

// Removed is what one purger deleted, for the report.
type Removed struct {
	Rows  int64  `json:"rows"`
	Bytes int64  `json:"bytes,omitempty"`
	Note  string `json:"note,omitempty"`
	// Tombstoned marks a purger that left identifiers behind on purpose, so
	// references elsewhere do not dangle. The catalog says which classes are
	// entitled to do this; ValidatePurgers checks the purger agrees with it.
	Tombstoned bool `json:"-"`
}

// Purger removes one resource class's data for one workspace.
//
// It takes a workspace ID and NOT a request, for the same reason the export's
// Sources do: a purge runs long after the request that asked for it — after a
// recovery window measured in days — so there is no request left to read.
type Purger struct {
	Resource string
	Purge    func(ctx context.Context, workspaceID string) (Removed, error)
}

// A NOTE ON WHAT IS NOT HERE: a `Purger.Also` field, letting one purger claim
// it settles several classes. Team/Scale now has one canonical filesystem tree
// per workspace, and deleting it does remove every file below it. The report
// still records each ownership class independently: a tree removal cannot
// prove that a class's database rows, external objects, or revoked credentials
// were handled correctly. Each class therefore needs its own purger, while the
// final workspace-files purger is only the filesystem-boundary cleanup.

// Entry is one resource class's line in the deletion report.
type Entry struct {
	Resource string          `json:"resource"`
	Class    ownership.Class `json:"class"`
	// Disposition is what the ownership catalog said should happen, carried
	// separately from what did happen. A report where the two can be compared
	// is one a reviewer can check; a report of outcomes alone requires them to
	// go and look up the policy.
	Disposition ownership.Disposition `json:"disposition"`
	Policy      string                `json:"policy"`
	Outcome     Outcome               `json:"outcome"`
	Rows        int64                 `json:"rows,omitempty"`
	Bytes       int64                 `json:"bytes,omitempty"`
	// Detail explains an unsettled entry, or carries a purger's note. Never a
	// value read out of the workspace — a deletion report that quotes the data
	// it deleted is a copy of that data (criterion 6 says no secret values,
	// and the safe reading of that is no values at all).
	Detail string `json:"detail,omitempty"`
}

// Report is what a completed purge produces.
type Report struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name,omitempty"`
	RequestedBy   string    `json:"requested_by"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
	Entries       []Entry   `json:"entries"`
	// Complete is false whenever ANY class is unsettled. Computed, never set
	// by a caller: a boolean somebody can assign is a boolean somebody
	// eventually assigns wrongly, and this particular one is the difference
	// between "your data is gone" and a statement that is not true.
	Complete bool `json:"complete"`
	// Survivors names the classes whose data is still present. Present even
	// when empty (as `[]`), so a reader can tell "nothing survived" from a
	// field an older build did not emit.
	Survivors []string `json:"survivors"`
}

// Options configures one purge run.
type Options struct {
	WorkspaceID   string
	WorkspaceName string
	RequestedBy   string
	Purgers       []Purger
	Now           func() time.Time
	// Ctx bounds the run. Separate from any request context for the same
	// reason the export's is: this runs days after the request, and a purge
	// that cannot be stopped by a shutting-down process is a purge that gets
	// killed halfway with no record.
	Ctx context.Context
}

// Run executes a purge and returns its report.
//
// It never returns early on a single class's failure. A deletion that stops at
// the first error leaves the caller with no idea which of the remaining
// twenty-six classes were reached, and the natural retry re-runs the ones that
// already succeeded. Every class is attempted; the report says what happened
// to each.
//
// The error return is for the run as a whole — a cancelled context — and is
// separate from any class's failure, which lives in that class's entry.
func Run(opts Options) (Report, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	plan := ownership.WorkspaceDeletionPlan()
	byResource := map[string]Purger{}
	for _, purger := range opts.Purgers {
		byResource[purger.Resource] = purger
	}

	report := Report{
		WorkspaceID:   opts.WorkspaceID,
		WorkspaceName: opts.WorkspaceName,
		RequestedBy:   opts.RequestedBy,
		StartedAt:     now().UTC(),
		Entries:       make([]Entry, 0, len(plan)),
	}

	var runErr error
	for _, planned := range plan {
		entry := Entry{
			Resource:    planned.Resource,
			Class:       planned.Class,
			Disposition: planned.Disposition,
			Policy:      planned.Deletion,
		}
		switch {
		case runErr != nil:
			entry.Outcome = OutcomeNotPurged
			entry.Detail = "the purge was stopped before this class was reached"
		case planned.Disposition == ownership.Retained:
			// A decision, not a gap. The catalog's own sentence says why this
			// class outlives the workspace, and it is carried in Policy so the
			// report states the reason rather than asserting it.
			entry.Outcome = OutcomeRetained
		default:
			purger, ok := byResource[planned.Resource]
			if !ok {
				entry.Outcome = OutcomeNotPurged
				entry.Detail = "this build has no purger registered for this resource class, so its data survives the deletion"
				break
			}
			if err := ctx.Err(); err != nil {
				runErr = err
				entry.Outcome = OutcomeNotPurged
				entry.Detail = "the purge was stopped before this class was reached"
				break
			}
			removed, err := purger.Purge(ctx, opts.WorkspaceID)
			switch {
			case err != nil:
				entry.Outcome = OutcomeFailed
				entry.Detail = err.Error()
			case removed.Tombstoned:
				entry.Outcome = OutcomeTombstoned
				entry.Rows, entry.Bytes, entry.Detail = removed.Rows, removed.Bytes, removed.Note
			default:
				entry.Outcome = OutcomePurged
				entry.Rows, entry.Bytes, entry.Detail = removed.Rows, removed.Bytes, removed.Note
			}
		}
		report.Entries = append(report.Entries, entry)
	}

	report.CompletedAt = now().UTC()
	report.finalise()
	return report, runErr
}

// finalise computes Complete and Survivors from the entries. Called once, at
// the end, so the two cannot disagree with the list they summarise.
func (r *Report) finalise() {
	survivors := []string{}
	for _, entry := range r.Entries {
		if !entry.Outcome.Settled() {
			survivors = append(survivors, entry.Resource)
		}
	}
	sort.Strings(survivors)
	r.Survivors = survivors
	r.Complete = len(survivors) == 0
}

// ErrIncomplete is returned by callers that must not describe an incomplete
// purge as a finished deletion.
var ErrIncomplete = errors.New("workspacepurge: the deletion did not cover every resource class")

// ValidatePurgers reports purgers that do not match the catalog.
//
// Two checks, and the second is the one that is easy to get wrong. A purger
// naming a class the catalog does not carry is a typo whose only symptom is
// the class it meant to cover staying unpurged — the same silent failure
// workspaceexport.ValidateSources exists to catch. And a purger registered for
// a class the catalog marks Retained is a contradiction: either the code is
// deleting data the policy says survives, or the policy is stale. Both are
// worth stopping at the boundary rather than discovering in a report.
func ValidatePurgers(purgers []Purger) error {
	planned := map[string]ownership.PlanEntry{}
	for _, entry := range ownership.WorkspaceDeletionPlan() {
		planned[entry.Resource] = entry
	}
	var problems []string
	for _, purger := range purgers {
		name := strings.TrimSpace(purger.Resource)
		entry, known := planned[name]
		switch {
		case !known:
			problems = append(problems, fmt.Sprintf("%q is not a workspace-owned resource in the ownership catalog", name))
		case purger.Purge == nil:
			problems = append(problems, fmt.Sprintf("%q is registered with no purge function", name))
		case entry.Disposition == ownership.Retained:
			problems = append(problems, fmt.Sprintf(
				"%q is registered for purging but the catalog says it is retained (%q) — "+
					"either the code deletes data the policy keeps, or the policy is stale",
				name, entry.Deletion))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("workspacepurge: %s", strings.Join(problems, "; "))
}

// Coverage returns the workspace-owned resource classes that a given purger
// set does NOT cover, so a build guard can hold the gap to a known list.
func Coverage(purgers []Purger) []string {
	covered := map[string]bool{}
	for _, purger := range purgers {
		covered[strings.TrimSpace(purger.Resource)] = true
	}
	var uncovered []string
	for _, entry := range ownership.WorkspaceDeletionPlan() {
		if entry.Disposition == ownership.Retained || covered[entry.Resource] {
			continue
		}
		uncovered = append(uncovered, entry.Resource)
	}
	sort.Strings(uncovered)
	return uncovered
}
