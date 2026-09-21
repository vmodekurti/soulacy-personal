---
name: inbox-triage
description: Sort incoming email or messages into what needs the person, what is FYI, and what is noise; pull out deadlines and commitments; draft replies in their voice for the ones that need one. Use for "go through my inbox", "anything important", "reply to these", scheduled triage.
license: Apache-2.0
allowed-tools: person.model person.observe semantic_memory_search session_search read_file write_file channel.send read_skill
metadata:
  builtin: "true"
  category: communication
---

# Inbox triage

The person should end with three short lists and a set of ready-to-send
drafts — not a summary of every message.

## Classify each message

| Bucket | Test |
|---|---|
| **Needs you** | asks the person a question, needs a decision, or has a deadline they own |
| **FYI** | worth knowing, no action (updates, confirmations, receipts they should see) |
| **Noise** | newsletters, promotions, automated notices with no action |

Use `person.model` for who matters (family, boss, key clients rank up;
their stated mutes rank down) and for how they like things handled. When a
sender is unknown and the ask is money, credentials, or urgency, mark it
**suspicious** — do not draft a reply, do not follow links.

## Extract

From every non-noise message: sender, one-line gist, **deadline** (exact
date if stated), and any **commitment the person made or is being asked
to make**. Deadlines within 48 hours go to the top.

## Draft replies (Needs-you only)

Follow the `writing-in-your-voice` skill (`read_skill writing-in-your-voice`).
One draft per message, ready to paste, answering every question asked. If
the reply depends on something only the person knows (are they free
Thursday? do they approve the budget?), write the draft with a clearly
marked `[YES/NO]` or `[amount]` slot rather than guessing.

## Report format

```
Needs you (3)
1. Mark — contract redlines, due Thu → draft below
2. Dana — confirm Friday? → draft below [YES/NO]
3. Bank — unusual login (verify in the app; no reply)

FYI (2)
• Delivery arrives Wed · Priya shared the deck

Noise: 14 archived-worthy (newsletters, promos)

— Drafts —
[1] Mark …
[2] Dana …
```

- Never send anything yourself unless the person has explicitly turned on auto-reply for that sender; `channel.send` is for delivering *this report*.
- Keep it under a screen: if Needs-you exceeds 7, show the 7 most urgent and say how many more.
- When the person changes a classification ("Dana is always FYI"), record it with `person.observe`.
