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
  never stored.
- **Strategy fit:** aggregate pass/fail counts for provider, model, and strategy.
  Prompt text, output text, identities, and sessions are excluded.
- **Preferences:** sanitized behavioral additions that recur across at least two
  different agents. Preferences are scoped to the authenticated user.
- **Lessons:** accepted repair guidance and observed response keys embedded in a
  local SQLite/sqlite-vec index.

Only explicit `run.completed` outcomes seed run learning. Degraded or failed
runs cannot become workflow patterns. ActionLog replay after restart is
idempotent by run ID.

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
