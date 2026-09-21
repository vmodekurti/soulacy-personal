---
name: documents
description: Read, extract, fill, create and edit PDF, Word (.docx), Excel (.xlsx) and PowerPoint (.pptx) files. Use whenever a task involves an office document or PDF — summarising a contract, filling a form, building a deck or spreadsheet from data, or converting between formats.
license: Apache-2.0
allowed-tools: read_file write_file list_dir find_files run_script python_eval install_library read_skill_file
metadata:
  builtin: "true"
  category: productivity
---

# Documents

You work with office files through the small scripts bundled with this skill.
They live in `scripts/` next to this file; read one with `read_skill_file`
before you run it so you know its exact arguments. Run them with `run_script`.

## First: what does the person actually want?

Before touching a file, decide which of these it is — the output differs:

| Ask | Do |
|---|---|
| "What does this say?" / "Summarise" | extract text, then answer in prose; quote page numbers |
| "Pull out X" (dates, totals, names, tables) | extract, then return **only** X, as a table if there are several |
| "Fill in this form / template" | inspect fields first, ask for anything missing, then fill and save a **copy** |
| "Make me a document / deck / sheet" | draft an outline in chat, get a nod if the content is non-trivial, then build |
| "Convert / merge / split" | do it; report the output path and page counts |

Never overwrite the person's original. Write results next to it with a
suffix (`report.filled.pdf`, `notes.summary.docx`) and say where.

## Reading

- **PDF** → `scripts/pdf_extract.py <file> [--pages 1-5]` prints text per page. If it prints almost nothing, the PDF is scanned; say so and ask whether to proceed without OCR (do not guess contents).
- **DOCX** → `scripts/docx_extract.py <file>` prints paragraphs and tables in order.
- **XLSX** → `scripts/xlsx_extract.py <file> [--sheet NAME] [--rows 50]` prints sheets as CSV-ish text. Ask which sheet if there are many and the person did not say.
- **PPTX** → `scripts/pptx_extract.py <file>` prints slide-by-slide text and speaker notes.

Large files: extract only the pages/sheets you need; never paste more than
~2 000 words of raw content into the conversation — summarise and offer the
rest.

## Writing

- **DOCX** → write the content as Markdown to a temp file, then `scripts/docx_write.py <in.md> <out.docx>`. Headings, bullets, numbered lists, bold/italic and simple tables are supported.
- **XLSX** → `scripts/xlsx_write.py <data.json> <out.xlsx>` where the JSON is `{"Sheet name": [[header…],[row…],…]}`. Numbers stay numbers; the first row is bold with a filter.
- **PPTX** → `scripts/pptx_write.py <deck.json> <out.pptx>` where each slide is `{"title": "...", "bullets": ["..."], "notes": "..."}`. Keep to 3–6 bullets a slide; put detail in notes.
- **PDF** → build a DOCX first, then `scripts/docx_to_pdf.py` if LibreOffice is present; otherwise deliver the DOCX and say why.

## Filling forms

`scripts/pdf_fields.py <form.pdf>` lists field names and current values.
Map the person's answers to those names, then
`scripts/pdf_fill.py <form.pdf> <answers.json> <out.pdf>`. Show the mapping
before filling when any field name is ambiguous.

## When a library is missing

Each script prints `MISSING: <package>` and exits 3 if its Python dependency
is absent. Install it with `install_library` (`pypdf`, `python-docx`,
`openpyxl`, `python-pptx`) and rerun once. If installation is not permitted
in this deployment, fall back: read `.docx`/`.xlsx`/`.pptx` as zip+XML with
`python_eval` for extraction, and tell the person that creating files needs
the library.

## Quality bar

- Numbers and dates copied from a document are quoted exactly; do not round or reformat unless asked.
- When you summarise, name the document, its length (pages/slides/rows) and the sections you did not read.
- A deck or document you create gets a one-line "what changed / what I assumed" note in chat.
