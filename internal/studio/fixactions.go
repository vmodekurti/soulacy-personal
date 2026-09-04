package studio

// fixactions.go — the one vocabulary of remediation actions.
//
// Every kind of finding Studio produces — preflight issues, contract checks,
// security findings, readiness items — ends up in front of a user who wants to
// know one thing: what do I click. Prose alone cannot answer that. An id can:
// the client turns it into a button that either lands the user on the screen
// that owns the setting, or edits the draft outright.
//
// The ids and their button text live HERE, in one list, for two reasons.
//
// First, a button's wording is part of the finding, not part of the panel. When
// three panels each kept their own switch, the same action read as "Configure
// provider" in one place and "Fix this" in another.
//
// Second, this is a hand-maintained seam across two languages, and it has
// already drifted twice in this codebase. Studio.svelte's readinessAction once
// handled five actions the server never emitted while six the server did emit
// fell through to a no-op — a "Fix this" button that did nothing at all. The
// security panel then grew its own parallel vocabulary. Both sides were
// internally consistent; nothing checked them against each other.
//
// So: add an action here, and gui/src/lib/studio/fixactions.js must handle it.
// fixactions.test.js reads both files and fails the build in either direction.

import "strings"

// What pressing the button does.
const (
	// FixKindNavigate sends the user to the screen that owns the setting. The
	// change is not Studio's to make — a provider key, a channel binding, an
	// operator's acceptance of risk.
	FixKindNavigate = "navigate"
	// FixKindApply edits the draft in place. Studio owns the value, so the
	// button can just set it.
	FixKindApply = "apply"
	// FixKindFocus keeps the user where they are and moves their attention —
	// reveals a node, opens the bench, switches wizard step.
	FixKindFocus = "focus"
)

// Action ids. These strings cross the wire; treat them as API.
const (
	FixOpenProviders = "open_providers"
	FixOpenMCP       = "open_mcp"
	FixOpenDelivery  = "open_delivery"
	FixOpenSecrets   = "open_secrets"

	FixChooseModel   = "choose_model"
	FixAddAssertions = "add_assertions"
	FixRunLive       = "run_live"
	FixOpenStudio    = "open_studio"
	FixOpenPreflight = "open_preflight"
	FixRevealNode    = "reveal_node"
	FixRenameAgent   = "rename_agent"

	// Draft edits Studio performs itself.
	FixInternalChannelsOnly = "restrict_to_internal_channels"
	FixIntentGateDeny       = "set_intent_gate_deny"
	FixWriteHelperPrompt    = "write_helper_prompt"
	FixSetJoinNode          = "set_join_node"
)

// FixAction is one entry in the vocabulary.
type FixAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// fixActions is the whole vocabulary, in the order a UI would sensibly list it.
var fixActions = []FixAction{
	{FixOpenProviders, "Configure provider", FixKindNavigate},
	{FixOpenMCP, "Connect server", FixKindNavigate},
	{FixOpenDelivery, "Open Delivery", FixKindNavigate},
	{FixOpenSecrets, "Add the secret", FixKindNavigate},

	{FixChooseModel, "Choose a model", FixKindFocus},
	{FixAddAssertions, "Add an assertion", FixKindFocus},
	{FixRunLive, "Run it live", FixKindFocus},
	{FixOpenStudio, "Open the editor", FixKindFocus},
	{FixOpenPreflight, "Open the editor", FixKindFocus},
	{FixRevealNode, "Show the step", FixKindFocus},
	{FixRenameAgent, "Rename this agent", FixKindFocus},

	{FixInternalChannelsOnly, "Use internal channels only", FixKindApply},
	{FixIntentGateDeny, "Set the intent gate to deny", FixKindApply},
	{FixWriteHelperPrompt, "Write a starter prompt", FixKindApply},
	{FixSetJoinNode, "Set the join step", FixKindApply},
}

// FixActions returns the vocabulary. Copied so callers cannot mutate it.
func FixActions() []FixAction {
	out := make([]FixAction, len(fixActions))
	copy(out, fixActions)
	return out
}

// FixActionByID looks up one entry.
func FixActionByID(id string) (FixAction, bool) {
	id = strings.TrimSpace(id)
	for _, a := range fixActions {
		if a.ID == id {
			return a, true
		}
	}
	return FixAction{}, false
}

// FixActionLabel is the button text for an id, or "" when the id is unknown.
//
// Returning "" rather than a friendly default is deliberate: an unknown id is a
// bug — the client will not have a handler for it either — and a plausible
// label would hide that behind a button that silently does nothing.
func FixActionLabel(id string) string {
	if a, ok := FixActionByID(id); ok {
		return a.Label
	}
	return ""
}

// IsFixAction reports whether an id is in the vocabulary.
func IsFixAction(id string) bool {
	_, ok := FixActionByID(id)
	return ok
}

// FixActionIDs lists every id, for tests and for clients that want to validate.
func FixActionIDs() []string {
	out := make([]string, 0, len(fixActions))
	for _, a := range fixActions {
		out = append(out, a.ID)
	}
	return out
}

// resolveFixLabel picks the button text for a finding: its own override when it
// has one (so a finding can be specific about what its button does in context),
// otherwise the vocabulary's default.
func resolveFixLabel(action, override string) string {
	if s := strings.TrimSpace(override); s != "" {
		return s
	}
	return FixActionLabel(action)
}
