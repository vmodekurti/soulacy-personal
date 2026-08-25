# Soulacy core E2E verification — 2026-08-24

## Environment

- Team mode, PostgreSQL tenancy, OIDC login
- Local gateway: `http://localhost:1947`
- Workspace: RajSri Workspace
- Builder/runtime model: `ollama-cloud / glm-5.2`
- Container MCP servers observed: Maverick (37 tools), Stock Scanner (44 tools), Weather (6 tools)
- Test agent: `e2e-qa-market-risk-analyst`

## Outcome summary

| Scenario | Initial result | Final result |
|---|---|---|
| Build a complex market-risk agent in Studio | Failed semantically: requested name lost, all 81 MCP tools and 12 unrelated skills attached, invalid 12-step setting, output capped too low | Pass: requested name preserved, exactly six relevant MCP tools, no unrelated skills, valid 8-step plan limit, 8192 output-token allowance |
| Run the complex agent with real MCP calls | Completed but final prose was truncated; completed run remained shown as in-flight | Pass: grounded AAPL/MSFT result completed without truncation; terminal event removes run from in-flight state |
| MCP discovery and execution | Concurrent first use could initialize one server more than once; installed-server UI/runtime state could race | Pass: first-use initialization is coalesced; installed container servers expose namespaced tools and the test agent invoked them |
| Tool/source failure | Invalid ticker had multiple empty/error responses | Pass: successful ticker evidence preserved; every failed source was labelled; unavailable ticker received zero confidence; no fabricated data |
| Token budget too small | Run stopped before its next model call | Pass as designed: safe pre-call halt, usage/limit shown, recommended next-run budget and deployment ceiling presented, retry controls available |
| Explicit manual invocation | Refiner could convert it into a schedule | Pass: explicit manual/chat intent overrides a model-invented schedule |
| Parallel multi-agent Studio workflow | Builder returned empty twice; old fallback flattened researchers/critic/coordinator into a two-node linear digest | Pass: no silent flattening. A deterministic 2–6 specialist topology now supplies a parallel fan-out, join barrier, partial-failure policy, critic, and coordinator when the builder is empty |
| Synthesized workflow peers use MCP tools | Peer agents were created without the parent's approved tool/MCP allowlist | Pass: peers inherit only the parent's explicit workspace-scoped capabilities, skills, and knowledge |
| Full gateway tests | Reported PASS, then failed after 60 seconds with `Test I/O incomplete` | Pass: restart route no longer recursively launches the Go test binary and inherits its output pipes |

## Scenario details and reproduction

### 1. Complex agent authoring and successful execution

Prompt shape:

1. Create a manually invoked named market-risk analyst.
2. Accept two tickers and a risk horizon.
3. Use only selected Maverick/Stock Scanner MCP tools.
4. Return a comparison table, Mermaid diagram, and recommendation in chat.
5. Preserve partial results when a source fails.

Observed initial failures:

- Studio replaced the requested name with a generic name.
- The phrase “only available MCP market-data tools” expanded to the entire installed MCP catalog plus unrelated skills.
- `max_plan_steps: 12` violated the platform limit.
- The original prompt was not passed through the compile pipeline.
- `max_tokens: 0` used a response allowance too small for a tool-rich report.

Final verification:

- Agent saved/enabled as `E2E QA Market Risk Analyst`.
- Six relevant MCP tools and zero unrelated skills were attached.
- AAPL vs MSFT completed with real MCP evidence and a full final response.
- Chat rendered the result, chart/diagram content, and Thinking activity.

### 2. Controlled MCP/source failure

Reproduce with the test agent:

```text
Compare AAPL and NOTAREALTICKERZZZ for a medium-term horizon. Use every assigned source, preserve partial results, and explicitly list failures.
```

Observed failures for the invalid symbol included empty price history, null fundamentals, missing filings/CIK, empty technicals, and a Reddit ticker validation error. The run still completed in about 62 seconds, preserved AAPL evidence, marked the invalid ticker unavailable, assigned it 0/100 confidence, and produced the requested decision table, Mermaid flow, and recommendation.

