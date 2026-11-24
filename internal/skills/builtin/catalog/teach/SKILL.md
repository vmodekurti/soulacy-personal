---
name: teach
description: Teach the person a new skill or subject over many sessions — grounded in why they want it, built from trusted sources, delivered as short lessons with retrieval practice, and reviewed on a spaced schedule so it sticks. Use for "teach me…", "I want to learn…", "help me get better at…", "quiz me on…", and for continuing an existing course.
license: MIT
allowed-tools: web_search fetch_url read_file write_file list_dir find_files create_monitor list_monitors person.model person.observe channel.send
metadata:
  builtin: "true"
  category: learning
  upstream: "https://github.com/mattpocock/skills/tree/main/skills/productivity/teach"
  upstream-license: "MIT"
---

# Teach

Learning a topic properly takes several sessions. Keep the course in files
so each session picks up where the last one stopped.

## The course folder

One folder per topic inside the workspace, `learning/<topic>/`:

| File | Holds |
|---|---|
| `MISSION.md` | why they are learning this; every lesson traces back to it |
| `RESOURCES.md` | the trusted sources found so far, with a line on what each is good for |
| `records/NNNN-<slug>.md` | what the person has *shown* they know (numbered, one short paragraph each) |
| `lessons/NNNN-<slug>.md` | the lessons, numbered in order |
| `reference/<name>.md` | cheat sheets and a glossary distilled from the lessons |
| `NOTES.md` | how they like to be taught |

`list_dir` the folder at the start of every session and read
`MISSION.md`, the latest records and `NOTES.md` before teaching.

## Start with the mission

If there is no `MISSION.md`, the first job is to ask why. Push past
abstract answers ("to understand wine") to the real outcome ("pick a good
bottle for dinner parties without the menu panic"). Write:

```
# Mission: <topic>
## Why            1–3 sentences, the concrete outcome
## Success looks like   observable things they will be able to do
## Constraints    time, budget, preferences
## Out of scope   what they do not want to chase now
```

Keep it to one screen. When the goal changes, confirm with the person,
update it, and write a record saying why.

## Find knowledge before teaching it

Until `RESOURCES.md` has a few good entries, your main job is finding
high-trust sources with `web_search` and `fetch_url`: official docs,
respected books and courses, recognised experts. Do not teach from memory
alone; cite a source for every claim in a lesson.

## Lessons

Each lesson teaches **one** tightly scoped thing tied to the mission, at
the edge of what the records show they know: challenging, but only just.

- Short enough to finish in ten minutes and readable on a phone.
- Knowledge first, kept as easy as possible; then practice, where effort
  is the point.
- Practice is **retrieval**: questions they answer from memory, or real
  steps to do and report back. Give feedback immediately.
- In multiple-choice questions, make every option the same length and
  vary where the right answer sits; no clues from formatting or order.
- End with the single best source to read or watch next and a reminder
  that they can ask you anything.
- Save it as `lessons/NNNN-<slug>.md` and deliver it with `channel.send`
  when they are not in chat. Add any reusable fact to `reference/`.

## Records: only what is shown

Write a record when they **demonstrate** understanding, state prior
knowledge ("I already know X"), have a misconception corrected, or the
mission changes. Covering material is not learning; wait for evidence.
When a later record contradicts an earlier one, mark the old one
superseded instead of deleting it.

## Spaced review

Fluency today is not memory next month. After a lesson, schedule short
reviews with `create_monitor` one-shot `at` times: about 1, 3, 7 and 21
days later, at an hour `person.model` says suits them. Each review asks
three retrieval questions from earlier lessons, mixing topics. Check
`list_monitors` first so reviews do not pile up, and stop scheduling when
they ask you to.

## Wisdom comes from people

For questions that need real-world judgement, give your best answer and
point to a reputable community (a forum, a local class, a club) where they
can test it. If they do not want communities, respect that and record it
with `person.observe`.

## Credits

Adapted from `teach` by Matt Pocock, MIT License (see
`LICENSE.upstream.txt`). Changes: Markdown lessons readable on a phone
instead of HTML pages and shared assets, a `learning/<topic>/` course
folder, spaced reviews scheduled with Soulacy monitors, and delivery
through Soulacy channels.
