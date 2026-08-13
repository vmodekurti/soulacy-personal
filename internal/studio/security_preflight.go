// security_preflight.go — S6 (Cohort F) Studio security review that
// runs before Save / Build-until-it-works commits a workflow.
//
// The review complements — does not replace — Preflight (structural /
// setup) and AssessContract (reasoning agent shape). It answers the
// operator's "what will this thing actually be allowed to do?"
// question in one place:
//
//   - Content trust boundaries — which tools this draft calls that
//     produce untrusted external content (web fetches, KB lookups,
//     file reads, MCP results). Directly consumes internal/trust's
//     classifier so the summary matches the runtime's actual behaviour.
//   - Network access — fetch_url, http_request, download_file usage.
//   - File access — read_file, write_file, list_dir, find_files usage.
//   - Channel output — channel.send usage + configured channels list;
//     highlights when a shared-external channel is in play.
//   - Privileged tools — the S3 high-risk gate list; each occurrence is
//     recorded with a note about the intent-gate behaviour.
//   - Confirmation requirements — the agent's ConfirmTools list + the
//     configured intent_gate mode.
//
// The review returns Blockers (Save is refused) and Warnings (Save is
// allowed but the operator should acknowledge). The Recommendations
// list suggests scoped alternatives — kb_write instead of write_file,
// a per-domain http_request allowlist, etc.
//
// This function is pure + deterministic: no I/O, no clock reads. The
// caller wires it through the same handleStudioContract path so the
// preflight can respond to a draft that hasn't been saved yet.
package studio

import (
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/intent"
	"github.com/soulacy/soulacy/internal/trust"
	"github.com/soulacy/soulacy/pkg/agent"
)

// SecurityReview is the structured output of SecurityPreflight. Every
// field is safe to marshal to JSON for the Studio UI + the S7 Doctor.
type SecurityReview struct {
	OK              bool                     `json:"ok"`
	Blockers        []SecurityFinding        `json:"blockers,omitempty"`
	Warnings        []SecurityFinding        `json:"warnings,omitempty"`
	Recommendations []SecurityRecommendation `json:"recommendations,omitempty"`
	Summary         SecuritySummary          `json:"summary"`
}

