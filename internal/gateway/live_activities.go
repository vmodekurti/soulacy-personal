package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/message"
)

// liveActivityTracker follows every run through the event hub and mirrors it
// onto paired phones as a Live Activity.
//
// Which runs earn one: a background run (cron, webhook, location, another
// agent) as soon as it calls its first tool, and any run at all the moment it
// needs an approval. Interactive chat is excluded until it blocks on the
// user, because the person is already looking at that conversation.
type liveActivityTracker struct {
	mu   sync.Mutex
	runs map[string]*liveRun

	adapter   func() *mobilechan.Adapter
	agentName func(string) string
	timeout   func() time.Duration
	log       *zap.Logger
	now       func() time.Time
	// minGap throttles routine progress updates; stage changes bypass it.
	minGap time.Duration
	// maxAge ends activities whose run never reported completion.
	maxAge time.Duration
	wg     sync.WaitGroup
}

type liveRun struct {
	key, agentID, agentName, sessionID, destination string
	chat                                            bool
	startedAt                                       time.Time
	steps                                           int
	started, needsYou, ended                        bool
	callID, detail                                  string
	lastPush                                        time.Time
	pushMu                                          sync.Mutex
}

func newLiveActivityTracker(adapter func() *mobilechan.Adapter, agentName func(string) string, timeout func() time.Duration, log *zap.Logger) *liveActivityTracker {
	if log == nil {
		log = zap.NewNop()
	}
	return &liveActivityTracker{
		runs: map[string]*liveRun{}, adapter: adapter, agentName: agentName, timeout: timeout,
		log: log.Named("live-activity"), now: time.Now, minGap: 2 * time.Second, maxAge: 3 * time.Hour,
	}
}

func liveRunKey(agentID, sessionID string) string { return agentID + "/" + sessionID }

// Observe is the event-hub hook. It must stay cheap: it only updates state
// and hands pushes to goroutines.
func (t *liveActivityTracker) Observe(ev message.Event) {
	if ev.AgentID == "" || ev.SessionID == "" || ev.AgentID == "system" {
		return
	}
	switch ev.Type {
	case "message.in":
		t.begin(ev)
	case "tool.call":
		var call struct {
			Name string `json:"name"`
		}
		decodePayload(ev.Payload, &call)
		t.progress(ev, "Running "+humanTool(call.Name), true)
	case "tool.result":
		var res struct {
			Name    string `json:"name"`
			IsError bool   `json:"is_error"`
		}
		decodePayload(ev.Payload, &res)
		detail := humanTool(res.Name) + " finished"
		if res.IsError {
			detail = humanTool(res.Name) + " failed, recovering"
		}
		t.progress(ev, detail, false)
	case "llm.call":
		t.progress(ev, "Thinking", false)
	case "run.completed":
		var done struct {
			Success bool `json:"success"`
		}
		decodePayload(ev.Payload, &done)
		t.finish(ev.AgentID, ev.SessionID, done.Success, "")
	}
}

func (t *liveActivityTracker) begin(ev message.Event) {
	var in struct {
		Channel string `json:"channel"`
		UserID  string `json:"user_id"`
	}
	decodePayload(ev.Payload, &in)
	key := liveRunKey(ev.AgentID, ev.SessionID)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()
	if _, exists := t.runs[key]; exists {
		return
	}
	dest := ""
	if strings.TrimSpace(in.UserID) != "" {
		dest = "user:" + strings.TrimSpace(in.UserID)
	}
	t.runs[key] = &liveRun{
		key: key, agentID: ev.AgentID, sessionID: ev.SessionID, agentName: t.agentName(ev.AgentID),
		destination: dest, chat: in.Channel == "http", startedAt: t.now(), detail: "Starting",
	}
}

// progress records a step and decides whether the phones need to hear about
// it. A tool call starts the activity for background runs; later steps are
// throttled updates.
func (t *liveActivityTracker) progress(ev message.Event, detail string, isStep bool) {
	key := liveRunKey(ev.AgentID, ev.SessionID)
	t.mu.Lock()
	run := t.runs[key]
	if run == nil || run.ended {
		t.mu.Unlock()
		return
	}
	if isStep {
		run.steps++
	}
	run.detail = detail
	var action string
	switch {
	case !run.started && isStep && !run.chat:
		run.started = true
		action = "start"
	case run.started && !run.needsYou && t.now().Sub(run.lastPush) >= t.minGap:
		action = "update"
	}
	if action != "" {
		run.lastPush = t.now()
	}
	state := t.stateLocked(run)
	attrs := t.attrsLocked(run)
	dest := run.destination
	t.mu.Unlock()
	if action != "" {
		t.push(run, dest, action, attrs, state, nil)
	}
}

