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
| `find-skills` | find, vet (audits, source, license, fit) and, on your yes, install a skill Soulacy lacks |
| `skill-creator` | turn a repeated workflow into a skill with an `eval.md`, test it, and tune its description |
| `grill-me` | stress-test a plan in rounds of questions, each with a recommended answer |
| `teach` | a multi-session course: mission, sourced lessons, retrieval practice and spaced reviews |
| `weather` | current conditions and forecasts with no API key, answered as a decision |
| `community-pulse` | what people said in the last 30 days on Hacker News, Reddit, GitHub and the web, with dated quotes |
| `humanizer` | remove AI writing patterns from a draft while keeping every fact |
| `photo-album` | a curated, captioned album from photos you pick in Google Photos: a story-style HTML page, optionally a real Google Photos album ([setup](photo-album.md)) |

Every skill has a `references/eval.md` with a handful of prompts and the
expected behaviour, so it can be checked by hand (and, later, automatically).

## Third-party credits

Some built-in skills are adapted from popular open-source skills. Each of
these folders keeps the upstream license as `LICENSE.upstream.txt` and
lists its changes in a **Credits** section at the end of `SKILL.md`.

| Skill | Based on | License |
|---|---|---|
| `find-skills` | [vercel-labs/skills](https://github.com/vercel-labs/skills) `find-skills`, Vercel, Inc. | MIT |
| `skill-creator` | [anthropics/skills](https://github.com/anthropics/skills) `skill-creator`, Anthropic | Apache-2.0 |
| `grill-me` | [mattpocock/skills](https://github.com/mattpocock/skills) `grilling`, Matt Pocock | MIT |
| `teach` | [mattpocock/skills](https://github.com/mattpocock/skills) `teach`, Matt Pocock | MIT |
| `weather` | [openclaw/openclaw](https://github.com/openclaw/openclaw) `weather`, OpenClaw Foundation | MIT |
| `humanizer` | [blader/humanizer](https://github.com/blader/humanizer), Siqi Chen | MIT |
| `community-pulse` | inspired by [mvanhorn/last30days-skill](https://github.com/mvanhorn/last30days-skill), Matt Van Horn (no code or text reused) | MIT |

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
