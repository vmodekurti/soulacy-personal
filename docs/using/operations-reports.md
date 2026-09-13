# Report on Soulacy's own operations

**Useful for:** answering “What did my agents do this week?”, “Where did the
model spending go?”, and “What should I investigate?” This is operational
reporting about Soulacy, not an agent writing a research report.

## Before you start

- Use a gateway version containing **System → Reports** and its reporting API.
- Sign in with the administrator key, or an administrator identity that has
  `metrics:read`. Operator/viewer roles cannot read gateway-wide reports.
- Enable a durable activity backend (SQLite or PostgreSQL) and the cost ledger
  for both sections. Missing sources are explicitly marked **unavailable**;
  they are never filled with invented zeros.

Reports are read-only. Opening or exporting one does not call a model, send a
message, start an agent, change permissions, or modify logging configuration.
There is no automatic delivery or new native iPhone report screen in this version.
The web report works on narrow screens.

## Your first report

1. Open **Reports** in the **System** section of the web sidebar.
2. Choose **Last 24 hours**, **Last 7 days**, or **Last 30 days**.
3. Check **Both sources available**, **Partial data**, or **Data unavailable**.
   Read **Sources and limitations** before interpreting the numbers.
4. Read the top cards: incoming requests, reply-complete sessions, sessions
   with errors, and recorded estimated model cost.
5. Use **Activity by agent**, **Cost by agent**, and **Cost by model** to find
   the source of unexpected activity or spend.
6. Use **Check next** to identify missing pricing, rejected model requests,
   and sessions needing investigation. **Inspect Runs** opens the existing
   activity screen; it does not retry anything.
7. Choose **Markdown**, **CSV**, or **JSON** to download the exact snapshot you
   reviewed. Refresh first if you want new data. Refresh clears the old report
   so a failed refresh cannot leave old totals looking current.

These are rolling UTC periods, not local calendar days. Both backends use the
same start-inclusive/end-exclusive boundaries, shown on screen and in exports.
The end is rounded to a second to match the usage ledger. Buffered events may
appear on the next refresh. The stores are sampled separately, so they are not
a transactional cross-database snapshot.

## Understand the counts

| Metric | What it means—and what it does not mean |
| --- | --- |
| Incoming requests | Persisted `message.in` events in the period. Not model calls. |
| Sessions | Distinct agent + conversation IDs with an incoming request in the period. Several requests can share a session. Events without a session ID do not invent one. |
| Reply-complete sessions | Replies cover incoming requests and no error/dead-letter event was recorded in the period. This does **not** verify answer quality, tool success, or delivery to an external recipient. |
| Sessions with errors | At least one error or dead-letter event. An error may subsequently have recovered; inspect the underlying run before retrying. |
| Unresolved sessions | Other started sessions with fewer replies than requests. Could be active, interrupted, or cross a time boundary—not automatically failed or hung. |
| Model requests | Cost-ledger records, including requests rejected before execution. One incoming request can lead to several model requests. |
| Provider attempts | Recorded provider attempts, including retries. Do not add this to model requests; it is a different measure. |
| Failed / rejected model requests | Failed recorded outcomes and pre-execution rejections are separate. Older records without a status are not assumed to have failed. |
| Recorded model cost | Estimated USD from the ledger, not the provider's invoice. Excludes hosting, external tools, and other unrecorded charges. Older USD-only records are included. |
| Unknown pricing | Requests without a usable pricing status; zero recorded cost does not make these free. Explicitly free models are separate from unknown models. Rejected requests are excluded from this warning. |
| Single-request reply latency | Time from input to reply for sessions with exactly one of each and no error in the window. Average and nearest-rank p95 include the displayed sample count. Multi-request conversations are excluded. |

Boundary-crossing sessions may be partial. Retention, disabled logging, dropped
events, or older instrumentation can reduce coverage. No records does not prove
that nothing happened or that the gateway stayed online. This is not an uptime,
SLA, billing reconciliation, or answer-quality report.

## Worked example: your weekly operations review

The following numbers are **fictional**, not measurements of your installation.

Suppose the weekly report shows 120 incoming requests in 40 sessions: 35
reply-complete, 3 with errors, and 2 unresolved. The ledger shows $2.40 recorded
model cost, but 8 requests have unknown pricing.

1. Do **not** report “35 of 120 runs succeeded.” Requests and sessions are
   different units, and a reply is not a verified business outcome.
2. Inspect the 3 error sessions in **Runs**. Check whether the errors recovered
   and whether any external action already happened before rerunning anything.
3. Check the 2 unresolved sessions. If they are still running, give them time;
   do not restart the gateway just to make the report look clean.
4. Find the 8 unpriced requests in **Cost by model**. Review the corresponding
   pricing setup under your provider/cost configuration. Treat $2.40 as the
   recorded estimate, not a complete invoice; correcting pricing does not
   automatically reprice historical ledger rows.
5. Export Markdown for a human-readable review and CSV for your spreadsheet.
   Make changes only after inspecting the evidence, then compare a later period.

## Export safely

Reports omit chat bodies, prompts, tool arguments, raw errors, session IDs,
and user identities. They still contain potentially private agent/model names
and usage totals. Review the export before sharing it.

- **Markdown:** summary, attention items, tables, and limitations.
- **CSV:** one row per metric, with source status and UTC boundaries. Money is
  exported as `cost_micros`: divide by **1,000,000** for USD. Formula-like text
  identifiers are neutralized for spreadsheet import.
- **JSON:** versioned structured snapshot, useful for your own reporting tools.

Reports are not saved in browser local/session storage. Changing sign-in clears
the displayed report; downloads you already saved remain on your device.

## API

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $SOULACY_API_KEY" \
  "https://YOUR_GATEWAY/api/v1/reports/operations?window=7d"
```

Only `24h`, `7d`, and `30d` are accepted. The JSON has `schema_version: 1`,
`start`, `end`, `generated_at`, `status`, `sources`, `activity`, `usage`, and
`notes`. A missing section is `null`, not zero. A `200` response can still have
`status: partial` or `unavailable`: inspect source status before using totals.
Responses use `Cache-Control: no-store`.

The report queries share the configured HTTP timeout with a ceiling of ten
seconds for the whole report. Activity is bounded at 100,000
agent/session groups and usage at 10,000 agent/provider/model groups. Exceeding
either limit makes that section unavailable; totals are never silently truncated.

## Troubleshooting

| What you see | What to check |
| --- | --- |
| Administrator access required | Role and `metrics:read` scope. Do not broaden other people's access just to produce a report. |
| Gateway does not support reports | Update the gateway as well as the web UI; coordinate a safe restart after active work drains. |
| Partial data | Read which source is missing; check the durable activity backend or cost-ledger configuration. |
| No retained activity / model usage | Time window, retention, whether any work ran, and whether logging/accounting was enabled then. |
| Query failed or limit reached | Try a shorter period; check gateway/database health. Do not interpret this as zero activity. |
| Sign-in changed | Refresh under the new identity. Old in-flight responses cannot repopulate the previous report. |

Continue with [Dashboard & Activity](dashboard.md), [cost controls](../LLM_COST_CONTROLS.md),
and [schedules](schedules.md) when you need the underlying operational controls.
