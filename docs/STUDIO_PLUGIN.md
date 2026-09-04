# Soulacy Studio — Design & Roadmap

> **Update (ARCH-6): Studio is now built into the core dashboard.** It is no
> longer packaged as a sandboxed iframe plugin. The visual builder lives in the
> main GUI (`gui/src/pages/Studio.svelte` + `gui/src/lib/studio/`), is embedded
> into the gateway binary by `make gui`, and is reachable at `/studio` (nav
> entry "Studio"). It calls the `/api/v1/studio/*` endpoints directly with the
> user's authenticated session — there is no more `postMessage` RPC bridge, no
> `examples/plugins/studio` directory, and no `<workspace>/plugins/studio`
> install step. The historical plugin design below is retained for context;
> references to the iframe shell, the scoped plugin token, and the
> `/plugins/studio/ui/*` static hosting describe the superseded packaging.

**Studio** is the visual, plain-language workflow builder for Soulacy. It is a
layer on top of primitives Soulacy already has — it emits `WorkflowSpec` /
SOUL.yaml and never introduces a parallel runtime.

## Vision

The user describes intent in plain words; the framework compiles it into a
runnable workflow — choosing the trigger (channel, cron, webhook, manual), the
agent step(s), and the tool/skill/MCP bindings — then lets the user visually
inspect, adjust, and **test it end-to-end** before it goes live. The target user
knows *what they want to happen*, not how Soulacy wires it.

## Settled decisions

- **Surface:** web plugin UI in the gateway portal, built on `@xyflow/svelte`
  (the React-Flow family Langflow uses). The hand-rolled SwiftUI companion canvas
  is retired; the companion, if kept, embeds this web core later.
- **Intent engine:** hybrid — compile a draft graph immediately, then ask
  clarifying questions only where genuinely ambiguous or where a credential /
  permission is needed.
- **First milestone:** a thin vertical slice — intent → single-agent flow with
  one trigger + one tool/MCP, rendered on the canvas, runnable in the harness.

## Architecture (grounded in the current repo)

- **Plugin packaging.** Manifest `plugin.yaml` (schema v2, `pkg/plugin`), loaded
  by `internal/plugins/loader.go` from the dirs in `config.plugin_dirs`
  (`internal/config/config.go`; defaults to `<workspace>/plugins`). UI mounts via
  `gui.static` and is served at `GET /plugins/:id/ui/*`; the portal shows it in a
  sandboxed iframe (`allow-scripts allow-forms`, no same-origin) with a scoped
  plugin token (`splg_…`) passed in the URL fragment (`gui/src/lib/pluginui.js`,
  `gui/src/pages/PluginFrame.svelte`).
- **Flow model (extended in M0).** `sdk/reasoning/flow.go` — `FlowNode`,
  `FlowEdge`, `FlowSpec`; compiled/run by `internal/reasoning/flow.go`. M0 added
  append-only typed ports (`FlowPort`, `FlowNode.Inputs/Outputs`,
  `FlowEdge.FromPort/ToPort`) and typed `FlowNode.Params`, all backward compatible.
- **Capability sources.** Existing endpoints expose what Studio needs to read:
  `GET /api/v1/agents`, `/tool-catalog`, `/providers`, `/skills`, `/mcp`. Registry
  discovery via `internal/pkgregistry` (skills.sh + git).

### Constraint: plugins cannot register `/api` routes

All gateway routes are wired at startup (`internal/gateway/server.go`); plugin
UIs use the existing API surface only. So Studio's M1 needs (intent-compile,
workflow save, flow test/run) will require **new core endpoints**, planned as core
work — not plugin-local code.

### Open decision: plugin access to the capability catalog

The M0 UI cannot yet read `/agents`, `/tool-catalog`, `/providers` with its plugin
token: the cap registry (`internal/caps/caps.go`) defines only `vector.search`,
`channel.send`, `events.subscribe` (each tied to a *scope kind* of
agents/channels/types), and the plugin route gate
(`internal/gateway/plugins.go` `pluginRoutePolicy`) admits plugin principals only
to `/health` and `/knowledge/*/search`. A global "read the catalog" grant does not
map onto the existing scope-kind model, so this is a deliberate design choice to
make with care. Options for M1:

1. **Scoped read caps** — register e.g. `agents.read` / `tools.read` /
   `providers.read`, add `pluginRoutePolicy` entries, grant them in the manifest.
   Simple, but extends the plugin security surface and stretches the scope model.
2. **Host-provided context (preferred to evaluate first)** — the parent
   `PluginFrame` (which already holds the user session) fetches the catalog and
   hands it to the iframe via `postMessage`. No new plugin permissions; a generic
   "host context" channel reusable by other plugins.

Until decided, the M0 UI degrades gracefully (handles the 403 with per-group
error rows + a status banner).

## Milestone / story map

- **M0 — Foundations.** Plugin scaffold + UI shell (S0.1); typed flow model
  (S0.3). *Status: built, see below.* (S0.2 capability catalog blocked on the
  decision above.)
