# Learning notebook

Soulacy can turn useful experience into reusable guidance: personal preferences,
facts to recheck, and step-by-step procedures. New lessons are drafts until you
approve them. Corrections create new versions instead of overwriting history.
This is learning through memory and retrieval, not model-weight training.

## Use it

1. Enable **Learning notebook** in the agent editor. Enable **Propose useful
   lessons during tasks** if you want suggestions during ordinary work.
2. Tell the agent a lasting preference, correct a mistake, or work through a
   useful procedure. It can search earlier lessons and propose a new lesson or
   refinement using its normal tool loop. It may reasonably propose nothing.
3. On web, open **Brain Mem → Lessons**. On iPhone, open **Agents → your agent →
   Learning**. Both show the same notebook when using the same sign-in.
4. Review the exact guidance, supporting quotes, verification steps,
   limitations, and previous version when applicable. Acknowledge the review,
   then approve or reject. **Teach** can also turn an explicit note into a draft
   using one model call, without running tools.
5. Approved preferences and relevant facts appear in later runs. Procedures
   appear as short search results; the agent reads the full procedure only when
   needed. Give feedback or disable guidance that is wrong. Restoring an old
   version creates another draft requiring approval.

Approving a procedure does not execute it or grant tool permissions. Disabling
it stops future injection and reads; it cannot erase context already loaded by
an in-progress response or earlier messages in a conversation.

## Configuration

```yaml
learning:
  enabled: true
  auto_propose: true
  max_proposals: 2
```

`max_proposals` limits newly created drafts per run, clamped to 1–3 (omitted:
1). `auto_propose: false` asks the agent to propose only on your request;
approved guidance can still be used. `min_chars` remains readable for old
configurations but no longer drives excerpt generation.

If the agent uses an explicit `builtins` allowlist, add `learning.search`,
`learning.read`, and `learning.propose` to that list. Existing allowlists and
caller tool restrictions are never widened automatically. An empty list still
means no tools. The separate Teach action remains a tool-free, permission-gated
model call.

Authentication is required. Runtime learning is available to admin/operator
principals with agent-read and memory-write permission. API reads require
agent-read and memory-read; writes require memory-write. Agent object-access
rules also apply. URL query credentials are not accepted for notebook APIs.
Shared external-channel messages, unauthenticated runs, and mission simulations
do not collect private learning. Subagents cannot inherit another agent's
learning sources.

## What is remembered

- **Preference:** an explicit user preference; tool output alone cannot prove it.
- **Fact:** a factual observation with instructions for rechecking it. Failed
  tool output cannot substantiate a fact.
- **Skill:** a reusable procedure, including verification and optional pitfalls.
  Failure observations may explain pitfalls, but do not prove recovery worked.
- **Episode:** a bounded, redacted request/reply and run outcome, searchable as
  past context. An assistant reply is not admissible evidence for a new lesson.

Draft citations must exactly match current user or observed tool text. Only
cited excerpts are stored with the lesson. Tool evidence includes bounded
arguments and results; learning tools, delegated-agent outputs, session search,
and Safe Undo outputs are excluded from evidence collection. Source existence
does **not** prove that a model interpreted it correctly or that a workflow is
effective. Human review is the final safeguard.

Exact normalized guidance is deduplicated across runs, including
rejected/disabled revisions; semantic paraphrases can still need human cleanup.
Refinements reference the exact current active revision. Two
concurrent corrections cannot both replace it: a stale approval fails with a
conflict and must be refreshed/rebased. Only one revision per topic can be
active. Restores and repeated approvals are idempotent.

## Privacy, storage, and limits

The SQLite notebook is `learning-notebook.db` in the workspace's data directory,
using WAL, full synchronous commits, and file mode `0600`. Back up it and its
SQLite sidecars using a consistent SQLite backup or with the server stopped.
It is **not encrypted at rest**. Disk backups, server operators, configured
model providers and existing application logging policies remain trust
boundaries. Do not enter secrets; detection/redaction is heuristic and cannot
catch every secret or prompt injection.

