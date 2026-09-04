// Package injection is the Cohort F S2 deterministic scanner for
// prompt-injection patterns in untrusted content.
//
// The scanner runs on the body of every wrapped <external_content>
// block (see internal/trust) as it enters the runtime. It records
// findings on the tool.result event so Activity/Studio can surface a
// compact warning, and returns a maximum severity that S3's
// tool-call intent gate consumes when deciding whether to allow /
// prompt / deny an unrelated followup privileged tool call.
//
// The scanner is deliberately BLIND to intent — it flags patterns
// with a strong prior of malicious use (imperative "ignore previous
// instructions", role-override attempts, secret exfiltration prompts,
// hidden HTML/Markdown instructions, obfuscated payloads). False
// positives on benign content are acceptable and expected — the
// design contract is "surface the warning, let harmless summarization
// continue by default, only gate privileged tool calls at S3." This
// keeps the operator informed without blocking real work.
//
// Rules for adding a new pattern:
//   - Prior of malicious use must be strong enough that false-positive
//     surfaced warnings won't drown the operator in noise.
//   - The pattern should be case-insensitive but tight — "ignore" and
//     "instructions" separately are useless; the phrase together plus
//     an imperative frame is a signal.
//   - Severity is one of Info / Low / Medium / High. High requires
//     confirmation before privileged tool calls (per S3); Medium logs
//     a warning; Low/Info surface in traces only.
package injection

import (
	"regexp"
	"strings"
)

// Severity ranks a finding on a coarse scale. High = "block privileged
// followups without operator confirmation" (per S3); Medium = "warn
// prominently in the trace"; Low/Info = surface in the raw trace.
type Severity int

const (
	SeverityNone Severity = iota
	SeverityInfo
	SeverityLow
	SeverityMedium
	SeverityHigh
)

// String renders the severity as a lowercase token suitable for event
// payloads, log fields, and UI labels.
func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityLow:
		return "low"
	case SeverityMedium:
		return "medium"
	case SeverityHigh:
		return "high"
	default:
		return "none"
	}
}

// Family names the class of pattern that fired. Coarser than the
// specific regex so the UI can group findings ("prompt override" fired
// twice from different phrasings looks like one issue, not two).
type Family string

const (
	FamilyOverride     Family = "prompt_override"     // "ignore previous instructions"
	FamilyRoleSwap     Family = "role_swap"           // "act as system", "you are now …"
	FamilySecretExfil  Family = "secret_exfiltration" // "reveal your system prompt / api key"
	FamilyToolIncite   Family = "tool_incitement"     // "call this tool: shell_exec …"
	FamilyHiddenText   Family = "hidden_text"         // HTML comments, zero-width chars
	FamilyObfuscation  Family = "obfuscation"         // base64 blocks, unicode escapes
	FamilyDataExfil    Family = "data_exfiltration"   // "send everything to https://…"
	FamilyChannelAbuse Family = "channel_abuse"       // "post this to slack #general"
)

// Finding is one match of one pattern. The scanner returns zero or more
// per scan; callers aggregate to a MaxSeverity for S3's intent gate.
//
// The Location is optional and best-effort — "html_comment",
// "code_block", "attribute", etc. — so the UI can render a hint
// without re-parsing the content. Empty when the pattern matched
// inline plain text.
type Finding struct {
	Severity Severity `json:"severity"`
	Family   Family   `json:"family"`
	Pattern  string   `json:"pattern"` // short human label for the specific pattern that fired
	Snippet  string   `json:"snippet"` // ≤160 chars of matched context, safe for logs
	Location string   `json:"location,omitempty"`
	// Source is the tool name that produced the scanned content (e.g.
	// fetch_url, kb_search, mcp__foo__bar). Populated by ScanTrusted.
	Source string `json:"source,omitempty"`
}

// Report is what a scan returns: the list of findings + a rollup
// maximum severity for the intent gate + a summary count per family.
type Report struct {
	Findings    []Finding      `json:"findings"`
	MaxSeverity Severity       `json:"max_severity"`
	Counts      map[Family]int `json:"counts,omitempty"`
}

