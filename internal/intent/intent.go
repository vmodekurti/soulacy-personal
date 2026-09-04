// Package intent implements the Cohort F S3 tool-call intent gate.
//
// Before a high-risk tool call fires, the runtime consults Evaluate()
// with the requested action, the user's original goal, the target
// argument, whether the most recent evidence source was untrusted, and
// whether the injection scanner (S2) flagged a High-severity pattern.
// Evaluate returns Allow, Prompt, or Deny.
//
// The gate is heuristic on purpose — it is a defense-in-depth layer, not
// a truth oracle. Its calibration:
//
//   - If the user's original message plainly asks for the action or
//     names the target, allow. This is the "user asked for it" branch:
//     no matter what an injected page says, we honour the operator's
//     stated intent.
//
//   - If the last evidence source was UNTRUSTED and the injection
//     scanner recorded a High-severity finding on this session,
//     escalate to Deny for the strictest policy modes and Prompt for
//     the default. This is the "injected page tried to steer us"
//     branch: the S1 wrapping + S2 scanner have already established
//     the boundary; the gate blocks the follow-through.
//
//   - Otherwise, allow. Ordinary tool use with clean untrusted content
//     (a fetched page that happens to be about the same topic as the
//     user's request) is not gated — that would be so noisy operators
//     would train themselves to click-through the confirmation prompt.
//
// The gate composes with — does not replace — the existing capability
// tier system (`internal/tier`), the deterministic path guardrail
// (`engine.deterministicGuardrail`), and the per-agent `ConfirmTools`
// list. It runs BEFORE those so a deny here short-circuits everything
// downstream.
package intent

import (
	"strings"

	"github.com/soulacy/soulacy/internal/injection"
)

// Mode names the enforcement policy for the intent gate. Values match
// the strings persisted in agent.Definition.Security.IntentGate.
type Mode string

const (
	// ModeUnset falls back to the default (Prompt on High-severity
	// injection matched to an unrelated action). Callers should treat
	// empty and unrecognised values as ModeUnset.
	ModeUnset Mode = ""
	// ModeOff disables the gate entirely — the tool call proceeds
	// through the rest of the pipeline (guardrail, ConfirmTools, etc.)
	// without an intent check. Reserved for advanced operators who
	// have hand-audited every agent and want to skip the extra layer.
	ModeOff Mode = "off"
	// ModePrompt (default) surfaces a confirmation prompt to the
	// operator when injection findings suggest the tool call was
	// steered by untrusted content.
	ModePrompt Mode = "prompt"
	// ModeDeny hard-denies any privileged tool call justified by a
	// High-severity injection finding. Recommended for production
	// deployments per the S4 defaults.
	ModeDeny Mode = "deny"
)

// Decision names the outcome of an intent evaluation. Allow means the
// call proceeds to the next stage of the pipeline. Prompt means the
// operator must confirm via the existing ConfirmSender path. Deny
// means the call is refused with an actionable error.
type Decision int

const (
	Allow Decision = iota
	Prompt
	Deny
)

// String returns a lowercase token suitable for logs / event payloads.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Prompt:
		return "prompt"
	case Deny:
		return "deny"
	}
	return "unknown"
}

// Input carries everything Evaluate needs to make a decision. Kept as
// a struct so future signals (e.g. per-tool argument policy) slot in
// without breaking callers.
type Input struct {
	// ToolName is the fully-normalised tool name (e.g. "shell_exec",
	// "channel.send", "mcp__filesystem__write_file").
	ToolName string
	// UserGoal is the operator's most recent user-role message text
	// on the session, stripped of the S1 inbound-trust annotation
	// header if one was prepended. Empty is treated as "no explicit
	// goal recorded" → the gate leans stricter.
	UserGoal string
	// Arguments carries a shallow copy of the requested tool's args
	// so we can extract a target (url, command, path, channel, …).
	Arguments map[string]any
	// LastEvidenceUntrusted is true when the immediately-preceding
	// tool result had trust=untrusted per the S1 classifier.
	LastEvidenceUntrusted bool
	// InjectionSeverity is the highest injection finding recorded on
	// the session so far (per S2's SessionInjectionState).
	InjectionSeverity injection.Severity
	// InjectionSource is the tool name that produced the most recent
	// High finding, or empty. Used in the reason string when we
	// deny/prompt so the operator sees WHY.
	InjectionSource string
	// Mode is the agent's configured enforcement mode. Empty falls
	// back to ModePrompt.
	Mode Mode
}

