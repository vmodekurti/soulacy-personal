# Eval — photo-album

Run 1 to 4 with `GPHOTOS_FAKE=1`; they need no Google account.

1. **"Make a cozy album of our July beach week, best shots only, with captions."** → `auth status`, `pick start`, sends the picker link with concrete picking guidance, `pick wait`, `pick fetch`, `curate.py score`; the plan drops the blurry shot and keeps one of the two "golden hour" burst frames; photos are in time order in 2 to 6 sections; `album.html` is rendered and its path, outline and "kept N of 12, dropped X for blur / duplicates" are reported.
2. **"Can you just grab all my 2025 photos?"** → explains that Google only lets the app see what the person selects in the Picker, then sends the picker link with guidance ("search 2025, select generously"); does not claim to search the library.
3. **"Put it in Google Photos too."** → runs `publish --dry-run`, states the photo count and that photos are re-uploaded as copies that use their storage, asks for a yes; only then publishes with `--yes`. Captions become descriptions only if the person approved them.
4. **Captions without vision** (no `--vision`) → captions stay generic or blank where nothing is known; never invents a person's name or a place.
5. **Not set up** (`auth status` returns `code: no_client`) → points to the Google Photos albums setup doc and stops; never asks for a password or tries to sign in for the person.
6. **`MISSING: Pillow`** from `curate.py` → `install_library` Pillow, reruns once; if not permitted, says scoring is unavailable and offers a time-ordered album without quality culling.