// SecurityFinding is one issue in the review. Severity is "block" or
// "warn"; Category groups findings for the UI ("trust", "network",
// "file", "channel", "privileged", "confirmation").
type SecurityFinding struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`

	// Action is a machine-readable id for a fix Studio can apply to the draft
	// on the user's behalf, and ActionLabel is the button that does it. Both
	// empty when the fix is not Studio's to make — a config.yaml channel
	// binding, or an operator's acceptance of risk at deploy time.
	//
	// Prose alone was the problem this pair solves. "Set security.intent_gate:
	// deny in the SOUL.yaml" is accurate and still leaves the reader hunting
	// for a field that Studio does not render anywhere.
	//
	// Every id here MUST be handled by the client switch in
	// gui/src/pages/Studio.svelte. That is a hand-maintained seam across two
	// languages, and it has already drifted once — readinessAction shipped
	// handling five actions the server never emitted while six the server did
	// emit fell through to a no-op, so a "Fix this" button did nothing at all.
	// gui/src/lib/studio/securityactions.test.js now fails the build if the two
	// sides disagree.
	Action      string `json:"action,omitempty"`
	ActionLabel string `json:"action_label,omitempty"`
}

// The two fixes below use the shared vocabulary in fixactions.go. They used to
// be declared here, which meant the security panel had its own parallel action
// list — a second seam to keep in step with the client, next to the one that
// had already drifted.
const (
	SecurityFixInternalChannelsOnly = FixInternalChannelsOnly
	SecurityFixIntentGateDeny       = FixIntentGateDeny
)

// SecurityRecommendation is a suggested safer alternative surfaced
// separately from findings so operators can act on them without
// treating them as errors.
type SecurityRecommendation struct {
	From    string `json:"from"`    // tool being used
	Suggest string `json:"suggest"` // safer alternative
	Reason  string `json:"reason"`
}

// SecuritySummary is the one-glance dashboard rendered at the top of
// the Studio preflight modal. Counts by category are cheaper for the
// UI to render than re-scanning the findings list.
type SecuritySummary struct {
	UntrustedContentSources   []string `json:"untrusted_content_sources"`
	NetworkTools              []string `json:"network_tools"`
	FileTools                 []string `json:"file_tools"`
	ChannelTools              []string `json:"channel_tools"`
	PrivilegedTools           []string `json:"privileged_tools"`
	ConfirmTools              []string `json:"confirm_tools"`
	IntentGateMode            string   `json:"intent_gate_mode"`
	RequiresSystemCapability  bool     `json:"requires_system_capability"`
	DeclaresSystemCapability  bool     `json:"declares_system_capability"`
	PrivilegedChannelExposure bool     `json:"privileged_channel_exposure"`
}

// sharedExternalChannel is the same set used by the runtime + the
// gateway security readiness. Kept here (as a lowercase copy) so this
// package has no gateway dependency.
var studioSharedExternalChannels = map[string]bool{
	"telegram": true, "slack": true, "discord": true,
	"whatsapp": true, "whatsapp_web": true,
	"email": true, "teams": true, "google_chat": true,
	"sms": true, "webhook": true,
}

// systemRequiringTools are the tools whose availability implies the
// agent NEEDS the "system" capability declared. If they appear in the
// draft without def.Capabilities including "system", we block save.
var systemRequiringTools = map[string]bool{
	"shell_exec":      true,
	"run_script":      true,
	"install_library": true,
	"write_file":      true,
	"download_file":   true,
	"python_eval":     true,
}

// SecurityPreflight is the S6 entrypoint. `def` is the persisted agent
// Definition when the draft carries an ID (nil for un-saved drafts —
// the review still runs but skips the capability-declaration check).
// workspaceIntentGateDefault is the F-Bridge workspace-scoped fallback
// consulted only when def.Security.IntentGate is empty; empty here means
// the report treats the mode as "prompt (default)" (unchanged pre-Bridge
// behaviour).
func SecurityPreflight(draft Draft, def *agent.Definition, workspaceIntentGateDefault string) SecurityReview {
	rev := SecurityReview{OK: true}
	sum := SecuritySummary{}

	tools := allDraftTools(draft, def)
	channels := allDraftChannels(draft, def)

	// Classify every tool through the trust + intent packages so the
	// review's categories match the runtime's actual behaviour.
	for _, name := range tools {
		if trust.ToolTrust(name) == trust.Untrusted {
			sum.UntrustedContentSources = appendUnique(sum.UntrustedContentSources, name)
		}
		if isNetworkTool(name) {
			sum.NetworkTools = appendUnique(sum.NetworkTools, name)
		}
		if isFileTool(name) {
			sum.FileTools = appendUnique(sum.FileTools, name)
		}
		if isChannelTool(name) {
			sum.ChannelTools = appendUnique(sum.ChannelTools, name)
		}
		if intent.IsHighRisk(name) {
			sum.PrivilegedTools = appendUnique(sum.PrivilegedTools, name)
		}
		if systemRequiringTools[name] {
			sum.RequiresSystemCapability = true
		}
	}

	// Confirmation surface. Prefer the live draft because Studio may be
	// validating before the agent has ever been saved, or after the user edited
	// generated guardrails in the canvas.
	for _, t := range draft.ConfirmTools {
		sum.ConfirmTools = appendUnique(sum.ConfirmTools, strings.TrimSpace(t))
	}
	if draft.Security != nil {
		sum.IntentGateMode = strings.TrimSpace(draft.Security.IntentGate)
	}
	if def != nil {
		for _, t := range def.ConfirmTools {
			sum.ConfirmTools = appendUnique(sum.ConfirmTools, strings.TrimSpace(t))
		}
		if def.Security != nil && sum.IntentGateMode == "" {
			sum.IntentGateMode = strings.TrimSpace(def.Security.IntentGate)
		}
		if def.HasCapability("system") {
			sum.DeclaresSystemCapability = true
		}
	}
	// F-Bridge — fall back to the workspace-scoped default before the
	// display sentinel so a workspace configured for "deny" shows up in the
	// review summary even when the per-agent SOUL.yaml doesn't override.
	if sum.IntentGateMode == "" {
		sum.IntentGateMode = strings.TrimSpace(workspaceIntentGateDefault)
	}
	if sum.IntentGateMode == "" {
		sum.IntentGateMode = "prompt (default)"
	}

	// Channel exposure — privileged tools + a shared channel = risky.
	if len(sum.PrivilegedTools) > 0 {
		for _, ch := range channels {
			if studioSharedExternalChannels[strings.ToLower(strings.TrimSpace(ch))] {
				sum.PrivilegedChannelExposure = true
				break
			}
		}
	}

	sort.Strings(sum.UntrustedContentSources)
	sort.Strings(sum.NetworkTools)
	sort.Strings(sum.FileTools)
	sort.Strings(sum.ChannelTools)
	sort.Strings(sum.PrivilegedTools)
	sort.Strings(sum.ConfirmTools)

	// Blockers.
	if sum.RequiresSystemCapability && !sum.DeclaresSystemCapability {
		rev.Blockers = append(rev.Blockers, SecurityFinding{
			Severity: "block",
			Category: "privileged",
			Message: fmt.Sprintf(
				"Workflow uses %s but the agent does not declare the 'system' capability.",
				strings.Join(intersect(sum.PrivilegedTools, keysOf(systemRequiringTools)), ", "),
			),
			// No Action: capabilities are not a field on the Studio draft, so
			// there is nothing here for Studio to set. Saying where the field
			// actually lives beats offering a button that cannot work.
			Fix: "Open the SOUL.yaml view (the </> tab above the canvas) and add `system` to the agent's top-level `capabilities:` list. The alternative is to drop the privileged tools from the workflow entirely.",
		})
	}
	if sum.PrivilegedChannelExposure {
		// Blocker only when def is present + accept_privileged_exposure
		// isn't set on any binding; otherwise a warn. Because the
		// draft-level review can't see per-binding accept flags, we
		// surface as a warning and let the deployment-readiness check
		// (S4) be the enforcement layer. Save is still gated by the
		// existing capability-audit modal shipped in Cohort A/Story 5.
		rev.Warnings = append(rev.Warnings, SecurityFinding{
			Severity: "warn",
			Category: "channel",
			Message: fmt.Sprintf(
				"This workflow uses privileged tools (%s) AND is exposed on shared channels (%s). Confirm every binding sets accept_privileged_exposure:true after auditing the risk.",
				strings.Join(sum.PrivilegedTools, ", "),
				strings.Join(sharedExposedChannels(channels), ", "),
			),
			Fix:         "Two ways out. Studio can untick the shared channels for you and leave this agent on HTTP only — the button does exactly that. Or accept the exposure per binding on the Delivery page: open the channel, tick \"accept privileged exposure\". That half stays on Delivery on purpose — the operator putting an agent on a public channel is the one accepting the risk, not the person who wrote it.",
			Action:      SecurityFixInternalChannelsOnly,
			ActionLabel: "Use internal channels only",
		})
	}

	// Warnings for untrusted-content flow — heuristic: fetching web
	// content AND then calling a privileged tool in the same graph is
	// the classic injection pipeline. The S3 gate catches it at run
	// time; the preflight warns at authoring time so the operator sees
	// the risk shape before shipping.
	if len(sum.UntrustedContentSources) > 0 && len(sum.PrivilegedTools) > 0 && !strings.EqualFold(strings.TrimSpace(sum.IntentGateMode), "deny") {
		rev.Warnings = append(rev.Warnings, SecurityFinding{
			Severity: "warn",
			Category: "trust",
			Message: fmt.Sprintf(
				"Workflow both ingests untrusted content (%s) and can call privileged tools (%s). The S3 intent gate will confirm/deny injection-steered calls at runtime; consider setting security.intent_gate:deny for stricter enforcement.",
				strings.Join(sum.UntrustedContentSources, ", "),
				strings.Join(sum.PrivilegedTools, ", "),
			),
			Fix:         "The button sets `security.intent_gate: deny` on this draft, so a tool call steered by injected content is refused outright instead of prompting someone to approve it. Worth pairing with the Recommendations below, which swap broad tools for scoped ones.",
			Action:      SecurityFixIntentGateDeny,
			ActionLabel: "Set the intent gate to deny",
		})
	}

	// Recommendations — safer scoped alternatives.
	for _, t := range sum.FileTools {
		if t == "write_file" && !containsStrSlice(tools, "kb_write") {
			rev.Recommendations = append(rev.Recommendations, SecurityRecommendation{
				From:    "write_file",
				Suggest: "kb_write",
				Reason:  "kb_write persists structured artifacts to a scoped knowledge base rather than the filesystem; safer when the goal is 'remember this' rather than 'change a config file'.",
			})
		}
	}
	if containsStrSlice(tools, "shell_exec") {
		rev.Recommendations = append(rev.Recommendations, SecurityRecommendation{
			From:    "shell_exec",
			Suggest: "a scoped Python tool via python_file",
			Reason:  "A dedicated Python tool with a typed argument schema is auditable in the SOUL.yaml and can't be steered into arbitrary commands by an injected page; shell_exec is a broad capability.",
		})
	}
	if containsStrSlice(tools, "http_request") {
		rev.Recommendations = append(rev.Recommendations, SecurityRecommendation{
			From:    "http_request",
			Suggest: "an MCP server for the target service",
			Reason:  "An MCP server exposes a curated, typed surface for the target API; http_request is the raw byte pipe and every remote endpoint is reachable from the same call site.",
		})
	}

	rev.OK = len(rev.Blockers) == 0
	rev.Summary = sum
	return rev
}

// allDraftTools returns every tool name reachable from the draft +
// (when present) the persisted agent. Deduplicated + case-preserved.
func allDraftTools(draft Draft, def *agent.Definition) []string {
	seen := map[string]bool{}
	var out []string
	push := func(vals []string) {
		for _, t := range vals {
			t = strings.TrimSpace(t)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	push(draft.Tools)
	for _, n := range draft.Flow.Nodes {
		if n.Kind != "tool" {
			continue
		}
		t := strings.TrimSpace(n.Tool)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if def != nil && def.Builtins != nil {
		push(*def.Builtins)
	}
	if def != nil && def.MCPTools != nil {
		push(*def.MCPTools)
	}
	return out
}

// allDraftChannels returns every channel id the draft targets or the
// persisted agent binds to. Deduplicated.
func allDraftChannels(draft Draft, def *agent.Definition) []string {
	seen := map[string]bool{}
	var out []string
	push := func(vals []string) {
		for _, v := range vals {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	push(draft.Channels)
	if def != nil {
		push(def.Channels)
	}
	return out
}

func sharedExposedChannels(channels []string) []string {
	var out []string
	for _, c := range channels {
		if studioSharedExternalChannels[strings.ToLower(strings.TrimSpace(c))] {
			out = append(out, c)
		}
	}
	return out
}

func isNetworkTool(name string) bool {
	switch name {
	case "fetch_url", "http_request", "download_file", "web_search":
		return true
	}
	return false
}

func isFileTool(name string) bool {
	switch name {
	case "read_file", "write_file", "list_dir", "find_files":
		return true
	}
	return false
}

func isChannelTool(name string) bool {
	return name == "channel.send" || name == "channel.status"
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

func containsStrSlice(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

var _ = containsStrSlice // used inside SecurityPreflight recommendations

func intersect(a []string, b []string) []string {
	set := map[string]bool{}
	for _, v := range b {
		set[v] = true
	}
	var out []string
	for _, v := range a {
		if set[v] {
			out = append(out, v)
		}
	}
	return out
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// detectPrivilegedRegression compares a pre-repair draft to its
// candidate post-repair form and reports whether the repair introduced
// a privileged tool (S3 high-risk list) or a shared-channel exposure
// that wasn't already present. Used by the S6 buildloop guard so
// "Build until it works" cannot silently escalate an agent's capability
// surface — that decision needs an operator's visible acknowledgement.
//
// Returns (true, reason) when a regression is detected; (false, "")
// when the repair is safe to apply.
func detectPrivilegedRegression(before, after Draft) (bool, string) {
	beforeTools := draftToolSet(before)
	afterTools := draftToolSet(after)
	for name := range afterTools {
		if beforeTools[name] {
			continue
		}
		if intent.IsHighRisk(name) {
			return true, fmt.Sprintf("repair would add privileged tool %q not present in the pre-repair draft; add it deliberately if you want it exposed", name)
		}
	}
	beforeChannels := draftChannelSet(before)
	afterChannels := draftChannelSet(after)
	for name := range afterChannels {
		if beforeChannels[name] {
			continue
		}
		if studioSharedExternalChannels[strings.ToLower(strings.TrimSpace(name))] {
			return true, fmt.Sprintf("repair would add shared-channel exposure %q not present in the pre-repair draft; add it deliberately if you want it exposed", name)
		}
	}
	return false, ""
}

func draftToolSet(d Draft) map[string]bool {
	out := map[string]bool{}
	for _, t := range d.Tools {
		if t = strings.TrimSpace(t); t != "" {
			out[t] = true
		}
	}
	for _, n := range d.Flow.Nodes {
		if n.Kind == "tool" {
			if t := strings.TrimSpace(n.Tool); t != "" {
				out[t] = true
			}
		}
	}
	return out
}

func draftChannelSet(d Draft) map[string]bool {
	out := map[string]bool{}
	for _, c := range d.Channels {
		if c = strings.TrimSpace(c); c != "" {
			out[c] = true
		}
	}
	return out
}
