# Workflow Steps

A `workflow:` block turns an agent into a checkpointed tool pipeline — deterministic steps that survive crashes and resume where they left off.

## Quick Start

```yaml title="agents/report-writer/SOUL.yaml"
id: report-writer
name: Report Writer
description: Run a repeatable reporting pipeline
trigger: channel
channels: [http]
llm:
  provider: openai
  model: gpt-4o
system_prompt: Run the reporting workflow.

workflow:
  steps:
    - id: search
      tool: web_search
      input: '{"query":"{{.trigger}}"}'
      output: search_results
      on_error: retry

    - id: summarize
      tool: summarize_report
      input: '{"search_results":{{.search_results}}}'
      output: report
      on_error: abort

enabled: true
```

When an agent declares `workflow:`, the runtime delegates to the workflow
executor instead of the free-form LLM loop. The reply is the output of the
last completed step.

!!! tip
    Linear steps run strictly in order. If you need conditional routing,
    loops (refine→judge), or fan-out, use the graph form —
    [Flow Graphs](flows.md) — which lives under the same `workflow:` key.

## Step Fields

| Field | Description |
|-------|-------------|
| `id` | Unique within the workflow. Used as the checkpoint key. |
| `tool` | Tool name to invoke — any tool the agent can normally call. |
| `prompt` | Optional prompt text reserved for LLM-assisted step behavior. |
| `if` | Go template condition; the step is skipped when it renders empty, `false`, or `0`. |
| `on_error` | `abort` (default), `retry`, or `skip`. |
| `input` | Go template that renders the tool's input string (usually JSON). |
| `output` | Variable name that stores this step's parsed result for later steps. |

## Template Variables

Templates are standard Go `text/template`. The variable map starts with:

| Variable | Description |
|----------|-------------|
| `.trigger` | The flattened inbound message text. |

Each step with `output: some_name` adds `.some_name` for every later step.
Results that parse as JSON become structured values (so `{{.report.title}}`
works); anything else is stored as a plain string. Missing keys render as
zero values rather than erroring.

```yaml
workflow:
  steps:
    - id: gather
      tool: web_search
      input: '{"query":"{{.trigger}}"}'
      output: search

    - id: write
      tool: write_brief
      input: '{"topic":"{{.trigger}}","search":{{.search}}}'
      output: brief

    - id: publish
      if: '{{.brief}}'          # skip when the brief is empty
      tool: post_brief
      input: '{{.brief}}'
```

## Error Handling

| `on_error` | Behavior |
|------------|----------|
| `abort` (or empty) | Mark the step failed, stop the workflow, surface the error. |
| `retry` | Re-run the tool once; abort if it still fails. |
| `skip` | Mark the step completed with no output and continue. |

A failed step writes a `failed` checkpoint, so you can see exactly where a
run died.

## Checkpointing and Resume

The executor checkpoints around every step:

1. Before running: the step is marked `in_progress`.
2. After success: marked `completed` with the step's output saved as state.
3. On failure: marked `failed`.

When a run is **resumed with the same run ID** (after a crash or restart),
completed steps are skipped entirely — their saved output is restored into
the template variable map — and execution continues from the first
non-completed step. Side effects of completed steps are never repeated.

!!! warning
    Resume identity is the run ID. A fresh trigger always gets a new run ID
    and re-executes everything; only resuming an existing run skips work.

## When To Use Workflows

Workflows shine for deterministic operational jobs:

- scheduled reports (pair with `trigger: cron`),
- ingest-and-summarize pipelines,
- multi-step sync jobs,
- anything where retry/skip semantics and restart safety matter.

Use normal tool calling or a [reasoning strategy](reasoning.md) when the
model should decide the plan at runtime, and [Flow Graphs](flows.md) when the
plan is fixed but branches or loops.
