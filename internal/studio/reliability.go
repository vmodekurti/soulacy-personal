package studio

import (
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

const completionContractHeading = "Completion contract:"

// completionContractPrompt is appended to Studio-generated system prompts so
// both fixed workflows and reasoning agents share the same "done means done"
// contract. It is deliberately short and generic: the validator enforces the
// shape, while the prompt prevents the model from treating an intermediate
// search/tool result as a successful final answer.
func completionContractPrompt(draft Draft) string {
	intent := strings.ToLower(strings.TrimSpace(draft.Intent + " " + draft.RawIntent))
	if intent == "" && !requiresCompletionContract(draft) {
		return ""
	}
	var b strings.Builder
	b.WriteString(completionContractHeading)
	b.WriteString(" A run is complete only when every user-requested operation has either succeeded or returned a clear, useful fallback. Raw tool JSON, search results, IDs, delivery receipts, or partial intermediate data are not final answers. Before responding, verify that discovery, fetching, transformation, storage, artifact creation, polling, and delivery steps requested by the user have all been handled. If a required step cannot be completed, say exactly which step failed and what remains.")
	if strings.Contains(intent, "podcast") || strings.Contains(intent, "audio overview") || strings.Contains(intent, "notebooklm") || strings.Contains(intent, "notebook lm") {
		b.WriteString(" For NotebookLM/audio tasks, do not finish after search: create or select the notebook, add every source, generate the audio artifact, wait/poll until it is ready when supported, then return or deliver the final link/status.")
	}
	if deliveryRequested(intent) || len(draft.Channels) > 0 || (draft.Output != nil && strings.TrimSpace(draft.Output.Channel) != "") {
		b.WriteString(" For channel delivery, produce a human-readable message first; delivery is successful only after a configured channel route accepts that message, or after you report a clear routing problem.")
	}
	return b.String()
}

func requiresCompletionContract(draft Draft) bool {
	intent := strings.ToLower(strings.TrimSpace(draft.Intent + " " + draft.RawIntent))
	if deliveryRequested(intent) || artifactRequested(intent) || storageRequested(intent) || multiStepRequested(intent) {
		return true
	}
	if len(draft.Channels) > 0 || draft.Output != nil {
		return true
	}
	for _, n := range draft.Flow.Nodes {
		if nodeSuggestsCompletionContract(n) {
			return true
		}
	}
	for _, t := range draft.Tools {
		if toolSuggestsCompletionContract(t) {
			return true
		}
	}
	return false
}

func nodeSuggestsCompletionContract(n sdkr.FlowNode) bool {
	return toolSuggestsCompletionContract(n.Tool) || strings.EqualFold(strings.TrimSpace(n.Kind), "agent")
}

func toolSuggestsCompletionContract(tool string) bool {
	t := strings.ToLower(strings.TrimSpace(tool))
	return strings.Contains(t, "channel.send") ||
		strings.Contains(t, "kb_write") ||
		strings.Contains(t, "notebook") ||
		strings.Contains(t, "generate") ||
		strings.Contains(t, "poll") ||
		strings.Contains(t, "audio")
}

func completionContractValidateIssues(draft Draft) ([]ValidateError, []ValidateWarning) {
	var errs []ValidateError
	var warns []ValidateWarning
	intent := strings.ToLower(strings.TrimSpace(draft.Intent + " " + draft.RawIntent))
	if intent == "" {
		return nil, nil
	}

	if deliveryRequested(intent) && !hasDeliveryConfigured(draft) && !hasContextualDelivery(draft) {
		errs = append(errs, ValidateError{Source: ValidateSourceCompletion, Message: "The intent asks for delivery/notification, but no routable output channel or schedule output is configured. HTTP is an inbound trigger/testing surface, not a cron delivery destination."})
	}
	if strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "schedule") && !hasDeliveryConfigured(draft) && normalizeStudioDeliveryMode(draft.DeliveryMode) != "none" {
		warns = append(warns, ValidateWarning{Message: "This scheduled agent has no explicit output channel. If no global default output exists, completed runs will only appear in Runs/Activity."})
	}

	if !draft.IsAgent() {
		if searchOnlyFinal(draft) && (artifactRequested(intent) || deliveryRequested(intent) || storageRequested(intent) || multiStepRequested(intent)) {
			errs = append(errs, ValidateError{Source: ValidateSourceCompletion, Message: "This workflow appears to stop at discovery/search, but the intent asks for a finished artifact, stored content, delivery, or a complete report."})
		}
		if notebookRequested(intent) && !hasNotebookOperation(draft) {
			errs = append(errs, ValidateError{Source: ValidateSourceCompletion, Message: "The intent asks for NotebookLM/audio/podcast work, but the graph does not include NotebookLM create/add/generate/poll steps."})
		}
		if storageRequested(intent) && !hasToolContaining(draft, "kb_write") {
			warns = append(warns, ValidateWarning{Message: "The intent asks to store or ingest content into Knowledge, but the graph does not call kb_write."})
		}
	}

	if draft.IsAgent() {
		if strings.Contains(strings.ToLower(draft.SystemPrompt), strings.ToLower(completionContractHeading)) || hasEditableCompletionContract(draft) {
			return errs, warns
		}
		if requiresCompletionContract(draft) {
			warns = append(warns, ValidateWarning{Message: "This reasoning agent should include a completion contract so it does not stop after intermediate tool results."})
		}
	}
	return errs, warns
}

