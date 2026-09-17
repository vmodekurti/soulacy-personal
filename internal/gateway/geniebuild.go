// geniebuild.go — Genie hands a build to the builder instead of improvising one.
//
// Genie could already create a monitor: a prompt, a cron, and an agent minted
// on the spot. That is the right shape for "watch this and tell me", and the
// wrong shape for almost anything else. Asked for something that needs a tool,
// a delivery channel or more than one step, Genie either produced a monitor
// that could not do the job or told the user to go and use Studio — which, for
// someone who came to Soulacy to talk to Genie, is the product admitting that
// the front door is not the way in.
//
// The web front door already routes a build to the conversational builder. It
// does so from the screen, around Genie, which works there and nowhere else:
// not on the phone, not on a channel, not in a voice session. So the builder
// becomes a tool Genie can call, and every surface gets the same behaviour.
//
// What Genie gets is the same pipeline the Studio screen drives — the same
// clarifying questions, the same tool resolution against what is actually
// installed, the same save gate — with the conversation relayed through
// Genie's own reply rather than a second chat window.
package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/runtime"
)

// BuildAgentForGenie runs one turn of the builder on Genie's behalf.
//
// It returns one of two shapes, because the builder conversation has two
// outcomes and collapsing them would make Genie guess:
//
//	status "needs_detail" — the builder asked something. The question and the
//	session come back, and Genie asks the user and calls again with the same
//	session and their answer.
//
//	status "built" — the agent exists. The id, what it will do and when it
//	will run come back, so Genie can say what happened rather than "done".
//
// An error is a failure, never a refusal: a refusal comes back as a result
// Genie can read out, since "the gate said no and here is why" is something
// the user can act on and "build_agent failed" is not.
func (s *Server) BuildAgentForGenie(ctx context.Context, session, request string) (map[string]any, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return nil, fmt.Errorf("build_agent: say what the agent should do, in the user's own words")
	}
	if s.engine == nil {
		return nil, fmt.Errorf("build_agent: the builder is unavailable on this install")
	}
	session = strings.TrimSpace(session)
	if session == "" {
		session = "genie-build-" + uuid.New().String()
	}

	provider, model := s.resolveProviderModel("", "")
	resp, err := s.engine.BuilderChat(ctx, session, request, provider, s.buildToolCatalogPrompt())
	if err != nil {
		return nil, fmt.Errorf("build_agent: %w", err)
	}

	// Not ready is the normal case on the first turn, and it is not a failure:
	// the builder has asked for the one thing it still needs. Genie relays the
	// question rather than inventing an answer to it.
	if resp == nil || !resp.Ready {
		question := ""
		if resp != nil {
			question = strings.TrimSpace(resp.Reply)
		}
		if question == "" {
			question = "What should this agent do, and when should it run?"
		}
		out := map[string]any{
			"status":   "needs_detail",
			"session":  session,
			"question": question,
			"hint":     "ask the user this, then call build_agent again with the same session and their answer as the request",
		}
		if resp != nil && resp.Understanding != nil && len(resp.Understanding.Missing) > 0 {
			out["missing"] = resp.Understanding.Missing
		}
		return out, nil
	}

	// A schedule the user asked for in conversation is armed here, unlike the
	// screen's deploy, which leaves it for a person to arm after watching one
	// run. There is no screen in this conversation to come back to, and Genie
	// can list and pause what it built — so the honest options are to arm it
	// and say so, or to promise a daily job that never fires.
	scheduleWanted := resp.Understanding != nil && resp.Understanding.Trigger != nil &&
		strings.EqualFold(strings.TrimSpace(resp.Understanding.Trigger.Type), "cron")

	built, err := s.deployFromUnderstanding(ctx, resp.Understanding, builderDeployOptions{
		Provider:         provider,
		Model:            model,
		ActivateSchedule: scheduleWanted,
		// Marked as Genie's, so list_monitors shows it and pause_monitor and
		// cancel_monitor can act on it. Something the user cannot find again
		// is not something they can stop.
		Labels: map[string]string{"soulacy.owner": runtime.GenieAgentID, "soulacy.kind": "agent"},
	})
	if err != nil {
		return nil, fmt.Errorf("build_agent: %w", err)
	}

	switch {
	case len(built.UnknownTools) > 0:
		return map[string]any{
			"status":        "blocked",
			"session":       session,
			"reason":        "the build needs capabilities this install does not have",
			"unknown_tools": built.UnknownTools,
			"hint":          "tell the user which capability is missing and offer to build it without that step",
		}, nil
	case built.Decision.Refused != "":
		return map[string]any{"status": "blocked", "session": session, "reason": built.Decision.Refused}, nil
	case len(built.Decision.Blockers) > 0:
		problems := make([]string, 0, len(built.Decision.Blockers))
		for _, b := range built.Decision.Blockers {
			problems = append(problems, b.Problem)
		}
		return map[string]any{
			"status":  "blocked",
			"session": session,
			"reason":  "the agent did not pass validation",
			"details": problems,
			"hint":    "ask the user for what is missing, then call build_agent again with the same session",
		}, nil
	case built.Decision.RequiresConsent:
		// Nobody has said yes to putting a privileged agent on a channel, and
		// a tool call is not a person saying yes.
		items := make([]string, 0, len(built.Decision.ConsentItems))
		for _, it := range built.Decision.ConsentItems {
			items = append(items, it.Name)
		}
		return map[string]any{
			"status":  "needs_consent",
			"session": session,
			"reason":  "this agent would be reachable on " + strings.Join(items, ", ") + ", which the user has to approve on screen",
			"hint":    "tell the user to finish this one in Studio, where the approval lives",
		}, nil
	}

	s.log.Info("genie built an agent through the builder",
		zap.String("agent_id", built.Def.ID),
		zap.String("builder_session", session),
		zap.Bool("scheduled", built.Scheduled),
	)

	out := map[string]any{
		"status":    "built",
		"session":   session,
		"agent_id":  built.Def.ID,
		"name":      built.Def.Name,
		"enabled":   built.Def.Enabled,
		"scheduled": built.Scheduled,
	}
	if built.Def.Description != "" {
		out["description"] = built.Def.Description
	}
	if built.Def.Schedule != nil && strings.TrimSpace(built.Def.Schedule.Cron) != "" {
		out["cron"] = built.Def.Schedule.Cron
	}
	if built.Def.Schedule != nil && !built.Scheduled {
		out["schedule_pending"] = true
		out["hint"] = "tell the user it is saved but not running on a schedule yet"
	}
	// An agent that reports success and then delivers nothing forever is worse
	// than one that refuses, so the warning travels with the good news.
	if built.Delivery != "" {
		out["delivery_warning"] = built.Delivery
	}
	return out, nil
}
