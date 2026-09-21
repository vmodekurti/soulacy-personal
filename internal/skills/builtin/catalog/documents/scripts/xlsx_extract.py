#!/usr/bin/env python3
"""Print sheets of an .xlsx as tab-separated rows.  usage: xlsx_extract.py FILE [--sheet NAME] [--rows N]"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

openpyxl = need("openpyxl", "openpyxl")
ap = argparse.ArgumentParser()
ap.add_argument("file")
ap.add_argument("--sheet")
ap.add_argument("--rows", type=int, default=50)
a = ap.parse_args()
wb = openpyxl.load_workbook(a.file, read_only=True, data_only=True)
print("sheets:", ", ".join(f"{ws.title} ({ws.max_row}x{ws.max_column})" for ws in wb.worksheets))
for ws in wb.worksheets:
    if a.sheet and ws.title != a.sheet:
        continue
    print(f"\n--- {ws.title} (first {a.rows} rows) ---")
    for n, row in enumerate(ws.iter_rows(values_only=True), 1):
        if n > a.rows:
            print(f"... ({ws.max_row - a.rows} more rows)")
            break
        print("\t".join("" if v is None else str(v) for v in row))
