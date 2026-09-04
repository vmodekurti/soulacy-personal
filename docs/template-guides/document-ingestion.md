# Document Ingestion

Builds a knowledge base from your documents (PDF, Markdown, text) and answers
questions grounded in them, citing the source document — and says when something
isn't in the knowledge base.

## Requirements

| Requirement | Needed | Notes |
| --- | --- | --- |
| LLM provider | Yes | Any configured provider |
| Tools | `search_knowledge` | Retrieval over the KB |
| Knowledge base | Yes | Ingest your documents first |
| Secrets | None (uses local KB) | Embeddings provider key if configured |
| Channels | Optional | For Q&A over a channel |

## Install

1. Create or select a knowledge base and ingest your documents (see
   [Knowledge Bases](../using/knowledge.md)).
2. Review the readiness checklist — it confirms the KB is populated.
3. Run the **mock test** with a question you know the answer to.
4. Run the **real test** against the ingested corpus.
5. Install.

## Verify

```bash
# after ingesting the fixtures and setting KB_FIXTURE_LOADED=1
sy eval --agent document-ingestion --suite evals/golden/kb-ingestion.yaml
```

The KB cases are gated on `KB_FIXTURE_LOADED` and skip cleanly when unset.

## Notes

Answers are grounded and cite the source document; out-of-scope questions get an
honest "not in the knowledge base." See
[Common failures](../troubleshooting/common-failures.md).
