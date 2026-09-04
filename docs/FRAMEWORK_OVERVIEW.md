# Soulacy — Framework Overview

## What it is

Soulacy is a self-hosted, single-binary framework for running agentic LLM workloads. You write declarative agent specs (a `SOUL.yaml` file per agent: system prompt, tools, LLM choice, knowledge bases, peers, memory policy, schedule), drop them in an agent directory, and the gateway loads them, exposes them on whatever channels you've configured (HTTP, Telegram, Slack, Discord, WhatsApp), and runs the agentic loop — LLM call → tool execution → LLM call → final reply — until each agent produces an answer. It bundles its own multi-tenant runtime, channel adapters, an LLM provider router (OpenAI / Anthropic / Gemini / Ollama / any OpenAI-compatible endpoint), an MCP client, a SQLite-backed RAG layer, an Agent-Skills loader, a cron scheduler, a hot-reload file watcher, a Prometheus surface, and a Svelte GUI compiled into the binary. The pitch to a developer evaluating it is: one Go binary, one config file, declarative agents, no orchestration glue, no per-LLM SDK to learn, no separate vector DB process, and the operational surfaces (auth, metrics, action log, structured outputs, peer-agent calls, tool-choice forcing) you'd otherwise build yourself.

## High-level architecture

The gateway is a Fiber HTTP server (`internal/gateway/server.go`) wired up at process boot in `cmd/soulacy/main.go`. That boot sequence is the best map of how the pieces fit:

```
                     ┌─────────────────────────────────────────────────────┐
                     │                  cmd/soulacy/main.go              │
                     │  (loads config, builds every subsystem, ties them   │
                     │   together, runs the worker pool + Fiber listener)  │
                     └─────────────────────────────────────────────────────┘
                          │                          │
        ┌─────────────────┘                          └───────────────────┐
        ▼                                                                ▼
┌──────────────────┐   inbox    ┌───────────────────────┐    Handle()  ┌──────────────────────┐
│  Channel        │◀──────────▶│  channels.Registry    │─────────────▶│   runtime.Engine     │
│  adapters       │             │  (shared chan)        │              │  (agent loop)        │
│  telegram/slack │             └───────────────────────┘              │                      │
│  discord/wa/http│                                                    │  ┌────────────────┐  │
└──────────────────┘                                                   │  │  llm.Router    │──┼─→  OpenAI / Anthropic
        ▲                                                              │  └────────────────┘  │    Gemini / Ollama /
        │ Send()                                                       │  ┌────────────────┐  │    any OpenAI-compat
        └────── reply ─────────────────────────────────────────────────│  │  python tool   │  │
                                                                       │  │  subprocess    │  │
┌──────────────────┐   load/hot-reload                                 │  └────────────────┘  │
│ runtime.Loader   │◀───────── runtime.Watcher (fsnotify) ─────────────│  ┌────────────────┐  │
│  (SOUL.yaml →    │           debounced on *.yaml writes              │  │  mcp.Client    │──┼─→  stdio / http MCP
│   Definition)    │                                                   │  └────────────────┘  │    servers
└──────────────────┘                                                   │  ┌────────────────┐  │
        │                                                              │  │  knowledge.    │  │
        ▼                                                              │  │  Service       │──┼─→  SQLite + sqlite-vec
┌──────────────────┐   triggers                                        │  └────────────────┘  │
│  scheduler       │──────── synthesises message.Message ──────────────▶│  ┌────────────────┐ │
│  (cron + once)   │                                                   │  │ peer agent →   │ │
└──────────────────┘                                                   │  │ engine.Handle  │ │   recurse (depth-limited)
                                                                       │  └────────────────┘  │
                                                                       └──────────────────────┘
                                                                                   │
                                                                                   ▼ EventSink.Emit
                                                                       ┌──────────────────────┐
                                                                       │  gateway.EventHub    │──→ WebSocket /ws/events
                                                                       │                      │
                                                                       │  internal.actionlog  │──→ per-agent JSONL + SQLite
                                                                       └──────────────────────┘
```

The high-level components, with cite points:

The **gateway** (`internal/gateway/`) is the only externally visible surface. It owns the Fiber app, REST routes, CORS, rate limiting, the WebSocket event stream, the embedded Svelte GUI (`internal/webui/dist`, mounted via `go:embed`), and the WhatsApp inbound webhook (`internal/gateway/server.go:255-295`). It does not contain agent logic — it is a thin adapter between HTTP and the engine/loader/scheduler/channels/mcp/knowledge subsystems.

The **agent execution engine** (`internal/runtime/engine.go`) is the heart of the framework. `Engine.Handle` runs the agentic loop for one inbound message: build context, build the tool catalog for this agent, call the LLM, execute any tool calls in parallel, append the results, repeat. It is reentrant — when a peer agent is invoked as a tool, that's just another `Handle` call on a fresh session (`runAgentCall`, engine.go:1324).

The **agent loader** (`internal/runtime/loader.go`) parses `SOUL.yaml` files into `agent.Definition` values and serves them to the rest of the system. `Get` returns a shallow copy so a hot-reload mid-run can't mutate the value the engine is holding.

The **file watcher** (`internal/runtime/watcher.go`) uses `fsnotify` to debounce `.yaml` changes anywhere under the configured `agent_dirs` and trigger `Loader.LoadAll` + scheduler re-registration. It also watches the Python tool directories; on a `.py` change it fires `OnPyChange` to invalidate the gateway's tool-catalog cache so a new tool appears in the GUI without a restart.

The **channel adapters** (`internal/channels/`) each implement a small `Adapter` interface — `Start(ctx, inbox)`, `Send(ctx, msg)`, `Stop`, `Status` — defined in `channels/channel.go`. The four real adapters are:

- `telegram/adapter.go` — long-polls `getUpdates`, sends via `sendMessage`; plain HTTP, no SDK.
- `slack/adapter.go` — Socket Mode (WebSocket from Slack to the gateway, no public URL required).
- `discord/adapter.go` — Discord Gateway WebSocket with reconnection, REST for replies.
- `whatsapp/adapter.go` — Meta Cloud webhook (`GET /channels/whatsapp/webhook` for verification, `POST` for inbound), with HMAC signature verification (the gateway computes `X-Hub-Signature-256` against `app_secret` before dispatching). Outbound goes through the Graph API.

A fifth, `http/adapter.go`, is always on and synchronous: `POST /api/v1/chat` calls `Engine.Handle` directly and returns the reply in the HTTP response. That is what the GUI's chat tester and `sy chat` use.

