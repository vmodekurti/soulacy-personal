# Studio Authors Python — Design

_Status: proposal / RFC. Captures the "Studio can write Python, deploy it, and
wire it into a workflow" direction. Running example throughout: the NotebookLM
podcast workflow._

## 1. The idea, in one line

Studio can already turn intent into a graph. The next leap: **Studio can author
the code a step needs, and the runtime already knows how to run code safely.**
Your platform ships a Python executor (process-per-call, sandboxed) and treats
every `*.py` in `~/.soulacy/soulspace/tools/` as a first-class tool (auto-scanned into the
catalog, watched for changes). So we are not building an execution engine — we
are letting the LLM write the script and dropping it into a node.

## 2. Two tiers of the same feature

The "inline custom block" and the "generated reusable tool" are the same
capability at two altitudes. Design them as one continuum with a promotion path.

**Tier A — Inline Python block (glue).** A new node kind you drag from the
palette ("Custom Python"). The script lives *in the workflow draft*. Upstream
node outputs arrive as a typed `inputs` dict; the script returns a value
captured into the node's output var. Zero registration. Ideal for: parsing a
CLI's stdout, reshaping data, formatting a message, or shelling out to a local
CLI. Edited (or generated) in the Inspector.

**Tier B — Promoted / generated tool (reuse).** When an inline block earns its
keep, **promote it**: Studio writes it to the tools dir, it is auto-registered,
and it appears in the palette as a typed, reusable tool (`notebooklm.add_sources`)
usable across workflows. The compiler can also do this *proactively*: when intent
references a capability with no matching tool ("NotebookLM CLI is installed"), it
scaffolds the tool instead of failing the compile.

Mental model: Tier A is writing a lambda inline; Tier B is "extract function."

## 3. Why it fits the existing architecture

| Need | Existing primitive |
|---|---|
| Run code | `internal/executor/process` (process-per-call) + sandbox (rlimits, env allowlist) |
| Register a tool | drop a `*.py` in `~/.soulacy/soulspace/tools/`; `scanPythonTools()` + the fs watcher pick it up |
| Show in palette | `/tool-catalog` → palette `Tools` group (now via the integrated Studio) |
| Trust before live | the dry-run **test bench** (mocks + assertions) already exists |
| Gate privilege | `tier.Explain()` classifies Privileged/Active/ReadOnly; Studio's plan/consent enforces it |
| Long-running steps | workflow **checkpoints** (durable execution) for async jobs |

The LLM writes the code, the sandbox runs it, the test bench proves it, the tier
classifier gates it. Studio is the only surface that has all four in one place.

## 4. Node model

Add a `python` (a.k.a. `code`) flow node kind alongside `tool`/`agent`/`branch`:

```jsonc
{
  "id": "make_podcast",
  "kind": "python",
  "code": "…inline script…",        // Tier A; absent for Tier B
  "tool": "notebooklm.generate",     // Tier B: references a deployed tool instead
  "inputs":  [{ "name": "urls", "type": "string[]" }],
  "outputs": [{ "name": "audio_url", "type": "string" }],
  "params":  { "timeout_s": 600 },
  "requires": ["system", "network"]  // declared capabilities (drives consent)
}
```

- `code` present → inline; the runtime materialises it as an ephemeral
  process-per-call invocation. `tool` present → a deployed `*.py`.
- `inputs`/`outputs` are the existing typed ports, so a code node wires into the
  graph and `validate` checks its contract like any other node.
- `requires` is new and is the heart of the security model (§5).

## 5. Security & consent — the crux

This is where "creative" must meet "careful." Your sandbox docs are honest that
it is **resource limits, not isolation**. Running LLM-authored code raises the
stakes, so:

1. **Capability classification from the code itself.** Static-inspect imports /
   calls. A pure data transform (`json`, `re`, arithmetic) is **ReadOnly** and
   needs no consent. Anything importing `subprocess`/`os.system`/`socket`/
   `requests`/file writes escalates to **system** and/or **network**, sets the
   node's `requires`, and feeds `tier.Explain` so saving the workflow hits the
   existing consent gate. Reuse the tier machinery; extend it to read code nodes.
