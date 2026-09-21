#!/usr/bin/env python3
"""Markdown → .docx (headings, bullets, numbered lists, bold/italic, simple pipe tables).
usage: docx_write.py IN.md OUT.docx"""
import os
import re
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

docx = need("docx", "python-docx")
src, out = sys.argv[1:3]
d = docx.Document()


def runs(par, text):
    for tok in re.split(r"(\*\*[^*]+\*\*|\*[^*]+\*)", text):
        if tok.startswith("**"):
            par.add_run(tok[2:-2]).bold = True
        elif tok.startswith("*"):
            par.add_run(tok[1:-1]).italic = True
        elif tok:
            par.add_run(tok)


lines = open(src, encoding="utf-8").read().splitlines()
i = 0
while i < len(lines):
    ln = lines[i]
    if ln.startswith("|") and i + 1 < len(lines) and re.match(r"^\|[\s:-|]+\|$", lines[i + 1]):
        rows = []
        while i < len(lines) and lines[i].startswith("|"):
            if not re.match(r"^\|[\s:-|]+\|$", lines[i]):
                rows.append([c.strip() for c in lines[i].strip("|").split("|")])
            i += 1
        t = d.add_table(rows=len(rows), cols=len(rows[0]))
        t.style = "Table Grid"
        for r, row in enumerate(rows):
            for c, cell in enumerate(row):
                t.cell(r, c).text = cell
        continue
    m = re.match(r"^(#{1,6})\s+(.*)", ln)
    if m:
        d.add_heading(m.group(2), level=len(m.group(1)))
    elif re.match(r"^\s*[-*]\s+", ln):
        runs(d.add_paragraph(style="List Bullet"), re.sub(r"^\s*[-*]\s+", "", ln))
    elif re.match(r"^\s*\d+[.)]\s+", ln):
        runs(d.add_paragraph(style="List Number"), re.sub(r"^\s*\d+[.)]\s+", "", ln))
    elif ln.strip():
        runs(d.add_paragraph(), ln)
    i += 1
d.save(out)
print(f"wrote {out}")
