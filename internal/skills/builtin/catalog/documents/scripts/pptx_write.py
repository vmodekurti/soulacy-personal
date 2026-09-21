#!/usr/bin/env python3
"""JSON → .pptx.  usage: pptx_write.py DECK.json OUT.pptx   DECK = [{"title","bullets":[...],"notes"}]"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

pptx = need("pptx", "python-pptx")
src, out = sys.argv[1:3]
deck = json.load(open(src))
prs = pptx.Presentation()
for i, s in enumerate(deck):
    layout = prs.slide_layouts[0 if i == 0 and not s.get("bullets") else 1]
    slide = prs.slides.add_slide(layout)
    slide.shapes.title.text = s.get("title", "")
    if s.get("bullets") and len(slide.placeholders) > 1:
        tf = slide.placeholders[1].text_frame
        tf.text = s["bullets"][0]
        for b in s["bullets"][1:]:
            tf.add_paragraph().text = b
    if s.get("notes"):
        slide.notes_slide.notes_text_frame.text = s["notes"]
prs.save(out)
print(f"wrote {out} with {len(deck)} slide(s)")
