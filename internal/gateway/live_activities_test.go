package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/message"
)

const liveStartTok = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const liveUpdateTok = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func livePushes(p *pushCapture) []map[string]any {
	var out []map[string]any
	for _, n := range p.all() {
		if n["_path"] == "/v1/live" {
			out = append(out, n)
		}
	}
	return out
}

func liveEvent(n map[string]any) (event string, state map[string]any, aps map[string]any) {
	payload, _ := n["payload"].(map[string]any)
	aps, _ = payload["aps"].(map[string]any)
	event, _ = aps["event"].(string)
	state, _ = aps["content-state"].(map[string]any)
	return
}

func waitLive(t *testing.T, tr *liveActivityTracker, p *pushCapture, want int) []map[string]any {
	t.Helper()
	tr.wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := livePushes(p); len(got) >= want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected %d live pushes, got %d: %+v", want, len(livePushes(p)), livePushes(p))
	return nil
}

func TestLiveActivityFollowsBackgroundRunFromFirstToolToCompletion(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	store, pushes := mobileFixture(t, s)
	if err := store.SetLiveStartToken(context.Background(), "personal", "admin", "phone-1", liveStartTok); err != nil {
		t.Fatal(err)
	}
	tr := s.liveActivities
	tr.minGap = 0
	tr.agentName = func(string) string { return "Morning brief" }

	// A cron-style run: internal channel, no chat surface watching it.
	tr.Observe(message.Event{Type: "message.in", AgentID: "brief", SessionID: "s1", Payload: message.Message{Channel: "internal", UserID: "admin"}})
	tr.Observe(message.Event{Type: "llm.call", AgentID: "brief", SessionID: "s1"})
	tr.wait()
	if len(livePushes(pushes)) != 0 {
		t.Fatal("thinking alone must not start a Live Activity")
	}
	tr.Observe(message.Event{Type: "tool.call", AgentID: "brief", SessionID: "s1", Payload: message.ToolCall{Name: "web_search"}})
	got := waitLive(t, tr, pushes, 1)
	event, state, aps := liveEvent(got[0])
	if event != "start" || got[0]["token"] != liveStartTok || aps["attributes-type"] != "SoulacyRunAttributes" {
		t.Fatalf("expected push-to-start on the phone's start token, got %+v", got[0])
	}
	attrs, _ := aps["attributes"].(map[string]any)
	if attrs["agent_name"] != "Morning brief" || attrs["run_key"] != "brief/s1" || state["detail"] != "Running web search" || state["stage"] != "running" {
		t.Fatalf("start payload wrong: attrs=%+v state=%+v", attrs, state)
	}

	// The phone shows the activity and reports its update token.
	code, body := gatewayJSON(t, s, "POST", "/api/v1/mobile/activities", "secret",
		`{"device_id":"phone-1","agent_id":"brief","session_id":"s1","token":"`+liveUpdateTok+`","environment":"production"}`)
	if code != 201 || body["run_key"] != "brief/s1" {
		t.Fatalf("register activity: %d %+v", code, body)
	}

	tr.Observe(message.Event{Type: "tool.result", AgentID: "brief", SessionID: "s1", Payload: message.ToolResult{Name: "web_search"}})
	got = waitLive(t, tr, pushes, 2)
	event, state, _ = liveEvent(got[1])
	if event != "update" || got[1]["token"] != liveUpdateTok || state["detail"] != "web search finished" || got[1]["priority"] != float64(5) {
		t.Fatalf("expected a low-priority update on the activity token, got %+v", got[1])
	}
	// A second start for the same run must not be issued to a phone already showing it.
	tr.Observe(message.Event{Type: "tool.call", AgentID: "brief", SessionID: "s1", Payload: message.ToolCall{Name: "send_email"}})
	got = waitLive(t, tr, pushes, 3)
	if e, st, _ := liveEvent(got[2]); e != "update" || st["steps"] != float64(2) {
		t.Fatalf("expected step 2 update, got %+v", got[2])
	}

	tr.Observe(message.Event{Type: "run.completed", AgentID: "brief", SessionID: "s1", Payload: map[string]any{"success": true}})
	got = waitLive(t, tr, pushes, 4)
	event, state, aps = liveEvent(got[3])
	if event != "end" || state["stage"] != "done" || aps["dismissal-date"] == nil || got[3]["priority"] != float64(10) {
		t.Fatalf("expected an end push with dismissal, got %+v", got[3])
	}
	if left, _ := store.LiveActivities(context.Background(), "personal", "brief/s1"); len(left) != 0 {
		t.Fatalf("tokens must be forgotten after the end, still have %d", len(left))
	}
	tr.mu.Lock()
	_, tracked := tr.runs["brief/s1"]
	tr.mu.Unlock()
	if tracked {
		t.Fatal("finished run must leave the tracker")
	}
}

