# Workboard

The Workboard is a kanban board where you queue tasks for agents, run them with one click, and review their output, files, and history in one place.

## Quick start

1. Open **▦ Workboard** in the GUI.
2. Click **+ New Task**, give it a title, a description (this becomes the agent's instructions), and assign an **Agent**.
3. On the new card, click **▶ Run**. The card moves to **Running**, then to **Needs Review** or **Failed**.

API equivalents:

```bash
# Create a task
curl -X POST http://localhost:18789/api/v1/workboard/tasks \
  -H "Authorization: Bearer $SOULACY_API_KEY" -H "Content-Type: application/json" \
  -d '{"title": "Summarize Q2 numbers", "description": "...", "agent_id": "research-agent"}'

# Run it
curl -X POST http://localhost:18789/api/v1/workboard/tasks/<id>/run \
  -H "Authorization: Bearer $SOULACY_API_KEY"
```

## The board

Five columns: **Todo → Running → Needs Review → Done → Failed**. Move cards with the ◀ / ▶ buttons (or edit the status in the task editor); filter the whole board by agent with the dropdown in the header. While anything is running the board quietly refreshes every few seconds so status flips show up on their own.

## Task fields

Open any card to edit:

| Field | Notes |
|---|---|
| Title / Description | The description is what the agent receives as its task |
| Agent | Which agent runs it; "— none —" makes it a plain tracking card |
| Status | One of the five columns |
| Owner | Who reviews this — shown as an `@owner` badge on the card |
| Priority | `low` · `normal` · `high` · `urgent` (badges: ▽ · — · ▲ · ‼) |
| Tags | Comma-separated labels |
| Due date | Cards show "due today / due tomorrow / overdue (…)" — overdue turns red |

## Running tasks and retries

- **▶ Run** starts a new attempt through the assigned agent. A task that ended in **Failed** shows **▶ Retry** instead — retrying starts attempt #2, #3, … while preserving all prior attempts in the history.
- The server rejects duplicate concurrent runs of the same task (409), so double-clicking is safe.
- Each attempt appears in the editor's **Run history** with its attempt number, status badge, start/end time, session token/cost metrics, the result text, the failure reason (if any), and the session/action-log identifiers for deeper digging.

Run lifecycle events (`run.started`, `run.finished`, `run.failed`) are published to the event stream, so you can wire failure webhooks — see [Dashboard & Activity](dashboard.md).

## Artifacts

Files the agent writes during a run are captured automatically and listed in the task editor's **Artifacts** panel — name, size, when it was created, which tool produced it, and which run. Click **⬇ Download** to fetch the file.

API:

```bash
curl "http://localhost:18789/api/v1/workboard/tasks/<id>/artifacts" \
  -H "Authorization: Bearer $SOULACY_API_KEY"

# Direct download link (also what the GUI button uses)
GET /api/v1/workboard/artifacts/<artifact-id>/download?api_key=<key>
```

Every captured file also emits a `run.artifact` event with its path, size, and producing tool.

## Comments & review notes

Each task has a discussion thread in the editor. Two kinds of entries:

- **💬 comment** — ordinary notes.
- **🔍 review note** — visually highlighted, for reviewer feedback (pairs naturally with the **Needs Review** column and the **Owner** field).

Type in the compose row, pick the kind, press **Enter** or **Add**. Entries record author and timestamp and can be deleted individually.

## When runs fail hard

Two safety nets catch failures:

1. **The Failed column + Retry.** Normal task failures (tool errors, model errors, timeouts) land the task in **Failed** with the reason in its run history. Fix the cause and hit **▶ Retry**.
2. **The dead-letter queue.** When a run fails at the engine level, the failed message is pushed to the DLQ so nothing is silently lost. Inspect and clean it via the admin API:

    ```bash
    curl "http://localhost:18789/api/v1/admin/dlq?queue=<agent-id>" \
      -H "Authorization: Bearer $SOULACY_API_KEY"
    # GET  /api/v1/admin/dlq/<id>      — one item with payload, error, attempts
    # DELETE /api/v1/admin/dlq/<id>    — discard after handling
    ```

!!! tip
    Use **Needs Review** as the agent→human handoff: have agents do the work, then a human owner checks the run output and artifacts, leaves a 🔍 review note, and moves the card to **Done**.
