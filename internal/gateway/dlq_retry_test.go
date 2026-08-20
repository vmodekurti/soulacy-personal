// dlq_retry_test.go — retrying a parked job must not cross a tenant boundary.
//
// A dead letter's payload is a serialized message.Message, and that struct has
// a WorkspaceID field. Reading it is the obvious implementation and it is the
// bug: the payload is CONTENT — bytes written into a row — while the row's own
// workspace_id was recorded by the gateway from a verified principal. The two
// can disagree, and the disagreement is the attack.
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/pkg/message"
)

// fakeDLQ is an in-memory Store that scopes by workspace exactly as the
// SQLite one does, so a handler bug cannot be masked by a permissive fake.
type fakeDLQ struct {
	entries  map[string]map[string]dlq.DeadLetter // workspace → id → entry
	deleted  []string
	failNext bool
}

func newFakeDLQ() *fakeDLQ {
	return &fakeDLQ{entries: map[string]map[string]dlq.DeadLetter{}}
}

func (f *fakeDLQ) put(entry dlq.DeadLetter) {
	if f.entries[entry.WorkspaceID] == nil {
		f.entries[entry.WorkspaceID] = map[string]dlq.DeadLetter{}
	}
	f.entries[entry.WorkspaceID][entry.ID] = entry
}

func (f *fakeDLQ) Push(context.Context, dlq.DeadLetter) error { return nil }

func (f *fakeDLQ) List(_ context.Context, workspaceID, _ string) ([]dlq.DeadLetter, error) {
	var out []dlq.DeadLetter
	for _, entry := range f.entries[workspaceID] {
		out = append(out, entry)
	}
	return out, nil
}

func (f *fakeDLQ) Get(_ context.Context, workspaceID, id string) (dlq.DeadLetter, error) {
	if entry, ok := f.entries[workspaceID][id]; ok {
		return entry, nil
	}
	return dlq.DeadLetter{}, dlq.ErrNotFound
}

func (f *fakeDLQ) Delete(_ context.Context, workspaceID, id string) error {
	if f.failNext {
		return context.DeadlineExceeded
	}
	if _, ok := f.entries[workspaceID][id]; !ok {
		return dlq.ErrNotFound
	}
	delete(f.entries[workspaceID], id)
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeDLQ) Close() error { return nil }

func retryServer(t *testing.T, store *fakeDLQ, inboxSize int) (*Server, *fiber.App, *channels.Registry) {
	t.Helper()
	reg := channels.NewRegistry(inboxSize)
	s := &Server{log: zap.NewNop(), dlqStore: store, channels: reg}
	app := fiber.New(fiber.Config{DisableStartupMessage: true, Immutable: true})
	app.Post("/:ws/:id", func(c *fiber.Ctx) error {
		identity, err := requestctx.New(requestctx.Input{
			Subject: "u", OrganizationID: "org", WorkspaceID: c.Params("ws"),
			MembershipID: "m", Role: "owner", RequestID: "r",
		})
		if err != nil {
			t.Fatal(err)
		}
		c.Locals(workspaceIdentityLocal, identity)
		return s.handleRetryDeadLetter(c)
	})
	return s, app, reg
}

func parkedEntry(t *testing.T, id, rowWorkspace, payloadWorkspace, agentID string) dlq.DeadLetter {
	t.Helper()
	payload, err := json.Marshal(message.Message{
		ID: "m1", AgentID: agentID, Channel: "slack",
		WorkspaceID: payloadWorkspace, Parts: message.Text("do the thing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dlq.DeadLetter{
		ID: id, WorkspaceID: rowWorkspace, Queue: "agent", Payload: payload,
		ErrorMsg: "provider timeout", Attempts: 2,
		CreatedAt: time.Now().UTC(), LastAttemptAt: time.Now().UTC(),
	}
}

func TestATamperedPayloadCannotRedirectARetryIntoAnotherWorkspace(t *testing.T) {
	store := newFakeDLQ()
	// The row says ws-a. The payload claims ws-victim. A handler that read the
	// payload would enqueue a message stamped for a tenant that never asked
	// for it, and the engine would run it under that tenant's agents.
	store.put(parkedEntry(t, "d1", "ws-a", "ws-victim", "reporter"))
	_, app, reg := retryServer(t, store, 4)

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/ws-a/d1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp.StatusCode)
	}

	select {
	case msg := <-reg.Inbox():
		if msg.WorkspaceID != "ws-a" {
			t.Errorf("the retried message was stamped %q — the payload's claim overrode the row's "+
				"recorded workspace", msg.WorkspaceID)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was enqueued")
	}
}

func TestAnotherTenantsDeadLetterIsNotFound(t *testing.T) {
	store := newFakeDLQ()
	store.put(parkedEntry(t, "d1", "ws-a", "ws-a", "reporter"))
	_, app, _ := retryServer(t, store, 4)

	// ws-b asking for ws-a's entry gets the same answer as for an entry that
	// does not exist. Distinguishing them would make dead-letter IDs an
	// enumeration oracle over another tenant's failures.
	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/ws-b/d1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if len(store.deleted) != 0 {
		t.Error("a cross-tenant retry deleted the owner's entry")
	}
}

func TestAFullInboxLeavesTheEntryParked(t *testing.T) {
	store := newFakeDLQ()
	store.put(parkedEntry(t, "d1", "ws-a", "ws-a", "reporter"))
	// Capacity zero: nothing can be enqueued.
	_, app, _ := retryServer(t, store, 0)

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/ws-a/d1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	// The important half. Deleting an entry the retry could not enqueue would
	// destroy the only record of the job, and a full inbox is exactly the
	// condition under which an operator is retrying things.
	if len(store.deleted) != 0 {
		t.Error("a failed retry discarded the parked entry, losing the job permanently")
	}
	if _, err := store.Get(context.Background(), "ws-a", "d1"); err != nil {
		t.Errorf("the entry is gone: %v", err)
	}
}

func TestASuccessfulRetryDiscardsTheParkedEntry(t *testing.T) {
	// Otherwise the same job is retried by the next operator who reads the
	// list, and neither of them can tell it already ran.
	store := newFakeDLQ()
	store.put(parkedEntry(t, "d1", "ws-a", "ws-a", "reporter"))
	_, app, reg := retryServer(t, store, 4)

	if _, err := app.Test(httptest.NewRequest(http.MethodPost, "/ws-a/d1", nil)); err != nil {
		t.Fatal(err)
	}
	<-reg.Inbox()

	if len(store.deleted) != 1 {
		t.Errorf("the entry was not discarded after a successful retry: %v", store.deleted)
	}
}

func TestAnUnparseablePayloadIsReportedAsUnreplayable(t *testing.T) {
	// A 500 would invite the operator to try again; 422 tells them reading it
	// once more will not help and discarding is the next move.
	store := newFakeDLQ()
	store.put(dlq.DeadLetter{ID: "d1", WorkspaceID: "ws-a", Queue: "agent", Payload: []byte("{not json")})
	_, app, _ := retryServer(t, store, 4)

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/ws-a/d1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}
