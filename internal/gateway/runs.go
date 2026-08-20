package gateway

// runs.go — the durable run API (MU-020).
//
// POST /api/v1/runs             submit; 202 with a run id and event cursor
// GET  /api/v1/runs             list this workspace's runs
// GET  /api/v1/runs/:id         inspect one
// POST /api/v1/runs/:id/cancel  ask for cancellation
//
// The submission path returns immediately and the work continues without the
// caller. That is the difference from /chat: a client may disconnect, a
// gateway may restart, and the run is still findable by its id.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/runs"
)

// SetRunStore wires the durable run record. Routes 503 until it is set.
func (s *Server) SetRunStore(store *runs.Store) { s.runStore = store }

func newRunID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// A predictable run ID would make one tenant's run guessable by
		// another. Refusing to invent one is the only safe response, and the
		// caller sees a 500 rather than a colliding id.
		panic("gateway: crypto/rand unavailable, refusing to mint a predictable run id: " + err.Error())
	}
	return "run_" + hex.EncodeToString(b)
}

func (s *Server) requireRunStore(c *fiber.Ctx) (*runs.Store, bool) {
	if s.runStore == nil {
		_ = s.errMsg(c, fiber.StatusServiceUnavailable, "durable runs are not configured")
		return nil, false
	}
	return s.runStore, true
}

