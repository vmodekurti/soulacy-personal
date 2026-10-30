"""Offline tests for the photo-album skill scripts.

Run: python3 photo_album_test.py <path to catalog/photo-album/scripts>
(TestPhotoAlbumScripts in photo_album_test.go does this under `go test`.)
Everything runs in fake mode or against a stubbed HTTP layer; nothing
touches the network. Tests that need Pillow skip without it.
"""

import contextlib
import io
import json
import os
import shutil
import stat
import sys
import tempfile
import unittest
import urllib.parse

sys.dont_write_bytecode = True  # keep __pycache__ out of the embedded catalog
SCRIPTS = os.path.abspath(sys.argv.pop(1) if len(sys.argv) > 1 else "../catalog/photo-album/scripts")
sys.path.insert(0, SCRIPTS)

import curate  # noqa: E402
import gphotos  # noqa: E402
import render  # noqa: E402

try:
    import PIL  # noqa: F401

    HAVE_PIL = True
except ImportError:
    HAVE_PIL = False

EXAMPLE_PLAN = os.path.join(SCRIPTS, "..", "references", "plan.example.json")


def run(main, argv):
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        code = main(argv)
    return code, json.loads(buf.getvalue())


class Env(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.creds = os.path.join(self.tmp, "creds")
        self.old_env = dict(os.environ)
        os.environ["GOOGLE_PHOTOS_CREDS_DIR"] = self.creds
        os.environ.pop("GPHOTOS_FAKE", None)

    def tearDown(self):
        os.environ.clear()
        os.environ.update(self.old_env)
        shutil.rmtree(self.tmp)


@unittest.skipUnless(HAVE_PIL, "Pillow not installed")
class FakeEndToEnd(Env):
    def test_pick_score_render_publish(self):
        work = os.path.join(self.tmp, "album")
        code, r = run(gphotos.main, ["--fake", "pick", "fetch", "s", "--out", work])
        self.assertEqual((code, r["count"]), (0, 12))

        scored = curate.score(work)
        ranked = [i["id"] for i in scored["items"]]
        self.assertEqual(ranked[-1], "fake-003", "the blurry shot should rank last")
        by_id = {i["id"]: i for i in scored["items"]}
        self.assertEqual(by_id["fake-000"]["dup_group"], by_id["fake-006"]["dup_group"], "burst frames group together")
        self.assertEqual(sum(by_id[x]["best_of_group"] for x in ("fake-000", "fake-006")), 1)
        self.assertEqual(scored["dup_groups"], 11)

        out = os.path.join(work, "album.html")
        code, r = run(render.main, [EXAMPLE_PLAN, work, "--out", out])
        self.assertEqual((code, r["photos"]), (0, 7))
        with open(out) as f:
            page = f.read()
        self.assertIn("<title>Salt, Sun &amp; Sandcastles</title>", page)
        self.assertEqual(page.count("<figure"), 7)
        self.assertEqual(page.count("data:image/jpeg;base64,"), 8)  # 7 photos + cover

        code, r = run(gphotos.main, ["--fake", "publish", EXAMPLE_PLAN, "--workdir", work])
        self.assertTrue(r["dry_run"])
        self.assertEqual(r["summary"]["photos_to_upload"], 7)


class RenderValidation(unittest.TestCase):
    by_id = {"a": {"id": "a", "preview": "a.jpg"}, "b": {"id": "b", "preview": "b.jpg"}}

    def check(self, plan, msg):
        with self.assertRaisesRegex(render.PlanError, msg):
            render.validate(plan, self.by_id)

    def test_errors(self):
        self.check({"title": "", "sections": []}, "no title")
        self.check({"title": "t", "sections": [{"items": [{"id": "zzz"}]}]}, "unknown photo id")
        self.check({"title": "t", "sections": [{"items": [{"id": "a"}]}, {"items": [{"id": "a"}]}]}, "twice")
        self.check({"title": "t", "sections": [{"items": []}]}, "no photos")
        self.check({"title": "t", "theme": "neon", "sections": [{"items": [{"id": "a"}]}]}, "theme")

    def test_text_is_escaped(self):
        tmp = tempfile.mkdtemp()
        try:
            for k in "ab":
                with open(os.path.join(tmp, k + ".jpg"), "wb") as f:
                    f.write(gphotos._TINY_JPEG)
            plan = {"title": "<script>x</script>", "sections": [{"heading": "h", "items": [{"id": "a", "caption": "\"><img onerror=1>"}]}]}
            page, _ = render.render(plan, self.by_id, tmp)
            self.assertNotIn("<script>x</script>", page)
            self.assertNotIn("<img onerror", page)
        finally:
            shutil.rmtree(tmp)


class PublishPlan(Env):
    def plan(self, n, descs=False):
        items = [{"id": "p%d" % i, "caption": "c%d" % i} for i in range(n)]
        items.append({"id": "v", "type": "VIDEO", "caption": "a video"})
        return {
            "title": "T",
            "subtitle": "S",
            "sections": [{"heading": "One", "items": items[:3]}, {"heading": "Two", "intro": "i", "items": items[3:]}],
            "publish": {"captions_as_descriptions": descs},
        }

    def test_order_chunking_and_descriptions(self):
        calls = gphotos.publish_calls(self.plan(60))
        kinds = [c["call"] for c in calls]
        self.assertEqual(
            kinds,
            ["albums.create", "albums.addEnrichment", "albums.addEnrichment", "mediaItems.batchCreate",
             "albums.addEnrichment", "mediaItems.batchCreate", "mediaItems.batchCreate"],
        )
        self.assertEqual(calls[4]["text"], "Two\ni")
        sizes = [len(c["items"]) for c in calls if c["call"] == "mediaItems.batchCreate"]
        self.assertEqual(sizes, [3, 50, 7], "max 50 per batchCreate, videos skipped")
        self.assertTrue(all(i["description"] == "" for c in calls if "items" in c for i in c["items"]))
        with_desc = gphotos.publish_calls(self.plan(2, descs=True))
        self.assertEqual(with_desc[3]["items"][0]["description"], "c0")

    def test_real_publish_needs_yes(self):
        path = os.path.join(self.tmp, "plan.json")
        with open(path, "w") as f:
            json.dump(self.plan(2), f)
        code, r = run(gphotos.main, ["publish", path])
        self.assertEqual((code, r["code"]), (2, "needs_confirm"))
        self.assertEqual(r["summary"]["photos_to_upload"], 2)


class Auth(Env):
    def write_client(self):
        os.makedirs(self.creds)
        with open(os.path.join(self.creds, "client_secret.json"), "w") as f:
            json.dump({"installed": {"client_id": "cid.apps.googleusercontent.com", "client_secret": "shh"}}, f)

    def test_no_client(self):
        code, r = run(gphotos.main, ["auth", "start"])
        self.assertEqual((code, r["code"]), (2, "no_client"))

    def test_start_finish_with_pkce(self):
        self.write_client()
        code, r = run(gphotos.main, ["auth", "start"])
        self.assertEqual(code, 0)
        q = urllib.parse.parse_qs(urllib.parse.urlparse(r["consent_url"]).query)
        self.assertEqual(q["scope"], [gphotos.SCOPE_PICK], "no publish scope unless asked")
        self.assertEqual(q["code_challenge_method"], ["S256"])
        pending = os.path.join(self.creds, "pending.json")
        self.assertEqual(stat.S_IMODE(os.stat(pending).st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(os.stat(self.creds).st_mode), 0o700)

        code, r = run(gphotos.main, ["auth", "finish", "http://127.0.0.1:8765/?code=abc&state=WRONG"])
        self.assertEqual(r["code"], "state")

        sent = {}

        def fake_http(method, url, token=None, body=None, headers=None, raw=False):
            sent.update(body)
            return {"access_token": "AT", "refresh_token": "RT", "expires_in": 3600, "scope": gphotos.SCOPE_PICK}

        real, gphotos.http = gphotos.http, fake_http
        try:
            redirect = "http://127.0.0.1:8765/?code=abc&state=" + q["state"][0]
            code, r = run(gphotos.main, ["auth", "finish", redirect])
        finally:
            gphotos.http = real
        self.assertEqual(code, 0)
        self.assertEqual(sent["code"], "abc")
        self.assertTrue(sent["code_verifier"])
        self.assertNotIn("AT", json.dumps(r), "tokens never reach stdout")
        tok = os.path.join(self.creds, "token.json")
        self.assertEqual(stat.S_IMODE(os.stat(tok).st_mode), 0o600)
        self.assertFalse(os.path.exists(pending))

        code, r = run(gphotos.main, ["publish", EXAMPLE_PLAN, "--yes"])
        self.assertEqual(r["code"], "missing_scope")

    def test_creds_dir_defaults(self):
        os.environ.pop("GOOGLE_PHOTOS_CREDS_DIR")
        os.environ["SOULACY_WORKSPACE"] = "/ws"
        self.assertEqual(gphotos.creds_dir(), "/ws/credentials/google-photos")


class VisionStaysLocal(unittest.TestCase):
    def test_remote_ollama_refused(self):
        code, r = run(curate.main, ["score", "/nowhere", "--vision", "ollama", "--ollama-url", "https://example.com/api/generate"])
        self.assertEqual(code, 2)
        self.assertFalse(r["ok"])


if __name__ == "__main__":
    unittest.main(verbosity=1)
