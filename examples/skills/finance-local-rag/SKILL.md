---
name: finance-local-rag
description: Query personal financial data from the local sqlite-vec knowledge base (fast, offline RAG) instead of making live Rocket Money API calls. Use kb_search on the finance-local KB for accounts, net worth, budget, subscriptions, and 90 days of transactions.
---

# finance-local-rag

**Query personal financial data from the local sqlite-vec knowledge base instead of making live Rocket Money API calls.**

---

## What this skill gives you

The `finance-local` knowledge base is refreshed daily at 7 AM by the `finance-sync` agent. It contains:

| Document type | What's inside |
|---|---|
| `accounts` | Every linked account with current balance and institution |
| `net_worth` | Total assets, total debts, net worth, itemised by account |
| `budget` | Budget plan items with monthly amounts |
| `subscriptions` | Tracked recurring services |
| `transactions/chunk-*` | Up to 90 days of transactions, 50 per chunk, with date / merchant / amount / category |

**Advantage over live MCP calls:** kb_search is instant (< 100 ms) vs. 2–8 seconds for a live Rocket Money API round-trip. Use the local KB for routine questions. Fall back to live MCP only for data that may have changed today.

---

## How to query

Use the `kb_search` built-in tool with `kb="finance-local"`.

### Example queries

**Net worth**
```
kb_search(kb="finance-local", query="net worth assets debts total", top_k=3)
```

**Recent spending at a specific merchant**
```
kb_search(kb="finance-local", query="Amazon transactions purchases", top_k=5)
```

**Account balances**
```
kb_search(kb="finance-local", query="checking savings account balance", top_k=3)
```

**Monthly budget**
```
kb_search(kb="finance-local", query="budget plan monthly spending", top_k=3)
```

**Subscriptions**
```
kb_search(kb="finance-local", query="subscriptions recurring services monthly", top_k=3)
```

**Spending in a category (e.g. dining, groceries)**
```
kb_search(kb="finance-local", query="restaurant food dining transactions", top_k=8)
```

**Large transactions**
```
kb_search(kb="finance-local", query="large purchase transactions over 500 dollars", top_k=5)
```

---

## Formatting rules (unchanged from live MCP)

- All amounts in the KB are already in **dollars** (the sync converted from cents).
- Format dollar amounts as `$X,XXX.XX` in responses.
- Dates are `YYYY-MM-DD`.

---

## When to fall back to live MCP

Use the live `mcp__rocketmoney__*` tools when:

1. **The user asks about transactions from today** — the KB was last synced at 7 AM, so intraday activity is missing.
2. **The user asks the KB and gets zero or stale results** — the sync may not have run yet. Call the live tool and note: "Using live data — the local cache may be empty or outdated."
3. **The sync agent reported an error** — cookie may have expired. Tell the user to refresh it.

---

## Workflow: answer a financial question

```
1. Identify what type of data the question needs (accounts / net_worth / transactions / budget / subscriptions).
2. Call kb_search with a descriptive semantic query.
3. Read the <kb_results> block — extract the relevant numbers from the chunk content.
4. If the results are empty or clearly stale, note this and use the live MCP tool as a fallback.
5. Present the answer with dollar-formatted numbers.
```

---

## Sample Q&A

**Q: What is my net worth?**
```python
kb_search(kb="finance-local", query="net worth total assets debts", top_k=2)
# → reads the net_worth chunk, extracts Total Assets / Total Debts / Net Worth lines
```

**Q: How much did I spend on groceries last month?**
```python
kb_search(kb="finance-local", query="grocery supermarket food transactions", top_k=10)
# → scans transaction chunks, sums amounts for grocery merchants
```

**Q: What are my subscriptions?**
```python
kb_search(kb="finance-local", query="subscriptions recurring services", top_k=2)
# → reads the subscriptions chunk
```

**Q: What's my Chase checking balance?**
```python
kb_search(kb="finance-local", query="Chase checking account balance", top_k=3)
# → reads the accounts chunk, finds Chase entry
```
