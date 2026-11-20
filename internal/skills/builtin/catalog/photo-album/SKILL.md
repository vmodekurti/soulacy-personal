---
name: photo-album
description: Turn the person's Google Photos into a polished album from a plain-language ask ("a cozy album of our 2025 beach trips, best shots only, with captions"). They pick candidate photos in Google's own picker; you cull blur and duplicates, choose the best, order them into sections with a title and captions, and deliver a story-style HTML page, and optionally a real Google Photos album.
license: Apache-2.0
allowed-tools: run_script install_library read_skill_file read_file write_file list_dir channel.send person.model
compatibility: Python 3. Pillow for scoring (install_library Pillow). Optional local Ollama vision model for photo descriptions.
metadata:
  builtin: "true"
  category: life
---

# Photo album

Scripts live in `scripts/`; read one with `read_skill_file` before first use
and run it with `run_script`. Every script prints one JSON object; `ok:false`
carries a `code` and an `error` you act on. Setup for the person (Google
Cloud project, OAuth client) is in the docs page *Google Photos albums*.

## What Google allows (say this plainly when it matters)

- You **cannot browse or search** the person's library. Since March 2025
  Google only lets an app read photos the person hands it through the
  **Google Photos Picker**, a Google-hosted page where *they* select items.
  So the prompt narrows the selection in two steps: the person picks
  candidates (you tell them what to look for), then you curate.
- Location, faces and Google's own labels are **not** exposed. Captions come
  from capture time, the person's words, and (if available) a local vision
  model's description. Never invent names or places.
- A real Google Photos album can only hold photos **this app uploaded**, so
  publishing re-uploads the chosen photos as new copies. They count toward
  the person's Google storage and appear twice in their library. Sharing
  the album is done by the person in the Google Photos app; the API cannot.

## 1. Turn the ask into a brief

From the prompt (and `person.model` for names and tone), settle: mood/theme
(`cozy`, `editorial`, `minimal`, `night`), what belongs (trip, dates,
people, subject), how many (best only means roughly 15 to 30; "everything
good" can be 60+), captions yes/no, and output: **page** (default), or
**page + Google Photos album**. Ask only if the subject itself is unclear.

## 2. Sign-in (the person does it)

`gphotos.py auth status`.

- `no_client`: setup is not done. Point them to the setup doc and stop.
- not signed in: `gphotos.py auth start` (add `--publish` when they want a
  Google Photos album). Send them `consent_url`. They sign in and approve
  **in their own browser**; you never see or ask for a password. Their
  browser then lands on a `127.0.0.1` address that may fail to load; they
  paste that whole address back, and you run `gphotos.py auth finish '<url>'`.
  If the gateway runs on their own computer, they can instead run
  `python3 scripts/gphotos.py auth login` in a terminal.
- `missing_scope` at publish time: run `auth start --publish` and repeat.

## 3. The person picks candidates

`gphotos.py pick start --max 300` then send `picker_uri` with specific
guidance drawn from the brief, for example: *"Search 'beach' in the picker,
scroll to summer 2025, and select generously, bursts included; I'll keep
only the best. Tap Done when finished."* Then `gphotos.py pick wait <session>`
(about 4 minutes per call; rerun while `ready:false`). If the window
closed, start a new session.

## 4. Fetch and score

1. `gphotos.py pick fetch <session> --out <workdir>` downloads 1600 px
   previews and writes `manifest.json`. Use a fresh workdir under the
   workspace, e.g. `albums/<slug>/`. Picker download links last 60 minutes,
   so fetch right after picking.
2. `curate.py score <workdir>` (add `--vision ollama` if an Ollama vision
   model runs on this machine; it never sends photos elsewhere). If it
   prints `MISSING: Pillow`, `install_library` Pillow and rerun once.

## 5. Curate and write the plan

Using the scored list:

- Drop items outside the brief's dates (`time`), and anything with
  `sharp` under 0.2 or `exp` under 0.15 unless it is the only shot of a moment.
- Keep only `best_of_group: true` from each duplicate group.
- Take the top by `q` up to the target count, then **reorder by time**.
- Split into 2 to 6 sections by day, place, or moment (time gaps of
  several hours are natural breaks). Give each a short heading and a
  one-line intro. Mark one strong photo per section `feature: true`.
- Captions: under 12 words, concrete, warm, grounded in `desc`/time/the
  person's words. No invented names or places; if unsure, leave it blank.
- Title: short and evocative; subtitle says what and when.

Write `<workdir>/plan.json`:

```json
{"title": "...", "subtitle": "...", "theme": "cozy", "cover": "<id>",
 "sections": [{"heading": "...", "intro": "...",
   "items": [{"id": "<id>", "caption": "...", "feature": true}]}],
 "publish": {"captions_as_descriptions": false}}
```

`references/plan.example.json` is a full example.

## 6. Deliver

`render.py <workdir>/plan.json <workdir>` writes `<workdir>/album.html`, a
single self-contained page (photos embedded). Tell the person the path, the
title, the section outline, how many photos you kept out of how many and
why the rest went (blur, duplicates, outside dates). Offer tweaks: swap a
photo, retitle, change theme. Re-render after each change.

## 7. Publish to Google Photos (only when asked)

1. `gphotos.py publish <plan.json> --workdir <workdir> --dry-run` and show
   the summary: album title, photo count, section headers, and the
   storage/duplicate note above.
2. Ask for a clear yes. Captions go onto the photos as descriptions only if
   the person approved them (Google asks that descriptions be the person's
   own text); otherwise leave `captions_as_descriptions` false and the
   section headers still appear as album text.
3. On yes, rerun without `--dry-run` and with `--yes`. Report `photos_created`.
4. Finally `gphotos.py pick done <session>`.

## Trying it without an account

Set `GPHOTOS_FAKE=1` in the script environment or pass `--fake` before the
subcommand (`gphotos.py --fake pick fetch x --out <dir>`): sign-in, picking,
fetching (12 synthetic beach photos, including a blurry shot, a dark shot and
a burst duplicate) and publishing (always a dry run) all work offline.

## Never

- Ask for, type, or store the person's Google password, or automate consent.
- Print or paste the token or `client_secret.json` contents.
- Publish, or re-run a publish, without an explicit yes for that album.
