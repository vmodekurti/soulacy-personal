# Workboard API

The Workboard is Soulacy's Kanban task board: create tasks, assign them to
agents, run them, review results, and download produced files. Everything
the Workboard GUI does goes through these endpoints.

All routes live under `/api/v1` and require
[authentication](index.md#authentication). They return
`503 {"error": "workboard not enabled"}` when no workboard store is wired.

## Tasks

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/workboard/tasks?status=&agent_id=` | List tasks, optionally filtered |
| `POST` | `/workboard/tasks` | Create a task (`201`) |
| `GET` | `/workboard/tasks/:id` | Get one task |
| `PATCH` | `/workboard/tasks/:id` | Update fields (partial) |
| `DELETE` | `/workboard/tasks/:id` | Delete a task (`204`) |

### Create a task

```bash
curl -X POST http://localhost:18789/api/v1/workboard/tasks \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Summarize this weeks support tickets",
    "description": "Group by product area, flag anything urgent.",
    "agent_id": "support-bot",
    "priority": "high",
    "tags": ["support", "weekly"],
    "due_at": "2026-07-01T10:00:00Z"
  }'
```

Body fields (all optional except a meaningful `title`): `title`,
`description`, `agent_id`, `status`, `owner`, `priority`, `tags`
(string array), `due_at` (RFC3339).

### Update a task

`PATCH` sends only the fields you want to change. An empty-string
`due_at` **clears** the due date:

```bash
curl -X PATCH http://localhost:18789/api/v1/workboard/tasks/12 \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"status": "done", "due_at": ""}'
```

### List tasks

```bash
curl -H "Authorization: Bearer $SOULACY_API_KEY" \
  "http://localhost:18789/api/v1/workboard/tasks?status=running&agent_id=support-bot"
```

```json
{
  "tasks": [ { "id": 12, "title": "…", "status": "running", "agent_id": "support-bot", … } ],
  "statuses": ["todo", "running", "needs_review", "done", "failed"]
}
```

(The `statuses` array is the server's authoritative status list — render
columns from it rather than hard-coding.)

## Runs

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/workboard/tasks/:id/run` | Start a run through the task's assigned agent (`202`) |
| `GET` | `/workboard/tasks/:id/runs` | List the task's run history |

`POST …/run` returns `202 Accepted` with the run record; the agent
executes **asynchronously** (a single run is bounded at 15 minutes).
Errors: `400` when the task has no `agent_id`, `409` when a run is
already active for the task.

```bash
curl -X POST -H "Authorization: Bearer $SOULACY_API_KEY" \
  http://localhost:18789/api/v1/workboard/tasks/12/run
```

Follow progress via the [event stream](../configuration/events.md):
`run.started`, `run.finished` / `run.failed`, and `run.artifact` events
carry `task_id`, `run_id`, and `attempt`.

## Artifacts

Files produced during a run (detected from file-writing tool calls) are
attached to the task automatically.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/workboard/tasks/:id/artifacts` | List a task's artifacts |
| `GET` | `/workboard/artifacts/:id/download` | Download one artifact file |

```bash
curl -H "Authorization: Bearer $SOULACY_API_KEY" \
  http://localhost:18789/api/v1/workboard/tasks/12/artifacts
```

```json
{
  "artifacts": [
    { "id": 3, "path": "/Users/you/reports/summary.md",
      "size_bytes": 14302, "tool": "write_file",
      "created_at": "2026-06-06T21:04:05Z" }
  ],
  "count": 1
}
```

The download endpoint streams the file with a `Content-Disposition:
attachment` header. It returns `404` for an unknown artifact and
`410 Gone` when the recorded file no longer exists on disk.

## Comments & reviewer notes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/workboard/tasks/:id/comments` | List comments on a task |
| `POST` | `/workboard/tasks/:id/comments` | Add a comment (`201`) |
| `DELETE` | `/workboard/comments/:id` | Delete a comment (`204`) |

```bash
curl -X POST http://localhost:18789/api/v1/workboard/tasks/12/comments \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"author": "vasu", "body": "Looks good — ship it.", "kind": "review"}'
```

## Error shapes

| Status | Meaning |
|--------|---------|
| `400` | Invalid body, non-integer ID, bad `due_at`, or task missing an agent |
| `404` | Task / comment / artifact not found |
| `409` | Task already has an active run |
| `410` | Artifact file no longer exists on disk |
| `503` | Workboard store not configured |

All errors use the standard `{"error": "…"}` envelope.
