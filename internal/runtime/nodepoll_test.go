package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestIsPollNode(t *testing.T) {
	cases := map[string]bool{
		"mcp__notebooklm__studio_status":   true,
		"mcp__notebooklm__research_status": true,
		"mcp__x__job_poll":                 true,
		"mcp__x__check_status":             true,
		"mcp__x__wait_ready":               true,
		"mcp__notebooklm__studio_create":   false,
		"mcp__notebooklm__research_start":  false,
		"mcp__notebooklm__research_import": false,
		"mcp__notebooklm__notebook_create": false,
		"web_search":                       false,
	}
	for tool, want := range cases {
		if got := isPollNode(sdkr.FlowNode{Tool: tool}); got != want {
			t.Errorf("isPollNode(%q)=%v want %v", tool, got, want)
		}
	}
}

func TestResultPending(t *testing.T) {
	pending := [][]byte{
		[]byte(`{"status":"success","summary":{"completed":0,"in_progress":1},"artifacts":[{"status":"in_progress","audio_url":null}]}`),
		[]byte(`{"artifact_status":"in_progress"}`),
		[]byte(`{"state":"PENDING"}`),
		[]byte(`{"jobs":{"pending":2}}`),
	}
	for _, p := range pending {
		if !resultPending(p) {
			t.Errorf("expected pending: %s", p)
		}
	}
	terminal := [][]byte{
		[]byte(`{"status":"success","summary":{"completed":1,"in_progress":0},"artifacts":[{"status":"completed","audio_url":"https://x/a.mp3"}]}`),
		[]byte(`{"status":"completed"}`),
		[]byte(`{"status":"error","error":"boom"}`),
		[]byte(`{"notebook_id":"abc","imported_count":52}`),
	}
	for _, term := range terminal {
		if resultPending(term) {
			t.Errorf("expected terminal: %s", term)
		}
	}
}

func TestAutoPoll_LoopsUntilTerminal(t *testing.T) {
	calls := 0
	recall := func(ctx context.Context) ([]byte, error) {
		calls++
		if calls < 3 {
			return []byte(`{"artifacts":[{"status":"in_progress"}]}`), nil
		}
		return []byte(`{"artifacts":[{"status":"completed","audio_url":"https://x/a.mp3"}]}`), nil
	}
	first := []byte(`{"artifacts":[{"status":"in_progress"}]}`)
	out, err := autoPoll(context.Background(), first, 2*time.Second, 5*time.Millisecond, recall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resultPending(out) {
		t.Errorf("should have polled to a terminal result; got %s", out)
	}
	if calls < 2 {
		t.Errorf("expected multiple polls, got %d", calls)
	}
}

func TestAutoPoll_TerminalFirstNoCall(t *testing.T) {
	called := false
	recall := func(ctx context.Context) ([]byte, error) { called = true; return nil, nil }
	out, err := autoPoll(context.Background(), []byte(`{"status":"completed"}`), time.Second, time.Millisecond, recall)
	if err != nil || called {
		t.Fatalf("a terminal first result must not poll; called=%v err=%v", called, err)
	}
	if string(out) != `{"status":"completed"}` {
		t.Errorf("should return the first result unchanged: %s", out)
	}
}

func TestAutoPoll_BudgetExhaustedReturnsLast(t *testing.T) {
	recall := func(ctx context.Context) ([]byte, error) {
		return []byte(`{"status":"in_progress"}`), nil // never completes
	}
	out, err := autoPoll(context.Background(), []byte(`{"status":"in_progress"}`), 30*time.Millisecond, 5*time.Millisecond, recall)
	if err != nil {
		t.Fatalf("budget exhaustion should not error: %v", err)
	}
	if !resultPending(out) {
		t.Errorf("should return the last (still-pending) state on budget exhaustion")
	}
}

func TestAutoPoll_RespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recall := func(ctx context.Context) ([]byte, error) { return []byte(`{"status":"in_progress"}`), nil }
	_, err := autoPoll(ctx, []byte(`{"status":"in_progress"}`), time.Minute, 10*time.Millisecond, recall)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestPollBudgetAndInterval(t *testing.T) {
	// timeout_s in params → budget; poll_interval → interval.
	n := sdkr.FlowNode{Params: map[string]any{"timeout_s": float64(600), "poll_interval": float64(30)}}
	if got := pollBudget(n, ""); got != 600*time.Second {
		t.Errorf("budget from timeout_s: got %v", got)
	}
	if got := pollInterval(n, ""); got != 30*time.Second {
		t.Errorf("interval from poll_interval: got %v", got)
	}
	// Defaults when nothing declared.
	if got := pollBudget(sdkr.FlowNode{}, `{"x":1}`); got != defaultPollBudget {
		t.Errorf("default budget: got %v", got)
	}
}
