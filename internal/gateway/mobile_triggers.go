package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// Location triggers (E51).
//
// A paired phone asks the gateway which regions to watch, monitors them with
// Core Location (which works in the background), and reports enter/exit
// events. The gateway runs the matching agent with a "location" trigger and
// delivers the reply to that phone. The gateway never tracks the phone: it
// only ever learns "the phone crossed a boundary it agreed to watch".

// mobileTrigger is the phone-facing description of one region.
type mobileTrigger struct {
	AgentID   string  `json:"agent_id"`
	AgentName string  `json:"agent_name"`
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	RadiusM   float64 `json:"radius_m"`
	On        string  `json:"on"`
	Device    string  `json:"device,omitempty"`
	Cooldown  string  `json:"cooldown,omitempty"`
}

// triggerCooldowns remembers the last fire per (agent, device) so a phone
// oscillating on a region edge cannot run an agent in a loop.
var triggerCooldowns = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func (s *Server) registerMobileTriggerRoutes(api fiber.Router) {
	api.Get("/mobile/triggers", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleListMobileTriggers)
	api.Post("/mobile/triggers/:id/fire", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleFireMobileTrigger)
}

// locationTriggers returns every enabled agent with a valid location trigger.
func (s *Server) locationTriggers() []mobileTrigger {
	var out []mobileTrigger
	for _, def := range s.loader.All() {
		if def == nil || !def.Enabled || def.Trigger != agent.TriggerLocation || def.Location == nil {
			continue
		}
		loc := def.Location
		out = append(out, mobileTrigger{
			AgentID: def.ID, AgentName: def.Name, Name: loc.Name,
			Latitude: loc.Latitude, Longitude: loc.Longitude, RadiusM: loc.RadiusM,
			On: strings.ToLower(strings.TrimSpace(loc.On)), Device: strings.TrimSpace(loc.Device), Cooldown: loc.Cooldown,
		})
	}
	if out == nil {
		out = []mobileTrigger{}
	}
	return out
}

func (s *Server) handleListMobileTriggers(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	device := strings.TrimSpace(c.Query("device_id"))
	all := s.locationTriggers()
	mine := make([]mobileTrigger, 0, len(all))
	for _, t := range all {
		if t.Device == "" || device == "" || t.Device == device {
			mine = append(mine, t)
		}
	}
	return c.JSON(fiber.Map{"triggers": mine, "max_regions": 20})
}

