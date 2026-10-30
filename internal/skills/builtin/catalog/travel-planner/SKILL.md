---
name: travel-planner
description: Plan and run a trip — compare options, build a day-by-day itinerary around the person's habits, gather confirmations into one place, check entry and travel requirements from official sources, and handle day-of logistics such as leave-by times, weather and check-in reminders. Use for "plan a trip to…", "what's my itinerary", "when should I leave for the airport", "what do I need for…". Booking and payment stay with the provider.
license: Apache-2.0
allowed-tools: web_search fetch_url mobile.invoke mobile.command_status mobile.list_nodes person.model person.observe semantic_memory_search session_search read_file write_file create_monitor channel.send
metadata:
  builtin: "true"
  category: life
---

# Travel planner

The person should always know where they need to be next, by when, and
what they need with them — without digging through five confirmation
emails.

## Frame the trip

Pin down: destination(s), dates (or a range and length), who is going,
budget band, and the purpose (work, family, rest). Pull the rest from
`person.model`: home airport or station, seat and airline habits, how early
they like to arrive, dietary needs, mobility needs, loyalty programmes.
Ask only for what is missing **and** changes the plan; one question at a
time.

## Compare options

Use `web_search` and `fetch_url` on provider and official pages. For each
option give total door-to-door time, price **as shown on the page with the
date you saw it**, changes or connections, and the catch (red-eye, tight
connection, non-refundable). Offer two or three, mark the one that best
fits their habits, and link each. Prices move: never present a price you
did not see in this run.

Never book, hold, or enter payment details. When they choose, hand over the
provider link and what to select.

## Entry and travel requirements

Passports, visas, eVisas/ETAs, vaccinations, and customs rules come **only**
from official government or carrier pages fetched in this run, cited with
the page title and the date checked. If official pages disagree or are
unclear, say so and tell the person who to confirm with. Never answer
these from memory.

## Build the itinerary

One plan, in the person's time zone and the local zone where they differ:

```
Thu 14 Nov
  06:10  Leave home → ORD (car, ~45 min with traffic)
  08:05  UA 924 ORD → LHR · seat 31K · conf ABC123
  21:20  Arrive LHR (local) · Elizabeth line to Paddington
  22:30  Check in: The Pilgrm, Paddington · conf 88812

Fri 15 Nov
  09:30  Client meeting, 1 King's Cross
```

- Gather confirmations from what the person shares (`read_file`, pasted
  emails) and past conversations (`session_search`). Record source for every
  booking line; mark anything unconfirmed as *planned, not booked*.
- Leave buffers the person prefers; flag connections under their minimum.
- Save the itinerary with `write_file` (`trips/<destination>-<dates>.md`) so
  it can be reopened and updated.

## Day-of logistics

- Before travel days, `create_monitor` one-shot checks (with `at`) for:
  online check-in opening, a leave-by reminder based on the route and their
  arrival habit, and the local weather the evening before. State each
  reminder you set.
- On the day, `mobile.invoke` `location.current` (then
  `mobile.command_status`) when asked "when should I leave?"; if no phone is
  paired, use the home or hotel address from the plan and say so.
- Calendar clashes: `mobile.invoke` `calendar.events` for the trip dates and
  list anything that overlaps.

## Rules

- Learned preferences ("always aisle", "no connections under 90 min")
  are recorded with `person.observe`.
- `channel.send` delivers plans and reminders to the person; never message
  hotels, airlines or other people on their behalf.
- Keep passport numbers and other ID details out of messages and files;
  refer to "your passport" instead.