2. **Capabilities stay default-deny.** Same as today: `system` needs the server
   flag (`runtime.allow_system_tools`) AND the agent capability; `network` is
   allow-listed. A code node that wants them is refused unless the operator has
   granted them — and the consent dialog names exactly why.
3. **Secrets via the vault, never inline.** Generated code reads creds (CLI auth,
   API keys) from env injected through the allowlist from the secrets vault. The
   author/LLM never sees or hardcodes a secret.
4. **Dependencies.** Declared per node (`params.pip: [...]`), installed into the
   sandbox via the gated `install_library` path; pinned + cached. No implicit
   network installs at run time.
5. **Isolation roadmap (the real ceiling).** rlimits are not a security boundary.
   Before this is safe for *non-trusted* authors, code nodes that hold `system`
   should run under real isolation (container / nsjail / seccomp). Until then,
   treat code execution as "operator-trusted," make that explicit in the UI, and
   keep the consent gate loud.

## 6. Execution & test-bench

- **Run:** the flow engine, on hitting a `python` node, renders `inputs` from
  flow vars, invokes the process executor (inline `code` or deployed `tool`),
  and captures stdout/return → the node's `output` var. `on_error` (retry/skip/
  abort) and `timeout_s` apply.
- **Test before live:** the dry-run bench executes code nodes against sample
  inputs in the sandbox, with the option to **mock** them (fake `audio_url`) so
  you can validate the whole graph without side effects. Assertions check the
  shape. This is the loop that makes generated code trustworthy.

## 7. Promote-to-tool & compiler-authored tools

- **Promote:** "Save as reusable tool" writes the inline `code` to
  `~/.soulacy/soulspace/tools/<name>.py` with a typed header (name, params, doc), the
  watcher registers it, and the node is rewritten to reference `tool: <name>`.
  Versioned; the draft records the version it was promoted at.
- **Compiler-authored:** `suggestMissing()` already detects capability gaps.
  Extend it: for a gap that looks like a local CLI/library, offer "author a
  tool" — the LLM scaffolds the `*.py` wrapper (typed signature, shells to the
  CLI, parses output, handles errors), runs it once in the sandbox against a
  mock, and wires the workflow to it. Failing compiles become *offers to build*,
  not dead ends.

## 8. NotebookLM walkthrough (end-to-end)

Intent: _"every other day at 7am, fetch top 10 AI articles, add them as sources
to a new NotebookLM notebook, generate a podcast, notify me on a channel."_

```
schedule(cron 0 7 */2 * *, + alternating-day state guard)
  → agent: curate_top_ai_articles        (web_search → urls[])
  → python: notebooklm_pipeline           (requires: system, network)
        inputs: urls[]
        - shell: nlm create-notebook            → notebook_id
        - shell: nlm add-sources <id> <urls>
        - shell: nlm gen-podcast <id>           → job_id
        - poll until ready (checkpoint-friendly) → audio_url
        outputs: audio_url
  → tool: channel.send                     (audio_url → message on the channel)
```

Tier A gets you here today-ish: one Custom Python block does the NotebookLM
dance; it is consent-gated (it shells out), and you mock it in the bench before
the first real run. Tier B later splits those shell calls into clean
`notebooklm.*` tools.

## 9. Phased build plan

1. **Custom Python block (Tier A).** Node kind + palette block + Inspector code
   editor + sandbox execution + test-bench support + import-based capability
   classification feeding consent. _Foundation for everything else._
2. **Promote-to-tool (Tier B).** "Save as tool" → write `*.py` → palette.
3. **Compiler-authored tools.** Capability-gap → scaffold + deploy + wire.
4. **Async/durable steps.** Code node "kick off + await" on checkpoints, for
   long jobs like podcast generation; real stateful scheduling.
