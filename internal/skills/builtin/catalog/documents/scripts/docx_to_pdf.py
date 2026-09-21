#!/usr/bin/env python3
"""Convert .docx → .pdf with LibreOffice if present.  usage: docx_to_pdf.py IN.docx [OUTDIR]"""
import os
import shutil
import subprocess
import sys

soffice = shutil.which("soffice") or shutil.which("libreoffice")
if not soffice:
    print("MISSING: libreoffice (not installable here) — deliver the .docx instead")
    sys.exit(3)
src = sys.argv[1]
outdir = sys.argv[2] if len(sys.argv) > 2 else os.path.dirname(os.path.abspath(src))
subprocess.run([soffice, "--headless", "--convert-to", "pdf", "--outdir", outdir, src], check=True)
print("wrote", os.path.join(outdir, os.path.splitext(os.path.basename(src))[0] + ".pdf"))
