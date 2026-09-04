# Research Librarian

Produces a short, sourced research brief on a topic. It synthesizes findings,
cites where claims come from, and refuses to fabricate citations.

## Requirements

| Requirement | Needed | Notes |
| --- | --- | --- |
| LLM provider | Yes | A capable reasoning model recommended |
| Tools | `web_search`, `fetch_url` | Gather and read sources |
| Secrets | `WEB_SEARCH_API_KEY` | If web search uses a keyed provider |
| Channels | Optional | For scheduled briefs |
| Schedule | Optional | e.g. weekly topic digest |

## Install

1. Review the readiness checklist; enable web search and add
   `WEB_SEARCH_API_KEY`.
2. Run the **mock test** ("brief on solid-state batteries") to confirm structure.
3. Add the secret, run the **real test** with web search live.
4. (Optional) Schedule a recurring brief to a channel.
5. Install.

## Verify

```bash
sy eval --agent research-librarian --suite evals/golden/research-librarian.yaml
```

## Notes

The agent is designed to say when it lacks a source rather than invent one. See
[Common failures](../troubleshooting/common-failures.md).