// ApprovalPending is the broker hook: the run now needs the user. This
// starts an activity even for chat runs, because "waiting on you" is the
// whole point of having one on the lock screen.
func (t *liveActivityTracker) ApprovalPending(p runtime.PendingApproval) {
	if p.AgentID == "" || p.SessionID == "" {
		return
	}
	key := liveRunKey(p.AgentID, p.SessionID)
	t.mu.Lock()
	run := t.runs[key]
	if run == nil {
		run = &liveRun{key: key, agentID: p.AgentID, sessionID: p.SessionID, agentName: t.agentName(p.AgentID), startedAt: t.now()}
		t.runs[key] = run
	}
	if run.ended {
		t.mu.Unlock()
		return
	}
	if run.destination == "" && p.Principal != "" {
		run.destination = approvalDestination(p.Principal)
	}
	run.needsYou, run.callID = true, p.CallID
	run.detail = "Needs your approval: " + humanTool(p.Tool)
	action := "update"
	if !run.started {
		run.started, action = true, "start"
	}
	run.lastPush = t.now()
	state := t.stateLocked(run)
	attrs := t.attrsLocked(run)
	dest := run.destination
	alert := &mobilechan.Notification{Title: run.agentName + " needs you", Body: run.detail}
	t.mu.Unlock()
	t.push(run, dest, action, attrs, state, alert)
}

// ApprovalResolved clears the waiting state once anyone decides.
func (t *liveActivityTracker) ApprovalResolved(p runtime.PendingApproval, approved bool) {
	key := liveRunKey(p.AgentID, p.SessionID)
	t.mu.Lock()
	run := t.runs[key]
	if run == nil || !run.started || run.ended || run.callID != p.CallID {
		t.mu.Unlock()
		return
	}
	run.needsYou, run.callID = false, ""
	if approved {
		run.detail = "Approved, continuing"
	} else {
		run.detail = "Denied, agent is wrapping up"
	}
	run.lastPush = t.now()
	state := t.stateLocked(run)
	attrs := t.attrsLocked(run)
	dest := run.destination
	t.mu.Unlock()
	t.push(run, dest, "update", attrs, state, nil)
}

func (t *liveActivityTracker) finish(agentID, sessionID string, success bool, detail string) {
	key := liveRunKey(agentID, sessionID)
	t.mu.Lock()
	run := t.runs[key]
	if run == nil {
		t.mu.Unlock()
		return
	}
	delete(t.runs, key)
	if !run.started || run.ended {
		t.mu.Unlock()
		return
	}
	run.ended, run.needsYou, run.callID = true, false, ""
	if detail == "" {
		detail = "Finished"
		if !success {
			detail = "Stopped with an error"
		}
	}
	run.detail = detail
	state := t.stateLocked(run)
	attrs := t.attrsLocked(run)
	dest := run.destination
	t.mu.Unlock()
	t.push(run, dest, "end", attrs, state, nil)
}

// sweepLocked ends activities whose run went silent for longer than maxAge.
func (t *liveActivityTracker) sweepLocked() {
	cutoff := t.now().Add(-t.maxAge)
	for key, run := range t.runs {
		if run.startedAt.Before(cutoff) {
			delete(t.runs, key)
			if run.started && !run.ended {
				run.ended = true
				run.detail = "Lost track of this run"
				state := t.stateLocked(run)
				attrs := t.attrsLocked(run)
				t.push(run, run.destination, "end", attrs, state, nil)
			}
		}
	}
}

func (t *liveActivityTracker) stateLocked(run *liveRun) mobilechan.LiveState {
	stage := mobilechan.LiveStageRunning
	switch {
	case run.ended && strings.HasPrefix(run.detail, "Stopped"), run.ended && strings.HasPrefix(run.detail, "Lost"):
		stage = mobilechan.LiveStageFailed
	case run.ended:
		stage = mobilechan.LiveStageDone
	case run.needsYou:
		stage = mobilechan.LiveStageWaiting
	}
	return mobilechan.LiveState{Stage: stage, Detail: run.detail, Steps: run.steps, NeedsYou: run.needsYou, CallID: run.callID, UpdatedAt: t.now().Unix()}
}

