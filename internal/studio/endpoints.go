package studio

import (
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// Endpoint blocks (Phase A — visual authoring). A trigger/exit node carries its
// configuration in Params:
//
//	trigger: { "kind": "cron"|"http"|"channel", "config": { "cron": "...", "channel": "..." } }
//	exit:    { "route": "http"|"channel"|"console", "config": { "channel": "..." } }
//
// These make the canvas the source of truth for HOW the flow starts and where its
// result goes, instead of that living only in draft-level metadata. DeriveEndpoints
// projects them back onto Draft.Trigger / Draft.Channels so the existing save,
// schedule, and channel-exposure logic (which reads those fields) reflects what
// the user wired. ValidateEndpoints reports authoring mistakes. Both are no-ops
// for legacy drafts that contain no endpoint nodes.

// triggerNode returns the first trigger node and true, or false when none.
func triggerNodes(d *Draft) []*sdkr.FlowNode {
	var out []*sdkr.FlowNode
	for i := range d.Flow.Nodes {
		if d.Flow.Nodes[i].Kind == sdkr.FlowNodeTrigger {
			out = append(out, &d.Flow.Nodes[i])
		}
	}
	return out
}

func exitNodes(d *Draft) []*sdkr.FlowNode {
	var out []*sdkr.FlowNode
	for i := range d.Flow.Nodes {
		if d.Flow.Nodes[i].Kind == sdkr.FlowNodeExit {
			out = append(out, &d.Flow.Nodes[i])
		}
	}
	return out
}

// paramStr reads a string Params value (trimmed), "" when absent/non-string.
func paramStr(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	if v, ok := p[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// paramConfig reads the nested "config" object from a node's Params.
func paramConfig(p map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	if v, ok := p["config"].(map[string]any); ok {
		return v
	}
	return nil
}

// addChannel appends ch to d.Channels if not already present (case-insensitive).
func addChannel(d *Draft, ch string) {
	ch = strings.TrimSpace(ch)
	if ch == "" {
		return
	}
	for _, c := range d.Channels {
		if strings.EqualFold(c, ch) {
			return
		}
	}
	d.Channels = append(d.Channels, ch)
}

// DeriveEndpoints makes the canvas authoritative: when the flow contains trigger/
// exit blocks, it projects their config onto Draft.Trigger and Draft.Channels and
// points the flow entry at the trigger. No-op when there are no endpoint nodes, so
// drafts authored the old (metadata-only) way are unaffected.
func DeriveEndpoints(d *Draft) {
	if d == nil {
		return
	}
	triggers := triggerNodes(d)
	if len(triggers) > 0 {
		t := triggers[0]
		kind := strings.ToLower(paramStr(t.Params, "kind"))
		cfg := paramConfig(t.Params)
		switch kind {
		case "cron", "schedule":
			d.Trigger.Type = "schedule"
			if cron := paramStr(cfg, "cron"); cron != "" {
				if d.Trigger.Config == nil {
					d.Trigger.Config = map[string]any{}
				}
				d.Trigger.Config["cron"] = cron
			}
		case "http", "webhook":
			d.Trigger.Type = "webhook"
		case "channel":
			d.Trigger.Type = "channel"
			if ch := paramStr(cfg, "channel"); ch != "" {
				addChannel(d, ch)
			}
		case "chat":
			d.Trigger.Type = "chat"
		case "manual":
			d.Trigger.Type = "manual"
		}
		// The trigger block is where the flow starts.
		if strings.TrimSpace(t.ID) != "" {
			d.Flow.Entry = t.ID
		}
	}

	// Exit blocks that deliver to a channel contribute to the channel set.
	for _, ex := range exitNodes(d) {
		route := strings.ToLower(paramStr(ex.Params, "route"))
		cfg := paramConfig(ex.Params)
		if route == "channel" {
			if ch := paramStr(cfg, "channel"); ch != "" {
				addChannel(d, ch)
			}
		}
	}
}

// EndpointIssue is one authoring problem with the trigger/exit wiring.
type EndpointIssue struct {
	Severity string `json:"severity"` // "block" | "warn"
	Node     string `json:"node,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// ValidateEndpoints reports trigger/exit authoring mistakes. It only fires when
// the user has opted into endpoint blocks (at least one trigger OR exit present),
// so legacy metadata-only drafts produce no issues.
func ValidateEndpoints(d *Draft) []EndpointIssue {
	if d == nil {
		return nil
	}
	triggers := triggerNodes(d)
	exits := exitNodes(d)
	if len(triggers) == 0 && len(exits) == 0 {
		return nil
	}
	var issues []EndpointIssue
	if len(triggers) > 1 {
		issues = append(issues, EndpointIssue{
			Severity: "block",
			Node:     triggers[1].ID,
			Message:  "A flow can have only one trigger block.",
			Fix:      "Remove the extra trigger so exactly one block starts the flow.",
		})
	}
	if len(triggers) == 1 && strings.TrimSpace(d.Flow.Entry) != "" &&
		d.Flow.Entry != triggers[0].ID {
		issues = append(issues, EndpointIssue{
			Severity: "warn",
			Node:     triggers[0].ID,
			Message:  "The flow does not start at the trigger block.",
			Fix:      "Connect the trigger as the first block (it becomes the entry on save).",
		})
	}
	if len(triggers) >= 1 && len(exits) == 0 {
		issues = append(issues, EndpointIssue{
			Severity: "warn",
			Message:  "The flow has no exit block, so its result isn't delivered anywhere.",
			Fix:      "Add an exit block (HTTP, channel, or console) at the end.",
		})
	}
	return issues
}