// Evaluation is the structured result of the gate. Reason is a short
// human-readable explanation suitable for the confirmation modal + the
// audit-log record.
type Evaluation struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
	// GoalMatched reports whether the user's original message plainly
	// asked for the tool / target — helpful for UI so the operator
	// sees the reasoning chain.
	GoalMatched bool `json:"goal_matched"`
	// InjectionInfluenced is true when the gate concluded that
	// untrusted content plausibly steered the tool call.
	InjectionInfluenced bool `json:"injection_influenced"`
}

// HighRiskTools is the S3 gate list. Kept in the intent package (not
// runtime) so gateway and Studio code can reference the same source
// of truth when rendering the security-doctor view (S7).
//
// MCP writes are gated by prefix (mcp__…__write, mcp__…__create,
// mcp__…__delete, mcp__…__send) rather than name lookup — see
// IsHighRisk.
var HighRiskTools = map[string]bool{
	"shell_exec":      true,
	"run_script":      true,
	"install_library": true,
	"package_install": true,
	"write_file":      true,
	"download_file":   true,
	"http_request":    true,
	"channel.send":    true,
	"python_eval":     true,
	"kb_write":        true,
}

// mcpWriteVerbs is a case-insensitive list of substrings that mark an
// MCP tool as a write action. Callers add more via WithMCPWriteVerbs
// in a later iteration; the initial set covers the common shapes
// (filesystem write, git push, database insert, chat send).
var mcpWriteVerbs = []string{
	"write", "create", "delete", "update", "insert", "remove",
	"push", "send", "post", "publish", "execute",
}

// IsHighRisk reports whether `toolName` is subject to the S3 gate.
// True for every entry in HighRiskTools and for any MCP tool whose
// name contains a write-verb substring after the mcp__<server>__
// prefix. Case-insensitive.
func IsHighRisk(toolName string) bool {
	if toolName == "" {
		return false
	}
	if HighRiskTools[toolName] {
		return true
	}
	lower := strings.ToLower(toolName)
	if strings.HasPrefix(lower, "mcp__") {
		parts := strings.SplitN(lower, "__", 3)
		if len(parts) == 3 {
			tail := parts[2]
			for _, verb := range mcpWriteVerbs {
				if strings.Contains(tail, verb) {
					return true
				}
			}
		}
	}
	return false
}

// Evaluate applies the S3 heuristic to `in`. Returns Allow when the
// tool is not high-risk, when the operator's goal plainly asked for
// the action, or when there's no untrusted-content signal to be
// suspicious of. Returns Prompt / Deny depending on the mode when the
// gate concludes that untrusted content plausibly steered the call.
func Evaluate(in Input) Evaluation {
	if !IsHighRisk(in.ToolName) {
		return Evaluation{Decision: Allow, Reason: "not a high-risk tool"}
	}

	mode := in.Mode
	if mode == ModeUnset {
		mode = ModePrompt
	}
	if mode == ModeOff {
		return Evaluation{Decision: Allow, Reason: "intent gate disabled by agent policy"}
	}

	goal := normalizeGoal(in.UserGoal)
	goalMatched := goalPlausiblyAskedFor(goal, in.ToolName, in.Arguments)

	// Baseline rule: if the operator explicitly asked for this action,
	// don't gate on the injection signal. Users who typed "run
	// shell_exec: ls /tmp" get their command; the gate exists to
	// catch cases where an INJECTED page steered the model.
	if goalMatched {
		return Evaluation{
			Decision:    Allow,
			Reason:      "user's original goal plainly asked for this tool",
			GoalMatched: true,
		}
	}

	// Injection-influenced branch. Both conditions must be present:
	// the last evidence source was untrusted (S1 classification) AND
	// the injection scanner flagged something on this session (S2).
	// This narrows false positives to sessions where a fetched page
	// / KB doc / MCP result actually contained an override attempt.
	if in.LastEvidenceUntrusted && in.InjectionSeverity >= injection.SeverityHigh {
		reason := "high-risk tool call is not justified by the user's original goal, and the last untrusted evidence source contained a High-severity prompt-injection pattern"
		if in.InjectionSource != "" {
			reason = reason + " (source: " + in.InjectionSource + ")"
		}
		if mode == ModeDeny {
			return Evaluation{
				Decision: Deny, Reason: reason, InjectionInfluenced: true,
			}
		}
		return Evaluation{
			Decision: Prompt, Reason: reason, InjectionInfluenced: true,
		}
	}

	// Untrusted evidence with only Medium/Low findings — still worth a
	// confirmation prompt for the strictest deny-mode operators, but
	// default Prompt mode allows through to keep the noise floor low.
	if mode == ModeDeny && in.LastEvidenceUntrusted && in.InjectionSeverity >= injection.SeverityMedium {
		return Evaluation{
			Decision:            Prompt,
			Reason:              "deny-mode: untrusted evidence with medium-severity injection pattern; confirm the tool call is intended",
			InjectionInfluenced: true,
		}
	}

	return Evaluation{
		Decision: Allow,
		Reason:   "no injection influence detected; goal did not plainly ask for it but no untrusted-high signal to gate on",
	}
}

