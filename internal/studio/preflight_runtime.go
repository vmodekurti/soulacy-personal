// preflight_runtime.go — the RUNTIME half of preflight (ST-07 / ST-08):
// credentials, LLM provider/model, and the machine-readable remediation tokens
// that turn a prose fix into a button.
//
// Why this exists as its own file: the checks here are the ones that were
// missing, and the failure mode they prevent is specific. Preflight validated
// the SHAPE of a draft — graph, templates, arguments, channels — and then
// reported "ready" for an agent whose provider had no API key and whose model
// was not servable. PreflightInput.SecretsSet was collected by the gateway and
// read by nobody in the production path, so a workspace with zero credentials
// still went green. The first time anyone learned otherwise was the first real
// run, which for a scheduled agent is unattended, at 7am, with nobody watching.
//
// Everything here is caller-state driven and silent when that state is absent:
// a nil map means "not supplied", never "nothing is configured". Inventing a
// blocker a user cannot clear is worse than the gap it closes — it trains
// operators to click past the gate.
package studio

import (
	"sort"
	"strings"
)

// ── remediation vocabulary ──────────────────────────────────────────────────

// actionForKind maps a preflight issue Kind onto certify.go's action token
// vocabulary. It is the SAME vocabulary on purpose: a second, parallel token
// set would mean the GUI has to know which surface produced an issue before it
// can render the button, and would silently drop the tokens it didn't
// recognise. "open_studio" is the catch-all — every issue class that is fixed
// by editing the workflow itself resolves there, so a blocker is never left
// without an action.
func actionForKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "secret":
		return "open_providers"
	case "provider":
		return "open_providers"
	case "model":
		return "choose_model"
	case "mcp":
		return "open_mcp"
	case "channel":
		return "open_delivery"
	default:
		// tool | agent | field | dependency | template | schedule | confirmation
		// | policy | … — all of these are repaired in the workflow editor.
		return "open_studio"
	}
}

// ── credentials (ST-07) ─────────────────────────────────────────────────────

// vault key prefixes. These mirror internal/secrets' canonical names
// (llm.providers.<id>.api_key, channels.<id>.<token>). They are re-stated here
// rather than imported because internal/secrets' key builders are unexported —
// but they are only ever used to LOOK UP a name the caller already gave us in
// SecretsSet, never to assert that a name must exist, so a convention drift
// degrades to "no requirement derived" rather than to a false blocker.
const (
	secretPrefixProvider = "llm.providers."
	secretPrefixChannel  = "channels."
	secretPrefixMCP      = "mcp."
)