5. **Isolation hardening.** Container/nsjail for `system`-tier code nodes.

## 10. Open decisions

- New `python` kind vs. reuse `tool` with an inline-code field? (Leaning: new
  kind — cleaner classification and UI.)
- Where inline code is stored in the draft / how it round-trips through
  save→agent. Does a code node become an inline step in the agent def, or always
  promote to a file on save?
- Trust posture before isolation lands: operator-only authoring, or allow
  ReadOnly (no `system`/`network`) code for everyone and gate the rest?
- Dependency policy: allow pip at all, or a curated allowlist of packages?

---

## 11. Phase 1b — execution path & classifier (implementation plan)

Grounded in the actual code. Phase 1 made code nodes *authorable*; 1b makes them
*run*, *classified*, and *consent-gated*. Build in the order below; each step is
independently compilable/testable on a CGO-capable machine.

### 11.1 The inline contract

A Custom Python node's `code` defines a single entry function:

```python
def run(inputs):       # inputs = this node's rendered input, parsed from JSON
    ...                # return a JSON-serialisable value (or a str)
    return result
```

`inputs` is the node's `Input` template output (default `{}` when empty). The
return value becomes the node's `Output` flow var. This matches the Phase-1
starter and keeps glue code intuitive (one object in, one value out) rather than
the `**kwargs` convention used by file-based tools.

### 11.2 Execution (makes nodes run)

The process executor already runs inline code under the sandbox
(`internal/executor/process`: `Run(ctx, pyFile, funcName, inline, argsJSON)`,
`cmd.Env = sandbox.FilteredEnviron(nil)`). Two small changes:

1. **`buildScript` (process.go):** add an `inline != "" && funcName != ""`
   branch that wraps the user code with the same read-stdin → call-func → print
   harness the `pyFile` path uses, but passing a single positional arg:

   ```text
   import sys as _sys, json
   _orig = _sys.stdout; _sys.stdout = _sys.stderr   # quarantine user prints
   <USER CODE>
   _args = json.loads(_sys.stdin.read() or "{}")
   _r = run(_args)
   _sys.stdout = _orig
   print(_r if isinstance(_r, str) else json.dumps(_r))
   ```

   (stdout is redirected to stderr while user code loads so stray prints don't
   corrupt the result, then restored to emit the JSON — mirrors the file harness.)

2. **Engine method (engine.go):**
   ```go
   func (e *Engine) RunInlinePython(ctx context.Context, code string, argsJSON []byte) (json.RawMessage, error)
   ```
   calls `e.pyExecutor.Run(ctx, "", "run", code, argsJSON)` (fallback to the
   legacy exec-per-call path when `pyExecutor == nil`) and returns the unwrapped
   JSON. `params.timeout_s` → a `context.WithTimeout`; rlimits already enforced
   by the sandbox.

3. **Flow bridge (internal/runtime/flow.go `runNode`):** add, before the
   tool/agent dispatch —
   ```go
   if node.Kind == sdkr.FlowNodePython && node.Code != "" {
       in := renderedInput; if in == "" { in = "{}" }
       return w.engine.RunInlinePython(ctx, node.Code, []byte(in))
   }
   ```
   A python node that instead references a deployed tool (`node.Tool != ""`)
   falls through to the existing `RunTool` path. `on_error`/retry already apply
   via `reasoning.RunFlow`.

_Tests (Mac): executor inline round-trip; flow integration running a `print`-free
`def run` node; timeout honored._

### 11.3 Capability classifier (makes nodes honest)

New `internal/studio/codeclass` (pure Go, testable here):
`func Classify(code string) (requires []string, dynamic bool)`.

- Static scan of imports/usage:
  - `subprocess`, `os.system`, `os.popen`, `pty`, file writes (`open(...,"w"/"a")`) → **`system`**
  - `socket`, `http`, `urllib`, `requests`, `httpx`, `aiohttp` → **`network`**
  - only stdlib data (`json`, `re`, `math`, `datetime`, `itertools`, …) → **none (ReadOnly)**
