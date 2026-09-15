# Soulacy as a true personal assistant — program

The vision: Soulacy builds its understanding of a person through every
available sense and manages their life for them. This document turns that
sentence into a build plan. It follows the shape of the iPhone program
(`IPHONE_EXTENSION_PROGRAM.md`): slices that ship gateway and clients
together, each usable on its own, each with tests.

## The loop

An assistant that manages a life runs four steps continuously.

| Step | What it means | Where Soulacy stands |
|---|---|---|
| **Perceive** | Every sense: phone and watch sensors, channels, documents, photos, voice. | Strongest part. Location, health, motion, Focus, calendar, contacts, camera, photos with on-device text, share sheet, Siri, CarPlay, watch; email, chat and document channels. |
| **Understand** | A persistent model of the person that no single agent owns: routines, relationships, commitments, preferences, current state. | The gap. Adaptive memory stores facts and recalls them; nothing digests signals into a model, and every agent re-derives context from scratch. |
| **Act** | Agents with tools, gated by approvals, on every surface. | Solid: tools, approvals with deadlines, canvases, notifications, Live Activities, spoken replies, workboard. |
| **Learn** | What the person accepts, rejects, corrects and ignores changes what happens next. | In pieces: memory extraction, reply feedback, learning proposals. Not yet tied to policy. |

Slice 1 builds the missing centre. Everything after it feeds or reads that
centre.

## Principles

1. **Senses feed the model, agents read the model.** No agent calls twelve
   device commands to work out what kind of day it is. Observers digest
   signals into the person model; agents ask one tool.
2. **The model is the person's, not the system's.** It is inspectable and
   editable in the app and on the web, exportable, owner-scoped, and every
   entry says which sense it came from and when.
3. **Proactive by default, silent when nothing changed.** Triggers come from
   perception, not only the clock. A day with no deviation produces no
   notification.
4. **Trust is earned per person, per kind of action.** Approvals are the
   starting point. Each decision teaches the policy engine; over time the
   assistant asks less and does more, and the person can see and reset that.
5. **Nothing leaves the gateway that the person did not attach.** On-device
   recognition (text in photos, speech) runs on the phone; the gateway keeps
   the result, never the raw stream unless asked.

## Where we start from

Already shipped: adaptive memory with a change feed and on-device cache;
device commands (`device.info`, `location.current`, `contacts.search`,
`calendar.events`, `reminders.list`, `motion.current`, `health.summary`,
`focus.status`, `camera.capture`, `photos.pick`, `canvas.present`,
`canvas.snapshot`, `system.notify`); location triggers; Live Activities;
Siri, CarPlay and Apple Watch voice loops; photo attachments with on-device
text; household identities; scheduled agents; the Phone brief template.

## Slices

### Slice 1 — The person model

The centre of the loop. A structured, owner-scoped store next to adaptive
memory, with a fixed shape so agents and observers agree on it.

| Story | Gateway | Clients |
|---|---|---|
| **P1 Model store** (shipped) | `internal/person`: SQLite store, owner-scoped, precedence manual > agent > sense, expiring entries, change feed. `GET /person/model`, `PUT /person/model/entries`, `DELETE …/entries/:section/:key`, `GET /person/model/sync?since=`, `DELETE /person/model?confirm=true`. See `docs/using/person-model.md`. | Web **About You** page: every line with its provenance, guesses marked as guesses, sense switches with their purposes, add or correct by hand, forget everything. Still to do: phone cache in the app group and an About You screen on iOS. |
| **P2 One tool** (shipped) | `person.model(sections?, query?)` answers in prose; `person.observe(...)` records what an agent learned as `agent:<id>`. Both opt-in per agent via `builtins:`, like the mobile tools. Risk tiers: read safe, observe write. | Agent templates gain `person.model` in their builtins (next). |
| **P3 Observers** (state, routine, commitments shipped) | Small scheduled digesters, not LLM agents where a rule will do: **routine** (from `location.current`, `motion.current`, `focus.status` samples: usual wake, leave, arrive, sleep windows and the deviation of today), **commitments** (from calendar, reminders, email threads with a question or a deadline), **relationships** (from contacts, calendar attendees, message senders: who, how often, what is pending), **state** (from health summary, Focus, time of day, travel). Each writes only its section and never overwrites a manual entry. | Phone answers the sampling commands in the background where iOS allows (location, Focus, health summary once a day). |
| **P4 Consent and visibility** (gateway + web shipped) | Each observer is a capability switch with a plain-language purpose string, off by default, on per sense. Switching one off deletes what it concluded and the signals behind it. | Web: the switches live on About You, each with its purpose. Still to do: the same switches during iOS onboarding and in Settings. |

