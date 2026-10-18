package gateway

// A reply sent after a failure does not undo the failure.
//
// The Portfolio Replacement Strategist timed out mid-run, the framework sent
// the last thing it had, and the run was filed as:
//
//	ok: true, status: "success", error: "context deadline exceeded"
//
// All three at once, which cannot all be right. summarizeActionEvents replays
// events in timestamp order and the message.out arm set success
// unconditionally, so the trailing reply overwrote the recorded failure.
//
// Not cosmetic: Failed runs, the dead-letter queue and the scheduler's
// consecutive-failure auto-disable all read this field. An agent that timed out
// every morning and answered with a fragment would appear in none of them.

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func at(sec int) time.Time {
	return time.Date(2026, 8, 11, 3, 11, sec, 0, time.UTC)
}

// degradedRun is the observed shape: a tool fails, then a reply is still sent.
func degradedRun() []message.Event {
	return []message.Event{
		{Type: "message.in", Timestamp: at(1), Payload: map[string]any{"text": "advise on my portfolio"}},
		{Type: "tool.result", Timestamp: at(2), Payload: map[string]any{
			"name": "market_data_get_market_overview", "is_error": true,
			"content": "error: context deadline exceeded"}},
		{Type: "message.out", Timestamp: at(3), Payload: map[string]any{
			"parts": []any{map[string]any{"type": "text", "text": "ticker: CVX; outlook: strongly bullish"}}}},
	}
}

func TestSummarizeActionEvents_KeepsAFailedRunFailed(t *testing.T) {
	row, ok := summarizeActionEvents("r1", "s1", degradedRun())
	if !ok {
		t.Fatal("no row produced")
	}
	if row.Ok || row.Status != "failed" {
		t.Fatalf("a timed-out run was filed as ok=%v status=%q — it will never reach Failed runs, "+
			"the dead-letter queue, or the failure-streak auto-disable", row.Ok, row.Status)
	}
	if row.Error == "" {
		t.Error("the failure was recorded without the reason that explains it")
	}
}

// An ordinary run must still be a success — the fix must not paint every run red.
func TestSummarizeActionEvents_StillReportsACleanRunAsSuccess(t *testing.T) {
	events := []message.Event{
		{Type: "message.in", Timestamp: at(1), Payload: map[string]any{"text": "hello"}},
		{Type: "tool.result", Timestamp: at(2), Payload: map[string]any{
			"name": "market_data_get_quote", "is_error": false, "content": `{"symbol":"MU"}`}},
		{Type: "message.out", Timestamp: at(3), Payload: map[string]any{
			"parts": []any{map[string]any{"type": "text", "text": "MU is trading at 861."}}}},
	}
	row, ok := summarizeActionEvents("r2", "s2", events)
	if !ok {
		t.Fatal("no row produced")
	}
	if !row.Ok || row.Status != "success" {
		t.Fatalf("a clean run was filed as ok=%v status=%q", row.Ok, row.Status)
	}
}

func TestSummarizeActionEvents_IncompleteDeliveredMissionRemainsFailed(t *testing.T) {
	events := []message.Event{
		{Type: "message.in", Timestamp: at(1), Payload: map[string]any{"text": "make today's podcast"}},
		{Type: "message.out", Timestamp: at(2), Payload: map[string]any{
			"parts": []any{map[string]any{"type": "text", "text": "I found articles but did not create the podcast."}}}},
		{Type: "task.contract.completed", Timestamp: at(3), Payload: map[string]any{
			"state": "blocked", "outcome": "blocked", "blocker": "podcast was not generated"}},
		{Type: "run.completed", Timestamp: at(4), Payload: map[string]any{
			"success": false, "degraded": false, "outcome": "incomplete", "task_outcome": "blocked"}},
		{Type: "schedule.output", Timestamp: at(5), Payload: map[string]any{
			"delivered": true, "channel": "mobile", "to": "all", "trigger": "manual"}},
	}
	row, ok := summarizeActionEvents("r3", "s3", events)
	if !ok {
		t.Fatal("no row produced")
	}
	if row.Ok || row.Status != "failed" {
		t.Fatalf("incomplete delivered mission filed as ok=%v status=%q", row.Ok, row.Status)
	}
	if row.DeliveryStatus != "delivered" {
		t.Fatalf("delivery status = %q, want delivered", row.DeliveryStatus)
	}
	if row.Error != "podcast was not generated" {
		t.Fatalf("error = %q, want mission blocker", row.Error)
	}
}