- `dynamic = true` when it sees `eval`/`exec`/`__import__`/`getattr(`-style
  indirection — flagged loudly (static analysis can't see through these; treat
  as needs-review, not auto-safe).
- Persist the result: add `Requires []string` to `sdkr.FlowNode` and set it on
  the node at compile/save (Studio) and recompute on edit. Surface it in the
  Inspector as read-only chips ("needs: system, network") so the author sees
  *why* consent will be asked.

This is a **guardrail + consent prompt, not a security boundary** — say so in the
doc and UI. Real isolation is Phase 5.

### 11.4 Consent (reuses the existing gate — no new UI)

In Studio's `save`/`plan` agent-definition derivation: for each python node, fold
its `Requires` into the derived `agent.Definition` so `tier.Explain` sees it —
e.g. a `system`-requiring node contributes a privileged-builtin marker (or sets
`capabilities:[system]`), pushing the workflow to **Privileged**. The existing
`plan.go` path then sets `requiresConsent` for privileged+channel workflows and
the current consent dialog fires verbatim. Runtime stays default-deny: a
`system` code node only executes when `runtime.allow_system_tools` + the agent
capability are present, exactly like `shell_exec`.

_Test (here): a workflow with a `subprocess` python node classifies Privileged
and `Plan.RequiresConsent == true`._

### 11.5 Test bench

The dry-run `studio/test` path already supports per-node **mocks** keyed by node
id. Extend its node executor with the same python case (§11.2) so un-mocked code
nodes run against sample input, while mocked ones return the fake value — letting
you validate the NotebookLM graph with a fake `audio_url` before granting any
`system` capability.

### 11.6 Build order

1. Inline harness + `RunInlinePython` + flow `python` case (+ tests) — **nodes run.**
2. `codeclass` + `Requires` field + Inspector chips (+ tests) — **nodes are honest.**
3. Consent wiring in `save`/`plan` (+ test) — **nodes are gated.**
4. Test-bench python execution/mocks — **nodes are provable before live.**

### 11.7 Open risks

- Classifier is bypassable (`eval`, dynamic import) — mitigated by the `dynamic`
  flag + loud consent, not solved. Phase 5 (container/nsjail) is the real fix.
- Output size / runaway loops — cap stdout and rely on sandbox cpu/mem rlimits;
  surface stderr on failure.
- Secrets: code reads creds from env (vault → allowlist), never inline.

---

## 12. Capability Resolver — existing vs MCP vs Python, and inline MCP creation

The Python-tool work answers "how does a step get code." This section answers
the prior question: **when a capability is missing, what *kind* of thing should
fill it — an existing tool, an MCP server (connect or create), or a Python
tool — and how do we let the user resolve it without leaving Studio.**

### 12.1 The decision, made at compile time

`Suggestion.Kind` already enumerates `tool | agent | skill | mcp`, and
`suggestMissing()` already detects gaps. Extend the compiler (LLM-assisted) to
classify each gap's *shape* and emit a ranked resolution, cheapest first:

1. **Already available** — the capability is in the catalog → just wire it. (No
   resolver needed.)
2. **Existing community MCP** — the gap names an external *service/product*
   (NotebookLM, Slack, Notion, GitHub, Gmail…) that likely has a published MCP
   server. Emit `Suggestion{Kind:"mcp", Name, Reason}` and pre-search the MCP
   registry/Glama. Resolution = **connect** it.
3. **Local CLI / bespoke glue** — a single local command, file op, or data
   transform → a **Python tool** (inline block or promoted), per §1–11.
4. **Net-new reusable service integration, no community MCP** — multi-operation,
   auth-bearing, reusable, and nothing exists to connect → **generate a new MCP
   server**. Heaviest path; rare.

