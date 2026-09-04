package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DefaultSOULRules is the built-in authoring rulebook. It is injected into the
// builder (generate), the AI fixer, and shown in the GUI editor where the user
// can edit it; their saved copy (in the workspace) takes precedence. Tier 1 is
// generic and applies to every agent; Tier 2 collects per-tool I/O contracts the
// catalog can't express (output shapes), which is where the "use the right
// field" knowledge lives.
const DefaultSOULRules = `# SOUL.yaml Authoring Rules

These rules ground how Soulacy generates, validates, and fixes agent workflows.
Add a rule whenever you discover a new failure mode, and add tool I/O contracts
under "Tier 2" so the builder knows each tool's real shape.

## Tier 1 — Generic rules (apply to every agent)

### Macro-Workflow Architecture
- R1. Choose the architecture first, then generate. Use a Macro-Workflow graph for fixed, predictable pipelines; use an 'auto' tool-calling agent for conversational assistants and ordinary runtime tool selection; use 'plan_execute' for long multi-phase jobs. Use 'react' only when the user explicitly asks for ReAct/think-act-observe.
- R2. Visual Macro-Workflows MUST be high-level and simple (usually max 3-5 nodes). Do NOT generate 10-15 node pipelines.
- R3. Instead of creating a separate node for every step of data extraction or cleaning, combine data manipulation, filtering, and JSON parsing into a SINGLE 'python' node.
- R4. Delegate complex summarizing or domain reasoning to an 'agent' node. For example, create a 'Summarizer' or 'Researcher' peer agent and call it in one node.
- R5. Do not use a Macro-Workflow when each run must decide which tools to call, loop over unknown user intent, or recover interactively. Build an 'auto' agent for that case so the runtime tool loop can adapt.

### Tools & Capabilities
- R6. If the workflow needs to parse complex JSON, manipulate lists, format strings, or do math, add a 'python' node. Do NOT try to use template variables like {{ .var }} to do complex data mangling.
- R7. Do NOT invent tool names. If you need a capability that doesn't exist in the catalog, script it in a 'python' node, or use a web_search tool.
- R8. When generating 'new_agents' for the overarching workflow to delegate to, ensure their system prompts are fully self-contained and describe exactly the output format they must return.
- R9. Every tool node input MUST be a JSON object. Never pass raw text or a whole upstream reply directly into a tool node.
- R10. If a tool expects structured arguments and the upstream node is an agent/LLM/free-form output, insert an LLM Extract or Python Transform node, or pass the upstream text through a JSON-safe field using {{ toJson .var }} unquoted.
- R11. Prefer typed ports or JSON-safe {{ toJson .var }} handoffs. Never put a free-form or structured upstream value inside quotes like "content": "{{ .agent_reply }}"; quotes/newlines in the reply will break JSON.
- R12. For "ingest documents/URLs into KB" tasks, build a safe Knowledge Ingestion flow: extract source(s) -> fetch/read content -> classify/tag/summarize -> kb_write -> optional verification search. Store the cleaned artifact record and metadata, not raw HTML dumps, activity traces, or arbitrary host files.
- R13. For temporary workflow state, queues, buffers, or cross-step handoffs, use queue_create/queue_put/queue_take/queue_list instead of write_file. Queue tools are in-memory and do not require system authorization. Use an explicit queue name when the workflow has multiple buffers; otherwise the runtime uses the "default" queue. Use kb_write only for durable searchable knowledge.
- R14. channel.send uses the exact JSON arguments {"channel":"telegram|slack|discord|whatsapp","to":"destination id or chat/thread id","text":"message text"}. The field is text, not message. If the channel has a default outbound destination or the run arrived from an inbound channel, "to" may be omitted; otherwise include it. If delivery routing is uncertain or a send fails, call channel.status once and follow its diagnosis instead of guessing alternate argument names.

### Scheduling & Delivery
- R15. A schedule trigger needs a valid cron (e.g. "0 7 * * *").
- R16. Every channel the agent delivers to must be configured and enabled.
- R17. Clean Channel Delivery: Agents that deliver output to channels (Telegram, Slack, Discord, WhatsApp) MUST output ONLY final clean text. System prompts MUST explicitly instruct the agent NOT to emit internal progress commentary (e.g. "data retrieved, now composing...") or thought headers.
- R18. Execution Strategy Selection: Always select 'auto' for standard tool-calling, conversational, and scheduled digest agents. Select 'plan_execute' for open-ended, multi-stage planning tasks. Do not auto-select 'react'; reserve it for explicit ReAct experiments or a user override.
- R19. Plain Text Formatting: Conversational and chat agents MUST output strictly as plain Markdown text. System prompts MUST instruct the agent NOT to wrap its response in a JSON object or JSON key-value pairs unless output_format is explicitly configured to json.

## Tier 2 — Tool contracts (input args + output shapes)

Document each tool's INPUT arguments and OUTPUT shape so steps reference the
right fields. (Argument schemas for connected MCP tools are also given to the
builder automatically from the live catalog; add OUTPUT shapes here.)

- web_search — input {query, num_results}; output
  {query, result_count, results:[{title, url, content}]}. Read .results.
- fetch_url — input {url, max_bytes}; output is fetched page text. Always pass a JSON object such as {"url":"{{ .url }}"}.
- kb_write — input {kb, content, title, source, mime_type}; requires kb and content. Use it for knowledge ingestion. content may be a string, object, or array; pass structured artifacts unquoted with {"kb":"KB Name","content":{{ toJson .tagged_artifact }},"title":"...","source":"..."}. Do not use write_file for KB ingestion.
- kb_search — input {kb, query, top_k}; output is search results text.
- channel.status — input {channel, to, include_channels}; output {ok, channel, to, route_source, registered, connected, requires_destination, diagnosis:{category,reason,fix}}. Use before channel.send when the route/default destination is uncertain, or after a send failure.
- queue_create — input {queue}; output {ok,queue,created}. Creates a named in-memory queue; queue defaults to "default".
- queue_names — input {}; output {ok,count,queues:[{queue,count}]}. Lists current in-memory queues.
- queue_put — input {queue, item, ttl_seconds}; output {ok,id,queue,expires_at}. Use for temporary handoffs only; item must be valid JSON. queue defaults to "default".
- queue_take — input {queue}; output {ok,item,id,queue,created_at,expires_at} or {ok:false, empty:true}. Removes the oldest item. queue defaults to "default".
- queue_list — input {queue, limit}; output {ok,count,items:[{id,item,created_at,expires_at}]} without removing items. queue defaults to "default".
- queue_clear — input {queue}; output {ok,cleared}. queue defaults to "default".
- (add your tools below, e.g.)
- mcp__notebooklm__notebook_create — input {title}. Do not use {name}. output {notebook_id, title}; the notebook id is
  {{ .<output>.notebook_id }}.
- mcp__notebooklm__source_add — input {notebook_id, source_type, url|text|file_path, wait}. Do not use {content}. For fetched article text use {"notebook_id":"...","source_type":"text","text":"...","wait":true}; for multiple URLs, add sources one at a time unless the live tool schema explicitly supports urls[].
- mcp__notebooklm__studio_create — input {notebook_id, artifact_type}. If it returns pending_confirmation, retry once with {notebook_id, artifact_type, confirm:true}.
- mcp__notebooklm__studio_status — input needs the notebook id (notebook_id),
  not the audio/artifact id. Poll until the status/artifact is completed and an audio_url or final artifact link exists; never treat completed:0, in_progress>0, pending, running, or generating status JSON as a final answer.
`

