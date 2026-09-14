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
| **E56 Capture into knowledge** | Share-target endpoints already exist for facts; add a knowledge-base ingest path for shared files and photos with captions. | Share sheet: **File this** to a knowledge base; camera capture flows into extraction (the honest multimodal path). |
| **E57 Voice in hand** | Existing voice pipeline. | Push-to-talk; CarPlay messaging integration for hands-free agent replies. |

### Slice 3 — First-class node

| Story | Gateway | App |
|---|---|---|
| **E58 Watch** | None. | Inbox on the wrist: approve, deny, snooze; complications for pending count. |
| **E59 On-device memory** | Fact sync API with cursors. | Extraction and recall with on-device models when offline; sync on reconnect. |
| **E60 Household** | Multiple owners per gateway with owner-scoped memory and inboxes (already the tenancy model). | Multiple profiles on one phone; per-person pairing. |
| **E61 Agent-built UI** | Canvas command already exists; add typed components. | Checklists, forms and charts rendered natively from an agent's canvas. |

## Non-goals

- Running agents continuously on the phone. iOS does not allow it, and the
  gateway model is the point.
- A shrunk-down copy of the desktop authoring UI on the phone.

## Identity note

A paired phone holds a managed credential named `mobile-companion`. Adaptive
memory, approvals and triggers are all keyed by the credential's subject, which
in Personal resolves to the same owner as the web login. Slice 1 pins this with
a test so web and phone never split a person's memory in two.
