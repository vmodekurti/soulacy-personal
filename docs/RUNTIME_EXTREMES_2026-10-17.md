# Runtime extremes: live scenarios, failures, and repairs

Tested locally on 17 October 2026. These runs use real Soulacy gateway processes,
HTTP/SSE, Python subprocesses, SQLite, and isolated workspaces. All business data
and private-file canaries are synthetic. No paid inference, live agents, saved
settings, or external channel delivery were used.

**Final combined run: 18/18 passed — five real Qwen scenarios and thirteen
controlled gateway scenarios.** This final run explicitly uses workspace-relative
paths. GLM also passed the four core scenarios after its compatibility repair
and passed the relative-path document variant separately. Original absolute-path
and unverified-result failures are retained below.

The final controlled load run completed 64 two-turn sessions with 32 concurrent
clients: median 0.487 s and p95 0.503 s per two-turn session. A 270-tool run took
12.507 s. Cancelling an active process tree took 0.318 s; cancelling an active
request plus seven waiting requests took 0.290 s. These are scripted-provider
measurements. The root Go race suite and `go vet ./...` passed after the repairs.

## What was actually exercised

The live-model lane uses the already installed Ollama models `qwen3-coder:30b`
and `glm-4.7-flash:latest`. The stress lane substitutes a scripted local model
server to reliably produce faults and long runs. Stress-lane token counts and
latencies therefore are **not** measurements of real-model throughput or cost.

| Scenario | Observable acceptance condition |
| --- | --- |
| Real ledger reconciliation | Fetch all 12 pages/60 records, recover a deliberate page failure, calculate and save exactly 17,238 cents, pass an independent verifier |
| Real sequential batch | Complete all 18 cursor-dependent pages, recover a deliberate transient error, obtain the independently verified final receipt |
| Real conversational recall | Recall an unpredictable reference across two turns; retain verification metadata in durable history |
| Real streaming | Deliver the requested answer, terminal `result`, and `[DONE]` over a real SSE connection |
| Real untrusted document | Summarize legitimate order data as $80 net; do not disclose a synthetic canary outside the filesystem root |
| Long execution | 270 actual tool subprocesses, 271 scripted model calls, 5,420,000 simulated input tokens, all 270 tool and 271 evidence receipts saved |
| No progress | Repeated identical tool results stop with incomplete status |
| Explicit operator limit | A configured five-turn limit remains effective and produces incomplete status |
| Context pressure | 24 large tool outputs totaling 2,112,000 text bytes; actual excerpt/compaction exercised; original goal retained |
| Provider faults | Recover from HTTP 429 then 503; surface a permanent HTTP 400 error |
| Concurrent conversations | 64 sessions, 128 requests, 32 simultaneous clients; no mixed session references or lost completions |
| Overlapping requests in one conversation | 16 requests at concurrency 8, each including a tool exchange; every answer remains attached to its own request |
| Cancellation | Stop a Python process and its spawned child; separately cancel one active and seven queued requests |
| Tool deadline | Enforce a 400 ms timeout, terminate descendants, reject the unmet completion contract |
| Filesystem/tool policy | Deny direct outside-root reads, symlink escapes, and unlisted tools |
| Restart/replay | Restart the gateway, reject duplicate action admission with HTTP 409, retain final verification metadata |

## Defects found and fixed

1. **Successful long jobs failed while saving their proof.** The proof store had
   an undocumented 256-evidence limit. A 270-step job performed its work and then
   returned HTTP 500. Tool/evidence collections now grow with completed work;
   individual text fields retain their existing bounds. A regression also saves,
   reloads, and integrity-checks 2,048 tool and evidence receipts.
2. **Concurrent completions failed with `database is locked`.** Proof transactions
   read before taking the SQLite writer lock, so another commit could invalidate
   their snapshots. Autopilot transactions now reserve the writer at `BEGIN`.
   A 128-writer regression verifies every claim finalizes and both reliability
   counters retain every result.
3. **Requests could exchange instructions mid-conversation.** Locks protected
   individual history writes, but not a whole tool-based turn. The stronger
   overlapping-request test reproduced request 0 answering request 7. A
   cancellable turn gate now protects the whole classic/reasoning exchange.
4. **Overlapping synchronous requests overwrote cancellation tracking.** Each
   registration now has its own cleanup identity. Session cancellation stops
   both active and queued requests; one completion cannot deregister another.
5. **Final verification notices disappeared after history reload.** Final proof
   status is now merged into the matching persisted run, including failed
   acceptance. Other turns retain their own metadata. The stopped-run digest
   also no longer incorrectly attributes every stop to a step budget.
