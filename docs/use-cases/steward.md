# The Steward

The agent that looks after your day. It reads what Soulacy understands
about you, works out what is different about today, and proposes at most
three things worth doing.

It is the piece that turns a set of capable tools into an assistant. Every
other agent waits to be asked. This one notices.

## What it actually does

1. Reads the person model. That is its whole picture of you, so it never
   asks your phone a dozen questions to rebuild context it already has.
2. Decides what is different about today. Today's business is narrow on
   purpose: a commitment counts only if it is overdue, due today, or due
   tomorrow. Your usual start time is not news unless today departs from it.
3. Picks at most three things, preferring what has a deadline, then what
   leaves another person waiting, then what is easy to lose track of.
4. Says why, naming what it read. "Priya has been waiting since Friday, and
   you told us she is your manager."
5. Hands specialist work to the agent that does it best rather than doing
   everything itself.

**A quiet day produces a quiet answer.** "Nothing needs you today" is a
complete reply, and the agent is told to prefer it. An assistant that finds
something urgent every single morning is one you stop reading.

## What it will not do

- It will not brief you on an empty model. If Soulacy does not know you
  yet, it says so and points at the Getting to Know You agent instead of
  guessing its way to something that sounds useful.
- It will not state a guess as a fact. The model labels every line with
  where it came from, and the steward is required to carry that through:
  something you said is stated plainly, something a sensor inferred is
  marked as an inference.
- It will not invent a commitment, a person or a deadline that is not in
  the model.

## Setting it up

The steward is installed on a new gateway automatically, alongside Getting
to Know You. On an existing one, install it from Templates.

It needs material. On day one that comes from a few minutes with Getting to
Know You; after that the observers keep it current, if you have switched
them on under About You:

| Sense | Gives the steward |
| --- | --- |
| `commitments` | What you are on the hook for, from your reminders |
| `state` | Whether you are driving, in a Focus, or short of sleep |
| `routine` | Whether today departs from your usual shape |

Run it from chat, from Siri ("Run Steward in Soulacy"), or on a schedule.
A morning schedule is the obvious one, but it is more useful on a trigger:
see `docs/PERSONAL_ASSISTANT_PROGRAM.md` for perception triggers.

## A note on models

The steward's value is in judgement: what matters, what to leave out, how
to say it. That asks more of a model than most agents do. On a small local
model it will still find the right things but can be clumsy about
attribution and length. Give it your best available model.
