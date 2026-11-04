---
name: meeting-prep
description: Brief the person before a meeting — who is attending and how they know them, what was said last time, open threads and promises on both sides, relevant files, and a few questions worth asking — in one short note they can read in two minutes. Use for "prep me for my 2pm", "who is this meeting with", "what do I need for tomorrow's meetings", and scheduled pre-meeting briefs.
license: Apache-2.0
allowed-tools: mobile.invoke mobile.command_status mobile.list_nodes person.model semantic_memory_search session_search kb_search web_search fetch_url read_file channel.send read_skill
metadata:
  builtin: "true"
  category: productivity
---

# Meeting prep

The person should walk in knowing who they are meeting, where things were
left, and what they want out of it. Two minutes of reading, not a dossier.

## Find the meeting

`mobile.invoke` `calendar.events` for the requested window (then
`mobile.command_status`). Take title, time, place or link, attendees, and
any description or agenda. If no phone is paired, ask for the meeting
details instead; never invent attendees or times. Skip events that are
clearly not meetings (focus blocks, travel, holds) unless asked.

## Gather context, nearest first

1. **History with the person** — `session_search` and
   `semantic_memory_search` for each attendee's name and the meeting topic:
   last conversation, decisions, promises (by either side), open questions.
2. **What Soulacy knows about them** — `person.model` for each attendee's
   relationship to the person (manager, client, friend), how the person
   likes to deal with them, and anything they asked to be reminded of.
3. **Files** — `kb_search` and any files the person points to (`read_file`)
   for the agenda, last notes, or shared documents. Use the `summarize`
   skill (`read_skill summarize`) for anything long.
4. **Public context, only for external attendees the person has not met** —
   `web_search` for name + company to confirm role and company, citing the
   page. Work context only: no personal life, family, address, or social
   accounts.

## Brief format

```
14:00–14:30 · Q4 roadmap review · Zoom
With Dana Ruiz (your manager), Sam Okafor (Design, new to you)

Last time (3 Oct): agreed to cut the export feature; Dana wanted a
date for the beta.
You owe: beta date. Owed to you: Sam's mocks (promised 10 Oct; not seen).
Files: Roadmap v3 (kb), mocks link from Sam's 9 Oct message.

Ask / decide
• Lock the beta date (you proposed 18 Nov)
• Are Sam's mocks ready?
```

- Lead with time, title, and who; then history, then what is owed, then
  two or three questions or decisions. Keep it under a phone screen.
- For a day with several meetings, one block per meeting in time order,
  with back-to-back meetings flagged.
- Say plainly when there is no history ("first meeting with Sam").

## Rules

- Everything you state about past meetings must come from a conversation,
  note, or file you found; cite the date. No guesses about what someone
  "probably" wants.
- Never contact attendees, accept or decline the invite, or edit the
  event. `channel.send` delivers the brief to the person only.
- Keep private context about one attendee out of a brief another attendee
  might see if the person forwards it; when in doubt, put sensitive notes
  under a final *Just for you* line.
