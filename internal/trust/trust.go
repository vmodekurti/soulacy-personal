// Package trust classifies tool results as trusted host metadata vs.
// untrusted external content, and wraps untrusted content in an envelope
// the runtime prompt tells the model how to treat.
//
// This is the S1 backbone of Cohort F — Security Hardening. Every tool
// result that could carry attacker-controlled bytes (web fetches, file
// reads, KB lookups, queue reads, channel inbound payloads, MCP tool
// responses) gets wrapped in an <external_content> envelope. The runtime
// system prompt (see engine.go::externalContentGuide) says how to treat
// the wrapped bytes: as evidence, not as instructions.
//
// A tool can explicitly opt out of the envelope by returning a
// TrustedResult — that path is reserved for tool results the framework
// mints itself and knows are safe (e.g. channel.send status, queue put
// acknowledgements, sandbox capability metadata). Anything from the
// network, filesystem, KB, queue, channel, or MCP layer defaults to
// untrusted.
//
// The wrapping is intentionally visible in the transcript so the model,
// the operator, and any downstream classifier (S2's injection scanner,
// S3's intent gate) can see the boundary at a glance. The envelope also
// carries a `source` field so traces can filter by origin.
//
// Trust labels do NOT prevent the model from acting on the content —
// they inform. Denial is S3's job (tool-call intent gate), which
// consults the trust label to decide whether an unrelated followup tool
// call is justified by user intent or by injected bytes.
package trust

import (
	"fmt"
	"regexp"
	"strings"
)

// Level tags the trust classification of a tool result payload.
//
//   - Trusted   — content minted by the framework itself, safe to treat
//     as authoritative (e.g. channel.send delivery status).
//   - Untrusted — content that originated outside the framework and
//     may contain adversarial instructions (web, file, KB, queue,
//     channel inbound, MCP results by default).
//   - Mixed    — a payload that combines trusted framework metadata
//     with untrusted external bytes (e.g. a wrapped MCP result with a
//     framework-added timing header).
//
// The zero value (Unknown) means the classification never ran; callers
// that see Unknown for a security decision must fail closed.
type Level int

const (
	Unknown Level = iota
	Trusted
	Untrusted
	Mixed
)

// String renders the level as a lowercase token suitable for log fields,
// trace metadata, and the `trust=` attribute in the envelope.
func (l Level) String() string {
	switch l {
	case Trusted:
		return "trusted"
	case Untrusted:
		return "untrusted"
	case Mixed:
		return "mixed"
	default:
		return "unknown"
	}
}

// LevelFromString parses a trust level token. Case-insensitive. Unknown
// input returns Unknown so downstream policy can fail closed.
func LevelFromString(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trusted":
		return Trusted
	case "untrusted":
		return Untrusted
	case "mixed":
		return Mixed
	default:
		return Unknown
	}
}

// envelopeOpen is the opening tag for the untrusted-content envelope.
// The XML-ish shape was picked because every LLM we support (Claude,
// GPT, Ollama models, Gemini) handles tag-delimited spans naturally and
// won't mis-attribute the inner text as system-role content.
const (
	envelopeOpen  = "<external_content"
	envelopeClose = "</external_content>"
)

// envelopeRe extracts the envelope attributes + inner body. Kept
// permissive on whitespace + attribute order because the wrapping is
// done by us but the extractor may see model-echoed variants (e.g. the
// model quoting the envelope back at itself in a reply).
var envelopeRe = regexp.MustCompile(`(?s)<external_content\s+([^>]*?)>\s*(.*?)\s*</external_content>`)

// attrRe extracts a single key="value" pair from an envelope's attribute
// list. Values are single-line so no need for cross-line matching here.
var attrRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// Envelope is the decoded shape of an <external_content> block. Source
// tells us which builtin/MCP tool produced the content; Level records
// the classification the wrapping site declared.
type Envelope struct {
	Level  Level
	Source string
	Body   string
}

