# Execution policy

Soulacy uses shared, generous execution defaults. Restrictive budgets are
optional; explicit saved settings remain authoritative. Updating the application
does not erase budgets already present in config.yaml or SOUL.yaml.

## Everyday and advanced settings

The portal opens Settings and the agent editor in Everyday view. Settings is
available in the simple menu. Everyday covers the provider, search, memory,
updates and support; the agent editor keeps purpose, model, tools and permissions
visible. Advanced reveals execution limits, model roles and tuning, memory
internals, and server administration. Switching views preserves pending edits
and does not disable existing overrides.

New agents start with the configured workspace provider and its default model,
inherit turn/output allowances, and leave nucleus sampling to the provider.
Workspace saves send changed fields; API sections that replace their members
retain their other saved values. A basic edit does not reset advanced limits,
Studio build allowances, cost policies, price metadata or plugin settings.

## Defaults and overrides

| Setting | New default | Override semantics |
| --- | --- | --- |
| runtime.default_max_turns | 256 | 0 means unlimited |
| runtime.max_turns_ceiling | 0 | Positive values cap all agents, including unlimited requests |
| runtime.default_budget.max_tokens / max_llm_calls | 0 / 0 | 0 means unlimited for that dimension |
| runtime.max_budget.max_tokens / max_llm_calls | 0 / 0 | Optional workspace ceilings |
| costs.default_max_output_tokens | 16384 | Per-response allowance; model capacity still applies |
| costs.max_output_tokens_ceiling | 0 | Optional workspace output ceiling |
| runtime.timeouts.run | 48h | "0" means no practical deadline; parent deadlines still apply |
| runtime.timeouts.http | 48h1m | Must accommodate the run; use "0" for unlimited runs |
| runtime.timeouts.llm / step / tool | 5m / 10m / 2m | Individual operation timeouts, separately configurable |
| llm.studio.max_build_tokens / max_build_cost_usd | 0 / 0 | Optional per-build token/spend limits |
| llm.studio.max_build_attempts | 32 | -1 means unlimited; 0 selects the default |
| llm.studio.max_build_elapsed | 48h | "0" means no practical elapsed-time limit |

Agent max_turns: 0 or omitted inherits the workspace value; -1 requests
unlimited turns. An omitted budget inherits the workspace default. An explicit
budget with zero values requests unlimited tokens/calls, subject to positive
workspace ceilings. run_timeout is inherited when absent and accepts "0".
An explicit reasoning.max_steps controls reasoning iterations; -1 is unlimited.
Workspace turn ceilings still apply to reasoning. Generated agents and Genie
monitors inherit the workspace policy rather than embedding shorter caps.
Studio preserves explicit overrides through editing and saving.

Settings → Runtime accepts zero to remove turn limits. Runtime preparation
metadata and run.limits events expose resolved execution policy. Saved custom
agents can still have shorter limits; change those explicitly when more work
is needed. New portal agents, bundled templates and execution profiles inherit output allowances
and new agents inherit turn limits. The editor has no fixed 50-turn or 8192-token
ceiling. Zero inherits; -1 requests unlimited turns. Paid provider limits,
model context/output capacity, permissions, and parent cancellation remain in
force. Token quotas configured separately in rate_limit and dollar budgets in
costs also remain effective.

## Progress and completion

The tool progress detector tracks a bounded window of result signatures.
Twenty consecutive repeated results stop the run as incomplete. Fresh results
reset the counter; changing arguments alone does not demonstrate progress.
This also detects alternating tools returning the same previous evidence.
Set no_progress_limit on the agent to tune the threshold, or 0 to disable it
for jobs where repeated acknowledgements are expected. This is a liveness
heuristic, not proof of task success.

The classic tool loop keeps tools available while repairing empty or
progress-only replies. Exhausting turns, budget, or the progress threshold
produces incomplete metadata instead of a success claim. Both graph and step
workflows evaluate declared outcome assertions. With no assertions, completion
is explicitly unverified rather than independently verified.

For the classic tool loop, context pressure triggers extractive compaction
before old messages are discarded. It preserves the goal, latest request,
system instructions and recent tool exchanges, and labels older excerpts and
omissions. An individual tool result larger than the input allowance is also
excerpted, with an omission marker. Compaction does not mutate the original
messages; configured logging and retention policies still govern stored evidence.
Output reservation cannot make the fallback input allowance negative. If system
instructions and schemas alone exceed the estimated allowance, dropping the
task cannot resolve that conflict: the provider's actual limit is surfaced.
This does not make context unlimited or provide a semantic summary;
original history remains subject to the configured retention policy.

## Workflow recovery

Workflow checkpoints use the runtime run ID. Retrying a matching request with
the same ID restores completed steps, including their outcome evidence. Run
identity includes the user, session, authenticated principal, input and workflow
version. Concurrent retries in the same process serialize. Legacy checkpoints without
a verified request identity require reconciliation rather than automatic reuse. An interrupted
in-progress step is refused because its external action may already have
succeeded; reconcile that result before retrying. Checkpoint storage failures
are surfaced rather than treated as missing state. This supports explicit
same-ID recovery, not automatic replay on process startup or exactly-once
execution across external services.

## OpenClaw reference

Reviewed upstream commit
[469521a10633c06ce216a7b193cc11f7efbcf95a](https://github.com/openclaw/openclaw/tree/469521a10633c06ce216a7b193cc11f7efbcf95a),
fetched October 17, 2026. Relevant reference implementations:

- [Run timeout resolution](https://github.com/openclaw/openclaw/blob/469521a10633c06ce216a7b193cc11f7efbcf95a/src/agents/timeout.ts)
- [Tool loop detection](https://github.com/openclaw/openclaw/blob/469521a10633c06ce216a7b193cc11f7efbcf95a/src/agents/tool-loop-detection.ts)

The shared principles are configurable generous defaults, explicit unlimited
settings, progress-based stall detection and preserving useful context.
Soulacy's Go runtime implements these within its existing architecture; this
is not a wholesale OpenClaw port or a claim of feature parity.

## Delivery and long-running work

Independent tool call IDs execute fresh reads even when arguments match. A
replayed call ID reuses its result, preserving its success/failure status.
Scheduler run ownership lasts until execution actually exits, so a run beyond
one hour does not permit overlapping cron work. Partial cron results count as
failures and are not recorded or announced as completed schedules.

HTTP and SSE replies include completion metadata; SSE sends a named `result`
event containing the final answer, session ID, run ID and status even for a
non-streaming provider. Conversation history stores reply metadata in an additive
SQLite column; existing text history remains intact. Chat preserves status
through local reloads and durable-history reopening. Terminal run events also
include metadata for reconnect recovery. Autopilot's final proof is recorded in
its proof store and terminal event. SQLite conversation entries are updated by
run ID when final verification arrives, preserving unverified/incomplete notices
on reload without changing another run's status.

Classic/reasoning turns in the same agent conversation serialize across their
whole model/tool exchange. Separate conversations remain concurrent. Cancelling
a synchronous conversation stops its active and queued requests; completing one
request does not remove cancellation tracking for another.

Proof receipt counts do not impose an additional execution limit. Every observed
tool/evidence receipt is retained; individual text fields remain bounded. Proof
finalization reserves the SQLite writer before reading so concurrent completions
can wait rather than fail while promoting stale read snapshots.

The Python SDK's `chat_response` returns full metadata and accepts `session_id`;
`chat` keeps its string-returning interface. Its default request timeout is
48 hours plus one minute, configurable with `None` or `0` for unlimited waiting.
