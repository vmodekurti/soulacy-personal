package gateway

import (
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

// dlq_retry.go — MU-034 criterion 5: "operators can inspect, retry, or discard
// dead-letter jobs with authorization and audit records."
//
// Inspect and discard already existed. Retry did not, and its absence was not
// cosmetic: a dead letter is the ONLY record of a job that failed with nobody
// watching, and the only actions available were to read it and to throw it
// away. An operator who found a job that failed on a transient provider error
// had to reconstruct the request by hand from the payload — which is a copy of
// a user's message, so "reconstruct by hand" means reading it.
//
// THE WORKSPACE COMES FROM THE ROW, NEVER FROM THE PAYLOAD. This is the same
// rule MU-018 established for channel adapters — inbound identity is stamped
// after the adapter, not by it — and it matters more here, because the payload
// is a serialized message.Message that HAS a WorkspaceID field sitting right
// there, and using it would be the obvious thing to write. The row's
// workspace_id was recorded by the gateway at push time from the run's
// verified principal. The payload is content.
//
// So the retry re-stamps the workspace unconditionally rather than reading it,
// and the entry is fetched through the requester's own workspace, so another
// tenant's dead letter is ErrNotFound before any of this is reached.

// handleRetryDeadLetter re-enqueues a parked job.
//
//	POST /api/v1/admin/dlq/:id/retry
func (s *Server) handleRetryDeadLetter(c *fiber.Ctx) error {
	if s.dlqStore == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "dlq not configured")
	}
	if s.channels == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no channel registry is available to accept a retry")
	}
	// s.dlqScope(c) is passed INLINE at every store call rather than hoisted
	// into a variable. TestSharedStoreReadsNameATenant requires that, and the
	// requirement is right even though a variable would read better here: the
	// guard is syntactic so that it cannot be argued with, and a handler that
	// derives the workspace once and then uses it three times is one edit away
	// from a handler that derives it once and forgets it on the fourth call.
	// Making the guard smart enough to follow a variable would make it smart
	// enough to be wrong.
	entry, err := s.dlqStore.Get(c.Context(), s.dlqScope(c), c.Params("id"))
	if err != nil {
		if err == dlq.ErrNotFound {
			// Another tenant's entry and a nonexistent one are the same
			// answer, as they are for Get and Delete: distinguishing them
			// would make dead-letter IDs an enumeration oracle over other
			// tenants' failures (product invariant 8).
			return s.errMsg(c, fiber.StatusNotFound, "not found")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}

	var msg message.Message
	if err := json.Unmarshal(entry.Payload, &msg); err != nil {
		// A payload that will not parse cannot be retried, and saying so is
		// more useful than a 500: the operator's next move is to discard it,
		// and they need to know that reading it again will not help.
		return s.errMsg(c, fiber.StatusUnprocessableEntity,
			"this entry's payload is not a replayable message; inspect and discard it")
	}

	// Re-stamped, not read. The payload's own WorkspaceID is content — it
	// arrived inside a serialized message — and the row's is what the gateway
	// recorded from a verified principal.
	msg.WorkspaceID = wsroot.Normalize(entry.WorkspaceID)

	if !s.channels.Enqueue(msg) {
		// The inbox is full. The entry is deliberately NOT deleted: a retry
		// that dropped the only record of the job because the queue was busy
		// would lose it permanently, and busy is exactly when an operator is
		// retrying things.
		return s.errMsg(c, fiber.StatusServiceUnavailable,
			"the message inbox is full; the entry is still parked, retry again shortly")
	}

	// Deleted only AFTER the enqueue succeeded, and the ordering is the point.
	// If the retry fails again the engine pushes a fresh entry with the new
	// error, so the operator sees the second failure rather than the first —
	// whereas deleting first and failing to enqueue would leave nothing.
	if err := s.dlqStore.Delete(c.Context(), s.dlqScope(c), entry.ID); err != nil {
		// Enqueued but not deleted: the job WILL run, and the stale entry is
		// a duplicate an operator may retry again. Reported as success with
		// the discrepancy named, because the important half happened and
		// telling them it failed would invite a second retry of a job that is
		// already running.
		s.log.Warn("dead letter was retried but could not be discarded; it may be retried again",
			zap.String("dlq_id", entry.ID), zap.String("workspace_id", entry.WorkspaceID), zap.Error(err))
	}

	// The audit record is written by the auditing() wrapper on the route, not
	// here: it records the response status too, so a refused retry is audited
	// as a refusal rather than not at all. Writing a second record here would
	// mean two entries for one action, disagreeing about whether it happened.
	return c.JSON(fiber.Map{
		"status":   "retried",
		"id":       entry.ID,
		"agent_id": msg.AgentID,
		"attempts": entry.Attempts,
	})
}
