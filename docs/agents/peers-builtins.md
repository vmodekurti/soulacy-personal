# Peer Agents & Built-ins

Compose specialists into teams: the `agents:` list turns other agents into callable tools, and the `builtins` modes control exactly what else the orchestrator can touch.

## Quick Start: An Orchestrator

```yaml title="agents/coordinator/SOUL.yaml"
id: coordinator
name: Coordinator
description: Routes work to the researcher and the writer.
trigger: channel
channels: [http]

llm:
  provider: anthropic
  model: claude-sonnet-4-6

system_prompt: |
  Delegate research to agent__researcher and composition to
  agent__writer. Synthesize their replies into one answer.

agents: [researcher, writer]   # exposes agent__researcher, agent__writer
builtins: []                   # peers only — no raw built-ins to bypass them

enabled: true
```

## Peer Agents

`agents:` lists the IDs of other agents this agent may invoke. The engine
registers one tool per peer, named `agent__<id>`, whose description comes
from the target agent's own `description` field — so write peer descriptions
for a model audience.

Semantics:

- Each call runs the peer as a **fresh session** — no shared history with the
  caller — using the peer's own model, tools, memory, knowledge, and
  timeouts. The peer's final reply is the tool result.
- `agents: ["*"]` (or `["all"]`) exposes every other loaded agent.
- Self-references are silently skipped.
- Recursion is bounded: chains deeper than 5 peer calls return an error tool
  result the parent can recover from.

!!! tip
    The `description` field is your delegation contract. "Researches current
    topics on the web and returns cited findings" tells the orchestrator
    exactly when to call `agent__researcher`; "helper agent" tells it
    nothing.

## Built-ins Modes

`builtins` decides which Go-native tools (`web_search`, `kb_search`,
`read_skill`, …) the agent sees, with three modes:

| YAML | Mode | Use it for |
|------|------|-----------|
| Field absent | **default** — every built-in whose gate passes is auto-injected. | Standalone agents that should use everything available. |
| `builtins: []` | **none** — no built-ins at all. | Orchestrators. Without this, an agent with `agents: [web-researcher]` *also* sees the raw `web_search` built-in and may bypass the peer. |
| `builtins: [web_search]` | **restricted** — only the named built-ins, still subject to their gates. | Agents that need one or two capabilities and nothing else. |

`["*"]` / `["all"]` are synonyms for the default mode. The GUI editor exposes
the same three modes as a radio: *Default*, *None (peer-only orchestrator)*,
*Restricted*. Note that listing a gated built-in does not bypass its gate —
`kb_search` without a `knowledge:` list is still a no-op.

## Forcing Delegation with `tool_choice`

Local models often skip tool calls and answer from training data. When a
workflow *requires* delegation, pin the first turn:

```yaml
llm:
  provider: ollama
  model: qwen3:32b
  tool_choice: agent__researcher   # turn 1 MUST call this tool
```

Accepted values:

| Value | First-turn behavior |
|-------|---------------------|
| empty / `auto` | Model decides freely. |
| `none` | Model must not call any tool. |
| `required` | Model must call at least one tool. |
| `<tool name>` | Model must call exactly this tool — use the full name, e.g. `agent__researcher`. |

Only the **first** turn is constrained; later turns revert to `auto` so the
model can synthesize the final answer from the tool results.

## When To Build an Orchestrator

Reach for peer agents when:

- **Specialists need different models.** A heavy reasoning model coordinates;
  cheap local models do the legwork — each peer has its own `llm` block.
- **Tool surfaces should be isolated.** The researcher gets `web_search`, the
  publisher gets the Slack MCP tools, and neither sees the other's tools.
- **Prompts conflict.** A skeptical critic and an enthusiastic drafter can't
  share one system prompt; as peers they each keep their own.
- **You want reuse.** The same `researcher` serves the coordinator, a cron
  digest agent, and direct chat.

Skip the orchestra when one agent with a few tools would do — every peer hop
adds a full agent run of latency and tokens.

## A Complete Trio

```yaml
# agents/researcher/SOUL.yaml
id: researcher
description: Searches the web and returns cited findings as bullet points.
trigger: internal              # only reachable as a peer tool
llm: { provider: ollama, model: qwen3:32b }
system_prompt: Research the topic. Return cited bullet points.
builtins: [web_search]
enabled: true
```

```yaml
# agents/writer/SOUL.yaml
id: writer
description: Turns research notes into a polished short brief.
trigger: internal
llm: { provider: anthropic, model: claude-sonnet-4-6 }
system_prompt: Write a tight brief from the supplied notes. Keep citations.
builtins: []
enabled: true
```

The `coordinator` from the quick start ties them together. `trigger:
internal` keeps the specialists out of your channel routing — they exist only
to be called.