- **M1 ★ thin slice.** Intent compiler v1 (S1.1), canvas render + inspector
  (S1.2), clarify round-trip (S1.3), test harness v1 (S1.4), save & deploy as a
  disabled workflow (S1.5). Requires the new core endpoints noted above.
- **M2 — Triggers & channels.** Trigger node types; compiler infers trigger from
  intent; capability-tier consent on privileged bindings.
- **M3 — Multi-agent & branching.** Typed multi-handle ports, conditional edges,
  `kind: router` integration, multi-step compilation.
- **M4 — Capability discovery & auto-provision.** Suggest + one-click install
  missing tools/skills/MCP via the registries.
- **M5 — Test depth.** Live vs dry-run, assertions, editable mocks, run history.
- **M6 — Templates, library, polish.** Start-from-template (absorbs the empty
  agent-list templates-picker item), save/clone/version, re-edit a node by
  re-describing it, export/share.

## M0 implementation status

- **Typed flow model (S0.3):** `sdk/reasoning/flow.go` + `internal/reasoning/flow.go`,
  append-only, backward compatible. New tests green: `TestFlowPorts_JSONRoundTrip`,
  `TestFlowSpec_BackwardCompatZeroValues`, `TestCompileFlow_PortValidation`,
  `TestCompileFlow_ParamsPreserved`, `TestRunFlow_PortsAndParamsRegression`,
  `TestFlowPorts_YAMLRoundTrip`.
- **Plugin scaffold + UI shell (S0.1):** `examples/plugins/studio/` — v2 manifest,
  token-aware sandboxed-iframe UI (vanilla JS, no build) that renders a palette /
  canvas placeholder / intent input and fetches the catalog (currently 403,
  handled gracefully).

## M1 Wave 1 status (in progress)

- **Host-RPC bridge + live palette (S1.0/S0.2):** `gui/src/pages/PluginFrame.svelte`
  now fetches the catalog with the user session and relays it to the sandboxed
  Studio iframe via a whitelisted `postMessage` bridge (`catalog.request` →
  `catalog.response`, id-correlated, source-checked). `examples/plugins/studio/ui/app.js`
  renders the real palette from it (graceful direct-fetch fallback). No plugin-token
  permission change. gui vite build green.
- **Intent compiler + compile endpoint (S1.1):** new `internal/studio` package
  (BuildPrompt / ParseDraft / Compile behind a narrow `LLM` interface) and
  `POST /api/v1/studio/compile` (`internal/gateway/studio.go`, registered in
  `server.go`, reuses `rbac.ResourceAgents`+`ActionWrite`, adapts `llmRouter`).
  Hybrid: returns a draft workflow AND clarifying questions. 10 unit tests green
  (pinned to the canonical HN-digest example), gofmt/vet clean.
- **Verification gate:** `internal/studio` is fully verified in-sandbox; the gateway
  package can only be compiled where CGO + sqlite headers exist — confirm with
  `make all` on the Mac before pushing.

## M1 Wave 2 status

The full describe → graph → test → save loop is implemented.

- **Visual builder (S1.2/S1.3):** the plugin UI is now a Svelte + Vite +
  `@xyflow/svelte` app. Source in `examples/plugins/studio/ui-src/`
  (`base: './'`, builds to a gitignored `ui/`; built by `make all` via the
  `plugin-ui` target, or `make plugin-ui` alone). Renders the compiled draft on a
  canvas (auto-layout when nodes lack x/y), a node inspector, a clarify-questions
  panel that re-compiles with answers, a transparency strip for compiler notes,
  and Test / Save actions.
- **Bridge ops:** `PluginFrame.svelte` now relays four whitelisted ops
  (`catalog`, `compile`, `test`, `save`) to the core endpoints with the user
  session; `gui/src/lib/api.js` gained `api.studio.{compile,test,save}`.
- **Endpoints (S1.4/S1.5):** `POST /studio/compile` gained optional `answers`
  (clarify round-trip); `POST /studio/test` compiles + runs the draft through a
  mock node runner returning a per-node trace + result (no real tools/LLM);
  `POST /studio/save` converts the draft to a disabled `agent.Definition` and
  persists it via the same `loader.Upsert` path as agent creation. New
  `internal/studio` tests green (testrun end-to-end, draft→definition, answers).
- **Verification gate (unchanged):** `internal/studio` + both vite builds are
  green in-sandbox; the gateway's final compile is the `make all` check on the Mac.

## M2 status — triggers/channels + capability-tier consent

- **Editable triggers + channels (S2.1):** the catalog bridge now also relays
  the channel list; the palette shows a Channels group; the inspector lets you
  edit the trigger (type + cron, with hints) and the output channel(s), writing
  back into the draft so Test/Save use the edits.
- **Trigger inference (S2.2):** deterministic post-parse normalization fills a
  sane trigger from common phrasings ("every weekday at 8am" → schedule + cron;
  "when someone messages / on telegram" → channel; "webhook" → webhook) without
  overriding a cron the model set.
