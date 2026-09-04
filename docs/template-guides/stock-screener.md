# Stock Screener

Screens equities by your criteria (market cap, sector, valuation, momentum) and
reports the top matches. It surfaces quantitative results and declines to give
personalized investment advice.

## Requirements

| Requirement | Needed | Notes |
| --- | --- | --- |
| LLM provider | Yes | Any configured provider |
| Tools | `get_stock_price`, market-data/screen tool | Live quotes and screening |
| Secrets | `STOCK_DATA_API_KEY` | Market-data provider key |
| Channels | Optional | For scheduled digests |
| Schedule | Optional | e.g. pre-market screen |

## Install

1. Review the readiness checklist; add the market-data tool and
   `STOCK_DATA_API_KEY`.
2. Run the **mock test** ("screen large-cap tech") — no live call.
3. Add the secret, run the **real test** (e.g. latest price for AAPL).
4. (Optional) Schedule a daily screen to a channel.
5. Install.

## Verify

```bash
sy eval --agent stock-screener --suite evals/golden/stock-screener.yaml
```

The live-price case is secret-backed and skips when `STOCK_DATA_API_KEY` is unset.

## Notes

This agent is not a financial advisor and won't recommend trades. See
[Common failures](../troubleshooting/common-failures.md) for delivery/data issues.
