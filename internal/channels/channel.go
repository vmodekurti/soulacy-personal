// Package channels defines the channel adapter interface and registry.
// A channel adapter is responsible for:
//  1. Connecting to a messaging platform.
//  2. Translating inbound platform events into canonical message.Message values.
//  3. Translating outbound message.Message values back into platform-specific sends.
//
// All adapters run concurrently. The gateway calls Start() on each enabled adapter
// at boot and Stop() on each during shutdown.
package channels

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/queue"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
	sdkchannel "github.com/soulacy/soulacy/sdk/channel"
)

// Adapter is the interface every channel must implement. Canonical
// definition lives in the versioned SDK (Story E9); this alias keeps every
// existing import path working unchanged.
type Adapter = sdkchannel.Adapter

// AdapterStatus describes the current connection state of a channel adapter.
type AdapterStatus = sdkchannel.AdapterStatus

// Registry holds all registered channel adapters and routes outbound messages.
//
// Currently Register is only called during boot, so reads after Register
// returns are race-free in practice. The RWMutex is here so a future
// enable/disable-channel-at-runtime flow can mutate the map without
// triggering Go's concurrent-map-write detector. (PRODUCTION_AUDIT → HIGH.)
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
	inbox    chan message.Message
	log      *zap.Logger
	// ingress is set in Team/Scale mode. Adapter traffic is published to the
	// deployment queue before it may enter the process-local worker inbox, so a
	// full gateway buffer causes broker redelivery instead of message loss.
	ingressMu      sync.RWMutex
	ingress        queue.Backend
	ingressSubject string
	ingressCtx     context.Context
	ingressSub     queue.Subscription
	receiptMu      sync.Mutex
	receipts       map[string]ingressReceipt
	pendingAcks    map[string]*queue.Message
	receiptSweep   uint64
	// owners records which workspace each channel connection belongs to
	// (MU-018). Empty means every channel is personal's, which is what a
	// single-tenant deployment is.
	owners ownership
}

type ingressReceipt struct {
	pending   bool
	expiresAt time.Time
}

type ingressReceiptState uint8

const (
	receiptNew ingressReceiptState = iota
	receiptPending
	receiptDelivered
	ingressReceiptTTL  = time.Hour
	maxIngressReceipts = 65_536
)

func durableIngressGroup(subject string) string {
	digest := sha256.Sum256([]byte(subject))
	return fmt.Sprintf("soulacy-channel-ingress-%x", digest[:6])
}

