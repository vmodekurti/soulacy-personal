// Package proactive turns recent activity into concrete "you could automate
// this" suggestions. It is the detector behind the assistant persona's
// proactive layer: instead of waiting to be asked, the assistant looks at what
// the user has actually been doing and surfaces a small number of high-signal
// opportunities (schedule a repeated manual task, review a flaky agent, enable
// learning). The analysis is a pure function over already-recorded events plus
// lightweight agent metadata, so it adds no cost to the runtime hot path.
package proactive

import (
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/pkg/message"
)

// Suggestion is one recommended action for the user to consider.
type Suggestion struct {
	Kind    string `json:"kind"`               // "schedule" | "review" | "enable_learning"
	AgentID string `json:"agent_id,omitempty"` // the agent the suggestion is about
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Action  string `json:"action"`          // a short imperative next step
	Score   int    `json:"score"`           // higher = more confident/urgent
	Metric  int    `json:"metric,omitempty"` // the count that triggered it (runs, failures)
}

// AgentSnapshot is the minimal per-agent metadata the detector needs.
type AgentSnapshot struct {
	ID              string
	Name            string
	HasSchedule     bool
	LearningEnabled bool
}

const (
	manualRunThreshold = 3 // repeated manual runs before we suggest scheduling
	failureThreshold   = 2 // failures before we suggest reviewing an agent
)

// manualChannels are inbound channels that indicate a human kicked off the run
// by hand (as opposed to cron/scheduler-driven execution).
var manualChannels = map[string]bool{
	"http": true,
	"chat": true,
	"":     true, // unlabeled inbound defaults to manual
}

// Detect analyzes recent events and returns prioritized suggestions. agents
// supplies per-agent metadata (schedule/learning state); agents not present in
// the map still yield suggestions but with less precise wording.
func Detect(events []message.Event, agents map[string]AgentSnapshot) []Suggestion {
	type stat struct {
		manualRuns int
		failures   int
		total      int
	}
	stats := map[string]*stat{}
	get := func(id string) *stat {
		s := stats[id]
		if s == nil {
			s = &stat{}
			stats[id] = s
		}
		return s
	}

	for _, ev := range events {
		id := strings.TrimSpace(ev.AgentID)
		if id == "" {
			continue
		}
		switch ev.Type {
		case "message.in":
			s := get(id)
			s.total++
			if manualChannels[strings.ToLower(payloadChannel(ev.Payload))] {
				s.manualRuns++
			}
		case "error":
			get(id).failures++
		}
	}

	var out []Suggestion
	for id, s := range stats {
		snap, known := agents[id]
		name := id
		if known && strings.TrimSpace(snap.Name) != "" {
			name = snap.Name
		}

		// 1) Repeated manual runs of an unscheduled agent → suggest scheduling.
		if s.manualRuns >= manualRunThreshold && (!known || !snap.HasSchedule) {
			out = append(out, Suggestion{
				Kind:    "schedule",
				AgentID: id,
				Title:   fmt.Sprintf("Schedule “%s”", name),
				Detail:  fmt.Sprintf("You've run %s by hand %d times recently. If it's routine, a schedule can run it for you and deliver the result to a channel.", name, s.manualRuns),
				Action:  "Open the Schedule page and add a cron trigger",
				Score:   50 + s.manualRuns,
				Metric:  s.manualRuns,
			})
		}

		// 2) Repeated failures → suggest reviewing/repairing the agent.
		if s.failures >= failureThreshold {
			out = append(out, Suggestion{
				Kind:    "review",
				AgentID: id,
				Title:   fmt.Sprintf("Review “%s”", name),
				Detail:  fmt.Sprintf("%s failed %d times recently. Studio can diagnose a failed run from its action log and propose a fix.", name, s.failures),
				Action:  "Open a failed run in Activity and send it to Studio to repair",
				Score:   70 + s.failures*5,
				Metric:  s.failures,
			})
		}

		// 3) Busy agent without learning on → suggest enabling the learning loop.
		if known && !snap.LearningEnabled && s.total >= manualRunThreshold {
			out = append(out, Suggestion{
				Kind:    "enable_learning",
				AgentID: id,
				Title:   fmt.Sprintf("Turn on learning for “%s”", name),
				Detail:  fmt.Sprintf("%s has handled %d runs. With learning enabled it can propose reusable skills and memories from successful runs, which you review before they take effect.", name, s.total),
				Action:  "Set learning.enabled: true in the agent's SOUL.yaml",
				Score:   30 + s.total,
				Metric:  s.total,
			})
		}
	}

	// Highest score first; stable tiebreak by agent id then kind for determinism.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].Kind < out[j].Kind
	})
	if out == nil {
		out = []Suggestion{}
	}
	return out
}

func payloadChannel(payload any) string {
	if m, ok := payload.(map[string]any); ok {
		if c, ok := m["channel"].(string); ok {
			return c
		}
	}
	// message.Message carries a Channel field.
	if msg, ok := payload.(message.Message); ok {
		return msg.Channel
	}
	return ""
}
