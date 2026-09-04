# Templates

The Templates tab turns a known-good starter into a working agent: pick a shipped workflow or starter, open **Install**, check readiness, run a mock test, choose optional schedule delivery, then deploy it as a normal agent.

## Quick start

1. Open **📋 Templates** in the GUI.
2. Under **Agentic workflows**, find **Meeting Minutes & Action Items** and click **Install**.
3. Review the readiness checks, keep or edit the suggested agent ID, and click **Mock test**.
4. Click **Install agent**, then go to **Chat**, select the new agent, and paste any meeting transcript.

API equivalents:

```bash
curl http://localhost:18789/api/v1/templates \
  -H "Authorization: Bearer $SOULACY_API_KEY"

curl -X POST http://localhost:18789/api/v1/templates/<name>/instantiate \
  -H "Authorization: Bearer $SOULACY_API_KEY"
```

## The four shipped workflows

These ship embedded in the binary and are tagged `workflow`:

### Meeting Minutes & Action Items

Paste (or forward via any channel) a raw meeting transcript — Zoom/Meet export, dictation, hand notes — and get structured minutes: attendees, decisions, and a numbered action-item list with owners and due dates, formatted to paste straight into the Workboard or any task tracker.

### Smart Inbox Triage

Forward emails or messages (or paste a batch into Chat): each item is classified **URGENT / NEEDS REPLY / FYI / NOISE** with a one-line reason, and everything in needs-reply gets a ready-to-edit draft in your voice. Nothing is ever sent automatically — every draft is for human review.

### Competitor & Market Monitor

A cron agent (`0 8 * * 1`, Mondays 8 AM) that tracks the companies and topics in its WATCHLIST using web search and page fetches, reporting **deltas, not noise**. It is created **disabled** on purpose — edit the system prompt's WATCHLIST first, then enable it and optionally point `schedule.output` at a chat bot (see [Schedules](schedules.md)).

### Document Compliance Auditor

Audits draft text against your reference policies using a knowledge base (RAG). Setup: create a KB on the Knowledge page, upload your policies/style guides/contracts, set the agent's **Knowledge bases** field to that KB, then paste any draft into Chat for a clause-by-clause compliance report with citations to the exact policy passages. See [Knowledge bases](knowledge.md).

## Starters

Below the workflows sit simpler starting points — including **Basic Chat** (one LLM, one prompt, no tools), **RAG over your docs**, **Scheduled briefing** (daily cron at 7 AM), and **Web researcher**. Use them as skeletons for your own agents.

## Guided Install

**Install** opens a guided wizard instead of writing YAML immediately. The wizard:

- checks live provider, secret, channel, and MCP readiness;
- previews what the agent would do with a no-side-effect mock test;
- lets cron/scheduled templates choose an output bot from configured Telegram, Slack, Discord, WhatsApp, or webhook mappings;
- auto-fills the destination and template when a channel mapping has defaults;
- creates a real agent whose `SOUL.yaml` lands in your agents directory.

Each template card shows a source badge:

- `embedded` — ships with the gateway,
- `user` — your own template from `~/.soulacy/templates/`.

The same catalog is reachable from the Agents page via **📋 From template…**, which additionally opens the new agent straight in the editor.

## Customizing afterwards

A template-created agent is a completely normal agent — nothing stays linked to the template:

- **Agents** page → edit the system prompt (e.g. the Market Monitor's WATCHLIST, the Inbox Triage classification rules), swap the LLM provider/model, attach knowledge bases, tools, or channels.
- **Schedule** page → change cron expressions, set the output bot, enable/disable, and send a delivery smoke test.
- Click **Validate** in the editor after substantial edits to catch problems before they hit a live run.

!!! tip
    Templates that need setup say so in their description ("follow the setup note in its description, go"). The two that won't work out-of-the-box without it: Market Monitor (fill the WATCHLIST, then enable) and Compliance Auditor (create + attach a KB).

Typical post-creation checklist:

1. Open the agent on the **Agents** page and skim its system prompt — every shipped template documents its own behavior there.
2. Replace placeholder content (watchlists, tone-of-voice notes) with yours.
3. Attach what it needs: a KB for RAG templates, an output bot for cron templates.
4. **Validate**, **Save**, then test it in the inline **💬 Test** playground before pointing real traffic at it.

## Adding your own templates

Drop agent-definition `*.yaml` files into `~/.soulacy/templates/` — they appear in the catalog with a `user` badge. Tag one `workflow` to have it listed in the Agentic workflows section.

!!! note
    Templates and **Studio** solve different problems: Templates install a known-good design; Studio generates or repairs a bespoke workflow from intent. Start with a template when one is close to what you need, and use Studio when the automation needs custom steps.
