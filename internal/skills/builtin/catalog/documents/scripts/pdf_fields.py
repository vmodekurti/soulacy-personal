#!/usr/bin/env python3
"""List fillable form fields of a PDF as JSON {name: value}.  usage: pdf_fields.py FORM.pdf"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
from _deps import need  # noqa: E402

pypdf = need("pypdf", "pypdf")
reader = pypdf.PdfReader(sys.argv[1])
fields = reader.get_fields() or {}
print(json.dumps({k: (v.get("/V") if hasattr(v, "get") else None) for k, v in fields.items()}, indent=2, default=str))
