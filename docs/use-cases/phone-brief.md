# A Brief From Your Phone's Signals

**Goal:** a morning brief built from things only your iPhone knows: last
night's sleep, today's calendar, whether a Focus is on. The agent reads them
through the paired phone, writes four to ten lines, and shows a native card.

**Agent:** [`examples/agents/phone-brief/SOUL.yaml`](https://github.com/vmodekurti/soulacy-personal/blob/main/examples/agents/phone-brief/SOUL.yaml).
Copy the folder into your agents directory; Soulacy hot-loads it.

## 1. Switch on what the agent may read

On the iPhone, open **Settings → Device access** and enable **Health
summary**, **Focus status**, **Calendar events** and **Soulacy canvas**.
Each asks for its system permission the first time. Nothing is advertised to
the gateway until you enable it, and the agent's `builtins` list means it
can reach nothing but the phone tools.

Device commands run only while the Soulacy app is open, so the phone is the
place to ask for this brief.

## 2. Ask for it

- In the app, open **Phone brief** and send "brief me".
- Or ask Siri: "Run Phone brief in Soulacy". The reply is read aloud, and
  the card is waiting when you pick up the phone.
- Or from CarPlay, pick **Phone brief** in the conversation list and speak.

The agent lists your paired phones, queues `health.summary` for yesterday,
`calendar.events` for today and `focus.status`, reads the results, and
writes the brief. If a command is declined it tells you which switch to
turn on rather than guessing.

## 3. What you get

A card with **Slept** and **Steps yesterday** as metrics, today's first
three events as a checklist you can tick through the day, and at most one
suggestion tied to the calendar. The same text comes back in chat, which is
what Siri reads.

Say "skip the steps line" or "I start at nine" once; adaptive memory keeps
the preference and the next brief honours it. The day's numbers are never
remembered.

## Things to try

- Turn on a Focus, ask again: the brief drops to four lines.
- Disable Health summary: the agent says so and still delivers the calendar.
- Pair a second person's phone from the web (**Pair a phone** with their
  name): their brief reads their phone and their memory, never yours.