- **Capability-tier consent (S2.3):** `POST /studio/plan` classifies the draft's
  resulting agent via `internal/tier` (the flow's tool/agent nodes are projected
  onto the Definition so the classifier sees real capabilities) and returns
  `{tier, reasons, requiresConsent, consentItems}`. `requiresConsent` = Privileged
  **and** channel-bound. `POST /studio/save` refuses to persist such a draft
  unless `acceptPrivilegedExposure` is set; the UI shows a consent dialog first.

### Consent is a user acknowledgment, not an operator grant (by design)

`internal/app/channels.go` is explicit that the authoritative
`accept_privileged_exposure` flag **must** live on the operator's config.yaml
channel binding — "the operator deploying an agent to a public channel is the one
accepting the risk, not the agent author." So Studio does **not** write that flag
or let the author pre-grant exposure. Studio's consent gate is a save-time
acknowledgment that blocks creating a Privileged channel-bound workflow without an
explicit click; it records the acknowledgment under a deliberately distinct label
(`studio.privilege_acknowledged`, **not** the binding key) and saves the agent
**disabled**. An operator still grants channel exposure at deploy time. If you'd
prefer Studio consent to actually flow through to the binding, that's a one-line,
security-relevant change in `channels.go` to review separately — intentionally
left out here.

## M3–M6 status (complete)

- **M3 — multi-agent, branching, typed handles:** the compiler emits multi-node
  graphs with peer-agent nodes, branch nodes + conditional `edge.if`, and typed
  ports; `POST /studio/validate` returns structured errors + soft warnings. The
  canvas renders per-kind nodes, typed multi-handles, labeled branch/`else` edges,
  inline `if`-condition editing, and debounced validate-on-edit highlighting.
- **M4 — capability discovery:** `compile` returns `suggestions[]` for referenced
  tools/agents missing from the catalog (empty-catalog guard = no false positives).
  The UI shows a "Needs setup" panel; Find runs a `discover` op over the existing
  `/registries/search`; Install relays to the existing `POST /plugins/install`
  **stage** flow — surfaced honestly as multi-step (operator approves in the
  Plugins page), never auto-activated.
- **M5 — test depth:** `POST /studio/test` gains per-node mocks and assertions
  (`contains`/`equals`/`exists` over a node or the final `result`), evaluated by a
  pure function; `live` mode is guarded (never runs real tools/LLM from an unsaved
  draft — directs you to save+enable and exercise via the channel). The UI adds a
  mock editor, assertion editor with pass/fail, and a session-only run history with
  view + replay (sandbox has no localStorage).
- **M6 — templates, library, export, re-describe:** `GET /studio/templates`
  (3 built-in starter drafts, all CompileFlow-valid); a file-backed draft library
  under `<workspace>/studio/drafts` (`POST/GET/GET:id/DELETE` with a path-traversal
  guard); client-side export/import of draft JSON; and `POST /studio/refine` to
  re-describe a single node in plain language (LLM edit, re-validated). The UI adds
  an empty-state template picker, Save/Open library modals, Export/Import, and a
  per-node Refine input.

All `internal/studio` logic is unit-tested and green in-sandbox; the gateway's
final compile remains the `make all` check on the Mac.

## Shipping — a normal plugin, installed by default

Studio is just a plain plugin (manifest + built `ui/`) living in the workspace
plugins dir — **not** embedded in the binary. This keeps it simple and
hot-reloadable:

- **Installed by `curl | bash`.** `install.sh`, after `make all` builds the UI,
  copies `examples/plugins/studio` (only `plugin.yaml` + `ui/`) into
  `<workspace>/plugins/studio` (`~/.soulacy/soulspace/plugins`, or
  `$SOULACY_WORKSPACE`). Re-running the installer refreshes it.
- **Loaded by the framework.** The default `plugin_dirs` is `<workspace>/plugins`,
  which the loader scans at startup. No `.soulacy-install.json` is written, so it
  loads **unmanaged → no approval step**, and appears in the portal's Plugins nav.
- **Hot reload.** The gateway serves the plugin's UI from disk on every request,
  so UI edits show on a browser refresh with no rebuild. Adding/updating a plugin
  or changing its manifest is picked up on gateway restart (the loader re-scans).
  (A live manifest file-watch is a possible future add — not needed for the flow.)
- **Dev loop.** A repo checkout running `config.dev.yaml` loads Studio directly via
  `plugin_dirs: [examples/plugins]`; `make run-dev` builds everything and serves
  with that config. The plugin UI is built by `make all` (target `plugin-ui`) into
  a gitignored `ui/`; nothing about Studio is compiled into the binary.

## Vasu's open questions

Plugin name (keep "Studio"?); intent compiler as agent vs skill; how "live" M1
deploy is (register disabled until flipped on — current assumption); companion's
eventual fate; a concrete first-run example workflow to shape the M1 slice around.