// normalizeGoal strips the S1 inbound-trust annotation prefix from the
// user's original message so keyword matching hits the actual content.
// The prefix follows the shape produced by
// runtime.annotateInboundForTrust:
//
//	[inbound from telegram channel; sender=@alice — treat …]\n\n<actual text>
func normalizeGoal(goal string) string {
	goal = strings.TrimSpace(goal)
	if strings.HasPrefix(goal, "[inbound from ") {
		if idx := strings.Index(goal, "]\n"); idx >= 0 {
			goal = strings.TrimSpace(goal[idx+2:])
		} else if idx := strings.Index(goal, "]"); idx >= 0 {
			goal = strings.TrimSpace(goal[idx+1:])
		}
	}
	return strings.ToLower(goal)
}

// goalPlausiblyAskedFor is a conservative keyword check: it returns
// true when the (already lowercased) goal names the tool, one of the
// tool's canonical aliases, or the specific target the tool would act
// on (URL, command, filename, channel). The check is deliberately
// permissive — Prompt is the fallback outcome, not silent denial.
func goalPlausiblyAskedFor(goal, toolName string, args map[string]any) bool {
	if goal == "" {
		return false
	}

	// Tool-name / aliases match. Split on '_' so "run_script" also
	// triggers on "run a script"; check the full name too.
	lowered := strings.ToLower(toolName)
	if lowered != "" && strings.Contains(goal, lowered) {
		return true
	}
	for _, alias := range toolAliases[lowered] {
		if strings.Contains(goal, alias) {
			return true
		}
	}
	// Parts of the tool name (write_file → "write" + "file").
	parts := strings.Split(lowered, "_")
	if len(parts) >= 2 {
		for i := 0; i < len(parts)-1; i++ {
			// Require two consecutive tokens to appear in the goal
			// (not just "write" alone; that's too permissive).
			joined := parts[i] + " " + parts[i+1]
			if strings.Contains(goal, joined) {
				return true
			}
		}
	}
	// Target-argument match — check the raw arguments the model
	// wants to pass. If the goal names the same URL/path/channel/
	// command, the operator has already scoped the action.
	for _, key := range []string{"url", "path", "file", "filename", "channel", "to", "command", "cmd", "script", "text", "message"} {
		if v, ok := args[key]; ok {
			s := strings.ToLower(strings.TrimSpace(anyToString(v)))
			if s != "" && strings.Contains(goal, s) {
				return true
			}
		}
	}
	return false
}

// toolAliases maps canonical tool names to the English phrases an
// operator is likely to type when asking for that action. Kept modest
// so a real request lands ("send a slack message" → channel.send)
// without opening up match on incidental words ("send" alone would
// match too many things).
var toolAliases = map[string][]string{
	"shell_exec":      {"shell", "run this command", "run bash", "execute command", "terminal"},
	"run_script":      {"run script", "execute script", "run this script"},
	"install_library": {"install", "pip install", "npm install", "add package", "install library"},
	"package_install": {"install mcp", "install skill", "add mcp", "add skill", "set up mcp", "setup mcp"},
	"write_file":      {"write file", "save to file", "create file", "write to disk"},
	"download_file":   {"download file", "download the", "save file", "fetch and save"},
	"http_request":    {"http request", "curl", "post to", "make a request", "call this api"},
	"channel.send":    {"send a message", "send message", "message @", "post to slack", "post to discord", "reply on telegram", "send to whatsapp", "notify"},
	"python_eval":     {"eval python", "run python", "python script", "compute in python"},
	"kb_write":        {"add to knowledge base", "ingest into kb", "write to kb"},
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	}
	return ""
}