**Schema (slice 1).** One document per owner, versioned; sections are
independently updatable.

```
person.model
├── identity        name, pronouns, timezone, home/work places (from location clusters, confirmed by the person)
├── routine         windows: sleep, wake, leave home, arrive work, lunch, return; per weekday; confidence; today's deviation
├── state           now: asleep|waking|commuting|working|free|travelling|unwell(?); focus mode; energy (from sleep/HR); last updated
├── relationships   [{ person, how_known, channels, last_contact, cadence, pending: [commitments to/from] }]
├── commitments     [{ what, to_whom, due, source, status: open|done|dropped, last_nudged }]
├── preferences     [{ topic, preference, evidence: [decisions], confidence }]   e.g. "prefers morning meetings", "never books middle seats"
├── decisions       ring buffer of approvals/rejections with tool, agent, args fingerprint, outcome   (feeds Slice 4)
└── meta            per-section: source(s), observed_at, expires_at, manual_overrides
```

Every row: `{value, source: "sense:location" | "sense:calendar" | "agent:planner" | "manual", observed_at, confidence 0..1, expires_at?}`.
Manual beats agent beats sense on conflict; a sense may propose a change to a
manual value but never apply it.

**Done when:** an agent given only `person.model` can answer "what does my
day look like and what am I forgetting" without calling a device command;
the person can see every entry and where it came from; turning a sense off
stops that section updating within one cycle.

### Slice 2 — Triggers from perception

| Story | Gateway | Clients |
|---|---|---|
| **P5 Event sources** | The scheduler accepts triggers on model changes: `state.now` changed, `routine.deviation > x`, `commitments.due < 2h`, `relationships[x].pending`, `location.arrived(place)`, `focus.changed`. Debounced; one run per change per agent. | Phone reports the underlying signals; nothing new in the UI. |
| **P6 Channel senses** | Email and messaging channels become perception inputs, not only chat surfaces: an observer reads new mail/messages for commitments and relationship updates (with the person's consent per channel). | Web: per-channel "let Soulacy read this for commitments" switch. |
| **P7 Quiet by design** | A run started by a trigger that finds nothing worth saying produces no notification, only an audit entry. Notification budget per day, per person, tunable. | Settings: "How often may Soulacy interrupt?" |

### Slice 3 — The life loop agent

| Story | Gateway | Clients |
|---|---|---|
| **P8 Steward template** (shipped) | One agent template, `steward`, that owns the model: on each trigger it reads the model, reconciles commitments, and proposes at most three actions as approvals or a canvas checklist. It delegates to specialist agents (Planner, Receipt Keeper, Meeting Prep) rather than doing their work. | Phone brief becomes a view of the steward's morning pass; CarPlay and watch read its short form. |
| **P9 Explain itself** (prompt-level, shipped) | Every proposal carries "because": the model rows it used. | Tap "why" on a proposal to see the rows; correcting a row re-runs the proposal. |

### Slice 4 — Trust that grows

| Story | Gateway | Clients |
|---|---|---|
| **P10 Decision learning** | The policy engine reads `person.decisions`: after N consistent approvals of the same tool+args fingerprint by this person, the tier drops one step (ask → notify-after) for that fingerprint; any rejection resets it. Visible, reversible. | Settings: "What Soulacy does without asking" list with a reset per row; the phone's approval card shows "this will stop asking after 2 more". |
| **P11 Household trust** | Trust is per person; a viewer's approvals never lower anyone's tier. | Household page shows each person's earned autonomy. |

### Slice 5 — More senses, chosen by the model's gaps

Only when the model shows a gap that a sense would fill: wearable sleep and
workouts in detail, the car (CarPlay context), the home (HomeKit scenes as
state), screen time, spending (receipts and bank feeds through a channel).
Each sense lands as an observer with a consent switch, nothing else.

## Order and sizing

Slice 1 first, about a week: store and endpoints (2 days), tool and phone
cache (1 day), the four observers (2 days), consent UI (1 day). Slice 2 and
3 together next; Slice 4 once there are enough decisions to learn from.
The killer agents already proposed (Receipt Keeper, Doorstep, Meeting Prep)
become the first specialists the steward delegates to.