// EnableDurableIngress places the deployment queue in front of the local
// channel inbox. The subject must belong to the configured durable stream.
// It is boot wiring: swapping the broker while deliveries are in flight would
// make it impossible to know which backend owns their acknowledgements.
func (r *Registry) EnableDurableIngress(ctx context.Context, backend queue.Backend, subject string) error {
	if backend == nil {
		return fmt.Errorf("channels: durable ingress backend is required")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("channels: durable ingress subject is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.ingressMu.Lock()
	defer r.ingressMu.Unlock()
	if r.ingress != nil {
		return fmt.Errorf("channels: durable ingress is already enabled")
	}
	group := durableIngressGroup(subject)
	sub, err := backend.Subscribe(ctx, subject, group, func(delivery *queue.Message) {
		var msg message.Message
		if err := json.Unmarshal(delivery.Data, &msg); err != nil {
			// A malformed durable record can never become valid through retry.
			// Acknowledge it after recording the failure so it cannot become a
			// poison message that blocks the consumer forever.
			r.log.Error("durable channel ingress contains an invalid message",
				zap.String("subject", delivery.Subject), zap.Error(err))
			if err := delivery.Ack(); err != nil {
				r.log.Error("invalid durable ingress acknowledgement failed",
					zap.String("subject", delivery.Subject), zap.Error(err))
			}
			return
		}
		receiptKey, receiptState := r.reserveIngressReceipt(msg, delivery.Data)
		switch receiptState {
		case receiptDelivered:
			// Ack can fail after completed processing. A broker retry of the same
			// provider message must close that acknowledgement gap, not run the
			// agent twice.
			if err := delivery.Ack(); err != nil {
				r.log.Error("duplicate durable ingress acknowledgement failed",
					zap.String("msg_id", msg.ID), zap.String("channel", msg.Channel), zap.Error(err))
			}
			return
		case receiptPending:
			// Another concurrent delivery owns admission. Leaving this copy
			// unacknowledged lets the broker retry if that owner fails.
			return
		}
		r.receiptMu.Lock()
		if r.pendingAcks == nil {
			r.pendingAcks = make(map[string]*queue.Message)
		}
		r.pendingAcks[receiptKey] = delivery
		r.receiptMu.Unlock()
		if !r.enqueueDurableDelivery(msg) {
			r.releaseIngressReceipt(receiptKey)
			// Deliberately no Ack: JetStream/external durable backends redeliver
			// after capacity becomes available.
			return
		}
	})
	if err != nil {
		return fmt.Errorf("channels: subscribe durable ingress: %w", err)
	}
	r.ingress, r.ingressSubject, r.ingressCtx, r.ingressSub = backend, subject, ctx, sub
	r.log.Info("durable channel ingress ready", zap.String("subject", subject), zap.String("group", group))
	return nil
}

// CompleteInbound acknowledges a durable delivery after the router has
// finished processing it. Personal-mode/direct messages have no pending
// acknowledgement and are a no-op. Calling this before engine completion
// would recreate the crash-loss window durable ingress exists to close.
func (r *Registry) CompleteInbound(msg message.Message) {
	key := ingressReceiptKey(msg, nil)
	r.receiptMu.Lock()
	delivery := r.pendingAcks[key]
	delete(r.pendingAcks, key)
	r.receiptMu.Unlock()
	if delivery == nil {
		return
	}
	r.completeIngressReceipt(key)
	if err := delivery.Ack(); err != nil {
		r.log.Error("durable channel ingress acknowledgement failed",
			zap.String("msg_id", msg.ID), zap.String("channel", msg.Channel), zap.Error(err))
	}
}

func ingressReceiptKey(msg message.Message, data []byte) string {
	if strings.TrimSpace(msg.ID) != "" {
		return strings.TrimSpace(msg.WorkspaceID) + "\x00" + strings.TrimSpace(msg.Channel) + "\x00" + strings.TrimSpace(msg.ID)
	}
	if len(data) == 0 {
		data, _ = json.Marshal(msg)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("payload:%x", digest[:])
}

func (r *Registry) reserveIngressReceipt(msg message.Message, data []byte) (string, ingressReceiptState) {
	key := ingressReceiptKey(msg, data)
	now := time.Now()
	r.receiptMu.Lock()
	defer r.receiptMu.Unlock()
	if existing, ok := r.receipts[key]; ok {
		if existing.pending {
			// Active work has no arbitrary TTL: agents may declare runs longer
			// than the usual timeout, and expiry during execution would admit a
			// concurrent duplicate.
			return key, receiptPending
		}
		if existing.expiresAt.After(now) {
			return key, receiptDelivered
		}
	}
	if r.receipts == nil {
		r.receipts = make(map[string]ingressReceipt)
	}
	r.receiptSweep++
	if r.receiptSweep%1024 == 0 || len(r.receipts) >= maxIngressReceipts {
		for candidate, receipt := range r.receipts {
			if !receipt.pending && !receipt.expiresAt.After(now) {
				delete(r.receipts, candidate)
			}
		}
	}
	if len(r.receipts) >= maxIngressReceipts {
		// Prefer evicting the oldest COMPLETED duplicate receipt. Active
		// deliveries are never evicted; if all 65k are active, broker
		// backpressure is safer than losing completion correlation.
		oldestKey := ""
		var oldestExpiry time.Time
		for candidate, receipt := range r.receipts {
			if receipt.pending {
				continue
			}
			if oldestKey == "" || receipt.expiresAt.Before(oldestExpiry) {
				oldestKey, oldestExpiry = candidate, receipt.expiresAt
			}
		}
		if oldestKey == "" {
			return "", receiptPending
		}
		delete(r.receipts, oldestKey)
	}
	r.receipts[key] = ingressReceipt{pending: true}
	return key, receiptNew
}

func (r *Registry) releaseIngressReceipt(key string) {
	if key == "" {
		return
	}
	r.receiptMu.Lock()
	delete(r.receipts, key)
	delete(r.pendingAcks, key)
	r.receiptMu.Unlock()
}

func (r *Registry) completeIngressReceipt(key string) {
	if key == "" {
		return
	}
	r.receiptMu.Lock()
	r.receipts[key] = ingressReceipt{expiresAt: time.Now().Add(ingressReceiptTTL)}
	r.receiptMu.Unlock()
}

// NewRegistry creates an empty channel registry with a shared inbox.
func NewRegistry(inboxBufferSize int) *Registry {
	return &Registry{
		adapters: make(map[string]Adapter),
		inbox:    make(chan message.Message, inboxBufferSize),
		log:      zap.NewNop(),
	}
}

// SetLogger attaches a zap logger to the registry for drop-event logging.
// Call this once at boot before messages start flowing.
func (r *Registry) SetLogger(l *zap.Logger) {
	r.log = l.Named("channels")
}

// Register adds an adapter. It will be started when StartAll is called.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.ID()] = a
}

// BindWorkspace records that a channel connection belongs to one workspace
// (MU-018 criterion 1). Call it alongside Register; an unbound channel is the
// personal workspace's.
func (r *Registry) BindWorkspace(channelID, workspaceID string) error {
	return r.owners.Bind(channelID, workspaceID)
}

// WorkspaceOf reports which workspace owns a channel.
func (r *Registry) WorkspaceOf(channelID string) string { return r.owners.WorkspaceOf(channelID) }

// ChannelsOf lists the channels one workspace owns.
func (r *Registry) ChannelsOf(workspaceID string) []string { return r.owners.ChannelsOf(workspaceID) }

// stampedInbox returns a channel an adapter may write to, whose messages are
// re-stamped with the connection's workspace before reaching the shared inbox.
//
// This is why adapters are not simply handed r.inbox any more. An adapter
// receives whatever an external sender wrote — display name, user ID, body,
// and any field a hostile or merely buggy adapter chooses to fill in. If the
// workspace could survive that journey, the tenant boundary would be an input
// field. Stamping *after* the adapter is done means there is no code path in
// which content selects a tenant, rather than a rule saying it must not.
func (r *Registry) stampedInbox(channelID string) chan message.Message {
	// Unbuffered on purpose: hosted adapters may not build a second volatile
	// backlog in front of the durable ingress WAL. The handoff applies
	// backpressure immediately when persistence is unavailable.
	staged := make(chan message.Message)
	go func() {
		for msg := range staged {
			msg.WorkspaceID = r.owners.WorkspaceOf(channelID)
			if msg.Channel == "" {
				msg.Channel = channelID
			}
			r.Enqueue(msg)
		}
	}()
	return staged
}

// Inbox returns the shared inbound message channel (read by the gateway router).
func (r *Registry) Inbox() <-chan message.Message { return r.inbox }

// Enqueue posts a message through durable ingress when configured, otherwise
// directly onto the shared inbox without blocking. Used by
// the gateway's startup crash-recovery (re-injecting in-flight runs that
// the previous process didn't get to finish) and by any future internal
// caller that needs to fan messages into the worker pool.
//
// Returns false if the inbox is full. We never block the caller: the
// recovery code is allowed to drop replays under heavy startup load, and
// the operator can re-trigger them manually if they care.
// (PRODUCTION_AUDIT → F2, 2026-05-27)
func (r *Registry) Enqueue(msg message.Message) bool {
	r.ingressMu.RLock()
	backend, subject, ctx := r.ingress, r.ingressSubject, r.ingressCtx
	r.ingressMu.RUnlock()
	if backend != nil {
		data, err := json.Marshal(msg)
		if err != nil {
			r.log.Error("channel message could not be encoded for durable ingress",
				zap.String("msg_id", msg.ID), zap.String("channel", msg.Channel), zap.Error(err))
			return false
		}
		publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := backend.Publish(publishCtx, subject, data); err != nil {
			r.log.Error("channel message could not be persisted to durable ingress",
				zap.String("msg_id", msg.ID), zap.String("channel", msg.Channel), zap.Error(err))
			return false
		}
		return true
	}
	return r.enqueueLive(msg)
}

func (r *Registry) enqueueLive(msg message.Message) bool {
	return r.tryEnqueueLocal(msg, false)
}

func (r *Registry) enqueueDurableDelivery(msg message.Message) bool {
	return r.tryEnqueueLocal(msg, true)
}

func (r *Registry) tryEnqueueLocal(msg message.Message, durable bool) bool {
	select {
	case r.inbox <- msg:
		metrics.ChannelInboundTotal.WithLabelValues(channelMetricLabel(msg.Channel)).Inc()
		return true
	default:
		if durable {
			r.log.Warn("local channel inbox full — durable delivery deferred",
				zap.String("msg_id", msg.ID), zap.String("agent_id", msg.AgentID),
				zap.String("channel", channelMetricLabel(msg.Channel)), zap.Int("inbox_cap", cap(r.inbox)))
			return false
		}
		// Inbox is full — increment the Prometheus drop counter and log at
		// ERROR level so operators can alert on this condition. A sustained
		// non-zero rate here means the inbox buffer (config: channel.inbox_buffer)
		// is too small for the current load, or agent processing has stalled.
		ch := msg.Channel
		if ch == "" {
			ch = "unknown"
		}
		metrics.ChannelInboxDropsTotal.WithLabelValues(ch).Inc()
		r.log.Error("channel inbox full — message dropped",
			zap.String("msg_id", msg.ID),
			zap.String("agent_id", msg.AgentID),
			zap.String("session_id", msg.SessionID),
			zap.String("channel", ch),
			zap.Int("inbox_cap", cap(r.inbox)),
		)
		return false
	}
}

// StartAll starts all registered adapters. Adapters that fail to start are logged
// but do not prevent others from starting.
func (r *Registry) StartAll(ctx context.Context) []error {
	// Snapshot under lock so Start() can't observe a partially-populated map
	// (and so a Start() that takes a while doesn't block Register).
	r.mu.RLock()
	snapshot := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		snapshot = append(snapshot, a)
	}
	r.mu.RUnlock()

	var errs []error
	for _, a := range snapshot {
		if err := a.Start(ctx, r.stampedInbox(a.ID())); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// StartAdapter registers and starts one adapter immediately. It is used by
// channel setup flows that can connect without a full gateway restart.
func (r *Registry) StartAdapter(ctx context.Context, a Adapter) error {
	r.mu.Lock()
	old := r.adapters[a.ID()]
	r.adapters[a.ID()] = a
	r.mu.Unlock()

	if old != nil {
		_ = old.Stop()
	}

	if err := a.Start(ctx, r.stampedInbox(a.ID())); err != nil {
		r.mu.Lock()
		if r.adapters[a.ID()] == a {
			delete(r.adapters, a.ID())
		}
		r.mu.Unlock()
		return err
	}
	return nil
}

// StopAdapter removes one adapter and stops it, so a channel can be disabled
// without restarting the gateway.
//
// ITS ABSENCE WAS THE ASYMMETRY THAT KEPT CHANNELS BOOT-ONLY. StartAdapter has
// existed for a while and is used live by the WhatsApp pairing flow, so
// connecting a channel without a restart was already possible. Disconnecting
// one was not, and a save path can only be hot if BOTH directions are — an
// operator who can enable a channel live but must restart to disable it has a
// restart in their workflow either way, which is why the handlers went on
// telling them to restart for both.
//
// REMOVED FROM THE REGISTRY FIRST, under the lock, and stopped afterwards.
// Same ordering as MCP's RemoveServer and for the same reason: disabling a
// channel has to take effect immediately, so no new send may resolve it, and
// holding the registry lock across a stranger's network shutdown would block
// every other channel operation for the length of it.
//
// Returns false when there was no such adapter. Not an error: disabling a
// channel that was never started is the ordinary case for a config edit made
// before the adapter could connect, and reporting it as a failure would make
// the GUI show a red state for a successful change.
func (r *Registry) StopAdapter(channelID string) (bool, error) {
	channelID = strings.TrimSpace(channelID)
	r.mu.Lock()
	adapter, present := r.adapters[channelID]
	if present {
		delete(r.adapters, channelID)
	}
	r.mu.Unlock()

	if !present {
		return false, nil
	}
	if err := adapter.Stop(); err != nil {
		// Already out of the registry, so the disable HAS taken effect even
		// though the adapter complained on the way down. Reported so an
		// operator sees a lingering connection in the provider's dashboard and
		// knows why, rather than concluding the disable did not work.
		return true, err
	}
	return true, nil
}

// Adapters returns a snapshot of the registered adapters.
//
// Exists so a caller can BUILD a channel's adapters without starting them and
// then ask what it got — which is how a hot channel reload learns the adapter
// IDs a config produces, without a naming convention. Adapter IDs are derived
// from the config (a `bots:` list becomes several adapters with suffixed IDs),
// so "which adapters belong to channel X" is a question only the construction
// path can answer; inferring it from an ID prefix would be a rule every future
// channel type has to remember to obey.
func (r *Registry) Adapters() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		out = append(out, adapter)
	}
	return out
}

// StopAll gracefully stops all adapters.
func (r *Registry) StopAll() []error {
	r.mu.RLock()
	snapshot := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		snapshot = append(snapshot, a)
	}
	r.mu.RUnlock()

	var errs []error
	for _, a := range snapshot {
		if err := a.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	r.ingressMu.Lock()
	if r.ingressSub != nil {
		if err := r.ingressSub.Unsubscribe(); err != nil {
			errs = append(errs, err)
		}
		r.ingressSub = nil
	}
	r.ingressMu.Unlock()
	return errs
}

// Send routes an outbound message to the correct channel adapter.
func (r *Registry) Send(ctx context.Context, msg message.Message) error {
	ch := channelMetricLabel(msg.Channel)
	r.mu.RLock()
	a, ok := r.adapters[msg.Channel]
	r.mu.RUnlock()
	if !ok {
		metrics.ChannelOutboundTotal.WithLabelValues(ch, "unregistered").Inc()
		return fmt.Errorf("channel adapter %q is not registered", msg.Channel)
	}
	// Ownership is checked here, at execution time, rather than when the run
	// was admitted (MU-018 criterion 4). A message can be minutes or days old
	// by the time it is sent — scheduled deliveries, recovered runs, retries —
	// and speaking through another tenant's bot is indistinguishable, to the
	// recipient, from that tenant speaking.
	if owner := r.owners.WorkspaceOf(msg.Channel); owner != wsroot.Normalize(msg.WorkspaceID) {
		metrics.ChannelOutboundTotal.WithLabelValues(ch, "not_owned").Inc()
		// Redacted diagnostic (criterion 6): the channel, the two workspaces
		// and the agent, never the message body — a refused send is exactly
		// the case where the content is most likely to be somebody else's.
		r.log.Error("outbound send refused: channel is not owned by the sending workspace",
			zap.String("channel", msg.Channel),
			zap.String("channel_workspace", owner),
			zap.String("message_workspace", wsroot.Normalize(msg.WorkspaceID)),
			zap.String("agent_id", msg.AgentID),
			zap.String("msg_id", msg.ID),
		)
		return fmt.Errorf("%w: channel %q belongs to another workspace", ErrChannelNotOwned, msg.Channel)
	}
	if err := a.Send(ctx, msg); err != nil {
		metrics.ChannelOutboundTotal.WithLabelValues(ch, "error").Inc()
		return err
	}
	metrics.ChannelOutboundTotal.WithLabelValues(ch, "success").Inc()
	return nil
}

func channelMetricLabel(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// Statuses returns a map of adapter ID → status for the admin API.
func (r *Registry) Statuses() map[string]AdapterStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[string]AdapterStatus, len(r.adapters))
	for id, a := range r.adapters {
		m[id] = a.Status()
	}
	return m
}
