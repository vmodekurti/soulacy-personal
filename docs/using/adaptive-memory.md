# Adaptive memory

Soulacy remembers what matters about you and forgets what stopped being true.
After each conversation turn it quietly distils durable facts (preferences,
identity, constraints, and the people, places, and things you mention), checks
them against what it already knows so newer facts replace stale ones, and adds
the few most relevant facts to the next prompt. Nothing about this adds latency
to chat, and everything it remembers is yours to inspect, edit, or erase.

Adaptive memory is on by default, runs entirely on your own machine at no extra
cost, and is private to each signed-in user.

## What it does, in one turn

1. **You chat.** "I moved to New York last month, and please keep answers
   short."
2. **The agent replies** as usual. The reply is not delayed.
3. **In the background**, a small extraction call reads the turn and proposes
   facts: *User lives in New York* (identity) and *User prefers short answers*
   (preference). Casual turns such as "thanks" produce nothing and write
   nothing.
4. **Each fact is reconciled** against your existing facts. If you previously
   said you lived in San Francisco, that fact is marked superseded and the New
   York fact becomes active. Compatible details are added alongside; exact
   restatements are dropped.
5. **On your next message**, the agent's system prompt gains a short block:

   ```
   ### 🧠 MEMORY & PREFERENCES
   - User lives in New York
   - User prefers short answers
   ```

   The block is capped at about 50 tokens, so memory never crowds out the task.

## Where facts live and who can see them

Every fact is scoped to three things: the workspace, the **owner** (the
signed-in identity that had the conversation), and the agent. The runtime never
reads facts across owners, and the management API enforces the same rule.
Workspace admins can inspect another member's memory; nobody else can.

Facts are extracted only from turns with a stable authenticated identity. Turns
on shared external channels, dry runs, and simulations are excluded, so a fact
is always attributable to one person.

Superseded facts are kept, marked `superseded` with a pointer to the fact that
replaced them. That is the audit trail: you can see what the agent used to
believe and when it changed.

## Managing what is remembered

Open **Learning → 🧠 What it remembers**. You can:

- **Search** with plain words; results are ranked by relevance.
- **Filter** active facts, superseded facts, or both.
- **Edit** a fact inline, or change its category.
- **➕ Add Fact** by hand, for anything you want the agent to know now.
- **🗑️ Delete** any fact; agents stop seeing it on the next turn.
- **📥 Export Memory (JSON)** for a copy of everything, including superseded
  facts.
- **⚠️ Purge All User Memory**, behind a confirmation, to erase everything for
  one agent or across all agents.

The same operations are available on the API under `/api/v1/memory/facts`.

## Turning it off for one agent

```yaml
# SOUL.yaml
memory:
  adaptive: false
```

An agent with `adaptive: false` neither extracts facts nor receives the memory
block. Everything else about it is unchanged.

## Configuration

```yaml
memory:
  adaptive:
    enabled: true
    provider: local          # local (default) or an external provider
    model_provider: ""       # small model for extraction; "" = the agent's provider
    model: ""
    min_confidence: 0.5      # discard weaker candidates
    similarity_threshold: 0.85
    max_prompt_facts: 5
    prompt_token_budget: 50
```

All of these can be changed from **Config → Adaptive memory** and apply to the
next turn without a restart. Environment variables use the usual prefix, for
example `SOULACY_MEMORY_ADAPTIVE_ENABLED=false`.

**Extraction cost.** Each extraction prompt is under 200 input tokens, and
conflict checks are shorter still. Point `model_provider`/`model` at a small,
fast model to keep this negligible; a local Ollama model works well.

**Embeddings.** Conflict detection and relevance ranking use the same embedding
model as Knowledge (`knowledge.embedding_provider`). Without an embedder,
adaptive memory still works using keyword matching.

## Using an external memory service

If your organisation already runs a hosted or self-hosted memory service that
speaks the Mem0 API, you can hand extraction and storage to it instead of the
built-in engine:

```yaml
memory:
  adaptive:
    provider: mem0
    mem0:
      base_url: https://api.mem0.ai     # or your self-hosted server
      api_key: ${MEM0_API_KEY}
      enable_graph: false
```

The switch takes effect immediately when saved from **Config → Adaptive
memory**. The same GUI, API, and per-agent controls apply; the provider does
its own extraction and reconciliation. If the provider is not configured or
its settings are invalid, Soulacy logs a warning and keeps using the built-in
engine, so memory never silently disappears.

## Relationship to the other memory tiers

| Tier | What it holds | Scope |
|---|---|---|
| Conversation memory | Recent turns of a session | Session / agent |
| Archive & vector memory | Searchable transcript history | Agent |
| Learning notebook | Reviewed lessons and procedures | Owner + agent |
| **Adaptive memory** | Facts about the user, auto-maintained | Owner + agent |

Adaptive memory is the only tier that updates itself as facts change. The
Learning notebook stays review-first: procedures and lessons never activate
without a human decision.
