# Memory

Soulacy agents remember on two levels: conversation memory (scopes) keeps recent context flowing between turns, and brain memory (episodic / semantic / procedural layers) lets an agent learn across tasks — including a versioned, lockable rulebook it can update itself.

## Quick start

```yaml
# SOUL.yaml
memory:
  read_scopes: [session]        # what the agent loads as context
  write_scopes: [session]       # where its turns are persisted
  max_tokens: 2000

brain_memory:
  episodic:
    enabled: true               # remember past tasks
  procedural:
    enabled: true
    auto_update: true           # let it refine its own operating rules
```

In the GUI: the **Memory** section and the **Brain memory** layer cards live in the **Agents** editor; explore what an agent has remembered on the **🧠 Brain Mem** page.

## Conversation memory scopes

Each agent declares which memory tiers it reads from and writes to:

| Scope | Meaning |
|---|---|
| `session` | Per-conversation history — forgotten when the session ends |
| `agent` | Shared across all sessions of this agent |
| `global` | Persistent across conversations, visible beyond one agent |

`memory.max_tokens` caps how much recent history is injected into context. Inspect an agent's stored entries with:

```bash
sy memory list --agent <agent-id>
```

## Brain memory layers

Brain memory is long-term, structured memory with three independent layers (toggle each in the agent editor's **Brain memory** cards):

- **🕐 Episodic** — a record of past tasks and their outcomes, injected as "Recent task history". Written automatically after each reply; you can also write records by hand.
- **🔍 Semantic** — agent-scoped records retrieved by persistent native
  sqlite-vec similarity search. Production does not silently fall back to an
  in-process vector index.
- **📋 Procedural** — a markdown rulebook of operating rules, injected into the system prompt as `## Operating rules`.

Each layer has a `max_inject` knob (how many items to inject per task). Brain memory requires a memory directory — if the Brain Mem page warns it isn't enabled, set the `SOULACY_MEMORY_DIR` environment variable and restart.

### Semantic storage and embeddings

The embedded semantic backend is the effective default:

```yaml
vector:
  backend: sqlite-vec
  dims: 768

knowledge:
  embedding_provider: ollama
  embedding_model: nomic-embed-text
```

Entries are written to the workspace SQLite archive and remain available after
restart. Searches include the agent ID as a storage-level filter, preventing
one agent's semantic records from entering another agent's results. If the
vector store or embedder cannot initialize, semantic writes and retrieval fail
explicitly and startup logs explain why.

Changing embedding models can change vector dimensions. Stop the gateway,
inspect the migration, rebuild the index, and then update `vector.dims`:

```bash
sy memory reindex --model text-embedding-3-large --dry-run
sy memory reindex --model text-embedding-3-large --confirm-offline
```

The command probes the configured model's dimensions, checkpoints each batch,
resumes after interruption, and atomically swaps the verified index into place.
Use `--restart` only when intentionally discarding a checkpoint created for a
different target model. Provider credentials come from Soulacy configuration,
not command-line flags. See [Storage & backends](../configuration/storage.md).

### The Brain Mem page

**🧠 Brain Mem** shows, per agent: episodic record count, whether procedural rules are active, and last activity. Three tabs:

- **🕐 Episodic** — searchable timeline of records with tags; **+ Write** adds a manual record, **Clear all** wipes them.
- **📋 Procedural** — the rulebook editor (markdown, with 👁 live preview), plus locking and version history (below).
- **🔍 Context Preview** — type a hypothetical task and see the **exact** memory block that would be injected into the system prompt, with per-layer counts and a token estimate.

API base: `/api/v1/brain-memory/...` (stats, episodic and procedural CRUD, context preview).

## Procedural rulebooks: versions, locking, rollback

Self-updating agents are powerful and need guardrails, so every rulebook write is versioned (GitOps-style, append-only).

### Auto-update is opt-in

```yaml
brain_memory:
  procedural:
    enabled: true
    auto_update: true
```

Only with `auto_update: true` does the reasoning loop persist its learned rule changes after a run — each write emits a `rulebook.updated` event (visible in Activity). Without the opt-in, proposed updates are discarded and the rulebook only changes when you edit it.

### Version history & diff

On the Procedural tab, **⧗ History** lists every version with:

- a provenance badge — `auto` (the reasoning loop), `manual` (your edits), or `rollback`,
- timestamp and size,
- **Diff vs current** — a line-level add/remove view against the live rules,
- **Roll back**.

API:

```bash
curl http://localhost:1947/api/v1/brain-memory/<agent>/rulebook \
  -H "Authorization: Bearer $SOULACY_API_KEY"
# → {current, locked, versions[]}

# One historical version's full text
GET /api/v1/brain-memory/<agent>/rulebook/<version>
```

### Rollback never rewrites history

Rolling back to v3 re-applies v3's text as a **new** version with source `rollback` — the audit trail stays append-only.

```bash
curl -X POST http://localhost:1947/api/v1/brain-memory/<agent>/rulebook/rollback \
  -H "Authorization: Bearer $SOULACY_API_KEY" -H "Content-Type: application/json" \
  -d '{"version": 3}'
```

### Locking freezes the rules entirely

The **🔒 Lock** toggle freezes the rulebook: auto-updates from the reasoning loop **and** manual edits are refused (the API returns HTTP 423) until you unlock. The agent's runs still succeed — only the rule write is refused, and a warning event records it. Rollback also requires unlocking first.

```bash
curl -X POST http://localhost:1947/api/v1/brain-memory/<agent>/rulebook/lock \
  -H "Authorization: Bearer $SOULACY_API_KEY" -H "Content-Type: application/json" \
  -d '{"locked": true}'
```

!!! tip
    A good rhythm for self-updating agents: let `auto_update` run for a while, review the History diffs, roll back anything that drifted, then **Lock** once the rules are where you want them.

!!! note
    Don't confuse the layers: **Knowledge bases** (the 📚 Knowledge page) are document RAG you curate; **brain memory** is what the agent accumulates from its own experience.
