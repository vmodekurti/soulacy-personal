# Google Photos albums

The built-in `photo-album` skill turns a plain-language ask into a finished
album:

> "A cozy album of our 2025 beach trips, best shots only, with captions."

You pick candidate photos in Google's own picker. The agent then drops
blurry, dark and duplicate shots, keeps the best, orders them into sections
with a title, headings and captions, and gives you a story-style HTML page.
If you ask, it also creates a real album in Google Photos.

## What Google allows

Google changed the Photos APIs on 31 March 2025, and the skill is built
around the rules that apply now:

| You might expect | What is actually possible |
|---|---|
| The agent searches your whole library | **Not allowed.** Apps can only see photos you select in the [Google Photos Picker](https://developers.google.com/photos/picker), a Google page where you do the choosing. The agent tells you what to look for; you select generously; it curates. |
| It reads places, faces or Google's labels | **Not exposed.** The picker gives capture time, size, camera and the image. Captions come from your words, the time, and an optional local vision model. |
| It adds your existing photos to an album | **Not allowed.** An app can only put photos it uploaded itself into an album it created. Publishing therefore uploads copies: they use your Google storage and show up twice in your library. |
| It shares the album for you | **Removed** from the API. Share it from the Google Photos app. |

That is why the HTML page is the default output: it needs only read access,
creates no copies, and works offline. Publishing to Google Photos is an
extra step you approve each time.

## How a run goes

1. **Sign in.** The agent sends you a Google consent link. You sign in and
   approve in your own browser; Soulacy never sees your password.
2. **Pick.** It sends a picker link with tips drawn from your ask
   (for example "search *beach*, scroll to summer 2025, include bursts").
   You select and tap **Done**.
3. **Curate.** It downloads 1600 px previews, scores sharpness and exposure,
   groups near duplicates and keeps the best of each, then filters to your
   dates and target count.
4. **Lay out.** Photos are put in time order, split into a few sections,
   and given a title, headings and short captions.
5. **Deliver.** You get `album.html`, a single file with every photo
   embedded, in one of four looks: `cozy`, `editorial`, `minimal`, `night`.
   Ask for changes ("swap the third photo", "make it darker") and it
   re-renders.
6. **Publish (optional).** It shows what will be uploaded and asks first.

## One-time setup

You need your own Google Cloud project and OAuth client. This takes about
ten minutes and is free.

### 1. Create a project and enable the APIs

1. Open the [Google Cloud console](https://console.cloud.google.com/) and
   create a project, for example `soulacy-photos`.
2. Under **APIs & Services > Library**, enable **Photos Picker API**.
3. If you want real Google Photos albums too, also enable
   **Photos Library API**.

### 2. Configure the consent screen

1. **APIs & Services > OAuth consent screen** (shown as **Google Auth
   Platform** in newer consoles). Choose **External**.
2. Fill in the app name and your email. Leave the app in **Testing**.
3. Under **Audience** (or **Test users**), add the Google account whose
   photos you will use.
4. Under **Data access** (or **Scopes**), add the scopes from the table below.

!!! note "Testing apps re-ask every week"
    While an app is in Testing, Google expires its refresh tokens after
    7 days, so you will be asked to sign in again about weekly. Publishing
    the app removes that but needs Google's verification, which is not
    worth it for personal use.

### 3. Create the OAuth client

1. **APIs & Services > Credentials > Create credentials > OAuth client ID**.
2. Application type: **Desktop app**. Name it anything.
3. Download the JSON.

### 4. Put the client file where the skill looks

```bash
mkdir -p ~/.soulacy/credentials/google-photos
mv ~/Downloads/client_secret_*.json ~/.soulacy/credentials/google-photos/client_secret.json
chmod 700 ~/.soulacy/credentials/google-photos
chmod 600 ~/.soulacy/credentials/google-photos/client_secret.json
```

The skill looks for its credentials directory in this order:

| Location | When it applies |
|---|---|
| `$GOOGLE_PHOTOS_CREDS_DIR` | set it when you run the scripts by hand |
| `$SOULACY_WORKSPACE/credentials/google-photos` | the default inside the gateway, so it survives restarts on a persistent volume |
| `~/.soulacy/credentials/google-photos` | fallback when neither is set |

On a gateway, put the file in the workspace location instead of your home
directory. The token the skill saves after sign-in (`token.json`) goes in
the same directory, mode `0600`.

!!! warning "Never commit these files"
    `client_secret.json` and `token.json` grant access to your photos. Keep
    them out of any git checkout. The skill never prints them, and it never
    passes them to the model.

!!! info "Why not the credential vault?"
    Skill scripts run through `run_script` with a filtered environment
    (`PATH`, `HOME`, `LANG`, `TMPDIR` and the `SOULACY_*` path hints) and no
    access to the vault, by design. A private, `0600` file outside the
    repository is the safest place a script can read from today.

### Scopes

| Scope | Used for | Requested |
|---|---|---|
| `photospicker.mediaitems.readonly` | creating a picker session and reading the photos you picked | always |
| `photoslibrary.appendonly` | creating an album, uploading photos, adding headings | only when you ask to publish |

The skill asks for the second scope only when you first want a Google
Photos album, so a page-only run never holds write access.

### Signing in from a remote gateway

Consent uses a loopback redirect (`http://127.0.0.1:8765/`). If the gateway
runs on another machine, your browser lands on that address after you
approve and shows a "can't connect" page. That is expected: copy the full
address from the address bar and paste it back to the agent. It is a
one-time code that only works with your client file and this sign-in.

If the gateway runs on the same computer as your browser, you can sign in
from a terminal instead:

```bash
python3 "$SOULACY_WORKSPACE/skills/photo-album/scripts/gphotos.py" auth login
```

## Optional: photo descriptions with a local model

Captions are better when the agent knows what is in each photo. If
[Ollama](https://ollama.com/) runs on the same machine with a vision model
(`ollama pull llava`), the agent adds `--vision ollama` when scoring and
each photo gets a one-line description. The scorer only talks to
`localhost`, so photos never leave the machine.

## Trying it without an account

Fake mode serves 12 synthetic beach photos (including a blurry shot, a dark
shot and a burst duplicate) and needs no Google account or network:

```bash
cd "$SOULACY_WORKSPACE/skills/photo-album"
W=$(mktemp -d)
python3 scripts/gphotos.py --fake pick fetch demo --out "$W"
python3 scripts/curate.py score "$W"
python3 scripts/render.py references/plan.example.json "$W" --out "$W/album.html"
python3 scripts/gphotos.py --fake publish references/plan.example.json --workdir "$W"
```

The last command prints the exact Google Photos calls a publish would make,
without sending any. `curate.py` needs Pillow (`pip install Pillow`).

## Removing access

Ask the agent to sign out, or run:

```bash
python3 scripts/gphotos.py auth revoke
```

This revokes the token at Google and deletes it locally. You can also remove
the app at [myaccount.google.com/permissions](https://myaccount.google.com/permissions).
Albums the skill created stay in Google Photos until you delete them.

## Troubleshooting

| Message | Fix |
|---|---|
| `no_client` | the client file is missing; see setup step 4 |
| `not_signed_in` or `auth` | sign in again (Testing apps expire after 7 days) |
| `missing_scope` | you asked to publish; approve the extra scope when asked |
| `session_timed_out` | the picking window closed; the agent starts a new one |
| `MISSING: Pillow` | the agent installs it, or run `pip install Pillow` |
| `403` from the Library API | enable **Photos Library API** in your project |
