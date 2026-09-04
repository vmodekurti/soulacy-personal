// Package securitydoctor is the S7 Cohort F synthesis view: for one
// agent, "what can it do, who can reach it, what happens if untrusted
// content tries to make it do something risky?"
//
// The Doctor consumes the outputs of every earlier Cohort F story:
//
//   - internal/tier.Explain — capability tier + reasons
//   - internal/trust.ToolTrust — per-tool trust boundaries
//   - internal/intent.IsHighRisk — the S3 gate list
//   - internal/injection — pattern scanner (used by DryRun)
//   - internal/policy — declared allow/deny/prompt rules
//
// A single Report struct enumerates: tools + MCP servers + capabilities
// + policies + channel bindings + confirmation gates + environment
// variables + sandbox backend. Risky-combination flags call out
// wildcard MCP + channel exposure, missing domain allowlists, and
// unattended privileged agents.
//
// The DryRun sub-API takes an adversarial content sample and reports
// what the intent gate + injection scanner + policy layer WOULD decide
// without actually executing anything.
package securitydoctor

import (
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/injection"
	"github.com/soulacy/soulacy/internal/intent"
	"github.com/soulacy/soulacy/internal/tier"
	"github.com/soulacy/soulacy/internal/trust"
	"github.com/soulacy/soulacy/pkg/agent"
)

// Report is the full Doctor view for one agent. All fields are safe
// to marshal to JSON for the Doctor UI.
type Report struct {
	AgentID        string         `json:"agent_id"`
	AgentName      string         `json:"agent_name,omitempty"`
	Tier           string         `json:"tier"`
	TierReasons    []string       `json:"tier_reasons,omitempty"`
	Tools          []ToolEntry    `json:"tools"`
	MCPServers     []string       `json:"mcp_servers,omitempty"`
	MCPTools       []string       `json:"mcp_tools,omitempty"`
	Capabilities   []string       `json:"capabilities,omitempty"`
	Channels       []ChannelEntry `json:"channels,omitempty"`
	ConfirmTools   []string       `json:"confirm_tools,omitempty"`
	IntentGateMode string         `json:"intent_gate_mode"`
	PolicyEnabled  bool           `json:"policy_enabled"`
	PolicyRules    []string       `json:"policy_rules,omitempty"`
	EnvVars        []string       `json:"env_vars,omitempty"`
	SandboxBackend string         `json:"sandbox_backend"`
	Unattended     bool           `json:"unattended"`
	PublicDocsURLs []string       `json:"public_docs_urls,omitempty"`
	Findings       []Finding      `json:"findings,omitempty"`
}

// ToolEntry describes one tool on the allowlist + its Doctor
// classifications so the UI can render a compact table.
type ToolEntry struct {
	Name     string `json:"name"`
	Category string `json:"category"` // network/file/kb/queue/channel/mcp/plugin/peer/system/memory/skill/history/other
	Trust    string `json:"trust"`    // trusted / untrusted
	HighRisk bool   `json:"high_risk"`
	Confirm  bool   `json:"confirm"`
}

// ChannelEntry is one channel binding through which this agent can be
// reached. Shared==true means the channel is a shared external
// surface (Telegram/Slack/etc.); Accepted==true means the binding
// declared accept_privileged_exposure:true.
type ChannelEntry struct {
	Name     string `json:"name"`
	Shared   bool   `json:"shared"`
	Accepted bool   `json:"accepted"`
}

