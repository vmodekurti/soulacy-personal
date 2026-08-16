// runs_test.go — the HTTP contract of MU-020: 202 with an addressable run id
// and cursor, an idempotent retry, and a workspace boundary that does not turn
// run IDs into an enumeration oracle.
package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/runs"
)

func runsGateway(t *testing.T) (*Server, *runs.Store) {
	t.Helper()
	srv := newTestGateway(t, "secret")
	store, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv.SetRunStore(store)
	return srv, store
}

func postRun(t *testing.T, srv *Server, body, idempotencyKey string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// Submission returns immediately with the two things a client needs to come
// back later: the run's own id, and where its events begin.
func TestSubmittingARunReturns202WithAnAddressableID(t *testing.T) {
	srv, store := runsGateway(t)
	seedAgent(t, srv)
	status, body := postRun(t, srv, `{"agent_id":"contract-agent","payload":{"prompt":"go"}}`, "")
	if status != http.StatusAccepted {
		t.Fatalf("submit returned %d, want 202: %v", status, body)
	}
	runID, _ := body["run_id"].(string)
	if runID == "" {
		t.Fatalf("no run id returned: %v", body)
	}
	if cursor, _ := body["cursor"].(string); cursor == "" {
		t.Fatalf("no event cursor returned, so a client that disconnects cannot resume: %v", body)
	}
	if _, err := store.Get(context.Background(), "ws_personal", runID); err != nil {
		t.Fatalf("the submitted run was not durable: %v", err)
	}
}

// A retry that never saw the first response must end up with one run.
//
// Two layers cover this, and they are complementary rather than redundant:
//
//   - The gateway's Idempotency-Key middleware replays the *original response*
//     verbatim, which is the stronger guarantee — the client gets the same
//     bytes it would have got the first time. It is in-memory, capped and
//     TTL'd, so it does not survive a restart.
//   - The run store dedups durably. It is what catches the retry that arrives
//     after an eviction, a restart, or from a client that puts the key in the
//     body rather than the header.
//
// This asserts the outcome that matters for both: one run, same id.
func TestARetriedSubmissionIsAbsorbedByTheResponseCache(t *testing.T) {
	srv, store := runsGateway(t)
	seedAgent(t, srv)
	first, firstBody := postRun(t, srv, `{"agent_id":"contract-agent"}`, "daily-report")
	if first != http.StatusAccepted {
		t.Fatalf("first submit returned %d: %v", first, firstBody)
	}
	_, secondBody := postRun(t, srv, `{"agent_id":"contract-agent"}`, "daily-report")
	if firstBody["run_id"] != secondBody["run_id"] {
		t.Fatalf("the retry started a second run: %v vs %v", firstBody["run_id"], secondBody["run_id"])
	}
	list, err := store.List(context.Background(), "ws_personal", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d runs were created for one idempotency key", len(list))
	}
}

// The durable half, exercised on its own by putting the key in the body so the
// response cache never sees it. This is the path a retry takes after a
// gateway restart, which is precisely when a duplicated run would be most
// expensive and least visible.
func TestARetryAfterTheResponseCacheMissesStillDedups(t *testing.T) {
	srv, store := runsGateway(t)
	seedAgent(t, srv)
	body := `{"agent_id":"contract-agent","idempotency_key":"nightly"}`

	first, firstBody := postRun(t, srv, body, "")
	if first != http.StatusAccepted {
		t.Fatalf("first submit returned %d: %v", first, firstBody)
	}
	second, secondBody := postRun(t, srv, body, "")
	if second != http.StatusOK {
		t.Fatalf("store-level replay returned %d, want 200: %v", second, secondBody)
	}
	if replayed, _ := secondBody["replayed"].(bool); !replayed {
		t.Fatalf("the retry was not reported as a replay: %v", secondBody)
	}
	if firstBody["run_id"] != secondBody["run_id"] {
		t.Fatalf("the retry started a second run: %v vs %v", firstBody["run_id"], secondBody["run_id"])
	}
	list, err := store.List(context.Background(), "ws_personal", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d runs were created for one idempotency key", len(list))
	}
}

// A run belonging to another workspace is reported exactly as one that does
// not exist, so run IDs cannot be probed (product invariant 8).
func TestAnotherWorkspacesRunIsReportedAsMissing(t *testing.T) {
	srv, store := runsGateway(t)
	if _, _, err := store.Submit(context.Background(), runs.Run{
		ID: "run_other", WorkspaceID: "ws_other", AgentID: "bot",
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_other", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace run fetch returned %d, want 404", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "ws_other") {
		t.Fatalf("the 404 leaked the owning workspace: %s", raw)
	}
}

// Cancelling a finished run is a conflict, not a not-found: the run is theirs
// and they may see it, it has simply already ended.
func TestCancellingAFinishedRunIsAConflict(t *testing.T) {
	srv, store := runsGateway(t)
	ctx := context.Background()
	if _, _, err := store.Submit(ctx, runs.Run{ID: "run_1", WorkspaceID: "ws_personal", AgentID: "bot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, "ws_personal", "run_1", runs.StatusRunning, runs.TransitionOptions{}); err != nil {
		t.Fatal(err)
	}

	cancel := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_1/cancel", nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := srv.app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if code := cancel(); code != http.StatusOK {
		t.Fatalf("first cancel returned %d, want 200", code)
	}
	if code := cancel(); code != http.StatusConflict {
		t.Fatalf("cancelling an already-finished run returned %d, want 409", code)
	}
}