// Wrap returns a rendered envelope carrying `body` with the given trust
// level and source label. `source` should be a stable identifier — the
// tool name is the canonical choice — so downstream filters can group
// by origin.
//
// Trusted results are returned WITHOUT the envelope by design: the
// envelope exists to warn the model about untrusted bytes; wrapping
// trusted content would waste tokens and dilute the signal. Callers who
// need to explicitly mark a payload as trusted host metadata (e.g. a
// wire-format contract with a downstream scanner) can still call
// WrapExplicit.
func Wrap(level Level, source, body string) string {
	if level != Untrusted && level != Mixed {
		return body
	}
	return WrapExplicit(level, source, body)
}

// WrapExplicit unconditionally renders the envelope, even for trusted
// content. Reserved for the rare case where a downstream consumer (a
// scanner, a Studio preflight report) needs to see the trust label
// declared in-band.
func WrapExplicit(level Level, source, body string) string {
	if source == "" {
		source = "unknown"
	}
	// Extremely defensive: strip a nested opening tag from the body so
	// we can't accidentally build an envelope that closes on the wrong
	// tag when the model echoes our own wrapping back at us.
	body = strings.ReplaceAll(body, envelopeOpen, "<external_content_nested")
	body = strings.ReplaceAll(body, envelopeClose, "</external_content_nested>")
	return fmt.Sprintf("%s trust=%q source=%q>\n%s\n%s",
		envelopeOpen, level.String(), source, body, envelopeClose)
}

// IsWrapped reports whether `content` already contains an envelope. Used
// by callers that concatenate multiple tool results (e.g. the parallel
// executor) to avoid double-wrapping.
func IsWrapped(content string) bool {
	return envelopeRe.MatchString(content)
}

// Extract parses the first envelope found in `content`. Returns
// (envelope, true) on match; returns a zero-value envelope + false when
// no envelope is present. Callers that need to enumerate multiple
// envelopes (a mixed-result summary) should use ExtractAll.
func Extract(content string) (Envelope, bool) {
	m := envelopeRe.FindStringSubmatch(content)
	if len(m) != 3 {
		return Envelope{}, false
	}
	return parseEnvelope(m[1], m[2]), true
}

// ExtractAll returns every envelope in `content`, in document order.
// Empty input or content with no envelopes returns a nil slice — the
// caller can range over it safely.
func ExtractAll(content string) []Envelope {
	matches := envelopeRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]Envelope, 0, len(matches))
	for _, m := range matches {
		if len(m) != 3 {
			continue
		}
		out = append(out, parseEnvelope(m[1], m[2]))
	}
	return out
}

func parseEnvelope(attrsRaw, body string) Envelope {
	env := Envelope{Body: body}
	for _, kv := range attrRe.FindAllStringSubmatch(attrsRaw, -1) {
		if len(kv) != 3 {
			continue
		}
		switch kv[1] {
		case "trust":
			env.Level = LevelFromString(kv[2])
		case "source":
			env.Source = kv[2]
		}
	}
	return env
}

// ToolTrust classifies a builtin/MCP/plugin tool name into its default
// trust level. The classifier is CLOSED — anything not on either the
// trusted-host list or the untrusted-external list defaults to Trusted
// (so a new builtin doesn't accidentally get its results marked
// untrusted, which would then influence S3's intent gate). Builtins
// that fetch external content MUST be listed on untrustedExternal.
//
// The philosophy: "everything from the outside world is untrusted
// unless the framework proves otherwise" applies to CONTENT, not to
// framework STATUS. A `channel.send` status ("delivered to chat_id
// 123") is trusted because the framework minted it. A `fetch_url`
// response body is untrusted because a stranger wrote it.
func ToolTrust(toolName string) Level {
	if toolName == "" {
		return Trusted
	}
	if untrustedExternal[toolName] {
		return Untrusted
	}
	// MCP tools are namespaced mcp__<server>__<tool>. Anything served
	// by an MCP server is by default untrusted — we don't know what
	// the server returns, and its bytes could be attacker-controlled
	// (e.g. an MCP server that fetches a web page and returns the
	// text). Operators can override on a per-tool basis via config in
	// a later story if needed.
	if strings.HasPrefix(toolName, "mcp__") {
		return Untrusted
	}
	// Plugin tools are namespaced plugin__<pluginID>__<tool>. Same
	// argument as MCP: plugin authors are third parties and their
	// output goes into the LLM prompt.
	if strings.HasPrefix(toolName, "plugin__") {
		return Untrusted
	}
	// Peer-agent tool results (agent__<peer-id>) inherit the peer's
	// own trust boundary — the peer's runtime already wrapped any
	// untrusted content it saw. So the reply itself is trusted host
	// metadata; the wrapped inner envelopes propagate through.
	if strings.HasPrefix(toolName, "agent__") {
		return Trusted
	}
	return Trusted
}

