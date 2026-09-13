# Verified Autopilot

Autopilot is a place to inspect evidence and control bounded autonomy. Start
with [the release walkthrough](../use-cases/verified-release.md); it explains the
actual buttons with a read-only test agent.

## What each tab is for

| Web tab | Use it for | Do not confuse it with |
|---|---|---|
| Run proofs | Inspect a run's checks, tools, duration, cost, and revision | Independent verification of every real-world claim |
| Releases | Snapshot, simulate, run candidates, canary, promote, freeze, roll back | Deploying the server binary or iOS app |
| Regression checks | Review checks derived from failures against candidate evidence | Automatically rewriting agent instructions |
| Goal teams | Run a bounded dependency graph of agent tasks | Unrestricted background workers with unlimited budget |
| Safe Undo | Review configured reversible external changes | A universal undo for arbitrary tools |

On iPhone, use **Settings → Verified Autopilot**; some controls are also
available from the agent detail screen. Gateway capability and authorization
still apply even when the app contains the screen.

## What counts as a proof?

A proof records the revision, observed calls, outcome, and explicit acceptance
checks. No acceptance contract means an outcome may be **unverified**. A
simulation is clearly marked and cannot raise the production reliability score.
The local checksum detects changes; it is not an external auditor's signature.

Useful mission checks include output text/regex, a required successful tool,
an exact tool allowlist, maximum duration, and maximum inference cost. A
`business_outcome` claim is not made true by asking a model to assert it.

For example, add this fragment to a saved test agent, preserving its other fields:

```yaml
mission:
  goal: Summarize supplied notes without external actions.
  acceptance:
    - id: questions
      type: output_contains
      value: Open questions
    - id: no-false-send
      type: output_not_contains
      value: I sent the email
  limits:
    max_cost_usd: 0.25
    max_duration: 2m
    allowed_tools: []
```

The second substring check is illustrative, not a complete detector of false
send claims. Budgets are ceilings, not price estimates. Normal model policy,
pricing admission, and tool permissions also apply. See [cost controls](../LLM_COST_CONTROLS.md).

## A small goal team: draft, then review

Create two saved read-only agents first. In **Goal teams**, give the goal a
title/objective and bounded cost/time limits. The task JSON uses **real agent
IDs**, not display names. Replace the IDs below before saving:

```json
[
  {
    "id": "draft",
    "title": "Draft action plan",
    "agent_id": "notes-assistant",
    "prompt": "Use only these fictional notes: Maya sends the draft Tuesday; demo owner unknown. Produce an action plan and Open questions.",
    "depends_on": [],
    "budget": {"max_cost_usd": 0.25, "max_duration_ms": 120000}
  },
  {
    "id": "review",
    "title": "Check the draft",
    "agent_id": "notes-reviewer",
    "prompt": "Check the dependency output against the supplied notes. Flag invented owners or dates. Do not send or change anything.",
    "depends_on": ["draft"],
    "budget": {"max_cost_usd": 0.25, "max_duration_ms": 120000}
  }
]
```

Set an overall budget that deliberately accommodates the tasks (for this
example, at most $0.50 and five minutes). Saving creates a draft; starting is
separate. Inspect task results and dependency handling. A reviewer agent is
another fallible model, not an independent ground-truth source.

## Stop and recovery controls

**Freeze now** blocks new runs and cancels this owner's active runs for the
agent. It cannot reverse completed external work. **Roll back** changes future
routing to an available prior version in that release channel; inspect the
target before confirming. Do not use these buttons casually on someone else's
active workload.

If a run's outcome is uncertain, inspect the source system and receipts before
repeating any write. [Safe Undo](../SAFE_UNDO.md) has a separate recovery model
for the narrow resources it supports.
