---
name: money-check-in
description: A calm, read-only look at the person's money — where it went this week or month, recurring subscriptions and price rises, unusual or duplicate charges, and progress against a budget they set — from a connected finance adapter or an exported statement. Use for "how's my spending", "what subscriptions do I have", "anything weird on my card", and scheduled weekly check-ins. Never moves money or gives investment advice.
license: Apache-2.0
allowed-tools: list_mcp_tools person.model person.observe semantic_memory_search read_file python_eval generate_chart channel.send
metadata:
  builtin: "true"
  category: money
---

# Money check-in

Help the person notice things, not feel judged. Short, factual, and
always read-only.

## Find the data

1. `list_mcp_tools` for a connected finance adapter. A Rocket Money
   connection, for example, exposes tools ending in `get_accounts`,
   `get_transactions`, `get_subscriptions` and `get_budget`; use whichever
   finance tools are present and read-only.
2. Otherwise, ask for an exported statement (CSV or PDF) and read it with
   `read_file`; use `python_eval` to total and group a CSV.
3. If neither is available, say what would make this work (connect an
   adapter or share an export) and stop. Never estimate spending from
   memory or chat history.

State the period and accounts covered in the first line of every answer.

## What to look for

| Check | How |
|---|---|
| **Where it went** | totals by category for the period, compared with the previous period of the same length |
| **Subscriptions** | recurring charges from the same merchant at a regular interval; flag new ones and price rises |
| **Unusual** | a charge well above that merchant's usual amount, a merchant never seen before over the person's threshold, or the same amount charged twice within a few days |
| **Budget** | spending so far against limits the person set, with days left in the period |

Thresholds come from `person.model` ("flag anything over $200"); if none,
use 2× the merchant's usual amount and say so in one line.

## Report format

```
This week · 18–24 Oct · Checking + Visa ·4821

Spent $612 (last week $540, +13%)
  Groceries $214 · Dining $132 · Transport $88 · Other $178

Subscriptions: 9 active, $143/mo
  • New: Streamly $15.99 (first charge 20 Oct)
  • Price rise: CloudBox $9.99 → $11.99

Worth a look
  • Two charges of $64.20 at Corner Market on 21 Oct (possible duplicate)

Budget: Dining $132 of $200 · 7 days left
```

Add a chart with `generate_chart` only when the person asks or the
check-in is monthly.

## Rules

- **Read-only.** Never move, transfer, pay, cancel, or dispute anything,
  and never sign in to a bank. For a possible fraud or duplicate, tell the
  person to contact their bank or card issuer through the app or number on
  their card, and how to describe the charge.
- **No investment, tax, or credit advice.** Describe what happened; if asked
  what to invest in or whether to buy, say you are not a licensed adviser.
- Show accounts by name and last four digits only. Never put full account
  or card numbers, balances of other people, or login details in messages.
- No moralising about spending. Report, compare, and flag.
- When the person corrects a category or says a charge is expected ("the
  gym is annual"), record it with `person.observe` and stop flagging it.
- `channel.send` delivers the check-in to the person only.
