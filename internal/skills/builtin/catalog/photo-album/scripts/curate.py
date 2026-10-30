#!/usr/bin/env python3
"""Score the photos in a workdir so the agent can keep the best shots.

  curate.py score <workdir> [--vision ollama] [--model llava] [--prompt "..."]

Reads <workdir>/manifest.json (written by `gphotos.py pick fetch`), adds a
`scores` block and a `dup_group` to every item, and writes it back. Prints a
compact summary, best first, that the agent reads to build the album plan.

Scores (0..1, higher is better), from the preview image with Pillow:
  sharpness   edge energy; low means motion blur or missed focus
  exposure    distance of mean brightness from mid-grey, and clipping
  resolution  from the original's pixel count in the metadata
  quality     a weighted blend of the three

Near duplicates (bursts, the same shot twice) share a `dup_group`; keep the
best of each group. With --vision ollama, a local Ollama vision model writes a
one-line `description` per photo. It runs on localhost only, so photos never
leave the machine and no API key is involved.

Without Pillow the script still runs: it prints MISSING: Pillow and exits 3,
the same contract the documents skill uses.
"""

import argparse
import base64
import json
import os
import sys
import urllib.parse
import urllib.request

VISION_PROMPT = (
    "Describe this photo in one plain sentence of at most 20 words: who or what is in it, "
    "the setting, the light and the mood. Do not guess names."
)


def need_pillow():
    try:
        from PIL import Image  # noqa: F401
    except ImportError:
        print("MISSING: Pillow")
        sys.exit(3)


def measure(path):
    from PIL import Image, ImageFilter, ImageStat

    with Image.open(path) as im:
        g = im.convert("L")
        g.thumbnail((512, 512))
        edges = g.filter(ImageFilter.FIND_EDGES)
        edge_mean = ImageStat.Stat(edges).mean[0]
        st = ImageStat.Stat(g)
        mean = st.mean[0]
        hist = g.histogram()
        n = float(sum(hist)) or 1.0
        clipped = (sum(hist[:6]) + sum(hist[250:])) / n
        small = g.resize((9, 8))
        px = list(small.getdata())
    sharp = min(1.0, edge_mean / 18.0)
    # Anything from dim to bright reads fine; only near-black, near-white and clipping cost.
    exposure = max(0.0, 1.0 - max(0.0, abs(mean - 128.0) - 60.0) / 68.0 - 2.0 * clipped)
    # dHash: 64 bits from horizontal gradients; near duplicates differ in few bits.
    bits = 0
    for row in range(8):
        for col in range(8):
            bits = (bits << 1) | (px[row * 9 + col] > px[row * 9 + col + 1])
    return sharp, exposure, bits


def hamming(a, b):
    return bin(a ^ b).count("1")


def describe(path, model, url):
    with open(path, "rb") as f:
        img = base64.b64encode(f.read()).decode()
    body = json.dumps({"model": model, "prompt": VISION_PROMPT, "images": [img], "stream": False}).encode()
    req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.loads(r.read()).get("response", "").strip()


def score(workdir, vision=None, model="llava", ollama_url="http://127.0.0.1:11434/api/generate", dup_bits=10):
    need_pillow()
    mpath = os.path.join(workdir, "manifest.json")
    with open(mpath) as f:
        manifest = json.load(f)
    items = manifest["items"]
    hashes = {}
    for it in items:
        sharp, exposure, h = measure(os.path.join(workdir, it["preview"]))
        mp = ((it.get("width") or 0) * (it.get("height") or 0)) / 1e6
        res = min(1.0, mp / 8.0) if mp else 0.5
        quality = 0.55 * sharp + 0.30 * exposure + 0.15 * res
        if sharp < 0.2:  # visibly blurred: no blend should rescue it
            quality *= 0.5
        quality = round(quality, 3)
        it["scores"] = {"sharpness": round(sharp, 3), "exposure": round(exposure, 3), "resolution": round(res, 3), "quality": quality}
        hashes[it["id"]] = h

    # Group near duplicates: greedy, in capture order, so a burst stays one group.
    group_of, groups = {}, []
    for it in sorted(items, key=lambda x: x.get("create_time") or ""):
        for gi, rep in enumerate(groups):
            if hamming(hashes[rep], hashes[it["id"]]) <= dup_bits:
                group_of[it["id"]] = gi
                break
        else:
            group_of[it["id"]] = len(groups)
            groups.append(it["id"])
    for it in items:
        it["dup_group"] = group_of[it["id"]]
    best = {}
    for it in items:
        g = it["dup_group"]
        if g not in best or it["scores"]["quality"] > best[g]["scores"]["quality"]:
            best[g] = it
    for it in items:
        it["best_of_group"] = best[it["dup_group"]] is it

    vision_note = None
    if vision == "ollama":
        for it in items:
            try:
                it["description"] = describe(os.path.join(workdir, it["preview"]), model, ollama_url)
            except Exception as e:  # noqa: BLE001  any failure just means no descriptions
                vision_note = "Ollama vision unavailable (%s); scored without descriptions." % e.__class__.__name__
                for x in items:
                    x.pop("description", None)
                break

    with open(mpath, "w") as f:
        json.dump(manifest, f, indent=2)

    ranked = sorted(items, key=lambda x: -x["scores"]["quality"])
    return {
        "ok": True,
        "count": len(items),
        "dup_groups": len(groups),
        "vision": vision if vision and not vision_note else None,
        "note": vision_note,
        "items": [
            {
                "id": it["id"],
                "q": it["scores"]["quality"],
                "sharp": it["scores"]["sharpness"],
                "exp": it["scores"]["exposure"],
                "dup_group": it["dup_group"],
                "best_of_group": it["best_of_group"],
                "time": it.get("create_time"),
                "type": it.get("type"),
                "file": it.get("filename"),
                "desc": it.get("description") or it.get("fake_subject"),
            }
            for it in ranked
        ],
    }


def main(argv=None):
    p = argparse.ArgumentParser(prog="curate.py")
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("score")
    s.add_argument("workdir")
    s.add_argument("--vision", choices=["ollama"], help="describe each photo with a local vision model")
    s.add_argument("--model", default="llava")
    s.add_argument("--ollama-url", default="http://127.0.0.1:11434/api/generate")
    a = p.parse_args(argv)
    host = urllib.parse.urlparse(a.ollama_url).hostname
    if host not in ("127.0.0.1", "localhost", "::1"):
        print(json.dumps({"ok": False, "error": "--ollama-url must be on this machine; photos are not sent elsewhere"}))
        return 2
    print(json.dumps(score(a.workdir, a.vision, a.model, a.ollama_url), indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main())