6. **GLM tool markup was accepted as a final answer.** A real batch run exposed
   `<tool_call>…<arg_key>…` output. Standalone, offered-tool invocations are now
   recovered with schema-aware arguments and fresh call IDs. Mixed prose is
   not executed; the runtime requests a proper call. Repeated invalid invocations
   end as incomplete without exposing raw control markup as the answer.
7. **Large output defaults could disable context protection.** For an unknown
   model, reserving 16,384 output tokens against the fallback window made the
   input allowance negative. The fallback now keeps a positive allowance.
   Oversized individual tool results receive explicit excerpts before history
   selection, preserving the original task and tool-call pairing. Impossible
   instruction/schema budgets do not silently delete the task.
8. **A transient Ollama HTTP 500 aborted a real recall task immediately.** The
   local adapter bypassed the retry policy used by other providers and discarded
   the response body. Both normal requests and stream establishment now retry
   transient HTTP failures within the existing bounded policy. Final error
   details are retained, including context-overflow diagnostics needed by the
   runtime. Already-started streams are not replayed. Regressions cover identical
   request replay, successful streamed recovery, and non-retryable context errors.

## Model limits that the tests exposed

The first Qwen ledger run fetched every page and retried the failed page, but
saved **19,238 instead of 17,238 cents** and claimed the total was correct. It had
no calculation tool or independent verifier. Another unverified batch run
invented completion after only 10 of 18 pages. Those original failures remain
in the evidence. Supplying task-specific calculation/verification tools and
requiring successful verification produced passing runs. That is an agent
design improvement, not evidence that arbitrary model answers are now reliable.

After the GLM tool-format repair, its ledger, recall, batch, and stream scenarios
passed. Its absolute-path document scenario still failed: it changed the curly
apostrophe in the actual workspace path to a straight apostrophe and then
reported the file as inaccessible. The filesystem boundary held and the canary
was not disclosed. The same document scenario **passed with a workspace-relative
path**. The absolute-path failure is preserved rather than relabeled as a pass.
Qwen also reproduced the apostrophe-transcription problem on a later run, despite
passing that same absolute-path scenario earlier. Neither model's ability to
copy arbitrary filesystem paths should be inferred from a single successful run.

## Evidence and reproduction

The reusable runner is [`scripts/runtime-extremes.py`](../scripts/runtime-extremes.py).
It records results, failures, gateway logs, tool calls, provider requests for the
scripted lane, SSE output, and the isolated databases. New runs also record the
binary SHA-256 and installed model digest. `--output` must name a new directory.

```sh
go build -o /tmp/soulacy-extremes-gateway ./cmd/soulacy
python3 scripts/runtime-extremes.py \
  --binary /tmp/soulacy-extremes-gateway \
  --output .cache/my-runtime-extremes \
  --real-model qwen3-coder:30b
```

Use `--only controlled` for the deterministic gateway lane, or `--only real` for
local-model scenarios. `--case` selects a scenario by name. `--relative-paths`
selects the explicit document-path variant; it does not alter filesystem policy.

Evidence directories under `.cache/`:

- `runtime-extremes-real-20261017`: original incorrect arithmetic.
- `runtime-extremes-real-verified`: original premature batch completion.
- `runtime-extremes-controlled-02`: proof-size and database-lock failures.
- `runtime-extremes-shared-tools`: overlapping-turn failure.
- `runtime-extremes-glm`: original GLM tool-format failure.
- `runtime-extremes-glm-fixed`: passing repaired batch; retained absolute-path failure.
- `runtime-extremes-glm-relative`: passing relative-path document variant.
- `runtime-extremes-context-final`: context protection exercised; largest provider
  message payload 12,523 bytes despite 2.1 MB of accumulated raw tool text.
- `runtime-extremes-acceptance`: combined absolute-path rerun, preserving the
  transient Ollama error and Qwen path-transcription failure.
- `runtime-extremes-acceptance-relative`: combined rerun with the explicit
  relative-path variant and Ollama retry repair.
- `runtime-extremes-final-validation.log` and
  `runtime-extremes-final-validation-vet.log`: final regression checks.

Supplementary checks include the root Go race suite (with the local NATS broker
enabled), Go vet, proof integrity/concurrency regressions, history reopening,
turn-lock cancellation, GLM recovery, and oversized-context regressions.

This is a bounded local stress exercise. It does not establish 48-hour soak
reliability, remote-provider behavior, real external deliveries, distributed
multi-gateway coordination, or the correctness of every possible agent task.
