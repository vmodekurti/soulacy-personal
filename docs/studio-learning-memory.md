# Studio learning and memory

Studio can learn reusable workflow structure, model/strategy reliability,
repeated authoring preferences, and repair lessons. Learning is enabled by
default and can be disabled with:

```yaml
llm:
  studio:
    learning: false
```

## What is stored

- **Workflow patterns:** sanitized intent family, tool sequence, node order,
  branch identity, and parallel-group structure. Tool arguments and results are
  never stored. Explicit helpful/unhelpful ratings boost or suppress matching
  patterns without double-counting a user who changes their rating.
- **Strategy fit:** aggregate pass/fail counts for provider, model, and strategy.
  Prompt text, output text, identities, and sessions are excluded.
- **Preferences:** sanitized behavioral additions that recur across at least two
  different agents. Preferences are scoped to the authenticated user.
- **Lessons:** accepted repair guidance and observed response keys embedded in a
  local SQLite/sqlite-vec index.

Only explicit `run.completed` outcomes seed run learning. Degraded or failed
runs cannot become workflow patterns. ActionLog replay after restart is
idempotent by run ID.

## Human feedback

Chat responses expose 👍 and 👎 actions. The GUI submits the response's server-
issued `run_id` and `response_id` to:

```text
POST /api/v1/chat/feedback
```

Ratings are stored durably, redacted, capped, and upserted per user/response.
An unhelpful rating suppresses a single-run workflow pattern from future Studio
retrieval; helpful ratings increase its ranking evidence. Optional written
feedback becomes a pending procedural-learning proposal for review and never
changes an agent's rulebook automatically. Operators can inspect recent signals
with `GET /api/v1/learning/feedback`.

The feedback store retains at most 10,000 recent entries. A rating is accepted
only for an exact terminal `run.completed` event matching the submitted agent,
session, and run when ActionLog validation is available. This prevents a client
from manufacturing positive evidence for a run that never completed.

## Safety and control

Learned text is credential-redacted, length-bounded, rejected when it contains
instruction-override language, and quoted as untrusted data when included in a
generation prompt. A generated draft carries a one-time server proof; Studio
ignores a client-supplied preference baseline that was not issued by the server.

Operators can inspect learned state with:

```text
GET /api/v1/studio/learning-memory
```

Individual entries can be removed with:

```text
DELETE /api/v1/studio/learning-memory/workflow-pattern/{id}
DELETE /api/v1/studio/learning-memory/lesson/{id}
DELETE /api/v1/studio/learning-memory/preference/{id}
```

The endpoints require the same agent read/write RBAC permissions as Studio.
Preference deletion is restricted to the caller's user scope.

## Storage overrides

The defaults live under `SOULACY_WORKSPACE` (or `~/.soulacy`). They can be
overridden with `SOULACY_STUDIO_MACROS`, `SOULACY_STUDIO_STRATEGY_FIT`,
`SOULACY_STUDIO_PREFERENCES`, and `SOULACY_STUDIO_LESSONS`.

Changing the configured embedding provider or model automatically rebuilds the
lesson vector index from its retained lesson text. Embedding calls use the
configured LLM timeout hierarchy.

Agent semantic memory and Studio lesson retrieval share Soulacy's native
sqlite-vec infrastructure but remain separate datasets: agent memory is
agent-scoped runtime context, while lessons are user-scoped authoring guidance.
