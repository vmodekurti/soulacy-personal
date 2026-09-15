# Triggers from what changed

Most agents run on a clock. A morning briefing fires whether or not
anything happened, and a person learns to ignore it.

A **person trigger** runs an agent because something about the person
actually changed: a deadline came close, they started driving, today
stopped looking like a normal Tuesday.

```yaml
trigger: person
person:
  when: commitment.due
  within: 4h        # only for commitment.due; how close counts as "soon"
  cooldown: 2h      # shortest gap between two runs for one person
```

## The conditions

| `when` | Fires when |
| --- | --- |
| `state.changed` | "Right now" becomes something else: started driving, entered a Focus, woke up. |
| `commitment.due` | A commitment crosses into `within`. Defaults to 24 hours. |
| `routine.deviation` | Today departs from the usual shape of this weekday, or departs further than it already had. |

An unrecognised condition is a **load error**, not a warning. An agent that
silently never runs is the worst outcome here, because nothing looks broken.

## Why it does not fire constantly

The obvious implementation fires far too often. Three things stop that.

- **It compares two snapshots, not writes.** An observer that rewrites an
  identical line every minute is not a change. A trigger fires on the
  difference between the model before a digest and after it.
- **A condition already true does not fire again.** A commitment that was
  inside the window last time stays quiet until something about it changes.
  Otherwise an assistant becomes an alarm clock nobody trusts.
- **A cooldown, per agent and per person.** One hour by default. A phone that
  has been offline delivers a burst of observations at once, and every one of
  them changes the model. Set `cooldown: 0s` to opt out.

Losing a state is not news either. "Right now" expiring is the absence of
information, not a change worth waking anybody for.

## What the agent is told

The inbound turn names every reason, and ends by saying that silence is
allowed:

```
__trigger:person__ A commitment is coming up: Send Priya the proposal (due
tomorrow). A commitment is coming up: Pay the water bill (overdue since 12
Sep). Decide whether this is worth telling them about, and say nothing if it
is not.
```

An agent that does not know why it was woken will greet the person instead
of answering. And an agent that replies with nothing produces no
notification: **silence is a feature**, not a failed run.

## Getting the changes at all

Triggers evaluate after an observation digest, so they need a sense switched
on under About You. Nothing is watched until the person says so, and a
sense that is off produces no changes and therefore no triggers.

See `docs/using/person-model.md` for the model itself, and
`docs/use-cases/steward.md` for the agent most worth putting on a trigger.
