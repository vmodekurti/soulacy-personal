package runtime

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// ConfirmRequest is the payload emitted as an SSE "tool_confirm" event.
// The client renders a dialog and POSTs the result back to /api/v1/chat/confirm.
type ConfirmRequest struct {
	CallID string         `json:"call_id"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Reason string         `json:"reason,omitempty"`
}

// ConfirmSenderFunc is called by the engine when a tool requires confirmation.
// It emits the SSE event and returns a channel that receives the user's decision.
type ConfirmSenderFunc func(req ConfirmRequest) <-chan bool

// confirmSenderKey is the context key for ConfirmSenderFunc.
type confirmSenderKey struct{}

// WithConfirmSender stores fn in ctx so the engine can emit confirm requests
// over the active SSE connection.
func WithConfirmSender(ctx context.Context, fn ConfirmSenderFunc) context.Context {
	return context.WithValue(ctx, confirmSenderKey{}, fn)
}

// confirmSenderFrom retrieves the ConfirmSenderFunc stored in ctx, if any.
func confirmSenderFrom(ctx context.Context) (ConfirmSenderFunc, bool) {
	fn, ok := ctx.Value(confirmSenderKey{}).(ConfirmSenderFunc)
	return fn, ok
}

// dryRunKey is the context key for a per-request dry-run override.
type dryRunKey struct{}

// WithDryRun marks ctx so this run simulates side-effecting tool calls instead
// of executing them, regardless of the agent's own DryRun setting.
func WithDryRun(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, dryRunKey{}, on)
}

// dryRunFrom reports whether ctx requested dry-run.
func dryRunFrom(ctx context.Context) bool {
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

// ── The approval broker (MU-022) ────────────────────────────────────────────
//
// The broker is the in-process RENDEZVOUS: the engine blocks on a channel and
// somebody, eventually, sends a bool down it. That part cannot be persisted —
// a channel is a live goroutine's ear, and when the process dies so does the
// thing that was listening.
//
// What CAN be persisted is the question. Before MU-022 the broker was the only
// record: a map from call ID to channel plus metadata, with no workspace on it
// at all. Two consequences followed directly.
//
// A restart lost every pending approval and told nobody — the approvals page
// simply stopped listing something a person had been asked to decide.
//
// And listing and deciding were gated on an `admin` bool computed as "the
// role is owner or admin", with no tenant in it. In a multi-user deployment
// that made any workspace's admin an approver for every other workspace, with
// read access to their paused calls' arguments. Those arguments are the most
// sensitive payload the system holds by construction: they are the things
// something decided were dangerous enough to stop.
//
// So the broker is now a facade. The durable record lives in
// internal/approvals and is the authority for what exists, who may see it, and
// what was decided; the map holds only the channel to wake. Where the two
// could disagree — has this been decided, may this actor decide it — the store
// wins, because it is the one that survives.

// PendingApproval is the device-agnostic view of a tool call awaiting a human
// decision, as the /approvals API and the mobile companion render it.
//
// Args are the REDACTED form. The full arguments never leave the process that
// is blocked on the answer; see approvals.Redact for why.
type PendingApproval struct {
	ID          string         `json:"id"`
	CallID      string         `json:"call_id"`
	WorkspaceID string         `json:"workspace_id"`
	Tool        string         `json:"tool"`
	Args        map[string]any `json:"args,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	AgentID     string         `json:"agent_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	RunID       string         `json:"run_id,omitempty"`
	Requester   string         `json:"requester,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

type pendingEntry struct {
	ch          chan bool
	workspaceID string
	meta        PendingApproval
}

// ConfirmBroker maps call IDs to the channels blocked runs are waiting on, and
// records each paused call durably when a store is configured.
type ConfirmBroker struct {
	mu         sync.Mutex
	pending    map[string]*pendingEntry
	onRegister func(PendingApproval)
	store      *approvals.Store
	log        *zap.Logger
}

func newConfirmBroker() *ConfirmBroker {
	return &ConfirmBroker{pending: make(map[string]*pendingEntry)}
}

// SetStore installs the durable approval record.
//
// Optional: a deployment without one keeps the old in-memory behaviour, which
// is correct for a personal install where the only approver is the person
// watching. It is NOT correct for Team or Scale, so wiring checks that — see
// internal/app.
func (b *ConfirmBroker) SetStore(store *approvals.Store, log *zap.Logger) {
	b.mu.Lock()
	b.store, b.log = store, log
	b.mu.Unlock()
}

// SetOnRegister installs a callback fired whenever a new approval becomes
// pending. Safe to call once at startup.
func (b *ConfirmBroker) SetOnRegister(fn func(PendingApproval)) {
	b.mu.Lock()
	b.onRegister = fn
	b.mu.Unlock()
}

// ApprovalRequest is everything the broker needs to record and route one
// paused call.
type ApprovalRequest struct {
	CallID    string
	Tool      string
	Args      map[string]any
	Reason    string
	AgentID   string
	SessionID string
}

// Register records a paused tool call and returns the channel its run blocks
// on.
//
// The workspace, the run and the requester come from ctx rather than from the
// caller. A gateway handler that had to pass them could pass the wrong ones,
// and the one it would most plausibly pass wrong is the workspace.
func (b *ConfirmBroker) Register(ctx context.Context, req ApprovalRequest) chan bool {
	workspaceID := WorkspaceFromContext(ctx)
	ch := make(chan bool, 1)
	meta := PendingApproval{
		ID:          req.CallID,
		CallID:      req.CallID,
		WorkspaceID: workspaceID,
		Tool:        req.Tool,
		Args:        approvals.Redact(req.Args),
		Reason:      req.Reason,
		AgentID:     req.AgentID,
		SessionID:   req.SessionID,
		RunID:       RunIDFromContext(ctx),
		Requester:   SubjectFromContext(ctx),
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(approvals.DefaultTTL),
	}

	b.mu.Lock()
	store, log := b.store, b.log
	b.pending[req.CallID] = &pendingEntry{ch: ch, workspaceID: workspaceID, meta: meta}
	fn := b.onRegister
	b.mu.Unlock()

	if store != nil {
		stored, err := store.Request(ctx, approvals.Approval{
			ID: req.CallID, WorkspaceID: workspaceID, RunID: meta.RunID,
			SessionID: req.SessionID, AgentID: req.AgentID,
			Tool: req.Tool, Reason: req.Reason,
			RequesterSubject: meta.Requester,
			RequiredResource: rbac.ResourceApprovals, RequiredAction: rbac.ActionWrite,
			ExpiresAt: meta.ExpiresAt,
		}, req.Args)
		if err != nil && log != nil {
			// The run still blocks and the in-memory route still works, so a
			// storage failure degrades durability rather than stopping the
			// action. What it must not do is degrade SILENTLY.
			log.Error("approval could not be recorded durably; it will not survive a restart",
				zap.String("call_id", req.CallID), zap.String("tool", req.Tool), zap.Error(err))
		} else if err == nil {
			b.mu.Lock()
			if entry, ok := b.pending[req.CallID]; ok {
				// Take the stored redaction and expiry rather than keeping the
				// broker's own, so every reader — API, event stream, this map —
				// is looking at one answer.
				entry.meta.Args, entry.meta.ExpiresAt = stored.Args, stored.ExpiresAt
				meta = entry.meta
			}
			b.mu.Unlock()
		}
	}

	if fn != nil {
		go fn(meta)
	}
	return ch
}

// Forget drops a pending approval nobody will ever answer, and closes its
// durable record.
//
// Called on every exit path of a blocked run. Resolve used to be the only
// deletion, so a run that timed out or was cancelled with a confirmation
// outstanding left a permanent map entry holding the full tool-call arguments,
// still listed as though a human could act on it. With a store the same
// omission would be worse: the record would outlive the process AND the run.
func (b *ConfirmBroker) Forget(ctx context.Context, callID string) bool {
	b.mu.Lock()
	entry, ok := b.pending[callID]
	delete(b.pending, callID)
	store, log := b.store, b.log
	b.mu.Unlock()
	if ok && store != nil {
		if _, err := store.InvalidateRun(context.WithoutCancel(ctx), entry.workspaceID, entry.meta.RunID, approvals.ReasonRunEnded); err != nil && log != nil {
			log.Warn("approval record could not be closed", zap.String("call_id", callID), zap.Error(err))
		}
		// A call with no run id is not covered by InvalidateRun, so close it
		// by id. Chat confirmations have no durable run behind them and are
		// the common case, not an exception.
		if strings.TrimSpace(entry.meta.RunID) == "" {
			_, _ = store.InvalidateApproval(context.WithoutCancel(ctx), entry.workspaceID, callID, approvals.ReasonRunEnded)
		}
	}
	return ok
}

// List returns the approvals a workspace is currently waiting on.
//
// Reads the store when there is one, because the store is what survives a
// restart and what a second gateway process can see. The map is the fallback
// for a personal deployment with no store, and it is filtered by workspace
// there too — the boundary does not depend on which backing is in use.
func (b *ConfirmBroker) List(ctx context.Context, workspaceID string) []PendingApproval {
	workspaceID = wsroot.Normalize(workspaceID)
	b.mu.Lock()
	store := b.store
	b.mu.Unlock()

	if store != nil {
		stored, err := store.ListPending(ctx, workspaceID)
		if err == nil {
			out := make([]PendingApproval, 0, len(stored))
			for _, approval := range stored {
				out = append(out, fromRecord(approval))
			}
			return out
		}
		if b.log != nil {
			b.log.Warn("approval list fell back to in-process state", zap.Error(err))
		}
	}

	b.mu.Lock()
	out := make([]PendingApproval, 0, len(b.pending))
	for _, entry := range b.pending {
		if entry.workspaceID != workspaceID {
			continue
		}
		out = append(out, entry.meta)
	}
	b.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Resolve delivers a human's decision.
//
// Order matters and is the point: the durable decision is recorded FIRST, and
// only a recorded decision wakes the run. A broker that woke the run first and
// wrote afterwards would let two approvers both release the action — the
// store's single-use guard would refuse the second write, but the second
// wake-up would already have happened.
func (b *ConfirmBroker) Resolve(ctx context.Context, workspaceID, callID string, approved bool, by approvals.Eligibility, reason string) error {
	workspaceID = wsroot.Normalize(workspaceID)
	b.mu.Lock()
	store := b.store
	entry, present := b.pending[callID]
	b.mu.Unlock()

	if store != nil {
		if _, err := store.Decide(ctx, workspaceID, callID, approved, by, reason); err != nil {
			return err
		}
	} else {
		// No store: the map is the only record, so eligibility is checked
		// against it — through approvals.Authorize, the SAME function the
		// store uses. A second implementation here is how this path ends up
		// missing the workspace comparison, which is precisely the bug the
		// durable record was introduced to fix.
		if !present || entry.workspaceID != workspaceID {
			return approvals.ErrNotFound
		}
		if err := approvals.Authorize(approvals.Approval{
			WorkspaceID:      entry.workspaceID,
			RequiredResource: rbac.ResourceApprovals,
			RequiredAction:   rbac.ActionWrite,
		}, by); err != nil {
			return err
		}
	}

	b.mu.Lock()
	entry, present = b.pending[callID]
	if present {
		delete(b.pending, callID)
	}
	b.mu.Unlock()
	if present {
		entry.ch <- approved
	}
	// A decision with nothing listening is recorded, not an error: the run may
	// have moved to another process, or ended between the decision and here.
	// The record is what the approver was promised.
	return nil
}

// Decision returns the durable outcome of one approval, for the fingerprint
// check the engine makes before executing an approved call.
func (b *ConfirmBroker) Decision(ctx context.Context, workspaceID, callID string) (approvals.Approval, bool) {
	b.mu.Lock()
	store := b.store
	b.mu.Unlock()
	if store == nil {
		return approvals.Approval{}, false
	}
	approval, err := store.Get(ctx, workspaceID, callID)
	if err != nil {
		return approvals.Approval{}, false
	}
	return approval, true
}

func fromRecord(a approvals.Approval) PendingApproval {
	return PendingApproval{
		ID: a.ID, CallID: a.ID, WorkspaceID: a.WorkspaceID,
		Tool: a.Tool, Args: a.Args, Reason: a.Reason,
		AgentID: a.AgentID, SessionID: a.SessionID, RunID: a.RunID,
		Requester: a.RequesterSubject,
		CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt,
	}
}