### 3. Controlled token-budget failure

Reproduction used a temporary `budget.max_tokens: 5000` on the test agent, followed by the same multi-source market request.

Observed behavior:

```text
Run paused before the next model call because its prompt no longer fits the run token budget.
Used about 0 of 5000 tokens. Recommended next-run token budget: 20000.
```

The halt occurred before an over-budget model call, the UI displayed one-click retry/manual/default controls, and the deployment ceiling remained enforced. The test agent was restored to `100000` tokens afterward.

### 4. Parallel multi-agent workflow

Prompt shape:

```text
Build a workflow named E2E QA Competitive Intelligence Council. Run two independent market researchers in parallel, then a risk critic and coordinator. Continue with partial results and return one chat-only report.
```

Initial failure reproduction:

1. Open Studio and select Workflow.
2. Generate the prompt above with `ollama-cloud / glm-5.2`.
3. The builder can return an empty response (observed twice, after long generation waits).
4. The previous fallback silently produced a two-node market digest with incorrect guessed tool arguments.

Fix and final observation:

- Empty builder responses receive one bounded retry.
- If still empty, Soulacy builds a topology-preserving deterministic council instead of a straight line.
- The real Studio UI rendered a validated parallel workflow with specialist nodes, a named `risk_critic` barrier, coordinator, synthesized peer profiles, and Save enabled.
- Worker cardinality and requested name are read from the original user prompt; detailed instructions continue to use the refined prompt.
- The incorrect four-worker draft observed during regression testing was not saved. A regression test now proves that a refined prompt cannot inflate an original two-worker request.

### 5. MCP installation/runtime path

The workspace's previously installed container MCP servers were used rather than adding and deleting another user-visible server during this pass. End-to-end validation covered the post-install path:

1. MCP page showed all three isolated container servers.
2. Tool discovery returned 37, 44, and 6 tools respectively.
3. Weather's expanded card exposed its discovered schemas.
4. Studio selected namespaced MCP tools.
5. Runtime successfully invoked Maverick and Stock Scanner tools from the generated agent.

Known installation failures already reproduced in this workspace—missing Docker/credential helper, unsupported repository layout, repository identity mismatch, and stdio closing before initialization—remain surfaced as explicit installation or connection errors rather than disappearing records.

## Bugs fixed

- Original Studio intent/name lost between GUI, gateway, and compiler.
- Manual invocation overwritten by a model-invented schedule.
- Broad MCP/skill attachment when the operator requested only relevant MCP capabilities.
- Invalid Plan-Execute step default.
- Response truncation for complex MCP reports.
- Terminal runs retained in the in-flight session tracker.
- Duplicate MCP server initialization on concurrent first use.
- Overlapping Studio preflight dialogs during generation/refinement.
- Parallel workflow silently flattened after an empty builder response.
- Refined prose changing requested worker cardinality/name.
- Synthesized peer agents missing approved MCP/tool capabilities.
- Recursive gateway test restart causing `Test I/O incomplete`.
- OIDC refresh sessions, token rotation, replay-family revocation, and logout
  revocation being lost whenever the gateway restarted.

## Automated verification

Passed:

```text
go test -p 1 ./... -count=1
make build
GUI: 87 test files, 892 tests
```

The default fully parallel `go test ./...` can exhaust the local Docker test runner and killed one isolation probe once; that exact probe passed immediately in isolation, and the complete suite passed with package parallelism bounded to one. This is test-host resource contention, not a host-execution fallback—the isolation test remains fail-closed.

## Restart-session persistence verification

JWT/OIDC refresh state now uses an atomic, owner-only journal in the deployment
data directory. Only SHA-256 token hashes and identity/revocation metadata are
persisted; raw bearer refresh tokens are never written. Automated restart tests
prove that a refresh continues after issuer reconstruction, workspace tenancy
survives rotation, consumed-token replay remains detectable after another
restart, and replay revokes the complete rotated family. Corrupt files and
symlink store paths fail closed during gateway initialization.
