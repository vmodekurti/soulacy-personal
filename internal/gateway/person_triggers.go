package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/person"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// Person triggers: agents that run because something about the person
// changed, rather than because a clock fired.
//
// A scheduled briefing runs whether or not anything happened, and a person
// learns to ignore it. This fires only on a real change, and the guards
// below exist because the obvious implementation fires far too often: a
// phone that has been offline delivers a burst of observations, and every one
// of them changes the model.

// personTriggerCooldowns remembers the last run per (agent, owner).
var personTriggerCooldowns = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// personTriggerAgents returns the enabled agents watching the person model,
// with their conditions parsed. A definition whose durations do not parse is
// skipped and logged rather than silently given defaults.
func (s *Server) personTriggerAgents() []*agent.Definition {
	var out []*agent.Definition
	for _, def := range s.loader.All() {
		if def == nil || !def.Enabled || def.Trigger != agent.TriggerPerson || def.Person == nil {
			continue
		}
		if _, err := person.ParseWithin(def.Person.Within); err != nil {
			s.log.Warn("person trigger has an unusable window", zap.String("agent", def.ID), zap.Error(err))
			continue
		}
		if _, err := person.ParseCooldown(def.Person.Cooldown); err != nil {
			s.log.Warn("person trigger has an unusable cooldown", zap.String("agent", def.ID), zap.Error(err))
			continue
		}
		out = append(out, def)
	}
	return out
}

// firePersonTriggers runs every agent whose condition the change satisfies.
//
// Called after a digest, with the model as it was before and as it is now.
// Never blocks the caller: the phone that pushed the observations is often in
// a background window measured in seconds.
func (s *Server) firePersonTriggers(before, after person.Model, owner string, principal runtime.Principal) {
	defs := s.personTriggerAgents()
	if len(defs) == 0 {
		return
	}
	now := time.Now().UTC()
	for _, def := range defs {
		within, _ := person.ParseWithin(def.Person.Within)
		condition := person.TriggerCondition{When: strings.TrimSpace(def.Person.When), Within: within}
		fires := person.Evaluate(before, after, []person.TriggerCondition{condition}, now)
		if len(fires) == 0 {
			continue
		}
		cooldown, _ := person.ParseCooldown(def.Person.Cooldown)
		if !claimPersonTrigger(def.ID, owner, cooldown, now) {
			s.log.Debug("person trigger still cooling down", zap.String("agent", def.ID), zap.String("owner", owner))
			continue
		}
		if s.scheduler != nil && !s.scheduler.TryStartRun(def.ID) {
			s.log.Debug("person trigger skipped; agent already running", zap.String("agent", def.ID))
			continue
		}
		// One run per evaluation even when several commitments matched. The
		// agent is told about all of them; waking it once per line is how an
		// assistant turns a quiet morning into a pile of notifications.
		go s.runPersonTrigger(def, fires, owner, principal)
	}
}

// claimPersonTrigger reports whether this agent may run for this person now,
// and records the run when it may.
func claimPersonTrigger(agentID, owner string, cooldown time.Duration, now time.Time) bool {
	key := agentID + "\x00" + owner
	personTriggerCooldowns.Lock()
	defer personTriggerCooldowns.Unlock()
	if last, ok := personTriggerCooldowns.last[key]; ok && now.Sub(last) < cooldown {
		return false
	}
	personTriggerCooldowns.last[key] = now
	return true
}

// personTriggerMessage turns the reasons into the agent's inbound turn. An
// agent that does not know why it was woken will greet the person instead of
// answering.
func personTriggerMessage(def *agent.Definition, fires []person.Fire, owner string) message.Message {
	reasons := make([]string, 0, len(fires))
	for _, fire := range fires {
		reasons = append(reasons, fire.Reason)
	}
	sessionID := fmt.Sprintf("person-%s-%d", def.ID, time.Now().UnixNano())
	return message.Message{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		AgentID:   def.ID,
		Channel:   "internal",
		UserID:    owner,
		Username:  "person-trigger",
		Role:      message.RoleUser,
		Parts: message.Text("__trigger:person__ " + strings.Join(reasons, " ") +
			" Decide whether this is worth telling them about, and say nothing if it is not."),
		CreatedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"trigger":        "person",
			"person.when":    fires[0].When,
			"person.owner":   owner,
			"person.reasons": strings.Join(reasons, " "),
		},
	}
}

func (s *Server) runPersonTrigger(def *agent.Definition, fires []person.Fire, owner string, principal runtime.Principal) {
	if s.scheduler != nil {
		defer s.scheduler.FinishRun(def.ID)
	}
	msg := personTriggerMessage(def, fires, owner)
	ctx, cancel := context.WithTimeout(context.Background(), s.resolveRunTimeout(def))
	defer cancel()
	ctx = runtime.WithPrincipal(ctx, principal)
	ctx = llm.WithCallMetadata(ctx, llm.CallMetadata{
		Subject: principal.Subject, AgentID: def.ID, SessionID: msg.SessionID,
		RunID: msg.ID, Source: "person", Trigger: "person",
	})
	s.log.Info("person trigger fired",
		zap.String("agent", def.ID), zap.String("when", fires[0].When), zap.Int("reasons", len(fires)))

	reply, err := s.engine.Handle(ctx, msg)
	if err != nil {
		s.log.Warn("person-triggered run failed", zap.String("agent", def.ID), zap.Error(err))
		return
	}
	// An agent that decided there was nothing worth saying must not produce a
	// notification. Silence is the feature.
	if strings.TrimSpace(plainText(reply)) == "" {
		s.log.Debug("person trigger produced nothing to say", zap.String("agent", def.ID))
		return
	}
	if reply.Metadata == nil {
		reply.Metadata = map[string]string{}
	}
	reply.Metadata["trigger"] = "person"
	reply.Metadata["person.when"] = fires[0].When
	if adapter := mobilechan.DefaultAdapter(); adapter != nil && mobilechan.DefaultStore() != nil {
		if err := adapter.Send(ctx, reply); err != nil {
			s.log.Warn("person-triggered delivery failed", zap.String("agent", def.ID), zap.Error(err))
		}
	}
	if s.hub != nil {
		s.hub.Emit(message.Event{
			Type: "trigger.person", AgentID: def.ID, SessionID: msg.SessionID, Timestamp: time.Now().UTC(),
			Payload: map[string]any{"when": fires[0].When, "reasons": len(fires)},
		})
	}
}

// plainText is the reply's text, for deciding whether anything was said.
func plainText(msg message.Message) string {
	var parts []string
	for _, part := range msg.Parts {
		if part.Type == message.ContentText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}
