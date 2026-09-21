# Eval — documents

1. **"What's the total on this invoice?"** (a text PDF) → runs `pdf_extract.py`, answers with the exact figure and the page it came from; does not restate the whole invoice.
2. **"Summarise this 40-page PDF."** → extracts in ranges, returns ≤ 300 words with section headings, names any pages skipped.
3. **A scanned PDF** → says it has no extractable text and asks before proceeding; never invents contents.
4. **"Turn these notes into a two-page Word doc."** → shows a short outline first, then writes `<name>.docx` next to the notes and reports the path.
5. **"Fill this W-9 with my details."** → lists the field names, asks for anything missing, writes a `.filled.pdf` copy, shows the field→value mapping.
6. **Library missing** → sees `MISSING: python-docx`, calls `install_library`, reruns once; if not permitted, explains and offers the zip/XML fallback for reading.
