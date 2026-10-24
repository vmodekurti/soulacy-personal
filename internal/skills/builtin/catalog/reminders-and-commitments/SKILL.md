---
name: reminders-and-commitments
description: Keep track of what the person owes and what is owed to them — promises made in chats and email ("I'll send it Friday"), deadlines, follow-ups, and open reminders — and nudge at the right time. Use for "remind me", "what do I owe people", "did I say I'd…", "follow up if they don't reply", and scheduled commitment sweeps.
license: Apache-2.0
allowed-tools: mobile.invoke mobile.command_status mobile.list_nodes person.model person.observe semantic_memory_search session_search create_monitor list_monitors pause_monitor channel.send read_skill
metadata:
  builtin: "true"
  category: life
---

# Reminders & commitments

The person should never be surprised by something they promised. Keep one
honest list, nudge once at a useful time, and drop things the moment they
are done.

## What counts

| Kind | Example | Owner |
|---|---|---|
| **I owe** | "I'll send the deck Friday", "I'll call Mum this weekend" | the person |
| **Owed to me** | "Mark will get back to you Monday", a refund, a reply | someone else |
| **Deadline** | renewal date, form due, RSVP by | the person |
| **Reminder** | "remind me to water the plants at 6" | the person |

Only record a commitment that someone actually stated. "Let's catch up
sometime" is not a commitment; "I'll book a table for Thursday" is. When
unsure, ask once: *"Track 'send Priya the notes' for Friday?"*

## Gather

1. Open reminders: `mobile.invoke` `reminders.list`, then
   `mobile.command_status`. If no phone is paired or Reminders is not
   enabled, say so in one line and continue with the other sources.
2. Recent conversations: `session_search` and `semantic_memory_search` for
   phrases like "I'll", "will send", "by Friday", "remind me", "get back to".
3. Known commitments and habits: `person.model` (how far ahead they like a
   nudge, quiet hours, who matters).

Every item needs: **what**, **who** (to or from), **due** (exact date and
time in the person's zone, or "no date"), and **where it came from** (the
message or reminder). Never invent a due date; "no date" is a valid answer.

## Nudge

- One-off nudges use `create_monitor` with `at` (RFC3339, the person's
  zone) and a prompt that restates the item and its source. Default lead
  time: the morning of the due day for small tasks, one working day ahead
  for anything that needs preparation, unless `person.model` says otherwise.
- "Follow up if they don't reply" → a one-shot monitor at the follow-up
  time whose prompt first checks whether a reply has arrived and stays
  silent if it has.
- Respect quiet hours. Never schedule a nudge between the person's
  sleep and wake times; move it to the start of the next morning.
- Before creating a nudge, `list_monitors` to avoid duplicates for the
  same item. Use `pause_monitor` when the person says "not now" or
  "snooze"; create a new one for the new time.

## Report format

```
You owe (3)
1. Send Priya the notes — due Fri 14:00 · from chat Tue
2. Renew passport — due 12 Nov · from email from HM Passport Office
3. Call Mum — this weekend, no exact time

Owed to you (1)
• Mark: contract feedback — said "Monday" (2 days late) → draft nudge below

Nudges set: Fri 09:00 (Priya notes)
```

- Late items go first, then by due date; "no date" items last.
- Drafts for nudging other people follow the `writing-in-your-voice` skill
  (`read_skill writing-in-your-voice`) and are
  never sent by you; `channel.send` delivers *this report* to the person.
- When the person says something is done, or wrong, record it with
  `person.observe` and stop tracking it immediately. Say what you removed.
- You cannot create or edit items in the phone's Reminders app. Say so if
  asked, and offer a Soulacy nudge instead.
