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
   York fact becomes active. If you say something is *no longer true* ("I don't
   have a dog anymore"), the old fact is **retracted** rather than replaced.
   Compatible details are added alongside; exact restatements are dropped.
   Relationships between entities ("User → works at → Acme") are stored as a
   small graph; a new object for the same subject and relationship supersedes
   the old one.
5. **On your next message**, the agent's system prompt gains a short block:

   ```
   ### 🧠 MEMORY & PREFERENCES
   - User lives in New York
   - User prefers short answers
   - User works at Acme
   ```

   Facts come first, then the most relevant relationships if budget remains.
   The block is capped at about 50 tokens, so memory never crowds out the task.

## Facts that come and go

- **This-conversation-only facts.** "For this chat, answer in French" is
  extracted as a *temporary* fact. It is recalled only inside the session it
  came from and never leaks into other conversations. The Learning page marks
  these "this conversation".
- **Expiring facts.** Any fact can carry an expiry date, for example "User is
  travelling until Friday". Expired facts stop being recalled but stay listed
  until you delete them. Set an expiry when adding or editing a fact.
- **Retracted facts.** When you say something stopped being true, the old
  fact is retracted, kept for audit, and never recalled again.

## Where facts live and who can see them

Every fact is scoped to three things: the workspace, the **owner** (the
signed-in identity that had the conversation), and the agent. The runtime never
reads facts across owners, and the management API enforces the same rule.
Workspace admins can inspect another member's memory; nobody else can.

Facts are extracted only from turns with a stable authenticated identity. Turns
on shared external channels, dry runs, and simulations are excluded, so a fact
is always attributable to one person.

Superseded facts are kept, marked `superseded` with a pointer to the fact that
replaced them; retracted facts are kept, marked `retracted`. Every fact also
has a **change history**: created, edited (with the previous wording),
superseded, retracted, or deleted, with who did it (you, the extractor, or an
external provider) and when. The history survives deletion of the fact.

## Managing what is remembered

Open **Learning → 🧠 What it remembers**. You can:

- **Search** with plain words; results are ranked by relevance.
- **Filter** active, superseded, or retracted facts, or all of them.
- **Edit** a fact inline, change its category, or set an expiry date.
- **🕘 History** to see every change a fact went through.
- **Relationships** view to inspect and delete the entity graph.
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
    graph_enabled: true      # remember relationships between entities
    instructions: ""         # extra guidance for extraction (max 240 chars)
    custom_categories: []    # e.g. [role, project, health]
```

All of these can be changed from **Config → Adaptive memory** and apply to the
next turn without a restart.

**Shaping what gets remembered.** `instructions` is appended to the extraction
prompt, for example "Also capture the user's job title and team. Ignore
weather small talk." `custom_categories` adds categories beyond the built-in
four; extracted or hand-added facts may then use them. Both are forwarded to an
external provider when one is configured. Environment variables use the usual prefix, for
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
its own extraction and reconciliation, and its graph memory, change history,
expiry dates, and custom instructions and categories map onto the same
features here. Two things stay local-only: the superseded/retracted audit
states (an external provider rewrites or deletes in place, so only its history
log shows what changed) and this-conversation-only facts. If the provider is not configured or
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
