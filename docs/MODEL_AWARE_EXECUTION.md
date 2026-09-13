# Model-aware execution

Soulacy now prepares an agent for the model it will actually use. It adapts the
working method, not the user's goal, completion criteria, permissions or budget.
This applies to runs started from web, iOS, schedules and delegated agents.
Existing saved agent definitions are not rewritten.

## What happens before a run

1. Resolve the selected provider and its concrete default model. Explicit model
   choices remain authoritative. An explicitly configured reasoning strategy
   retains the existing global reasoner fallback; automatic mode never switches
   providers or models to obtain more capabilities.
2. Check the agent's provider/model allowlists before metadata discovery.
3. Read bounded, cached model metadata. No completion, benchmark prompt, model
   download or tool execution is performed to discover capabilities.
4. Add a short working guide to a per-run clone of the system prompt. Small
   evidenced context windows get concise, checked milestones; larger windows
   get relevant-evidence comparison guidance. Reported reasoning support adds
   assumption/dependency checking, not a request to expose private reasoning.
5. For automatic agents, retain native tools when supported or unreported. When
   a model reports no native tools, use the existing guarded JSON/ReAct protocol.
   Tool authorization, argument validation and approvals still apply.
6. Emit `agent.prepared` with the model profile, selected strategy, approach,
   warnings and any blocking incompatibility. The event contains no task text,
   credentials, endpoint URLs or provider templates.

If automatic fallback cannot preserve a forced-tool choice (including `none`)
or a structured-output contract, preparation blocks the run and explains why.
It does not erase those requirements to make the run appear successful. Explicit
strategies remain configured as authored, including existing system-tool safety
rules. Authored workflows are not rewritten: each model call is checked against
its own model, and delegated agents prepare themselves. Tool-only workflows and
message routers do not require a root model.

## Evidence, not model-name guesses

| Provider | Evidence used |
| --- | --- |
| Ollama | `/api/show` capability flags; the smaller of a positive configured `num_ctx` and the reported architectural context maximum |
| Gemini | Model details: separate input/output limits, generation methods and the optional thinking flag |
| OpenAI-compatible, Anthropic and other providers without an optional profiler | Concrete adapter default where available; unreported capabilities stay **unknown** |

The Ollama adapter reports its JSON-format API support, not the model's ability
to produce correct arguments. If `num_ctx` is unset/non-positive, the architecture
maximum is **not** reported as the serving window. Gemini input and output limits
are not combined into an invented shared window. Vision support is informational;
this change does not add new image/audio ingestion.

Metadata availability and feature flags are not intelligence scores. There is no
unverified “strong model” rating, name-based capability inference or claim that a
model is best at a task. Unknown providers retain compatible guarded behavior;
existing context-estimation fallback remains when no evidenced limit is known.

The metadata formats follow [Ollama model details](https://docs.ollama.com/api-reference/show-model-details),
[Ollama context configuration](https://docs.ollama.com/faq) and
[Gemini model details](https://ai.google.dev/api/models).

## Runtime boundaries

- Discovery uses the configured provider endpoint only: two-second bound, 2 MiB
  response ceiling, no redirects and no retries. Keys stay in headers.
- Profiles are cached for five minutes, failed lookups for 30 seconds; the cache
  is capped at 128 entries and coalesces concurrent requests for one model.
  Provider re-registration invalidates prior profiles. An inference call uses a
  coherent adapter/default/profile snapshot even during replacement.
- Invalid capability values and limits are normalized. Provider descriptions,
  errors, templates and free-form advice never become operating instructions.
- Every governed router completion checks reported chat/tool/JSON compatibility
  and context/output limits, including final synthesis and workflow LLM nodes.
  It rejects oversized immutable prompts instead of silently removing goals.
  History trimming still preserves the system prefix and latest user request.
- Output ceilings only decrease. An otherwise-unbounded call with an evidenced
  ceiling uses a conservative output reserve. Automatic fallback also caps phase
  output and step allowances to the agent's existing output/turn limits.
- Token and call budgets are enforced at the router, not just in the native loop.
  Nested and parallel agents share ancestor reservations. Streams remain reserved
  until drained; uncertain provider failures keep their estimated charge.
  These are conservative token estimates, not guarantees about provider billing
  or an API that ignores requested limits. Dollar-budget enforcement is unchanged.
- The model-call deadline covers the entire response stream, not just opening
  it. A timed-out/cancelled stream is an interrupted run, not a successful empty
  answer. Streaming tokens and completion are covered by repeated regressions.
- No extra tool permissions, reasoning spend, learned memories or saved settings
  are granted by preparation. A blocked/unknown capability is visible rather than
  silently selecting a paid model or weakening a contract.

## Web, iOS and API

The web agent editor and iOS agent detail include **Model preparation**. They
show the resolved model, working approach, evidenced limits and unknowns.
The web preview is for the saved configuration: save edits before refreshing.
Both clients discard stale reads after identity/connection changes and render
provider/model text as text, not HTML or executable content.

`GET /api/v1/agents/:id/model-preparation` is read-only, requires agent-read
authorization and returns `Cache-Control: no-store`. It checks scoped credentials
even without optional RBAC. Unknown agents return 404; invalid/disallowed model
configuration returns 422. A known incompatible model returns the preparation
with `blocked_reason`, so the UI can explain it before the next run. The actual
run refuses that preparation.

SDK providers remain source-compatible. They may optionally implement
`sdk/llm.DefaultModelProvider` and `sdk/llm.ModelProfiler`. Profilers must honor
context cancellation and perform metadata-only discovery. Provider wrappers
must explicitly forward these optional interfaces if they want to preserve them.

## Validation and limits

See [validation results](MODEL_AWARE_VALIDATION_2026-09-12.md). Tests include real
local-model inference, but this is not a general model-quality benchmark or a
proof that every task, provider or device works. Cloud deployment and physical
iPhone validation remain separate release gates when access is unavailable.