type fireTriggerBody struct {
	Event     string  `json:"event"`
	DeviceID  string  `json:"device_id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	AccuracyM float64 `json:"accuracy_m"`
	Name      string  `json:"region_name"`
}

func cooldownFor(loc *agent.LocationTrigger) time.Duration {
	if loc == nil {
		return 30 * time.Minute
	}
	d, err := time.ParseDuration(strings.TrimSpace(loc.Cooldown))
	if err != nil || d < 0 {
		return 30 * time.Minute
	}
	return d
}

func (s *Server) handleFireMobileTrigger(c *fiber.Ctx) error {
	id := strings.Clone(c.Params("id"))
	if isProtectedSystemAgent(id) {
		return protectedSystemAgentResponse(c)
	}
	def := s.loader.Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	if !def.Enabled || def.Trigger != agent.TriggerLocation || def.Location == nil {
		return s.errMsg(c, fiber.StatusConflict, "agent does not have an enabled location trigger")
	}
	var body fireTriggerBody
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	body.Event = strings.ToLower(strings.TrimSpace(body.Event))
	if body.Event != "enter" && body.Event != "exit" {
		return s.errMsg(c, fiber.StatusBadRequest, "event must be enter or exit")
	}
	want := strings.ToLower(strings.TrimSpace(def.Location.On))
	if body.Event != want {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"ok": true, "ignored": true, "reason": fmt.Sprintf("agent runs on %s, not %s", want, body.Event)})
	}
	body.DeviceID = strings.TrimSpace(body.DeviceID)
	if body.DeviceID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "device_id is required")
	}
	if def.Location.Device != "" && def.Location.Device != body.DeviceID {
		return s.errMsg(c, fiber.StatusForbidden, "this trigger is bound to a different device")
	}
	_, userID, _ := mobileIdentity(c)

	key := id + "|" + body.DeviceID
	cooldown := cooldownFor(def.Location)
	triggerCooldowns.Lock()
	if last, ok := triggerCooldowns.last[key]; ok && cooldown > 0 && time.Since(last) < cooldown {
		triggerCooldowns.Unlock()
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"ok": true, "ignored": true, "reason": "cooldown", "retry_after": (cooldown - time.Since(last)).Round(time.Second).String()})
	}
	triggerCooldowns.last[key] = time.Now()
	triggerCooldowns.Unlock()

	if !s.scheduler.TryStartRun(id) {
		return s.errMsg(c, fiber.StatusConflict, "agent is already running")
	}

	sessionID := fmt.Sprintf("location-%s-%d", id, time.Now().UnixNano())
	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   id,
		Channel:   "internal",
		ThreadID:  "device:" + body.DeviceID,
		UserID:    userID,
		Username:  "location-trigger",
		Role:      message.RoleUser,
		Parts:     message.Text(fmt.Sprintf("__trigger:location__ %s %s", body.Event, regionLabel(def.Location, body))),
		CreatedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"trigger": "location", "location.event": body.Event, "location.device": body.DeviceID,
			"location.name":     regionLabel(def.Location, body),
			"location.latitude": fmt.Sprintf("%.5f", body.Latitude), "location.longitude": fmt.Sprintf("%.5f", body.Longitude),
		},
	}
	principal, _ := requestPrincipal(c)
	s.recordAdminAudit(c, "trigger.location", "agent", id, "accepted", map[string]any{"event": body.Event, "device": body.DeviceID})
	s.log.Info("location trigger fired", zap.String("agent", id), zap.String("event", body.Event), zap.String("device", body.DeviceID))

	// Run asynchronously: the phone may be in a background execution window
	// measured in seconds, so it gets a receipt now and the result as a push.
	go s.runLocationTrigger(def, msg, principal, body.DeviceID)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"ok": true, "run_id": msg.ID, "session_id": sessionID, "agent_id": id})
}

func regionLabel(loc *agent.LocationTrigger, body fireTriggerBody) string {
	if loc != nil && strings.TrimSpace(loc.Name) != "" {
		return strings.TrimSpace(loc.Name)
	}
	if strings.TrimSpace(body.Name) != "" {
		return strings.TrimSpace(body.Name)
	}
	return "the region"
}

// runLocationTrigger executes the agent and delivers the reply to the phone
// that fired the trigger, as a mobile delivery plus a push.
func (s *Server) runLocationTrigger(def *agent.Definition, msg message.Message, principal runtime.Principal, deviceID string) {
	defer s.scheduler.FinishRun(def.ID)
	ctx, cancel := context.WithTimeout(context.Background(), s.resolveRunTimeout(def))
	defer cancel()
	ctx = runtime.WithPrincipal(ctx, principal)
	ctx = llm.WithCallMetadata(ctx, llm.CallMetadata{Subject: principal.Subject, AgentID: def.ID, SessionID: msg.SessionID, RunID: msg.ID, Source: "location", Trigger: "location"})
	reply, err := s.engine.Handle(ctx, msg)
	adapter := mobilechan.DefaultAdapter()
	if err != nil {
		s.log.Warn("location-triggered run failed", zap.String("agent", def.ID), zap.Error(err))
		if adapter != nil && adapter.CanPush() {
			_ = adapter.Notify(ctx, "personal", "device:"+deviceID, mobilechan.Notification{
				Title: def.Name + " could not run", Body: err.Error(), Category: mobilechan.CategoryTrigger,
				ThreadID: "trigger-" + def.ID, DeepLink: "soulacy://activity",
			})
		}
		return
	}
	// Reuse the delivery path so the result lands in the phone's inbox even
	// when push is unavailable; the adapter itself pushes when it can.
	reply.ThreadID = "device:" + deviceID
	if reply.Metadata == nil {
		reply.Metadata = map[string]string{}
	}
	reply.Metadata["trigger"] = "location"
	reply.Metadata["location.event"] = msg.Metadata["location.event"]
	reply.Metadata["location.name"] = msg.Metadata["location.name"]
	if store := mobilechan.DefaultStore(); store != nil && adapter != nil {
		if err := adapter.Send(ctx, reply); err != nil {
			s.log.Warn("location-triggered delivery failed", zap.String("agent", def.ID), zap.Error(err))
		}
	}
	if s.hub != nil {
		s.hub.Emit(message.Event{Type: "trigger.location", AgentID: def.ID, SessionID: msg.SessionID, Timestamp: time.Now().UTC(),
			Payload: map[string]any{"event": msg.Metadata["location.event"], "device": deviceID, "region": msg.Metadata["location.name"]}})
	}
}