// HasHighSeverity is a convenience for the S3 gate: any single High
// finding flips this to true. Use in preference to reading MaxSeverity
// directly so future additional severities (e.g. "Critical") slot in.
func (r Report) HasHighSeverity() bool {
	return r.MaxSeverity >= SeverityHigh
}

// pattern is one compiled scanner rule. The regex is case-insensitive
// by default (the wrappers in patterns[] set `(?i)` where relevant).
type pattern struct {
	family   Family
	severity Severity
	label    string // short name for the finding's Pattern field
	re       *regexp.Regexp
	location string // static location hint when the regex fires in a specific context
}

// patterns is the deterministic ruleset. Ordered from strongest signal
// to weakest so the trace lists the biggest concerns first. New
// patterns MUST include a comment above them naming a real attack that
// motivated the rule.
var patterns = []pattern{
	// Family: prompt_override — the classic "ignore prior instructions"
	// injection. High severity because it explicitly directs the model
	// to abandon its system prompt / tool policy.
	{
		family: FamilyOverride, severity: SeverityHigh, label: "ignore-previous-instructions",
		re: regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\b[^.\n]{0,40}\b(previous|prior|earlier|above|all)\b[^.\n]{0,40}\b(instructions?|prompts?|rules?|directions?|orders?|commands?)\b`),
	},
	{
		family: FamilyOverride, severity: SeverityMedium, label: "new-instructions",
		re: regexp.MustCompile(`(?i)\b(new|updated|revised)\s+(instructions?|prompts?|directives?|rules?)\s*[:\-—]\s*`),
	},

	// Family: role_swap — "you are now …", "act as system administrator".
	// High severity when the target role names a privileged role or an
	// override framing.
	{
		family: FamilyRoleSwap, severity: SeverityHigh, label: "act-as-privileged",
		re: regexp.MustCompile(`(?i)\b(act|behave|pretend|respond)\s+as\s+(the\s+)?(system|root|admin|administrator|developer|dan)\b`),
	},
	{
		family: FamilyRoleSwap, severity: SeverityMedium, label: "you-are-now",
		re: regexp.MustCompile(`(?i)\byou\s+are\s+(now|from\s+now\s+on)\s+`),
	},

	// Family: secret_exfiltration — direct requests to reveal the
	// system prompt, credentials, environment variables, or agent
	// configuration. High severity because the whole point is data loss.
	{
		family: FamilySecretExfil, severity: SeverityHigh, label: "reveal-system-prompt",
		re: regexp.MustCompile(`(?i)\b(show|reveal|print|dump|output|leak|expose)\b[^.\n]{0,50}\b(system\s+prompt|initial\s+prompt|prompt\s+above|instructions?|api\s+key|api\s+token|secret|password|credential|env(ironment)?\s+var)`),
	},
	{
		family: FamilySecretExfil, severity: SeverityHigh, label: "repeat-verbatim",
		re: regexp.MustCompile(`(?i)\brepeat\b[^.\n]{0,40}\b(above|prompt|instructions?|system|initial|context)\b[^.\n]{0,40}\bverbatim\b`),
	},

	// Family: tool_incitement — the injected content directs the model
	// to run a specific privileged tool. High severity because it lines
	// up 1:1 with the S3 gate list.
	{
		family: FamilyToolIncite, severity: SeverityHigh, label: "call-privileged-tool",
		re: regexp.MustCompile(`(?i)\b(call|invoke|execute|run|use)\s+(the\s+)?(shell_exec|run_script|install_library|write_file|download_file|http_request|channel\.send)\b`),
	},
	{
		family: FamilyToolIncite, severity: SeverityMedium, label: "run-shell",
		re: regexp.MustCompile(`(?i)\brun\s+(this|the\s+following|these)\s+(command|shell|bash|script)s?\b`),
	},

	// Family: data_exfiltration — "post this to <URL>", "send to
	// attacker.com". Medium by default; escalated to High if it
	// couples with a known-privileged tool name.
	{
		family: FamilyDataExfil, severity: SeverityMedium, label: "send-to-external",
		re: regexp.MustCompile(`(?i)\b(send|post|forward|leak|email|dm)\b[^.\n]{0,30}\b(to|at)\b\s+(https?://|@|\w+@\w+\.\w+)`),
	},

	// Family: channel_abuse — "post to slack channel #general", "reply
	// on Telegram to @user". Medium severity because it directs
	// outbound behaviour the user didn't request.
	{
		family: FamilyChannelAbuse, severity: SeverityMedium, label: "post-to-channel",
		re: regexp.MustCompile(`(?i)\b(post|send|reply|forward|dm|@?mention)\b[^.\n]{0,40}\b(slack|discord|telegram|whatsapp|teams|email|channel|group)\b`),
	},

	// Family: hidden_text — HTML comments, zero-width chars, invisible
	// markdown constructs. Medium severity — the attacker went out of
	// their way to hide the payload from a human eyeballing the page.
	{
		family: FamilyHiddenText, severity: SeverityMedium, label: "html-comment",
		re:       regexp.MustCompile(`(?s)<!--(.{5,}?)-->`),
		location: "html_comment",
	},
	{
		family: FamilyHiddenText, severity: SeverityMedium, label: "zero-width-chars",
		re:       regexp.MustCompile(`[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{206F}]{3,}`),
		location: "zero_width",
	},
	{
		family: FamilyHiddenText, severity: SeverityLow, label: "markdown-comment",
		re:       regexp.MustCompile(`(?m)^\[//\]:\s*#\s*\(`),
		location: "markdown_comment",
	},

	// Family: obfuscation — long base64 blocks, unicode-escaped
	// payloads, hex-encoded blobs. Low severity on their own (many
	// legitimate pages have base64 assets); flagged so an operator
	// investigating a High finding sees adjacent suspicious blobs.
	{
		family: FamilyObfuscation, severity: SeverityLow, label: "long-base64-block",
		// 200+ contiguous base64-ish chars is a lot for prose content.
		re:       regexp.MustCompile(`[A-Za-z0-9+/]{200,}={0,2}`),
		location: "base64",
	},
	{
		family: FamilyObfuscation, severity: SeverityLow, label: "unicode-escape-run",
		re:       regexp.MustCompile(`(\\u[0-9a-fA-F]{4}){5,}`),
		location: "unicode_escape",
	},
}

// Scan runs the deterministic ruleset against `body`. Returns a Report
// with zero or more findings; MaxSeverity aggregates for the S3 gate.
// Empty input is safe — returns SeverityNone + no findings.
func Scan(body string) Report {
	if strings.TrimSpace(body) == "" {
		return Report{}
	}
	out := Report{Counts: map[Family]int{}}
	for _, p := range patterns {
		match := p.re.FindStringIndex(body)
		if match == nil {
			continue
		}
		f := Finding{
			Severity: p.severity,
			Family:   p.family,
			Pattern:  p.label,
			Snippet:  snippetAround(body, match[0], match[1]),
			Location: p.location,
		}
		out.Findings = append(out.Findings, f)
		out.Counts[p.family]++
		if p.severity > out.MaxSeverity {
			out.MaxSeverity = p.severity
		}
	}
	return out
}

// ScanTrusted is Scan with a source label pre-populated on every
// finding. Used by the runtime wire so downstream consumers know
// which tool produced the flagged content.
func ScanTrusted(body, source string) Report {
	r := Scan(body)
	if source == "" {
		return r
	}
	for i := range r.Findings {
		r.Findings[i].Source = source
	}
	return r
}

// snippetAround returns up to 160 chars of context around the match,
// with leading/trailing ellipses when the match sits inside a longer
// body. Newlines are collapsed so a single-line log record stays
// readable.
func snippetAround(body string, start, end int) string {
	const (
		windowBefore = 40
		windowAfter  = 80
	)
	lo := start - windowBefore
	if lo < 0 {
		lo = 0
	}
	hi := end + windowAfter
	if hi > len(body) {
		hi = len(body)
	}
	prefix := ""
	if lo > 0 {
		prefix = "…"
	}
	suffix := ""
	if hi < len(body) {
		suffix = "…"
	}
	snippet := body[lo:hi]
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.ReplaceAll(snippet, "\r", " ")
	// Collapse runs of whitespace so the record stays log-friendly.
	snippet = strings.Join(strings.Fields(snippet), " ")
	if len(snippet) > 160 {
		snippet = snippet[:160] + "…"
	}
	return prefix + snippet + suffix
}