// untrustedExternal enumerates the builtins whose payloads originate
// outside the framework and must therefore be wrapped by default.
//
// Rules for adding to this list:
//   - The tool returns bytes that a stranger on the internet, an
//     attacker-uploaded file, or a shared queue writer can influence.
//   - The tool's result is fed back into the LLM prompt.
//
// Rules for NOT adding:
//   - The tool's result is a framework-minted status line (e.g.
//     "delivered", "queued", "installed"). That's trusted.
//   - The tool's result is derived from a signed / integrity-checked
//     source we control (e.g. our own skill catalog). That's trusted.
var untrustedExternal = map[string]bool{
	// Web / network
	"fetch_url":     true, // engine_tools_http.go — fetches arbitrary URLs
	"http_request":  true, // engine_tools_http.go — arbitrary HTTP
	"download_file": true, // engine_tools_http.go — downloads to disk, returns path/preview
	"web_search":    true, // engine.go — search result snippets are user-generated
	// Filesystem
	"read_file":  true, // files can be attacker-uploaded (see channel adapters)
	"list_dir":   true, // directory listings can include attacker-chosen filenames
	"find_files": true,
	// Knowledge base
	"kb_search": true, // KB documents are operator-ingested — often from the web
	// Queue reads (writers may be untrusted peers or channels)
	"queue_take": true,
	"queue_list": true,
	// Channel inbound helpers — outbound (send) is trusted framework status
	"channel.status": false, // status is framework metadata → trusted
	// Skill file reads — skills are operator-authored, but the files
	// themselves can be community-authored and shipped through plugins.
	// Conservative default: untrusted. Operators who ship curated
	// skills can override on a per-tool basis in a later story.
	"read_skill_file": true,
	// Session-history search across peers — content came from prior
	// runs which processed untrusted channel inputs.
	"session_search": true,
}

// SourceRegistry describes a tool's category for the trace annotation.
// The category is coarser than the tool name — the trace UI groups by
// category (`network`, `file`, `kb`, `queue`, `channel`, `mcp`) so an
// operator scanning a session can see at a glance which surface the
// untrusted bytes came from.
func SourceCategory(toolName string) string {
	if toolName == "" {
		return "unknown"
	}
	if strings.HasPrefix(toolName, "mcp__") {
		return "mcp"
	}
	if strings.HasPrefix(toolName, "plugin__") {
		return "plugin"
	}
	if strings.HasPrefix(toolName, "agent__") {
		return "peer"
	}
	switch toolName {
	case "fetch_url", "http_request", "download_file", "web_search":
		return "network"
	case "read_file", "list_dir", "find_files", "write_file":
		return "file"
	case "kb_search", "kb_write":
		return "kb"
	case "queue_take", "queue_list", "queue_put", "queue_create", "queue_clear", "queue_names":
		return "queue"
	case "channel.send", "channel.status":
		return "channel"
	case "read_skill", "read_skill_file":
		return "skill"
	case "session_search":
		return "history"
	case "semantic_memory_search":
		return "memory"
	case "shell_exec", "run_script", "install_library", "package_install", "python_eval":
		return "system"
	}
	return "other"
}
