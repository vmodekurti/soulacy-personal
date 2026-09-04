# SOUL.yaml Reference

One `SOUL.yaml` file fully defines an agent — its identity, trigger, model, tools, memory, and behavior — and Soulacy hot-loads it the moment you save.

## Quick Start

```yaml title="agents/assistant/SOUL.yaml"
id: assistant
name: Assistant
description: General-purpose helper

trigger: channel
channels: [http]

llm:
  provider: openai
  model: gpt-4o-mini
  temperature: 0.2

system_prompt: |
  You are concise, helpful, and careful.

max_turns: 6
enabled: true
```

Validate any file before deploying:

```bash
sy agent validate path/to/SOUL.yaml          # human-readable
sy agent validate path/to/SOUL.yaml --json   # machine-readable
```

## Annotated Complete Example

```yaml
# ── Identity ──────────────────────────────────────────────
id: research-writer          # stable runtime ID: URLs, logs, agent__<id> tool names
name: Research Writer        # display name
description: Researches topics and writes a brief.   # shown to peer agents too
version: "1.0.0"
tags: [research, writing]
labels: { owner: ops }

# ── Trigger ───────────────────────────────────────────────
trigger: channel             # primary start mode: channel | cron | oneshot | webhook | internal
channels: [http, telegram]   # channel adapter IDs this agent serves
surfaces: [chat, telegram]   # optional UI/invocation surfaces; empty = derived

# ── Model ─────────────────────────────────────────────────
llm:
  provider: anthropic
  model: claude-sonnet-4-6
  temperature: 0.2
  max_tokens: 2048
  allowed_providers: [anthropic]   # refuse to run on any other provider
  tool_choice: auto                # auto | none | required | <tool name> (turn 1 only)

system_prompt: |
  Produce a short brief. Cite sources when available.

# ── Tools ─────────────────────────────────────────────────
tools:                       # agent-local Python tools — see Agent Tools page
  - name: summarize_csv
    description: Summarize a CSV file and return compact JSON.
    python_file: tools/summarize_csv.py
    timeout: 2m
    parameters:
      type: object
      properties:
        path: { type: string }
      required: [path]

builtins: [web_search, kb_search]  # restrict Go-native built-ins; [] = none; absent = default
knowledge: [company-docs]          # KBs searchable via kb_search
skills: [csv-analysis]             # Agent Skills (or ["*"] for all installed)
agents: [critic]                   # peer agents exposed as agent__critic
mcp_servers: [github]              # MCP allowlist by server
mcp_tools: [mcp__github__search_repositories]   # …or by full tool name
capabilities: [system]             # SEC-3: grant privileged OS tools (shell_exec, write_file, …)
system_tools: false                # legacy alias for capabilities: [system]
env: [GITHUB_TOKEN]                # SEC-5: extra host env var NAMES passed to tool subprocesses
confirm_tools: [write_file, shell_exec]   # pause for approval; ["*"] = all built-ins

# ── Memory ────────────────────────────────────────────────
memory:
  read_scopes: [agent, session]
  write_scopes: [agent]
  max_tokens: 2000

brain_memory:                # long-term memory layers
  episodic:   { enabled: true, max_inject: 5 }
  semantic:   { enabled: true, max_inject: 5 }
  procedural: { enabled: true, auto_update: false }

# ── Reasoning loop ────────────────────────────────────────
reasoning:
  strategy: react            # auto | react | plan_execute | flow | custom name
  max_steps: 6
  step_timeout: 30s
  total_timeout: 3m

# ── Schedule (cron / oneshot triggers) ────────────────────
schedule:
  cron: "0 8 * * *"
  run_missed_on_startup: true
  missed_startup_window: 24h
  output: { channel: telegram, to: "123456789", template: "{reply}" }

notify_on_failure:
  channel: telegram
  to: "123456789"
  include_error: true

security:
  passphrase: "change-me"

# ── Runtime ───────────────────────────────────────────────
max_turns: 10
stream_reply: true
enabled: true
run_timeout: 10m             # whole-run wall-clock cap (default 15m)
```

## Identity

| Field | Type | Notes |
|-------|------|-------|
| `id` | string | Required stable runtime ID. Avoid whitespace and path separators. |
| `name` | string | Human-readable display name. |
| `description` | string | Also shown to other agents when this agent is exposed as a peer tool. |
| `version`, `tags`, `labels` | misc | Optional metadata for packaging and filtering. |

## Trigger and Channels

`trigger` is the agent's primary start mode:

| Trigger | Meaning |
|---------|---------|
| `channel` | Inbound channel traffic (HTTP, Telegram, Slack, Discord, WhatsApp). |
| `cron` | Run on a cron schedule. Requires `schedule.cron`. |
| `oneshot` | Run once at `schedule.at`. |
| `webhook` | Activated by an HTTP POST to the agent's endpoint. |
| `internal` | Callable by peer agents, not normally user-facing. |

`channels` binds the agent to channel adapter IDs. Platform credentials and
inbound routing live in `config.yaml` — the agent only declares which adapters
it serves.

