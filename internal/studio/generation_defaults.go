package studio

import (
	"fmt"
	"sort"
	"strings"

	intentpkg "github.com/soulacy/soulacy/internal/intent"
	"github.com/soulacy/soulacy/internal/trust"
	"github.com/soulacy/soulacy/pkg/agent"
)

// inferredTriggerFromIntent returns the trigger implied by the prompt without
// letting a pre-filled "manual" default suppress schedule/channel/webhook
// inference. Studio owns this architecture decision, so deterministic builders
// should ask for the inferred trigger before falling back to manual.
func inferredTriggerFromIntent(intent string) Trigger {
	d := Draft{}
	normalizeTrigger(&d, intent)
	if strings.TrimSpace(d.Trigger.Type) == "" {
		d.Trigger = Trigger{Type: "manual"}
	}
	return d.Trigger
}

// applyGenerationDefaults bakes Studio's production defaults into every
// generated draft before contract/preflight/save. These are not LLM decisions:
// they are Soulacy platform rules for scheduled delivery and privileged
// side-effect tools.
func applyGenerationDefaults(d *Draft, intent string) []string {
	if d == nil {
		return nil
	}
	var notes []string

	if shouldReplaceManualTrigger(d.Trigger, intent) {
		inferred := inferredTriggerFromIntent(intent)
		if typ := strings.TrimSpace(inferred.Type); typ != "" && !strings.EqualFold(typ, "manual") {
			d.Trigger = inferred
			if strings.EqualFold(typ, "schedule") {
				notes = append(notes, "Inferred scheduled trigger from the prompt"+cronNote(inferred)+".")
			} else {
				notes = append(notes, "Inferred "+typ+" trigger from the prompt.")
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(d.Trigger.Type), "schedule") {
		d.Unattended = true
	}
	// Interactive replies already travel back through the request's channel.
	// A builder that adds channel.send turns that ordinary response into a
	// second, privileged outbound write, so Chat correctly stops for approval
	// and the user sees their answer trapped inside an Action Required modal.
	// Enforce this after model/deterministic capability selection, before
	// confirmation defaults are derived, so every Studio generation path agrees.
	if normalizeSameChannelReply(d, intent) {
		notes = append(notes, "Removed outbound channel tools from a same-channel conversational agent; normal replies are returned automatically.")
	}

	tools := allDraftTools(*d, nil)
	var added []string
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" || !shouldConfirmGeneratedTool(tool) || confirmToolsContain(d.ConfirmTools, tool) {
			continue
		}
		d.ConfirmTools = append(d.ConfirmTools, tool)
		added = append(added, tool)
	}
	if len(added) > 0 {
		sort.Strings(added)
		d.ConfirmTools = dedupeNonEmpty(d.ConfirmTools)
		notes = append(notes, "Added confirmation gates for privileged generated tools: "+strings.Join(added, ", ")+".")
	}

	if shouldDenyIntentGate(*d, tools) {
		if d.Security == nil {
			d.Security = &agent.SecurityConfig{}
		}
		mode := strings.ToLower(strings.TrimSpace(d.Security.IntentGate))
		if mode == "" || mode == "prompt" {
			d.Security.IntentGate = string(intentpkg.ModeDeny)
			notes = append(notes, "Set security.intent_gate:deny because this generated agent can combine external content with privileged delivery or NotebookLM side effects.")
		}
	}
	return notes
}