func TestSummarizeActionEvents_IntermediateToolFailureDoesNotFinishRun(t *testing.T) {
	events := []message.Event{
		{Type: "message.in", Timestamp: at(1), Payload: map[string]any{"text": "make today's podcast"}},
		{Type: "tool.result", Timestamp: at(2), Payload: map[string]any{
			"name": "fetch_url", "is_error": true, "content": "error: HTTP 403"}},
		{Type: "llm.call", Timestamp: at(3), Payload: map[string]any{"turn": 3}},
	}
	row, ok := summarizeActionEvents("r4", "s4", events)
	if !ok {
		t.Fatal("no row produced")
	}
	if row.Ok || row.Status != "pending" || row.Error != "" {
		t.Fatalf("in-flight recovery was finalized: ok=%v status=%q error=%q", row.Ok, row.Status, row.Error)
	}
}

func TestSummarizeActionEvents_FinalContractOverridesIntermediateToolFailure(t *testing.T) {
	events := []message.Event{
		{Type: "message.in", Timestamp: at(1), Payload: map[string]any{"text": "make today's podcast"}},
		{Type: "tool.result", Timestamp: at(2), Payload: map[string]any{
			"name": "fetch_url", "is_error": true, "content": "error: HTTP 403"}},
		{Type: "tool.result", Timestamp: at(3), Payload: map[string]any{
			"name": "authenticated_fetch", "is_error": true, "content": "saved session needs refresh"}},
		{Type: "task.contract.completed", Timestamp: at(4), Payload: map[string]any{
			"state": "blocked", "outcome": "blocked", "blocker": "refresh the Gartner Website Access sign-in"}},
		{Type: "run.completed", Timestamp: at(5), Payload: map[string]any{
			"success": false, "outcome": "incomplete", "task_outcome": "blocked"}},
	}
	row, ok := summarizeActionEvents("r5", "s5", events)
	if !ok {
		t.Fatal("no row produced")
	}
	if row.Ok || row.Status != "failed" {
		t.Fatalf("blocked run filed as ok=%v status=%q", row.Ok, row.Status)
	}
	if row.Error != "refresh the Gartner Website Access sign-in" {
		t.Fatalf("error = %q, want final contract blocker", row.Error)
	}
}

func TestSummarizeActionEvents_RecoveredToolFailureCanFinishSuccessfully(t *testing.T) {
	events := []message.Event{
		{Type: "message.in", Timestamp: at(1), Payload: map[string]any{"text": "make today's podcast"}},
		{Type: "tool.result", Timestamp: at(2), Payload: map[string]any{
			"name": "fetch_url", "is_error": true, "content": "error: HTTP 403"}},
		{Type: "tool.result", Timestamp: at(3), Payload: map[string]any{
			"name": "authenticated_fetch", "is_error": false, "content": "article body"}},
		{Type: "run.completed", Timestamp: at(4), Payload: map[string]any{
			"success": true, "outcome": "success", "task_outcome": "evidence_based"}},
		{Type: "message.out", Timestamp: at(5), Payload: map[string]any{
			"parts": []any{map[string]any{"type": "text", "text": "Podcast ready"}}}},
	}
	row, ok := summarizeActionEvents("r6", "s6", events)
	if !ok {
		t.Fatal("no row produced")
	}
	if !row.Ok || row.Status != "success" || row.Error != "" {
		t.Fatalf("recovered run filed as ok=%v status=%q error=%q", row.Ok, row.Status, row.Error)
	}
}
