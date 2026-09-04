# Knowledge bases

Knowledge bases give your agents retrieval-augmented generation (RAG): upload your documents once, and any agent can search them semantically with the built-in `kb_search` tool.

## Quick start

1. Open **📚 Knowledge** in the GUI and click **+ New KB**. Name it (e.g. `product-docs`), keep the default embedding settings, **Create**.
2. Click **+ Add document** and pick one or more files (`.md`, `.txt`, `.pdf`, `.docx`) — or paste text. Files embed one at a time so a local Ollama isn't overwhelmed.
3. Use **Test search** at the bottom of the KB view to confirm a query returns the right chunks.
4. Wire it to an agent: **Agents** → your agent → **Knowledge bases** → pick the KB → **Save**.

Same flow via API:

```bash
# Create the KB
curl -X POST http://localhost:18789/api/v1/knowledge \
  -H "Authorization: Bearer $SOULACY_API_KEY" -H "Content-Type: application/json" \
  -d '{"name":"product-docs","description":"Product manuals","embedding_provider":"ollama","embedding_model":"nomic-embed-text"}'

# Ingest a document
curl -X POST http://localhost:18789/api/v1/knowledge/product-docs/documents \
  -H "Authorization: Bearer $SOULACY_API_KEY" -H "Content-Type: application/json" \
  -d '{"title":"FAQ","source":"paste","mime_type":"text/plain","content":"..."}'

# Search it
curl -X POST http://localhost:18789/api/v1/knowledge/product-docs/search \
  -H "Authorization: Bearer $SOULACY_API_KEY" -H "Content-Type: application/json" \
  -d '{"query":"refund policy","top_k":5}'
```

## Embedding model configuration

Each KB is bound to one embedding provider + model, chosen at creation (defaults come from the server, typically `ollama` / `nomic-embed-text`). The vector dimension is probed automatically from the embedder when the KB is created — you never set it by hand. The KB header shows the binding:

```
ollama/nomic-embed-text · dim 768 · chunks 256/32
```

!!! warning
    The embedding model cannot change after creation — all stored vectors were produced with it. To switch models, create a new KB and re-ingest.

If the page shows *"Knowledge store is disabled"*, set `knowledge.db_path` in `config.yaml` and restart the gateway.

## Managing documents

The KB detail view lists every document with title, source, chunk count, size, and ingest time:

- **Delete** removes one document (and its chunks/vectors).
- Checkboxes + the bulk bar delete many at once.
- Re-ingesting under the same title creates a new document — delete the stale one if you are refreshing content.

Under the hood, ingestion chunks text on sentence boundaries with a parent-child scheme: small chunks are embedded for precise matching, but searches return the larger surrounding parent chunk so the agent gets coherent context. Search itself is hybrid — vector similarity fused with full-text (BM25) ranking — so exact terms like IDs and names score well too.

## Wiring KBs to agents (`kb_search`)

Declare which KBs an agent may search in its SOUL.yaml:

```yaml
knowledge:
  - product-docs
  - finance-local
```

In the GUI this is the **Knowledge bases** chip-picker in the agent editor (it lists your real KBs with their document/chunk counts).

The agent then gets the built-in `kb_search` tool and calls it on its own:

```
kb_search(kb="product-docs", query="warranty period for model X")
```

Results come back in well under a second from the local store. Make the agent use it reliably by saying so in the system prompt — e.g. the shipped Document Compliance Auditor template instructs: *"Search before judging — never audit from memory."*

## Test search

The **Test search** box at the bottom of every KB view runs the exact same search the agent's `kb_search` performs — query, `top_k`, ranked hits with the source document title, distance score, and the chunk content. Use it to sanity-check retrieval before blaming the agent's prompt.

!!! tip
    If a query you care about doesn't surface the right chunk in Test search, the agent won't find it either. Fix it at the data layer first: better document titles, smaller focused documents, or re-phrasing key sections.

## Keeping a KB fresh automatically

Pair a KB with a cron agent that re-syncs data on a schedule (see [Schedules](schedules.md)): the agent's tool fetches from the source system, deletes that day's stale documents, and POSTs new ones to `/api/v1/knowledge/<kb>/documents`. The shipped `finance-sync` example agent does exactly this.
