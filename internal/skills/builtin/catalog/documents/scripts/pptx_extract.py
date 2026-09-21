#!/usr/bin/env python3
"""Print slide text and notes of a .pptx.  usage: pptx_extract.py FILE"""
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

pptx = need("pptx", "python-pptx")
prs = pptx.Presentation(sys.argv[1])
for i, slide in enumerate(prs.slides, 1):
    print(f"\n--- slide {i} ---")
    for shape in slide.shapes:
        if shape.has_text_frame and shape.text_frame.text.strip():
            print(shape.text_frame.text)
    if slide.has_notes_slide and slide.notes_slide.notes_text_frame.text.strip():
        print("[notes] " + slide.notes_slide.notes_text_frame.text)
