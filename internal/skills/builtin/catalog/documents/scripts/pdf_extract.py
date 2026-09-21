#!/usr/bin/env python3
"""Print the text of a PDF, page by page.  usage: pdf_extract.py FILE [--pages 1-5]"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

pypdf = need("pypdf", "pypdf")

ap = argparse.ArgumentParser()
ap.add_argument("file")
ap.add_argument("--pages", help="range like 1-5 or 3 (1-based)")
a = ap.parse_args()
reader = pypdf.PdfReader(a.file)
n = len(reader.pages)
lo, hi = 1, n
if a.pages:
    parts = a.pages.split("-")
    lo = int(parts[0]); hi = int(parts[-1])
print(f"# {os.path.basename(a.file)} — {n} page(s); showing {lo}-{min(hi, n)}")
empty = 0
for i in range(lo, min(hi, n) + 1):
    text = (reader.pages[i - 1].extract_text() or "").strip()
    if not text:
        empty += 1
    print(f"\n--- page {i} ---\n{text}")
if empty and empty == min(hi, n) - lo + 1:
    print("\nNOTE: no extractable text — this looks like a scanned PDF (image only).")
