#!/usr/bin/env python3
"""Render an album plan into one self-contained HTML story page.

  render.py <plan.json> <workdir> [--out album.html] [--linked]

The plan is what the agent writes after curating (see SKILL.md for the
schema). Every photo is embedded as a data URI, so the page is a single file
the person can open offline, keep, or send. --linked references the preview
files instead, for a much smaller page that only works next to the workdir.

Validation errors (unknown ids, empty album) print {"ok": false, ...} and
exit 2, so the agent can fix the plan and rerun.
"""

import argparse
import base64
import html
import json
import os
import sys

THEMES = {
    "cozy": {
        "font": "'Fraunces', Georgia, serif",
        "body": "'Nunito', system-ui, sans-serif",
        "fonts": "Fraunces:opsz,wght@9..144,400;9..144,700&family=Nunito:wght@400;600",
        "bg": "#f6efe4",
        "ink": "#3b2f25",
        "muted": "#8a7462",
        "card": "#fffaf2",
        "accent": "#c46a3c",
        "frame": "polaroid",
    },
    "editorial": {
        "font": "'Playfair Display', Georgia, serif",
        "body": "'Inter', system-ui, sans-serif",
        "fonts": "Playfair+Display:wght@400;800&family=Inter:wght@400;600",
        "bg": "#fbfbf8",
        "ink": "#111111",
        "muted": "#6b6b6b",
        "card": "#ffffff",
        "accent": "#b3261e",
        "frame": "bleed",
    },
    "minimal": {
        "font": "'Inter', system-ui, sans-serif",
        "body": "'Inter', system-ui, sans-serif",
        "fonts": "Inter:wght@300;400;600",
        "bg": "#ffffff",
        "ink": "#1d1d1f",
        "muted": "#86868b",
        "card": "#ffffff",
        "accent": "#1d1d1f",
        "frame": "plain",
    },
    "night": {
        "font": "'Cormorant Garamond', Georgia, serif",
        "body": "'Inter', system-ui, sans-serif",
        "fonts": "Cormorant+Garamond:wght@400;700&family=Inter:wght@400;600",
        "bg": "#0e1116",
        "ink": "#eef1f5",
        "muted": "#9aa4b2",
        "card": "#171b22",
        "accent": "#f2c46d",
        "frame": "glow",
    },
}

CSS = """
:root{--bg:%(bg)s;--ink:%(ink)s;--muted:%(muted)s;--card:%(card)s;--accent:%(accent)s}
*{box-sizing:border-box}html,body{margin:0;background:var(--bg);color:var(--ink)}
body{font-family:%(body)s;line-height:1.55;-webkit-font-smoothing:antialiased}
.hero{position:relative;min-height:88vh;display:flex;align-items:flex-end;overflow:hidden}
.hero img{position:absolute;inset:0;width:100%%;height:100%%;object-fit:cover;filter:saturate(1.05)}
.hero::after{content:"";position:absolute;inset:0;background:linear-gradient(180deg,transparent 35%%,rgba(0,0,0,.62))}
.hero .t{position:relative;z-index:1;padding:clamp(24px,6vw,72px);color:#fff;max-width:900px}
.hero h1{font-family:%(font)s;font-size:clamp(40px,8vw,96px);line-height:1.02;margin:0 0 12px;letter-spacing:-.01em}
.hero p{font-size:clamp(16px,2.2vw,22px);margin:0;opacity:.92}
.hero .when{text-transform:uppercase;letter-spacing:.18em;font-size:12px;opacity:.8;margin-bottom:14px}
main{max-width:1180px;margin:0 auto;padding:0 16px 96px}
section{padding-top:clamp(48px,8vw,96px)}
section h2{font-family:%(font)s;font-size:clamp(28px,4.5vw,48px);margin:0 0 8px;line-height:1.1}
section .intro{color:var(--muted);max-width:680px;margin:0 0 28px;font-size:18px}
section h2::before{content:"";display:block;width:48px;height:3px;background:var(--accent);margin-bottom:18px}
.grid{columns:3 300px;column-gap:20px}
figure{break-inside:avoid;margin:0 0 20px;background:var(--card);cursor:zoom-in}
figure img{display:block;width:100%%;height:auto}
figcaption{font-size:15px;color:var(--muted);padding:10px 4px 2px}
figure.feature{column-span:all;margin-bottom:28px}
figure.feature img{max-height:78vh;object-fit:cover}
.polaroid figure{padding:12px 12px 4px;box-shadow:0 10px 30px rgba(60,40,20,.12);border-radius:3px}
.polaroid figure:nth-child(3n+1){transform:rotate(-.6deg)}.polaroid figure:nth-child(3n+2){transform:rotate(.5deg)}
.polaroid figcaption{font-family:%(font)s;font-style:italic;color:var(--ink);text-align:center}
.bleed figure.feature{margin-left:calc(50%% - 50vw);margin-right:calc(50%% - 50vw)}
.bleed figcaption{font-size:13px;letter-spacing:.02em;border-left:2px solid var(--accent);padding-left:10px;margin-top:8px}
.glow figure{border-radius:14px;overflow:hidden;box-shadow:0 0 0 1px rgba(255,255,255,.06),0 18px 50px rgba(0,0,0,.5)}
.glow figcaption{padding:12px 14px}
.plain figure{border-radius:6px;overflow:hidden}
footer{text-align:center;color:var(--muted);font-size:13px;padding:40px 16px 56px}
.lb{position:fixed;inset:0;background:rgba(0,0,0,.92);display:none;align-items:center;justify-content:center;z-index:9;padding:24px;flex-direction:column}
.lb.on{display:flex}.lb img{max-width:100%%;max-height:84vh;object-fit:contain}
.lb p{color:#ddd;max-width:760px;text-align:center;margin:14px 0 0}
@media (max-width:640px){.grid{columns:1}.polaroid figure{transform:none!important}}
@media (prefers-reduced-motion:no-preference){figure{transition:transform .25s ease}figure:hover{transform:translateY(-3px)}}
"""