**MCP-vs-Python heuristic** (what "figure out" means): *named external service +
auth + multiple operations (especially with a known MCP) → MCP; single local CLI
call / transform / file glue → Python tool.* The LLM proposes; the resolver shows
the recommendation and lets the user override. Cost/risk ranking is deliberate:
**connect-existing > python-tool > generate-new-MCP** — never scaffold a server
when connecting one (or a 20-line Python tool) will do.

### 12.2 The mechanism — a Capability Resolver dialog

The "popup" the gap triggers. Studio already has a discover/install panel for
tool/skill suggestions (the registry search + stage flow); generalise it into a
resolver that, per gap, offers tabs:

- **Connect MCP** — runs `GET /mcp/registry/search?q=` (and Glama). Each hit has a
  "Connect" action → `POST /mcp/provision-registry` (or `/provision-glama`). The
  response's `env_required` drives inline secret fields written to the **vault**
  (never inline). On success the server's `mcp__<server>__<tool>` tools appear in
  the catalog/palette and the gap node is rewired to them. `POST /mcp/test`
  validates the connection before wiring.
- **Python tool** — "Use a Custom Python block" (drop an inline node, §1–11) or
  "Generate a tool" (LLM scaffolds `~/.soulacy/soulspace/tools/<name>.py`).
- **Generate MCP server** — scaffold a minimal stdio MCP server exposing the
  needed tools, register via `POST /mcp`, `POST /mcp/test`, then wire. Gated and
  rare (see risks).

Because `mcp` is already a Suggestion kind and the MCP endpoints already exist,
this is mostly **frontend** (the resolver dialog) plus a compiler change to emit
shaped suggestions — not new backend plumbing.

### 12.3 NotebookLM, revisited through the resolver

On compiling the NotebookLM intent, the resolver fires on the "NotebookLM" gap:

- It first searches the MCP registry. *If* a NotebookLM MCP exists → recommend
  **Connect** (one click + Google auth via `env_required`), and the workflow uses
  `mcp__notebooklm__{create,add_sources,gen_podcast}` — clean, typed, reusable.
- *If not* (likely today) → the user already said "NotebookLM CLI is installed,"
  so recommend a **Python tool** wrapping the CLI (the §8 path). The resolver
  makes that an explicit, ranked choice rather than a silent fallback.
- "Generate a new NotebookLM MCP server" is offered but de-emphasised — only
  worth it if they want a durable, shareable integration.

### 12.4 Honest risks

- **Generating a correct net-new MCP server is hard** — it's a long-lived
  protocol process with lifecycle, auth, and error handling, far heavier than a
  Python tool. Keep it the last resort; always `/mcp/test` before wiring.
- **Connecting a third-party MCP runs external code** — same trust posture as
  system tools. Provisioning mutates global MCP config (a persistent connection,
  unlike an ephemeral Python node), so it must be explicit, consented, and
  scoped/disable-able from the MCP page.
- **Secrets** come from `env_required` → vault, surfaced in the dialog, never
  written into the draft or code.
- **Determinism** — the LLM's MCP-vs-Python call won't always be right; the
  resolver must always let the user override and pick a different tab.

---

## 13. Consent policy — beyond-guardrail execution is per-case

**Principle (overrides the coarser posture in §5/§11.4): anything that runs
beyond the default guardrails is allowed only case by case, with sufficient
warning and explicit user consent — never by a single blanket switch.**

### 13.1 The guardrail line

- **Inside guardrails (no consent):** ReadOnly code — pure data transforms,
  stdlib compute, no `subprocess`/network/file-write, no external process, no
  MCP connection. Runs freely in the sandbox.
- **Beyond guardrails (per-case consent required):** anything the classifier
  marks `system`/`network`/`dynamic`; shelling out to a CLI; connecting or
  generating an MCP server; installing a dependency. Each such capability, at
  each node, is its own consent decision.

### 13.2 What "per-case" means

- **Not a global grant.** `runtime.allow_system_tools` is a *ceiling* the
  operator can lower to off — it is **not** a blanket "yes." Even with it on,
  every beyond-guardrail node still needs its own approval. Approving one node
  never auto-approves another.