// DeriveSecretRequirements works out which credentials a draft needs, using the
// caller's own credential inventory (`known`: the SecretsSet map) to discover
// the exact slot names. It deliberately requires only credentials the inventory
// already knows about: a slot the caller never mentioned cannot be asserted as
// required without guessing how this deployment names it, and a guessed
// credential name produces a blocker nobody can clear.
//
// Returned requirements are sorted by name so preflight output is deterministic.
func DeriveSecretRequirements(draft Draft, known map[string]bool) []SecretRequirement {
	if len(known) == 0 {
		return nil
	}
	var out []SecretRequirement
	seen := map[string]bool{}
	push := func(name, kind, owner string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, SecretRequirement{Name: name, Kind: kind, Owner: owner})
	}

	// The agent's runtime provider needs its API key.
	if p := strings.TrimSpace(draft.LLM.Provider); p != "" {
		name := secretPrefixProvider + strings.ToLower(p) + ".api_key"
		if _, ok := known[name]; ok {
			push(name, "provider", p)
		}
	}

	// Each delivery channel needs whatever token slots this deployment defines
	// for it (bot_token, signing_secret, …) — discovered by prefix, so a channel
	// that genuinely needs no credential contributes no requirement.
	for _, ch := range draft.Channels {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		prefix := secretPrefixChannel + strings.ToLower(ch) + "."
		for name := range known {
			if strings.HasPrefix(strings.ToLower(name), prefix) {
				push(name, "channel", ch)
			}
		}
	}

	// MCP servers the draft calls, when the deployment stores per-server keys.
	for _, srv := range draftMCPServers(draft) {
		prefix := secretPrefixMCP + strings.ToLower(srv) + "."
		for name := range known {
			if strings.HasPrefix(strings.ToLower(name), prefix) {
				push(name, "mcp", srv)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// checkCredentials evaluates SecretsSet — the map that used to be collected and
// never read. A missing credential is a BLOCKER, not a warning: the agent
// cannot run at all, and the whole point of preflight is to say so before the
// save rather than after the first failed run. The Fix names the exact secret,
// because "add the missing credential" is not an instruction anyone can follow.
//
// A nil SecretsSet means the caller did not supply credential state, and
// nothing is judged. That is the backward-compatibility contract: a caller that
// never passed secrets must not suddenly see blockers.
func checkCredentials(draft Draft, in PreflightInput, record func(PreflightIssue), pass func(kind, msg string)) {
	if in.SecretsSet == nil {
		return
	}
	reqs := in.RequiredSecrets
	if reqs == nil {
		reqs = DeriveSecretRequirements(draft, in.SecretsSet)
	}
	if len(reqs) == 0 {
		return
	}

	var missing, present []string
	for _, req := range reqs {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			continue
		}
		if in.SecretsSet[name] {
			present = append(present, name)
			continue
		}
		missing = append(missing, name)
		record(PreflightIssue{
			Severity: "block",
			Kind:     "secret",
			Message:  secretRequirementMessage(req) + " has no stored value.",
			Fix:      "Add the credential \"" + name + "\" in Settings → Secrets, then re-save.",
			Action:   "open_providers",
			ActionParams: map[string]string{
				"secret": name,
				"kind":   strings.TrimSpace(req.Kind),
				"owner":  strings.TrimSpace(req.Owner),
			},
		})
	}
	if len(missing) == 0 && len(present) > 0 {
		sort.Strings(present)
		pass("secret", "Every credential this agent needs is stored ("+strings.Join(present, ", ")+").")
	}
}

// secretRequirementMessage renders "who needs this" so the operator can tell a
// provider key apart from a channel token at a glance.
func secretRequirementMessage(req SecretRequirement) string {
	name := strings.TrimSpace(req.Name)
	owner := strings.TrimSpace(req.Owner)
	switch strings.ToLower(strings.TrimSpace(req.Kind)) {
	case "provider":
		return "The LLM provider \"" + owner + "\" needs the credential \"" + name + "\", which"
	case "channel":
		return "Delivery to \"" + owner + "\" needs the credential \"" + name + "\", which"
	case "mcp":
		return "The MCP server \"" + owner + "\" needs the credential \"" + name + "\", which"
	case "tool":
		return "The tool \"" + owner + "\" needs the credential \"" + name + "\", which"
	default:
		return "The credential \"" + name + "\", which this agent requires,"
	}
}

// ── provider / model (ST-08) ────────────────────────────────────────────────

// checkRuntimeModel verifies the agent's OWN runtime provider and model — the
// gap ST-08 names. Studio already advises on the BUILDER model
// (handleStudioModelAdvice), which is a different question: an agent can be
// built by a perfectly healthy builder model and still be unrunnable because
// the provider it will actually run on has no key, or the model id it pins is
// not served here.
//
// Both maps are opt-in. nil means "the caller cannot tell us what is available"
// and no verdict is emitted — an unverifiable check must be silent, not green
// and not red.
func checkRuntimeModel(draft Draft, in PreflightInput, record func(PreflightIssue), pass func(kind, msg string)) {
	provider := strings.TrimSpace(draft.LLM.Provider)
	model := strings.TrimSpace(draft.LLM.Model)

	if in.ProvidersAvailable != nil {
		switch {
		case provider == "" && !anyAvailable(in.ProvidersAvailable):
			record(PreflightIssue{
				Severity: "block", Kind: "provider",
				Message: "No LLM provider is configured, so this agent has nothing to run on.",
				Fix:     "Configure an LLM provider (add its API key) in Settings → Providers.",
				Action:  "open_providers",
			})
		case provider == "":
			// Not a blocker: the agent inherits the workspace default. Worth
			// saying out loud, because the agent's behaviour then changes
			// whenever that default changes, with no edit to the agent.
			record(PreflightIssue{
				Severity: "warn", Kind: "provider",
				Message: "This agent does not pin an LLM provider; it will run on the workspace default.",
				Fix:     "Pin a provider on the agent if its behaviour must not change when the workspace default does.",
				Action:  "open_providers",
			})
		case !in.ProvidersAvailable[provider] && !in.ProvidersAvailable[strings.ToLower(provider)]:
			record(PreflightIssue{
				Severity: "block", Kind: "provider",
				Message:      "This agent runs on the LLM provider \"" + provider + "\", which is not configured or not available.",
				Fix:          "Configure \"" + provider + "\" (add its API key) in Settings → Providers, or switch the agent to a configured provider.",
				Action:       "open_providers",
				ActionParams: map[string]string{"provider": provider},
			})
		default:
			pass("provider", "The agent's LLM provider \""+provider+"\" is configured and available.")
		}
	}

	if in.ModelsAvailable == nil {
		return
	}
	if model == "" {
		record(PreflightIssue{
			Severity: "block", Kind: "model",
			Message: "This agent does not specify a model to run on.",
			Fix:     "Choose a model for this agent.",
			Action:  "choose_model",
		})
		return
	}
	if modelAvailable(in.ModelsAvailable, provider, model) {
		pass("model", "The model \""+model+"\" is available"+providerSuffix(provider)+".")
		return
	}
	record(PreflightIssue{
		Severity: "block", Kind: "model",
		Message:      "The model \"" + model + "\" is not available" + providerSuffix(provider) + ", so this agent cannot run.",
		Fix:          "Choose a model that this workspace can serve, or install/enable \"" + model + "\".",
		Action:       "choose_model",
		ActionParams: map[string]string{"model": model, "provider": provider},
	})
}

// modelAvailable accepts either a bare model id or a provider-qualified
// "provider/model" key, because callers differ in whether model ids are unique
// across providers.
func modelAvailable(avail map[string]bool, provider, model string) bool {
	if avail[model] {
		return true
	}
	if provider != "" {
		if avail[provider+"/"+model] {
			return true
		}
		if avail[strings.ToLower(provider)+"/"+strings.ToLower(model)] {
			return true
		}
	}
	return avail[strings.ToLower(model)]
}

func providerSuffix(provider string) string {
	if provider == "" {
		return ""
	}
	return " on provider \"" + provider + "\""
}

func anyAvailable(m map[string]bool) bool {
	for _, ok := range m {
		if ok {
			return true
		}
	}
	return false
}

// ── shared helpers ──────────────────────────────────────────────────────────

// draftMCPServers returns the distinct MCP server names the draft calls, from
// both flow nodes and a reasoning agent's tool allowlist. Sorted for
// deterministic messages.
func draftMCPServers(draft Draft) []string {
	seen := map[string]bool{}
	consider := func(tool string) {
		tool = strings.TrimSpace(tool)
		if !strings.HasPrefix(tool, "mcp__") {
			return
		}
		if srv := mcpServerOf(tool); srv != "" {
			seen[srv] = true
		}
	}
	for _, n := range draft.Flow.Nodes {
		consider(n.Tool)
	}
	for _, t := range draft.Tools {
		consider(t)
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func allMCPConnected(connected map[string]bool, servers []string) bool {
	for _, s := range servers {
		if !mcpConnected(connected, s) {
			return false
		}
	}
	return true
}

// nonEmptyStrings trims and drops blanks, for rendering a list in a message.
func nonEmptyStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