func TestLiveActivityStartsChatRunOnlyWhenItNeedsApproval(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	store, pushes := mobileFixture(t, s)
	_ = store.SetLiveStartToken(context.Background(), "personal", "admin", "phone-1", liveStartTok)
	tr := s.liveActivities
	tr.minGap = 0
	s.wirePushNotifications()

	tr.Observe(message.Event{Type: "message.in", AgentID: "planner", SessionID: "chat-1", Payload: message.Message{Channel: "http", UserID: "admin"}})
	tr.Observe(message.Event{Type: "tool.call", AgentID: "planner", SessionID: "chat-1", Payload: message.ToolCall{Name: "shell_exec"}})
	tr.wait()
	if len(livePushes(pushes)) != 0 {
		t.Fatal("interactive chat must not put an activity on the lock screen while it is just working")
	}

	ch := s.engine.Broker().RegisterRequestForPrincipal(runtime.ConfirmRequest{CallID: "call-9", Tool: "shell_exec"}, "planner", "chat-1", "admin")
	got := waitLive(t, tr, pushes, 1)
	event, state, aps := liveEvent(got[0])
	alert, _ := aps["alert"].(map[string]any)
	if event != "start" || state["stage"] != "waiting" || state["needs_you"] != true || state["call_id"] != "call-9" || alert["title"] != "planner needs you" {
		t.Fatalf("expected a waiting start with an alert, got %+v", got[0])
	}
	_ = store.UpsertLiveActivity(context.Background(), "personal", mobilechan.LiveActivity{RunKey: "planner/chat-1", DeviceID: "phone-1", UserID: "admin", Token: liveUpdateTok})

	go func() { <-ch }()
	if !s.engine.Broker().Resolve("call-9", true) {
		t.Fatal("resolve failed")
	}
	got = waitLive(t, tr, pushes, 2)
	event, state, _ = liveEvent(got[1])
	if event != "update" || state["needs_you"] != false || state["stage"] != "running" || state["detail"] != "Approved, continuing" {
		t.Fatalf("expected the waiting state to clear, got %+v", got[1])
	}
}

func TestLiveActivityRegistrationValidatesInput(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	mobileFixture(t, s)
	code, _ := gatewayJSON(t, s, "POST", "/api/v1/mobile/activities", "secret", `{"device_id":"phone-1","agent_id":"a","session_id":"s","token":"nope"}`)
	if code != 400 {
		t.Fatalf("bad token should be 400, got %d", code)
	}
	code, _ = gatewayJSON(t, s, "POST", "/api/v1/mobile/activities", "secret", `{"device_id":"ghost","agent_id":"a","session_id":"s","token":"`+liveUpdateTok+`"}`)
	if code != 403 {
		t.Fatalf("unknown device should be refused, got %d", code)
	}
	code, _ = gatewayJSON(t, s, "POST", "/api/v1/mobile/devices", "secret", `{"id":"phone-1","name":"Ada","push_token":"`+liveStartTok+`","notifications_enabled":true,"live_start_token":"zz"}`)
	if code != 400 {
		t.Fatalf("bad live_start_token should be 400, got %d", code)
	}
}

func TestLiveActivityTokensMayBeLongerThanDeviceTokens(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	mobileFixture(t, s)
	long := strings.Repeat("ab", 60) // 120 hex chars: a realistic push-to-start token
	code, body := gatewayJSON(t, s, "POST", "/api/v1/mobile/devices", "secret", `{"id":"phone-1","name":"Ada","push_token":"`+liveStartTok+`","notifications_enabled":true,"live_start_token":"`+long+`"}`)
	if code != 201 {
		t.Fatalf("device registration with a long start token must succeed: %d %+v", code, body)
	}
	code, _ = gatewayJSON(t, s, "POST", "/api/v1/mobile/activities", "secret", `{"device_id":"phone-1","agent_id":"a","session_id":"s","token":"`+long+`"}`)
	if code != 201 {
		t.Fatalf("activity token of 60 bytes should register, got %d", code)
	}
	if code, _ := gatewayJSON(t, s, "POST", "/api/v1/mobile/activities", "secret", `{"device_id":"phone-1","agent_id":"a","session_id":"s","token":"abc"}`); code != 400 {
		t.Fatalf("a token too short to be real must still be refused, got %d", code)
	}
}
