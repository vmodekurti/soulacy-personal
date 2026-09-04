# Reasoning Strategies

A `reasoning:` block upgrades an agent from one LLM call to a multi-step think-act-observe loop that plans, calls tools, and reflects before answering.

## Quick Start

```yaml title="SOUL.yaml (excerpt)"
reasoning:
  strategy: react
  max_steps: 6
  step_timeout: 30s
  total_timeout: 3m
```

Agents **without** a `reasoning:` block keep the classic single-call behavior
untouched — the loop is strictly opt-in.

## Configuration

| Key | Default | Meaning |
|-----|---------|---------|
| `strategy` | (off) | `auto`, `react`, `plan_execute`, `flow`, or any custom registered name. |
| `max_steps` | 8 | Hard ceiling on think-act-observe iterations. |
| `max_plan_steps` | 6 | Plan decomposition cap (`plan_execute` only). |
| `step_timeout` | `30s` | Context deadline for each individual step. |
| `total_timeout` | `180s` | Deadline for the whole task. |

The LLM backend is derived automatically from `llm.provider` — Ollama,
Anthropic, OpenAI (and OpenAI-compatible endpoints such as Groq, Together, or
vLLM) are supported, with Ollama as the fallback. There is exactly one place
to configure the model: the agent's `llm` block.

## Choosing a Strategy

| Strategy | How it works | Reach for it when |
|----------|--------------|-------------------|
| `react` | Iterative loop: think → pick a tool → observe → repeat until done, then reflect into a final answer. | Exploratory tasks where each step depends on the last result. |
| `plan_execute` | Decomposes the task into a plan up front (capped by `max_plan_steps`), executes the steps, then reflects. | Tasks with a knowable shape — gather X, compute Y, write Z. |
| `auto` | A keyword heuristic picks `react` or `plan_execute` per task. | You don't want to decide. |
| `flow` | Walks a declarative graph you define under `workflow.nodes`/`edges` — deterministic routing with bounded cycles. See [Flow Graphs](flows.md). | The path should be authored, not improvised. |

Custom strategies registered through the SDK (`sdk/reasoning` +
`registry.RegisterReasoningStrategy`) are selected the same way — put their
registered name in `strategy:`. See
[`docs/REASONING_STRATEGIES.md`](../REASONING_STRATEGIES.md) in the repo for
the author-side contract.

!!! tip
    A typo'd or unregistered strategy name never bricks an agent — the engine
    falls back to ReAct and the run completes with the default loop.

## Tool Access Inside the Loop

The loop sees the agent's **full** tool surface: declared Python tools,
built-ins, MCP tools, plugin tools, and peer agents. Every call is bridged
through the same dispatch path as the classic loop, so the Python sandbox,
audit log, [confirmation gates](tools.md#confirmation-gates), and MCP/plugin
allowlists all apply unchanged. Tool failures become observations the loop
can react to — they never abort the run.

## Watching It Think

Reasoning runs are fully observable:

- **Chat** — the thinking section above the reply expands to show each step's
  thought, the tool it chose, and a preview of the observation.
- **Activity** — the run emits engine events you can follow live or audit
  later:

| Event | Payload |
|-------|---------|
| `reasoning.start` | `strategy`, `max_steps`, tool count |
| `reasoning.step` | per step: `index`, `thought`, `tool`, `observation` (truncated), `duration_ms` |
| `reasoning.result` | `steps`, `confident`, `duration_ms` |

`tool.call` / `tool.result` events stream in real time as each step executes,
exactly like the classic loop.

## Self-Updating Rulebooks

Reasoning interacts with procedural memory. When the agent opts in:

```yaml
brain_memory:
  procedural:
    enabled: true
    auto_update: true
```

the loop's final reflection may propose updated operating rules, and the
engine persists them as a **new immutable version** of the agent's rulebook,
emitting a `rulebook.updated` event. Without `auto_update: true` the proposal
is discarded. Every version is kept with provenance, you can diff and roll
back from the GUI, and a **locked** rulebook refuses all writes (the run
itself still succeeds). See
[Agent Memory & Rulebooks](../using/memory.md).

!!! warning
    `auto_update: true` lets an agent rewrite its own instructions after each
    run. Versioning makes drift visible and reversible, but review the
    history periodically — or lock the rulebook once behavior is where you
    want it.

## Example: A Researcher That Plans

```yaml
id: deep-researcher
name: Deep Researcher
description: Multi-step research with planning and reflection.
trigger: channel
channels: [http]

llm:
  provider: anthropic
  model: claude-sonnet-4-6

system_prompt: |
  Research thoroughly. Cite every claim with a source.

builtins: [web_search]
knowledge: [internal-docs]

reasoning:
  strategy: plan_execute
  max_plan_steps: 5
  max_steps: 10
  step_timeout: 45s
  total_timeout: 5m

brain_memory:
  procedural:
    enabled: true
    auto_update: false   # inject rules, but don't self-modify

enabled: true
```

Ask it a question in Chat and open the thinking section: you'll see the plan
land first, then each step execute with its tool call and observation, then
the reflection that becomes the reply.
