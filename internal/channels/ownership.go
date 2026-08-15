package channels

// ownership.go — which workspace a channel connection belongs to (MU-018).
//
// A channel connection is a bot token, a webhook URL, or a signed-in account.
// It is one tenant's: their Slack app, their Telegram bot, their inbox. The
// registry keyed adapters by channel ID alone, so nothing in the process
// recorded whose connection it was — inbound messages arrived with no tenant,
// and an outbound send routed on the channel name whoever asked for it.
//
// Two criteria follow from binding the connection instead:
//
//   - Inbound workspace identity comes from the *connection*, never from
//     message content (criterion 2). An external sender chooses their display
//     name, their user ID, and every byte of the message body. If any of that
//     could select a workspace, the tenant boundary would be an input field.
//   - Outbound sends verify ownership at execution time (criterion 4). A run
//     that has drifted — a scheduled job, a recovered message, a bug — must
//     not be able to speak through another tenant's bot, which to the
//     recipient is indistinguishable from that tenant speaking.

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// ErrChannelNotOwned reports an outbound send whose workspace does not own the
// channel it named.
var ErrChannelNotOwned = fmt.Errorf("channels: the channel is not owned by this workspace")

// ownership records channel-to-workspace bindings.
//
// The zero value is usable and means "everything is personal's", which is what
// a single-tenant deployment is and what this package assumed before.
type ownership struct {
	mu    sync.RWMutex
	owner map[string]string // channel ID → workspace ID
}

// Bind records that channelID belongs to workspaceID.
//
// Rebinding a channel to a different workspace is refused rather than
// overwritten. A channel that changes hands mid-process would leave in-flight
// messages attributed to the previous owner and new ones to the next, and no
// operator asked for that: a genuine hand-over is a disconnect and a
// reconnect, which is visible.
func (o *ownership) Bind(channelID, workspaceID string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return fmt.Errorf("channels: a channel id is required to bind ownership")
	}
	workspaceID = wsroot.Normalize(workspaceID)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owner == nil {
		o.owner = map[string]string{}
	}
	if existing, ok := o.owner[channelID]; ok && existing != workspaceID {
		return fmt.Errorf("channels: %q is already bound to workspace %q — disconnect it before binding it to %q",
			channelID, existing, workspaceID)
	}
	o.owner[channelID] = workspaceID
	return nil
}

// Unbind forgets a channel's binding, for a disconnected connection.
func (o *ownership) Unbind(channelID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.owner, strings.TrimSpace(channelID))
}

// WorkspaceOf returns the workspace a channel belongs to.
//
// An unbound channel is personal's, not "any". That is the correct default
// for the deployments that have no bindings at all — every existing
// single-tenant install — and it is the safe one for a multi-tenant install
// that forgot to bind: personal is the deployment's own workspace, so an
// unbound channel is attributed to the operator rather than to a tenant who
// never claimed it.
func (o *ownership) WorkspaceOf(channelID string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if workspace, ok := o.owner[strings.TrimSpace(channelID)]; ok {
		return workspace
	}
	return wsroot.PersonalWorkspaceID
}

// Bindings returns a copy of the current bindings, for diagnostics.
func (o *ownership) Bindings() map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make(map[string]string, len(o.owner))
	for channelID, workspaceID := range o.owner {
		out[channelID] = workspaceID
	}
	return out
}

// ChannelsOf lists the channels one workspace owns, sorted.
func (o *ownership) ChannelsOf(workspaceID string) []string {
	workspaceID = wsroot.Normalize(workspaceID)
	o.mu.RLock()
	defer o.mu.RUnlock()
	var out []string
	for channelID, owner := range o.owner {
		if owner == workspaceID {
			out = append(out, channelID)
		}
	}
	sort.Strings(out)
	return out
}
