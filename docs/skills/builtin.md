# Built-in skills

Soulacy ships a small catalog of Agent Skills inside the binary. On startup
they are written to `<workspace>/skills/<name>/`: ordinary skill folders you
can read, edit, or delete like any other. Each carries a `.soulacy-builtin`
marker, and the Skills page shows them with a **built-in** badge.

| Skill | What it is for |
|---|---|
| `documents` | read, extract, fill, create and edit PDF, Word, Excel and PowerPoint files (bundled scripts) |
| `web-research` | search, read and answer with real, checked citations |
| `summarize` | thread, transcript, article or file → a brief sized to the ask, with decisions and action items |
| `writing-in-your-voice` | drafts and rewrites that sound like you, using the person model |
| `daily-brief` | calendar, weather, what needs a reply, commitments and headlines: one phone message |
| `inbox-triage` | needs-you / FYI / noise, deadlines and commitments, replies drafted in your voice |
| `calendar-scheduler` | free slots, conflicts, invites: honouring your working hours and habits |
| `memory-curator` | see what Soulacy believes about you, where it came from, correct or forget it |
| `shopping-research` | compare products across connected shopping services and hand checkout to the person |
| `event-finder` | find and compare events through connected event services, then open provider ticket selection |
| `food-product-check` | explain barcode, ingredient, nutrition, allergen and label data with uncertainty |
| `reminders-and-commitments` | what you owe and are owed, deadlines and follow-ups, with one-time nudges outside quiet hours |
| `watch-for-changes` | "tell me when": a background monitor for prices, stock, page sections, status pages or new results |
| `travel-planner` | options, itinerary, official entry requirements and day-of reminders; booking stays with the provider |
| `money-check-in` | read-only spending, subscriptions and unusual charges from a finance adapter or exported statement |
| `meeting-prep` | who you are meeting, where things were left, what is owed, and what to ask |
| `photo-album` | a curated, captioned album from photos you pick in Google Photos: a story-style HTML page, optionally a real Google Photos album ([setup](photo-album.md)) |

Every skill has a `references/eval.md` with a handful of prompts and the
expected behaviour, so it can be checked by hand (and, later, automatically).

## Upgrades never clobber your edits

The seeder keeps a manifest (`<workspace>/skills/.builtin-manifest.json`) of
what it wrote. On each start:

- a skill that is **absent**, or **exactly what Soulacy wrote last time**, is
  written again, so a new release refreshes its own copies;
- a skill you have **edited** (any file differs from the manifest) is left
  alone, forever, with a log line;
- a directory you **created yourself** with a catalog name (no marker) is
  never touched.

To take a refreshed copy after editing, delete the folder and restart.

## Turning it off

```yaml
builtin_skills: false
```

in `config.yaml` ships nothing. Already-seeded folders stay until you delete
them.
