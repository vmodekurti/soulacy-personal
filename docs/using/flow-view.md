# Flow view

The Flow page draws every agent as a visual graph — classic agents get an editable anatomy view (trigger → prompt/memory → LLM → tools → output), and graph-form workflow agents get a faithful read-only render of their declared nodes and edges.

## Quick start

1. Open **⌘ Flow** in the GUI.
2. Pick an agent in the left rail (each entry shows its trigger, provider, and tool count).
3. Click any node — an inspector opens on the right. For classic agents you can edit there and click **Save Flow**.

## Classic agents: the anatomy view

A classic agent (no `workflow.nodes`) renders as its functional anatomy:

| Node | Shows | Inspector lets you edit |
|---|---|---|
| **Trigger** | `⏱ Cron — 0 8 * * *` or `📡 Channel — http, telegram` | Trigger type, cron expression, channels |
| **📝 System Prompt** | First ~80 chars | The full prompt in a textarea |
| **🧠 Memory** | Read scopes | Read/write scopes, max tokens |
| **🤖 LLM** (center) | Provider · model · temperature | Provider, model, temperature, max tokens, max turns, tool choice |
| **⚙ Tools** (one node each) | Tool name + script filename | Name, description, Python file; parameters shown read-only |
| **📤 Output** | Channels, or the schedule's output bot for cron agents | Channels |

Animated green edges mark the LLM↔tool wiring; a dashed "⚙ No tools wired" placeholder appears for tool-less agents.

Editing here uses the same agent-update API as the Agents page — **Save Flow** persists to the agent's SOUL.yaml and re-renders the graph. The **Edit agent →** button jumps to the full editor for everything the inspector doesn't cover.

!!! tip
    The Flow view is the fastest way to audit an unfamiliar agent: one glance shows what triggers it, which model thinks, which tools it can reach, and where replies go.

## Workflow agents: the graph render

Agents defined with a declarative graph in SOUL.yaml render their **actual** graph, read-only:

```yaml
workflow:
  entry: refine
  nodes:
    - id: refine
      tool: improve_draft
    - id: judge
      tool: evaluate_draft
      output: verdict
    - id: notify
      agent: editor-agent
  edges:
    - {from: refine, to: judge, max_iterations: 10}
    - {from: judge, to: refine, if: '{{not .verdict.ok}}', max_iterations: 5}
    - {from: judge, to: notify}
    - {from: notify, to: end}
```

How to read the render:

- **Layout** — nodes are placed in columns by BFS depth from the entry node, so execution generally flows left → right.
- **Entry** — the agent's trigger node is wired into the workflow's `entry` node (first node if `entry` is unset).
- **Node icons** — `⚙` tool nodes, `🤝` agent nodes (invoke a peer agent), `◇` branch nodes (pure routing, no action).
- **Edge labels** — each edge shows its `if` predicate (truncated) and, when its traversal budget exceeds 1, a `↺×N` cycle-budget label (e.g. `{{not .verdict.ok}}  ↺×5`).
- **Amber edges** — back-edges that close a cycle (refine→judge→refine loops) are highlighted amber and animated, so bounded loops are visible at a glance. Forward edges are green.
- **Termination** — edges to `end` (or with no target) are wired into the **📤 Output** node. Flows without an explicit terminal edge get dashed hint-wires from their deepest column to Output, since such flows end wherever no edge matches.

Clicking nodes shows their details, but graph-form workflows are **not editable** on this page yet — change the YAML under `workflow:` instead (full semantics in `docs/FLOW_GRAPHS.md`).

## Validating a graph before it runs

Graphs are validated at compile time — duplicate or missing node IDs, edges pointing at unknown nodes, bad node kinds, and an unknown `entry` are all refused with precise errors. Catch them before deploying:

```bash
sy agent validate path/to/SOUL.yaml
```

The same checks run behind the **Validate** button in the Agents editor (`POST /api/v1/agents/validate`), so you can iterate on the YAML and re-check without restarting anything.

## Resume after a crash

Each node visit in a workflow run is checkpointed. Re-running a crashed run restores completed visits — including their output variables — and recomputes the same deterministic path, so only the unfinished work executes. You don't need to do anything to get this; it is how the executor always runs graphs.

## Why the budgets matter

Every edge in a workflow graph carries `max_iterations` (default **1**), so cycles terminate by construction — a loop only repeats as many times as its back edge's budget allows, and `max_node_executions` (default 100) backstops the whole run. The `↺×N` labels on the Flow page are exactly those budgets, which makes runaway-loop review a visual check rather than a YAML audit.

!!! note
    The same graph runs in two places: as a scheduled workflow run, and in chat when the agent sets `reasoning.strategy: flow` — chat steps surface as the usual `reasoning.step` events in the Thinking panel and Activity log.