JS = """
const lb=document.querySelector('.lb'),li=lb.querySelector('img'),lp=lb.querySelector('p');
document.querySelectorAll('figure').forEach(f=>f.addEventListener('click',()=>{
li.src=f.querySelector('img').src;lp.textContent=(f.querySelector('figcaption')||{}).textContent||'';lb.classList.add('on')}));
lb.addEventListener('click',()=>lb.classList.remove('on'));
document.addEventListener('keydown',e=>{if(e.key==='Escape')lb.classList.remove('on')});
"""


class PlanError(Exception):
    pass


def load(plan_path, workdir):
    with open(plan_path) as f:
        plan = json.load(f)
    with open(os.path.join(workdir, "manifest.json")) as f:
        manifest = json.load(f)
    return plan, {it["id"]: it for it in manifest["items"]}


def validate(plan, by_id):
    if not (plan.get("title") or "").strip():
        raise PlanError("plan has no title")
    seen = set()
    total = 0
    for s in plan.get("sections", []):
        for it in s.get("items", []):
            if it["id"] not in by_id:
                raise PlanError("unknown photo id %r (not in manifest.json)" % it["id"])
            if it["id"] in seen:
                raise PlanError("photo %r appears twice" % it["id"])
            seen.add(it["id"])
            total += 1
    if total == 0:
        raise PlanError("plan has no photos")
    if plan.get("cover") and plan["cover"] not in by_id:
        raise PlanError("cover %r is not in manifest.json" % plan["cover"])
    if plan.get("theme", "cozy") not in THEMES:
        raise PlanError("theme must be one of " + ", ".join(sorted(THEMES)))
    return total


def _src(workdir, item, linked):
    if linked:
        return html.escape(item["preview"])
    with open(os.path.join(workdir, item["preview"]), "rb") as f:
        return "data:image/jpeg;base64," + base64.b64encode(f.read()).decode()


def _date_range(items):
    days = sorted((it.get("create_time") or "")[:10] for it in items if it.get("create_time"))
    if not days:
        return ""
    return days[0] if days[0] == days[-1] else "%s to %s" % (days[0], days[-1])


def render(plan, by_id, workdir, linked=False):
    validate(plan, by_id)
    th = THEMES[plan.get("theme", "cozy")]
    esc = html.escape
    chosen = [by_id[it["id"]] for s in plan["sections"] for it in s["items"]]
    cover = by_id[plan.get("cover") or chosen[0]["id"]]
    parts = [
        "<!doctype html><html lang='en'><head><meta charset='utf-8'>",
        "<meta name='viewport' content='width=device-width,initial-scale=1'>",
        "<title>%s</title>" % esc(plan["title"]),
        "<link rel='stylesheet' href='https://fonts.googleapis.com/css2?family=%s&display=swap'>" % th["fonts"],
        "<style>%s</style></head><body class='%s'>" % (CSS % th, th["frame"]),
        "<header class='hero'><img src='%s' alt=''><div class='t'>" % _src(workdir, cover, linked),
        "<div class='when'>%s</div>" % esc(plan.get("date_label") or _date_range(chosen)),
        "<h1>%s</h1>" % esc(plan["title"]),
    ]
    if plan.get("subtitle"):
        parts.append("<p>%s</p>" % esc(plan["subtitle"]))
    parts.append("</div></header><main>")
    for s in plan["sections"]:
        parts.append("<section>")
        if s.get("heading"):
            parts.append("<h2>%s</h2>" % esc(s["heading"]))
        if s.get("intro"):
            parts.append("<p class='intro'>%s</p>" % esc(s["intro"]))
        parts.append("<div class='grid'>")
        for it in s["items"]:
            m = by_id[it["id"]]
            cls = " class='feature'" if it.get("feature") else ""
            cap = it.get("caption") or ""
            parts.append(
                "<figure%s><img loading='lazy' src='%s' alt='%s'>%s</figure>"
                % (cls, _src(workdir, m, linked), esc(cap), "<figcaption>%s</figcaption>" % esc(cap) if cap else "")
            )
        parts.append("</div></section>")
    parts.append("</main><footer>%s</footer>" % esc(plan.get("footer") or "%d photos" % len(chosen)))
    parts.append("<div class='lb'><img alt=''><p></p></div><script>%s</script></body></html>" % JS)
    return "".join(parts), len(chosen)


def main(argv=None):
    p = argparse.ArgumentParser(prog="render.py")
    p.add_argument("plan")
    p.add_argument("workdir")
    p.add_argument("--out")
    p.add_argument("--linked", action="store_true", help="reference preview files instead of embedding them")
    a = p.parse_args(argv)
    out = a.out or os.path.join(a.workdir, "album.html")
    try:
        plan, by_id = load(a.plan, a.workdir)
        page, n = render(plan, by_id, a.workdir, a.linked)
    except (PlanError, KeyError) as e:
        print(json.dumps({"ok": False, "error": str(e)}))
        return 2
    with open(out, "w") as f:
        f.write(page)
    print(json.dumps({"ok": True, "out": out, "photos": n, "bytes": len(page)}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
