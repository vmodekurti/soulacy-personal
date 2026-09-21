#!/usr/bin/env python3
"""Fill PDF form fields.  usage: pdf_fill.py FORM.pdf ANSWERS.json OUT.pdf   (ANSWERS = {field: value})"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

pypdf = need("pypdf", "pypdf")
src, answers, out = sys.argv[1:4]
with open(answers) as f:
    values = json.load(f)
reader = pypdf.PdfReader(src)
writer = pypdf.PdfWriter()
writer.append(reader)
known = set((reader.get_fields() or {}).keys())
unknown = [k for k in values if k not in known]
for page in writer.pages:
    writer.update_page_form_field_values(page, {k: v for k, v in values.items() if k in known})
with open(out, "wb") as f:
    writer.write(f)
print(f"wrote {out}; filled {len(values) - len(unknown)} field(s)")
if unknown:
    print("UNKNOWN fields (not in the form):", ", ".join(unknown))
