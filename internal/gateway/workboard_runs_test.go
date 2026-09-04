package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// wbWaitTerminalRun polls the runs endpoint until the latest run reaches a
// terminal status (done/failed) or the deadline expires. Returns the run map.
func wbWaitTerminalRun(t *testing.T, s *Server, taskID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/"+taskID+"/runs", "secret", "")
		if status != http.StatusOK {
			t.Fatalf("list runs status = %d body=%v", status, body)
		}
		runs, _ := body["runs"].([]any)
		if len(runs) > 0 {
			run, _ := runs[0].(map[string]any)
			if st, _ := run["status"].(string); st == "done" || st == "failed" {
				return run
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal status within 5s")
	return nil
}

func wbTaskStatus(t *testing.T, s *Server, taskID string) string {
	t.Helper()
	status, task := gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/"+taskID, "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get task status = %d body=%v", status, task)
	}
	st, _ := task["status"].(string)
	return st
}

func wbWaitTaskStatus(t *testing.T, s *Server, taskID, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = wbTaskStatus(t, s, taskID)
		if got == want {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("task status did not become %q within 5s; last status = %q", want, got)
	return got
}

// wbCreateRunnableAgent registers an enabled agent backed by the fake LLM
// provider so engine.Handle succeeds.
func wbCreateRunnableAgent(t *testing.T, s *Server, id string) {
	t.Helper()
	body := fmt.Sprintf(`{
		"id": %q,
		"name": "WB Agent",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "test", "model": "wb-model"},
		"builtins": [],
		"system_prompt": "Do the task.",
		"enabled": true
	}`, id)
	status, resp := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d body=%v", status, resp)
	}
}

func TestWorkboardRun_NilStore503(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/1/run", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("run status = %d, want 503", status)
	}
	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/1/runs", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("list runs status = %d, want 503", status)
	}
}

func TestWorkboardRun_TaskNotFound404(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/9999/run", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("run missing task status = %d, want 404", status)
	}
}

func TestWorkboardRun_NoAgent400(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	created := wbCreate(t, s, `{"title":"unassigned"}`)
	id := fmt.Sprintf("%v", created["id"])

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/"+id+"/run", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("run without agent status = %d body=%v, want 400", status, body)
	}
}

func TestWorkboardRun_SuccessPath(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	wbCreateRunnableAgent(t, s, "wb-bot")
	created := wbCreate(t, s, `{"title":"summarise the report","description":"use bullet points","agent_id":"wb-bot"}`)
	id := fmt.Sprintf("%v", created["id"])

	status, run := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/"+id+"/run", "secret", "")
	if status != http.StatusAccepted {
		t.Fatalf("run status = %d body=%v, want 202", status, run)
	}
	if run["status"] != "running" {
		t.Errorf("initial run status = %v, want running", run["status"])
	}
	sess, _ := run["session_id"].(string)
	if !strings.HasPrefix(sess, "wb-") {
		t.Errorf("session_id = %q, want wb- prefix", sess)
	}
	if run["attempt"] != float64(1) {
		t.Errorf("attempt = %v, want 1", run["attempt"])
	}
	if run["started_at"] == nil {
		t.Error("started_at missing")
	}

	final := wbWaitTerminalRun(t, s, id)
	if final["status"] != "done" {
		t.Fatalf("final run = %v, want done", final)
	}
	result, _ := final["result"].(string)
	if result == "" {
		t.Error("result summary should be captured from the agent reply")
	}
	if final["ended_at"] == nil {
		t.Error("ended_at should be set")
	}
	if got := wbWaitTaskStatus(t, s, id, "needs_review"); got != "needs_review" {
		t.Errorf("task status after successful run = %q, want needs_review", got)
	}
}

func TestWorkboardRun_FailurePath(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	// Agent does not exist → engine.Handle errors → run fails.
	created := wbCreate(t, s, `{"title":"doomed","agent_id":"ghost-agent"}`)
	id := fmt.Sprintf("%v", created["id"])

	status, run := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/"+id+"/run", "secret", "")
	if status != http.StatusAccepted {
		t.Fatalf("run status = %d body=%v, want 202", status, run)
	}

	final := wbWaitTerminalRun(t, s, id)
	if final["status"] != "failed" {
		t.Fatalf("final run = %v, want failed", final)
	}
	reason, _ := final["failure_reason"].(string)
	if reason == "" {
		t.Error("failure_reason should be captured")
	}
	if got := wbWaitTaskStatus(t, s, id, "failed"); got != "failed" {
		t.Errorf("task status after failed run = %q, want failed", got)
	}
}

func TestWorkboardRun_DuplicateConcurrent409(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	created := wbCreate(t, s, `{"title":"busy","agent_id":"bot-1"}`)
	id := fmt.Sprintf("%v", created["id"])

	// Seed an active run directly in the store so the in-flight state is
	// deterministic regardless of executor speed.
	var taskID int64
	fmt.Sscanf(id, "%d", &taskID)
	if _, err := s.workboardStore.StartRun(t.Context(), taskID, "bot-1", "wb-stuck", ""); err != nil {
		t.Fatalf("seed active run: %v", err)
	}

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/"+id+"/run", "secret", "")
	if status != http.StatusConflict {
		t.Fatalf("duplicate run status = %d body=%v, want 409", status, body)
	}
}

func TestWorkboardRun_RetryPreservesAttempts(t *testing.T) {
	s := newTestGatewayWithWorkboard(t)
	created := wbCreate(t, s, `{"title":"flaky","agent_id":"ghost-agent"}`)
	id := fmt.Sprintf("%v", created["id"])

	// First attempt fails (agent doesn't exist).
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/"+id+"/run", "secret", "")
	if status != http.StatusAccepted {
		t.Fatalf("first run status = %d", status)
	}
	wbWaitTerminalRun(t, s, id)

	// Retry.
	status, retry := gatewayJSON(t, s, http.MethodPost, "/api/v1/workboard/tasks/"+id+"/run", "secret", "")
	if status != http.StatusAccepted {
		t.Fatalf("retry status = %d body=%v", status, retry)
	}
	if retry["attempt"] != float64(2) {
		t.Errorf("retry attempt = %v, want 2", retry["attempt"])
	}
	wbWaitTerminalRun(t, s, id)

	_, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/workboard/tasks/"+id+"/runs", "secret", "")
	runs, _ := body["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2 (prior attempts preserved)", len(runs))
	}
}
