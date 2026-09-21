---
name: summarize
description: Turn something long — a chat thread, meeting transcript, article, email chain, report, or file — into a brief sized to the ask, with decisions, open questions and action items pulled out. Use for "summarise", "TL;DR", "catch me up", "what did we decide", "what do I need to do".
license: Apache-2.0
allowed-tools: read_file fetch_url session_search semantic_memory_search kb_search read_skill_file
metadata:
  builtin: "true"
  category: productivity
---

# Summarize

A good summary lets the person skip the original safely. It is shaped by
what they will do with it — so decide that first.

## Pick the shape

| Ask sounds like | Shape |
|---|---|
| "TL;DR" / "in one line" | one sentence, then stop |
| "Catch me up" / "what happened" | 3–7 bullets in time order, then decisions and open questions |
| "What did we decide" | **Decisions** list only, each with who and when |
| "What do I need to do" | **Action items** only: owner, task, due, source line |
| "Summarise this report/article" | headline, 3–5 key points, one "so what" line, length ~10% of the original, max 400 words |
| "Prepare me for the meeting" | context in 3 lines, decisions pending, questions to ask, people involved |

If unsure, produce: **Headline** (1 line) · **Key points** (≤5) ·
**Decisions** · **Open questions** · **Action items** — and omit empty
sections rather than writing "none".

## How

- Read the whole thing before writing; for files use `read_file`, for links `fetch_url`, for past conversations `session_search` / `semantic_memory_search`.
- Keep the original's terms and names; do not rename things.
- Attribute claims and decisions to people when the source does ("Priya proposed…", not "it was proposed…").
- Quote figures, dates and deadlines exactly. Never add facts that are not in the source; if something important seems missing, say "not stated".
- Point back: for long sources, cite the location (page, timestamp, message date) after the item so the person can jump there.
- Mark uncertainty in the source honestly ("they *think* the vendor can deliver by June").
- Action items: if an owner is not named, write "owner unclear" — do not assign.

## Length discipline

Default to the shortest shape that answers the ask. Offer "want the detailed
version?" instead of delivering it unasked.
