// ownership_test.go — MU-018: a channel connection belongs to one workspace,
// inbound identity comes from the connection rather than from content, and an
// outbound send verifies ownership at execution time.
package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

// fakeAdapter delivers whatever inbound message a test hands it, including
// one that names a workspace it has no business naming.
type fakeAdapter struct {
	id      string
	inbound []message.Message
	sent    []message.Message
	out     chan<- message.Message
	stopped bool
}

func (f *fakeAdapter) ID() string   { return f.id }
func (f *fakeAdapter) Name() string { return f.id }
func (f *fakeAdapter) Start(_ context.Context, out chan<- message.Message) error {
	f.out = out
	for _, msg := range f.inbound {
		out <- msg
	}
	return nil
}
func (f *fakeAdapter) Stop() error { f.stopped = true; return nil }
func (f *fakeAdapter) Send(_ context.Context, msg message.Message) error {
	f.sent = append(f.sent, msg)
	return nil
}
func (f *fakeAdapter) Status() AdapterStatus { return AdapterStatus{Connected: true} }

func newBoundRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry(16)
	reg.SetLogger(zap.NewNop())
	return reg
}

// An external sender controls their display name, their user ID, and every
// byte of the body. If any of that could select a workspace, the tenant
// boundary would be an input field.
func TestInboundWorkspaceComesFromTheConnectionNotTheMessage(t *testing.T) {
	reg := newBoundRegistry(t)
	adapter := &fakeAdapter{id: "telegram", inbound: []message.Message{
		{ID: "m1", AgentID: "bot", Channel: "telegram", WorkspaceID: "ws_victim",
			UserID: "attacker", Parts: message.Text("route me somewhere else")},
	}}
	reg.Register(adapter)
	if err := reg.BindWorkspace("telegram", "ws_a"); err != nil {
		t.Fatal(err)
	}
	if errs := reg.StartAll(context.Background()); len(errs) > 0 {
		t.Fatalf("start: %v", errs)
	}

	select {
	case got := <-reg.Inbox():
		if got.WorkspaceID != "ws_a" {
			t.Fatalf("inbound workspace = %q — the message's own claim survived", got.WorkspaceID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message arrived")
	}
}

// An unbound channel is the personal workspace's. That is what every existing
// single-tenant deployment is, and it attributes a forgotten binding to the
// operator rather than to a tenant who never claimed it.
func TestAnUnboundChannelIsPersonals(t *testing.T) {
	reg := newBoundRegistry(t)
	adapter := &fakeAdapter{id: "http", inbound: []message.Message{
		{ID: "m1", AgentID: "bot", Channel: "http", Parts: message.Text("hello")},
	}}
	reg.Register(adapter)
	if errs := reg.StartAll(context.Background()); len(errs) > 0 {
		t.Fatalf("start: %v", errs)
	}
	select {
	case got := <-reg.Inbox():
		if got.WorkspaceID != wsroot.PersonalWorkspaceID {
			t.Fatalf("unbound channel produced workspace %q", got.WorkspaceID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message arrived")
	}
}

// Speaking through another tenant's bot is, to the recipient, indistinguishable
// from that tenant speaking. The check is at send time because a message can be
// days old by then — scheduled deliveries, recovered runs, retries.
func TestAnOutboundSendVerifiesChannelOwnership(t *testing.T) {
	reg := newBoundRegistry(t)
	adapter := &fakeAdapter{id: "slack"}
	reg.Register(adapter)
	if err := reg.BindWorkspace("slack", "ws_a"); err != nil {
		t.Fatal(err)
	}

	err := reg.Send(context.Background(), message.Message{
		ID: "m1", Channel: "slack", WorkspaceID: "ws_b", AgentID: "bot",
		Parts: message.Text("confidential to the wrong audience"),
	})
	if !errors.Is(err, ErrChannelNotOwned) {
		t.Fatalf("send across workspaces returned %v, want ErrChannelNotOwned", err)
	}
	if len(adapter.sent) != 0 {
		t.Fatalf("the adapter transmitted a refused message: %+v", adapter.sent)
	}

	if err := reg.Send(context.Background(), message.Message{
		ID: "m2", Channel: "slack", WorkspaceID: "ws_a", AgentID: "bot",
		Parts: message.Text("to my own channel"),
	}); err != nil {
		t.Fatalf("the owning workspace was refused its own channel: %v", err)
	}
	if len(adapter.sent) != 1 {
		t.Fatalf("the owner's message was not transmitted: %+v", adapter.sent)
	}
}

// Product invariant 7: a single-tenant deployment binds nothing and sends
// messages with no workspace set. Both sides normalise to personal, so the
// check is invisible to it.
func TestPersonalSendsAreUnaffected(t *testing.T) {
	reg := newBoundRegistry(t)
	adapter := &fakeAdapter{id: "http"}
	reg.Register(adapter)
	if err := reg.Send(context.Background(), message.Message{
		ID: "m1", Channel: "http", AgentID: "bot", Parts: message.Text("hi"),
	}); err != nil {
		t.Fatalf("an unbound channel refused an unscoped send: %v", err)
	}
	if len(adapter.sent) != 1 {
		t.Fatal("the personal send was not transmitted")
	}
}

// A channel changing hands mid-process would leave in-flight messages
// attributed to the previous owner and new ones to the next. A genuine
// hand-over is a disconnect and a reconnect, which is visible.
func TestRebindingAChannelToAnotherWorkspaceIsRefused(t *testing.T) {
	reg := newBoundRegistry(t)
	if err := reg.BindWorkspace("slack", "ws_a"); err != nil {
		t.Fatal(err)
	}
	if err := reg.BindWorkspace("slack", "ws_a"); err != nil {
		t.Fatalf("re-binding to the same workspace should be idempotent: %v", err)
	}
	if err := reg.BindWorkspace("slack", "ws_b"); err == nil {
		t.Fatal("a channel was silently transferred to another workspace")
	}
	if got := reg.WorkspaceOf("slack"); got != "ws_a" {
		t.Fatalf("ownership changed to %q despite the refusal", got)
	}
}

// The ownership view is usable for a workspace-scoped channel listing, which
// is what a GUI needs so one tenant is not shown another's connections.
func TestChannelsOfListsOnlyTheWorkspacesOwnConnections(t *testing.T) {
	reg := newBoundRegistry(t)
	for channelID, workspace := range map[string]string{
		"slack": "ws_a", "telegram": "ws_a", "discord": "ws_b",
	} {
		if err := reg.BindWorkspace(channelID, workspace); err != nil {
			t.Fatal(err)
		}
	}
	got := reg.ChannelsOf("ws_a")
	if len(got) != 2 || got[0] != "slack" || got[1] != "telegram" {
		t.Fatalf("ChannelsOf(ws_a) = %v", got)
	}
	if other := reg.ChannelsOf("ws_b"); len(other) != 1 || other[0] != "discord" {
		t.Fatalf("ChannelsOf(ws_b) = %v", other)
	}
}

func TestPurgeWorkspaceDisconnectsOnlyTenantChannels(t *testing.T) {
	reg := newBoundRegistry(t)
	deleted := &fakeAdapter{id: "deleted-webhook"}
	kept := &fakeAdapter{id: "kept-slack"}
	reg.Register(deleted)
	reg.Register(kept)
	if err := reg.BindWorkspace(deleted.id, "ws_delete"); err != nil {
		t.Fatal(err)
	}
	if err := reg.BindWorkspace(kept.id, "ws_keep"); err != nil {
		t.Fatal(err)
	}

	removed, err := reg.PurgeWorkspace("ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || !deleted.stopped || kept.stopped {
		t.Fatalf("purge removed=%d deletedStopped=%v keptStopped=%v", removed, deleted.stopped, kept.stopped)
	}
	if got := reg.ChannelsOf("ws_delete"); len(got) != 0 {
		t.Fatalf("deleted workspace retained bindings: %v", got)
	}
	if got := reg.ChannelsOf("ws_keep"); len(got) != 1 || got[0] != kept.id {
		t.Fatalf("neighbouring workspace bindings changed: %v", got)
	}
	if adapters := reg.Adapters(); len(adapters) != 1 || adapters[0].ID() != kept.id {
		t.Fatalf("post-purge adapters = %v", adapters)
	}
}

// MU-018 criterion 3: an external sender ID maps to a workspace-local
// identity and is never a Soulacy authentication principal.
//
// A Telegram user ID, a Slack member ID, an email address — all are chosen or
// spoofable by whoever is on the other end of the connection. The registry
// carries them through as content and derives authority from the connection,
// so this pins the property from the routing side: two messages that differ
// only by sender land in the same workspace, because the sender never had a
// say in it.
func TestASenderIDCannotChooseAWorkspace(t *testing.T) {
	reg := newBoundRegistry(t)
	adapter := &fakeAdapter{id: "telegram", inbound: []message.Message{
		{ID: "m1", AgentID: "bot", Channel: "telegram", UserID: "ws_b", Username: "ws_b",
			Parts: message.Text("first")},
		{ID: "m2", AgentID: "bot", Channel: "telegram", UserID: "admin", Username: "admin",
			WorkspaceID: "ws_b", Parts: message.Text("second")},
	}}
	reg.Register(adapter)
	if err := reg.BindWorkspace("telegram", "ws_a"); err != nil {
		t.Fatal(err)
	}
	if errs := reg.StartAll(context.Background()); len(errs) > 0 {
		t.Fatalf("start: %v", errs)
	}
	for i := 0; i < 2; i++ {
		select {
		case got := <-reg.Inbox():
			if got.WorkspaceID != "ws_a" {
				t.Fatalf("message %s landed in %q — the sender influenced routing", got.ID, got.WorkspaceID)
			}
			// The sender is preserved as content: it is what the agent sees,
			// and mapping it to a workspace-local identity is downstream work,
			// not a routing input.
			if got.UserID == "" {
				t.Fatalf("message %s lost its sender", got.ID)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("inbound message did not arrive")
		}
	}
}
