# Soulacy on iPhone

The gateway is the brain. It runs your agents, holds their memory, and enforces
every permission. The iPhone is the extension: the place an agent asks you for
a decision when you are not at a desk, and the only device that knows where you
are, what is on your calendar, how you slept, and whether a Focus is on.

That combination is what makes the iPhone integration different from an
assistant app. Your own agents, running on hardware you control, can reach the
phone's sensors through permissions you can read in a file, and every risky
step waits for you on the lock screen.

[Connect your iPhone](getting-started/iphone.md){ .md-button .md-button--primary }
[Build a brief from your phone's signals](use-cases/phone-brief.md){ .md-button }

!!! info "Two independent versions"
    The iOS app and the gateway are versioned separately. A feature described
    here may need a newer gateway, a newer TestFlight build, or both. The app
    shows what it can and cannot do against the server it is paired with.

## What only this integration does

| Capability | What happens | Why nothing else offers it |
|---|---|---|
| **Approve on the lock screen** | A run that needs your yes appears as a Live Activity with Approve and Deny. High-risk tools ask for Face ID. | The approval is a durable object on your own gateway, not a prompt inside a cloud chat. Who approved what is recorded. |
| **Phone sensors as declared tools** | An agent that lists `location.current`, `calendar.events`, or `health.summary` in its `SOUL.yaml` can read them. One it does not list cannot. | The permission is a line you can read, review, and roll back. The phone-side toggle in **Settings → Device access** is a second, independent gate. |
| **Signals that stay on your network** | Sleep, steps, Focus state, and location feed agents and adaptive memory on a box you own, with a local model if you choose. | Cloud assistants need the data uploaded. Apple keeps it on-device but does not run your agents there. |
| **Attended and unattended are different** | A privileged step in a scheduled 3 a.m. run is refused unless the agent file says `unattended: true`. An attended run pages your phone and waits. | The rule lives in the runtime, and the phone is the signal that a human is reachable. |
| **Location as a trigger** | An agent with `trigger: location` runs when you arrive at or leave a place it declared. | The trigger is part of the agent definition, so the run inherits the same permissions and records as any other. |
| **Hands-free in the car** | "Talk to Planner in Soulacy" works with Siri, AirPods, and CarPlay. Replies are read back. Approvals always wait for the phone. | Voice is a channel into the same runtime, not a separate assistant with its own rules. |

## Everything the iPhone app does today

**Decide anywhere.** A single Inbox of time-boxed decisions: tool approvals,
failed runs, tasks waiting for review, and learning proposals. Approvals also
appear inline in chat and as a heads-up when they arrive from elsewhere.

**Chat and run agents.** Streaming conversations with attachments, a
quick-question Genie, manual **Run now** with live progress, and continuation
of a run's result into chat.

**Let agents use the phone.** Opt-in device commands, each behind an iOS
permission and a Soulacy toggle:
`device.info`, `location.current`, `contacts.search`, `calendar.events`,
`reminders.list`, `motion.current`, `health.summary`, `focus.status`,
`system.notify`, `camera.capture`, `photos.pick`, `canvas.present`, and
`canvas.snapshot`. Camera and photo requests always need a visible choice on
the phone. Only totals leave the phone for Health.

**Lock screen, widgets, and Live Activities.** Background runs show as a Live
Activity once they start using tools. Home-screen widgets show attention counts.

**Siri, Shortcuts, and CarPlay.** Talk to an agent, run an agent, remember a
fact, approve a pending action, or open the Inbox by voice or from a Shortcut.
In CarPlay, Soulacy appears as a Communication app, and replies are announced
as messages.

**Share sheet capture.** Send a document, photo, link, or text from any app to
a knowledge base or an agent. Photos are read on the phone first, so the
gateway receives text it can index.

**Native delivery.** Scheduled results land in a durable inbox on the gateway.
Push is only a wake-up, so a missing or failing push service never loses a
result. See [the Mobile channel](channels/mobile.md).

**Household pairing.** Pair a phone for another person with their own identity,
inbox, and memory, with **Can run agents** or **View only** access.

**Safety features that carry over.** Safe Undo previews and receipts, the
Learning Notebook, Verified Autopilot gates, and published-file previews all
work from the phone. Offline actions queue in an outbox; sensitive writes must
be performed online.

## Try it in ten minutes

1. [Pair the phone](getting-started/iphone.md#1-pair-from-the-web-workspace)
   with a short-lived QR code. The permanent gateway key never leaves the server.
2. [Send a test chat](getting-started/iphone.md#3-verify-a-complete-chat) and
   confirm the run in Activity on the web.
3. [Run the phone brief](use-cases/phone-brief.md): enable Health summary,
   Focus status, and Calendar events, then say "Run Phone brief in Soulacy".
4. [Schedule a morning briefing](use-cases/morning-brief.md) and verify
   generation, inbox delivery, and push separately.

## Boundaries worth knowing

- Pairing is not consent to monitoring. Device capabilities start off and are
  foreground-bound. The app accepts device commands while it is open.
- iOS never tells apps which Health data was granted, so the Soulacy toggle is
  your consent record.
- Safe Undo covers explicitly configured resources. It is not a universal undo
  for email, purchases, or shell commands.
- A reply is not proof that an external action succeeded. Check Activity or
  the run's receipt.

## Where to go next

- [Connect your iPhone](getting-started/iphone.md): pairing, permissions, Siri,
  CarPlay, device access, and offline behaviour.
- [A brief from your phone's signals](use-cases/phone-brief.md): a worked agent.
- [Morning briefing on iPhone](use-cases/morning-brief.md): scheduled delivery.
- [Soulacy Mobile channel](channels/mobile.md): delivery, APNs, and location
  triggers from the gateway side.
- [SOUL.yaml reference](agents/soul-yaml.md): declaring device tools and
  location triggers.
- [iPhone extension program](IPHONE_EXTENSION_PROGRAM.md): the build plan for
  what comes next.