`surfaces` controls where the same agent is visible or invokable. Leave it
empty for Soulacy to derive a conservative default from `trigger` and
`channels`; set it explicitly when one agent should serve multiple interfaces.
This is the preferred way to make a scheduled agent interactive without
duplicating it:

```yaml
trigger: cron
channels: [telegram, slack]
surfaces: [schedule, chat, telegram, slack]
schedule:
  cron: "0 7 * * *"
  output: { channel: telegram, to: "@daily_brief" }
```

With that shape, the scheduler fires the agent, the Chat picker can test it,
and mapped Telegram/Slack bots can route inbound messages to the same agent.
The channel credentials and bot mappings still live under `config.yaml`.

## LLM Block

| Field | Notes |
|-------|-------|
| `provider` / `model` | Model selection. Empty provider falls back to `llm.default_provider` in `config.yaml`. |
| `temperature`, `max_tokens` | Sampling temperature and output token cap. |
| `base_url` | Per-agent override for OpenAI-compatible endpoints. |
| `output_schema` | JSON Schema enforced on the final reply, with one repair re-prompt. |
| `tool_choice` | First-turn tool policy: `auto`, `none`, `required`, or a specific tool name such as `agent__researcher`. Later turns are always `auto`. |
| `allowed_providers` | Guard list; the engine refuses to run on any provider outside it. Recommended for cron agents on local models: `allowed_providers: [ollama]`. |

## Tools, Skills, Knowledge, Peers

These are covered in depth on their own pages:

- [Agent Tools](tools.md) — `tools:` (Python), `builtins:`, `mcp_servers:`/`mcp_tools:`, `system_tools:`, `confirm_tools:`.
- [Skills](skills.md) — `skills:` names, or `["*"]` for all installed.
- [Peer Agents & Built-ins](peers-builtins.md) — `agents:` peer list and built-ins modes.

`knowledge:` lists knowledge base names this agent may search via the built-in
`kb_search` tool. Empty means no KB catalog is injected at all.

## Memory

`memory` controls classic session/cross-session memory injection
(`read_scopes`, `write_scopes`, `max_tokens`).

`brain_memory` controls the long-term layers — see
[Agent Memory & Rulebooks](../using/memory.md):

| Layer | Keys | Effect |
|-------|------|--------|
| `episodic` | `enabled`, `max_inject` | Injects past task records. |
| `semantic` | `enabled`, `max_inject` | Injects knowledge chunks. |
| `procedural` | `enabled`, `auto_update` | Injects the agent's rulebook; `auto_update: true` lets reasoning runs rewrite it (versioned, lockable). |

## Reasoning

The `reasoning:` block switches the agent from a single LLM call to a
multi-step loop — see [Reasoning Strategies](reasoning.md):

| Key | Default | Meaning |
|-----|---------|---------|
| `strategy` | (off) | `auto`, `react`, `plan_execute`, `flow`, or a custom registered name. |
| `max_steps` | 8 | Hard ceiling for ReAct iterations. |
| `max_plan_steps` | 6 | Plan decomposition cap for `plan_execute`. |
| `step_timeout` | 30s | Per-step deadline. |
| `total_timeout` | 180s | Whole-task deadline. |

The reasoning backend is derived automatically from `llm.provider` — there is
nothing extra to configure.

## Workflow

`workflow:` replaces the free-form LLM loop with a checkpointed pipeline. Two
shapes are supported:

- **Linear steps** (`workflow.steps`) — see [Workflow Steps](workflow.md).
- **Cyclic graphs** (`workflow.nodes` / `edges` / `entry` / `max_node_executions`)
  with conditional routing and bounded loops — see [Flow Graphs](flows.md).
  When nodes are declared they take precedence over steps.

## Schedule

Cron and one-shot agents use `schedule`:

```yaml
trigger: cron
schedule:
  cron: "0 8 * * *"
  run_missed_on_startup: true     # catch up after host downtime
  missed_startup_window: 24h      # only replay fires within this window
  output:                         # publish successful runs to a channel
    channel: telegram
    to: "123456789"
    template: "{reply}"
```

When `run_missed_on_startup` is `true`, Soulacy replays at most **one** missed
fire per startup — the latest one inside `missed_startup_window` (default
`24h`) — so a long outage never floods downstream channels.

Use `notify_on_failure` to route errors somewhere a human will see them.
Channel-triggered runs reply with errors automatically; cron and manual runs
fail silently unless this block is set.

## Security and Runtime

`security.passphrase` requires every new session to present the exact string
before the agent answers anything; it is enforced in Go before the LLM runs,
so no prompt injection can bypass it. `security.passphrase_prompt` customizes
the challenge message.

| Field | Notes |
|-------|-------|
| `max_turns` | Cap on LLM turns per run. |
| `stream_reply` | Stream tokens to channels that support it. |
| `enabled` | `false` parks the agent without deleting it. |
| `run_timeout` | Whole-run wall-clock cap (`5m`, `30m`, `1h`; default 15m). Raise it for agents that call slow tools. |

!!! tip
    The GUI agent editor (Agents page) round-trips every field on this page,
    including the tri-state `builtins` modes and per-tool timeouts — you never
    have to hand-edit YAML unless you prefer to.
