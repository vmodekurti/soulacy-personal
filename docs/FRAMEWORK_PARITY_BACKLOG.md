# Soulacy Framework Parity Backlog

This backlog captures the major feature areas Soulacy should revisit to compete
with mature agent frameworks such as OpenClaw and Hermes.

## Major Pending Feature Areas

1. **Agent evals and benchmark harness** — MVP closed; keep expanding coverage
   - `sy eval` now supports reusable suites, golden cases, tags, repeats, fail-fast, secret skips, latency/token summaries, tool assertions, and delivery assertions. Remaining work is more domain coverage and cost accounting when providers expose cost metadata.

2. **First-class multi-agent orchestration** — MVP partially closed
   - Peer agents, router agents, auto-delegate, transitive safety tiers, configurable delegation depth, opt-in structured peer-result envelopes, and opt-in parallel peer fan-out now exist. Remaining work is richer team templates, reviewer/critic packaged patterns, and shared workspace semantics beyond queue/KB handoffs.

3. **Production deployment and tenancy**
   - Users, orgs, workspaces, roles, audit logs, hosted mode, environment separation, and secrets isolation.

4. **Marketplace and plugin ecosystem**
   - Installable tool packs, MCP packs, templates, skills, verified connectors, permissions, versioning, and trust signals.

5. **Browser/computer automation runtime**
   - Headless sidecar, session reuse, screenshots, downloads, lifecycle cleanup, replay traces, and process janitor.

6. **Advanced Studio debugging**
   - Step replay, variable inspector, mocks, breakpoints, failed-vs-fixed comparison, path explanation, and test generation from failures.

7. **Observability and ops console**
   - Run traces, token/cost tracking, provider latency, tool failure rates, schedule reliability, channel delivery state, and alerting.

8. **Memory and knowledge governance**
   - Review queues, source citation, expiry, conflict resolution, approvals, and per-agent memory policies.

9. **Human-in-the-loop approvals**
   - Consistent approve/deny flows across chat, schedules, Telegram, Slack, browser actions, and tool execution.

10. **Cloud sync and remote workers**
    - Remote executors, isolated execution, long-running jobs, GPU/browser workers, and queue-backed dispatch.

11. **Enterprise connectors**
    - Google Drive, Gmail, Calendar, Slack, Teams, Notion, Jira, GitHub, Linear, Salesforce, HubSpot, SharePoint, Snowflake, Postgres, and S3.

12. **Agent publishing and sharing** — MVP partially closed
    - Agents can now be exported as `.soulacy-agent.json` packages with redacted SOUL.yaml, packaged local tool files, bundled eval suites/sample prompts, setup requirements, integrity checksums, optional Ed25519 signatures, and a secrets checklist. The Agents GUI can inspect/import a package, surface missing providers/channels/skills/peer agents/files, show packaged validation harness files, restore packaged files, and import disabled for review. The CLI can export, inspect, import, and sign packages. Remaining work is changelog/version promotion and rollback across imported versions.

## Current Strategic Focus

Soulacy can already build and run useful agents. The parity gap is now mostly
around reliability, orchestration, debugging, deployment, observability, and
ecosystem maturity.
