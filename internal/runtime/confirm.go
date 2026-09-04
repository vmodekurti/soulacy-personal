package runtime

import (
	"context"
	"sort"
	"sync"
	"time"
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

// PendingApproval is the device-agnostic view of a tool call awaiting a human
// decision. It is what the /approvals API and the mobile companion render so any
// paired device — not just the one that started the run — can approve or deny.
type PendingApproval struct {
	CallID    string         `json:"call_id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Principal string         `json:"-"`
	CreatedAt time.Time      `json:"created_at"`
}

type pendingEntry struct {
	ch   chan bool
	meta PendingApproval
}

// ConfirmBroker maps call IDs to pending approval channels and their metadata.
// The gateway registers a result channel when it emits the SSE event; the
// confirm/approvals endpoints resolve it when a user approves or denies from any
// device. onRegister, when set, is called for each newly pending approval (used
// to fan out a web-push notification).
type ConfirmBroker struct {
	mu         sync.Mutex
	pending    map[string]*pendingEntry
	onRegister func(PendingApproval)
}

// newConfirmBroker allocates a ConfirmBroker.
func newConfirmBroker() *ConfirmBroker {
	return &ConfirmBroker{pending: make(map[string]*pendingEntry)}
}

// SetOnRegister installs a callback fired whenever a new approval becomes
// pending. Safe to call once at startup.
func (b *ConfirmBroker) SetOnRegister(fn func(PendingApproval)) {
	b.mu.Lock()
	b.onRegister = fn
	b.mu.Unlock()
}

// Register stores a result channel for callID and returns it. Retained for
// backward compatibility; prefer RegisterRequest so the approval carries
// metadata visible to other devices.
func (b *ConfirmBroker) Register(callID string) chan bool {
	return b.RegisterRequest(ConfirmRequest{CallID: callID}, "", "")
}

// RegisterRequest stores a result channel plus the request metadata and returns
// the channel. The engine blocks on it until Resolve is called.
func (b *ConfirmBroker) RegisterRequest(req ConfirmRequest, agentID, sessionID string) chan bool {
	return b.RegisterRequestForPrincipal(req, agentID, sessionID, "")
}

func (b *ConfirmBroker) RegisterRequestForPrincipal(req ConfirmRequest, agentID, sessionID, principal string) chan bool {
	ch := make(chan bool, 1)
	meta := PendingApproval{
		CallID:    req.CallID,
		Tool:      req.Tool,
		Args:      req.Args,
		Reason:    req.Reason,
		AgentID:   agentID,
		SessionID: sessionID,
		Principal: principal,
		CreatedAt: time.Now().UTC(),
	}
	b.mu.Lock()
	b.pending[req.CallID] = &pendingEntry{ch: ch, meta: meta}
	fn := b.onRegister
	b.mu.Unlock()
	if fn != nil {
		go fn(meta)
	}
	return ch
}

// Forget drops a pending approval that nobody will ever answer.
//
// Resolve was the ONLY deletion, so every run that timed out or was cancelled
// with a confirmation outstanding left a permanent map entry — holding the full
// tool-call arguments, and still listed by GET /api/v1/approvals as though a
// human could still act on it. On a long-lived gateway that is unbounded growth
// plus a steadily more misleading approvals page.
//
// Returns whether an entry was actually removed.
func (b *ConfirmBroker) Forget(callID string) bool {
	b.mu.Lock()
	_, ok := b.pending[callID]
	delete(b.pending, callID)
	b.mu.Unlock()
	return ok
}

// List returns all currently pending approvals, newest first.
func (b *ConfirmBroker) List() []PendingApproval {
	return b.ListForPrincipal("", true)
}

func (b *ConfirmBroker) ListForPrincipal(principal string, admin bool) []PendingApproval {
	b.mu.Lock()
	out := make([]PendingApproval, 0, len(b.pending))
	for _, e := range b.pending {
		if !admin && (principal == "" || e.meta.Principal != principal) {
			continue
		}
		out = append(out, e.meta)
	}
	b.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Resolve delivers the user's decision (approved) for callID.
// Returns true if callID was found and the decision was delivered.
func (b *ConfirmBroker) Resolve(callID string, approved bool) bool {
	return b.ResolveForPrincipal(callID, approved, "", true)
}

func (b *ConfirmBroker) ResolveForPrincipal(callID string, approved bool, principal string, admin bool) bool {
	b.mu.Lock()
	e, ok := b.pending[callID]
	if ok && !admin && (principal == "" || e.meta.Principal != principal) {
		ok = false
	}
	if ok {
		delete(b.pending, callID)
	}
	b.mu.Unlock()
	if ok {
		e.ch <- approved
	}
	return ok
}