func (t *liveActivityTracker) attrsLocked(run *liveRun) mobilechan.LiveAttributes {
	return mobilechan.LiveAttributes{RunKey: run.key, AgentID: run.agentID, AgentName: run.agentName, SessionID: run.sessionID, StartedAt: run.startedAt.Unix()}
}

// push delivers one start/update/end off the hub goroutine. Pushes for the
// same run are serialised so an update cannot overtake its start.
func (t *liveActivityTracker) push(run *liveRun, destination, action string, attrs mobilechan.LiveAttributes, state mobilechan.LiveState, alert *mobilechan.Notification) {
	adapter := t.adapter()
	if adapter == nil || !adapter.CanPush() {
		return
	}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		run.pushMu.Lock()
		defer run.pushMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), t.timeout())
		defer cancel()
		var err error
		switch action {
		case "start":
			start := mobilechan.Notification{}
			if alert != nil {
				start = *alert
			}
			_, err = adapter.StartLiveActivity(ctx, "personal", destination, attrs, state, start)
		case "update":
			err = adapter.UpdateLiveActivity(ctx, "personal", attrs.RunKey, state, alert)
		case "end":
			err = adapter.EndLiveActivity(ctx, "personal", attrs.RunKey, state, alert)
		}
		if err != nil {
			t.log.Debug("live activity push", zap.String("run", attrs.RunKey), zap.String("action", action), zap.Error(err))
		}
	}()
}

// wait blocks until in-flight pushes finish; tests use it.
func (t *liveActivityTracker) wait() { t.wg.Wait() }

func decodePayload(payload any, into any) {
	if payload == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, into)
}

// humanTool turns "web_search" into "web search" for a lock screen.
func humanTool(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "a tool"
	}
	return strings.ReplaceAll(strings.ReplaceAll(name, "_", " "), ".", " ")
}

// ── Routes ──────────────────────────────────────────────────────────────────

type liveActivityRegistration struct {
	DeviceID    string `json:"device_id"`
	AgentID     string `json:"agent_id"`
	SessionID   string `json:"session_id"`
	Token       string `json:"token"`
	Environment string `json:"environment"`
}

func (s *Server) registerLiveActivityRoutes(api fiber.Router) {
	api.Post("/mobile/activities", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleRegisterLiveActivity)
}

// handleRegisterLiveActivity stores the update token a phone reports once an
// activity is on screen. Until this arrives the gateway can only start.
func (s *Server) handleRegisterLiveActivity(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	var req liveActivityRegistration
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	req.DeviceID, req.AgentID, req.SessionID = strings.TrimSpace(req.DeviceID), strings.TrimSpace(req.AgentID), strings.TrimSpace(req.SessionID)
	req.Token = strings.ToLower(strings.TrimSpace(req.Token))
	if req.DeviceID == "" || req.AgentID == "" || req.SessionID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "device_id, agent_id and session_id are required")
	}
	if !validHexToken(req.Token) {
		return s.errMsg(c, fiber.StatusBadRequest, "token must be a 32-byte APNs token encoded as hexadecimal")
	}
	err = store.UpsertLiveActivity(c.UserContext(), workspaceID, mobilechan.LiveActivity{
		RunKey: liveRunKey(req.AgentID, req.SessionID), DeviceID: req.DeviceID, UserID: userID, Token: req.Token,
		Environment: req.Environment, BundleID: personalIOSBundleID,
	})
	if err != nil {
		if err == mobilechan.ErrDeviceOwnership {
			return s.errMsg(c, fiber.StatusForbidden, "device is registered to another user")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"registered": true, "run_key": liveRunKey(req.AgentID, req.SessionID)})
}

// validHexToken accepts an ActivityKit token: hex, whole bytes, and between
// 16 and 256 bytes. Unlike APNs device tokens, Live Activity push-to-start
// and update tokens are not fixed at 32 bytes, so an exact-length check
// rejected real phones and took their whole device registration with it.
func validHexToken(v string) bool {
	if len(v) < 32 || len(v) > 512 || len(v)%2 != 0 {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
