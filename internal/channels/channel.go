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
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
	sdkchannel "github.com/soulacy/soulacy/sdk/channel"
	"strings"
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
	// owners records which workspace each channel connection belongs to
	// (MU-018). Empty means every channel is personal's, which is what a
	// single-tenant deployment is.
	owners ownership
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
	staged := make(chan message.Message, 16)
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

// Enqueue posts a message onto the shared inbox without blocking. Used by
// the gateway's startup crash-recovery (re-injecting in-flight runs that
// the previous process didn't get to finish) and by any future internal
// caller that needs to fan messages into the worker pool.
//
// Returns false if the inbox is full. We never block the caller: the
// recovery code is allowed to drop replays under heavy startup load, and
// the operator can re-trigger them manually if they care.
// (PRODUCTION_AUDIT → F2, 2026-05-27)
func (r *Registry) Enqueue(msg message.Message) bool {
	select {
	case r.inbox <- msg:
		metrics.ChannelInboundTotal.WithLabelValues(channelMetricLabel(msg.Channel)).Inc()
		return true
	default:
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
