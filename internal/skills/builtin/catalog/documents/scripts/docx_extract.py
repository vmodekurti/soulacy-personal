#!/usr/bin/env python3
"""Print paragraphs and tables of a .docx in order.  usage: docx_extract.py FILE"""
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

docx = need("docx", "python-docx")
d = docx.Document(sys.argv[1])
for p in d.paragraphs:
    if p.text.strip():
        prefix = "#" * int(p.style.name[-1]) + " " if p.style.name.startswith("Heading") and p.style.name[-1].isdigit() else ""
        print(prefix + p.text)
for ti, t in enumerate(d.tables, 1):
    print(f"\n--- table {ti} ---")
    for row in t.rows:
        print(" | ".join(c.text.strip() for c in row.cells))