// handleSubmitRun admits a run and returns 202 with its id and cursor.
func (s *Server) handleSubmitRun(c *fiber.Ctx) error {
	store, ok := s.requireRunStore(c)
	if !ok {
		return nil
	}
	var body struct {
		AgentID        string          `json:"agent_id"`
		SessionID      string          `json:"session_id"`
		IdempotencyKey string          `json:"idempotency_key"`
		Payload        json.RawMessage `json:"payload"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body")
	}
	body.AgentID = strings.TrimSpace(body.AgentID)
	if body.AgentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id is required")
	}
	// The header form is what HTTP clients expect; the body field stays
	// supported so a caller that cannot set headers is not locked out.
	if key := strings.TrimSpace(c.Get("Idempotency-Key")); key != "" {
		body.IdempotencyKey = key
	}

	scope := s.agents(c)
	def := scope.Get(body.AgentID)
	if def == nil {
		// Indistinguishable from "exists in another workspace", so run
		// submission is not an agent-enumeration oracle.
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	run := runs.Run{
		ID:          newRunID(),
		WorkspaceID: scope.WorkspaceID(),
		AgentID:     def.ID,
		// Pinned at admission: the agent may be edited while this run is still
		// going, and "which version produced this result" has to stay
		// answerable afterwards.
		//
		// def.Version is NOT that. It is a string the author types into
		// SOUL.yaml — usually empty, never updated by an edit, and entirely
		// under the control of whoever wrote the file. Pinning to it recorded
		// a value that does not change when the definition does, so every run
		// of an agent that never set `version:` was pinned to "" and the field
		// answered its own question with nothing. The content version changes
		// exactly when the definition changes, which is the whole job.
		AgentVersion:   def.ContentVersion(),
		SessionID:      strings.TrimSpace(body.SessionID),
		IdempotencyKey: body.IdempotencyKey,
		Payload:        body.Payload,
		Status:         runs.StatusQueued,
	}
	if identity, ok := requestIdentity(c); ok {
		run.Subject = identity.Subject()
		run.CredentialID = identity.CredentialID()
		run.PrincipalKind = identity.PrincipalKind()
		run.PolicySnapshot = policySnapshot(identity)
	}

	stored, replayed, err := store.Submit(c.UserContext(), run)
	if err != nil {
		s.log.Error("runs: submit failed", zap.String("agent", body.AgentID), zap.Error(err))
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if !replayed {
		// Enqueued only for a genuinely new run. A replay must not start the
		// work a second time — that is the entire promise of the key, and
		// enqueueing here rather than inside Submit keeps the store free of
		// any opinion about how a run is executed.
		if !s.enqueueRun(stored, body.Payload) {
			// The inbox is full. Marking the run failed rather than leaving it
			// queued is the honest outcome: nothing is going to pick it up,
			// and a run that sits in "queued" forever is indistinguishable
			// from one that is merely waiting its turn.
			if _, terr := store.Transition(c.UserContext(), stored.WorkspaceID, stored.ID,
				runs.StatusFailed, runs.TransitionOptions{
					FailureReason: "the run queue is full; retry when the gateway has drained",
				}); terr != nil {
				s.log.Error("runs: could not fail an unqueueable run", zap.String("run_id", stored.ID), zap.Error(terr))
			}
			return s.errMsg(c, fiber.StatusServiceUnavailable, "the run queue is full")
		}
	}

	status := fiber.StatusAccepted
	if replayed {
		// 200, not 202: nothing was accepted this time. The distinction lets a
		// client tell "my retry was absorbed" from "a second run started",
		// which is the entire reason it sent a key.
		status = fiber.StatusOK
	}
	return c.Status(status).JSON(fiber.Map{
		"run_id":   stored.ID,
		"status":   stored.Status,
		"cursor":   stored.Cursor,
		"replayed": replayed,
	})
}

// enqueueRun hands a submitted run to the worker pool.
//
// The message carries the run id in metadata and travels on the "run"
// pseudo-channel, which has no adapter: its reply is stored on the record
// rather than sent anywhere, because the caller has already left.
func (s *Server) enqueueRun(run runs.Run, payload json.RawMessage) bool {
	if s.channels == nil {
		return false
	}
	// The message shape lives with the record (internal/runs/message.go)
	// because the recovery sweep builds the same message for a run whose
	// worker died. Two copies would drift, and a drifted run_id key produces
	// a message that executes fine and records nothing.
	run.Payload = payload
	return s.channels.Enqueue(runs.InboundMessage(run))
}

// policySnapshot records the authorization state at admission.
//
// A long run outlives the grants that admitted it. An audit asking "was this
// allowed?" means allowed *then*, and re-deriving it later from current
// membership answers a different question.
func policySnapshot(identity requestctx.Identity) json.RawMessage {
	snapshot := map[string]any{
		"role":            identity.Role(),
		"organization_id": identity.OrganizationID(),
		"workspace_id":    identity.WorkspaceID(),
		"membership_id":   identity.MembershipID(),
		"scopes":          identity.Scopes(),
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	return encoded
}

func (s *Server) handleListRuns(c *fiber.Ctx) error {
	store, ok := s.requireRunStore(c)
	if !ok {
		return nil
	}
	list, err := store.List(c.UserContext(), s.agents(c).WorkspaceID(),
		strings.TrimSpace(c.Query("status")), c.QueryInt("limit", 100))
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if list == nil {
		list = []runs.Run{}
	}
	return c.JSON(fiber.Map{"runs": list, "count": len(list)})
}

func (s *Server) handleGetRun(c *fiber.Ctx) error {
	store, ok := s.requireRunStore(c)
	if !ok {
		return nil
	}
	run, err := store.Get(c.UserContext(), s.agents(c).WorkspaceID(), c.Params("id"))
	if errors.Is(err, runs.ErrNotFound) {
		return s.errMsg(c, fiber.StatusNotFound, "run not found")
	}
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(run)
}

// handleCancelRun asks for cancellation.
//
// A queued run is cancelled outright; a running one goes to `cancelling` and
// its worker observes the state change between bounded operations (MU-027
// criterion 5). Reporting the resulting status rather than a bare "ok" matters:
// "cancelled" and "asked to cancel" are different promises, and a caller told
// the wrong one stops watching too early — which is what this handler used to
// do, writing the terminal status directly while the work carried on.
func (s *Server) handleCancelRun(c *fiber.Ctx) error {
	store, ok := s.requireRunStore(c)
	if !ok {
		return nil
	}
	workspaceID := s.agents(c).WorkspaceID()
	reason := strings.TrimSpace(c.Query("reason"))
	if reason == "" {
		reason = "cancelled by request"
	}
	run, err := store.RequestCancel(c.UserContext(), workspaceID, c.Params("id"), reason)
	switch {
	case errors.Is(err, runs.ErrNotFound):
		return s.errMsg(c, fiber.StatusNotFound, "run not found")
	case errors.Is(err, runs.ErrTerminal):
		// 409, not 404: the run is theirs and they may see it, it has simply
		// already finished. Cancelling a finished run is a no-op the caller
		// should be told about, not an error to retry.
		return s.errMsg(c, fiber.StatusConflict, "run has already finished")
	case err != nil:
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"run_id": run.ID, "status": run.Status})
}