Lessons and episodes are partitioned by authenticated subject and agent ID.
They are never copied into shared rulebooks, the global skills directory, or
Studio prompts. People sharing one API key share its identity and notebook;
use separate user identities or keys for separate private notebooks. A different
managed key is a different owner. Disabling learning does not delete history.

Per owner/agent:

| Limit | Behavior |
| --- | --- |
| 100 active lessons | Disable one before adding another; a refinement can replace its active base. |
| 200 pending, 2,000 retained revisions | New drafts/restores are refused at capacity; history is never silently deleted. Reaching the retained limit currently requires an operator-managed archival/migration decision. |
| 500 recent episodes | Oldest episodes expire automatically. |
| 5,000 usage receipts | Recent run/lesson pairs suppress double-counting. Very old replayed runs may count again. |
| 3,500 bytes of injected lesson JSON | Preferences/facts are bounded; procedure bodies load on demand. |
| 12,000 bytes of prior guidance in Teach | Only complete matching lessons are included. |
| Current user + first 8 eligible tools | Evidence collection is bounded; later tool results are not available for citations in that run. |

Search uses weighted keyword overlap, not embeddings or full-text database
ranking. It can miss paraphrases. Feedback is your single current vote per
revision, not a model-generated score. A load count measures retrieval, not
improvement. There is no automatic promotion based on ratings or usage.

With notebook storage wired, `session_search` uses the same private episode
scope and refuses cross-agent searches. It requires an authenticated,
learning-enabled run; it no longer falls back to agent-wide action-log history.
Old unscoped sessions are not imported as private episodes.

## API

Base: `/api/v1/agents/:id/learning`. All responses are `Cache-Control: no-store`.

| Method/path | Input or behavior |
| --- | --- |
| `GET /lessons` | `status=pending\|active\|archived\|superseded\|rejected`, `offset=0`; up to 100 per page. |
| `GET /lessons/:lesson` | Exact version, including cited excerpts. |
| `POST /teach` | `{"note":"..."}`; `{lesson,created}`; `lesson:null` means no useful draft. |
| `POST /lessons/:lesson/review` | `{"action":"approve\|reject\|archive\|restore","confirmed":true}`. |
| `POST /lessons/:lesson/feedback` | `{"rating":1}` or `{"rating":-1}`. |
| `GET /lessons/:lesson/export` | Download an active skill as a portable `SKILL.md` document. This does not install it. |

Mutations are not automatically retried by web/iOS after transport failure.
Refresh before retrying an uncertain result. HTTP 400 means invalid input, 404
also hides records outside your scope, 409 means a conflict/capacity limit, and
503 means availability or model failure; it is not a success receipt.

## Existing memory and Hermes comparison

The old automatic reply-excerpt generator and its startup sweeper are no
longer run. Existing proposals remain under **Earlier drafts**, with their
legacy acceptance semantics; already installed skills/rulebooks are not
deleted or silently imported. Shared legacy skill installation additionally
requires skills-write permission. Existing Brain Memory settings remain
independent: turn off procedural `auto_update` if you want all new behavioral
learning to use notebook review.

Hermes was the reference for skill creation/refinement, progressive loading,
bounded memory and curation. Its official documentation describes
[agent-managed skills](https://hermes-agent.nousresearch.com/docs/user-guide/features/skills),
[persistent memory](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory),
and [skill curation](https://hermes-agent.nousresearch.com/docs/user-guide/features/curator).
Reference checkout: `NousResearch/hermes-agent` commit
`1c671beab29164d8931c5d01c5739502267089d8`, inspected 12 September 2026.

This implementation adds that core learn/reuse/refine cycle to Soulacy with
mandatory review and private web/iOS control. It is **not a claim of complete
Hermes parity or equal learning quality**: no auxiliary background model,
automatic skill consolidation, Honcho integration, FTS5 recall, self-installed
scripts, or model-weight training was added. The normal agent loop uses its
existing model/tool budget; Teach makes one governed call using the agent's
provider, model, and data classification. Extraction quality still depends on
that model. See the accompanying validation report for measured results and
remaining release gates.
