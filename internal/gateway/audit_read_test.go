// audit_read_test.go — MU-031 criterion 4: reads of the audit trail are
// themselves audited.
//
// Reading an audit trail is not a neutral act. It is how somebody learns what
// is being watched, and — for an insider — the step before deciding whether an
// action will be noticed. It left no trace at all, so the one question the
// trail could not answer was "who has been looking at it".
package gateway

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/storage"
)

// recordingLog captures what the audit path writes.
type recordingLog struct {
	storage.ActionLogBackend
	written []message.Event
	replay  []message.Event
}

func (r *recordingLog) Append(ev message.Event) { r.written = append(r.written, ev) }
func (r *recordingLog) AppendDurable(ev message.Event) bool {
	r.written = append(r.written, ev)
	return true
}
func (r *recordingLog) Tail(string, int) ([]message.Event, error) { return nil, nil }
func (r *recordingLog) EventFilePath(string) string               { return "" }
func (r *recordingLog) Close() error                              { return nil }

func (r *recordingLog) QueryEventsInWorkspace(_, _, _ string, _ int, _ map[string]bool) ([]message.Event, error) {
	return r.replay, nil
}
func (r *recordingLog) QueryFilteredInWorkspace(_, _ string, _ int, _ map[string]bool) ([]message.Event, error) {
	return r.replay, nil
}

func (r *recordingLog) actions(action string) []adminAuditRecord {
	var out []adminAuditRecord
	for _, ev := range r.written {
		rec, ok := ev.Payload.(adminAuditRecord)
		if ok && rec.Action == action {
			out = append(out, rec)
		}
	}
	return out
}

func TestReadingTheAuditTrailIsItselfAudited(t *testing.T) {
	log := &recordingLog{}
	srv := newTestGateway(t, "secret")
	srv.actions = log

	status, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/admin/audit", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("audit read = %d", status)
	}

	reads := log.actions("audit.read")
	if len(reads) != 1 {
		t.Fatalf("reading the audit trail produced %d audit records, want 1", len(reads))
	}
	if reads[0].Resource != "audit" {
		t.Fatalf("the record does not name what was read: %+v", reads[0])
	}
	// The size, not the contents. A copy of the trail inside itself helps
	// nobody and doubles every record's blast radius.
	if reads[0].Details["returned"] == nil || reads[0].Details["limit"] == nil {
		t.Fatalf("the record says nothing about the scope of the read: %+v", reads[0].Details)
	}
	if _, leaked := reads[0].Details["events"]; leaked {
		t.Fatalf("the audit record embedded the records it returned: %+v", reads[0].Details)
	}
}

// Recorded after the read succeeds, so a refusal does not leave a record
// implying the data was served — which would be worse than no record, because
// it asserts something that did not happen.
func TestARefusedAuditReadIsNotRecordedAsAServedOne(t *testing.T) {
	srv := newTestGateway(t, "secret")
	srv.actions = nil // the "action log disabled" refusal path

	status, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/admin/audit", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with no action log, got %d", status)
	}
	// Nothing to assert on the log — there isn't one — but the handler must
	// not have panicked trying to record into it, which is the shape of bug
	// an "audit everything" change introduces.
}

// pagingLog is a durable, paginating action log backed by the real
// implementation, so the handler is exercised against the query it will use in
// production rather than against a stub that agrees with it.
type pagingLog struct {
	*actionlog.Logger
}

// EventFilePath is the one ActionLogBackend method the Logger spells
// differently (Path). Bridged here rather than renamed in the Logger, which is
// the SDK-facing name in a frozen interface.
func (p pagingLog) EventFilePath(agentID string) string { return p.Path(agentID) }

// MU-031 criterion 4 at the HTTP edge. Asserted by WALKING the trail, not by
// checking that a cursor field is present: a next_cursor that does not advance
// is the failure this is for, and it looks identical to a working one in any
// single response.
func TestTheAuditTrailCanBeWalkedAPageAtATime(t *testing.T) {
	dir := t.TempDir()
	log, err := actionlog.New(dir, filepath.Join(dir, "events.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	srv := newTestGateway(t, "secret")
	srv.actions = pagingLog{log}
	for i := 0; i < 12; i++ {
		srv.recordAdminAudit(nil, "credential.create", "credential", fmt.Sprintf("cred_%02d", i), "ok", nil)
	}

	// The writer batches, so a read straight after recording races the flush.
	// Waited for rather than slept through: a sleep tuned to this machine is a
	// test that starts failing on somebody else's.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/admin/audit?limit=200", "secret", "")
		created := 0
		for _, raw := range body["events"].([]any) {
			if record, _ := raw.(map[string]any); record != nil {
				if action, _ := record["action"].(string); action == "credential.create" {
					created++
				}
			}
		}
		if created >= 12 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 12 audit records reached the durable store", created)
		}
		time.Sleep(10 * time.Millisecond)
	}

	seen := map[string]int{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 40 {
			t.Fatal("the walk did not terminate — a cursor is not advancing")
		}
		path := "/api/v1/admin/audit?limit=5"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		status, body := gatewayJSON(t, srv, http.MethodGet, path, "secret", "")
		if status != http.StatusOK {
			t.Fatalf("page %d = %d: %v", pages, status, body)
		}
		for _, raw := range body["events"].([]any) {
			record, _ := raw.(map[string]any)
			if action, _ := record["action"].(string); action != "credential.create" {
				continue // the audit.read records this walk is itself producing
			}
			target, _ := record["target"].(string)
			seen[target]++
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != 12 {
		t.Fatalf("the walk saw %d of 12 records: %v", len(seen), seen)
	}
	for target, count := range seen {
		if count != 1 {
			t.Errorf("%s was returned %d times — an investigator cannot count this trail", target, count)
		}
	}
}

// A malformed cursor must not read as the end of the data.
func TestABadAuditCursorIsRejected(t *testing.T) {
	dir := t.TempDir()
	log, err := actionlog.New(dir, filepath.Join(dir, "events.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	srv := newTestGateway(t, "secret")
	srv.actions = pagingLog{log}

	status, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/admin/audit?cursor=not-a-cursor", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("a malformed cursor returned %d, want 400 — an empty page would read as the end of the trail", status)
	}
}

// A backend that cannot paginate must SAY so when asked to continue, rather
// than answering an unpaginated first page — which would tell the caller they
// had seen everything.
func TestAnUnpaginatedBackendRefusesToContinue(t *testing.T) {
	srv := newTestGateway(t, "secret")
	srv.actions = &recordingLog{}

	if status, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/admin/audit", "secret", ""); status != http.StatusOK {
		t.Fatalf("a first page against a non-paginating backend = %d, want 200", status)
	}
	status, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/admin/audit?cursor=djF8MXwy", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("continuing against a non-paginating backend = %d, want 503", status)
	}
}
