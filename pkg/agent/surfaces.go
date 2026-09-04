package agent

import "strings"

// Surface names an interface where an agent can appear or be invoked.
const (
	SurfaceChat     = "chat"
	SurfaceSchedule = "schedule"
)

// EffectiveSurfaces returns where this agent should appear. If Surfaces is set
// explicitly it is returned as-is; otherwise it is derived from the trigger and
// channels so existing agents (no Surfaces field) behave sensibly:
//   - cron/oneshot  → ["schedule"]              (NOT chat — keeps the Chat picker clean)
//   - channel       → its channels (+ "chat" as a manual fallback)
//   - webhook       → ["webhook"]
//   - internal/manual/unknown → ["chat"]        (manually/programmatically run)
//
// This is the deterministic basis for interface-aware design (Stories #11/#12).
func (d *Definition) EffectiveSurfaces() []string {
	if len(d.Surfaces) > 0 {
		return append([]string(nil), d.Surfaces...)
	}
	switch d.Trigger {
	case TriggerCron, TriggerOneShot:
		return []string{SurfaceSchedule}
	case TriggerChannel:
		out := append([]string(nil), d.Channels...)
		out = append(out, SurfaceChat)
		return out
	case TriggerWebhook:
		return []string{"webhook"}
	default: // internal, manual, unknown
		return []string{SurfaceChat}
	}
}

// AppearsOn reports whether the agent should appear on the given surface
// (case-insensitive). Used to filter the Chat picker, channel routing, etc.
func (d *Definition) AppearsOn(surface string) bool {
	surface = strings.ToLower(strings.TrimSpace(surface))
	for _, s := range d.EffectiveSurfaces() {
		if strings.ToLower(strings.TrimSpace(s)) == surface {
			return true
		}
	}
	return false
}

// AppearsOnChat is the common case: should this agent show in the Chat picker?
func (d *Definition) AppearsOnChat() bool { return d.AppearsOn(SurfaceChat) }
