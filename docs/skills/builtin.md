# Built-in skills

Soulacy ships a small catalog of Agent Skills inside the binary. On startup
they are written to `<workspace>/skills/<name>/` — ordinary skill folders you
can read, edit, or delete like any other. Each carries a `.soulacy-builtin`
marker, and the Skills page shows them with a **built-in** badge.

| Skill | What it is for |
|---|---|
| `documents` | read, extract, fill, create and edit PDF, Word, Excel and PowerPoint files (bundled scripts) |
| `web-research` | search, read and answer with real, checked citations |
| `summarize` | thread, transcript, article or file → a brief sized to the ask, with decisions and action items |
| `writing-in-your-voice` | drafts and rewrites that sound like you, using the person model |
| `daily-brief` | calendar, weather, what needs a reply, commitments and headlines — one phone message |
| `inbox-triage` | needs-you / FYI / noise, deadlines and commitments, replies drafted in your voice |
| `calendar-scheduler` | free slots, conflicts, invites — honouring your working hours and habits |
| `memory-curator` | see what Soulacy believes about you, where it came from, correct or forget it |

Every skill has a `references/eval.md` with a handful of prompts and the
expected behaviour, so it can be checked by hand (and, later, automatically).

## Upgrades never clobber your edits

The seeder keeps a manifest (`<workspace>/skills/.builtin-manifest.json`) of
what it wrote. On each start:

- a skill that is **absent**, or **exactly what Soulacy wrote last time**, is
  (re)written — so a new release refreshes its own copies;
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
