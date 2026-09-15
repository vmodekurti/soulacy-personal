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

`person.observe` records something the agent learned. Use a stable key so
repeated observations update rather than pile up, and be honest about
confidence — anything below 0.7 is rendered as a guess.

```
person.observe(section: "preferences", key: "meetings",
               summary: "Prefers morning meetings", confidence: 0.8)
```

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

## Turning it off

It is a store, not a behaviour: an agent that does not list the builtins
never sees it, and `DELETE /person/model?confirm=true` forgets everything.
Observers (which write the `sense:` entries) are being added slice by slice
and each one is a separate consent switch — see
`docs/PERSONAL_ASSISTANT_PROGRAM.md`.
