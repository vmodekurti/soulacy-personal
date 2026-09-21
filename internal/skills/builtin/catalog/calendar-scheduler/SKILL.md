---
name: calendar-scheduler
description: Find times that work, spot conflicts, and draft invites — "when am I free", "find 30 minutes with Priya next week", "does this clash", "move my Thursday" — using the phone's calendar and the person's habits (no early mornings, buffer after meetings, home vs office days).
license: Apache-2.0
allowed-tools: mobile.invoke mobile.command_status mobile.list_nodes person.model person.observe semantic_memory_search channel.send
metadata:
  builtin: "true"
  category: life
---

# Calendar & scheduling

## Read the calendar

`mobile.invoke` `calendar.events` with the date range you need (ask for a
little more than the window — the day before and after — to see travel and
buffers), then `mobile.command_status` for the result. If no phone is
paired or the command is not enabled, say exactly that and stop; never
guess a schedule.

## Know the person's rules

From `person.model`: working hours, time zone, days at the office, "no
meetings before 10", lunch, buffer they like between meetings, focus blocks,
family pickups. Treat these as hard constraints unless they override them in
the ask. If a rule is missing and matters (working hours are the usual
one), assume 09:00–17:30 local and say so in one line.

## Find a slot

1. Build the free windows in the requested range after removing events,
   buffers (15 min default, or theirs), travel time when two events have
   different places, and the rules above.
2. Prefer: the person's stated preference → mid-morning/early afternoon →
   adjacent to existing meetings (fewer fragments) → not right after a long
   block.
3. Offer **three** options unless they asked for one, each as
   `Tue 23 Sep 14:00–14:30 (Chicago)`; when the other party is in another
   zone, show both zones on the same line.
4. If nothing fits, say why (which rule or event blocks it) and offer the
   nearest options that break the softest rule.

## Conflicts and moves

- "Does this clash?" → list overlapping events with times; note back-to-back
  travel that makes it impractical even without overlap.
- "Move X" → propose the new slot(s), list who else is on the event, and
  ask before anything is sent; you cannot edit the calendar yourself — draft
  the change for the person or the organiser.

## Draft the invite

Title (what, with whom), time in both zones, place or link, a one-line
agenda, and who is optional. Deliver as text via `channel.send` or in chat.
When the person picks an option, record any new preference you learned with
`person.observe` ("prefers afternoons for externals").
