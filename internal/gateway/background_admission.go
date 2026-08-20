package gateway

import (
	"context"
	"time"

	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// background_admission.go — MU-025 criterion 5: "background jobs revalidate
// workspace status and policy before committing."
//
// THE GAP THIS CLOSES is a window, not a permission. Every background job here
// carries a workspace it obtained from a request that has already returned —
// the preference miner runs after a Studio save, the learning replay runs at
// startup over whatever the loader holds. `workspaceContextMW` refuses writes
// to a workspace that has entered `deleting`, but it refuses them at the EDGE,
// and these jobs are past the edge. So a save made a second before an owner
// requested deletion mines a preference into that workspace minutes later,
// and a startup replay teaches lessons into a workspace whose recovery window
// closed while the process was down.
//
// Neither is a leak, and that is exactly why it went unnoticed: the data lands
// in the right tenant. What it does is defeat the recovery window's other
// half. "New writes stop when deletion begins" is what makes the window
// meaningful — a customer who cancels on day six should get back the workspace
// they had on day one, not one that has been quietly accumulating derived data
// the whole time. And a write that lands after the PURGE has run is data
// surviving a deletion the report said completed.
//
// The check is deliberately CHEAP and FAILS OPEN on an unavailable store.
// A learning job is not worth blocking on a tenancy database that is down;
// the cost of the rare wrong write is a stale lesson, and the cost of failing
// closed is that every deployment without a workspace lifecycle — which is
// every Personal one — silently stops learning.

// backgroundWriteAdmitted reports whether a background job may still commit to
// a workspace.
//
// Named for what a caller must do with it rather than for what it reads: the
// answer is "may I write", and every call site is one line before a write.
func (s *Server) backgroundWriteAdmitted(ctx context.Context, workspaceID string) bool {
	if s == nil || s.workspaceLifecycle == nil {
		// No lifecycle to consult. A Personal installation has one workspace
		// that cannot be deleted through the API, so there is no status that
		// could have changed under this job.
		return true
	}
	record, err := s.workspaceLifecycle.Workspace(ctx, wsroot.Normalize(workspaceID))
	if err != nil {
		// Fails OPEN. See the file comment: blocking derived-data jobs on a
		// tenancy database outage trades a rare stale write for a total,
		// silent stop.
		return true
	}
	return tenancy.WorkspaceAcceptsWrites(record.Status)
}

// backgroundAdmissionTimeout bounds the status read.
//
// Short, because the job it gates is optional. A background write that waits
// ten seconds to learn whether it may proceed has already cost more than the
// write is worth, and the read is a single indexed row.
const backgroundAdmissionTimeout = 3 * time.Second

// backgroundAdmissionContext gives the status read its own deadline, detached
// from whatever the job is doing.
func backgroundAdmissionContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), backgroundAdmissionTimeout)
}
