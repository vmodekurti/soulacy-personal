# Template Setup Guides

Templates are vetted, ready-to-run agents. Each declares the providers, tools,
MCP servers, secrets, channels, and schedule it needs, so the **Template Install
Wizard** can show a readiness checklist before you install and only enable
install once everything is satisfied.

## The install flow

1. **Readiness checklist.** The wizard lists every requirement — provider, tools,
   MCP servers, secrets, channels — and shows what's already satisfied.
2. **Mock test.** Run the agent against a mock with no real external side effects,
   to confirm the shape of the workflow.
3. **Real test.** After secrets are configured, run a real test end-to-end.
4. **Schedule & routing.** Optionally create the template's schedule and output
   channel routing.
5. **Install.** A failed install rolls back cleanly — you won't be left with a
   half-broken agent — and the wizard tells you exactly what to fix.

## Flagship templates

- [Weather](weather.md) — current conditions and forecasts on demand or on a
  schedule.
- [Stock Screener](stock-screener.md) — screen equities by your criteria.
- [Deal Finder](deal-finder.md) — find and rank flight/travel/shopping deals.
- [Research Librarian](research-librarian.md) — sourced research briefs.
- [Document Ingestion](document-ingestion.md) — build a knowledge base from your
  documents and answer from it.
- [Inbox / Meeting Assistant](inbox-meeting.md) — triage email and produce
  meeting minutes.

Each guide lists exact requirements and the fastest path to a working agent. If
something fails, see [Common failures](../troubleshooting/common-failures.md).