- **Bound to the exact thing.** A grant is tied to a **content hash** of the node
  (the code / command / MCP spec) plus its classified capabilities. Edit the code
  and the prior consent is **void** — re-prompt. This stops "approve once, then
  the code silently changes."
- **Scoped and revocable.** On approval the user picks a scope: *this run only*,
  *this workflow until edited*, or *until revoked*. Grants are listed on the
  workflow and individually revocable. Nothing is "forever."
- **Audited.** Every grant records who/when/what (capability, node, hash, scope).

### 13.3 Sufficient warning

The consent dialog for a beyond-guardrail node must show, plainly:

- **What it will do** — the classified capabilities (`system`, `network`,
  `dynamic`) in human terms, plus the **actual code / shell command / network
  destinations / MCP server + endpoints** it will run. No hidden behaviour.
- **Why it's risky** — "this runs commands on your machine," "this sends data to
  the network," "this connects third-party code," and that the sandbox is
  resource-limiting, *not* isolation (§5.5).
- **An affirmative action** — typed/explicit confirm, defaulting to decline. The
  `dynamic` flag (code the classifier can't see through) raises the warning level.

### 13.4 Enforcement

- **Author/save time:** Studio's `plan`/`save` consent gate becomes per-node:
  one `ConsentItem` per beyond-guardrail node (extend the existing
  `kind:"channel"` item with `kind:"code"`/`kind:"mcp"`, carrying the node id,
  capabilities, and content hash). Save is blocked until each is granted or the
  node is removed.
- **Run time (defence in depth):** the engine refuses to execute a
  beyond-guardrail node unless a **valid, matching** grant exists (hash + scope
  satisfied) AND the operator ceiling permits it. A revoked/expired/stale-hash
  grant fails closed — the node errors rather than running unconsented.
- **Test bench:** dry-runs default to **mocked** for beyond-guardrail nodes; a
  real (un-mocked) execution in the bench is itself a per-case consent.

This mirrors the platform's own action-permission model: explicit, per-action,
per-session, never generalised from one approval to the next.

---

## 14. Promote-to-tool — implementation notes & open items

Building the deployable file is done (`internal/studio.BuildPromotedTool`, tested):
it maps a requested name + inline `def run(inputs)` body into `<ident>.py` with a
docstring header and an entry function named after the tool that forwards kwargs
to `run`. Two things were learned grounding it, and they gate the endpoint:

1. **Call convention.** Deployed python tools are invoked as
   `getattr(module, <toolname>)(**kwargs)` (engine `executePythonTool`), i.e. the
   entry function name equals the tool name and args are KEYWORD args — different
   from the inline node's single-positional `run(inputs)`. `BuildPromotedTool`
   bridges this by generating `def <toolname>(**inputs): return run(inputs)`.
   Tool names are therefore forced to valid Python identifiers (`toPyIdent`), so
   a dotted name like `notebooklm.add_sources` becomes `notebooklm_add_sources`.
2. **Consent must not be bypassed.** A promoted tool node is `kind=tool`, so the
   runtime's per-case `consent.Authorize` (which only fires for inline `code`)
   would NOT gate it — promotion would otherwise launder beyond-guardrail code
   into an ungated tool. So the **promote endpoint must require consent for the
   code's capabilities before writing the file** (reuse the grant flow), making
   promotion a deliberate, recorded escalation into operator-trusted tools.

**Open items before wiring `POST /studio/promote`:** confirm that a `*.py`
dropped in the workspace tools dir is auto-registered as a *callable* tool (the
catalog scan lists it; verify the flow `tool: <name>` path resolves to it and
with which funcName), then add the consented endpoint + an Inspector "Promote to
reusable tool" action that writes the file and rewrites the node to `tool:
<name>` (clearing `code`). Held until that runtime callability is verified on a
real build rather than wired blind.