// Finding is one risk callout the Doctor flags on the agent. Severity
// is "info"/"warn"/"critical" so the UI can order the list.
type Finding struct {
	Severity string `json:"severity"`
	Category string `json:"category"` // wildcard_mcp / channel_exposure / domain_allowlist / unattended_privileged / missing_confirm / intent_gate
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// Input is the per-agent context the Doctor needs. Loader is the
// runtime.Loader so peer walks resolve. ChannelBindings is a map from
// channel-kind → list of ChannelEntry (built from config.Channels by
// the gateway wrapper).
type Input struct {
	Definition      *agent.Definition
	Loader          tier.Lookup
	ChannelBindings []ChannelEntry
	// SandboxBackend is a string describing the process-sandbox flavour
	// currently active (linux-rlimits / macos-advisory / disabled). The
	// gateway wraps runtime.sandboxLimits to derive it.
	SandboxBackend string
	// WorkspaceIntentGateDefault is the Cohort F-Bridge workspace default
	// for the tool-call intent gate. Consulted when the per-agent
	// def.Security.IntentGate is empty; both empty means the runtime falls
	// back to intent.ModePrompt at evaluation time. Sourced from
	// Config.Security.IntentGate by the gateway wrapper.
	WorkspaceIntentGateDefault string
}

// ResolveIntentGate returns the effective mode string for this input:
// per-agent value wins, workspace default is the fallback, and the
// empty string means the runtime will treat it as ModePrompt.
func (in Input) ResolveIntentGate() string {
	if in.Definition != nil && in.Definition.Security != nil {
		if s := strings.TrimSpace(in.Definition.Security.IntentGate); s != "" {
			return s
		}
	}
	return strings.TrimSpace(in.WorkspaceIntentGateDefault)
}

// Build assembles the full Report for one agent.
func Build(in Input) Report {
	def := in.Definition
	rep := Report{SandboxBackend: in.SandboxBackend}
	if def == nil {
		return rep
	}
	rep.AgentID = def.ID
	rep.AgentName = strings.TrimSpace(def.Name)

	// Tier.
	expl := tier.Explain(def, in.Loader)
	rep.Tier = expl.Tier.String()
	rep.TierReasons = expl.Reasons

	// Tools.
	seenTool := map[string]bool{}
	addTool := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seenTool[name] {
			return
		}
		seenTool[name] = true
		confirm := false
		for _, ct := range def.ConfirmTools {
			if strings.TrimSpace(ct) == name || strings.TrimSpace(ct) == "*" {
				confirm = true
			}
		}
		rep.Tools = append(rep.Tools, ToolEntry{
			Name:     name,
			Category: trust.SourceCategory(name),
			Trust:    trust.ToolTrust(name).String(),
			HighRisk: intent.IsHighRisk(name),
			Confirm:  confirm,
		})
	}
	if def.Builtins != nil {
		for _, t := range *def.Builtins {
			addTool(t)
		}
	}
	if def.MCPTools != nil {
		for _, t := range *def.MCPTools {
			addTool(t)
			rep.MCPTools = append(rep.MCPTools, strings.TrimSpace(t))
		}
	}
	if def.MCPServers != nil {
		for _, s := range *def.MCPServers {
			rep.MCPServers = append(rep.MCPServers, strings.TrimSpace(s))
		}
	}
	rep.Capabilities = append(rep.Capabilities, def.Capabilities...)
	sort.Slice(rep.Tools, func(i, j int) bool { return rep.Tools[i].Name < rep.Tools[j].Name })
	sort.Strings(rep.MCPServers)
	sort.Strings(rep.MCPTools)
	sort.Strings(rep.Capabilities)

	// Channels.
	rep.Channels = append(rep.Channels, in.ChannelBindings...)
	sort.Slice(rep.Channels, func(i, j int) bool { return rep.Channels[i].Name < rep.Channels[j].Name })

	// Confirmation + intent gate.
	for _, ct := range def.ConfirmTools {
		if s := strings.TrimSpace(ct); s != "" {
			rep.ConfirmTools = append(rep.ConfirmTools, s)
		}
	}
	sort.Strings(rep.ConfirmTools)
	// F-Bridge — resolver: per-agent value wins, then the workspace default,
	// then the "prompt (default)" display sentinel. The riskyCombinations
	// check downstream uses HasPrefix("prompt") which still catches both the
	// raw "prompt" resolution and the decorated "prompt (default)" display,
	// so a workspace-default of "deny" correctly stops the "consider deny"
	// finding from firing.
	if raw := in.ResolveIntentGate(); raw != "" {
		rep.IntentGateMode = raw
	} else {
		rep.IntentGateMode = "prompt (default)"
	}

	// Policy. def.Policy is a ToolPolicyConfig with a small closed
	// set of fields (Shell/File/Network + AllowDomains/DenyDomains/
	// DenyPaths) — render each populated field as a line the operator
	// can read at a glance.
	rep.PolicyEnabled = def.Policy.Enabled
	if def.Policy.Enabled {
		if s := strings.TrimSpace(def.Policy.Shell); s != "" {
			rep.PolicyRules = append(rep.PolicyRules, "shell: "+s)
		}
		if s := strings.TrimSpace(def.Policy.File); s != "" {
			rep.PolicyRules = append(rep.PolicyRules, "file: "+s)
		}
		if s := strings.TrimSpace(def.Policy.Network); s != "" {
			rep.PolicyRules = append(rep.PolicyRules, "network: "+s)
		}
		if len(def.Policy.AllowDomains) > 0 {
			rep.PolicyRules = append(rep.PolicyRules, "allow_domains: "+strings.Join(def.Policy.AllowDomains, ", "))
		}
		if len(def.Policy.DenyDomains) > 0 {
			rep.PolicyRules = append(rep.PolicyRules, "deny_domains: "+strings.Join(def.Policy.DenyDomains, ", "))
		}
		if len(def.Policy.DenyPaths) > 0 {
			rep.PolicyRules = append(rep.PolicyRules, "deny_paths: "+strings.Join(def.Policy.DenyPaths, ", "))
		}
	}

	// Env vars.
	rep.EnvVars = append(rep.EnvVars, def.Env...)
	sort.Strings(rep.EnvVars)

	rep.Unattended = def.Unattended

	rep.Findings = riskyCombinations(rep, def)
	return rep
}

