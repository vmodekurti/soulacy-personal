# Deal Finder

Finds and ranks deals — flights, travel, or shopping — from your criteria,
sorted cheapest first, with prices and a currency.

## Requirements

| Requirement | Needed | Notes |
| --- | --- | --- |
| LLM provider | Yes | Any configured provider |
| Tools | `search_flights` / search tool | Deal/price lookup |
| Secrets | `TRAVEL_SEARCH_API_KEY` | Search/travel provider key |
| Channels | Optional | For deal alerts |
| Schedule | Optional | e.g. daily fare watch |

## Install

1. Review the readiness checklist; add the search tool and
   `TRAVEL_SEARCH_API_KEY`.
2. Run the **mock test** ("cheap flights NYC → London") — no live call.
3. Add the secret, run the **real test**.
4. (Optional) Schedule a recurring watch to a channel.
5. Install.

## Verify

```bash
sy eval --agent deal-finder --suite evals/golden/deal-finder.yaml
```

The live-search case is secret-backed and skips when the key is unset.

## Troubleshooting

If searches return nothing for valid queries, confirm the search tool's key and
quota. See [Common failures](../troubleshooting/common-failures.md).
