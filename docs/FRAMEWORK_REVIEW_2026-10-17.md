# Framework and gateway portal review — October 17, 2026

This review follows execution from configuration and agent creation through
interactive chat, reasoning, workflows, scheduling, tool dispatch, local
processes, delivery, queues, history, and SDK clients. It extends the shared
execution-policy changes described in [Execution policy](EXECUTION_POLICY.md).
It is a risk-focused code and behavior review, not a claim that every integration
or every line has been exhaustively verified.

## Defaults and OpenClaw methodology

The reference is OpenClaw commit
[469521a10633c06ce216a7b193cc11f7efbcf95a](https://github.com/openclaw/openclaw/tree/469521a10633c06ce216a7b193cc11f7efbcf95a),
fetched October 17. Soulacy adopts shared configurable defaults, explicit
unlimited settings, progress-based stall detection, and preserving context
through long runs within its own architecture. This does not imply OpenClaw
feature parity.

New runs have no cumulative token or model-call budget by default. The shared
turn allowance is 256, output allowance 16,384 tokens, and run time 48 hours.
Workspace ceilings are opt-in. Unlimited execution can be requested explicitly;
provider capacity, permissions and parent cancellation still apply. Existing
saved limits remain authoritative. New portal agents, bundled templates and model profiles now
inherit these allowances instead of silently introducing smaller limits.

## Findings fixed

| Area | Failure before this change | Result |
| --- | --- | --- |
| Tool permissions | Hidden builtins could still execute if a model named them. Linear step workflows could bypass the normal dispatcher. | Discovery and execution share permission gates; loaded-agent workflows use the normal dispatcher. |
| Tool results | A repeated failed result could become a successful cached observation; identical new polling calls could reuse stale evidence. | Replayed call IDs retain failure state; new call IDs execute fresh reads. |
| Scheduling | A run older than one hour could lose its ownership lock even while still executing. Partial replies could be counted and announced as completed schedules. | Ownership lasts until the run exits; incomplete cron runs publish failure and do not advance successful completion state. |
| Local processes | Cancelling a shell could leave descendants holding output pipes open. | Unix local tool commands use a process group and cancel the group; pipe waiting is bounded. |
| Provider errors | The final retried HTTP response body was closed before the adapter could read its explanation. | Final error responses remain readable by the caller. |
| Queue contracts | Memory subscribers could share mutable payload bytes, remain registered after cancellation, or accept work after closure. SDK acknowledgement could run repeatedly. NATS ignored cancellation and retained durable consumers for temporary subscriptions. | Payloads are isolated; subscriptions clean up; cancelled/closed operations return errors; successful acknowledgement is idempotent and failed acknowledgement can retry. NATS uses ephemeral consumers and cleans up on cancellation/close. |
| Streaming chat | Non-streaming agents could finish with only an SSE done marker and no answer; every request created a new session. | Final answer fallback plus a named result event; callers can continue their session. |
| Completion evidence | Metadata was attached after message emission, omitted by the HTTP response, and lost through history/reloads. | Runtime emits status with replies; HTTP/SSE carry it; an additive history migration retains status; terminal events support recovery. |
| Reconnection | Transient tool errors, another session's events, or an earlier identical question could be mistaken for the current result. | Recovery uses the session, pre-send history boundary and terminal run events; partial result status survives recovery. |
| Python SDK | Short default requests could give up while long runs continued; string-only replies hid status and session continuity. | Generous configurable timeout, explicit unlimited waiting, session continuation and a full `chat_response` API; string-returning `chat` remains available. |
| Portal defaults | New agents used five turns and 512 output tokens; editor fields capped turns at 50 and output at 8,192. | New agents inherit workspace defaults; arbitrary form ceilings removed; inheritance and unlimited semantics explained. |
| Portal status | Chat, Genie, playground and mobile could present partial results without qualification. | Visible completion notices; Chat retains them on reload and history reopening. |
| Portal setup/settings | Empty Chat asked users to select a nonexistent agent; settings waited for auxiliary checks; technical JSON occupied a permanent column; canonical duration strings left a blank selector. | Setup links, independently loading checks, optional technical details, correct frequency selection, mobile field stacking and editor actions that no longer cover the form on small screens. |
| Diagrams | Sanitization removed Mermaid's HTML labels, leaving unlabeled boxes. | Mermaid uses SVG text labels while the sanitizer continues to block embedded HTML. |
| Dependencies | Vulnerable Go standard-library/network versions and browser dependencies were pinned. | Go/build pins updated to 1.26.9, x/net to 0.60.0 and compatible browser patches installed. |

The earlier harness pass also added outcome assertions for both workflow forms,
identity-bound same-run checkpoint recovery, extractive context compaction,
progress detection, and shared budgets for Studio, Genie and reasoning. See the
execution-policy document for exact semantics and recovery limits.

## Validation

- Full root Go suite with the race detector; separate Go SDK suite with the race detector; `go vet ./...`.
- Regression smoke suite and GUI-embedded gateway build.
- Frontend: 106 test files, 1,042 tests passing.
- Local NATS 2.15.1 broker: actual publish/acknowledge, ephemeral-consumer cleanup, cancellation, idempotent unsubscribe and backend shutdown under the race detector. CI installs the same disposable test broker.
- Python SDK: four request-contract tests passing; added Python SDK and separate Go SDK checks to CI.
- Clean-workspace public acceptance run: 21 passed, zero failed, four intentionally skipped credential/model-dependent checks.
- Chrome browser smoke covers 13 portal routes at desktop 1440×960 and mobile 390×844. Interaction fixtures test incomplete notices, reload persistence, mathematical text, diagram labels, the update selector, technical-details toggle and mobile layout. The actual agent editor also saves and reloads 512 turns and 32,768 output tokens through the real API, and shows the resolved allowances panel. Chat replies in this browser test are deterministic fixtures; they do not establish live-model quality.
- Meaningful regressions cover tool permission bypass, fresh polling versus duplicate invocation, cached failures, incomplete scheduled execution, legacy-history migration and reopen, subprocess descendants, queue cancellation/payload isolation, SDK acknowledgement, provider retry bodies and real-socket SSE completion/session handling.
- `npm audit`: zero vulnerabilities. `govulncheck`: zero affected symbols or imported packages. The module-level OpenPGP advisory GO-2026-5932 remains attached to x/crypto, but Soulacy does not import the affected package; CI rejects introducing that dependency.

Dependency references: [Go 1.26.9 release notes](https://go.dev/doc/devel/release#go1.26.9),
[Go HTTP advisory](https://pkg.go.dev/vuln/GO-2026-6613),
[KaTeX advisory](https://github.com/advisories/GHSA-238p-pmpm-9mq7),
[DOMPurify advisory](https://github.com/advisories/GHSA-p98j-92pf-mc4p),
[devalue advisory](https://github.com/advisories/GHSA-j22f-vq7h-c4qm).

Local validation evidence is saved under `.cache/framework-review-2026-10-17/`.
These logs and screenshots are local evidence, not required source artifacts.

## Practical limits and remaining work

- No live deployment, paid-model endurance run, real channel delivery, production
  broker or Postgres integration, Docker/SSH/cloud workload or Windows process-tree
  test was performed. Public acceptance deliberately skips credential-backed
  delivery. macOS Chrome testing does not establish Safari/iOS or Android parity.
- Autopilot's final proof metadata is recorded in the proof store and terminal
  event after base conversation persistence. Reopening history alone does not
  reconstruct later proof-state changes. Basic incomplete/outcome metadata is
  retained; a proof/history reconciliation layer remains separate work.
- Recovery serializes matching requests in one process. It does not provide
  distributed exactly-once side effects, automatic startup replay, or safe
  replay of an interrupted external action with an uncertain outcome.
- Context remains finite. Extractive compaction is not a semantic summary, and
  a no-progress heuristic can be disabled for intentional repeated polling.
- Existing accessibility warnings and large eager chart/highlighting bundles
  remain. The browser checks establish usable tested layouts and flows, not a
  complete accessibility or portal performance audit.
- Existing user-configured limits are not automatically removed. Changing the
  application defaults cannot override saved policy or a provider's capacity.

All code changes are local and reviewable. The live gateway and saved agents
were not reconfigured or deployed by this review.

## Settings usability follow-up

Settings and the agent editor now open in Everyday view, with Advanced available
for tuning and administration. Settings is reachable from the simple navigation.
New agents select the configured workspace provider/model and inherit generous
run allowances. Existing overrides remain active, and changing views keeps edits.

Configuration saves now send changes rather than rewriting the entire form.
Replacement-style API sections retain saved fields that the editor does not
expose, including Studio build limits and cost policies. Zero memory values and
pricing metadata survive basic edits. A leftover 512-token editor fallback was
removed. See [execution policy](EXECUTION_POLICY.md) for default semantics.

Validation: 108 frontend test files, 1,049 tests passed; production GUI and
embedded gateway builds passed. Chrome checked 13 routes and 13 interactions
at 1440×960 and 390×844, including real configuration/agent saves, preservation
of advanced configuration, edit retention across views and configured defaults
for new agents. Local screenshots and manifests are under
`.cache/settings-desktop/` and `.cache/settings-mobile/`; test/build logs are
`.cache/settings-all-tests.log` and `.cache/settings-build.log`. No live user
configuration was changed or deployed.

## Deployment follow-up

Deployed on October 17 to the local macOS background gateway at
http://127.0.0.1:18789. The installed version contained 69 newer commits than
the original review checkout. The deployment was reconciled with that installed
base (7fb362a7) in a separate worktree, preserving the newer portal, website
sign-ins, mission completion checks and all reviewed changes.

Running build: `review-20261017-7fb362a7`. Gateway health is `ok`; all eight
previously loaded agents remain present. Config and agent definition hashes
were unchanged. Both installed binaries match the validated artifacts. The
live portal passed Everyday/Advanced and mobile layout checks.

The combined source passed 1,080 frontend tests, the full Go race suite,
`go vet`, 18 runtime scenarios (five real-model and thirteen controlled), and
13 browser routes plus 13 interactions at desktop and phone sizes.

The source actually deployed, build artifacts, test evidence, and rollback
backup are retained at:

`/Users/clawagent/.soulacy/deployments/20261017-200958-framework-review`

Its `source/` worktree is the authoritative combined deployment source; this
original checkout remains the earlier review branch. `deployment.json` records
the artifact hashes and verification. `rollback/` contains the prior gateway,
CLI, service definition and workspace backup with 43 SQLite snapshots. No
GitHub release or remote production deployment was performed.