// riskyCombinations enumerates the S7-flagged risky combos on `rep`.
// Ordered from most severe (critical) to least (info) so the UI shows
// the biggest issues first.
func riskyCombinations(rep Report, def *agent.Definition) []Finding {
	var out []Finding

	// Wildcard MCP + channel exposure.
	hasWildcardMCP := false
	for _, s := range rep.MCPServers {
		if s == "*" || strings.EqualFold(s, "all") {
			hasWildcardMCP = true
		}
	}
	for _, s := range rep.MCPTools {
		if s == "*" || strings.EqualFold(s, "all") {
			hasWildcardMCP = true
		}
	}
	sharedExposed := false
	for _, ch := range rep.Channels {
		if ch.Shared {
			sharedExposed = true
		}
	}
	if hasWildcardMCP && sharedExposed {
		out = append(out, Finding{
			Severity: "critical",
			Category: "wildcard_mcp",
			Message:  "Wildcard MCP declaration exposes an unbounded tool surface on a shared channel.",
			Fix:      "Replace the wildcard with an explicit list of MCP servers or tools, and restrict the binding to internal channels.",
		})
	}

	// Privileged tier + shared channel without accepted flag.
	if rep.Tier == tier.Privileged.String() {
		for _, ch := range rep.Channels {
			if ch.Shared && !ch.Accepted {
				out = append(out, Finding{
					Severity: "critical",
					Category: "channel_exposure",
					Message:  "Privileged agent is exposed on shared channel " + ch.Name + " without accept_privileged_exposure:true.",
					Fix:      "Set accept_privileged_exposure:true on the binding after auditing, or restrict this agent to internal channels.",
				})
			}
		}
	}

	// http_request without a per-agent domain allowlist. We detect the
	// tool's presence; the runtime's SSRF layer enforces the allowlist,
	// but the Doctor flags the missing config so operators see it.
	for _, t := range rep.Tools {
		if t.Name == "http_request" && !hasDomainAllowlist(def) {
			out = append(out, Finding{
				Severity: "warn",
				Category: "domain_allowlist",
				Message:  "Agent uses http_request without an explicit domain allowlist in policy.",
				Fix:      "Add a policy rule that restricts http_request to a named domain set (see docs/using/policy.md).",
			})
			break
		}
	}

	// Unattended + privileged tools.
	if rep.Unattended && rep.Tier == tier.Privileged.String() {
		out = append(out, Finding{
			Severity: "warn",
			Category: "unattended_privileged",
			Message:  "Privileged agent is marked unattended: guardrail confirmations auto-approve on scheduled runs.",
			Fix:      "Consider tightening intent_gate to 'deny' and adding explicit ConfirmTools for shell/write/install operations.",
		})
	}

	// Missing confirm on privileged tools.
	for _, t := range rep.Tools {
		if !t.HighRisk {
			continue
		}
		if t.Confirm {
			continue
		}
		out = append(out, Finding{
			Severity: "info",
			Category: "missing_confirm",
			Message:  "Privileged tool " + t.Name + " is not listed under confirm_tools.",
			Fix:      "Add " + t.Name + " (or '*') to confirm_tools so the operator explicitly approves every call.",
		})
	}

	// Intent gate mode.
	if strings.HasPrefix(rep.IntentGateMode, "prompt") {
		out = append(out, Finding{
			Severity: "info",
			Category: "intent_gate",
			Message:  "Intent gate mode is prompt (default). Consider 'deny' for production deployments.",
			Fix:      "Set security.intent_gate:deny in the SOUL.yaml.",
		})
	}
	return out
}