The **LLM router** (`internal/llm/router.go`) is the provider abstraction. Each provider — `anthropic.go`, `gemini.go`, `ollama.go`, plus an OpenAI provider in `ollama.go:217` that doubles as the adapter for any OpenAI-compatible endpoint (OpenRouter, Together, Groq, vLLM) — implements the `Provider` interface (`Complete`, `Models`). `main.go` registers all configured providers at boot; `router.Complete` dispatches to the named provider or the default. The provider adapters translate Soulacy's neutral `CompletionRequest` (system + history + tools + temperature + tool-choice + optional JSON schema) into each vendor's quirks (Anthropic's `system` top-level field and tool-use content blocks, Gemini's function-call parts, Ollama's OpenAI-compatible `/api/chat`).

The **embedders** (`internal/llm/embed.go`) are deliberately separate from chat providers. `EmbedderRegistry` is a small map of `Embedder` implementations (`OllamaEmbedder` calls `/api/embed`; `OpenAIEmbedder` calls `/v1/embeddings` against any OpenAI-compatible host). Each KB stores the provider+model it was created with, so multi-provider deployments can mix-and-match embeddings per KB.

The **knowledge base / RAG layer** (`internal/knowledge/`) wraps a SQLite database with the `sqlite-vec` extension auto-loaded via cgo. `Open` (`store.go:91`) creates `knowledge_bases`, `documents`, `chunks` tables plus one `vec_<kb_id>` virtual table per KB so different KBs can use different embedding dimensions. `Service` (`service.go`) is the high-level facade the engine talks to; it owns a small bounded LRU (`embedLRU`, 256 entries) that caches the *query* embedding so an agent re-asking the same question doesn't pay the embed round-trip twice.

The **MCP integration** (`internal/mcp/`) is an MCP client (not a server): the gateway connects out to N configured MCP servers — stdio (spawned `npx ...` subprocesses) or HTTP/SSE — runs the MCP handshake (`initialize` + `initialized` notification), caches each server's tool list, and exposes tools under namespaced names like `mcp__filesystem__read_file`. By default, legacy agents that omit MCP allowlists still see every connected MCP tool; agents can restrict exposure with `mcp_servers: [rocketmoney]`, `mcp_tools: [mcp__rocketmoney__get_transactions]`, or disable MCP entirely with `mcp_servers: []`. Tool calls are routed to the right server via `Client.Call` after the same allowlist is enforced in the runtime. The MCP transport implementations live in `mcp/transport.go`.

The **tool/skill catalog** is the union of four things the engine builds per-agent in `allToolSchemas` (`engine.go:1146`):

1. Python tools declared in the agent's own `SOUL.yaml` (`tools:` list).
2. Go-native built-ins — `web_search` (Ollama Web Search API, works with any LLM provider since it only needs `OLLAMA_API_KEY`), `read_skill` / `read_skill_file` (gated on the agent having opted into Skills), `kb_search` (gated on the agent declaring at least one KB).
3. MCP tools admitted by the agent's `mcp_servers` / `mcp_tools` allowlists.
4. Peer-agent tools, one per declared peer, named `agent__<id>`.

The agent's `builtins:` field controls which built-ins it actually sees: absent = default gating, `[]` = none (useful for pure orchestrators that should only delegate), explicit list = only those.

The agent's `mcp_servers:` and `mcp_tools:` fields control which external MCP tools it sees and may call. If both fields are absent, all connected MCP tools are offered for backwards compatibility. If either field is present, MCP is deny-by-default and only matching servers/tools are allowed.

**Agent Skills** (`internal/skills/`, `pkg/skill/skill.go`) follow the agentskills.io spec — each skill is a directory with a `SKILL.md` containing YAML frontmatter and markdown instructions. The loader scans `~/.agents/skills/`, `~/.soulacy/skills/`, and project-level equivalents at boot. Progressive disclosure: only the name+description are in the system prompt by default (cheap catalog), `read_skill` loads the full body on demand, `read_skill_file` reads resource files like `scripts/extract.py`.

The **scheduler** (`internal/scheduler/scheduler.go`) wraps `robfig/cron/v3` with both standard 5-field expressions and optional 6-field (seconds) plus `@daily`-style descriptors. When a cron fires it builds a synthetic `message.Message` and calls `Engine.Handle`, the same path channel messages take. `TryStartRun` prevents overlapping invocations of the same agent.

The **action log** (`internal/actionlog/actionlog.go`) is the durable record of what every agent did. Each event (run start, LLM call, tool call/result, reply, error) is appended to a per-agent JSONL file in `~/.soulacy/logs/` *and* a SQLite table for cross-agent queries. A single buffered writer goroutine drains a 4096-entry queue and fsyncs in batches of up to 256 events / 250ms — the previous implementation held a global mutex across each fsync and was the engine's worst hotspot.

The **WebSocket event stream** (`internal/gateway/events.go`) broadcasts the same events live to any connected GUI. Each client has a buffered send queue; a slow reader has its events dropped rather than blocking the agent loop. Auth gates the upgrade itself (`server.go:237`) so an attacker who learns the WS URL can't read prompts, tool outputs, or memory writes.

The **memory layer** (`internal/memory/`) is a three-tier scheme: a JSONL file store (`memory/store.go`) for recent session history, a SQLite archive (`memory/sqlite.go`) for durable long-term memory, and an optional vector tier (currently stubbed — `MemoryConfig.VectorDB` is empty by default). Memory entries carry a `ProvenanceLabel` so the engine can later decide how much trust to give each piece (confirmed vs inferred vs ephemeral).

The **Conversational Builder** (`internal/runtime/builder.go`, gateway routes `/builder/chat`, `/builder/generate`, `/builder/deploy`) is a small chat-driven assistant that walks the user from "I want an agent that…" to a complete `SOUL.yaml`. It maintains in-memory `BuilderUnderstanding` state per session, asks one question per turn, and emits a structured `understanding` block alongside every reply so the GUI can render progress.

## Request/run lifecycle

A concrete trip through the system, from inbound message to outbound reply:

1. A channel adapter — say, the Telegram poller in `internal/channels/telegram/adapter.go` — receives an update, translates it into a `message.Message` (`pkg/message/types.go`), and posts it onto the shared inbox `chan` owned by `channels.Registry`.

2. One of `runtime.max_concurrent_sessions` worker goroutines spawned in `main.go:347` reads from `chanReg.Inbox()`. Each worker derives a context with `def.ResolvedRunTimeout(...)` (the agent's own `run_timeout`, fallback 5 min) and calls `engine.Handle`.

3. `Engine.Handle` (`engine.go:431`) resolves the agent definition, ensures it's enabled, writes the inbound message to the memory archive, primes the **system-prefix cache** for this session (the rendered system prompt + skill catalog + KB catalog + peer-agent catalog — expensive, deterministic, reused across every turn of this single message), and builds the message slice for the LLM.

4. The **tool schema list** is computed by `allToolSchemas` (engine.go:1146): the agent's Python tool defs, plus gated Go built-ins, plus MCP tools, plus one schema per peer agent.

5. If the agent has `llm.tool_choice: agent__<peer>` and that peer is in its declared peers list, the engine **auto-delegates** before the LLM ever runs (engine.go:492): it calls `runAgentCall` itself and synthesises a fake assistant→tool round-trip in the history so the model sees the peer's reply on its first turn. This exists because local models (notably qwen2.5:72b) regularly ignore the provider's `tool_choice` constraint and answer from training data; bypassing model cooperation is the only reliable fix.

6. The **agentic loop** runs up to `max_turns` (default 10) iterations. Each iteration: send the request via `llmRouter.Complete`; if the response has no tool calls, that's the final answer; otherwise dispatch each tool call (engine.go:974) in its own goroutine. Tool dispatch routes:
   - `mcp__server__tool` → `mcpClient.Call`
   - `agent__peer-id` → `runAgentCall` → recursive `Handle` with depth bookkeeping (cap 5)
   - matches a Go built-in → that built-in's handler runs in-process
   - matches a Python tool def → a bounded `python3 -c` subprocess loads the file, calls the named function with JSON args via stdin, captures stdout
   An **anti-loop guard** memoises `(name, args)` per run: if the model re-issues an identical call, the engine returns the cached result with a "don't call this again" nudge instead of re-running it. If *every* tool call in a turn is a duplicate, the loop short-circuits to the final-synthesis path.

7. **Final synthesis fallback** (engine.go:736): if the model burned all its turns without producing plain text (common with smaller local models that keep emitting tool calls), the engine forces one more LLM call with the tools omitted, instructing the model to write the final answer from what's already in context.

8. **Structured output enforcement**: if the agent's `LLMConfig.OutputSchema` is set, the engine validates the final reply parses as JSON (`parseJSONLoose` strips code fences first). On failure it runs `finalSynthesisStructured` once with the provider's native JSON-mode (OpenAI `response_format`, Gemini `responseSchema`, Anthropic forced tool-use, Ollama `format:`).

9. The reply is persisted to memory, wrapped as `message.Message{Role: assistant}`, and emitted as a `message.out` event into the EventHub (which broadcasts it on the WebSocket and persists it to the action log).

10. Back in the worker goroutine, `chanReg.Send(reply)` looks up the adapter by `msg.Channel` and dispatches — e.g. Telegram's `Send` POSTs to `sendMessage`.

The HTTP path is the same except step 1 is `POST /api/v1/chat` and step 10 is the HTTP response body — the call is synchronous (the handler calls `engine.Handle` directly).

## The agent definition model

Every agent is one `SOUL.yaml` file under a directory in `agent_dirs`. The on-disk convention is `<agent_dir>/<id>/SOUL.yaml` — folders are migrated from the legacy flat-file layout on the next write (`loader.go:134`). The schema is `agent.Definition` in `pkg/agent/types.go`. Key fields:

The **identity** block — `id`, `name`, `description`, `version`, `tags`, `labels` — is uncontroversial; `description` doubles as the LLM-visible tooltip when this agent is invoked as a peer.

The **trigger** block determines activation. `trigger: channel` plus a `channels:` list (`[http, telegram, ...]`) routes inbound messages from those channels to this agent. `trigger: cron` plus a `schedule:` block (`cron: "0 7 * * *"`) puts this agent on the scheduler. `trigger: oneshot` plus `schedule.at: <ISO time>` runs once. `internal` is the trigger you see in peer agents (`kb-researcher`'s `channels: [http, internal]`) so the engine can invoke them via `runAgentCall` even though they aren't bound to a user-facing channel.

The **intelligence** block — `system_prompt` and the `llm:` sub-block — picks the provider and model and parameters. Useful sub-fields:

- `llm.provider` / `llm.model` — names from the LLM router. Empty `provider` falls back to `llm.default_provider`.
- `llm.tool_choice` — `""` / `"auto"` / `"none"` / `"required"` / `"<tool_name>"`. Applied only on turn 1; turn 2+ is always `auto` so the model can synthesise.
- `llm.output_schema` — JSON Schema; if set, the engine enforces the final reply parses + retries once.

The **tools** list contains `ToolDef` entries with `name`, `description`, `parameters` (JSON Schema), and either `python_file: "~/tools/foo.py"` or `inline: "..."`. Optional `timeout: "30m"` overrides `runtime.tool_timeout` for that tool.

The **skills** field is a list of skill names (`["pdf", "xlsx"]` or `["*"]` for all). Empty list = no skill catalog and no `read_skill` injection — small agents stay focused.

The **knowledge** field is a list of KB names. Empty list = no KB catalog and no `kb_search` tool. Each name must match a KB in the knowledge store; mismatches are logged but not fatal (the KB may be created later via the API).

The **agents** field is the peer list — IDs of other agents this one may invoke as `agent__<id>` tools. `["*"]` exposes every other loaded agent. Self-references are silently dropped; cycles are bounded by `runtime.max_agent_call_depth` (default 5). Set `parallel_peer_calls: true` on an orchestrator when it should fan out two or more peer-agent calls emitted in the same LLM turn; peer results are still delivered back to the model in the original tool-call order. Set `structured_peer_results: true` when a coordinator should receive each peer reply as a `SOULACY_AGENT_RESULT` JSON envelope with `target_agent`, `ok`, `content`, and parsed `structured` data when the peer returned JSON.

The **builtins** field is a pointer-to-slice (`*[]string`) so the YAML round-trip can distinguish absent from `[]`:

- field absent → default gating applies (cheap built-ins like `web_search` always offered, gated ones offered if their condition is met).
- `builtins: []` → no built-ins. Useful for orchestrators that should only delegate to peers.
- `builtins: [name, ...]` → only those names.

The **memory** block is `MemoryPolicy{ ReadScopes, WriteScopes, MaxTokens }` — what session/cross-session memory this agent may read or write, and a token budget on injected memory.

The **runtime** controls — `max_turns`, `stream_reply`, `enabled`, `run_timeout` ("15m" syntax) — bound this agent's resource use. `run_timeout` is the wall-clock cap on the entire run (across all LLM calls and tool subprocesses); the worker pool in `main.go` uses `def.ResolvedRunTimeout` to set the context deadline.

The **hooks** field lists `ContextHook{Event, PythonFile, Function}` entries for lifecycle callbacks (e.g. mutate the context before the LLM is called).

## Knowledge / RAG pipeline

Documents enter the store via `POST /api/v1/knowledge/:kb/documents` (`internal/gateway/knowledge.go`). The upload can be a JSON body with raw text or a multipart upload; in both cases the mime type is inferred from filename or passed explicitly, and `knowledge.ExtractText` (`ingest.go:31`) does the conversion. Supported types are text/markdown directly, PDF via `ledongthuc/pdf` with a custom `cleanPDFText` that fixes the one-word-per-line fragmentation the underlying library produces, and DOCX via in-memory zip + XML walk over `word/document.xml`. Unknown mime types are best-effort treated as text.

Extracted text goes through `ChunkText`, a character-based chunker (default 1000 chars, 200-char overlap — configurable per KB at creation time). The chunker operates on rune slices so multi-byte characters aren't split.

Each chunk is embedded by the KB's configured embedder — looked up by `kb.EmbeddingProvider` against the `EmbedderRegistry` (`embed.go:255`). The KB row records both the provider id and the model, so a KB created with `nomic-embed-text` (768 dims) coexists fine with another KB on `text-embedding-3-small` (1536 dims) — each gets its own `vec_<kb_id>` virtual table sized to its dimensions. Chunks land in the `chunks` table; their embeddings land in the per-KB vec0 table.

At query time, `kb_search` is a built-in tool the engine offers any agent whose `knowledge:` field is non-empty (`engine.buildKBSearchBuiltin`, engine.go:313). The handler:

1. Resolves the KB by name (`Store.GetKB`).
2. Looks up the right embedder by `kb.EmbeddingProvider`.
3. Checks the bounded LRU (`embedLRU`, 256 entries, keyed by `provider|model|query`) — on a hit, skips the embed call.
4. Otherwise embeds the query, caches the vector, and runs a vec0 KNN against `vec_<kb_id>`.
5. Joins the hit rows back to `chunks` + `documents` to recover content, title, and source filename.
6. Formats the top-K results as a compact `<kb_results>` XML block (`service.go:153`), truncating any one chunk over 1500 chars. The format is deliberately easy for the LLM to parse and cite from.

The result is returned as a tool result; agents like `examples/agents/kb-researcher` then synthesise a bulleted brief with `(filename.pdf)` citations from those tags.

## Peer agents and the message router

The router has two halves. The first is the shared inbox owned by `channels.Registry` — every non-HTTP adapter posts messages onto the same `chan` and the worker pool in `main.go:347` drains it. Workers are bounded (`runtime.max_concurrent_sessions`, default 100), so a Telegram spammer can't OOM the process or fork a thousand Python subprocesses.

The second is **peer-agent calls inside the engine**. When the LLM emits a tool call whose name starts with `agent__`, `runTool` routes it to `runAgentCall` (engine.go:1324). That handler:

1. Verifies the target is in the caller's declared peer list (the model isn't trusted to fabricate `agent__some-other-id`).
2. Checks the call-depth limit (`agentCallDepth` is a `context.Value` bumped on every recursion; cap 5 catches A→B→A loops).
3. Resolves the target via `loader.Get` and rejects disabled agents.
4. Recurses into `engine.Handle` with a fresh session ID (so the peer has no shared history with the caller, which forces self-contained instructions) and `channel: "internal"`.
5. Returns the peer's flattened reply text as the tool result.

The peer runs its *own* agent loop with its own model, tools, KBs, peers, and `run_timeout`. To the caller, it's a tool. To the peer, it's a normal channel message. The pattern composes — a "writer" can delegate to a "researcher" who delegates to a "kb-searcher" — bounded by depth and by each agent's individual `run_timeout`. The `examples/agents/writer` agent is the reference for this style.

For coordinator-style agents, `parallel_peer_calls: true` makes independent peer calls in the same model turn run concurrently under the same nested-chain deadline. Normal built-in/Python/MCP tools remain sequential; the parallel path is intentionally limited to peer agents because they have isolated sessions and deterministic result ordering. If the coordinator also sets `structured_peer_results: true`, each peer response is wrapped before being returned as a tool result, which lets the coordinator inspect citations, confidence, or other JSON fields without requiring a breaking change to the SDK `ToolResult` shape.

Auto-delegation (engine.go:492) is the escape hatch when the model can't be relied on to actually pick the tool: `llm.tool_choice: agent__researcher` makes the engine itself run the peer before the LLM's first turn and stuff the result into the history as if the model had asked for it.

## HTTP API surface

All API routes live under `/api/v1` and are gated by the `Authorization: Bearer <key>` middleware when `server.api_key` is set (constant-time, length-hidden comparison via SHA-256 in `secretEqual`, `server.go:449`). The unauthenticated routes are `/ping` (`{"auth": "open"|"required", "status": "ok"}` — deliberately leaks no key material), `/ws/events` upgrade check (which performs its own auth via header or `?api_key=` query), `/channels/whatsapp/webhook` (Meta auth is HMAC-on-body, not API key), and the static GUI files.

The main route groups:

- **Agents CRUD** — `GET /agents`, `GET/POST/PUT/DELETE /agents/:id`, `POST /agents/:id/enable|disable|clone|trigger`, `GET /agents/:id/actions`.
- **Chat (synchronous HTTP channel)** — `POST /chat` runs `Engine.Handle` and returns the reply.
- **Channels** — `GET /channels`, `PATCH /channels/:id`, `POST /channels/:id/enable|disable`. The `channelSpecs` catalog (`internal/gateway/api.go:318`) tells the GUI which fields each channel needs.
- **Schedule** — `GET /schedule`, `GET /schedule/status`.
- **Memory** — `GET /memory/:agent_id`, `DELETE /memory/:agent_id/:session_id`.
- **Providers** — `GET /providers`, `GET /providers/:id/models`, `POST /providers/:id` (credentials), `POST /providers/:id/model` (default model).
- **Skills** — `GET /skills`, `GET /skills/:name`.
- **MCP** — `GET /mcp`, `POST /mcp`, `PATCH /mcp/:id`, `DELETE /mcp/:id`, `POST /mcp/test`.
- **Knowledge** — `GET/POST /knowledge`, `DELETE /knowledge/:kb`, `GET /knowledge/:kb/documents`, `POST /knowledge/:kb/documents`, `DELETE /knowledge/:kb/documents/:doc`, `POST /knowledge/:kb/search`.
- **Tool catalog** — `GET /tool-catalog` returns the union of Python tools, MCP tools, and built-ins, cached with TTL invalidation tied to the file watcher's `OnPyChange` hook.
- **Builder** — `POST /builder/chat`, `POST /builder/generate`, `POST /builder/deploy`, `DELETE /builder/session/:id`.
- **Config + Logs + Health + Metrics** — `GET/PATCH /config`, `GET /logs`, `GET /health`, `GET /metrics` (Prometheus, gated by the same auth as the rest of the API).

The WebSocket `GET /ws/events` streams `message.in`, `message.out`, `tool.call`, `tool.result`, `llm.call`, `llm.result`, `error`, `warn` events to any authenticated client.

## Configuration

Config is loaded by `internal/config/config.go` using Viper. Search order: an explicit path via `SOULACY_CONFIG_PATH` or `cfgPath` argument, then `./config.yaml`, then `~/.soulacy/config.yaml`. Environment variables prefixed with `SOULACY_` override every field — dots become underscores (`server.api_key` → `SOULACY_SERVER_API_KEY`).

The top-level structure mirrors `Config` in `config.go`:

- `server` — `host` (default `127.0.0.1` — the gateway refuses to start on a non-loopback host with an empty `api_key`, see `main.go:65`), `port` (18789), `gui_enabled`, `api_key`, `tls_cert`/`tls_key`, `allowed_origins`.
- `runtime` — `max_concurrent_sessions` (100), `default_max_turns` (20), `python_bin` (`python3`), `tool_timeout` (`120s`).
- `memory` — `dir`, `sqlite_path`, `vector_db` (empty by default — vector tier disabled), `max_history`.
- `llm` — `default_provider` (`ollama`) and a `providers` map. Standard ids are `ollama`, `openai`, `anthropic`, `google`; any other id with a `base_url` and `api_key` is registered as a generic OpenAI-compatible provider so you can add `openrouter`, `together`, `groq`, or `vllm` purely via config.
- `channels` — a free-form map keyed by adapter id; each adapter knows which keys it expects (`token`, `bot_token`/`app_token`, `phone_number_id`/`access_token`/`verify_token`/`app_secret`, etc.).
- `mcp.servers` — keyed by server id, each entry is `transport: stdio|http` + the relevant `command`/`args`/`env` or `url`/`headers`.
- `agent_dirs`, `plugin_dirs`, `skill_dirs` — lists of directories to scan.
- `knowledge` — `db_path` (empty → RAG disabled silently), `embedding_provider`/`embedding_model`, `chunk_size`/`chunk_overlap`.
- `log` — `level`, `format` (`console`/`json`), `file`.

`EnsureDirs` creates everything that's missing at startup. There is no migration system — schemas are `CREATE TABLE IF NOT EXISTS` and additive.

## Extension points

**Adding a new channel.** Implement `channels.Adapter` (`channel.go:19`) — `ID`, `Name`, `Start(ctx, inbox)`, `Send(ctx, msg)`, `Stop`, `Status`. `Start` must be non-blocking; spawn a goroutine that translates platform events to `message.Message` values and pushes them onto the provided inbox channel. Wire the constructor into `main.go`'s channel section (the existing `if tgCfg, ok := chanCfg["telegram"]; ok` block is the template). If the channel is webhook-driven (like WhatsApp), also add the route in `gateway/server.go` — for WhatsApp this is the verify-handshake `GET` and HMAC-validated `POST`.

**Adding a new LLM provider.** Implement `llm.Provider` (`router.go:75`) — `ID`, `Complete`, `Models`. The hard part is translating the neutral `CompletionRequest` (with tool schemas, tool-choice, optional JSON schema) into the vendor's call shape and decoding tool calls back. Look at `anthropic.go` or `gemini.go` for the non-OpenAI patterns; `ollama.go:217` for an OpenAI-compatible adapter. Register the provider in `main.go` next to the existing `llmRouter.Register(...)` calls. To add an OpenAI-compatible endpoint *without* code changes, just put it in `config.yaml` with `base_url` and `api_key` under a custom id — the generic loop at `main.go:127` picks it up.

**Adding a new tool to an agent.** Drop a Python file with a function whose name matches the tool, then reference it from `SOUL.yaml`:

```yaml
tools:
  - name: get_weather
    description: Look up current weather for a city.
    python_file: ~/tools/weather.py
    parameters:
      type: object
      properties:
        city: { type: string }
      required: [city]
```

The file watcher picks up edits without a restart; the gateway's tool-catalog cache is invalidated on `.py` changes too. For an in-process Go built-in, append a `BuiltinTool` to `engine.buildBuiltins` (`engine.go:171`) with a `Name`, `Description`, JSON Schema `Parameters`, optional `Gate`, and a `Handler func(ctx, args) (string, error)`.

**Adding a new MCP server.** Either edit `config.yaml`:

```yaml
mcp:
  servers:
    github:
      transport: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: ghp_...
```

…or `POST /api/v1/mcp` at runtime — the gateway connects on the fly and caches the server's tools. Legacy agents see them as `mcp__github__*`; restricted agents must list the server or individual tools in `mcp_servers:` / `mcp_tools:`. HTTP transports work too (set `transport: http` and `url`).

**Adding a new Skill.** Create a directory with a `SKILL.md` at any of the scan paths (`~/.agents/skills/<name>/SKILL.md`, `~/.soulacy/skills/<name>/SKILL.md`, project-level equivalents, or an extra dir from `skill_dirs`). Frontmatter at minimum `name:` and `description:`; rest is markdown. Optional `scripts/`, `references/`, `assets/` subdirs are loadable via `read_skill_file`.

## The Mac client

There's a separate `soulacy-mac` project (not in this repo — the GUI here is a Svelte web app embedded into the gateway binary at build time). The Mac client is a native macOS app that talks to a running gateway over its REST API and `/ws/events` WebSocket, using the same `Authorization: Bearer` auth as `sy` and the web GUI. From the gateway's perspective it's just another API client; nothing in the Go backend is aware of it specifically.

## Pointers

If you're poking around the code, the most useful entry points in priority order are `cmd/soulacy/main.go` (wiring), `internal/runtime/engine.go` (agent loop), `pkg/agent/types.go` (definition schema), `internal/gateway/server.go` (HTTP surface), `internal/channels/channel.go` (adapter contract), `internal/llm/router.go` (provider contract), `internal/knowledge/service.go` (RAG facade), and `internal/mcp/mcp.go` (MCP client). The `examples/agents/` directory has real agents at varying complexity — `hello-world` is the trivial case, `writer` + `critic` + `kb-researcher` + `web-researcher` is the multi-agent orchestration pattern.
