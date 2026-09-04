# Flow Graphs

Flow graphs let you author the agent's path explicitly — conditional routing and bounded loops (refine→judge, retry-until-pass, escalation) declared as nodes and edges under the same `workflow:` key, with crash-safe checkpointing built in.

## Quick Start: Refine → Judge → Ship

A writing pipeline that drafts, gets judged, loops back to refine until the
judge approves (at most 5 times), then ships:

```yaml title="agents/draft-shipper/SOUL.yaml"
id: draft-shipper
name: Draft Shipper
description: Draft, judge, refine until approved, then publish.
trigger: channel
channels: [http]
llm:
  provider: anthropic
  model: claude-sonnet-4-6
system_prompt: Run the drafting flow.

agents: [editor]            # peer used by the ship node

workflow:
  entry: refine                  # default: first node
  max_node_executions: 50        # global safety budget (default 100)
  nodes:
    - id: refine
      tool: improve_draft        # kind inferred: tool
      input: '{"topic": {{.trigger | printf "%q"}}, "feedback": {{.verdict.feedback | printf "%q"}}}'
      output: draft

    - id: judge
      tool: evaluate_draft
      input: '{"draft": {{.draft | printf "%q"}}}'
      output: verdict            # JSON result → fields addressable in predicates

    - id: ship
      agent: editor              # kind=agent → invoked as agent__editor
      input: 'Publish this approved draft: {{.draft}}'

  edges:
    - {from: refine, to: judge, max_iterations: 6}
    - {from: judge, to: refine, if: '{{not .verdict.ok}}', max_iterations: 5}
    - {from: judge, to: ship}    # fallback — order matters
    - {from: ship, to: end}      # "end" (or no edge) terminates

enabled: true
```

The judge's tool returns JSON like `{"ok": false, "feedback": "tighten the
intro"}`; the back edge fires while `ok` is false, and its `max_iterations: 5`
guarantees the loop terminates. When `ok` turns true (or the budget runs
out), the fallback edge ships the draft.

## Nodes

| Field | Notes |
|-------|-------|
| `id` | Unique within the flow; checkpoint keys derive from it. |
| `kind` | `tool` \| `agent` \| `branch`. Usually inferred: `tool:` set → tool, `agent:` set → agent, neither → branch. |
| `tool` | Tool to invoke (any tool the agent can normally call). |
| `agent` | Peer agent to invoke as `agent__<id>` — declare it in the agent's `agents:` list. |
| `input` | Go template over flow vars producing the node's input. |
| `output` | Flow variable that stores this node's result. |
| `on_error` | `abort` (default) \| `skip` \| `retry`. |

`branch` nodes do no work — they exist purely to fan edges out from one
decision point.

## Edges and Predicates

Edges leaving a node are evaluated **in declaration order**; the first edge
whose `if` renders truthy *and* whose traversal budget remains is taken. No
eligible edge ends the flow with the last node's result.

| Field | Notes |
|-------|-------|
| `from` / `to` | Node IDs. `to: end` (or omitted) terminates the flow. |
| `if` | Go template predicate over flow vars. Empty, `true`, or any non-zero output = take the edge; empty string, `false`, or `0` = don't. |
| `max_iterations` | How many times **this edge** may be traversed per run. **Default 1.** |

Flow variables are `.trigger` (the inbound message) plus every node's
`output`. Tool results that are JSON documents are unwrapped so predicates
can address fields (`{{.verdict.ok}}`); plain text stays a string.

## Bounded Cycles

Cycles terminate by construction:

- Every edge defaults to `max_iterations: 1` — a back edge must explicitly
  raise its budget to loop (`max_iterations: 5` above).
- `max_node_executions` (default 100) backstops the entire run; exceeding it
  aborts the flow.

!!! warning
    Remember to raise `max_iterations` on the *forward* edge into a looped
    node too — in the example, `refine → judge` carries `max_iterations: 6`
    because it is traversed once per refinement pass.

## Checkpointing and Resume

Each node visit checkpoints under `<node>#<visit>` in the same store as
[linear workflows](workflow.md). Resuming a crashed run ID restores completed
visits — flow variables included — and recomputes the same deterministic
path, so only unfinished work executes. Side effects are never repeated.

Graphs are validated at load time: duplicate or missing node IDs, unknown
edge endpoints, bad kinds, and unknown entry nodes are refused with precise
errors before the agent ever runs.

## Two Ways to Run a Flow

The same graph serves two entry points:

- **Workflow runs** — cron/scheduled or triggered runs execute the graph
  through the checkpointing workflow executor (the example above).
- **Chat runs** — set `reasoning.strategy: flow` and chat messages route
  through the graph as a reasoning strategy. Node actions go through the
  engine's standard tool-policy bridge (sandbox, allowlists, confirmation
  gates), and each node visit surfaces as a `reasoning.step` event in the
  Chat thinking section and Activity feed — see
  [Reasoning Strategies](reasoning.md).

```yaml
reasoning:
  strategy: flow      # chat messages walk workflow.nodes/edges
```

## Seeing the Graph

The [Flow View](../using/flow-view.md) page renders graph-form agents
read-only: nodes laid out in BFS columns, entry wired from the trigger,
predicates and `↺×N` iteration budgets labeled on edges, cycle back-edges
highlighted, and terminal edges into Output. Editing the graph visually is
planned; for now the YAML is the source of truth.

## When To Use a Flow

- Quality loops: draft → judge → refine until pass.
- Retry-until-success with an escalation path on exhaustion.
- Multi-branch routing where the next step depends on structured output.

Prefer [linear steps](workflow.md) when the pipeline never branches, and a
free-form [reasoning strategy](reasoning.md) when the model should invent the
plan itself.
