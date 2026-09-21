---
name: daily-brief
description: Compose the morning (or evening) brief — today's calendar, weather where the person is, what needs a reply, commitments due, and a few headlines they care about — in one consistent, scannable message for the phone. Use for scheduled briefings and "what's my day look like".
license: Apache-2.0
allowed-tools: mobile.invoke mobile.command_status mobile.list_nodes person.model web_search fetch_url semantic_memory_search session_search channel.send
metadata:
  builtin: "true"
  category: life
---

# Daily brief

One message the person can read in thirty seconds while making coffee.
Same order every day, so their eye knows where to look.

## Gather (skip any source that is unavailable; never block on it)

1. **Where and when**: `mobile.invoke` `location.current` for the city (only
   if the phone grants it; otherwise use the home location in `person.model`).
   Use the person's time zone for everything.
2. **Calendar**: `mobile.invoke` `calendar.events` for today (and tomorrow if
   it is an evening brief). Read the result with `mobile.command_status`.
3. **Weather**: `web_search`/`fetch_url` a weather source for that city —
   high/low, precipitation chance, one word for the sky.
4. **Needs a reply**: anything the inbox-triage skill flagged, or messages
   in `session_search` addressed to the person with a question and no answer.
5. **Commitments**: things the person promised that fall due today or
   tomorrow (`semantic_memory_search` for "I'll", "by Friday", reminders via
   `reminders.list`).
6. **Headlines**: 3 max, only on topics `person.model` says they follow. No
   general news unless they asked for it.

## Format (Markdown; phone-width; no tables)

```
☀️ Tue 23 Sep · Chicago · 71°/58°, dry until evening

📅 Today
• 09:30 Standup (30m)
• 12:00 Lunch w/ Priya — Lula Cafe
• 15:00 Dentist — leave by 14:30

✉️ Needs you
• Mark: contract redlines — asked twice
• Dana: confirm Friday? (quick yes/no)

⏰ Due
• Send Q3 numbers to Sam (promised Mon)

📰 3 things
• …
```

- Times in the person's zone, 24h or 12h as they use it.
- "Leave by" lines only when the calendar has a location and travel time is knowable.
- Empty section → drop it. Nothing due and nothing to reply → say "Inbox clear" once, not a blank list.
- Total under ~120 words unless the day is genuinely packed.

## Deliver

Send with `channel.send` to the mobile channel (or the channel the schedule
names). If a source failed, add one line at the end: "Couldn't reach the
calendar this morning." Never fabricate an event or a forecast.
