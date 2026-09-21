---
name: web-research
description: Research a question on the web and answer with real, checked sources. Use for "find out", "compare", "what's the latest on", "is it true that", prices, specs, news, or anything where a wrong or invented citation would matter.
license: Apache-2.0
allowed-tools: web_search fetch_url read_file write_file kb_search
metadata:
  builtin: "true"
  category: knowledge
---

# Web research

The failure mode to avoid is confident text with links that do not say what
you claim — or do not exist. Every citation you give must come from a page
you fetched in this run.

## Method

1. **Frame it.** Restate the question in one line, with the date range or
   region that matters. If the ask is ambiguous in a way that changes the
   answer (which product generation? which country's law?), ask one
   question — otherwise state your assumption and go.
2. **Search wide, read narrow.** `web_search` two or three phrasings. Pick
   the 3–6 most authoritative results: primary sources (the vendor, the
   law, the paper, the filing) beat commentary; recent beats old for
   anything that changes. Skip content farms.
3. **Fetch and read** each with `fetch_url`. Note the publication date on the
   page. If a page is paywalled or empty, say so — do not cite it.
4. **Cross-check** the key claims across at least two independent sources.
   Where sources disagree, report the disagreement instead of picking one.
5. **Answer first, then evidence.** Lead with the answer in 2–4 sentences.
   Then bullets, each ending with its source: `— [Site, date](url)`. Then a
   short "what I could not confirm / not found" line if anything is open.

## Rules

- Cite only URLs you fetched. Quote at most one short sentence per source.
- Prefer the page that *is* the fact (spec sheet, official docs, the ruling) over a page that talks about it.
- Numbers: give the unit, the date, and the source. If two sources give different numbers, show both.
- Say how fresh the answer is ("as of the sources' dates, latest 2026-09-…").
- When asked to *compare* options, use a table with the criteria the person named first, then ones you added — and say which are which.
- Do not pad. A question with a one-line answer gets a one-line answer plus the source.
- If the person's `kb_search` knowledge base has relevant material, check it first and say when a source is theirs rather than the web's.

## Save when useful

For anything the person will refer back to (a comparison, a how-to), offer to
save the brief with `write_file` as Markdown, sources included.
