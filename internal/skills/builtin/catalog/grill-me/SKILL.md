---
name: grill-me
description: Stress-test a plan, decision, or idea by interviewing the person round by round until every open decision is settled — each question comes with a recommended answer they can accept with "yes". Use for "grill me", "poke holes in this", "help me think this through", "am I missing anything", and before turning a vague request into an automation, trip, purchase, or project plan.
license: MIT
allowed-tools: person.model person.observe session_search semantic_memory_search kb_search web_search fetch_url
metadata:
  builtin: "true"
  category: thinking
  upstream: "https://github.com/mattpocock/skills/tree/main/skills/productivity/grilling"
  upstream-license: "MIT"
---

# Grill me

Interview the person until you both understand the plan the same way.
Nothing gets acted on until they say you have.

## Map the decisions

Treat the plan as a **tree of decisions**: each decision branches into the
ones that depend on it ("which city" comes before "which hotel"). The
**frontier** is every decision whose prerequisites are already settled —
the questions you can ask *now* without guessing at answers you have not
heard yet.

## Ask in rounds

Ask the whole frontier in one round, numbered, each with your recommended
answer, then wait. Word each question so "yes" accepts the recommendation.

```
Q1 · Budget: Keep the whole trip under $2,000 including flights?
→ Recommended: yes — that matches the two trips you took last year.

Q2 · Dates: Go the week of 10 March, before school holidays raise prices?
→ Recommended: yes.
```

- On a phone, keep a round to five questions or fewer. If the frontier is
  bigger, ask the five that unblock the most, and say how many remain.
- A question that depends on another question still open in this round
  belongs to a later round.
- After each round, settled answers push the frontier outward: recompute it
  and ask the next round.

## Facts are your job; decisions are theirs

Never ask the person something you can look up. Before a round, check
`person.model` (preferences, constraints, people involved),
`session_search` and `semantic_memory_search` (what they said before), and
`kb_search` / `web_search` + `fetch_url` for outside facts (opening hours,
prices, rules). Put what you found into the recommendation and cite it
briefly. The *choices* stay with the person: put each to them and wait.

When the person states a lasting preference ("I never take red-eyes"),
record it with `person.observe`.

## Finish

The session is done when the frontier is empty: every branch visited,
nothing silently assumed. Then give a short summary of every decision made
and ask the person to confirm it. Only after that confirmation hand the
plan to whatever comes next (a mission, an agent, a trip plan). If they
want to stop early, summarise what is settled and list what is still open.

## Credits

Adapted from `grilling` (invoked as `grill-me`) by Matt Pocock, MIT
License (see `LICENSE.upstream.txt`). Changes: Soulacy tools for finding
facts instead of sub-agents, phone-sized rounds, preference capture with
`person.observe`, and an explicit confirmed summary before any hand-off.