func hasEditableCompletionContract(draft Draft) bool {
	return draft.Policy != nil && draft.Policy.Contract != nil && strings.TrimSpace(draft.Policy.Contract.CompletionCriteria) != ""
}

func hasDeliveryConfigured(draft Draft) bool {
	if draft.Output != nil && isRoutableOutputChannel(draft.Output.Channel) {
		return true
	}
	for _, ch := range draft.Channels {
		if isRoutableOutputChannel(ch) {
			return true
		}
	}
	return false
}

func hasContextualDelivery(draft Draft) bool {
	// Scheduled work has no inbound request to reply to, so it always needs an
	// actual destination when delivery is requested. Manual, channel, webhook,
	// and other interactive invocation paths can return their normal result.
	if strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "schedule") {
		// "Runs / Activity only" is an explicit, valid output choice. It
		// intentionally overrides delivery language inferred from the prompt.
		return normalizeStudioDeliveryMode(draft.DeliveryMode) == "none"
	}
	switch normalizeStudioDeliveryMode(draft.DeliveryMode) {
	case "reply", "none":
		return true
	case "outbound":
		return false
	default:
		// Backward compatibility for drafts saved before DeliveryMode existed.
		// A channel trigger naturally has an invocation route, and explicit
		// same-channel wording is sufficiently unambiguous for manual/webhook
		// agents. New saves persist the structured choice above.
		if strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "channel") || strings.EqualFold(strings.TrimSpace(draft.Trigger.Type), "chat") {
			return true
		}
		intent := strings.ToLower(draft.Intent + " " + draft.RawIntent)
		return completionContainsAny(intent, "same channel", "same conversation", "reply directly", "return to the caller")
	}
}

func normalizeStudioDeliveryMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "reply", "none", "outbound":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return ""
	}
}

func normalizeStudioTriggerMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "chat", "manual", "internal", "schedule", "cron", "channel", "webhook":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return ""
	}
}

func isRoutableOutputChannel(ch string) bool {
	switch strings.ToLower(strings.TrimSpace(ch)) {
	case "", "http", "webhook":
		return false
	default:
		return true
	}
}

func searchOnlyFinal(draft Draft) bool {
	if len(draft.Flow.Nodes) == 0 {
		return false
	}
	final := strings.TrimSpace(draft.Flow.Output)
	if final == "" {
		final = strings.TrimSpace(lastNodeID(draft.Flow))
	}
	if final == "" {
		return false
	}
	var n sdkr.FlowNode
	found := false
	for _, node := range draft.Flow.Nodes {
		if node.ID == final {
			n = node
			found = true
			break
		}
	}
	if !found {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(n.Tool + " " + n.ID + " " + n.Output))
	return n.Kind == sdkr.FlowNodeTool && (strings.Contains(t, "search") || strings.Contains(t, "list"))
}

func lastNodeID(flow Flow) string {
	if len(flow.Nodes) == 0 {
		return ""
	}
	return flow.Nodes[len(flow.Nodes)-1].ID
}

func hasNotebookOperation(draft Draft) bool {
	for _, n := range draft.Flow.Nodes {
		t := strings.ToLower(strings.TrimSpace(n.Tool + " " + n.ID + " " + n.Description))
		if strings.Contains(t, "notebook") && (strings.Contains(t, "create") || strings.Contains(t, "add") || strings.Contains(t, "source") || strings.Contains(t, "audio") || strings.Contains(t, "generate") || strings.Contains(t, "poll")) {
			return true
		}
	}
	for _, t := range draft.Tools {
		if strings.Contains(strings.ToLower(t), "notebook") {
			return true
		}
	}
	return false
}

func hasToolContaining(draft Draft, needle string) bool {
	needle = strings.ToLower(needle)
	for _, n := range draft.Flow.Nodes {
		if strings.Contains(strings.ToLower(n.Tool), needle) {
			return true
		}
	}
	for _, t := range draft.Tools {
		if strings.Contains(strings.ToLower(t), needle) {
			return true
		}
	}
	return false
}

func deliveryRequested(intent string) bool {
	return completionContainsAny(intent, "send", "deliver", "notify", "notification", "telegram", "slack", "discord", "whatsapp", "email", "channel", "dm ")
}

func artifactRequested(intent string) bool {
	return completionContainsAny(intent, "podcast", "audio overview", "briefing", "report", "digest", "summary", "document", "file", "chart")
}

func storageRequested(intent string) bool {
	return completionContainsAny(intent, "store", "save", "ingest", "knowledge", "kb", "catalog", "archive")
}

func notebookRequested(intent string) bool {
	return completionContainsAny(intent, "notebooklm", "notebook lm", "podcast", "audio overview")
}

func multiStepRequested(intent string) bool {
	hits := 0
	for _, word := range []string{"search", "find", "fetch", "read", "summarize", "rank", "filter", "create", "generate", "poll", "store", "send", "deliver"} {
		if strings.Contains(intent, word) {
			hits++
		}
	}
	return hits >= 2
}

func completionContainsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
