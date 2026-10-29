#!/usr/bin/env python3
"""Google Photos client for the photo-album skill (stdlib only).

Subcommands (all print one JSON object on stdout):

  auth status                      is a token present, which scopes, which creds dir
  auth start [--publish]           print the consent URL the person opens themselves
  auth finish <redirect-url|code>  exchange the code from the redirect for a token
  auth login [--publish]           same flow with a loopback listener (run by the person
                                   in their own terminal on the machine with the browser)
  auth revoke                      revoke the token at Google and delete it locally

  pick start [--max N]             create a Picker session, print its pickerUri
  pick wait <session> [--timeout S]  poll until the person has finished picking
  pick fetch <session> --out DIR   download previews of the picked items + manifest.json
  pick done <session>              delete the Picker session

  publish <plan.json> --workdir DIR [--dry-run] [--yes]
                                   create a real Google Photos album from the plan

Fake mode: set GPHOTOS_FAKE=1 (or pass --fake) and every call is served
locally with synthetic photos. Nothing touches the network and no credentials
are needed. Use it to try the skill and in tests.

Credentials never enter this script's arguments or output. The OAuth client
file and the token live in the creds dir (see creds_dir()), mode 0600.
"""

import argparse
import base64
import hashlib
import http.server
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

AUTH_URL = "https://accounts.google.com/o/oauth2/v2/auth"
TOKEN_URL = "https://oauth2.googleapis.com/token"
REVOKE_URL = "https://oauth2.googleapis.com/revoke"
PICKER = "https://photospicker.googleapis.com/v1"
LIBRARY = "https://photoslibrary.googleapis.com/v1"

SCOPE_PICK = "https://www.googleapis.com/auth/photospicker.mediaitems.readonly"
SCOPE_PUBLISH = "https://www.googleapis.com/auth/photoslibrary.appendonly"

# The loopback redirect a Desktop OAuth client accepts. If the gateway is not
# on the person's machine, the browser shows "can't connect" after consent;
# the person copies that URL from the address bar and hands it to `auth finish`.
REDIRECT = "http://127.0.0.1:8765/"

PREVIEW_SIZE = "=w1600-h1600"
BATCH = 50  # mediaItems:batchCreate limit


class Fail(Exception):
    def __init__(self, msg, code="error", **extra):
        super().__init__(msg)
        self.code = code
        self.extra = extra


def out(obj):
    print(json.dumps(obj, indent=2, sort_keys=True))


def fake_mode(args=None):
    return bool(getattr(args, "fake", False)) or os.environ.get("GPHOTOS_FAKE") == "1"


# --- credentials -----------------------------------------------------------


def creds_dir():
    """Where the OAuth client and token live. Never inside a git checkout."""
    explicit = os.environ.get("GOOGLE_PHOTOS_CREDS_DIR")
    if explicit:
        return explicit
    ws = os.environ.get("SOULACY_WORKSPACE")
    if ws:
        return os.path.join(ws, "credentials", "google-photos")
    return os.path.join(os.path.expanduser("~"), ".soulacy", "credentials", "google-photos")


def _path(name):
    return os.path.join(creds_dir(), name)


def _write_private(name, obj):
    d = creds_dir()
    os.makedirs(d, mode=0o700, exist_ok=True)
    os.chmod(d, 0o700)
    p = _path(name)
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(obj, f)
    os.chmod(p, 0o600)


def _read(name):
    try:
        with open(_path(name)) as f:
            return json.load(f)
    except FileNotFoundError:
        return None


def load_client():
    raw = _read("client_secret.json")
    if raw is None:
        raise Fail(
            "No OAuth client found. Download the Desktop OAuth client JSON from Google Cloud "
            "and save it as client_secret.json in the creds dir.",
            code="no_client",
            creds_dir=creds_dir(),
        )
    c = raw.get("installed") or raw.get("web") or raw
    if not c.get("client_id"):
        raise Fail("client_secret.json has no client_id", code="bad_client")
    return c


# --- HTTP ------------------------------------------------------------------