func normalizeSameChannelReply(d *Draft, intent string) bool {
	if d == nil || !sameChannelReplyIntent(intent) {
		return false
	}
	removed := false
	d.Tools, removed = withoutChannelDeliveryTools(d.Tools)
	var confirmRemoved bool
	d.ConfirmTools, confirmRemoved = withoutChannelDeliveryTools(d.ConfirmTools)
	removed = removed || confirmRemoved || d.Output != nil
	d.Output = nil

	// Model-written prompts can contradict their own ordinary-reply rule by
	// requiring channel.send in a later Tool Usage or Completion section. Once
	// the tool is removed, delete those stale lines and leave one unambiguous
	// runtime rule. This is intentionally line-based: it preserves the agent's
	// role, market/tool guidance, and all unrelated completion criteria.
	var kept []string
	for _, line := range strings.Split(d.SystemPrompt, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "channel.send") || strings.Contains(low, "channel.status") {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	d.SystemPrompt = strings.TrimSpace(strings.Join(kept, "\n"))
	const replyRule = "For ordinary interactive replies, return the final answer normally; Soulacy automatically routes it back through the inbound channel. Do not perform a separate outbound send."
	if !strings.Contains(d.SystemPrompt, replyRule) {
		d.SystemPrompt = strings.TrimSpace(d.SystemPrompt) + "\n\n" + replyRule
	}
	// The editable operator contract is appended to the system prompt at save
	// time. Sanitize it too, otherwise a removed channel.send instruction is
	// silently reintroduced under "You are done only when".
	if d.Policy != nil && d.Policy.Contract != nil {
		c := d.Policy.Contract
		c.Goal = sameChannelContractText(c.Goal)
		c.Instructions = sameChannelContractText(c.Instructions)
		c.CompletionCriteria = sameChannelContractText(c.CompletionCriteria)
	}
	return removed
}

func sameChannelContractText(text string) string {
	text = strings.ReplaceAll(text, "channel.send", "the normal final response")
	text = strings.ReplaceAll(text, "channel.status", "the inbound channel route")
	return text
}

func withoutChannelDeliveryTools(tools []string) ([]string, bool) {
	out := make([]string, 0, len(tools))
	removed := false
	for _, tool := range tools {
		switch strings.ToLower(strings.TrimSpace(tool)) {
		case "channel.send", "channel.status":
			removed = true
		default:
			out = append(out, tool)
		}
	}
	return out, removed
}

func shouldReplaceManualTrigger(trigger Trigger, intent string) bool {
	typ := strings.ToLower(strings.TrimSpace(trigger.Type))
	if typ == "" {
		return true
	}
	if typ != "manual" {
		return false
	}
	inferred := inferredTriggerFromIntent(intent)
	return inferred.Type != "" && !strings.EqualFold(inferred.Type, "manual")
}

func cronNote(trigger Trigger) string {
	if trigger.Config == nil {
		return ""
	}
	if cron, _ := trigger.Config["cron"].(string); strings.TrimSpace(cron) != "" {
		return fmt.Sprintf(" (%s)", strings.TrimSpace(cron))
	}
	return ""
}

func shouldConfirmGeneratedTool(tool string) bool {
	n := strings.TrimSpace(tool)
	ln := strings.ToLower(n)
	if n == "" {
		return false
	}
	if intentpkg.IsHighRisk(n) {
		return true
	}
	switch {
	case ln == "channel.send":
		return true
	case strings.Contains(ln, "notebooklm__notebook_create"):
		return true
	case strings.Contains(ln, "notebooklm__studio_create"):
		return true
	default:
		return false
	}
}

func confirmToolsContain(confirmTools []string, tool string) bool {
	tool = strings.TrimSpace(tool)
	for _, t := range confirmTools {
		t = strings.TrimSpace(t)
		if t == "*" || strings.EqualFold(t, "all") || strings.EqualFold(t, tool) {
			return true
		}
	}
	return false
}

func shouldDenyIntentGate(d Draft, tools []string) bool {
	hasPrivileged := false
	hasExternal := false
	for _, tool := range tools {
		if shouldConfirmGeneratedTool(tool) {
			hasPrivileged = true
		}
		if trust.ToolTrust(tool) == trust.Untrusted {
			hasExternal = true
		}
	}
	if !hasPrivileged {
		return false
	}
	if hasExternal {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(d.Trigger.Type), "schedule") {
		return true
	}
	return len(d.Channels) > 0
}
