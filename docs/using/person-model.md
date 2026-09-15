# The person model

Adaptive memory remembers sentences. The person model is different: it is
the structured understanding Soulacy keeps of *you* — your usual day, how
you are right now, who matters, what you owe people, what you prefer — in a
shape every agent reads the same way.

It exists so an agent does not have to ask your phone twelve questions and
work out from raw samples what kind of day you are having. Small observers
digest the signals once; agents read the conclusion.

## What is in it

| Section | What it holds |
| --- | --- |
| `identity` | Name, pronouns, timezone, the places that count as home and work. |
| `routine` | The shape of a normal day, per weekday, and how far today departs from it. |
| `state` | Right now: awake, commuting, in a Focus, travelling. Short-lived on purpose. |
| `relationships` | Who matters, how you reach them, what is outstanding with each. |
| `commitments` | What you owe and are owed, with due dates. |
| `preferences` | What you like, learned from what you accepted and rejected. |

Each entry has a one-line summary, an optional structured value, a source, a
confidence and a time it was observed. Entries in `state` usually expire
within the hour: "commuting" is wrong an hour later, and a model that keeps
saying it is worse than one that says nothing.

## Who may write what

Three kinds of source, and they rank:

1. `manual` — you said it, in the app or on the web.
2. `agent:<id>` — an agent inferred it during a run.
3. `sense:<name>` — an observer digested it from a device signal.

**Higher always wins.** A location sense may not overwrite the home address
you typed; an agent may not either. When a lower source tries, the write is
refused and the caller is told what stands, so an agent can say "I thought
otherwise, but you told me X". Two observations of the same rank resolve by
recency, and a late-arriving old sample never replaces a newer one.

## Reading it as an agent

Agents opt in by listing the tools in `SOUL.yaml`:

```yaml
builtins:
  - person.model
  - person.observe
```

`person.model` answers in prose, not JSON, and can be narrowed:

```
person.model()                                  # everything
person.model(sections: ["state", "commitments"])
person.model(query: "priya proposal")
```

A reply looks like this, which is what goes into the prompt:

```
Who they are:
- Home is 14 Oak Street (they told us)

Right now:
- In a meeting until 3 (from calendar)

Open commitments:
- Owes Priya the proposal by Friday (inferred by planner)
```

The qualifiers matter: an agent that cannot tell a guess from a statement
will state guesses as fact.

### Quoting the person

A model asked to interview someone will, sooner or later, answer its own
question and record the answer as fact. The precedence rule cannot catch
that, because it arrives through the agent rather than around it. So
`person.observe` takes a `quote`, and the quote is checked against what the
person actually said in the message being answered.

- **Quote them and it is recorded as certain**, with the words kept as
  provenance. The model then reads "they told us", because they did.
- **Invent a quote and the write is refused**, with an error saying so.
- **Claim certainty with no quote and it is kept as a guess**, and the
  agent is told that is what happened.

Punctuation and case do not matter; a light paraphrase of real words
passes. A sentence nobody said does not.

`person.observe` records something the agent learned. Use a stable key so
repeated observations update rather than pile up, and be honest about
confidence — anything below 0.7 is rendered as a guess.

```
person.observe(section: "identity", key: "home", summary: "Lives in Oak Park",
               quote: "I live in Oak Park", confidence: 1)
person.observe(section: "preferences", key: "meetings",
               summary: "Prefers morning meetings", confidence: 0.6)
```

## The agent that fills it in

Soulacy ships a **Getting to Know You** agent. It reads the model, asks
only about what is missing, records each answer with a quote, and stops
after six questions. It has no tools but the person model, so it cannot
reach your files, your phone or the network: it learns by asking.

That is the honest way to start. Sensors need weeks before a routine means
anything; a short conversation on the first day gives every other agent
something to work with immediately.

## Reading and correcting it as a person

```
GET    /api/v1/person/model                          prose + entries
PUT    /api/v1/person/model/entries                  add or correct one entry
DELETE /api/v1/person/model/entries/:section/:key    remove one
GET    /api/v1/person/model/sync?since=&limit=       change feed for devices
DELETE /api/v1/person/model?confirm=true             forget everything
```

Everything is scoped to you. A household shares one gateway but never one
model; only an admin may pass `?owner=` to inspect another member, and that
is audited. A correction through `PUT` is a manual entry, so it outranks
every observer from then on. `PUT` answers **409** with the entry that
stands when a lower source tried to overwrite a higher one — not an error,
just the truth about who wins.

The change feed has the same shape as `/memory/facts/sync`, so a phone or
watch holds a local copy and catches up incrementally; `reset: true` means
the model was purged and the device should drop its copy.

## Where the sense entries come from

Observers are small digesters that turn raw device signals into model
entries. They are plain rules, not agents: "what time do they usually
leave" is a median, and a rule cannot hallucinate.

**The phone pushes; the gateway does not pull.** Device commands only run
while the Soulacy app is in the foreground, so a scheduled pull would find
an empty phone for most of the day. Region crossings and Focus changes
arrive in the background instead, which is exactly when they matter.

```
POST /api/v1/person/observations
{"observations": [
  {"kind": "arrival",  "at": "2026-09-15T08:55:00Z", "payload": {"place": "work"}},
  {"kind": "focus",    "payload": {"mode": "Work"}},
  {"kind": "sleep",    "payload": {"hours": 6.2}}
]}
```

The owner always comes from the credential, never from the body. The reply
says how many signals were recorded, how many were ignored for want of
consent, and how many model entries were applied or refused.

| Observer | Reads | Concludes |
| --- | --- | --- |
| `state` | `focus`, `motion`, `sleep` | `state/now` (driving, moving, in a Focus, settled) and `state/rest`. Expires within the hour, because a stale "commuting" is worse than silence. |
| `routine` | `arrival`, `departure` | One entry per weekday, kind and place ("Tuesday leaves home around 08:10"), plus `routine/today.deviation` when today is more than half an hour off. |

`routine` needs at least four matching days before it will call anything
usual, and ignores behaviour scattered over more than an hour. Today is
never counted into the habit it is measured against.

## Consent

Every sense is off until you switch it on, one at a time.

```
GET /api/v1/person/senses          each sense, whether it is on, and its purpose
PUT /api/v1/person/senses/state    {"enabled": true}
```

Each switch carries a plain-language purpose, because a consent control
with no stated reason is not consent. Switching a sense **off** removes
what it concluded and the raw signals it collected, not just what it would
conclude next: withdrawing consent should leave no inference behind. Signals
a switched-off sense would read are ignored on arrival, so a phone that has
not noticed yet cannot keep feeding it.

Raw signals are kept for 35 days at most, which is enough for a routine to
have an opinion and no longer.

## Turning it off

It is a store, not a behaviour: an agent that does not list the builtins
never sees it, and `DELETE /person/model?confirm=true` forgets everything.
Observers (which write the `sense:` entries) are being added slice by slice
and each one is a separate consent switch — see
`docs/PERSONAL_ASSISTANT_PROGRAM.md`.