def http(method, url, token=None, body=None, headers=None, raw=False):
    h = dict(headers or {})
    data = None
    if token:
        h["Authorization"] = "Bearer " + token
    if isinstance(body, (bytes, bytearray)):
        data = bytes(body)
    elif isinstance(body, dict) and h.get("Content-Type") == "application/x-www-form-urlencoded":
        data = urllib.parse.urlencode(body).encode()
    elif body is not None:
        data = json.dumps(body).encode()
        h.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, method=method, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            payload = r.read()
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "replace")[:500]
        if e.code in (401, 403):
            raise Fail("Google refused the request (%d): %s" % (e.code, detail), code="auth")
        raise Fail("Google API error %d: %s" % (e.code, detail), code="api")
    if raw:
        return payload
    return json.loads(payload or b"{}")


def access_token(need_publish=False):
    tok = _read("token.json")
    if not tok:
        raise Fail("Not signed in. Run `auth start` and have the person consent.", code="not_signed_in")
    scopes = tok.get("scope", "").split()
    if need_publish and SCOPE_PUBLISH not in scopes:
        raise Fail(
            "Publishing needs the photoslibrary.appendonly scope. Run `auth start --publish` "
            "and have the person consent again.",
            code="missing_scope",
        )
    if tok.get("expires_at", 0) > time.time() + 60:
        return tok["access_token"]
    if not tok.get("refresh_token"):
        raise Fail("Token expired and has no refresh token. Sign in again.", code="not_signed_in")
    c = load_client()
    fresh = http(
        "POST",
        TOKEN_URL,
        body={
            "client_id": c["client_id"],
            "client_secret": c.get("client_secret", ""),
            "refresh_token": tok["refresh_token"],
            "grant_type": "refresh_token",
        },
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    tok["access_token"] = fresh["access_token"]
    tok["expires_at"] = time.time() + int(fresh.get("expires_in", 3600))
    if fresh.get("scope"):
        tok["scope"] = fresh["scope"]
    _write_private("token.json", tok)
    return tok["access_token"]


# --- auth ------------------------------------------------------------------


def _consent_url(publish):
    c = load_client()
    verifier = secrets.token_urlsafe(64)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
    state = secrets.token_urlsafe(24)
    scopes = [SCOPE_PICK] + ([SCOPE_PUBLISH] if publish else [])
    _write_private("pending.json", {"verifier": verifier, "state": state, "created": time.time()})
    q = {
        "client_id": c["client_id"],
        "redirect_uri": REDIRECT,
        "response_type": "code",
        "scope": " ".join(scopes),
        "access_type": "offline",
        "prompt": "consent",
        "include_granted_scopes": "true",
        "code_challenge": challenge,
        "code_challenge_method": "S256",
        "state": state,
    }
    return AUTH_URL + "?" + urllib.parse.urlencode(q), scopes


def _exchange(code_or_url):
    pending = _read("pending.json")
    if not pending:
        raise Fail("No sign-in in progress. Run `auth start` first.", code="no_pending")
    code = code_or_url.strip()
    if code.startswith("http"):
        q = urllib.parse.parse_qs(urllib.parse.urlparse(code).query)
        if q.get("error"):
            raise Fail("Consent was not granted: " + q["error"][0], code="denied")
        if q.get("state", [""])[0] != pending["state"]:
            raise Fail("State mismatch: this redirect is not from the current sign-in.", code="state")
        code = q.get("code", [""])[0]
    if not code:
        raise Fail("No authorization code in that input.", code="no_code")
    c = load_client()
    tok = http(
        "POST",
        TOKEN_URL,
        body={
            "client_id": c["client_id"],
            "client_secret": c.get("client_secret", ""),
            "code": code,
            "code_verifier": pending["verifier"],
            "redirect_uri": REDIRECT,
            "grant_type": "authorization_code",
        },
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    old = _read("token.json") or {}
    _write_private(
        "token.json",
        {
            "access_token": tok["access_token"],
            "refresh_token": tok.get("refresh_token") or old.get("refresh_token"),
            "expires_at": time.time() + int(tok.get("expires_in", 3600)),
            "scope": tok.get("scope", ""),
        },
    )
    os.remove(_path("pending.json"))
    return tok.get("scope", "").split()


def cmd_auth(a):
    if fake_mode(a):
        out({"ok": True, "fake": True, "signed_in": True, "scopes": [SCOPE_PICK, SCOPE_PUBLISH]})
        return
    if a.action == "status":
        tok = _read("token.json")
        out(
            {
                "ok": True,
                "creds_dir": creds_dir(),
                "client_present": _read("client_secret.json") is not None,
                "signed_in": bool(tok),
                "scopes": (tok or {}).get("scope", "").split(),
                "can_publish": bool(tok) and SCOPE_PUBLISH in tok.get("scope", ""),
            }
        )
    elif a.action == "start":
        url, scopes = _consent_url(a.publish)
        out(
            {
                "ok": True,
                "consent_url": url,
                "scopes": scopes,
                "next": "The person opens consent_url, signs in and approves. Their browser then "
                "lands on 127.0.0.1 (it may say it cannot connect). They copy that full URL "
                "and you run `auth finish '<url>'`.",
            }
        )
    elif a.action == "finish":
        if not a.value:
            raise Fail("auth finish needs the redirect URL or code", code="usage")
        out({"ok": True, "signed_in": True, "scopes": _exchange(a.value)})
    elif a.action == "login":
        url, _ = _consent_url(a.publish)
        got = {}

        class H(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                got["url"] = "http://127.0.0.1:8765" + self.path
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(b"Signed in. You can close this tab.")

            def log_message(self, *args):
                pass

        srv = http.server.HTTPServer(("127.0.0.1", 8765), H)
        srv.timeout = 300
        print("Open this URL in your browser and approve:\n" + url, file=sys.stderr)
        srv.handle_request()
        if "url" not in got:
            raise Fail("Timed out waiting for the browser redirect.", code="timeout")
        out({"ok": True, "signed_in": True, "scopes": _exchange(got["url"])})
    elif a.action == "revoke":
        tok = _read("token.json")
        if tok:
            try:
                http(
                    "POST",
                    REVOKE_URL,
                    body={"token": tok.get("refresh_token") or tok["access_token"]},
                    headers={"Content-Type": "application/x-www-form-urlencoded"},
                )
            except Fail:
                pass  # already revoked at Google; still forget it here
            os.remove(_path("token.json"))
        out({"ok": True, "signed_in": False})


# --- fake backend ----------------------------------------------------------

FAKE_SUBJECTS = [
    ("beach at golden hour", (238, 170, 92)),
    ("kids building a sandcastle", (226, 205, 160)),
    ("wave crashing on rocks", (70, 120, 160)),
    ("blurry pocket shot", (90, 90, 90)),
    ("sunset over the pier", (230, 110, 80)),
    ("family dinner on the deck", (160, 110, 80)),
    ("beach at golden hour (burst)", (238, 170, 92)),
    ("tide pools", (60, 140, 130)),
    ("very dark night photo", (12, 12, 20)),
    ("ice cream stand", (240, 200, 210)),
    ("dog running on the sand", (200, 180, 140)),
    ("lighthouse", (180, 190, 210)),
]


def _fake_items(n):
    items = []
    base = time.mktime((2025, 7, 4, 9, 0, 0, 0, 0, -1))
    for i in range(n):
        subj, _ = FAKE_SUBJECTS[i % len(FAKE_SUBJECTS)]
        items.append(
            {
                "id": "fake-%03d" % i,
                "createTime": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(base + i * 5400)),
                "type": "PHOTO",
                "mediaFile": {
                    "baseUrl": "fake://%d" % i,
                    "mimeType": "image/jpeg",
                    "filename": "IMG_%04d.jpg" % (1000 + i),
                    "mediaFileMetadata": {"width": 4032, "height": 3024, "cameraMake": "Fake", "cameraModel": "Cam"},
                },
                "_fake_subject": subj,
            }
        )
    return items


# A 1x1 grey JPEG, used when Pillow is not installed.
_TINY_JPEG = base64.b64decode(
    "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDABALDA4MChAODQ4SERATGCgaGBYWGDEjJR0oOjM9PDkzODdASFxOQERXRTc4UG1R"
    "V19iZ2hnPk1xeXBkeFxlZ2P/2wBDARESEhgVGC8aGi9jQjhCY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2Nj"
    "Y2NjY2NjY2NjY2NjY2P/wAARCAABAAEDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAA"
    "AgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6"
    "Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXG"
    "x8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREA"
    "AgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5"
    "OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPE"
    "xcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwAooooA/9k="
)


def _fake_image(i):
    try:
        from PIL import Image, ImageDraw, ImageFilter
    except ImportError:
        return _TINY_JPEG
    import io
    import random

    subj, color = FAKE_SUBJECTS[i % len(FAKE_SUBJECTS)]
    # A burst shot reuses its original's layout so it lands in the same dup group.
    rng = random.Random(subj.replace(" (burst)", ""))
    img = Image.new("RGB", (800, 600), color)
    d = ImageDraw.Draw(img)
    for _ in range(40):  # detail, so sharpness and dHash have something to measure
        x, y, r = rng.randint(0, 800), rng.randint(0, 600), rng.randint(10, 120)
        shade = tuple(max(0, min(255, c + rng.randint(-90, 90))) for c in color)
        d.ellipse([x - r, y - r, x + r, y + r], fill=shade, outline=(255, 255, 255))
    d.rectangle([40, 480, 760, 560], fill=(255, 255, 255))
    d.text((60, 505), subj, fill=(0, 0, 0))
    if "blurry" in subj:
        img = img.filter(ImageFilter.GaussianBlur(12))
    buf = io.BytesIO()
    img.save(buf, "JPEG", quality=85)
    return buf.getvalue()


# --- picker ----------------------------------------------------------------


def _list_picked(token, session):
    items, page = [], None
    while True:
        q = {"sessionId": session, "pageSize": 100}
        if page:
            q["pageToken"] = page
        r = http("GET", PICKER + "/mediaItems?" + urllib.parse.urlencode(q), token=token)
        items.extend(r.get("mediaItems", []))
        page = r.get("nextPageToken")
        if not page:
            return items


def cmd_pick(a):
    fake = fake_mode(a)
    token = None if fake else access_token()
    if a.action == "start":
        if fake:
            out({"ok": True, "fake": True, "session": "fake-session", "picker_uri": "https://photos.google.com/picker/fake"})
            return
        body = {"pickingConfig": {"maxItemCount": str(a.max)}} if a.max else {}
        s = http("POST", PICKER + "/sessions", token=token, body=body)
        out(
            {
                "ok": True,
                "session": s["id"],
                "picker_uri": s["pickerUri"],
                "expire_time": s.get("expireTime"),
                "next": "Send picker_uri to the person. They select photos in Google Photos and tap Done. "
                "Then run `pick wait`.",
            }
        )
    elif a.action == "wait":
        if fake:
            out({"ok": True, "fake": True, "ready": True})
            return
        deadline = time.time() + a.timeout
        while True:
            s = http("GET", PICKER + "/sessions/" + urllib.parse.quote(a.session), token=token)
            if s.get("mediaItemsSet"):
                out({"ok": True, "ready": True})
                return
            cfg = s.get("pollingConfig") or {}
            if cfg.get("timeoutIn") in ("0s", "0"):
                out({"ok": False, "ready": False, "code": "session_timed_out", "error": "The picking window closed. Start a new session."})
                return
            if time.time() > deadline:
                out({"ok": True, "ready": False, "next": "The person has not finished picking yet. Run `pick wait` again."})
                return
            interval = float(str(cfg.get("pollInterval", "5s")).rstrip("s") or 5)
            time.sleep(min(max(interval, 2), 15))
    elif a.action == "fetch":
        if not a.out:
            raise Fail("pick fetch needs --out DIR", code="usage")
        os.makedirs(os.path.join(a.out, "previews"), exist_ok=True)
        items = _fake_items(a.fake_count) if fake else _list_picked(token, a.session)
        manifest = {"session": a.session, "fetched_at": int(time.time()), "fake": fake, "items": []}
        for i, it in enumerate(items):
            mf = it.get("mediaFile", {})
            meta = mf.get("mediaFileMetadata", {})
            local = os.path.join("previews", "%s.jpg" % _safe(it["id"]))
            if fake:
                data = _fake_image(i)
            else:
                # Videos and photos both have a still at this size; videos are not downloaded.
                data = http("GET", mf["baseUrl"] + PREVIEW_SIZE, token=token, raw=True)
            with open(os.path.join(a.out, local), "wb") as f:
                f.write(data)
            entry = {
                "id": it["id"],
                "type": it.get("type", "PHOTO"),
                "create_time": it.get("createTime"),
                "filename": mf.get("filename"),
                "mime_type": mf.get("mimeType"),
                "width": meta.get("width"),
                "height": meta.get("height"),
                "camera": " ".join(x for x in (meta.get("cameraMake"), meta.get("cameraModel")) if x) or None,
                "preview": local,
            }
            if fake:
                entry["fake_subject"] = it["_fake_subject"]
            manifest["items"].append(entry)
        with open(os.path.join(a.out, "manifest.json"), "w") as f:
            json.dump(manifest, f, indent=2)
        out({"ok": True, "fake": fake, "count": len(manifest["items"]), "manifest": os.path.join(a.out, "manifest.json")})
    elif a.action == "done":
        if not fake:
            http("DELETE", PICKER + "/sessions/" + urllib.parse.quote(a.session), token=token)
        out({"ok": True, "deleted": a.session})


def _safe(s):
    return "".join(ch if ch.isalnum() or ch in "-_" else "_" for ch in s)[:80]


# --- publish ---------------------------------------------------------------


def publish_calls(plan):
    """The ordered list of Library API calls a plan turns into.

    Pure function, so a dry run and the tests can inspect exactly what would be
    sent. Each section header becomes a text enrichment placed at the end of the
    album, followed by that section's photos, so the album reads top to bottom
    like the HTML page.
    """
    calls = [{"call": "albums.create", "title": plan["title"][:500]}]
    if plan.get("subtitle"):
        calls.append({"call": "albums.addEnrichment", "text": plan["subtitle"][:1000], "position": "LAST_IN_ALBUM"})
    use_desc = bool(plan.get("publish", {}).get("captions_as_descriptions"))
    for sec in plan.get("sections", []):
        text = "\n".join(x for x in (sec.get("heading"), sec.get("intro")) if x)
        if text:
            calls.append({"call": "albums.addEnrichment", "text": text[:1000], "position": "LAST_IN_ALBUM"})
        photos = [it for it in sec.get("items", []) if it.get("type", "PHOTO") == "PHOTO"]
        for i in range(0, len(photos), BATCH):
            chunk = photos[i : i + BATCH]
            calls.append(
                {
                    "call": "mediaItems.batchCreate",
                    "position": "LAST_IN_ALBUM",
                    "items": [
                        {"id": it["id"], "description": (it.get("caption") or "")[:1000] if use_desc else ""}
                        for it in chunk
                    ],
                }
            )
    return calls


def cmd_publish(a):
    with open(a.plan) as f:
        plan = json.load(f)
    calls = publish_calls(plan)
    n_photos = sum(len(c["items"]) for c in calls if c["call"] == "mediaItems.batchCreate")
    summary = {
        "title": plan["title"],
        "photos_to_upload": n_photos,
        "enrichments": sum(1 for c in calls if c["call"] == "albums.addEnrichment"),
        "note": "Each photo is re-uploaded at original quality as a new item, so it counts toward the "
        "person's Google storage and appears twice in their library.",
    }
    if a.dry_run or fake_mode(a):
        out({"ok": True, "dry_run": True, "fake": fake_mode(a), "summary": summary, "calls": calls})
        return
    if not a.yes:
        raise Fail("Publishing changes the person's Google Photos. Confirm with them, then pass --yes.", code="needs_confirm", summary=summary)
    token = access_token(need_publish=True)
    with open(os.path.join(a.workdir, "manifest.json")) as f:
        manifest = json.load(f)
    # Picker baseUrls last 60 minutes, so fetch fresh ones from the session.
    picked = {it["id"]: it for it in _list_picked(access_token(), manifest["session"])}
    album_id = None
    created = 0
    for c in calls:
        if c["call"] == "albums.create":
            album_id = http("POST", LIBRARY + "/albums", token=token, body={"album": {"title": c["title"]}})["id"]
        elif c["call"] == "albums.addEnrichment":
            http(
                "POST",
                LIBRARY + "/albums/%s:addEnrichment" % album_id,
                token=token,
                body={"newEnrichmentItem": {"textEnrichment": {"text": c["text"]}}, "albumPosition": {"position": c["position"]}},
            )
        else:
            new = []
            for it in c["items"]:
                src = picked.get(it["id"])
                if not src:
                    continue
                mf = src["mediaFile"]
                data = http("GET", mf["baseUrl"] + "=d", token=access_token(), raw=True)
                up = http(
                    "POST",
                    LIBRARY + "/uploads",
                    token=token,
                    body=data,
                    headers={
                        "Content-Type": "application/octet-stream",
                        "X-Goog-Upload-Content-Type": mf.get("mimeType", "image/jpeg"),
                        "X-Goog-Upload-Protocol": "raw",
                    },
                    raw=True,
                ).decode()
                entry = {"simpleMediaItem": {"uploadToken": up, "fileName": mf.get("filename", it["id"] + ".jpg")}}
                if it["description"]:
                    entry["description"] = it["description"]
                new.append(entry)
            if new:
                r = http(
                    "POST",
                    LIBRARY + "/mediaItems:batchCreate",
                    token=token,
                    body={"albumId": album_id, "newMediaItems": new, "albumPosition": {"position": c["position"]}},
                )
                created += sum(1 for x in r.get("newMediaItemResults", []) if x.get("mediaItem"))
    out({"ok": True, "album_id": album_id, "photos_created": created, "summary": summary})


# --- main ------------------------------------------------------------------


def main(argv=None):
    p = argparse.ArgumentParser(prog="gphotos.py")
    p.add_argument("--fake", action="store_true", help="serve everything locally, no network or credentials")
    sub = p.add_subparsers(dest="cmd", required=True)

    pa = sub.add_parser("auth")
    pa.add_argument("action", choices=["status", "start", "finish", "login", "revoke"])
    pa.add_argument("value", nargs="?")
    pa.add_argument("--publish", action="store_true", help="also request the album-creation scope")
    pa.set_defaults(fn=cmd_auth)

    pp = sub.add_parser("pick")
    pp.add_argument("action", choices=["start", "wait", "fetch", "done"])
    pp.add_argument("session", nargs="?", default="")
    pp.add_argument("--max", type=int, default=300)
    pp.add_argument("--timeout", type=int, default=240)
    pp.add_argument("--out")
    pp.add_argument("--fake-count", type=int, default=12)
    pp.set_defaults(fn=cmd_pick)

    pu = sub.add_parser("publish")
    pu.add_argument("plan")
    pu.add_argument("--workdir", required=False, default=".")
    pu.add_argument("--dry-run", action="store_true")
    pu.add_argument("--yes", action="store_true", help="the person has confirmed publishing")
    pu.set_defaults(fn=cmd_publish)

    a = p.parse_args(argv)
    if a.cmd == "pick" and a.action != "start" and not a.session:
        p.error("pick %s needs a session id" % a.action)
    try:
        a.fn(a)
    except Fail as e:
        out(dict({"ok": False, "code": e.code, "error": str(e)}, **e.extra))
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
