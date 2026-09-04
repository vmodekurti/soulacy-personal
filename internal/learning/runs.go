package learning

import (
	"encoding/json"
	"strings"

	"github.com/soulacy/soulacy/pkg/message"
)

// RunEvidence is the distilled learning material from one recorded agent run.
type RunEvidence struct {
	SessionID string
	UserText  string
	ReplyText string
	Channel   string
	Tools     []string
	FoundIn   bool
	FoundOut  bool
}

func RunFromEvents(events []message.Event, agentID, sessionID string) RunEvidence {
	out := RunEvidence{SessionID: sessionID}
	for _, ev := range events {
		if ev.AgentID != agentID || ev.SessionID != sessionID {
			continue
		}
		switch ev.Type {
		case "message.in":
			if text := MessageText(ev.Payload); strings.TrimSpace(text) != "" {
				out.UserText = text
				out.FoundIn = true
			}
			if ch := payloadString(ev.Payload, "channel"); ch != "" {
				out.Channel = ch
			}
		case "message.out":
			if text := MessageText(ev.Payload); strings.TrimSpace(text) != "" {
				out.ReplyText = text
				out.FoundOut = true
			}
		case "tool.call", "tool.result":
			if name := payloadString(ev.Payload, "name"); name != "" {
				out.Tools = append(out.Tools, name)
			}
		}
	}
	out.Tools = UniqueTools(out.Tools)
	return out
}

func RunsFromRecentEvents(events []message.Event, agentID string, maxRuns int) []RunEvidence {
	if maxRuns <= 0 {
		maxRuns = 20
	}
	bySession := map[string]*RunEvidence{}
	order := make([]string, 0)
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.AgentID != agentID || strings.TrimSpace(ev.SessionID) == "" {
			continue
		}
		if _, ok := bySession[ev.SessionID]; !ok {
			if len(order) >= maxRuns {
				continue
			}
			bySession[ev.SessionID] = &RunEvidence{SessionID: ev.SessionID}
			order = append(order, ev.SessionID)
		}
		run := bySession[ev.SessionID]
		switch ev.Type {
		case "message.in":
			if text := MessageText(ev.Payload); strings.TrimSpace(text) != "" && run.UserText == "" {
				run.UserText = text
				run.FoundIn = true
			}
			if ch := payloadString(ev.Payload, "channel"); ch != "" && run.Channel == "" {
				run.Channel = ch
			}
		case "message.out":
			if text := MessageText(ev.Payload); strings.TrimSpace(text) != "" && run.ReplyText == "" {
				run.ReplyText = text
				run.FoundOut = true
			}
		case "tool.call", "tool.result":
			if name := payloadString(ev.Payload, "name"); name != "" {
				run.Tools = append(run.Tools, name)
			}
		}
	}
	out := make([]RunEvidence, 0, len(order))
	for _, sessionID := range order {
		run := *bySession[sessionID]
		run.Tools = UniqueTools(run.Tools)
		out = append(out, run)
	}
	return out
}

func UniqueTools(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func MessageText(payload any) string {
	if msg, ok := payload.(message.Message); ok {
		return partsText(msg.Parts)
	}
	var m map[string]any
	if !payloadMap(payload, &m) {
		return ""
	}
	if text := payloadString(m, "text"); text != "" {
		return text
	}
	if parts, ok := m["parts"].([]any); ok {
		var b strings.Builder
		for _, part := range parts {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := pm["text"].(string); strings.TrimSpace(t) != "" {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString(t)
			}
		}
		return b.String()
	}
	return ""
}

func partsText(parts []message.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

func payloadString(payload any, key string) string {
	var m map[string]any
	if !payloadMap(payload, &m) {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func payloadMap(payload any, out *map[string]any) bool {
	if m, ok := payload.(map[string]any); ok {
		*out = m
		return true
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(b, out); err != nil {
		return false
	}
	return true
}
