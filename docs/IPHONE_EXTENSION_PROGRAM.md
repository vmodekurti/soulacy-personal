# iPhone as the extension of Soulacy — program

The gateway is the brain: it runs agents, holds memory, enforces every gate.
The iPhone is the extension: the hands, the eyes, the inbox, and the place a
decision happens when you are not at a desk. This document is the build plan
for making that the best iPhone integration of any personal agent framework.

It is written to be executed slice by slice. Each slice ships gateway and app
together, with tests on both sides, and is usable on its own.

## Principles

1. **Decisions with deadlines are the unit.** Approvals, failed runs, reviews,
   learning proposals. Every new surface (lock screen, watch, Siri) renders the
   same Inbox object.
2. **The phone changes what agents know, not just how you talk to them.**
   Location, calendar, health, photos and motion are signals only a phone has.
   They flow into adaptive memory and into triggers.
3. **Nothing risky happens on the phone either.** Every phone capability an
   agent can touch is an explicitly enabled command with a receipt, and
   approvals on the lock screen use the same broker as the web.
4. **The gateway stays the source of truth.** Push is a wake-up; the phone
   fetches the durable state. Offline actions queue in the outbox.

## Where we start from

Already shipped: Inbox of decisions, chat with streaming, native push for
deliveries, pairing with a scoped credential, share extension, home-screen
widget, offline outbox, Safe Undo, Learning notebook, Published files, and a
device-node command set agents can call: `device.info`, `location.current`,
`contacts.search`, `calendar.events`, `reminders.list`, `motion.current`,
`system.notify`, `camera.capture`, `photos.pick`, `canvas.present`,
`canvas.snapshot`.

## Slices

### Slice 1 — Decide anywhere, agents that know where you are

| Story | Gateway | App |
|---|---|---|
| **E50 Lock-screen approvals** | Approval broker fans out an APNs push with an interactive category (`SOULACY_APPROVAL`), thread id, and the call id. Time-sensitive interruption level. | Notification category with **Approve** / **Deny** actions (authentication required). Actions call the approvals API in the background; the app opens the approval on tap. |
| **E51 Location triggers** | New trigger kind `location` on an agent: region (lat, lon, radius), `on: enter\|exit`, optional device. `GET /mobile/triggers` lists regions a paired phone should monitor; `POST /mobile/triggers/:agent/fire` runs the agent with a `location` trigger and delivers the result to the phone. | Region monitoring via Core Location for every listed trigger, fired in the background, with a receipt shown in Activity. |
| **E52 Siri & Shortcuts** | None beyond the chat and memory APIs. | App Intents: **Ask an agent**, **Remember this**, **Approve the pending action**, **Run an agent**. Shortcut phrases so agents work from Siri, the Action button, Focus automations and CarPlay. |
| **E53 Memory on the phone** | Existing `/memory/facts` API. | **What it remembers** screen: search, edit, delete, add, history; share-sheet **Remember this**. |

### Slice 2 — The phone as the agent's senses

| Story | Gateway | App |
|---|---|---|
| **E54 Live Activities** (shipped) | Push-to-start token on device registration; `POST /mobile/activities` registers per-run update tokens; the event hub drives start/update/end with `apns-push-type: liveactivity`. Background runs start on their first tool call, any run starts when an approval is pending. | Lock-screen and Dynamic Island activity with elapsed time, steps, and Approve/Deny buttons backed by a `LiveActivityIntent` that decides without opening the app. Settings → Lock screen toggle. |
| **E55 Health & context signals** (shipped) | Node commands `health.summary` (steps, active energy, distance, exercise minutes, last night's sleep, workouts for a bounded window) and `focus.status`, allow-listed and gated by the phone's advertised capabilities like every other device command. | HealthKit read with an explicit **Health summary** capability toggle (iOS never reveals read grants, so the toggle is the consent record); **Focus status** capability via `INFocusStatusCenter`. Foreground-only, like all device commands. |
| **E56 Capture into knowledge** (shipped) | Knowledge ingest accepts a `caption` and, for files the gateway cannot read (photos, scans), `extracted_text` produced on the phone, ingested in place of the bytes. | Share sheet is real: pick a knowledge base or an agent, add a caption, and send files, photos (with on-device text recognition), links and text. Profiles live in the app group so the extension shares the paired credential. |
| **E57 Voice in hand** (shipped) | Existing voice pipeline and chat API. | Push-to-talk in chat. CarPlay Communication app (entitlement granted by Apple on 2026-09-14, Case-ID 22213896): the car shows the user's agents as conversations; Siri's messaging intents send a question by voice and the agent's reply comes back as a communication notification Siri reads aloud. Approvals are never offered in the car. |

### Slice 3 — First-class node

| Story | Gateway | App |
|---|---|---|
| **E58 Watch** (shipped) | None new: the phone mints a pairing code with `POST /pairing/tokens` (same subject) and the watch redeems it, so the watch holds its own managed key. | A watchOS app inside the iPhone build: a complication with the pending-approval count and oldest deadline (or the last spoken reply), an Ask screen that dictates a question and reads the reply aloud, and an approvals list with Approve and Deny. Pairs once through WatchConnectivity, then talks to the gateway directly. Snooze was dropped: approvals expire on their own and the complication shows the countdown. |
| **E59 On-device memory** (shipped) | `GET /memory/facts/sync?since=` change feed over the fact history: upserts and tombstones with a cursor, owner-scoped, `reset` after a purge, 501 for providers without history. | The phone keeps a local copy of the owner's facts, syncs incrementally on connect and reconnect, searches offline with on-device sentence embeddings, and queues additions (including Siri's Remember) until the gateway is reachable. |
| **E60 Household** (shipped) | Managed keys carry a subject and role; pairing tokens carry who they are for; `POST /pairing/tokens {name, role}` (admin) pairs a phone as another person, `GET /pairing/members` lists the household; legacy companion keys map to the owner. Fixes the pre-existing gap where a paired phone authenticated as its key id rather than the owner. | Pairing names the profile after the person; several profiles on one phone already switch identities. Web Mobile page gains name/role pairing and a Household list with Unpair. |
| **E61 Agent-built UI** (shipped) | `canvas.present` accepts typed `components` (text, checklist, form, chart, metric), validated on enqueue; `mobile.invoke` takes `expires_in_seconds`; a form keeps the command running until the person answers and the result carries the answers. | Native rendering with Swift Charts and form controls; the card appears as a sheet wherever the person is; Submit and Dismiss report back through the normal command result path. |

## Non-goals

- Running agents continuously on the phone. iOS does not allow it, and the
  gateway model is the point.
- A shrunk-down copy of the desktop authoring UI on the phone.

## Identity note

A paired phone holds a managed credential named `mobile-companion` (with the
person's name appended for household members). Adaptive memory, approvals
and triggers are all keyed by the credential's subject. Since Slice 3 the
key stores that subject explicitly and it is the person the code was
minted for; the owner's own phone is `admin`, the same identity as the web
login. Companion keys minted before that carried no subject and are mapped
to the owner. Tests in `household_test.go` and `mobile_triggers_test.go`
pin both halves so web and phone never split a person's memory in two.