// RulesVersion returns a short, stable content version for a rulebook.
//
// Rules are a bare file the user can overwrite at any time, so "the agent
// validated cleanly" is an unanchored claim: it does not say WHICH rulebook it
// validated against. That is the failure mode this prevents — an agent
// certified under one rulebook, silently re-read under an edited one, with no
// way to tell that the ground moved. A deployment pins this value so a later
// diff can answer "did the rules change, or did the agent?".
//
// Content-hash based, so it needs no counter, no coordination and no storage:
// identical rules always version identically, on any machine. Whitespace-only
// and line-ending differences are normalised away so a CRLF round trip through
// an editor does not read as a rules change.
//
// An empty rulebook returns "" — "no rules in force" is a real state and must
// not be dressed up as a version.
func RulesVersion(rules string) string {
	normalized := strings.ReplaceAll(rules, "\r\n", "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return "r" + hex.EncodeToString(sum[:])[:12]
}

// RulesPromptBlock wraps the (possibly user-edited) rulebook for injection into
// an LLM prompt. Returns "" when there are no rules so callers can append it
// unconditionally.
func RulesPromptBlock(rules string) string {
	rules = strings.TrimSpace(rules)
	if rules == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nAUTHORING RULES — obey every one of these; they take precedence:\n")
	sb.WriteString(rules)
	sb.WriteString("\n")
	return sb.String()
}