func hasDomainAllowlist(def *agent.Definition) bool {
	if def == nil || !def.Policy.Enabled {
		return false
	}
	// A domain allowlist is present when AllowDomains is non-empty OR
	// the network mode is explicitly "deny_all" / "off" (in which case
	// the agent can only reach what's on the allowlist by construction).
	if len(def.Policy.AllowDomains) > 0 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(def.Policy.Network)) {
	case "deny", "deny_all", "off":
		return true
	}
	return false
}

// DryRunInput describes one "what if untrusted content asks the agent
// to run this tool" scenario.
type DryRunInput struct {
	// UserGoal is the operator's most recent (or presumed) user
	// request. Optional — empty is treated as "no explicit goal
	// recorded" which triggers the stricter branch of the intent gate.
	UserGoal string
	// InjectedContent is the untrusted-content sample the operator is
	// simulating (a page body, a KB chunk, an inbound message). The
	// scanner runs on it verbatim.
	InjectedContent string
	// InjectionSource is the tool name to attribute findings to
	// (fetch_url, kb_search, mcp__foo__bar, …). Defaults to
	// "fetch_url" when empty.
	InjectionSource string
	// FollowupTool is the tool name the model would call after seeing
	// the injected content. This is what the intent gate evaluates.
	FollowupTool string
	// FollowupArgs mirrors the tool's arguments so the goal-match
	// heuristic can compare targets (url/path/channel).
	FollowupArgs map[string]any
	// WorkspaceIntentGateDefault is the Cohort F-Bridge fallback used
	// when the per-agent def.Security.IntentGate is empty. When both
	// are empty the simulation defaults to ModePrompt (matching the
	// pre-Bridge behaviour).
	WorkspaceIntentGateDefault string
}

// DryRunResult is the structured "what would happen" answer. The
// operator sees each layer's verdict + reason so the reasoning chain
// is transparent.
type DryRunResult struct {
	InjectionSeverity   string              `json:"injection_severity"`
	InjectionFindings   []injection.Finding `json:"injection_findings,omitempty"`
	IntentDecision      string              `json:"intent_decision"` // allow / prompt / deny
	IntentReason        string              `json:"intent_reason"`
	GoalMatched         bool                `json:"goal_matched"`
	InjectionInfluenced bool                `json:"injection_influenced"`
	// Verdict is the human-readable one-line summary suitable for the
	// dry-run modal ("The tool call would be DENIED …").
	Verdict string `json:"verdict"`
}

// DryRun simulates the S1+S2+S3 pipeline against `in.InjectedContent`
// and `in.FollowupTool` without executing anything. Consumed by the
// Doctor's "what if this page asks the agent to run shell" surface.
func DryRun(def *agent.Definition, in DryRunInput) DryRunResult {
	if in.InjectionSource == "" {
		in.InjectionSource = "fetch_url"
	}
	scan := injection.ScanTrusted(in.InjectedContent, in.InjectionSource)
	// F-Bridge — resolver: per-agent value wins, workspace default is the
	// fallback, both empty falls through to ModePrompt so the pre-Bridge
	// behaviour of "prompt on High-severity injection" is preserved.
	mode := intent.ModePrompt
	raw := ""
	if def != nil && def.Security != nil {
		raw = strings.TrimSpace(def.Security.IntentGate)
	}
	if raw == "" {
		raw = strings.TrimSpace(in.WorkspaceIntentGateDefault)
	}
	if raw != "" {
		mode = intent.Mode(raw)
	}
	ev := intent.Evaluate(intent.Input{
		ToolName:              in.FollowupTool,
		UserGoal:              in.UserGoal,
		Arguments:             in.FollowupArgs,
		LastEvidenceUntrusted: true,
		InjectionSeverity:     scan.MaxSeverity,
		InjectionSource:       in.InjectionSource,
		Mode:                  mode,
	})
	verdict := ""
	switch ev.Decision {
	case intent.Allow:
		verdict = "ALLOW — " + ev.Reason
	case intent.Prompt:
		verdict = "PROMPT — operator would be asked to confirm before the tool ran. " + ev.Reason
	case intent.Deny:
		verdict = "DENY — the runtime would refuse the tool call. " + ev.Reason
	}
	return DryRunResult{
		InjectionSeverity:   scan.MaxSeverity.String(),
		InjectionFindings:   scan.Findings,
		IntentDecision:      ev.Decision.String(),
		IntentReason:        ev.Reason,
		GoalMatched:         ev.GoalMatched,
		InjectionInfluenced: ev.InjectionInfluenced,
		Verdict:             verdict,
	}
}
