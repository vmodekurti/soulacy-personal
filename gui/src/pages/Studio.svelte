<script>
  import TourButton from '../lib/TourButton.svelte'
  import { confirmDestructive, confirmLocal } from '../lib/destructive.js'
  import { onMount, onDestroy } from 'svelte'
  import {
    SvelteFlow, Background, Controls, MiniMap, Position,
  } from '@xyflow/svelte'
  import '@xyflow/svelte/dist/style.css'
  import { writable, get } from 'svelte/store'

  // ARCH-6: Studio is now a first-class page of the core dashboard, not a
  // sandboxed iframe plugin. The old postMessage RPC bridge is replaced by a
  // thin client that calls the gateway directly through the GUI's
  // authenticated `api` session (studioApi.js keeps the same `bridge` shape).
  // `api` was USED in this file (three call sites) and never imported, so every
  // one of them threw ReferenceError. All three sit inside try/catch, so the
  // failure surfaced as "could not preview the change" — a message about the
  // change, not about a missing import — and the repair-preview feature was
  // quietly dead. Svelte compiles an undeclared identifier as a global, so
  // nothing failed at build time either.
  import { api } from '../lib/api.js'
  import { bridge } from '../lib/studio/studioApi.js'
  import { editAgent, studioDebugRun, studioSession } from '../lib/stores.js'
  import { toFlow, kindMeta } from '../lib/studio/graph.js'
  import { validateConnection } from '../lib/studio/portcompat.js'
  import { computeRunState } from '../lib/studio/runstate.js'
  import Palette from '../lib/studio/Palette.svelte'
  import Inspector from '../lib/studio/Inspector.svelte'
  import YamlView from '../lib/studio/YamlView.svelte'
  import { normalizeYamlEditorText } from '../lib/studio/yamltext.js'
  import BuildInspector from '../lib/studio/BuildInspector.svelte'
  import Collapsible from '../lib/studio/Collapsible.svelte'
  import StudioNode from '../lib/studio/nodes/StudioNode.svelte'
  import TriggerNode from '../lib/studio/nodes/TriggerNode.svelte'
  import OutputNode from '../lib/studio/nodes/OutputNode.svelte'
  import LiveEdge from '../lib/studio/LiveEdge.svelte'
  import { pythonCodeFor, pythonLabelFor } from '../lib/studio/pythonTemplates.js'
  import { autoConnectEdge, wouldCreateCycle } from '../lib/studio/autoconnect.js'
  import { explainPythonError } from '../lib/studio/pyerror.js'
  import { stepResultsByNode } from '../lib/studio/testresults.js'
  import { repairVerdict, repairProofLabel } from '../lib/studio/repairverdict.js'
  import { fixturesFromWorkflow, outcomeWithFixtures } from '../lib/studio/benchfixtures.js'
  import { specWithTrigger, unresolvedBlockers } from '../lib/studio/buildspecview.js'
  import {
    snapshotSession, sessionAfterDelete, promptsForDraft,
    providerSetupTarget, studioResumeRequested,
  } from '../lib/studio/session.js'
  import {
    partitionLibrary, filterLibrary, libraryFacets, hasActiveFilters, emptyQuery,
  } from '../lib/studio/libraryfilter.js'
  import { migrateEndpoints } from '../lib/studio/planlanes.js'
  import PlanView from '../lib/studio/PlanView.svelte'
  import StrategyPanel from '../lib/studio/StrategyPanel.svelte'
  import TriggerSettings from '../lib/studio/TriggerSettings.svelte'
  import {
    applyGenerationTrigger, deliveryModeOf, intentWithGenerationTrigger, toggleChannelPatch,
  } from '../lib/studio/triggersettings.js'
  import BuildSpecPanel from '../lib/studio/BuildSpecPanel.svelte'
  import ReadinessPanel from '../lib/studio/ReadinessPanel.svelte'
  import { NAVIGATE_TARGETS, actionKind, applyDraftFix } from '../lib/studio/fixactions.js'
  import WizardRail from '../lib/studio/WizardRail.svelte'
  import TestLabInputs from '../lib/studio/TestLabInputs.svelte'
  import FailedRunsPanel from '../lib/studio/FailedRunsPanel.svelte'
  import {
    STEP_DESCRIBE, STEP_BUILD, STEP_TEST, STEP_SAVE,
    // autoStep is deliberately no longer imported: it computed a "resume where
    // you left off" landing step, which is what sent the user back into the
    // middle of an agent instead of the Studio home. It remains exported and
    // tested for any caller that genuinely wants that behaviour.
    stepStates, canEnter, saveBlockedReason, intentOf, generatedMode,
  } from '../lib/studio/wizard.js'
  import '../lib/studio/studio.css'
  import './Studio.css'

  // (Removed) The old iframe build scrubbed the plugin token from the URL
  // fragment on mount. Embedded in the SPA we hold no token and must NOT touch
  // the hash — the core dashboard uses it for routing (#studio).

  const nodeTypes = {
    studio: StudioNode,
    studioTrigger: TriggerNode,
    studioOutput: OutputNode,
  }
  // Custom edge with weight + a heartbeat: glowing particles flow along it while
  // a build/run is in flight (see LiveEdge + the `building`→edge.active effect).
  const edgeTypes = { live: LiveEdge }

  // ── Palette (Wave 1) ──────────────────────────────────────────────────────
  let catalog = null
  let paletteStatus = 'Loading capabilities…'
  let paletteStatusKind = ''
  let paletteError = ''
  let lastGoodCatalog = null

  // Map each connected MCP tool's full name → its published param hint
  // ("title*:string, …"), so the Inspector can show a tool node's allowed
  // parameters and let you add one with a click.
  $: toolParams = (() => {
    const m = {}
    for (const t of (catalog && catalog.tools && catalog.tools.mcp_tools) || []) {
      if (t && t.full_name) m[t.full_name] = t.params || ''
    }
    return m
  })()

  async function loadCatalog() {
    paletteStatus = 'Loading capabilities…'
    paletteStatusKind = ''
    try {
      const next = await bridge.catalog()
      const failedParts = Object.entries(next || {})
        .filter(([, v]) => v && v.error)
        .map(([k]) => k)

      const hasAnyCatalogData =
        (next?.agents?.agents || []).length > 0 ||
        (next?.tools?.builtins || []).length > 0 ||
        (next?.tools?.python_tools || []).length > 0 ||
        (next?.tools?.mcp_tools || []).length > 0 ||
        Object.keys(next?.providers?.providers || {}).length > 0 ||
        (next?.channels?.channels || []).length > 0 ||
        (next?.skills?.skills || []).length > 0 ||
        ((next?.mcp?.servers || next?.mcp || [])).length > 0

      if (!hasAnyCatalogData && lastGoodCatalog) {
        catalog = lastGoodCatalog
        paletteStatus = 'Using cached capabilities while Studio refreshes.'
        paletteStatusKind = 'warn'
        paletteError = ''
        return
      }

      catalog = next
      if (hasAnyCatalogData) lastGoodCatalog = next
      paletteError = ''
      if (failedParts.length > 0) {
        paletteStatus = 'Capabilities loaded with limited data: ' + failedParts.join(', ')
        paletteStatusKind = 'warn'
      } else {
        paletteStatus = 'Capabilities loaded.'
        paletteStatusKind = 'ok'
      }
    } catch (e) {
      if (lastGoodCatalog) {
        catalog = lastGoodCatalog
        paletteError = ''
        paletteStatus = 'Using cached capabilities while Studio reconnects.'
        paletteStatusKind = 'warn'
      } else {
        paletteError = 'Unavailable'
        paletteStatus = 'Could not load capabilities: ' + (e.message || 'error')
        paletteStatusKind = 'error'
      }
    }
  }

  // ── Credentials (first-class): set the API keys an agent's tools/MCP need,
  // without leaving Studio. Loaded once; `set` marks which are already provided.
  let secrets = []
  let secretVals = {}     // { [name]: typed value }
  let secretBusy = ''     // name currently being saved
  let secretMsg = ''
  $: unsetSecrets = (secrets || []).filter((s) => s && s.set === false)
  async function loadSecrets() {
    try {
      const r = await bridge.listSecrets()
      secrets = (r && Array.isArray(r.secrets)) ? r.secrets : []
    } catch (_) { secrets = [] }
  }
  async function setSecretVal(name) {
    const v = (secretVals[name] || '').trim()
    if (!v || secretBusy) return
    secretBusy = name
    secretMsg = ''
    try {
      await bridge.setSecret(name, v)
      secretVals = { ...secretVals, [name]: '' }
      secretMsg = `Saved ${name}.`
      await loadSecrets()
    } catch (e) {
      secretMsg = (e && e.message) ? `Could not save ${name}: ${e.message}` : `Could not save ${name}`
    } finally {
      secretBusy = ''
    }
  }

  // Derive a COMPACT catalog of installed capability NAMES from the raw catalog
  // payload and thread it into compile, so the backend can flag missing
  // capabilities (M4). Without it the backend's empty-catalog guard returns no
  // suggestions. Keys MUST match the studio Request.Catalog JSON tags:
  //   { tools:[], agents:[], providers:[] }
  //
  // Raw shapes (see PluginFrame.svelte handleCatalogRequest):
  //   agents:    { agents:[{id,name,...}], count }            (GET /agents)
  //   tools:     { python_tools:[{name}], mcp_tools:[{name,server}],
  //                builtins:[{name}] }                        (GET /tool-catalog)
  //   providers: { providers:{<id>:{...}}, default_provider } (GET /providers)
  function compactCatalog(cat) {
    if (!cat) return undefined

    // Tools: unique names across python_tools + mcp_tools + builtins.
    const t = cat.tools || {}
    const toolNames = []
    const seenTool = new Set()
    const pushTool = (name) => {
      const n = (name == null ? '' : String(name)).trim()
      if (!n || seenTool.has(n)) return
      seenTool.add(n)
      toolNames.push(n)
    }
    for (const arr of [t.python_tools, t.mcp_tools, t.builtins]) {
      if (Array.isArray(arr)) for (const x of arr) pushTool(x && x.name)
    }

    // Agents: both ids AND names (a draft may reference either).
    const agentList = (cat.agents && Array.isArray(cat.agents.agents)) ? cat.agents.agents : []
    const agentNames = []
    const seenAgent = new Set()
    const pushAgent = (name) => {
      const n = (name == null ? '' : String(name)).trim()
      if (!n || seenAgent.has(n)) return
      seenAgent.add(n)
      agentNames.push(n)
    }
    for (const a of agentList) {
      pushAgent(a && a.id)
      pushAgent(a && a.name)
    }

    // Providers: the provider ids (keys of the providers map).
    const provMap = (cat.providers && cat.providers.providers) || {}
    const providerNames = (provMap && typeof provMap === 'object') ? Object.keys(provMap) : []

    return { tools: toolNames, agents: agentNames, providers: providerNames }
  }

  // ── Compile loop ──────────────────────────────────────────────────────────
  let intent = ''
  let compiling = false
  let compileError = ''
  let workflow = null
  let initialGeneratedWorkflow = null // immutable baseline for preference diff mining
  let questions = []
  let notes = []
  let notesExpanded = false  // collapse the compiler-notes pills by default
  let answers = {}            // { [questionId]: value }
  let explanation = null      // plain-language "what Studio built" (Story #10)
  let generationProfile = null // local/cloud builder guardrails used for compile
  let modelAdvice = null      // builder-model advice (local-first)
  let cloudAck = false        // user acknowledged cloud builder this session
  let cloudGate = null        // { provider, model } when the escalation dialog is open
  let workflowExperiment = null // explicit opt-in dialog for generated fixed graphs

  // ── Pre-generation prompt refinement ──────────────────────────────────────
  // Pressing Generate first runs a refine pass: the framework LLM rewrites the
  // rough intent into a clear spec, lists the assumptions it made, and asks any
  // clarifying questions. The dialog below shows all of it for confirmation;
  // the workflow is only compiled after the user confirms (and may edit the
  // refined intent / answer questions first). `refinement` is the open dialog
  // state; null when closed.
  let promptViewer = false     // full-prompt read/edit modal open
  let refinement = null        // { original, refined_intent, summary, assumptions[], questions[] }
  let agentRoute = null        // { mode, reason } when Studio built an agent instead of a workflow
  let refining = false         // refine request in flight
  let refineAnswers = {}       // { [questionId]: value } for refinement questions
  let rawPrompt = ''           // the user's ORIGINAL prompt (intent holds the refined one)
  let modalRefining = false    // inline refine (from the prompt editor) in flight
  let promptError = ''         // error shown inside the prompt editor
  // Optional creation-time override. "auto" keeps prompt inference; every
  // other value is an explicit user decision applied after model generation.
  let generationTrigger = { type: 'auto', cron: '', channel: '', delivery: 'auto', destination: '' }
  $: generationTriggerError = generationTrigger.type === 'schedule' && !generationTrigger.cron.trim()
    ? 'Enter a cron expression before generating.'
    : generationTrigger.type === 'channel' && !generationTrigger.channel
      ? 'Choose an inbound channel before generating.'
      : !['auto', 'reply', 'none'].includes(generationTrigger.delivery || 'auto') && !String(generationTrigger.destination || '').trim()
        ? 'Enter the delivery destination before generating.'
        : ''

  // ── "Studio understood" — the structured spec behind the Describe step ────
  // Derived from the prompt BEFORE generation so the user verifies Studio's
  // reading rather than discovering the misunderstanding on the canvas.
  let buildSpec = null         // /studio/build-spec payload
  let buildSpecLoading = false
  let buildSpecError = ''
  let buildSpecDebounce = null
  let buildSpecToken = 0       // guards against a slow response overwriting a newer one
  let buildSpecPrevIntent = '' // what the last spec was compared AGAINST

  // The generated workflow's own recommendation wins once it exists — that one
  // reflects what was actually built. Before Generate there is no workflow, so
  // the Strategy row used to read "not specified" for every prompt ever typed;
  // the build-spec endpoint now returns the same deterministic advice up front.
  $: specRecommendation =
    (workflow && workflow.recommendation) || (buildSpec && buildSpec.recommendation) || null

  // Required spec details the user has not answered yet. Shared with
  // BuildSpecPanel so both Generate buttons enforce the same gate.
  $: effectiveBuildSpec = specWithTrigger(buildSpec, generationTrigger)
  $: specUnresolved = unresolvedBlockers(effectiveBuildSpec, refineAnswers)

  // The prompt Studio should actually build from.
  //
  // There are two prompt boxes: "Your prompt" (rawPrompt) and the optional
  // "Refined prompt" (intent). Refining is a convenience, NOT a prerequisite —
  // but generate() read `intent` alone and returned silently when it was empty,
  // so typing in the main box and pressing Generate did nothing at all: no
  // request, no error, no explanation. The spec panel meanwhile fell back to
  // rawPrompt, so it filled in and enabled its Generate button, which is what
  // made the dead button look like a working one.
  //
  // Same fallback order as loadBuildSpec, so what the panel describes and what
  // generation builds are always the same text.
  //
  // Defined as a function with a reactive view over it, so handlers that run
  // right after one of the boxes changes read the current text rather than the
  // previous tick's — the staleness trap that nowCtx() and modeOf() exist for.
  $: effectiveIntent = intentOf(intent, rawPrompt)

  function scheduleBuildSpec() {
    if (buildSpecDebounce) clearTimeout(buildSpecDebounce)
    buildSpecDebounce = setTimeout(loadBuildSpec, 400)
  }

  async function loadBuildSpec() {
    // intentOf, not `intent || rawPrompt`: the latter tests raw truthiness, so a
    // refined box holding only whitespace shadowed the real prompt and the panel
    // showed nothing while generation built from the raw text. Same helper as
    // the Generate buttons, so panel and action cannot disagree about whether
    // there is anything to build.
    const text = intentOf(intent, rawPrompt)
    if (!text) { buildSpec = null; buildSpecError = ''; return }
    const token = ++buildSpecToken
    buildSpecLoading = true
    buildSpecError = ''
    try {
      // previous_intent is what turns this into a CHANGE summary rather than
      // just a re-read: without it a refine reports a new spec with no way to
      // see what actually moved.
      const res = await bridge.buildSpec(text, buildSpecPrevIntent || undefined)
      if (token !== buildSpecToken) return   // a newer request already answered
      buildSpec = res || null
    } catch (e) {
      if (token !== buildSpecToken) return
      // A failed spec read must not block generating — it is a review aid, not
      // a gate. Say so rather than leaving an empty panel that reads as "Studio
      // understood nothing".
      buildSpecError = (e && e.message) || 'Could not read the prompt.'
    } finally {
      if (token === buildSpecToken) buildSpecLoading = false
    }
  }

  // Re-read whenever the Describe surface is visible and the text settles.
  // Both surfaces count: the inline Describe step AND the legacy prompt modal.
  // Gating this on `promptViewer` alone left the inline step's "Studio
  // understood" pane permanently empty, because that modal is never open there.
  $: if (promptViewer || step === STEP_DESCRIBE) { intent; rawPrompt; scheduleBuildSpec() }

  // ── SOUL.yaml authoring rulebook (auto-injected into generate + fix) ───────
  let rulesOpen = false
  let rulesText = ''
  let rulesDefault = ''
  let rulesIsDefault = true
  let rulesLoading = false
  let rulesSaving = false
  let rulesMsg = ''

  async function openRules() {
    rulesOpen = true
    rulesLoading = true
    rulesMsg = ''
    try {
      const r = await bridge.getRules()
      rulesText = (r && r.rules) || ''
      rulesDefault = (r && r.default) || ''
      rulesIsDefault = !!(r && r.isDefault)
    } catch (e) {
      rulesMsg = '✗ ' + ((e && e.message) || 'Could not load rules')
    }
    rulesLoading = false
  }
  async function saveRules() {
    if (rulesSaving) return
    rulesSaving = true
    rulesMsg = ''
    try {
      const r = await bridge.saveRules(rulesText)
      rulesIsDefault = !!(r && r.isDefault)
      rulesMsg = '✓ Saved'
    } catch (e) {
      rulesMsg = '✗ ' + ((e && e.message) || 'Save failed')
    }
    rulesSaving = false
  }
  function resetRulesToDefault() {
    if (rulesDefault) rulesText = rulesDefault
  }

  // ── Missing-capability suggestions (M4) ───────────────────────────────────
  // The compile response may carry `suggestions:[{kind,name,reason,installed}]`
  // — capabilities the draft references that are NOT installed. They surface in
  // a non-blocking "Needs setup" strip; each can be discovered + staged for
  // install through the EXISTING registry-search / plugin-install endpoints.
  let suggestions = []
  // Per-suggestion UI state keyed by name:
  //   { loading, error, results:[pkg], message, staged }
  let discoverState = {}

  function suggestionKey(s) {
    return (s && s.name) || ''
  }

  // Find installable packages for one missing capability via the discover
  // bridge op (relays GET /registries/search). Degrades gracefully on error.
  async function findCapability(s) {
    const key = suggestionKey(s)
    if (!key) return
    discoverState = { ...discoverState, [key]: { loading: true, error: '', results: [], message: '', staged: '' } }
    try {
      const data = await bridge.discover(key, s.kind)
      const results = (data && Array.isArray(data.results)) ? data.results : []
      discoverState = { ...discoverState, [key]: { loading: false, error: '', results, message: results.length ? '' : 'No matches found.', staged: '' } }
    } catch (e) {
      discoverState = { ...discoverState, [key]: { loading: false, error: e.message || 'discovery failed', results: [], message: '', staged: '' } }
    }
  }

  // Stage an install for a chosen registry result via the install bridge op
  // (relays POST /plugins/install). Staging is real + consent-bearing and does
  // NOT activate the package — the operator must Approve it in the Plugins
  // page. We surface that honestly, then re-fetch the catalog so the palette
  // (and any now-installed suggestions) refresh.
  async function installResult(s, pkg) {
    const key = suggestionKey(s)
    if (!key || !pkg) return
    const source = pkg.source || pkg.slug || ''
    const prev = discoverState[key] || {}
    discoverState = { ...discoverState, [key]: { ...prev, loading: true, error: '', message: '' } }
    try {
      const data = await bridge.install({ source, checksum: pkg.checksum, name: pkg.slug || key })
      const note = (data && data.note) || ''
      const msg = data && data.multiStep
        ? (data.staged
            ? `Staged "${pkg.slug || key}" — approve it in the Plugins page to activate. ${note}`.trim()
            : `Staged for review. ${note}`.trim())
        : `Installed "${pkg.slug || key}".`
      discoverState = { ...discoverState, [key]: { ...discoverState[key], loading: false, message: msg, staged: (data && data.staged) || '' } }
      // Refresh the catalog so the palette + suggestions reflect the new state.
      await loadCatalog()
    } catch (e) {
      discoverState = { ...discoverState, [key]: { ...discoverState[key], loading: false, error: e.message || 'install failed' } }
    }
  }

  function pkgDesc(pkg) {
    return (pkg && (pkg.description || (pkg.provider ? '' : ''))) || ''
  }

  // ── Browse registry (S4.3, light) ─────────────────────────────────────────
  // A small Palette affordance that runs `discover` with the user's intent so
  // they can explore + stage installable packages without a suggestion. Reuses
  // the same install path (and catalog refresh) as the Needs-setup panel.
  let browse = { open: false, loading: false, error: '', results: [], message: '' }

  async function browseRegistry() {
    const q = (intent || '').trim()
    browse = { open: true, loading: true, error: '', results: [], message: '' }
    try {
      const data = await bridge.discover(q || 'skill')
      const results = (data && Array.isArray(data.results)) ? data.results : []
      browse = { open: true, loading: false, error: '', results, message: results.length ? '' : 'No matches found.' }
    } catch (e) {
      browse = { open: true, loading: false, error: e.message || 'discovery failed', results: [], message: '' }
    }
  }

  async function installBrowse(pkg) {
    if (!pkg) return
    const source = pkg.source || pkg.slug || ''
    browse = { ...browse, loading: true, error: '', message: '' }
    try {
      const data = await bridge.install({ source, checksum: pkg.checksum, name: pkg.slug })
      const note = (data && data.note) || ''
      browse = {
        ...browse,
        loading: false,
        message: data && data.multiStep
          ? `Staged "${pkg.slug}" — approve it in the Plugins page to activate. ${note}`.trim()
          : `Installed "${pkg.slug}".`,
      }
      await loadCatalog()
    } catch (e) {
      browse = { ...browse, loading: false, error: e.message || 'install failed' }
    }
  }

  // xyflow stores (the component expects writable stores for nodes/edges).
  const nodes = writable([])
  const edges = writable([])
  let selectedNode = null     // raw flow node for the inspector
  let selectedEdge = null     // { index, edge } for the inspector

  // ── Validation (M3) ───────────────────────────────────────────────────────
  // Non-blocking: a debounced /studio/validate after compile and after any
  // draft edit. Highlights offending nodes/edges and shows a status strip.
  let validation = null       // { ok, errors[], warnings[] } | null
  let validateTimer = null

  // Per-node execution status from the last build (id -> 'ok'|'repaired'|
  // 'problem'|'idle'), threaded into the graph so nodes carry a semantic accent.
  let nodeRunState = {}
  // When the Build Inspector's replay scrubber is active it sends a per-frame
  // status map that OVERRIDES the final-build status, so the canvas replays the
  // loop attempt-by-attempt. null = no replay (show the final build outcome).
  let replayOverride = null

  function rebuildGraph() {
    if (!workflow) {
      nodes.set([]); edges.set([]); return
    }
    const flow = toFlow(workflow, validation, nodeRunState)
    nodes.set(flow.nodes)
    edges.set(flow.edges)
  }

  // Recompute node run-state whenever a build finishes (or its preflight
  // updates), or follow the inspector's replay scrubber when active, and re-render
  // the graph so the canvas shows the success/repaired/problem accents.
  $: {
    nodeRunState = replayOverride || computeRunState({ report: buildReport, preflight })
    rebuildGraph()
  }

  // Heartbeat: while an autonomous build is in flight, light every edge so the
  // LiveEdge particles flow — the canvas visibly pulses with the run (principle:
  // "if a workflow is running, the canvas should gently pulse"). Reactive on
  // `building`; rebuildGraph resets edges to rest when the graph itself changes.
  function setEdgesActive(on) {
    edges.update((es) => es.map((e) => (e && e.data ? { ...e, data: { ...e.data, active: on } } : e)))
  }
  $: setEdgesActive(building)

  // ── Palette drag-and-drop (create nodes by dropping) ──────────────────────
  // Palette items carry an `application/studio-node` payload. Dropping one on
  // the canvas creates a real flow node (agent/tool) at the drop point, or
  // attaches a channel to the workflow output. New feature on top of ARCH-6.
  const DRAG_MIME = 'application/studio-node'

  // Starter script for a freshly-dropped Custom Python block. The runtime
  // contract (Phase 1): upstream node outputs arrive as `inputs` (a dict); the
  // value you return/print becomes this node's output.
  const PYTHON_STARTER = `# Custom Python step.
# Upstream node outputs arrive as 'inputs' (a dict).
# Return (or print) a value — it becomes this node's output.
def run(inputs):
    # TODO: your logic here
    return inputs
`
  const LLM_EXTRACT_SYSTEM = `Extract the user's intent into a compact JSON object.
Return only JSON. Do not include markdown.
Use null for fields that are not present.`

  function emptyWorkflow() {
    return {
      name: 'Untitled workflow',
      trigger: { type: 'manual' },
      channels: [],
      flow: { nodes: [], edges: [], entry: '' },
    }
  }

  function onCanvasDragOver(e) {
    if (!e.dataTransfer) return
    if (Array.from(e.dataTransfer.types || []).includes(DRAG_MIME)) {
      e.preventDefault()
      e.dataTransfer.dropEffect = 'copy'
    }
  }

  function onCanvasDrop(e) {
    if (!e.dataTransfer) return
    const raw = e.dataTransfer.getData(DRAG_MIME)
    if (!raw) return
    e.preventDefault()
    let drag
    try { drag = JSON.parse(raw) } catch (_) { return }
    addFromPalette(drag, dropFlowPosition(e))
  }

  // Invert the current viewport transform (read off the xyflow viewport element)
  // to turn the drop's screen coords into flow coords. Identity fallback when no
  // canvas is mounted yet (first drop onto the empty state).
  function dropFlowPosition(e) {
    const pane = document.querySelector('#studio-app .svelte-flow')
    if (!pane) return { x: 0, y: 0 }
    const rect = pane.getBoundingClientRect()
    let tx = 0, ty = 0, zoom = 1
    const vp = pane.querySelector('.svelte-flow__viewport')
    if (vp) {
      try {
        const m = new DOMMatrixReadOnly(getComputedStyle(vp).transform)
        tx = m.m41; ty = m.m42; zoom = m.a || 1
      } catch (_) { /* identity fallback */ }
    }
    return {
      x: (e.clientX - rect.left - tx) / zoom,
      y: (e.clientY - rect.top - ty) / zoom,
    }
  }

  function uniqueNodeId(prefix) {
    const used = new Set(((workflow && workflow.flow && workflow.flow.nodes) || []).map((n) => n.id))
    let i = 1, id
    do { id = prefix + '_' + i++ } while (used.has(id))
    return id
  }

  // Make a node the flow's entry/start node. The trigger always flows into the
  // entry node, so this is how the user "connects" the trigger to a given node.
  function setEntry(nodeId) {
    if (!nodeId || !workflow || !workflow.flow) return
    const flow = workflow.flow
    workflow = { ...workflow, flow: { ...flow, nodes: bakePositions(flow.nodes), entry: nodeId } }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  // Designate a node as the flow's output: its result becomes what's delivered
  // to the output channels (the runtime returns this node's result). Dragging a
  // node onto the output box, or the Inspector button, both call this.
  function setOutput(nodeId) {
    if (!nodeId || !workflow || !workflow.flow) return
    const flow = workflow.flow
    workflow = { ...workflow, flow: { ...flow, nodes: bakePositions(flow.nodes), output: nodeId } }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  // Snapshot current on-screen node positions into x/y on the raw nodes.
  // graph.js auto-lays-out only when EVERY node lacks x/y, so baking existing
  // positions before appending a positioned node keeps the compiled layout from
  // collapsing onto (0,0).
  function bakePositions(rawNodes) {
    const live = get(nodes)
    const pos = new Map(live.map((n) => [n.id, n.position]))
    return (rawNodes || []).map((n) => {
      const pt = pos.get(n.id)
      return pt ? { ...n, x: Math.round(pt.x), y: Math.round(pt.y) } : n
    })
  }

  function addFromPalette(drag, at) {
    if (!drag || !drag.kind) return

    // Channels attach to the workflow output rather than becoming a flow node.
    if (drag.kind === 'channel') {
      const base = workflow || emptyWorkflow()
      const chans = Array.isArray(base.channels) ? base.channels.slice() : []
      const id = drag.id || drag.name
      if (id && !chans.includes(id)) chans.push(id)
      workflow = { ...base, channels: chans }
      rebuildGraph()
      scheduleValidate()
      return
    }

    // Trigger / Exit -> structural endpoint blocks (Phase A). A trigger becomes
    // the flow entry; an exit is a terminal delivery block. Config lives in
    // node.params and is edited in the Inspector; on save the backend projects it
    // onto the workflow trigger/channels (DeriveEndpoints).
    if (drag.kind === 'trigger' || drag.kind === 'exit') {
      const eid = uniqueNodeId(drag.kind)
      const enode = {
        id: eid,
        kind: drag.kind,
        description: drag.kind === 'trigger' ? 'Trigger — starts the flow' : 'Exit — delivers the result',
        input: '', output: '', inputs: [], outputs: [],
        params: drag.kind === 'trigger'
          ? { kind: 'cron', config: { cron: '0 9 * * *' } }
          : { route: 'console', config: {} },
        x: Math.round(at.x),
        y: Math.round(at.y),
      }
      if (!workflow) {
        workflow = { ...emptyWorkflow(), flow: { nodes: [enode], edges: [], entry: drag.kind === 'trigger' ? eid : '' } }
      } else {
        const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
        const baked = bakePositions(flow.nodes)
        const entry = drag.kind === 'trigger' ? eid : (flow.entry || '')
        workflow = { ...workflow, flow: { ...flow, nodes: [...baked, enode], entry } }
      }
      selectedNode = enode
      selectedEdge = null
      rebuildGraph()
      scheduleValidate()
      return
    }

    // Agent / tool / python / llm / skill -> a new flow node at the drop point.
    // A skill becomes a `read_skill` tool node pre-pointed at the skill name
    // (read_skill takes a skill_name argument), so it's runnable immediately.
    const isSkill = drag.kind === 'skill'
    const id = uniqueNodeId(isSkill ? 'skill' : drag.kind)
    const node = {
      id,
      kind: isSkill ? 'tool' : drag.kind,
      ...(drag.kind === 'tool' ? { tool: drag.name } : {}),
      ...(isSkill ? { tool: 'read_skill', description: 'Read skill: ' + drag.name } : {}),
      ...(drag.kind === 'agent' ? { agent: drag.name } : {}),
      // Python: seed from a named template when the palette supplied one
      // (Guided Studio Builder), else the blank starter. The template's plain-
      // English label becomes the node description so it reads as domain work.
      ...(drag.kind === 'python'
        ? (drag.template
            ? { code: pythonCodeFor(drag.template), description: pythonLabelFor(drag.template) }
            : { code: PYTHON_STARTER })
        : {}),
      ...(drag.kind === 'llm'
        ? {
            description: 'Extract structured intent',
          }
        : {}),
      input: drag.kind === 'llm' ? '{{ .trigger.text }}' : (isSkill ? JSON.stringify({ skill_name: drag.name }) : ''),
      output: drag.kind === 'llm' ? 'extracted' : '',
      inputs: [],
      outputs: [],
      params: drag.kind === 'llm' ? { system: LLM_EXTRACT_SYSTEM, response_format: 'json' } : {},
      x: Math.round(at.x),
      y: Math.round(at.y),
    }

    if (!workflow) {
      workflow = { ...emptyWorkflow(), flow: { nodes: [node], edges: [], entry: id } }
    } else {
      const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
      const baked = bakePositions(flow.nodes)
      // Smart connection (Story 7): auto-wire the new step to the most recent
      // upstream step, unless that would create a cycle. Keeps a freshly-dropped
      // step from stranding under "Needs attention".
      const priorEdges = flow.edges || []
      const auto = autoConnectEdge(baked, node, priorEdges)
      const edges = (auto && !wouldCreateCycle(priorEdges, auto.from, auto.to))
        ? [...priorEdges, auto]
        : priorEdges
      workflow = { ...workflow, flow: { ...flow, nodes: [...baked, node], edges, entry: flow.entry || id } }
    }
    selectedNode = node     // select the new node for immediate editing
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()
  }

  // Phase B: compile a plain-language connector gate into a flow predicate.
  // Passed to the Inspector; returns the predicate string (or throws on error).
  async function compileGate(phrase, vars) {
    const res = await bridge.compileGate(phrase, vars)
    return (res && res.predicate) || ''
  }

  // Phase C: compile a node's plain-language intent into concrete config, then
  // merge the compiled tool/agent/python fields into the node on the canvas.
  async function compileNode(intent, node) {
    if (!node) return
    if (!ensureCloudOk(() => compileNode(intent, node))) return
    // Upstream outputs (var names) so the per-node compiler can wire by field.
    // When a recent dry-run captured real output shapes, attach them so the model
    // compiles against actual data instead of guessing the payload format.
    const shapeByName = {}
    for (const s of lastShapes) { if (s && s.name) shapeByName[s.name] = s.shape || '' }
    const upstream = ((workflow && workflow.flow && workflow.flow.nodes) || [])
      .filter((n) => n && n.id !== node.id && n.output)
      .map((n) => {
        const name = String(n.output).trim()
        return shapeByName[name] ? { name, shape: shapeByName[name] } : { name }
      })
    const res = await bridge.compileNode({ intent, nodeId: node.id, kind: node.kind || '', upstream })
    const compiled = res && res.node
    if (!compiled) return
    // Keep id/position; take the compiled kind/config/intent.
    applyNodePatch(node.id, {
      kind: compiled.kind,
      tool: compiled.tool || '',
      agent: compiled.agent || '',
      code: compiled.code || '',
      input: compiled.input || '',
      output: compiled.output || node.output || '',
      requires: compiled.requires || [],
      intent,
    })
  }

  // ── Delete a node / edge from the canvas ──────────────────────────────────
  function deleteNode(nodeId) {
    if (!nodeId || !workflow || !workflow.flow) return
    const flow = workflow.flow
    // Bake positions first so the survivors keep their places, then drop the
    // node and every edge that touched it.
    const remaining = bakePositions(flow.nodes).filter((n) => n.id !== nodeId)
    const remEdges = (flow.edges || []).filter((e) => e.from !== nodeId && e.to !== nodeId)
    let entry = flow.entry
    if (entry === nodeId) entry = remaining.length ? remaining[0].id : ''
    workflow = { ...workflow, flow: { ...flow, nodes: remaining, edges: remEdges, entry } }
    if (selectedNode && selectedNode.id === nodeId) selectedNode = null
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()
  }

  function deleteEdge(index) {
    if (index == null || !workflow || !workflow.flow || !Array.isArray(workflow.flow.edges)) return
    const edges = workflow.flow.edges.filter((_, i) => i !== index)
    workflow = { ...workflow, flow: { ...workflow.flow, edges } }
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()
  }

  // Merge a patch into one node of the draft (e.g. the Custom Python editor
  // writing inline code back). Bakes positions so the graph doesn't reflow, and
  // refreshes the selection reference so the Inspector keeps editing the live
  // node after rebuildGraph.
  function applyNodePatch(nodeId, patch) {
    if (!nodeId || !patch || !workflow || !workflow.flow) return
    const flow = workflow.flow
    let updated = null
    const nodes = bakePositions(flow.nodes).map((n) => {
      if (n.id !== nodeId) return n
      updated = { ...n, ...patch }
      return updated
    })
    if (!updated) return
    workflow = { ...workflow, flow: { ...flow, nodes } }
    if (selectedNode && selectedNode.id === nodeId) selectedNode = updated
    rebuildGraph()
    scheduleValidate()
  }

  // Delete/Backspace removes the selected node (or edge) — but never while the
  // user is typing in a field (intent box, inspector inputs, refine textarea).
  function onCanvasKeydown(e) {
    // Esc restores the split layout from any maximized frame.
    if (e.key === 'Escape' && maximizedFrame) {
      e.preventDefault()
      maximizedFrame = ''
      return
    }
    if (e.key !== 'Delete' && e.key !== 'Backspace') return
    const el = document.activeElement
    if (el && (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.isContentEditable)) return
    if (selectedNode) { e.preventDefault(); deleteNode(selectedNode.id) }
    else if (selectedEdge) { e.preventDefault(); deleteEdge(selectedEdge.index) }
  }

  onMount(() => {
    window.addEventListener('keydown', onCanvasKeydown)
    return () => window.removeEventListener('keydown', onCanvasKeydown)
  })

  // Debounced, best-effort validation. On any bridge error we degrade
  // gracefully: clear the strip rather than block editing.
  function scheduleValidate() {
    if (validateTimer) clearTimeout(validateTimer)
    // Skip flow validation for reasoning agents — they have no graph, so the
    // workflow validator only produces node/edge errors that don't apply and
    // confuse the user. The agent's own validity is checked on save / in YAML.
    if (!workflow || workflow.strategy) { validation = null; return }
    validateTimer = setTimeout(runValidate, 350)
  }

  async function runValidate() {
    if (!workflow) { validation = null; return }
    const snapshot = workflow
    try {
      const res = await bridge.validate(snapshot)
      // Ignore a stale response if the draft moved on under us.
      if (snapshot !== workflow) return
      validation = {
        ok: res && res.ok !== false && !(res && res.errors && res.errors.length),
        errors: (res && Array.isArray(res.errors)) ? res.errors : [],
        warnings: (res && Array.isArray(res.warnings)) ? res.warnings : [],
      }
    } catch (_) {
      // Bridge/host unavailable — stay silent and non-blocking.
      validation = null
    }
    rebuildGraph()   // re-render with (or without) highlights
  }

  // Generate button entry point. Mandatory pre-generation step: instead of
  // compiling the raw intent straight away, first run a refine pass and open
  // the confirmation dialog. The actual compile happens in runCompile, called
  // once the user confirms the refined spec.
  async function generate() {
    // intentOf, not intent alone: an un-refined prompt is still a prompt.
    const text = intentOf(intent, rawPrompt)
    if (!text || compiling || refining || cloudGate) return
    // Local-first cloud-escalation gate: if the builder is a cloud model and the
    // user hasn't acknowledged it this session, ask before sending the prompt
    // off-box. They can continue with cloud or switch to a local model.
    if (!ensureCloudOk(generate)) return
    // Regenerating from an edited prompt replaces the whole canvas (including any
    // manual rewiring). Confirm when there's existing work to lose — do this up
    // front, before spending a refine round-trip.
    if (workflow && workflow.flow && (workflow.flow.nodes || []).length) {
      let ok = true
      try { ok = confirmLocal('Regenerate from this prompt? It replaces the current workflow on the canvas.') } catch (_) { ok = true }
      if (!ok) return
    }
    pipelineModalHidden = false
    refining = true
    compileError = ''
    try {
      // If this prompt has already been refined once (fresh generate this
      // session, or a re-opened workflow that was saved as refined), run a fast
      // LIGHT touch-up that respects the user's edits instead of a full rewrite.
      const light = !!(workflow && workflow.refined)
      // Capture the ORIGINAL prompt: on a full refine the text being refined IS
      // the raw prompt. Don't overwrite an original the user typed in the editor,
      // and don't clobber it on a light re-refine of already-refined text.
      if (!light && !rawPrompt.trim()) rawPrompt = text
      const data = await bridge.refinePrompt(intentWithGenerationTrigger(text, generationTrigger), compactCatalog(catalog), light)
      refineAnswers = {}
      refinement = {
        original: (data && data.original) || text,
        refined_intent: (data && data.refined_intent) || text,
        summary: (data && data.summary) || '',
        assumptions: (data && Array.isArray(data.assumptions)) ? data.assumptions : [],
        questions: (data && Array.isArray(data.questions)) ? data.questions : [],
        // The framework's architecture call ('workflow' | 'auto' | 'react' | 'plan_execute')
        // and why — so confirmRefinement can build the AGENT directly instead of
        // drawing a fixed flow that a reasoning task can never satisfy.
        recommended_mode: (data && data.recommended_mode) || '',
        recommended_reason: (data && (data.recommended_reason || data.recommendation_rationale)) || '',
      }
    } catch (e) {
      // Refine should never block generation; if it fails, fall back to
      // compiling the original intent directly.
      compileError = (e && e.message) ? `Could not refine prompt (${e.message}); generating from your original text.` : ''
      await runCompile(text, undefined)
      await escalateIfReasoningFit()
    } finally {
      refining = false
    }
  }

  // The local-first promise ("using the cloud is always your choice") was
  // enforced on exactly two buttons — Generate and its streamed variant. Every
  // other builder-model call (Refine prompt, the prompt editor's Generate,
  // Apply answers, Build until it works, Fix with AI, AI review, Write code for
  // this step, and the Inspector's compile/refine) sent the same prompt AND the
  // same setup context (skills, tools, channels) to the cloud with no dialog at
  // all. Refine sits one button to the LEFT of the gated Generate.
  //
  // ensureCloudOk is the single gate. It returns true when the call may go
  // ahead; otherwise it opens the dialog, remembers what to resume, and returns
  // false so the caller stops.
  function ensureCloudOk(retry) {
    if (cloudAck) return true
    if (!(modelAdvice && modelAdvice.cloud_escalation)) return true
    cloudGate = { provider: modelAdvice.provider, model: modelAdvice.model, retry: retry || null }
    return false
  }

  // Cloud-escalation gate: user chose to continue with the cloud builder.
  function approveCloud() {
    // Resume whatever raised the gate. This used to always call generate(),
    // so approving from the STREAMED path silently ran the classic one, and
    // approving from any other action did nothing that action had asked for.
    const retry = cloudGate && cloudGate.retry
    cloudAck = true
    cloudGate = null
    if (typeof retry === 'function') retry()
    else generate()
  }
  // User chose to keep things local — open the model picker to switch.
  function declineCloud() {
    cloudGate = null
    openModelPicker()
  }

  // Confirm the refinement dialog. The framework's recommended architecture
  // decides what we generate: a fixed workflow (canvas) OR an Auto/ReAct/Plan-Execute
  // AGENT (no canvas — it reasons over tools). This is the wizard branch point.
  async function confirmRefinement() {
    if (!refinement || compiling) return
    const text = (refinement.refined_intent || '').trim() || refinement.original
    const ans = Object.keys(refineAnswers).length ? { ...refineAnswers } : undefined
    // Ordinary Generate never creates an experimental fixed graph. A model may
    // still identify a workflow-shaped task, but Studio routes that to
    // Plan-Execute until the operator explicitly selects Workflow.
    const mode = generatedMode(refinement.recommended_mode)
    const reason = refinement.recommended_reason || ''
    refinement = null
    // A tool/reasoning agent (no fixed flow): build the AGENT directly, take the
    // user to SOUL.yaml, and explain via a modal. "auto" is the recommended
    // default — the engine runs it as a reliable native tool-calling loop.
    await runAgentCompile(text, mode, ans)
    if (workflow && workflow.strategy) routeToAgent(mode, reason)
  }

  // routeToAgent finalises the "this is an agent, not a workflow" handoff: show
  // SOUL.yaml (where the agent actually lives) and raise the explainer modal.
  function routeToAgent(mode, reason) {
    agentRoute = {
      mode,
      reason: reason || (workflow && workflow.recommendation && workflow.recommendation.rationale) || '',
    }
    showCodeView()
  }

  // escalateIfReasoningFit converts a freshly-compiled WORKFLOW into the agent it
  // should have been, when the compiler judged the task reasoning-fit. This is
  // what makes "if it can't be a workflow, don't build a workflow" true even when
  // the verdict only emerges from the compile result. Manual toggles never hit
  // this path (they call runAgentCompile/runCompile directly).
  async function escalateIfReasoningFit() {
    const rec = workflow && !workflow.strategy ? workflow.recommendation : null
    const m = rec && rec.mode
    if (m !== 'auto' && m !== 'react' && m !== 'plan_execute') return
    const text = ((intent || (workflow && workflow.intent) || rawPrompt || '')).trim()
    if (!text) return
    await runAgentCompile(text, m)
    if (workflow && workflow.strategy) routeToAgent(m, rec.rationale || '')
  }

  // Generate an Auto/ReAct/Plan-Execute agent (no flow). The result draft carries
  // `strategy`, so the UI renders the agent-spec panel instead of the canvas,
  // and Save persists a strategy-based agent.
  async function runAgentCompile(text, mode, ans) {
    if (!text || compiling) return
    pipelineModalHidden = false
    compiling = true
    compileError = ''
    try {
      const data = await bridge.compileAgent(intentWithGenerationTrigger(text, generationTrigger), mode, ans, compactCatalog(catalog))
      applyCompile(data)
      // Mark the prompt as refined so a later edit + Generate uses the fast
      // LIGHT touch-up pass; persists via the workflow's `refined` field.
      if (workflow) workflow = { ...workflow, intent: text, refined: true, raw_intent: rawPrompt }
      intent = text
    } catch (e) {
      compileError = e.message || 'agent generation failed'
    } finally {
      compiling = false
    }
  }

  function cancelRefinement() {
    if (compiling) return
    refinement = null
  }

  // ── Output → delivery auto-wiring ──────────────────────────────────────────
  // Add/remove a delivery channel on the current draft. Surfaced as one-click
  // suggestions when a draft has no channel wired, so the result actually goes
  // somewhere instead of being produced and dropped.
  function addDeliveryChannel(id) {
    if (!workflow || !id) return
    applyFraming(toggleChannelPatch(workflow, id, true))
  }
  // True when the draft produces output but has nowhere to send it. A
  // channel-triggered agent replies on its trigger channel, so it's exempt.
  $: needsDelivery = !!workflow
    && (!workflow.channels || workflow.channels.length === 0)
    && !(workflow.trigger && workflow.trigger.type === 'channel')
    && channelOptions.length > 0

  // ── Try the agent: run the UNSAVED reasoning agent against one question ─────
  let tryQuestion = ''
  let tryResult = null   // { reply, error, trace, nodeTrace, input } | null
  let trying = false
  // Run Live is gated server-side now. runConfirm holds the 409 preview of what
  // the run would actually touch; runAck is the acknowledgement we replay with
  // once the user has seen it; runBlockers holds the 422 execution blockers.
  let runConfirm = null   // { tools, destinations, data_access, consent, ... }
  let runAck = null       // { confirm_side_effects, acknowledged_tools }
  let runBlockers = []
  // Mirrors the server's 422 condition so the button is refused before the round
  // trip. Warnings deliberately do NOT block — only blockers do.
  //
  // `preflight` alone was not enough: it is populated only by a Save attempt and
  // cleared when that dialog is dismissed, so before the user's first Save this
  // gate was inert and Run live happily started a workflow the server would
  // refuse with a 422. `readiness` is loaded for the draft itself, so it holds
  // whether or not a save has been tried.
  $: hasExecutionBlockers = !!(
    (preflight && (preflight.blockers || []).length) ||
    (readiness && (readiness.blockers || []).length) ||
    (securityReview && (securityReview.blockers || []).length)
  )
  // An empty graph is a distinct reason to refuse, and needs its own message.
  $: flowIsEmpty = !(workflow && workflow.flow && (workflow.flow.nodes || []).length)

  // What the strategy advisor concluded about the selected model, carried
  // through from the compile response so an override can be informed (ST-02).
  let strategyAdvice = null   // { warning, confidence, capabilities, mode, reason }
  let strategyFit = null      // aggregate local model/strategy outcome evidence
  $: unreliableStrategies = ((strategyFit && strategyFit.strategies) || [])
    .filter((row) => row && row.unreliable)
    .map((row) => row.strategy)
  function isStrategyUnreliable(mode) { return unreliableStrategies.includes(mode) }

  async function refreshStrategyFit(model = '', provider = '') {
    try { strategyFit = await bridge.strategyFit(model, provider) } catch (_) { strategyFit = null }
  }

  // The model the capability badge is ABOUT. Deliberately derived from the
  // advisor's own profile rather than from studioModelLabel: that one is the
  // BUILDER model (what Studio generates with), while these capabilities
  // describe the model that will RUN the agent. Showing one next to a badge
  // computed from the other is the exact conflation that makes a capability
  // warning untrustworthy. Empty when the advisor reported no profile — better
  // to name no model than the wrong one.
  $: adviceModelLabel = (() => {
    const c = strategyAdvice && strategyAdvice.capabilities
    if (!c) return ''
    if (c.provider && c.model) return `${c.provider} / ${c.model}`
    return c.model || ''
  })()

  // ── Strategy contract (ST-02) ─────────────────────────────────────────────
  // The per-strategy policy the Build step edits. Merged onto the draft so it
  // saves with everything else; the server fills in defaults for anything the
  // user has not set, and only shows the blocks that apply to the chosen mode.
  function updatePolicy(patch) {
    if (!workflow || !patch) return
    workflow = { ...workflow, policy: { ...(workflow.policy || {}), ...patch } }
    scheduleValidate()
  }
  // Warnings about the contract itself — an agent with no completion criteria
  // stops only when it runs out of budget, which is a timeout pretending to be
  // a decision. Advisory, never blocking: these are the operator's tradeoffs.
  $: policyWarnings = (() => {
    if (!workflow || currentMode === 'workflow') return []
    const p = workflow.policy || {}
    const c = p.contract || {}
    const r = p.react || {}
    const out = []
    if (!String(c.completion_criteria || '').trim()) {
      out.push('No completion criteria: the only thing that will stop this agent is its step budget.')
    }
    if (currentMode === 'react') {
      const conf = r.confidence_threshold
      if (conf != null && (conf < 0 || conf > 1)) out.push('Confidence threshold must be between 0 and 1.')
      if (r.invalid_step_budget === 0) {
        out.push('An invalid-step budget of 0 lets a malformed tool call retry until the whole run budget is gone.')
      }
      if (r.fallback_to_auto === false && r.preserve_best_result === false) {
        out.push('With neither fallback-to-Auto nor preserve-best-result, a loop that exhausts its budget returns nothing at all.')
      }
    }
    return out
  })()
  async function tryAgent() {
    if (trying || !workflow) return
    const isAgent = !!workflow.strategy
    const runnable = isAgent || (workflow.flow && (workflow.flow.nodes || []).length)
    if (!runnable) return
    // Agents take a free-form question; workflows take the sample input.
    const q = (isAgent ? tryQuestion : sampleInput).trim()
    if (!q) { compileError = 'Enter a sample input to run.'; return }
    trying = true
    tryResult = null
    runBlockers = []
    try {
      const res = await bridge.tryAgent(workflow, q, runAck)
      tryResult = {
        reply: (res && res.reply) || '',
        error: (res && res.error) || '',
        trace: (res && Array.isArray(res.trace)) ? res.trace : [],
        nodeTrace: (res && Array.isArray(res.node_trace)) ? res.node_trace : [],
        // What the run actually did to the outside world. Shown even when the
        // run failed — especially then, since "did it already send the message?"
        // is the first question after a half-completed run.
        executed: (res && Array.isArray(res.executed_side_effects)) ? res.executed_side_effects : [],
        // Keep the input that produced this run. A repair is only PROVEN by
        // replaying the exact input that broke it, so losing this downgrades
        // every subsequent fix to "applied but unverified".
        input: q,
      }
    } catch (e) {
      // The server now gates Run Live rather than quietly escalating privileges.
      // 422 = execution blockers; 409 = real side effects the user has not
      // acknowledged yet, with a preview of exactly what would happen.
      if (e && e.status === 409 && e.body) {
        runConfirm = e.body
        tryResult = null
        return
      }
      if (e && e.status === 422 && e.body) {
        const pf = e.body.preflight || {}
        const ct = e.body.contract || {}
        runBlockers = (
          (Array.isArray(pf.blockers) && pf.blockers.length ? pf.blockers : null)
          || (Array.isArray(ct.checks) ? ct.checks.filter((c) => c && c.status === 'block') : null)
          || (e.body.error ? [e.body.error] : [])
        ).slice()
        tryResult = null
        return
      }
      tryResult = { reply: '', error: (e && e.message) || 'run failed', input: q, executed: [] }
    } finally {
      trying = false
    }
    // A fresh run invalidates any prior repair proposals.
    repairProposals = []
    repairError = ''
    repairDone = ''
  }

  // The user has read the preview and accepted the side effects: replay the run
  // carrying that acknowledgement. Scoped to the tools the preview actually
  // listed, so acknowledging today's run does not pre-approve a tool added later.
  function confirmRun() {
    if (!runConfirm) return
    runAck = {
      confirm_side_effects: true,
      acknowledged_tools: runConfirmTools.map(
        t => (typeof t === 'string' ? t : t && t.name)
      ).filter(Boolean),
    }
    runConfirm = null
    tryAgent()
  }
  function cancelRun() { runConfirm = null }

  // The 409 body from /studio/try-agent is
  //   { error, requires_confirmation, unacknowledged_tools, preview }
  // and every categorised tool list lives on preview.summary (SecuritySummary).
  // The dialog used to read `runConfirm.confirm_tools` etc. — one and two levels
  // too high — so all six lists were permanently empty: the user was asked to
  // approve real, irreversible side effects with nothing shown, and the
  // acknowledgement that went back was an empty array.
  $: runConfirmPreview = (runConfirm && runConfirm.preview) || {}
  $: runConfirmSummary = runConfirmPreview.summary || {}
  $: runConfirmTools = (runConfirm && runConfirm.unacknowledged_tools)
    || runConfirmPreview.side_effect_tools || []

  // Any edit invalidates the acknowledgement. Deliberately conservative: the
  // preview the user approved described THAT draft, and an edit can add a tool
  // that sends or deletes something. Re-asking is cheap; a silent escalation
  // because the ack outlived the draft it described is not.
  $: if (workflow) runAck = null

  // Copy any value (full node input/output) to the clipboard for offline testing.
  let copiedKey = ''
  async function copyValue(text, key) {
    try {
      await navigator.clipboard.writeText(text || '')
      copiedKey = key
      setTimeout(() => { if (copiedKey === key) { copiedKey = ''; } }, 1500)
    } catch (_) { /* clipboard blocked; ignore */ }
  }
  // Soft failure: a node ran (no error) but its OUTPUT reports an error field.
  function nodeSoftError(n) {
    const raw = (n && (n.output_full || n.output)) || ''
    if (!raw) return ''
    try {
      const o = JSON.parse(raw)
      if (o && typeof o === 'object') {
        for (const k of ['error', 'errors', 'err']) {
          if (typeof o[k] === 'string' && o[k].trim()) return o[k]
        }
      }
    } catch (_) { /* not JSON */ }
    return ''
  }
  function nodeLooksWrong(n) { return !!(n && (n.error || nodeSoftError(n))) }

  // ── Observe-and-adjust: learn from the last Run Live and fix a node ─────────
  // After a run where a node broke because a real API returned an unexpected
  // shape, ask Studio to observe the actual node outputs and propose adjustments.
  let repairing = false
  let repairProposals = []   // [{ node_id, field, class, old, new, rationale, auto, observed_keys }]
  let repairError = ''
  let repairDone = ''
  // True when the last run has at least one node worth adjusting — either a hard
  // error OR a soft failure (ran green but its output reports an error).
  $: liveHasFailure = !!(tryResult && (tryResult.error || (tryResult.nodeTrace || []).some(nodeLooksWrong)))

  async function adjustToLiveOutput() {
    if (repairing || !workflow || !tryResult) return
    repairing = true
    repairError = ''
    repairDone = ''
    repairProposals = []
    try {
      const res = await bridge.repairLive(workflow, tryResult.nodeTrace || [])
      const props = (res && Array.isArray(res.proposals)) ? res.proposals : []
      if (!props.length) {
        repairDone = 'No automatic adjustment found. The failing node may be a real API/auth error rather than a format mismatch — check the node output below.'
      }
      repairProposals = props
    } catch (e) {
      repairError = (e && e.message) || 'could not analyze the run'
    } finally {
      repairing = false
    }
  }

  // Diff preview (Epic 4): compute what a repair proposal would change in the
  // SOUL.yaml WITHOUT saving, so the user can review before applying.
  let repairDiff = null   // { p, candidate, lines, stats }
  let repairDiffBusy = false
  let repairDiffFor = null // the proposal currently being diffed
  async function previewProposal(p) {
    if (!workflow || repairDiffBusy) return
    // Which proposal is being diffed. The label used to key off `repairDiff`,
    // which is only set AFTER both round-trips finish — so the busy label could
    // only ever appear on a repeat preview of the same proposal.
    repairDiffFor = p
    repairDiffBusy = true
    repairError = ''
    try {
      // Preview only: send the trace so the server judges the SAME candidate it
      // would judge on apply. Without it a repair the replay would reject still
      // previews as a clean diff, which is the mismatch this whole path exists
      // to remove.
      // preview:true — same judgement, no durable learning. Looking at a change
      // must not teach the generator from a repair the user hasn't accepted.
      const res = await bridge.applyRepair(
        workflow, p, tryResult && tryResult.input, (tryResult && tryResult.nodeTrace) || [], true
      )
      // On a rejected repair the server returns the ORIGINAL draft, so diffing it
      // would render an empty change and read as "this does nothing" rather than
      // "this was tested and rejected".
      const candidate = res && res.applied ? res.workflow : null
      if (!candidate) {
        repairError = repairVerdict(res, p) || 'Could not compute the change.'
        return
      }
      const d = await api.studio.diff(workflow, candidate)
      repairDiff = {
        p, candidate, res,
        lines: (d && d.lines) || [],
        stats: (d && d.stats) || { added: 0, removed: 0 },
        proof: repairProofLabel(res),
      }
    } catch (e) {
      repairError = (e && e.message) || 'could not preview the change'
    } finally {
      repairDiffBusy = false
      repairDiffFor = null
    }
  }
  function applyDiff() {
    if (!repairDiff) return
    workflow = repairDiff.candidate
    rebuildGraph()
    scheduleValidate()
    repairProposals = repairProposals.filter(x => x !== repairDiff.p)
    // The candidate in this diff was already judged by the server (the preview
    // runs the same replay), so report THAT verdict rather than the old blanket
    // "Run Live again to confirm" — which was wrong for a proven fix.
    repairDone = repairVerdict(repairDiff.res, repairDiff.p) || 'Applied.'
    repairDiff = null
  }
  function cancelDiff() { repairDiff = null }

  async function applyProposal(p) {
    if (!workflow || p._applying) return
    p._applying = true
    repairProposals = repairProposals
    try {
      const res = await bridge.applyRepair(
        workflow, p, tryResult && tryResult.input, (tryResult && tryResult.nodeTrace) || []
      )
      if (res && res.applied) {
        workflow = res.workflow          // patch the draft in place (triggers reactivity)
        if (res.valid === false) {
          repairError = 'Applied, but the node still needs attention: ' + ((res.errors || []).join('; '))
        } else {
          repairDone = repairVerdict(res, p)
        }
        // Drop the applied proposal from the list.
        repairProposals = repairProposals.filter(x => x !== p)
      } else {
        // Rolled back: the change was built, replayed, and did not hold up. Say
        // WHY — the whole point of proving a repair is that the user learns the
        // fix was wrong here rather than on the next real run.
        repairError = repairVerdict(res, p) || 'Could not apply the adjustment.'
      }
    } catch (e) {
      repairError = (e && e.message) || 'apply failed'
    } finally {
      p._applying = false
      repairProposals = repairProposals
    }
  }

  function rejectProposal(p) {
    repairProposals = repairProposals.filter(x => x !== p)
  }

  // ── Execution-mode override (Workflow ⇄ Auto ⇄ ReAct ⇄ Plan-Execute) ───────
  // The current draft's mode: a fixed workflow (no strategy) or a reasoning agent.
  // modeOf is the single definition; currentMode is just its reactive view.
  // Event handlers must call modeOf(workflow) directly, because a reactive
  // value still holds the previous draft's mode during the handler that
  // replaced the draft.
  function modeOf(wf) {
    return wf && wf.strategy ? String(wf.strategy).toLowerCase() : 'workflow'
  }
  $: currentMode = modeOf(workflow)

  // switchMode lets the developer override how Studio classified the task —
  // converting a result that's better as an agent (e.g. an autonomous
  // skill router built as a brittle fixed flow) into an Auto/Plan-Execute agent,
  // or back to a workflow. It re-compiles the original intent in the chosen mode,
  // reusing the same agent/flow compilers Generate uses.
  async function switchMode(mode) {
    if (compiling || mode === currentMode) return
    const text = ((intent || (workflow && workflow.intent) || rawPrompt || '')).trim()
    if (!text) {
      compileError = 'Add a prompt (Generate from a description) before switching mode.'
      return
    }

    // Workflow generation is never a one-click strategy switch. Opening this
    // dialog is the explicit opt-in boundary shared by the strategy panel and
    // the "build as workflow anyway" escape hatch.
    if (mode === 'workflow') {
      workflowExperiment = { text, from: currentMode }
      return
    }
    await performSwitchMode(mode, text)
  }

  async function performSwitchMode(mode, text, skipReplaceConfirm = false) {
    // Switching modes RE-COMPILES from the prompt and discards any manual edits to
    // the current draft (system prompt, tools, skills, or canvas wiring). Confirm
    // before throwing that work away.
    if (workflow && !skipReplaceConfirm) {
      let ok = true
      try {
        ok = confirmLocal(`Switch to ${recoLabel(mode)}? This regenerates from your prompt and discards any manual edits to the current ${currentMode === 'workflow' ? 'workflow' : 'agent'}.`)
      } catch (_) { ok = true }
      if (!ok) return
    }
    // Explicit Workflow pick forces a fixed workflow — otherwise the server's
    // reasoning-fit routing re-classifies the task and reverts to an agent.
    if (mode === 'workflow') await runCompile(text, undefined, true)
    else await runAgentCompile(text, mode)
  }

  async function confirmExperimentalWorkflow() {
    const pending = workflowExperiment
    workflowExperiment = null
    if (!pending || compiling) return
    await performSwitchMode('workflow', pending.text, true)
  }

  async function useAutoInstead() {
    const pending = workflowExperiment
    workflowExperiment = null
    if (!pending || compiling || currentMode === 'auto') return
    await performSwitchMode('auto', pending.text)
  }

  // runCompile performs the actual compile + canvas rebuild from a finalized
  // (refined) intent. Shared by the confirm path and the refine-failure
  // fallback.
  async function runCompile(text, ans, forceWorkflow = false) {
    if (!text || compiling) return
    pipelineModalHidden = false
    compiling = true
    compileError = ''
    try {
      const data = await bridge.compile(intentWithGenerationTrigger(text, generationTrigger), ans, compactCatalog(catalog), rawPrompt, forceWorkflow)
      applyCompile(data)
      // Remember the prompt on the draft so it persists through save/load and
      // the box stays populated for further edits. Mark it refined so a later
      // edit + Generate uses the fast LIGHT touch-up pass.
      if (workflow) workflow = { ...workflow, intent: text, refined: true, raw_intent: rawPrompt }
      // Surface the refined intent in the editor so further edits start from the
      // clarified version, not the original rough text.
      intent = text
    } catch (e) {
      compileError = e.message || 'compile failed'
    } finally {
      compiling = false
    }
  }

  // Prompt editor — "Refine": take the user's ORIGINAL prompt (rawPrompt) and
  // run the refine pass, dropping the result into the refined box (intent).
  async function refineFromModal() {
    const raw = rawPrompt.trim()
    if (!raw || modalRefining) return
    if (!ensureCloudOk(refineFromModal)) return
    // A previous "Run in background" must not silence THIS action too.
    pipelineModalHidden = false
    modalRefining = true
    promptError = ''
    // Pin what the NEXT spec read should be compared against, so the panel can
    // report what refining actually changed. ST-01 asks for a visible change
    // summary, and without a baseline all a refine can show is a new spec.
    buildSpecPrevIntent = (intent || raw).trim()
    try {
      const data = await bridge.refinePrompt(intentWithGenerationTrigger(raw, generationTrigger), compactCatalog(catalog), false)
      intent = (data && data.refined_intent) || raw
      if (workflow) workflow = { ...workflow, raw_intent: raw }
    } catch (e) {
      promptError = (e && e.message) ? `Could not refine (${e.message})` : 'Could not refine the prompt'
    } finally {
      modalRefining = false
    }
  }

  // Prompt editor — "Generate": build the agent straight from the REFINED
  // prompt (intent), skipping a re-refine. Falls back to the original if the
  // refined box is empty (e.g. the user only filled the original and hit go).
  async function generateFromModal() {
    const text = intentOf(intent, rawPrompt)
    if (!text || compiling || refining) return
    if (!ensureCloudOk(generateFromModal)) return
    if (workflow && workflow.flow && (workflow.flow.nodes || []).length) {
      let ok = true
      try { ok = confirmLocal('Generate from this prompt? It replaces the current workflow on the canvas.') } catch (_) { ok = true }
      if (!ok) return
    }
    promptViewer = false
    if (workflow && workflow.strategy) await runAgentCompile(text, workflow.strategy, undefined)
    else await runCompile(text, undefined)
  }

  async function applyAnswers() {
    // Re-send compile with the current answers map -> re-render.
    if (compiling) return
    if (!ensureCloudOk(applyAnswers)) return
    compiling = true
    compileError = ''
    try {
      // intentOf for the same reason as generate(): re-compiling with
      // answers must use the prompt the user actually wrote, refined or not.
      const data = await bridge.compile(intentWithGenerationTrigger(intentOf(intent, rawPrompt), generationTrigger), answers, compactCatalog(catalog))
      applyCompile(data)
    } catch (e) {
      compileError = e.message || 'compile failed'
    } finally {
      compiling = false
    }
  }

  // Friendly label for the compiler's recommended execution mode.
  function recoLabel(mode) {
    if (mode === 'auto') return 'Auto tool agent'
    if (mode === 'react') return 'ReAct (advanced loop)'
    if (mode === 'plan_execute') return 'Plan-Execute'
    if (mode === 'workflow') return 'Workflow (experimental fixed flow)'
    return mode || 'Auto tool agent'
  }

  // recoLabel('auto') already ends in "agent", but every call site that names a
  // build appends " agent" itself — producing "Generate Auto tool agent agent"
  // on the confirm dialog's primary button. Rather than strip the noun from the
  // label (which would leave a bare "Auto tool" where the label stands alone),
  // this adds it only when it is not already there.
  function recoAgentLabel(mode) {
    const label = recoLabel(mode)
    return /\bagents?$/i.test(label) ? label : `${label} agent`
  }

  // "Built as a Auto tool agent" — the article is hardcoded in the copy, so it
  // is wrong for every label starting with a vowel. Chosen from the label rather
  // than written into each sentence, so adding a strategy cannot reintroduce it.
  function recoArticle(mode) {
    return /^[aeiou]/i.test(recoAgentLabel(mode)) ? 'an' : 'a'
  }

  // resetTransientDraftState clears everything tied to a SPECIFIC prior draft so
  // it can't bleed into a freshly generated/loaded one. Without this, a new draft
  // inherited the previous agent's run history, build report, a leftover preflight
  // (which silently disabled Save), or a stale agent-route modal. Called by both
  // applyCompile (post-generate) and setWorkflow (template/load/import).
  function resetTransientDraftState() {
    loadedAgentId = null
    // A new or newly-opened draft is unsaved work again, so it must be carried
    // across navigation. Leaving this true after one save would silently stop
    // preserving every subsequent draft.
    sessionCommitted = false
    runTrace = null
    runDiagnosis = null
    runHistory = []
    selectedRunId = null
    // A new draft must not leave the trace panel pointed at the last agent
    // whose failure was inspected.
    runTraceAgentId = ''
    runTraceErr = ''
    buildReport = null
    buildLog = []
    buildGlue = []
    buildTraceId = null
    agentRoute = null
    preflight = null
    consent = null
    plan = null
    validation = null
    tryQuestion = ''
    tryResult = null
    generationProfile = null
    // A different draft has different side effects, so an acknowledgement of the
    // previous one must not carry over — that would be exactly the silent
    // escalation the Run Live gate exists to prevent.
    runConfirm = null
    runAck = null
    runBlockers = []
    strategyAdvice = null
    initialGeneratedWorkflow = null
    // A readiness verdict describes ONE draft. Leaving it up across a swap let
    // the Save step answer "ready?" about the draft the user just replaced.
    readiness = null
    readinessError = ''
    // Swapping in a different draft invalidates the SOUL.yaml view's serialization
    // (which is only re-generated on entry). Drop back to canvas so a stale YAML
    // can't be shown — or, worse, saved over the new draft. routeToAgent re-opens
    // SOUL.yaml afterwards when it wants the code view.
    if (viewMode === 'code') { viewMode = 'canvas'; codeYaml = ''; codeOrig = '' }
  }

  // `gate` opens the post-generation blocker dialog. It belongs to a generation
  // that SUCCEEDED but produced a draft that cannot run — that is the case it
  // was written for. A generation that FAILED must not raise it: the blockers
  // then describe the hole where the draft should be ("flow: no nodes declared",
  // "no runnable steps"), which buries the actual cause under three complaints
  // about a draft the user never got.
  function applyCompile(data, { gate = true } = {}) {
    // Remember which saved agent we were editing BEFORE reset clears it.
    const prevAgentId = loadedAgentId
    resetTransientDraftState()
    workflow = applyGenerationTrigger((data && data.workflow) || null, generationTrigger)
    // If we were editing an existing saved agent, keep the freshly generated
    // draft bound to that agent so re-generating from a tweaked prompt UPDATES
    // it instead of silently saving a brand-new duplicate (the "Flight Finder →
    // Flight Deal Finder" bug). Use "New agent" to intentionally start a
    // separate one — that path clears loadedAgentId first.
    if (prevAgentId && workflow) {
      loadedAgentId = prevAgentId
      workflow.id = prevAgentId
    }
    // Preserve exactly what generation produced. Later Goal/Instructions/prompt
    // edits are sent alongside the final draft so the backend can mine repeated
    // preferences across agents without guessing from the saved file.
    try { initialGeneratedWorkflow = workflow ? JSON.parse(JSON.stringify(workflow)) : null } catch (_) { initialGeneratedWorkflow = null }
    questions = (data && Array.isArray(data.questions)) ? data.questions : []
    notes = (data && Array.isArray(data.notes)) ? data.notes : []
    notesExpanded = false
    explanation = (data && data.explanation) || null
    generationProfile = (data && data.generation) || null
    // The strategy advisor computes a capability warning, a confidence and the
    // model's capability profile, and the compile response now carries all
    // three. They used to be dropped on the floor, which made the mode override
    // possible but not INFORMED: forcing ReAct onto a model that cannot sustain
    // it produced no warning even though the backend had already worked that out.
    strategyAdvice = (data && (data.capability_warning || data.confidence || data.capabilities))
      ? {
          warning: (data && data.capability_warning) || '',
          confidence: (data && data.confidence) || '',
          capabilities: (data && data.capabilities) || null,
          mode: (data && data.strategy_mode) || '',
          reason: (data && data.strategy_reason) || '',
        }
      : null
    refreshStrategyFit(workflow && workflow.llm && workflow.llm.model, workflow && workflow.llm && workflow.llm.provider).catch(() => {})
    if (gate && data && data.contract) {
      const generatedGate = contractToPreflight(data.contract)
      if (generatedGate.blockers.length) {
        preflight = {
          ok: false,
          blockers: generatedGate.blockers,
          warnings: generatedGate.warnings,
          contract: data.contract,
          generated: true,
        }
      }
    }
    // M4: surface missing-capability suggestions (non-blocking). Keep only the
    // ones not yet installed; a fresh compile resets any in-flight discovery.
    suggestions = (data && Array.isArray(data.suggestions))
      ? data.suggestions.filter((s) => s && s.installed !== true)
      : []
    discoverState = {}
    selectedNode = null
    selectedEdge = null
    rebuildGraph()
    scheduleValidate()        // validate the fresh draft (debounced)
    // No autosave draft here — see the note by refreshPaletteDrafts. A generated
    // workflow is recoverable via studioSession for the session; persisting it
    // as a draft cluttered the list with entries the user never chose to keep.
    // Guided default (Guided Studio Builder): land a freshly generated
    // deterministic workflow in the simple Plan view so the user reviews the
    // lanes + plain-English plan first. Agents keep their spec view.
    // ...and conversely, an agent must NOT be left on 'plan' from a previous
    // workflow draft: the Plan tab is rendered only for workflows, so the user
    // would be looking at a view with no tab to leave it by.
    if (workflow && !workflow.strategy) viewMode = 'plan'
    else if (workflow && viewMode === 'plan') viewMode = 'canvas'
    // A finished generate IS a milestone, so advance here rather than reactively.
    // Deliberately Build and not Test: the first thing you want after generating
    // is to see what was built, not to skip past it to a run.
    if (workflow) advanceToStep(STEP_BUILD)
  }

  // ── M6: set the current draft directly (templates / draft-load / import) ───
  // Shared by every path that swaps in a complete workflow WITHOUT a compile
  // round-trip. Mirrors the post-compile reset so the canvas, inspector,
  // plan/tier and validation all refresh consistently.
  // keepStep leaves wizard navigation alone. Used when the draft is being
  // REPLACED IN PLACE rather than opened — an auto-fix during the Save review
  // swaps the workflow, and navigating to Build there would throw the user out
  // of the review they were in the middle of.
  // keepPrompt is for IN-PLACE replacements (auto-fix, heal, troubleshoot, a
  // YAML re-parse): those swap the same draft for a repaired version, so the
  // user's prompt still describes it. Every OPEN (template, draft, agent,
  // import, clone) adopts the incoming draft's own prompt instead — including
  // adopting an EMPTY one, which is what stops a stale prompt from being shown
  // against, and generated over, a different agent.
  // Snapshot of the draft as it was LOADED. Compared against the live draft to
  // tell "the user has edited this" from "this is exactly what was opened".
  // Cheap because it is only taken on load and read on a view switch.
  let draftFingerprint = ''
  function fingerprintOf(wf) {
    try { return wf ? JSON.stringify(wf) : '' } catch (_) { return '' }
  }
  function draftEdited() {
    return fingerprintOf(workflow) !== draftFingerprint
  }

  function setWorkflow(wf, { name, keepStep = false, keepPrompt = false } = {}) {
    // Clear prior-draft state first; callers that re-establish identity (e.g.
    // openAgentOnCanvas setting loadedAgentId) do so AFTER this returns.
    resetTransientDraftState()
    // Migrate legacy draggable entry/exit NODES into trigger/delivery settings
    // so old flows adopt the lane model (Guided Studio Builder, Story 2).
    workflow = wf ? migrateEndpoints(wf) : null
    if (workflow && name && !workflow.name) workflow = { ...workflow, name }
    // Adopt the generating prompt of the draft being opened so the user can see
    // and edit the instruction that produced it. A draft with no stored prompt
    // adopts an EMPTY one — the previous draft's text must not survive here.
    if (!keepPrompt) {
      const p = promptsForDraft(workflow)
      intent = p.intent
      rawPrompt = p.rawPrompt
    }
    questions = []
    notes = []
    explanation = null
    suggestions = []
    discoverState = {}
    selectedNode = null
    selectedEdge = null
    plan = null
    validation = null
    saveMsg = ''
    saveError = ''
    // Taken AFTER the draft is fully established (migration + name applied) so a
    // draft nobody has touched compares equal to what was opened.
    draftFingerprint = fingerprintOf(workflow)
    refineState = { loading: false, error: '', message: '' }
    // Swap in this draft's saved test suite. hydrateBench always assigns, so a
    // workflow with no fixtures correctly empties the bench rather than
    // inheriting the previous draft's mocks — which would silently test the new
    // workflow against the old one's canned data.
    hydrateBench(workflow)
    rebuildGraph()
    scheduleValidate()
    // Opening a draft lands on Build; clearing the canvas returns to Describe,
    // because Describe is the only step with anything to show for an empty
    // workspace. Both are discrete actions, not typing.
    if (keepStep) return
    if (workflow) advanceToStep(STEP_BUILD)
    else { step = STEP_DESCRIBE; applyStepLayout(STEP_DESCRIBE) }
  }

  // ── Test fixtures live on the draft (ST-10) ───────────────────────────────
  // The bench used to hold these in component state, so every reload silently
  // discarded the suite. hydrateBench reads them in; persistBench writes them
  // back on every edit, which also means Save carries them without a separate
  // "save tests" step the user would forget.
  function hydrateBench(wf) {
    const f = fixturesFromWorkflow(wf)
    assertions = f.assertions
    mockText = f.mockText
    mockErrors = {}
    if (f.sampleInput) sampleInput = f.sampleInput
    testVariables = f.variables
    testEnvironment = f.environment
    testStartNode = f.startNode
  }

  function persistBench() {
    if (!workflow) return
    const outcome = outcomeWithFixtures(workflow.outcome, {
      assertions, mockText, sampleInput,
      variables: testVariables,
      environment: testEnvironment,
      startNode: testStartNode,
    })
    // Only touch the draft when the persisted shape actually changed, otherwise
    // every keystroke in the bench would churn `workflow` and re-trigger the
    // validation/security passes that watch it.
    const before = JSON.stringify(workflow.outcome || null)
    const after = JSON.stringify(outcome || null)
    if (before === after) return
    workflow = { ...workflow, outcome }
  }

  // ── Framing edits (trigger / output channels) ─────────────────────────────
  // Channels available to pick from (catalog payload is { channels: [...] }).
  $: channelOptions = (catalog && catalog.channels && catalog.channels.channels)
    ? catalog.channels.channels.map((ch) => ({ id: ch.id, name: ch.name || ch.id }))
    : []

  // Merge an Inspector patch (e.g. { trigger } or { channels }) into the
  // in-memory draft so subsequent Test/Save use the edited values, then
  // re-render the START/SINK framing on the canvas. A new object reference
  // keeps Svelte reactivity firing.
  function applyFraming(patch) {
    if (!workflow) return
    workflow = { ...workflow, ...patch }
    plan = null               // edits can change the tier; re-plan on next save
    rebuildGraph()
    scheduleValidate()        // re-validate after a framing edit
  }

  // Merge a patch into a single draft edge (flow.edges[index]) and re-render.
  // Used by the Inspector's `if` predicate field (selected edge or Edges list)
  // so Test/Save/Validate pick up the edited condition.
  function applyEdgePatch(index, patch) {
    if (!workflow || !workflow.flow || !Array.isArray(workflow.flow.edges)) return
    const edgesArr = workflow.flow.edges
    if (index < 0 || index >= edgesArr.length) return
    const nextEdges = edgesArr.map((e, i) => {
      if (i !== index) return e
      const merged = { ...e, ...patch }
      // Writing a port drops any legacy camelCase twin so the two can't
      // disagree about which port this wire uses.
      if ('from_port' in patch) delete merged.fromPort
      if ('to_port' in patch) delete merged.toPort
      return merged
    })
    workflow = { ...workflow, flow: { ...workflow.flow, edges: nextEdges } }
    // Keep the selected-edge mirror in sync so the field stays controlled.
    if (selectedEdge && selectedEdge.index === index) {
      selectedEdge = { index, edge: nextEdges[index] }
    }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  function onNodeClick(event) {
    // SvelteFlow dispatches { node } in event.detail.
    const n = event?.detail?.node
    selectedEdge = null
    if (!n || !n.data || !n.data.node) { selectedNode = null; return }
    selectedNode = n.data.node
  }

  // ── Visual edge wiring ────────────────────────────────────────────────────
  // The framing START/SINK chips (__trigger__/__output__) are synthetic — they
  // aren't real flow nodes. The OUTPUT sink can't be wired by hand, but the
  // TRIGGER can: dragging from it to a node sets that node as the entry, so the
  // trigger behaves like a normal, re-pointable connector.
  const TRIGGER_ID = '__trigger__'
  const OUTPUT_ID = '__output__'
  const isFramingId = (id) => typeof id === 'string' && id.startsWith('__')

  function realNodeIds() {
    return new Set(((workflow && workflow.flow && workflow.flow.nodes) || []).map((n) => n.id))
  }

  // Predicate xyflow calls DURING a drag to allow/reject a connection (gives the
  // user live valid/invalid feedback). Reject self-loops, framing nodes, unknown
  // nodes, and exact duplicates so invalid edges can't be drawn.
  function isValidConnection(conn) {
    const c = (conn && conn.connection) || conn || {}
    const { source, target, sourceHandle, targetHandle } = c
    if (!source || !target) return false
    if (source === target) return false
    // Trigger → any real node is allowed (re-points the entry).
    if (source === TRIGGER_ID) return realNodeIds().has(target)
    // Any real node → output is allowed (designates the flow's output node).
    if (target === OUTPUT_ID) return realNodeIds().has(source)
    if (isFramingId(source) || isFramingId(target)) return false
    const ids = realNodeIds()
    if (!ids.has(source) || !ids.has(target)) return false
    if (edgeExists(source, target, sourceHandle, targetHandle)) return false
    // Design-time type safety: a wire whose producer port type does not
    // structurally satisfy the consumer port type is rejected DURING the drag,
    // so the wrong connection is physically impossible to draw (principle #1).
    const rawNodes = (workflow && workflow.flow && workflow.flow.nodes) || []
    return validateConnection({ nodes: rawNodes, source, target, sourceHandle, targetHandle }).ok
  }

  // Ports are persisted as from_port/to_port (the SOUL.yaml + API contract).
  // Older drafts may still carry the camelCase spelling, so read both.
  function edgeFromPort(e) { return (e && (e.from_port || e.fromPort)) || '' }
  function edgeToPort(e) { return (e && (e.to_port || e.toPort)) || '' }

  function edgeExists(from, to, fromPort, toPort) {
    const edges = (workflow && workflow.flow && workflow.flow.edges) || []
    return edges.some((e) =>
      e.from === from && e.to === to &&
      edgeFromPort(e) === (fromPort || '') &&
      edgeToPort(e) === (toPort || ''))
  }

  // A connection was drawn on the canvas — persist it as a real flow edge.
  //
  // IMPORTANT: @xyflow/svelte delivers connections via the `onconnect` CALLBACK
  // PROP (it dispatches no Svelte `connect` event), and it passes the Connection
  // object DIRECTLY — not wrapped in `event.detail`. The library also optimistically
  // adds a default edge to its own store; persisting here + rebuildGraph() replaces
  // that optimistic edge with our real, typed flow edge so it survives the next
  // re-render (previously, drawing then dropping a block wiped the connection).
  function onConnect(conn) {
    const c = (conn && (conn.connection || conn.detail || conn)) || null
    if (!c || !c.source || !c.target) return
    // Dragging from the trigger re-points the entry rather than adding an edge.
    if (c.source === TRIGGER_ID) { setEntry(c.target); return }
    // Dragging a node onto the output box designates it as the flow's output.
    if (c.target === OUTPUT_ID) { setOutput(c.source); return }
    addEdge({ from: c.source, to: c.target, fromPort: c.sourceHandle, toPort: c.targetHandle })
  }

  // Append a new flow edge (shared by the canvas connect handler and the
  // Inspector "Add connection" button). Validates, dedupes, bakes positions.
  function addEdge(spec) {
    if (!workflow || !workflow.flow || !spec) return
    const { from, to } = spec
    const fromPort = spec.from_port || spec.fromPort || ''
    const toPort = spec.to_port || spec.toPort || ''
    if (!from || !to || from === to) return
    if (isFramingId(from) || isFramingId(to)) return
    const ids = realNodeIds()
    if (!ids.has(from) || !ids.has(to)) return
    if (edgeExists(from, to, fromPort, toPort)) return
    // Same design-time type gate as the canvas drag, so the Inspector's manual
    // "Add connection" can't introduce a type-mismatched wire either.
    const rawNodes = (workflow.flow && workflow.flow.nodes) || []
    const compat = validateConnection({ nodes: rawNodes, source: from, target: to, sourceHandle: fromPort, targetHandle: toPort })
    if (!compat.ok) { toast(compat.reason || 'Incompatible connection'); return }
    const flow = workflow.flow
    const edges = Array.isArray(flow.edges) ? flow.edges : []
    const newEdge = { from, to, if: '' }
    if (fromPort) newEdge.from_port = fromPort
    if (toPort) newEdge.to_port = toPort
    const nextEdges = [...edges, newEdge]
    workflow = { ...workflow, flow: { ...flow, nodes: bakePositions(flow.nodes), edges: nextEdges } }
    selectedNode = null
    selectedEdge = { index: nextEdges.length - 1, edge: nextEdges[nextEdges.length - 1] }
    plan = null
    rebuildGraph()
    scheduleValidate()
  }

  // Snapshot live canvas node positions into the draft before plan/save so
  // drag-repositioning persists with the saved workflow.
  function commitGraphPositions() {
    if (!workflow || !workflow.flow) return
    workflow = { ...workflow, flow: { ...workflow.flow, nodes: bakePositions(workflow.flow.nodes) } }
  }

  // Edge click -> select it for `if`-predicate editing. The xyflow edge carries
  // our data.index (ordinal in flow.edges) so we can read/write the right slot.
  function onEdgeClick(event) {
    const e = event?.detail?.edge
    const idx = e && e.data && Number.isInteger(e.data.index) ? e.data.index : -1
    if (idx < 0 || !workflow || !workflow.flow || !Array.isArray(workflow.flow.edges)) {
      selectedEdge = null
      // Framing edges (trigger→entry, node→output) are derived, not stored, so
      // they can't be deleted — explain how to change them instead of silently
      // ignoring the click.
      const id = e && typeof e.id === 'string' ? e.id : ''
      if (id.startsWith('e-trigger-')) {
        toast('The trigger always feeds the start node — it can’t be deleted. Drag the trigger to another block, or use “Make this the start node”, to re-point it.')
      } else if (id.includes('-output-')) {
        toast('Output links come from your Output Channels, not a stored edge. Click empty canvas, then uncheck Output Channels in the Inspector to remove them.')
      }
      return
    }
    selectedNode = null
    selectedEdge = { index: idx, edge: workflow.flow.edges[idx] }
  }

  // ── Test bench (M5) ─────────────────────────────────────────────────────
  // A real test bench: sample input + mode (dry/live), per-node output MOCKS,
  // ASSERTIONS, a per-node trace with mocked badges, assertion pass/fail, an
  // overall banner, and an IN-MEMORY run history with replay. The iframe is
  // sandboxed without same-origin (no localStorage), so EVERY bit of this state
  // lives in component memory only and is lost on reload — surfaced to the user.
  let testing = false
  let testError = ''
  let testResult = null       // { trace, result, assertions, passed, mode, warnings, shapes }
  // Phase D: the last run's captured output shapes (var -> sample), fed into the
  // per-node compiler so it wires downstream steps against REAL data.
  let lastShapes = []
  $: if (testResult && Array.isArray(testResult.shapes) && testResult.shapes.length) lastShapes = testResult.shapes
  let sampleInput = 'hello'
  let testMode = 'dry'        // 'dry' (default) | 'live' (rendered DISABLED)

  // The saved/loaded agent this draft corresponds to (set on save or when a
  // workflow is loaded onto the canvas). Enables fetching the agent's live
  // per-block run trace — what really happened on its last real run.
  let loadedAgentId = ''
  // Set once the draft has been persisted, so leaving Studio does not re-save it
  // as an in-progress session. Reset whenever a new draft starts.
  let sessionCommitted = false
  let runTrace = null         // { agentId, runId, startedAt, entries:[...] }
  let runDiagnosis = null     // { status, summary, rootCause, nextAction, suggestions, evidence }
  let runTraceErr = ''
  let runTraceLoading = false
  let runHistory = []         // [{runId, trigger, startedAt, ok, error, steps}] — every run
  let selectedRunId = ''      // which run's trace is shown ('' = latest)

  // Per-node mock editor state, keyed by node id: { text } (raw JSON the user
  // typed). Parsed lazily at run time; parse errors surface per-node via
  // mockErrors and the invalid mock is NOT sent.
  let mockText = {}           // { [nodeId]: string }
  let mockErrors = {}         // { [nodeId]: string }
  let showMocks = false       // collapsed by default to keep the panel tidy
  let showAssertions = true   // each bench sub-section folds independently

  // ── Workspace layout: collapse/resize the test panels + inspector ─────────
  // Lets the user reclaim space for the canvas (hide the bottom test/self-heal
  // section, hide the inspector) or widen the inspector to read code clearly.
  // Persisted across sessions so the chosen layout sticks.
  let showTests     = true
  let showInspector = true
  let inspectorWidth = 280     // px; clamped on resize
  let benchHeight   = 280      // px; height of the bottom workbench frame (drag-resizable)
  let maximizedFrame = ''      // '' | 'canvas' | 'bench' | 'inspector' — one frame fills the editor
  try {
    if (typeof localStorage !== 'undefined') {
      const p = JSON.parse(localStorage.getItem('studio.layout') || '{}')
      if (typeof p.showTests === 'boolean') showTests = p.showTests
      if (typeof p.showInspector === 'boolean') showInspector = p.showInspector
      if (typeof p.inspectorWidth === 'number') inspectorWidth = p.inspectorWidth
      if (typeof p.benchHeight === 'number') benchHeight = p.benchHeight
    }
  } catch (_) { /* ignore malformed/blocked storage */ }
  function persistLayout() {
    try {
      if (typeof localStorage !== 'undefined') {
        localStorage.setItem('studio.layout', JSON.stringify({ showTests, showInspector, inspectorWidth, benchHeight }))
      }
    } catch (_) { /* ignore */ }
  }
  // Maximize one frame to fill the editor (toggle off to restore the split view).
  function toggleMax(frame) { maximizedFrame = maximizedFrame === frame ? '' : frame }
  function toggleTests() { showTests = !showTests; persistLayout() }
  function toggleInspector() { showInspector = !showInspector; persistLayout() }
  function openWorkflowSettings() {
    selectedNode = null
    selectedEdge = null
    showInspector = true
    maximizedFrame = ''
    persistLayout()
  }

  // ── Canvas ⇄ Code (SOUL.yaml) view ────────────────────────────────────────
  // Code view is authoritative: switching to it serializes the current draft to
  // SOUL.yaml; switching back parses the (possibly edited) YAML into a draft and
  // warns about anything the canvas can't show; Save in code view writes the
  // YAML straight to disk.
  let viewMode = 'canvas'     // 'canvas' | 'code'
  let codeYaml = ''
  let codeOrig = ''           // last-synced text (dirty detection)
  // Tracks the agent id whose RAW on-disk SOUL.yaml we've loaded into the code
  // editor, so we read the real file once per loaded reasoning agent instead of
  // re-deriving (lossily) from the draft. Reset implicitly when loadedAgentId
  // changes (the guard compares the two).
  let codeRawForId = ''
  let codeWarnings = []
  let codeError = ''
  let codeLoading = false
  let codeValidation = null    // { ok, errors, warnings, items[], fixes[] }
  let codeValidating = false
  let codeFixing = false       // AI fix in progress
  let reviewing = false        // rules-grounded AI review in progress

  async function showCodeView() {
    if (viewMode === 'code' || !workflow) return
    codeError = ''
    codeLoading = true
    viewMode = 'code'
    try {
      const isAgent = ['react', 'plan_execute'].includes(String(workflow.strategy || '').toLowerCase())
      const dirty = codeYaml !== codeOrig
      let yamlText = ''
      // Source of truth for a SAVED reasoning agent is its on-disk SOUL.yaml, not
      // the draft (which can't model every field — run_timeout, memory scopes,
      // confirm_tools, …). Read the real file once per loaded agent, as long as
      // there are no unsaved code edits to preserve.
      // draftEdited() is the missing half of `dirty`: without it, edits made in
      // the AGENT PANEL were overwritten by the on-disk file the moment the user
      // looked at the code view.
      if (loadedAgentId && isAgent && !dirty && !draftEdited() && codeRawForId !== loadedAgentId) {
        try {
          const raw = await bridge.agentYaml(loadedAgentId)
          yamlText = (raw && raw.yaml) || ''
          if (yamlText) codeRawForId = loadedAgentId
        } catch (_) { /* fall through to the draft-derived YAML below */ }
      }
      if (!yamlText) {
        const r = await bridge.toYaml(workflow)
        yamlText = (r && r.yaml) || ''
      }
      codeYaml = normalizeYamlEditorText(yamlText)
      codeOrig = codeYaml
    } catch (e) {
      codeError = (e && e.message) || 'Could not generate YAML'
    }
    codeLoading = false
  }

  // Editing the YAML invalidates any prior validation report.
  $: { codeYaml; codeValidation = null }

  async function showCanvasView() {
    if (viewMode === 'canvas') return
    codeError = ''
    // Parse the authoritative YAML back into a draft for the canvas.
    if (codeYaml.trim()) {
      // codeLoading drives the view's "working" state; without it a large
      // SOUL.yaml made this tab look dead for the length of the parse.
      codeLoading = true
      try {
        const r = await bridge.fromYaml(codeYaml)
        codeWarnings = (r && Array.isArray(r.warnings)) ? r.warnings : []
        if (r && r.workflow) setWorkflow(r.workflow, { keepPrompt: true })
      } catch (e) {
        codeError = (e && e.message) || 'YAML could not be parsed'
        return // stay in code view so the user can fix it
      } finally {
        codeLoading = false
      }
    }
    codeOrig = codeYaml
    viewMode = 'canvas'
  }

  // Simple "Plan" view (Guided Studio Builder): lanes + readiness cards. Reads
  // the same `workflow` model, so switching from the CANVAS preserves edits.
  // Clicking a card jumps to the canvas + Inspector for that block.
  //
  // Coming from CODE view it must reparse first, exactly as showCanvasView does.
  // Without that step, Code → Plan → Code silently discarded unsaved SOUL.yaml
  // edits: the plan rendered a stale draft, and returning to code re-serialized
  // that stale draft over the user's text.
  async function showPlanView() {
    if (!workflow || viewMode === 'plan') return
    if (viewMode === 'code' && codeYaml.trim()) {
      codeError = ''
      codeLoading = true
      try {
        const r = await bridge.fromYaml(codeYaml)
        codeWarnings = (r && Array.isArray(r.warnings)) ? r.warnings : []
        if (r && r.workflow) setWorkflow(r.workflow, { keepPrompt: true })
      } catch (e) {
        codeError = (e && e.message) || 'YAML could not be parsed'
        return // stay in code view so the user can fix it
      } finally {
        codeLoading = false
      }
      codeOrig = codeYaml
    }
    viewMode = 'plan'
  }
  function planSelectNode(node) {
    selectedNode = node
    selectedEdge = null
    viewMode = 'canvas'
  }

  // Patch a single node's config from the Plan view's Configuration Card
  // (Story 9). Replaces the node in flow.nodes and re-validates.
  function updateNodeConfig(updated) {
    if (!workflow || !updated || !updated.id) return
    const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
    const nodes = (flow.nodes || []).map((n) => (n.id === updated.id ? updated : n))
    workflow = { ...workflow, flow: { ...flow, nodes } }
    if (selectedNode && selectedNode.id === updated.id) selectedNode = updated
    rebuildGraph()
    scheduleValidate()
  }

  // Insert a suggested Python step (Stories 3 & 4) right after the node it was
  // suggested for, pre-seeded from the matching template and auto-connected.
  // addStepFromText: describe a step in plain English; the backend compiles a
  // single block (recommending tool/python/agent) and appends it to the flow.
  let addStepBusy = false
  let addStepMsg = ''
  async function addStepFromText(instruction) {
    if (!workflow || !instruction || !instruction.trim() || addStepBusy) return
    addStepBusy = true
    addStepMsg = ''
    try {
      const res = await api.studio.addStep(workflow, instruction.trim())
      if (res && res.workflow) {
        workflow = res.workflow
        rebuildGraph()
        scheduleValidate()
        addStepMsg = `Added a ${res.recommended || 'step'}${res.step_summary ? `: ${res.step_summary}` : ''}.`
      }
    } catch (e) {
      addStepMsg = (e && e.message) || 'Could not add that step.'
    } finally {
      addStepBusy = false
    }
  }

  function addSuggestedPython(s) {
    if (!workflow || !s) return
    const flow = workflow.flow || { nodes: [], edges: [], entry: '' }
    const baked = bakePositions(flow.nodes)
    const anchor = baked.find((n) => n.id === s.nodeId)
    const id = uniqueNodeId('python')
    const node = {
      id, kind: 'python',
      code: pythonCodeFor(s.template), description: s.label,
      input: '', output: '', inputs: [], outputs: [], params: {},
      x: Math.round((anchor?.x || 0) + 220), y: Math.round(anchor?.y || 0),
    }
    // rewire anchor → python → (whatever anchor pointed at)
    let edges = flow.edges || []
    const downstream = edges.filter((e) => e && e.from === s.nodeId)
    edges = edges.filter((e) => !(e && e.from === s.nodeId))
    edges = [...edges, { from: s.nodeId, to: id }, ...downstream.map((e) => ({ ...e, from: id }))]
    workflow = { ...workflow, flow: { ...flow, nodes: [...baked, node], edges } }
    selectedNode = node
    rebuildGraph()
    scheduleValidate()
  }

  // Full validation of the edited YAML: syntax + definition + graph + runtime
  // (missing capabilities, unfilled args, template-reference bugs, …).
  async function validateCode() {
    if (codeValidating) return
    codeValidating = true
    codeError = ''
    try {
      codeValidation = await bridge.validateYaml(codeYaml)
    } catch (e) {
      codeError = (e && e.message) || 'Validation failed'
      codeValidation = null
    }
    codeValidating = false
  }

  // Rules-grounded AI review: ask the model to check the YAML against the
  // rulebook for judgment-call problems the deterministic validator can't catch,
  // and merge its findings into the validation panel.
  async function reviewWithAI() {
    if (reviewing) return
    if (!ensureCloudOk(reviewWithAI)) return
    reviewing = true
    codeError = ''
    try {
      const r = await bridge.reviewYaml(codeYaml)
      const aiItems = (r && Array.isArray(r.items)) ? r.items : []
      const base = codeValidation || { items: [], fixes: [] }
      const items = [...((base.items) || []).filter((i) => i.source !== 'ai'), ...aiItems]
      const errors = items.filter((i) => i.severity === 'error').length
      const warnings = items.filter((i) => i.severity === 'warning').length
      codeValidation = { ok: errors === 0, errors, warnings, items, fixes: (base.fixes) || [] }
    } catch (e) {
      codeError = (e && e.message) || 'AI review failed'
    }
    reviewing = false
  }

  // One-click fix: rewrite each flagged template reference to the suggested
  // scalar accessor (e.g. {{ .notebook.notebook }} -> {{ .notebook.notebook.id }})
  // across the whole YAML, then re-validate so the panel refreshes.
  async function applyTemplateFixes() {
    const fixes = (codeValidation && codeValidation.fixes) || []
    if (!fixes.length) return
    let text = codeYaml
    for (const f of fixes) {
      if (f && f.find) text = text.split(f.find).join(f.replace)
    }
    codeYaml = text
    await validateCode()
  }

  // Fix with AI: ask the framework model to rewrite the whole SOUL.yaml so every
  // issue is resolved (handles cases the deterministic quick-fix can't — picking
  // the right field, restructuring), then re-validate.
  async function fixWithAI() {
    if (codeFixing) return
    if (!ensureCloudOk(fixWithAI)) return
    codeFixing = true
    codeError = ''
    try {
      const r = await bridge.fixYaml(codeYaml)
      if (r && r.yaml) codeYaml = normalizeYamlEditorText(r.yaml)
      await validateCode()
    } catch (e) {
      codeError = (e && e.message) || 'AI fix failed — try again or fix manually'
    }
    codeFixing = false
  }

  // Save directly from the authoritative YAML (bypasses the draft round-trip so
  // fields the canvas can't express are preserved).
  //
  // It runs the SAME readiness gate the canvas Save runs. Previously it did not:
  // Plan and Canvas refused to save past blockers, while the SOUL.yaml tab wrote
  // straight to disk on the strength of server-side YAML validation alone — a
  // weaker, different check than AssessContract. A gate one tab can walk around
  // is not a gate, and the tab that bypassed it was the one an advanced user is
  // most likely to be in.
  //
  // The YAML stays authoritative for what gets WRITTEN; it is only parsed to a
  // draft so the contract has something to judge, so nothing the canvas cannot
  // express is lost.
  // The pre-save gate below is three network round-trips (parse + contract +
  // security review) BEFORE `saving` is set, and it can end by opening the
  // preflight dialog without ever saving. Without its own flag the button
  // stayed live and read "Save YAML" throughout, so the click looked ignored
  // and a second click re-ran the whole gate.
  let gateChecking = false
  async function saveFromCode(skipPreflight = false) {
    if (saving || gateChecking) return
    if (!skipPreflight) {
      let draftForGate = null
      gateChecking = true
      try {
        const parsed = await bridge.fromYaml(codeYaml)
        draftForGate = (parsed && parsed.workflow) || null
      } catch (_) {
        // Unparseable YAML cannot be gated here; the server's own validation
        // rejects it on save, which is the correct error to show.
      }
      if (draftForGate) {
        let report = null
        try { report = await runStudioContract(draftForGate) } catch (_) { report = null }
        if (report && ((report.blockers && report.blockers.length) || (report.warnings && report.warnings.length))) {
          preflight = report
          // Hold the judged document, so re-check / auto-fix / readiness all act
          // on the YAML rather than on the canvas draft, and so proceeding
          // resumes THIS save rather than the canvas one.
          gateDraft = draftForGate
          gateChecking = false
          return
        }
      }
      gateChecking = false
    }
    saving = true
    saveError = ''
    saveMsg = ''
    try {
      const res = await bridge.saveYaml(codeYaml, loadedAgentId)
      codeOrig = codeYaml
      const id = (res && res.id) || ''
      saveMsg = id ? `Saved ${id} — manage it from Deployed.` : 'Saved'
      // Mirror the canvas Save: hand off to Deployed to review/enable.
      if (id) { $editAgent = id; window.location.hash = '#agents' }
    } catch (e) {
      const v = e && e.body && e.body.validation
      if (v && Array.isArray(v.findings) && v.findings.length) {
        const f = v.findings.find((x) => x.severity === 'error') || v.findings[0]
        saveError = (f.field ? f.field + ': ' : '') + f.message
      } else {
        saveError = (e && e.message) || 'Save failed'
      }
    }
    saving = false
  }
  // Drag the splitter on the inspector's left edge to resize it. Width is taken
  // from the viewport's right edge (the inspector is the rightmost column).
  function startInspResize(e) {
    const onMove = (ev) => {
      inspectorWidth = Math.max(240, Math.min(760, window.innerWidth - ev.clientX))
    }
    const onUp = () => {
      persistLayout()
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    e.preventDefault()
  }

  // Drag the splitter above the bottom workbench to resize its height. Height is
  // taken from the editor's bottom edge upward (the workbench is the bottom frame).
  function startBenchResize(e) {
    const main = e.currentTarget.closest('main')
    const bottom = main ? main.getBoundingClientRect().bottom : window.innerHeight
    const onMove = (ev) => {
      benchHeight = Math.max(120, Math.min(bottom - 180, bottom - ev.clientY))
    }
    const onUp = () => {
      persistLayout()
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    e.preventDefault()
  }

  // Assertions editor: rows of { target, op, value }. target is a node id or
  // the literal "result"; op ∈ contains|equals|exists.
  let assertions = []         // [{ target, op, value }]
  // Named values a test run seeds beyond the trigger, plus an optional entry
  // point so a long pipeline can be iterated from the step that is broken
  // instead of re-running everything upstream of it (ST-10).
  let testVariables = {}      // { [name]: value }
  let testEnvironment = {}    // { [name]: value }
  let testStartNode = ''      // '' = the flow's own entry

  // In-memory run history (last ~10). Each entry keeps the full request +
  // response so it can be re-viewed or replayed exactly. Session-only.
  const HISTORY_MAX = 10
  let history = []            // [{ ts, inputSummary, passed, assertionCount, request, response }]
  let activeHistoryId = null  // ts of the entry currently shown (null = latest live run)

  // Draft nodes available as mock / assertion targets. Derived from the
  // in-memory workflow so edits keep them in sync.
  $: draftNodes = (workflow && workflow.flow && Array.isArray(workflow.flow.nodes))
    ? workflow.flow.nodes
    : []
  // Targets for an assertion dropdown: every node id plus the literal "result".
  $: assertionTargets = [...draftNodes.map((n) => n.id), 'result']

  // ── Mocks ─────────────────────────────────────────────────────────────────
  function setMockText(nodeId, value) {
    mockText = { ...mockText, [nodeId]: value }
    // Re-validate this one as the user types; clear stale errors when emptied.
    const trimmed = (value || '').trim()
    if (!trimmed) {
      const { [nodeId]: _drop, ...rest } = mockErrors
      mockErrors = rest
      persistBench()
      return
    }
    try {
      JSON.parse(trimmed)
      const { [nodeId]: _drop, ...rest } = mockErrors
      mockErrors = rest
      // Only a parseable mock is persisted. Half-typed JSON stays in the editor
      // (so a keystroke never destroys what you were writing) but must not reach
      // the draft, where it would fail to load somewhere far from the typo.
      persistBench()
    } catch (e) {
      mockErrors = { ...mockErrors, [nodeId]: 'Invalid JSON: ' + (e.message || 'parse error') }
    }
  }

  // Collect non-empty, VALID mocks into { <nodeId>: <parsedOutput> }. Invalid
  // JSON is skipped (and flagged) so we never send a malformed override.
  function collectMocks() {
    const out = {}
    for (const n of draftNodes) {
      const raw = (mockText[n.id] || '').trim()
      if (!raw) continue
      try {
        out[n.id] = JSON.parse(raw)
      } catch (_) { /* skipped — flagged in mockErrors */ }
    }
    return out
  }

  function hasMockErrors() {
    return Object.keys(mockErrors).length > 0
  }

  // ── Assertions ──────────────────────────────────────────────────────────
  function addAssertion() {
    const target = assertionTargets[assertionTargets.length - 1] || 'result'
    assertions = [...assertions, { target, op: 'contains', value: '' }]
    persistBench()
  }

  function removeAssertion(i) {
    assertions = assertions.filter((_, idx) => idx !== i)
    persistBench()
  }

  function updateAssertion(i, patch) {
    assertions = assertions.map((a, idx) => (idx === i ? { ...a, ...patch } : a))
    persistBench()
  }

  // ── Test variables / environment / start node ─────────────────────────────
  function setTestVariable(name, value) {
    testVariables = { ...testVariables, [name]: value }
    persistBench()
  }
  function removeTestVariable(name) {
    const { [name]: _drop, ...rest } = testVariables
    testVariables = rest
    persistBench()
  }
  function setTestEnvironment(name, value) {
    testEnvironment = { ...testEnvironment, [name]: value }
    persistBench()
  }
  function removeTestEnvironment(name) {
    const { [name]: _drop, ...rest } = testEnvironment
    testEnvironment = rest
    persistBench()
  }
  function setTestStartNode(nodeId) {
    testStartNode = nodeId || ''
    persistBench()
  }

  // Build the assertion payload — drop empty rows; "exists" needs no value.
  function collectAssertions() {
    return assertions
      .filter((a) => a && a.target && a.op)
      .map((a) => ({
        target: a.target,
        op: a.op,
        value: a.op === 'exists' ? undefined : a.value,
      }))
  }

  // ── Run ───────────────────────────────────────────────────────────────────
  function inputSummary(input) {
    const s = (input == null ? '' : String(input))
    return s.length > 48 ? s.slice(0, 48) + '…' : s
  }

  function pushHistory(request, response) {
    const ts = Date.now()
    const entry = {
      ts,
      inputSummary: inputSummary(request.input),
      passed: response && response.passed === true,
      assertionCount: (request.assertions && request.assertions.length) || 0,
      request,
      response,
    }
    history = [entry, ...history].slice(0, HISTORY_MAX)
    activeHistoryId = ts
    return entry
  }

  // Reveal the bench before a run writes into it. The test result renders
  // inside the `showTests` block, so with tests hidden (Build step hides them)
  // pressing Test produced a result nobody could see — the button looked dead.
  function revealBench() {
    if (showTests) return
    showTests = true
    persistLayout()
  }

  async function runTest() {
    if (!workflow || testing) return
    revealBench()
    testing = true
    testError = ''
    testResult = null
    if (hasMockErrors()) {
      // Don't send invalid JSON — collectMocks already skips them, but warn so
      // the user knows a mock was ignored rather than silently dropped.
      testError = 'Some mocks have invalid JSON and were skipped. Fix them or clear the field.'
    }
    // Snapshot the exact request so history/replay re-send it verbatim.
    const mocks = collectMocks()
    const asserts = collectAssertions()
    const request = {
      workflow,
      input: sampleInput,
      mode: testMode,
      ...(Object.keys(mocks).length ? { mocks } : {}),
      ...(asserts.length ? { assertions: asserts } : {}),
      // ST-10 inputs. Captured into the request snapshot too, so replaying a
      // history entry re-runs the SAME conditions — a replay that silently used
      // today's variables would not be a replay.
      ...(Object.keys(testVariables).length ? { variables: testVariables } : {}),
      ...(Object.keys(testEnvironment).length ? { environment: testEnvironment } : {}),
      ...(testStartNode ? { startNode: testStartNode } : {}),
    }
    try {
      const res = await bridge.test(request.workflow, request.input, {
        mocks: request.mocks,
        assertions: request.assertions,
        mode: request.mode,
        variables: request.variables,
        environment: request.environment,
        startNode: request.startNode,
      })
      testResult = res || null
      pushHistory(request, testResult)
    } catch (e) {
      testError = e.message || 'test failed'
    } finally {
      testing = false
    }
  }

  // Preview what a single run will do (Story #13): a one-click dry run through
  // the test bench, framed for the user as "here's what each run does." For a
  // scheduled agent there's no inbound message, so we send an empty input. No
  // mocks/assertions — just the per-node trace, which renders in the bench below.
  let previewing = false
  async function previewRun() {
    if (!workflow || previewing || testing) return
    revealBench()
    previewing = true
    testError = ''
    testResult = null
    try {
      const input = (workflow.trigger && /^(schedule|cron|webhook)$/i.test(workflow.trigger.type)) ? '' : sampleInput
      testResult = await bridge.test(workflow, input, { mode: 'dry' }) || null
    } catch (e) {
      testError = e.message || 'preview failed'
    } finally {
      previewing = false
    }
  }

  // "Fix with AI": feed a runtime/test error to the LLM, which rewrites the
  // draft so it won't recur, then applies + re-validates. Works for both the
  // test-bench error and a pasted runtime error.
  let troubleshooting = false
  function liveRunEvidence(result = tryResult) {
    const rows = []
    for (const t of (result && result.trace) || []) {
      const bits = [`step/tool: ${t.name || t.nodeId || '(unknown)'}`]
      if (t.detail) bits.push(`detail: ${t.detail}`)
      if (t.args) bits.push(`args: ${t.args}`)
      if (t.result) bits.push(`result: ${t.result}`)
      if (t.error) bits.push('status: error')
      rows.push('- ' + bits.join(' | '))
    }
    return rows.join('\n')
  }

  async function troubleshoot(errText, opts = {}) {
    const err = (errText || testError || '').trim()
    if (!workflow || !err || troubleshooting) return
    if (!ensureCloudOk(() => troubleshoot(errText, opts))) return
    troubleshooting = true
    try {
      const res = await bridge.troubleshoot(workflow, err, opts)
      if (res && res.workflow) {
        setWorkflow(res.workflow, { name: workflow.name, keepPrompt: true })
        toast(res.changed ? 'Applied an AI fix for that error — re-test to confirm.' : 'AI could not find an automatic fix for that error.')
        if (res.preflight && ((res.preflight.blockers || []).length || (res.preflight.warnings || []).length)) {
          preflight = res.preflight
        }
      }
    } catch (e) {
      testError = e.message || 'troubleshoot failed'
    } finally {
      troubleshooting = false
    }
  }

  function troubleshootLiveRun() {
    if (!tryResult || !tryResult.error) return
    troubleshoot(tryResult.error, {
      input: sampleInput,
      evidence: liveRunEvidence(tryResult),
    })
  }

  // Re-send a history entry's EXACT request (workflow snapshot + input + mocks
  // + assertions + mode) and prepend the fresh result. Non-blocking on error.
  async function replayHistory(entry) {
    if (!entry || !entry.request || testing) return
    testing = true
    testError = ''
    const request = entry.request
    try {
      // Forward EVERY captured condition. Replaying with only mocks/assertions
      // silently re-ran from the default start node with none of the variables
      // the original run used — and then filed that result under the original
      // request, which could not have produced it.
      const res = await bridge.test(request.workflow, request.input, {
        mocks: request.mocks,
        assertions: request.assertions,
        mode: request.mode,
        variables: request.variables,
        environment: request.environment,
        startNode: request.startNode,
      })
      testResult = res || null
      pushHistory(request, testResult)
    } catch (e) {
      testError = e.message || 'replay failed'
    } finally {
      testing = false
    }
  }

  // View a stored run again without re-sending it.
  function viewHistory(entry) {
    if (!entry) return
    testResult = entry.response
    testError = ''
    activeHistoryId = entry.ts
  }

  function clearHistory() {
    history = []
    activeHistoryId = null
  }

  // ── Plan + Save (M2) ──────────────────────────────────────────────────────
  let saving = false
  let saveError = ''
  let saveMsg = ''
  let plan = null              // last plan result { tier, reasons, requiresConsent, consentItems }
  let consent = null          // { items:[{kind,name,reason}] } when the dialog is open
  // Pre-save validation report when the dialog is open: { ok, blockers[], warnings[] }.
  // Blockers must be fixed before saving; warnings can be acknowledged and saved over.
  let preflight = null

  // F-GUI-3 — Cohort F S6 security review. Re-runs on debounced draft change
  // so the panel stays live while the operator edits the workflow, and its
  // blockers merge into the same pre-save preflight gate that Contract already
  // uses (so blockers block Save the same way).
  //
  //   securityReview  — last successful /studio/security_review response
  //   securityLoading — in-flight indicator for the panel
  //   securityError   — surface API failures in the panel without blocking Save
  //   securityRunToken — increments each debounce so stale replies don't win
  let securityReview   = null
  let securityLoading  = false
  let securityError    = ''
  let securityDebounce = null
  let securityRunToken = 0
  let securityPanelOpen = true

  // Save click: VALIDATE first (consolidated preflight), then PLAN, then either
  // save directly or raise the consent dialog. Every bridge op degrades
  // gracefully — a bridge/host error just surfaces as saveError. The optional
  // `skipPreflight` flag is set when the user clicked "Save anyway" past a
  // warnings-only report, so we don't re-gate in a loop.
  async function save(skipPreflight = false) {
    if (!workflow || saving || consent || (preflight && !skipPreflight)) return
    saving = true
    saveError = ''
    saveMsg = ''
    commitGraphPositions()   // keep drag-repositioned layout in the saved draft
    try {
      if (!skipPreflight) {
        let report = null
        try { report = await runStudioContract(workflow) } catch (_) { report = null }
        // Open the dialog when there's anything to show. Blockers stop the save;
        // warnings let the user proceed with "Save anyway". A failed/empty
        // contract check (host error) is non-blocking — fall through to plan/save.
        if (report && ((report.blockers && report.blockers.length) || (report.warnings && report.warnings.length))) {
          preflight = report
          return    // wait for the user to fix blockers or acknowledge warnings
        }
      }
      const p = await bridge.plan(workflow)
      plan = p || null
      if (p && p.requiresConsent) {
        openConsent(p.consentItems)
        return        // wait for the operator's acknowledgement
      }
      await doSave(false, [])
    } catch (e) {
      saveError = e.message || 'plan failed'
    } finally {
      saving = false
    }
  }

  // From the preflight dialog: proceed to save despite warnings (only reachable
  // when there are no blockers).
  // Explicit warning acknowledgement. Warnings may be overridden only after
  // the operator answers Yes; No keeps the save gated and leaves the repair
  // actions available. The affirmative decision is recorded with the save.
  let acceptWarnings = ''
  $: preflightHasBlockers = !!(preflight && preflight.blockers && preflight.blockers.length)
  $: preflightHasWarnings = !!(preflight && preflight.warnings && preflight.warnings.length)
  $: canProceedPastPreflight = !preflightHasBlockers && (!preflightHasWarnings || acceptWarnings === 'yes')

  // ── Wizard IA: Describe › Build › Test › Save ─────────────────────────────
  // The step the user is ON. `viewMode` (plan / canvas / code) becomes
  // sub-navigation WITHIN Build rather than top-level navigation, which is what
  // made the order of operations invisible before.
  let step = STEP_DESCRIBE

  // Built by a function rather than inline so it can ALSO be computed on demand.
  // The reactive value is one tick behind a synchronous mutation, so code that
  // sets `workflow` and immediately asks "can I enter Build?" would read a
  // context that still says there is no draft — and the advance would silently
  // do nothing.
  function buildWizardCtx(intent, rawPrompt, workflow, testResult, testError, readiness, loadedAgentId) {
    return {
      intent: (intent || rawPrompt || '').trim(),
      hasNodes: !!(workflow && workflow.flow && (workflow.flow.nodes || []).length),
      isAgent: !!(workflow && workflow.strategy),
      tested: !!testResult,
      testPassed: !!(testResult && testResult.passed !== false && !testError),
      readiness,
      saved: !!loadedAgentId,
    }
  }
  // Fresh context from whatever the values are RIGHT NOW.
  function nowCtx() {
    return buildWizardCtx(intent, rawPrompt, workflow, testResult, testError, readiness, loadedAgentId)
  }
  $: wizardCtx = buildWizardCtx(intent, rawPrompt, workflow, testResult, testError, readiness, loadedAgentId)
  $: wizardSteps = stepStates(step, wizardCtx)
  $: saveBlocked = saveBlockedReason(wizardCtx)

  // Layout that belongs to a step. Build owns the graph, Test owns the bench:
  // collapsing the bench on Build gives the canvas the room the design shows,
  // and expanding it on Test means "Test" does not land the user on a canvas
  // with a strip at the bottom they then have to go find.
  // `mode` is passed in rather than read from the reactive `currentMode`.
  // Callers reach here synchronously right after assigning `workflow` (via
  // setWorkflow → advanceToStep, and via applyCompile), and a `$:` value has not
  // recomputed at that point — so this read described the PREVIOUS draft and
  // opened an agent in the workflow layout, or vice versa. Same class of bug
  // that nowCtx() was introduced to fix a few lines below.
  function applyStepLayout(id, mode) {
    // Same derivation as the `currentMode` reactive value, computed from the
    // draft as it is RIGHT NOW rather than as of the last render.
    const m = mode || modeOf(workflow)
    if (id === STEP_BUILD && viewMode !== 'canvas' && viewMode !== 'code' && viewMode !== 'plan') {
      viewMode = m === 'workflow' ? 'plan' : 'canvas'
    }
    // A flowless agent has no Plan tab (the tab is rendered only for workflows),
    // so leaving viewMode on 'plan' from a previous workflow draft strands the
    // user on a view whose tab is not on screen.
    if (id === STEP_BUILD && m !== 'workflow' && viewMode === 'plan') {
      viewMode = 'canvas'
    }
    if (id === STEP_BUILD) { showTests = false; maximizedFrame = '' }
    if (id === STEP_TEST) { showTests = true; maximizedFrame = 'bench' }
    // The Save step's whole question is "ready?", and its panel used to open
    // reading "Nothing checked yet" — syncReadiness only fires for the preflight
    // DIALOG, so arriving at the step itself never ran a check.
    if (id === STEP_SAVE && workflow) loadReadiness()
    persistLayout()
  }

  // User-driven navigation.
  function goStep(id) {
    if (!canEnter(id, wizardCtx).ok) return
    step = id
    applyStepLayout(id)
  }

  // A generated draft is not a one-way door. Return to the exact prompt pair
  // that produced it, keep the current draft in place for comparison, and put
  // the cursor where the user can revise it. Generate already owns the explicit
  // replacement confirmation, so no work is discarded by this navigation.
  function editPromptForRegeneration() {
    goStep(STEP_DESCRIBE)
    setTimeout(() => {
      const id = String(rawPrompt || '').trim() ? 'd-raw' : 'd-refined'
      const editor = document.getElementById(id)
      if (editor) editor.focus()
    }, 0)
  }

  // Programmatic navigation, for MILESTONES only — a generate finishing, a draft
  // being opened, a new workflow being started.
  //
  // This was previously a reactive statement over wizardCtx, which was wrong:
  // `isDone(describe)` is true the moment `intent` is non-empty, so the FIRST
  // KEYSTROKE in the prompt box made Describe "done" and threw the user into
  // Build mid-sentence. Advancing is an event, not a derived value — reacting to
  // state means reacting to every intermediate state the user types through.
  function advanceToStep(id) {
    if (id === step) return
    // nowCtx(), not wizardCtx: callers advance immediately after setting
    // `workflow`, and the reactive value has not caught up yet.
    if (!canEnter(id, nowCtx()).ok) return
    step = id
    applyStepLayout(id)
  }

  // ── Server-side readiness (ST-07) ─────────────────────────────────────────
  // One verdict instead of three stitched calls. Rendered alongside the legacy
  // preflight lists during the transition; the legacy lists should be removed
  // once this is verified against a live server.
  let readiness = null
  let readinessLoading = false
  let readinessError = ''
  let readinessToken = 0

  // Fetch once per opening of the readiness dialog. Keyed on the preflight
  // object identity rather than a boolean so re-opening after an edit re-checks,
  // while a re-render of the same dialog does not re-fetch on every tick.
  //
  // The bookkeeping lives in a plain function rather than in the reactive block:
  // a reactive statement that both reads and writes the same variable is a
  // self-referential dependency and Svelte can reject it at compile time.
  let readinessFor = null
  $: syncReadiness(preflight)
  function syncReadiness(p) {
    if (p && readinessFor !== p) {
      readinessFor = p
      loadReadiness()
    } else if (!p && readinessFor) {
      readinessFor = null
      readiness = null
      readinessError = ''
    }
  }

  async function loadReadiness() {
    // Readiness is shown inside the preflight dialog, so it must describe the
    // document that dialog is judging — otherwise the YAML view reports the
    // canvas draft's readiness under the YAML's blockers.
    const subject = gateSubject()
    if (!subject) { readiness = null; return }
    const token = ++readinessToken
    readinessLoading = true
    readinessError = ''
    try {
      const res = await bridge.readiness(subject, false)
      if (token !== readinessToken) return
      readiness = res || null
    } catch (e) {
      if (token !== readinessToken) return
      // A failed readiness call must never read as "ready". Clear the report so
      // nothing stale is shown next to a fresh error — the old client-side
      // stitch's exact bug was letting a failed section quietly disappear.
      readiness = null
      readinessError = (e && e.message) || 'Could not check readiness.'
    } finally {
      if (token === readinessToken) readinessLoading = false
    }
  }

  // Deep-link a readiness item to the screen that fixes it. Anything that edits
  // THIS draft keeps the user inside Studio — bouncing them to another page and
  // losing an unsaved draft would be a worse outcome than the warning.
  // Every case here must be an action the SERVER actually emits. The previous
  // list (open_channels / open_secrets / add_to_confirm_tools / review_consent /
  // set_intent_gate) matched nothing in the Go tree, while the six actions the
  // server does emit besides open_providers/open_mcp fell through to `default`
  // — so a blocker rendered a "Fix this" button that did nothing at all.
  function readinessAction(item) {
    return applyFixAction(item)
  }

  // THE dispatcher for every finding, everywhere: preflight blockers, contract
  // checks, security findings, readiness items. One function so a given action
  // behaves identically no matter which panel offered it, and so there is
  // exactly one place that can fall out of step with the Go vocabulary —
  // fixactions.test.js watches that one place.
  function applyFixAction(item) {
    const action = item && item.action
    if (!action) return
    switch (actionKind(action)) {
      case 'navigate':
        window.location.hash = action === 'open_providers'
          ? providerSetupTarget(NAVIGATE_TARGETS[action], item, workflow)
          : NAVIGATE_TARGETS[action]
        return

      case 'apply': {
        // Studio owns the value, so just set it. The module is pure and returns
        // a new draft; an unchanged draft means there was nothing to do, which
        // we say out loud rather than flashing a success.
        const res = applyDraftFix(workflow, action, item.actionParams || item.action_params)
        if (res.draft) workflow = res.draft
        toast(res.message)
        // The fix changed the draft, so every verdict on screen is now stale.
        if (res.draft) { refreshSecurityReview(); if (readiness) loadReadiness() }
        return
      }

      case 'focus':
        switch (action) {
          case 'choose_model':
            closePreflight()
            openAgentModelPicker()
            return
          case 'rename_agent':
            // The collision is fixed by typing a different name, so put the
            // user in front of that field with it selected. Sending them to the
            // canvas — the default below — would land them nowhere near it.
            closePreflight()
            viewMode = 'canvas'
            goStep(STEP_SAVE)
            setTimeout(() => {
              const el = document.querySelector('[data-agent-name-field]')
              if (el) { el.focus(); el.select() }
            }, 0)
            return
          case 'add_assertions':
          case 'run_live':
            // Both are fixed at the bench: add an assertion, or exercise it live.
            closePreflight()
            viewMode = 'canvas'
            revealBench()
            goStep(STEP_TEST)
            return
          default:
            // reveal_node / open_studio / open_preflight — everything repaired
            // on the canvas.
            closePreflight()
            viewMode = 'canvas'
            goStep(STEP_BUILD)
            if (item && item.nodeId) revealNode(item.nodeId)
            return
        }

      default:
        // An id the client does not know. The server should never send one —
        // finishItem drops unknown ids before they reach us — so say so instead
        // of rendering a button that quietly does nothing.
        toast(`Studio does not know how to apply "${action}" — follow the written fix.`)
    }
  }

  // The draft the preflight dialog is currently judging.
  //
  // null means "the canvas draft" (`workflow`). Non-null means the dialog was
  // raised by the SOUL.yaml save and is judging the parsed YAML instead, so
  // proceeding must resume THAT save — writing the canvas draft would silently
  // discard whatever the YAML expressed that the canvas cannot.
  //
  // This used to be a bare `pendingCodeSave` boolean that only cancelPreflight
  // reset, so any other way of dismissing the dialog left it stuck true and the
  // next canvas Save wrote stale YAML over the user's canvas edits. Holding the
  // subject itself (rather than a flag about it) also lets re-check, auto-fix
  // and readiness act on the document that was actually judged — previously
  // they all operated on `workflow` regardless, so in the YAML view "Re-check"
  // could clear blockers that were never in the YAML.
  let gateDraft = null
  $: pendingCodeSave = gateDraft !== null

  // The draft under review, whichever view raised the dialog.
  function gateSubject() {
    return gateDraft || workflow
  }

  // Missing delivery is resolved where it is reported. Offer only configured
  // channels that are not already on the draft; selecting one updates the
  // exact document under review (canvas or authoritative YAML) and immediately
  // re-runs readiness instead of navigating away from the unsaved agent.
  $: readinessDestinationOptions = channelOptions.filter((ch) => {
    const subject = gateDraft || workflow
    const selected = (subject && Array.isArray(subject.channels)) ? subject.channels : []
    return ch && ch.id && !selected.includes(ch.id)
  })
  function chooseReadinessDestination(channelId) {
    const subject = gateSubject()
    if (!subject || !channelId) return
    const patch = toggleChannelPatch(subject, channelId, true)
    if (gateDraft) {
      gateDraft = { ...subject, ...patch }
    } else {
      applyFraming(patch)
    }
    const choice = channelOptions.find((ch) => ch.id === channelId)
    toast(`Results will be delivered to ${(choice && choice.name) || channelId}. Re-checking readiness…`)
    loadReadiness()
  }

  // Single exit point, so a new dismissal path cannot forget to reset the
  // subject the way revealNode and readinessAction did.
  function closePreflight() {
    preflight = null
    gateDraft = null
  }

  function proceedAfterPreflight() {
    if (!preflight || !canProceedPastPreflight) return
    const wasCodeSave = gateDraft !== null
    closePreflight()
    if (wasCodeSave) {
      saveFromCode(true)
      return
    }
    save(true)
  }

  function cancelPreflight() {
    if (saving) return
    closePreflight()
    acceptWarnings = ''
  }

  // Jump from a validation blocker/warning to the offending block: close the
  // dialog and select that node so it opens in the Inspector for editing.
  function revealNode(nodeId) {
    const wf = workflow && workflow.flow && Array.isArray(workflow.flow.nodes) ? workflow.flow.nodes : []
    const node = wf.find((n) => n && n.id === nodeId)
    closePreflight()
    if (node) {
      selectedNode = node
      selectedEdge = null
      // Highlight the node on the canvas (xyflow reads `selected` per node).
      try { nodes.update((arr) => arr.map((gn) => ({ ...gn, selected: gn.id === nodeId }))) } catch (_) {}
    }
  }

  // Re-run validation in place (e.g. after the user fixed something) without
  // closing the dialog.
  let rechecking = false
  async function rerunPreflight() {
    // Re-check the document that was judged, not whatever is on the canvas.
    const subject = gateSubject()
    if (!subject || rechecking) return
    rechecking = true
    acceptWarnings = ''
    try { preflight = await runStudioContract(subject) } catch (_) { /* keep current */ }
    finally { rechecking = false }
  }

  // "Fix automatically": run deterministic contract + data-flow repair on the
  // current draft, apply the result to the canvas, then re-check. This handles
  // structure blockers (empty/invalid/brittle graphs) as well as common
  // wiring/var-name mismatches without regenerating from scratch.
  let fixing = false
  let preflightFixMsg = ''
  let preflightFixKind = 'info'
  async function fixAutomatically() {
    const subject = gateSubject()
    if (!subject || fixing) return
    // Repairing the YAML draft must write back to the YAML. Applying the repair
    // to the canvas instead was doubly wrong: it rewrote a draft the user was
    // not looking at, and proceedAfterPreflight then saved the untouched
    // codeYaml, throwing the repair away.
    const repairingCode = gateDraft !== null
    fixing = true
    preflightFixMsg = 'Studio is checking whether these findings have a safe automatic repair...'
    preflightFixKind = 'info'
    try {
      const res = await bridge.autowire(subject)
      const fixedCount = Number(res && res.fixed ? res.fixed : 0)
      if (res && res.workflow && repairingCode) {
        gateDraft = res.workflow
        try {
          const y = await bridge.toYaml(res.workflow)
          if (y && y.yaml) codeYaml = normalizeYamlEditorText(y.yaml)
        } catch (_) {
          // Leave the editor untouched rather than showing YAML that does not
          // match the draft now being gated.
        }
        toast(fixedCount ? `Auto-fixed ${fixedCount} issue${fixedCount === 1 ? '' : 's'} in the YAML.` : 'No auto-fixable issues found. Re-checking the latest contract.')
      } else if (res && res.workflow) {
        // keepStep: this is an in-place repair during a review, not opening a
        // different draft — the user must stay where they were.
        const editingAgentId = loadedAgentId
        setWorkflow(res.workflow, { name: workflow.name, keepStep: true, keepPrompt: true })
        // setWorkflow clears the loaded-agent binding (correct when SWAPPING
        // drafts, wrong here): losing it would make the next Save create a
        // duplicate agent instead of updating the one being repaired.
        if (editingAgentId) loadedAgentId = editingAgentId
        toast(fixedCount ? `Auto-fixed ${fixedCount} issue${fixedCount === 1 ? '' : 's'}.` : 'No auto-fixable issues found. Re-checking the latest contract.')
      }
      const next = await runStudioContract((res && res.workflow) || subject)
      preflight = next
      const blockerCount = (next && next.blockers && next.blockers.length) || 0
      const warningCount = (next && next.warnings && next.warnings.length) || 0
      if (fixedCount > 0 && blockerCount === 0) {
        preflightFixKind = 'success'
        preflightFixMsg = `Studio applied ${fixedCount} automatic repair${fixedCount === 1 ? '' : 's'} and cleared all blockers.`
      } else if (fixedCount > 0) {
        preflightFixKind = 'warn'
        preflightFixMsg = `Studio applied ${fixedCount} automatic repair${fixedCount === 1 ? '' : 's'}, but ${blockerCount} blocker${blockerCount === 1 ? '' : 's'} still need attention.`
      } else if (blockerCount === 0 && warningCount > 0) {
        preflightFixKind = 'info'
        preflightFixMsg = 'No structural rewrite was needed. Only warnings remain, so you can review them and save anyway.'
      } else if (blockerCount === 0) {
        preflightFixKind = 'success'
        preflightFixMsg = 'No automatic repair was needed; the latest contract is clear.'
      } else {
        preflightFixKind = 'warn'
        // "No safe automatic rewrite" is only half an answer. Say WHY automatic
        // repair stops here and what the manual move is, because the previous
        // message left the user staring at a list with no next step.
        preflightFixMsg =
          'Studio only rewrites issues it can fix deterministically — wiring, templates and contract shape. ' +
          `${blockerCount} blocker${blockerCount === 1 ? '' : 's'} below ${blockerCount === 1 ? 'needs' : 'need'} a decision it cannot make for you ` +
          '(a missing credential, an unconfigured destination, or a step whose intent is ambiguous). ' +
          'Use “Fix this” on an item to jump straight to the setting, or “Show the step” to open it on the canvas.'
      }
    } catch (e) {
      saveError = e.message || 'auto-fix failed'
      preflightFixKind = 'error'
      preflightFixMsg = saveError
    } finally {
      fixing = false
    }
  }

  async function runStudioContract(draft) {
    // F-GUI-3 — pair the contract report with the security review so their
    // blockers/warnings land in the same preflight modal. Both requests fire
    // in parallel; a security failure never blocks the contract path.
    let contract = null
    let secReview = null
    await Promise.all([
      (async () => { try { contract = await bridge.contract(draft) } catch (_) { contract = null } })(),
      (async () => {
        try { secReview = await bridge.securityReview(draft) } catch (_) { secReview = null }
      })(),
    ])
    // Also refresh the always-visible panel state so operators aren't
    // surprised when Save opens the modal.
    if (secReview) securityReview = secReview

    const contractIssues = contractToPreflight(contract)

    const secIssues = (() => {
      if (!secReview) return { blockers: [], warnings: [] }
      const toIssue = (severity) => (f) => ({
        severity,
        kind: 'studio.security.' + (f.category || 'finding'),
        nodeId: '',
        message: `Security ${f.category || 'finding'}: ${f.message || ''}`.trim(),
        fix: f.fix || '',
      })
      return {
        blockers: (secReview.blockers || []).map(toIssue('blocker')),
        warnings: (secReview.warnings || []).map(toIssue('warning')),
      }
    })()

    if (!contract && !secReview) {
      // Fall back to legacy preflight for downstream compatibility.
      try { return await bridge.preflight(draft) } catch (_) { return null }
    }
    return {
      ok: (!contract || !!contract.ok) && (!secReview || !!secReview.ok),
      blockers: [...contractIssues.blockers, ...secIssues.blockers],
      warnings: [...contractIssues.warnings, ...secIssues.warnings],
      contract,
      security: secReview,
    }
  }

  function contractToPreflight(contract) {
    if (!contract) return { blockers: [], warnings: [] }
    const checks = Array.isArray(contract.checks) ? contract.checks : []
    const toIssue = (c) => ({
      severity: c.status === 'block' ? 'blocker' : 'warning',
      kind: c.id || 'studio.contract',
      nodeId: c.nodeId || '',
      message: `${c.title || 'Studio contract'}: ${c.message || ''}`.trim(),
      fix: c.fix || '',
    })
    return {
      blockers: checks.filter((c) => c && c.status === 'block').map(toIssue),
      warnings: checks.filter((c) => c && c.status === 'warn').map(toIssue),
    }
  }

  // F-GUI-3 — debounced always-on security review so the panel stays fresh
  // while the operator edits the graph. Keeps the pre-save gate honest without
  // exploding the request count. Only fires when we have a draft.
  function scheduleSecurityReview() {
    if (typeof window === 'undefined') return
    if (securityDebounce) clearTimeout(securityDebounce)
    securityDebounce = setTimeout(refreshSecurityReview, 700)
  }
  async function refreshSecurityReview() {
    if (!workflow) return
    const token = ++securityRunToken
    securityLoading = true
    securityError = ''
    try {
      const res = await bridge.securityReview(workflow)
      if (token !== securityRunToken) return  // stale
      securityReview = res || null
    } catch (e) {
      if (token !== securityRunToken) return
      securityError = e.message || 'security review failed'
    } finally {
      if (token === securityRunToken) securityLoading = false
    }
  }

  // F-GUI-3 — unambiguous rewrites for the top three risky tools. Any tool
  // node in the workflow whose `tool` (or nested tool ref) matches `from`
  // gets rewritten to `suggest`. When there's no exact match (custom tools,
  // MCP calls, etc.), we quietly skip.
  function applySecurityRecommendation(rec) {
    if (!rec || !rec.from || !rec.suggest || !workflow) return
    const from = String(rec.from)
    // The suggested replacement text for shell_exec / http_request in
    // security_preflight.go is a sentence, not a tool name; only auto-apply
    // when it looks like a bare tool identifier.
    const suggestId = /^[a-zA-Z0-9_.-]+$/.test(rec.suggest) ? rec.suggest : ''
    if (!suggestId) {
      toast(`Recommendation applied to notes — replace ${from} with ${rec.suggest} manually.`)
      return
    }
    const flow = workflow.flow || {}
    const nodes = Array.isArray(flow.nodes) ? flow.nodes : []
    let hits = 0
    const nextNodes = nodes.map((n) => {
      if (!n) return n
      const cfg = n.config || {}
      if (n.tool === from || cfg.tool === from) {
        hits++
        return {
          ...n,
          tool: n.tool === from ? suggestId : n.tool,
          config: cfg.tool === from ? { ...cfg, tool: suggestId } : cfg,
        }
      }
      return n
    })
    if (!hits) {
      toast(`No ${from} usage found to rewrite.`)
      return
    }
    setWorkflow({ ...workflow, flow: { ...flow, nodes: nextNodes } }, { name: workflow.name, keepPrompt: true })
    toast(`Rewrote ${hits} ${from} → ${suggestId}.`)
    scheduleSecurityReview()
  }

  // React to workflow updates via a reactive statement. `workflow` is
  // reassigned by many code paths (edits, autowire, build) so this covers all
  // of them without threading calls into each mutator.
  $: if (workflow) scheduleSecurityReview()

  // ── Architect: autonomous build-verify-repair ("Build until it works") ────
  // One click drives the whole loop server-side: fill capability holes with
  // generated glue code, synthesize self-tests, then repair every blocker AND
  // every runtime error — actually RUNNING the agent — until it works or the
  // budget is hit. The healed draft replaces the canvas and the transcript is
  // shown so the user sees exactly what was wrong and how each problem was fixed.
  let building = false
  let buildReport = null // { workflow, ok, verified, attempts[], summary, residual[] }
  let buildGlue = []
  let buildLog = [] // live progress lines while building: [{kind, message}]
  let buildTraceId = null // id of the last build's durable trace (build inspector)
  let showBuildInspector = false
  async function buildUntilWorks() {
    if (!workflow || building) return
    if (!ensureCloudOk(buildUntilWorks)) return
    building = true
    buildReport = null
    buildGlue = []
    buildLog = []
    saveError = ''
    try {
      const res = await bridge.buildStream(workflow, intent, true, (ev) => {
        if (!ev || !ev.message) return
        // Append a live line; glue notes also feed the report's glue list.
        buildLog = [...buildLog, ev]
        if (ev.kind === 'glue') buildGlue = [...buildGlue, ev.message.replace(/^🧩\s*/, '')]
      })
      if (res && res.report) {
        buildReport = res.report
        buildTraceId = res.traceId || null
        if (res.report.workflow) setWorkflow(res.report.workflow, { name: workflow.name, keepPrompt: true })
        preflight = res.preflight && !res.preflight.ok ? res.preflight : null
        toast(res.report.summary || 'Build complete.')
      }
    } catch (e) {
      saveError = e.message || 'build failed'
    } finally {
      building = false
    }
  }

  // ── Runtime self-heal: failed (incl. scheduled) runs ──────────────────────
  // The DLQ feed of runs that failed at run time. Diagnosing one loads the saved
  // agent, repairs it against the REAL error, re-verifies, and drops the healed
  // draft onto the canvas to review + Save. No copy-paste of errors anywhere.
  let showFailedRuns = false
  let failedRuns = []
  // An apply-repair response for the selected failed run. Null until a repair
  // has been proposed AND judged, so the review UI only appears once there is
  // something real to review.
  let loadingFailed = false
  let healing = '' // id currently being healed
  let healResult = null
  // Story 3 AC3 — heal produces a concrete patch with before/after diff, not a
  // silent canvas replace. `healDiff` holds the candidate + rendered diff and
  // waits for operator confirmation via applyHealDiff() before touching the
  // live workflow. Reuses .repair-diff CSS so the pane looks identical to the
  // per-node repair-diff renderer (both surfaces are "before/after this fix").
  let healDiff = null // { candidate, lines, stats, source, name }
  let healDiffBusy = false
  async function prepareHealDiff(res, source) {
    if (!res || !res.workflow) return
    healDiffBusy = true
    try {
      const before = workflow || { name: (res.workflow && res.workflow.name) || 'workflow', flow: { nodes: [], edges: [] } }
      const d = await api.studio.diff(before, res.workflow)
      healDiff = {
        candidate: res.workflow,
        lines: (d && d.lines) || [],
        stats: (d && d.stats) || { added: 0, removed: 0 },
        source,
        name: (res.workflow && res.workflow.name) || (workflow && workflow.name) || 'workflow',
      }
    } catch (_) {
      // Diff service failed — fall back to no-diff panel but still let operator
      // apply the healed workflow. Preserves the pre-AC3 behaviour minus the
      // silent replace.
      healDiff = {
        candidate: res.workflow,
        lines: [],
        stats: { added: 0, removed: 0 },
        source,
        name: (res.workflow && res.workflow.name) || (workflow && workflow.name) || 'workflow',
      }
    } finally {
      healDiffBusy = false
    }
  }
  function applyHealDiff() {
    if (!healDiff) return
    setWorkflow(healDiff.candidate, { name: healDiff.name, keepPrompt: true })
    const kind = healDiff.source === 'session' ? 'Debugged real run applied' : 'Heal applied'
    toast(kind + ' — review and Save to persist.')
    healDiff = null
  }
  function cancelHealDiff() { healDiff = null }
  async function loadFailedRuns() {
    loadingFailed = true
    try {
      const r = await bridge.failedRuns()
      failedRuns = (r && r.runs) || []
    } catch (_) {
      failedRuns = []
    } finally {
      loadingFailed = false
    }
  }
  async function toggleFailedRuns() {
    showFailedRuns = !showFailedRuns
    if (showFailedRuns) await loadFailedRuns()
  }
  // "Retry unchanged" has to run the agent unchanged.
  //
  // It was wired to healFailedRun — the same handler as "Propose a repair" —
  // so the button offered next to a TRANSIENT fault (a timeout, a 503) instead
  // diagnosed the agent and suggested editing it. Two buttons, two different
  // promises, one behaviour, and the one that lied was the one whose whole
  // point is that nothing needs changing.
  async function retryFailedRun(run) {
    const agentId = run && (run.agentId || run.agent_id)
    if (!agentId || healing) return
    healing = `retry:${agentId}`
    saveError = ''
    try {
      await api.agents.trigger(agentId)
      toast('Re-running — check Failed runs again in a moment to see whether it passed.')
    } catch (e) {
      // A 409 is the ordinary case when a schedule is mid-flight, not a fault.
      saveError = (e && e.message) || 'could not re-run this agent'
    } finally {
      healing = ''
    }
  }

  async function healFailedRun(id) {
    if (healing) return
    healing = id
    healResult = null
    healDiff = null
    saveError = ''
    try {
      const res = await bridge.diagnoseRun(id)
      healResult = res
      if (res && res.workflow) {
        await prepareHealDiff(res, 'run')
        toast(res.changed ? 'Healed — preview the change below and click Apply this fix to load it.' : 'No fix was suggested for this error.')
      }
    } catch (e) {
      saveError = e.message || 'diagnose failed'
    } finally {
      healing = ''
    }
  }

  async function healActivityRun(agentId, sessionId) {
    if (healing || !agentId || !sessionId) return
    healing = `session:${agentId}:${sessionId}`
    healResult = null
    healDiff = null
    saveError = ''
    try {
      const res = await bridge.diagnoseSession(agentId, sessionId)
      healResult = res
      loadedAgentId = agentId
      // Story 3 AC2b — prefill the Test bench sample input with the original
      // failing user input so operators re-test the exact case that broke,
      // not the literal 'hello' default. Empty string skips (keeps whatever
      // the operator already had typed).
      if (res && typeof res.failing_input === 'string' && res.failing_input.trim() !== '') {
        sampleInput = res.failing_input
      }
      if (res && res.workflow) {
        await prepareHealDiff(res, 'session')
        toast(res.changed ? 'Debugged real run — preview the change below and click Apply this fix to load it.' : 'No fix was suggested for this run.')
      }
    } catch (e) {
      saveError = e.message || 'debug failed'
    } finally {
      healing = ''
    }
  }

  // Persist the draft. acceptPrivilegedExposure threads the operator's
  // channel-consent; grants threads the per-node code consent (§13). Handles the
  // 409 consent fallback (error carrying requiresConsent + consentItems) by
  // opening the same dialog.
  async function doSave(acceptPrivilegedExposure, grants) {
    saveError = ''
    try {
      const warningAcceptance = acceptWarnings === 'yes'
        ? 'Warnings accepted by workspace operator.'
        : ''
      const res = await bridge.save(workflow, acceptPrivilegedExposure, grants, warningAcceptance, initialGeneratedWorkflow)
      const id = res.agentId
      loadedAgentId = id || loadedAgentId
      // A workflow that delegates to a peer the workspace didn't have causes
      // that peer to be created here. Say so: otherwise the agent list silently
      // grows and the user has no idea where "summarizer" came from.
      const peers = Array.isArray(res.peerAgents) ? res.peerAgents : []
      const alsoMade = peers.length
        ? ` Also created ${peers.length === 1 ? 'helper agent' : 'helper agents'} ${peers.join(', ')} — this workflow delegates to ${peers.length === 1 ? 'it' : 'them'}.`
        : ''
      // Say which of the two actually happened. A save that leaves a running
      // agent running must not report it as disabled — that message was the
      // only notice a user got, and for an edit it was wrong.
      saveMsg = res.enabled
        ? `Saved ${id} — it stays enabled and the schedule is updated.${alsoMade}`
        : `Saved as disabled agent ${id} — enable it from Deployed.${alsoMade}`
      // The justification belongs to the save that consumed it; carrying it into
      // the next one would silently reuse a reason the user never re-affirmed.
      acceptWarnings = ''
      $editAgent = res.agentId
      // A saved draft is finished work, not work-in-progress.
      //
      // Save navigates to Deployed, so onDestroy would snapshot the draft and
      // Studio would reopen mid-agent the next time it was clicked — with no way
      // back to the home screen short of a page refresh. The agent is persisted
      // and listed under "Continue existing work", so keeping a private copy of
      // it as an unfinished session is both redundant and misleading.
      //
      // The flag is what actually makes this stick: navigating unmounts Studio,
      // and onDestroy runs AFTER this line, so clearing the store here alone
      // would be immediately overwritten by the very snapshot being suppressed.
      sessionCommitted = true
      studioSession.set(null)
      window.location.hash = '#agents'
      consent = null
    } catch (e) {
      // 409 fallback: server demands consent even though plan didn't (or the
      // draft changed). Show the dialog rather than a raw error.
      if (e && e.requiresConsent && !acceptPrivilegedExposure) {
        openConsent(e.consentItems)
        return
      }
      if (e && e.contract) {
        const gate = contractToPreflight(e.contract)
        preflight = {
          ok: false,
          blockers: gate.blockers,
          warnings: gate.warnings,
          contract: e.contract,
          security: null,
        }
        return
      }
      if (e && e.preflight) {
        preflight = e.preflight
        return
      }
      saveError = e.message || 'save failed'
      consent = null
    }
  }

  // Fetch the saved agent's most recent live run trace — the per-block record
  // of what actually ran (input, output, duration, error, whether the input came
  // from typed port wires). Surfaced so a non-technical user can see WHERE a real
  // run went wrong, not just that it failed.
  // Which agent the trace panel is showing. The Failed-runs list spans EVERY
  // agent, so a failure can belong to something other than the draft on the
  // canvas — and with an unsaved draft open there is no loadedAgentId at all.
  // Keying the fetch off loadedAgentId meant clicking a failure either returned
  // silently ("No diagnosis for this run") or loaded the wrong agent's trace.
  let runTraceAgentId = ''
  async function loadRunTrace(runId = '', agentId = '') {
    const aid = agentId || runTraceAgentId || loadedAgentId
    if (!aid || runTraceLoading) return
    runTraceAgentId = aid
    runTraceLoading = true
    runTraceErr = ''
    selectedRunId = runId || ''
    try {
      // Fetch the chosen run's trace AND refresh the full run history (every run,
      // scheduled or on-demand) so the picker stays current.
      const [res, hist, diagnosis] = await Promise.all([
        bridge.runTrace(aid, runId || undefined),
        bridge.runHistory(aid).catch(() => ({ runs: [] })),
        bridge.runDiagnosis(aid, runId || undefined).catch(() => null),
      ])
      runTrace = res || null
      runDiagnosis = diagnosis || null
      runHistory = (hist && hist.runs) || []
      if (!selectedRunId && runTrace && runTrace.runId) selectedRunId = runTrace.runId
      if (!res || !res.entries || !res.entries.length) {
        runTraceErr = runHistory.length
          ? 'Select a run above to view its trace.'
          : 'No runs yet — enable the agent and let it run once (on schedule or on demand).'
      }
    } catch (e) {
      runDiagnosis = null
      runTraceErr = e.message || 'could not load run trace'
    } finally {
      runTraceLoading = false
    }
  }

  function openConsent(items, mode = 'save') {
    const list = Array.isArray(items) ? items : []
    // Default every code item's scope to "this workflow until the code changes".
    const scopes = {}
    for (const it of list) {
      if (it.kind === 'code') scopes[it.name] = 'workflow'
    }
    consent = { items: list, scopes, mode }
  }

  // selectNodeById opens a block in the inspector by id, so an error message can
  // take the user straight to the field that fixes it (e.g. a timeout) instead
  // of making them hunt for the block on the canvas.
  function selectNodeById(nodeId) {
    const node = ((workflow && workflow.flow && workflow.flow.nodes) || []).find((n) => n.id === nodeId)
    if (!node) return
    selectedEdge = null
    selectedNode = node
  }

  // ── Consent from a FAILED LIVE RUN ──────────────────────────────────────────
  // A live run posts the UNSAVED draft, so the engine's fail-closed consent gate
  // refuses any beyond-guardrail python block that carries no stamp. Previously
  // that surfaced as a raw Go error whose only offered action was "Self-correct
  // workflow" — which would ask a model to rewrite code that isn't broken. The
  // fix is a grant, so offer a grant.
  //
  // Consent items come from the SERVER (the existing /studio/plan endpoint), not
  // from the client: plan computes the authoritative code hash, and a grant is
  // bound to that hash. Computing it here would risk drifting from Go's.
  let consentLoading = false
  async function openConsentForRunError(nodeId) {
    if (!workflow || consentLoading) return
    consentLoading = true
    try {
      const p = await bridge.plan(workflow)
      const all = (p && p.consentItems) || []
      // Narrow to the node that actually failed when we can identify it — the
      // user asked about THIS block, not every pending grant in the draft.
      const forNode = all.filter((it) => it.kind === 'code' && it.name === nodeId)
      const items = forNode.length ? forNode : all.filter((it) => it.kind === 'code')
      if (!items.length) {
        compileError = 'Could not determine what needs approval — try saving the workflow to review consent.'
        return
      }
      openConsent(items, 'run')
    } catch (e) {
      compileError = (e && e.message) || 'Could not load the consent details.'
    } finally {
      consentLoading = false
    }
  }

  // Stamp the in-memory draft's nodes with the granted consent, then re-run.
  // Deliberately does NOT save: the whole point is to keep iterating in the
  // workbench. This is not a security shortcut — the engine re-validates every
  // stamp against the code it actually receives (hash binding), and the
  // allow_system_tools ceiling is enforced server-side regardless, so a grant
  // made here can never widen what the server permits.
  async function grantAndRerun() {
    if (!consent || !workflow) return
    const grants = collectGrants()
    const nodes = (workflow.flow && workflow.flow.nodes) || []
    const now = new Date().toISOString()
    for (const g of grants) {
      const node = nodes.find((n) => n.id === g.nodeId)
      if (!node) continue
      node.consent = {
        hash: g.hash,
        capabilities: g.capabilities,
        scope: g.scope,
        granted_at: now,
        granted_by: 'studio',
      }
    }
    workflow = workflow // tell Svelte the draft changed
    consent = null
    await tryAgent()
  }

  function setConsentScope(nodeId, scope) {
    if (!consent) return
    consent = { ...consent, scopes: { ...consent.scopes, [nodeId]: scope } }
  }

  // The inline code for a consent item's node, shown in the dialog so the user
  // sees exactly what they're approving.
  function codeForNode(nodeId) {
    const n = draftNodes.find((x) => x.id === nodeId)
    return (n && n.code) || ''
  }

  // Build the per-node grant array from the code consent items + chosen scopes.
  function collectGrants() {
    if (!consent) return []
    return (consent.items || [])
      .filter((it) => it.kind === 'code')
      .map((it) => ({
        nodeId: it.name,
        hash: it.hash,
        capabilities: it.capabilities || [],
        scope: (consent.scopes && consent.scopes[it.name]) || 'workflow',
      }))
  }

  async function acknowledgeConsent() {
    if (saving) return
    saving = true
    try {
      await doSave(true, collectGrants())
    } finally {
      saving = false
    }
  }

  function cancelConsent() {
    consent = null
    saving = false
  }

  function tierLabel(t) {
    if (t === 'privileged') return 'privileged'
    if (t === 'active') return 'active'
    if (t === 'readonly') return 'read-only'
    return t || ''
  }

  function fmt(v) {
    if (v == null) return ''
    if (typeof v === 'object') return JSON.stringify(v, null, 2)
    return String(v)
  }

  // prettyJSON renders a value as indented JSON — and, crucially, parses a STRING
  // that holds JSON (the rendered node input arrives as a string) so it reads as
  // formatted JSON instead of one long escaped line.
  function prettyJSON(v) {
    if (v == null) return ''
    if (typeof v === 'string') {
      const s = v.trim()
      if (s && (s[0] === '{' || s[0] === '[')) {
        try { return JSON.stringify(JSON.parse(s), null, 2) } catch (_) { /* not JSON */ }
      }
      return v
    }
    try { return JSON.stringify(v, null, 2) } catch (_) { return String(v) }
  }

  // asObject coerces a step's output (object or JSON string) to an object, or null.
  function asObject(v) {
    if (v && typeof v === 'object') return v
    if (typeof v === 'string') { try { return JSON.parse(v) } catch (_) { return null } }
    return null
  }

  // stepToolError surfaces a TOOL-LEVEL error that the transport hid: a node can
  // return `error:""` (the MCP call succeeded) while its OUTPUT carries
  // {"status":"error","error":"…"}. This returns that human error message (or the
  // node's own runtime error), else "" — so a failed step reads as failed.
  function stepToolError(step) {
    if (step && step.error) return step.error
    const o = asObject(step && step.output)
    if (o && (o.status === 'error' || o.status === 'failed')) return o.error || o.message || 'tool returned an error'
    if (o && o.error) return String(o.error)
    return ''
  }

  // stepSummary gives a one-line, human read of a successful step: its status plus
  // the most useful identifying field and any message — so you rarely need to open
  // the raw JSON at all.
  function stepSummary(step) {
    const o = asObject(step && step.output)
    if (!o) return ''
    const bits = []
    if (o.status) bits.push(o.status)
    for (const k of ['notebook_id', 'task_id', 'artifact_id', 'id', 'imported_count', 'sources_found', 'count']) {
      if (o[k] != null) { bits.push(`${k}: ${String(o[k]).slice(0, 48)}`); break }
    }
    if (o.message) bits.push(String(o.message).slice(0, 120))
    return bits.join(' · ')
  }

  const kinds = ['tool', 'agent', 'branch']

  // ── M6: Templates + empty-state picker (S6.1) ─────────────────────────────
  // No localStorage in the sandbox, so "is there a current draft?" is simply
  // `workflow != null`. On a fresh open (no draft) we show a template picker;
  // a top-bar "Templates" button reopens it any time. Choosing one loads its
  // workflow as the current draft.
  let templatePicker = { open: false, loading: false, error: '', items: [] }

  async function openTemplates() {
    templatePicker = { open: true, loading: true, error: '', items: [] }
    try {
      const data = await bridge.templates()
      const items = (data && Array.isArray(data.templates)) ? data.templates : []
      templatePicker = { open: true, loading: false, error: '', items }
    } catch (e) {
      templatePicker = { open: true, loading: false, error: e.message || 'could not load templates', items: [] }
    }
  }

  function closeTemplates() {
    templatePicker = { ...templatePicker, open: false }
  }

  function chooseTemplate(t) {
    if (!t || !t.workflow) return
    setWorkflow(t.workflow, { name: t.name })
    closeTemplates()
  }

  // ── Draft library + "My Workflows" (published agent workflows) ────────────
  let savingDraft = false
  let library = { open: false, loading: false, error: '', drafts: [], agents: [], busyId: '' }

  // Read-only SOUL.yaml browser: view the raw on-disk SOUL.yaml of ANY registered
  // agent (not only workflow-bearing ones), straight from the file — no lossy
  // draft round-trip. Purely for inspection; editing still goes through the
  // canvas/code views.
  let yamlBrowser = {
    open: false, loading: false, error: '',
    agents: [], selectedId: '', yaml: '', path: '', yamlLoading: false, yamlError: '',
  }

  // Open the browser and load the full agent list (every agent, not just
  // Studio-authored workflows).
  async function openYamlBrowser() {
    yamlBrowser = { ...yamlBrowser, open: true, loading: true, error: '', agents: [], selectedId: '', yaml: '', path: '', yamlError: '' }
    try {
      const res = await bridge.allAgents()
      const agents = Array.isArray(res?.agents) ? res.agents : (Array.isArray(res) ? res : [])
      yamlBrowser = { ...yamlBrowser, loading: false, agents }
      // Auto-select the first agent for immediate context.
      if (agents.length) viewAgentYaml(agents[0].id)
    } catch (e) {
      yamlBrowser = { ...yamlBrowser, loading: false, error: e?.message || 'Failed to load agents' }
    }
  }

  function closeYamlBrowser() {
    yamlBrowser = { ...yamlBrowser, open: false }
  }

  // Fetch and show the raw SOUL.yaml for one agent.
  async function viewAgentYaml(id) {
    if (!id) return
    yamlBrowser = { ...yamlBrowser, selectedId: id, yamlLoading: true, yamlError: '', yaml: '', path: '' }
    try {
      const res = await bridge.agentYaml(id)
      yamlBrowser = { ...yamlBrowser, yamlLoading: false, yaml: res?.yaml || '', path: res?.path || '' }
    } catch (e) {
      yamlBrowser = { ...yamlBrowser, yamlLoading: false, yamlError: e?.message || 'Failed to load SOUL.yaml' }
    }
  }

  // Autosave-as-a-draft has been REMOVED.
  //
  // It wrote a "⟳ Last session" draft after every generate, intending one
  // recoverable slot. In practice the dedup key lived in memory, so every reload
  // started a fresh slot and the drafts list filled with identical entries —
  // burying the drafts a user had deliberately named. A recovery feature that
  // makes your real work harder to find is a net loss, so the drafts list is now
  // only ever what the user chose to save ("Save draft").
  //
  // In-session recovery is unaffected: studioSession still restores the working
  // draft across navigation within the app.

  // Drafts shown in the left palette (Drafts group, "Draft" badge). Refreshed
  // on mount and whenever a draft is saved/deleted.
  let paletteDrafts = []
  async function refreshPaletteDrafts() {
    try {
      const res = await bridge.draftsList()
      paletteDrafts = (res && Array.isArray(res.drafts)) ? res.drafts : []
    } catch (_) { /* leave as-is on error */ }
  }
  // Load a draft onto the canvas by id (palette click).
  function openDraftById(id) { if (id) loadDraft({ id }) }
  // Delete a draft from the palette, then refresh the list.
  async function deleteDraftFromPalette(id, name) {
    let ok = true
    try { ok = confirmDestructive(`Delete draft “${name || id}”?`) } catch (_) { ok = true }
    if (!ok) return
    try { await bridge.draftDelete(id) } catch (_) { /* best-effort */ }
    await refreshPaletteDrafts()
  }

  // { name } while the name dialog is open, null otherwise.
  let draftPrompt = null

  function saveDraft() {
    if (!workflow || savingDraft) return
    draftPrompt = { name: (workflow.name || '').trim() || 'My workflow' }
  }

  async function confirmSaveDraft() {
    if (!workflow || savingDraft || !draftPrompt) return
    const fallback = (workflow.name || '').trim() || 'My workflow'
    const name = (draftPrompt.name || '').trim() || fallback
    draftPrompt = null
    savingDraft = true
    saveError = ''
    saveMsg = ''
    try {
      // Rename FIRST, then save that. The old order saved the pre-rename draft
      // and renamed only the copy on screen, so reopening the saved draft
      // brought back the name the user had just replaced.
      if (name && workflow.name !== name) {
        workflow = { ...workflow, name }
        rebuildGraph()
      }
      const res = await bridge.draftSave(name, workflow)
      const id = (res && res.id) || ''
      toast(`Saved draft “${name}”${id ? ' (' + id + ')' : ''}.`)
      refreshPaletteDrafts()
    } catch (e) {
      saveError = e.message || 'could not save draft'
    } finally {
      savingDraft = false
    }
  }

  // Opens the unified "My Workflows" panel: published agent workflows + drafts,
  // fetched together. Each list degrades independently so one failure doesn't
  // hide the other.
  // Load the agents + drafts lists. Split out from openLibrary because the
  // Describe step needs them too: gating the block palette to Build left the
  // opening screen with no route to existing work at all, so the only way to
  // reach a saved agent was to know the modal was there. `open` is preserved so
  // this can refresh in the background without popping the dialog.
  async function loadLibrary({ open = library.open } = {}) {
    library = { ...library, open, loading: true, error: '' }
    const [agentsRes, draftsRes] = await Promise.allSettled([
      bridge.agentWorkflows(),
      bridge.draftsList(),
    ])
    const agents = agentsRes.status === 'fulfilled' && Array.isArray(agentsRes.value?.agents)
      ? agentsRes.value.agents : []
    const drafts = draftsRes.status === 'fulfilled' && Array.isArray(draftsRes.value?.drafts)
      ? draftsRes.value.drafts : []
    const errs = []
    if (agentsRes.status === 'rejected') errs.push('agents: ' + (agentsRes.reason?.message || 'failed'))
    if (draftsRes.status === 'rejected') errs.push('drafts: ' + (draftsRes.reason?.message || 'failed'))
    library = { open, loading: false, error: errs.join(' · '), drafts, agents, busyId: '' }
  }

  async function openLibrary() {
    library = { ...library, open: true, loading: true, error: '' }
    await loadLibrary({ open: true })
  }

  // Populate on mount so Describe can offer "continue existing work" without
  // the user having to open a dialog to discover their own agents exist.
  onMount(() => { loadLibrary({ open: false }).catch(() => {}) })

  function closeLibrary() {
    // busyId is the per-row "in flight" lock. Only the error paths used to
    // clear it, so a SUCCESSFUL load left it set forever and every library and
    // palette button stayed disabled (or silently returned at its guard) until
    // the page was reloaded.
    library = { ...library, open: false, busyId: '' }
  }

  // ── Library search + faceted filters (ST-15) ──────────────────────────────
  let libQuery = emptyQuery()
  $: libParts = partitionLibrary(library.agents, library.drafts)
  $: libAll = [...libParts.deployed, ...libParts.saved, ...libParts.drafts]
  $: libFacets = libraryFacets(libAll)
  $: libDeployed = filterLibrary(libParts.deployed, libQuery)
  $: libSaved = filterLibrary(libParts.saved, libQuery)
  $: libDrafts = filterLibrary(libParts.drafts, libQuery)
  $: libFiltered = hasActiveFilters(libQuery)
  $: libMatchCount = libDeployed.length + libSaved.length + libDrafts.length
  // Everything openable, for the rail's "Open…" badge.
  $: libCount = libParts.deployed.length + libParts.saved.length + libParts.drafts.length
  function clearLibFilters() { libQuery = emptyQuery() }

  // Clone: open a copy on the canvas under a new name, leaving the original
  // untouched. Deliberately does NOT save — a clone is a starting point, and
  // silently creating a second stored agent from a browse action would be a
  // surprising write.
  async function cloneFromLibrary(item) {
    if (!item || !item.id || library.busyId) return
    library = { ...library, busyId: item.id, error: '' }
    try {
      const src = item.kind === 'draft'
        ? await bridge.draftLoad(item.id)
        : await bridge.loadAgentWorkflow(item.id)
      const wf = (src && (src.workflow || src.draft)) || null
      if (!wf) throw new Error('could not read that workflow')
      const base = (item.name || item.id).replace(/\s*\(copy( \d+)?\)$/i, '')
      setWorkflow({ ...wf, name: `${base} (copy)` })
      loadedAgentId = null   // a clone is unsaved: Save must create, not overwrite
      library = { ...library, busyId: '', open: false }
      toast(`Cloned “${item.name || item.id}”. Save to keep it.`)
    } catch (e) {
      library = { ...library, busyId: '', error: (e && e.message) || 'could not clone' }
    }
  }

  // Test: load it and drop straight into the bench, so "does this still work?"
  // does not require finding the run controls afterwards.
  async function testFromLibrary(item) {
    if (!item || !item.id) return
    if (item.kind === 'draft') await loadDraft(item)
    else await loadAgentForEdit(item)
    closeLibrary()
    showTests = true
    viewMode = 'canvas'
    runTest()
  }

  // Export: download the SOUL.yaml without loading it onto the canvas, so
  // exporting cannot disturb whatever is currently open.
  async function exportFromLibrary(item) {
    if (!item || !item.id || library.busyId) return
    library = { ...library, busyId: item.id, error: '' }
    try {
      let yaml = ''
      if (item.kind === 'draft') {
        const src = await bridge.draftLoad(item.id)
        const wf = src && (src.workflow || src.draft)
        const r = wf ? await bridge.toYaml(wf) : null
        yaml = (r && r.yaml) || ''
      } else {
        const raw = await bridge.agentYaml(item.id)
        yaml = (raw && raw.yaml) || ''
      }
      if (!yaml) throw new Error('nothing to export')
      downloadText(`${(item.name || item.id).replace(/[^\w.-]+/g, '_')}.soul.yaml`, yaml)
      library = { ...library, busyId: '' }
    } catch (e) {
      library = { ...library, busyId: '', error: (e && e.message) || 'could not export' }
    }
  }

  // Deploy / undeploy from the library. This is the enabled flag — the thing
  // that decides whether the schedule actually fires — so it is confirmed, and
  // the list is updated in place rather than silently re-fetched.
  async function setDeployed(item, enabled) {
    if (!item || !item.id || library.busyId) return
    if (enabled) {
      let ok = true
      try {
        ok = confirmDestructive(`Deploy “${item.name || item.id}”? It will start running on its trigger.`)
      } catch (_) { ok = true }
      if (!ok) return
    }
    library = { ...library, busyId: item.id, error: '' }
    try {
      if (enabled) await bridge.agentEnable(item.id)
      else await bridge.agentDisable(item.id)
      library = {
        ...library,
        busyId: '',
        agents: library.agents.map((a) => (a.id === item.id ? { ...a, enabled } : a)),
      }
      toast(`${enabled ? 'Deployed' : 'Undeployed'} ${item.name || item.id}.`)
      await loadCatalog()
    } catch (e) {
      library = { ...library, busyId: '', error: (e && e.message) || 'could not change deployment' }
    }
  }

  // Start a brand-new, empty workflow (clears the canvas).
  function newWorkflow() {
    setWorkflow(null)
    closeLibrary()
  }

  // Click an agent in the left palette → load its workflow onto the canvas for
  // editing. Re-Saving (same name → same id) upserts it.
  async function openAgentOnCanvas(id) {
    if (!id || library.busyId) return
    // Same row-level lock the library list uses, so the palette row greys out
    // while the workflow loads and a double-click cannot race two setWorkflow
    // calls onto the canvas.
    library = { ...library, busyId: id }
    try {
      const data = await bridge.loadAgentWorkflow(id)
      const wf = (data && data.workflow) || null
      if (!wf) { toast('This agent has no editable workflow.'); return }
      setWorkflow(wf, { name: wf.name })
      loadedAgentId = id
      runTrace = null
      runDiagnosis = null
      toast('Loaded workflow — edit and Save to update the agent.')
    } catch (e) {
      toast(e.message || 'Could not load agent workflow')
    } finally {
      library = { ...library, busyId: '' }
    }
  }

  // When the agent currently ON the canvas is deleted, the canvas must go with
  // it. Leaving the workflow up is worse than untidy: the editor still thinks
  // it is editing agent X, so hitting Save would silently RECREATE the agent the
  // user just deleted. Clearing loadedAgentId is what actually prevents that;
  // wiping the canvas is what makes the deletion believable.
  function clearCanvasIfShowing(id) {
    if (!id || loadedAgentId !== id) return false
    loadedAgentId = ''
    runTrace = null
    runDiagnosis = null
    codeYaml = ''
    codeOrig = ''
    codeValidation = null
    codeWarnings = []
    compileError = ''
    setWorkflow(null)
    studioSession.set(null)
    return true
  }

  // Drop any STORED session that was showing the deleted agent, separately from
  // the live canvas.
  //
  // Clearing only the canvas left the deleted agent in the cross-navigation
  // snapshot, so it reappeared the next time the user came back to Studio —
  // which is why the delete looked like it needed a page refresh to take effect.
  // Called on every delete path, including the ones where the canvas was showing
  // something else entirely.
  function forgetDeletedAgentSession(id) {
    studioSession.set(sessionAfterDelete(get(studioSession), id))
    forgetCarriedAgent(id)
  }

  // Delete an agent straight from the palette (with confirm), then refresh the
  // palette so it disappears.
  async function deleteAgentFromPalette(id, name) {
    if (!id) return
    let ok = true
    try { ok = confirmDestructive(`Delete agent “${name || id}”? This cannot be undone.`) } catch (_) { ok = true }
    if (!ok) return
    try {
      await bridge.deleteAgent(id)
      const wasOpen = clearCanvasIfShowing(id)
      forgetDeletedAgentSession(id)
      toast(wasOpen
        ? `Deleted agent ${name || id}. Canvas cleared.`
        : `Deleted agent ${name || id}.`)
      await loadCatalog()
    } catch (e) {
      toast(e.message || 'Could not delete agent')
    }
  }

  // Load a saved agent's workflow back onto the canvas for editing. Re-saving
  // (with the same name → same id) upserts the agent.
  async function loadAgentForEdit(a) {
    if (!a || !a.id || library.busyId) return
    library = { ...library, busyId: a.id, error: '' }
    try {
      const data = await bridge.loadAgentWorkflow(a.id)
      const wf = (data && data.workflow) || null
      if (!wf) throw new Error('agent has no editable workflow')
      setWorkflow(wf, { name: wf.name || a.name })
      loadedAgentId = a.id
      runTrace = null
      runDiagnosis = null
      closeLibrary()
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not load agent' }
    }
  }

  async function deleteAgentWorkflow(a) {
    if (!a || !a.id || library.busyId) return
    let ok = true
    try { ok = confirmDestructive(`Delete agent “${a.name || a.id}”? This cannot be undone.`) } catch (_) { ok = true }
    if (!ok) return
    library = { ...library, busyId: a.id, error: '' }
    try {
      await bridge.deleteAgent(a.id)
      library = { ...library, busyId: '', agents: library.agents.filter((x) => x.id !== a.id) }
      if (clearCanvasIfShowing(a.id)) toast(`Deleted agent ${a.name || a.id}. Canvas cleared.`)
      forgetDeletedAgentSession(a.id)
      await loadCatalog()
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not delete agent' }
    }
  }

  async function loadDraft(d) {
    if (!d || !d.id || library.busyId) return
    library = { ...library, busyId: d.id, error: '' }
    try {
      const data = await bridge.draftLoad(d.id)
      const wf = (data && data.workflow) || null
      if (!wf) throw new Error('draft has no workflow')
      setWorkflow(wf, { name: (data && data.name) || d.name })
      closeLibrary()
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not load draft' }
    }
  }

  async function deleteDraft(d) {
    if (!d || !d.id || library.busyId) return
    library = { ...library, busyId: d.id, error: '' }
    try {
      await bridge.draftDelete(d.id)
      const drafts = library.drafts.filter((x) => x.id !== d.id)
      library = { ...library, busyId: '', drafts }
    } catch (e) {
      library = { ...library, busyId: '', error: e.message || 'could not delete draft' }
    }
  }

  function draftWhen(d) {
    const u = d && d.updated
    if (!u) return ''
    const t = typeof u === 'number' ? u : Date.parse(u)
    if (Number.isNaN(t)) return String(u)
    return new Date(t).toLocaleString()
  }

  // ── M6: Export / Import (S6.4) — client-side, no backend ──────────────────
  let importError = ''
  let fileInputEl                    // bound hidden <input type=file>

  function safeFileName(base) {
    const s = (base || 'workflow').trim().toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '')
    return (s || 'workflow') + '.studio.json'
  }

  // Download arbitrary text as a file. Shared by the draft export and the
  // library's per-item export so both use one code path for the
  // create-anchor/click/revoke dance.
  function downloadText(filename, text, mime = 'text/plain') {
    const blob = new Blob([text], { type: mime })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    document.body.appendChild(a)
    a.click()
    a.remove()
    // Revoke after the click has a chance to start the download.
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  function exportDraft() {
    if (!workflow) return
    try {
      downloadText(safeFileName(workflow.name), JSON.stringify(workflow, null, 2), 'application/json')
      toast('Exported draft.')
    } catch (e) {
      saveError = e.message || 'export failed'
    }
  }

  function triggerImport() {
    importError = ''
    if (fileInputEl) fileInputEl.click()
  }

  function onImportFile(event) {
    const file = event && event.target && event.target.files && event.target.files[0]
    // Reset the input so picking the same file again re-fires change.
    if (event && event.target) event.target.value = ''
    if (!file) return
    importError = ''
    const reader = new FileReader()
    reader.onerror = () => { importError = 'Could not read the file.' }
    reader.onload = () => {
      let parsed
      try {
        parsed = JSON.parse(String(reader.result || ''))
      } catch (e) {
        importError = 'Not valid JSON: ' + (e.message || 'parse error')
        return
      }
      // Accept either a bare workflow or an export envelope.
      const wf = (parsed && parsed.flow) ? parsed : (parsed && parsed.workflow) ? parsed.workflow : null
      if (!wf || typeof wf !== 'object' || !wf.flow) {
        importError = 'That file does not look like a Studio workflow.'
        return
      }
      setWorkflow(wf, { name: wf.name })
      toast('Imported draft.')
    }
    reader.readAsText(file)
  }

  // ── M6: Per-node refine (S6.3) ────────────────────────────────────────────
  // Lives in the Inspector when a node is selected. Sends { workflow, nodeId,
  // instruction } to the host's refine op and REPLACES the current workflow
  // with the returned one, then re-validates. Spinner + graceful errors.
  let refineState = { loading: false, error: '', message: '' }

  async function refineNode(nodeId, instruction) {
    const instr = (instruction || '').trim()
    if (!workflow || !nodeId || !instr || refineState.loading) return
    if (!ensureCloudOk(() => refineNode(nodeId, instruction))) return
    refineState = { loading: true, error: '', message: '' }
    try {
      const data = await bridge.refine(workflow, nodeId, instr)
      const wf = (data && data.workflow) || null
      if (!wf || typeof wf !== 'object') throw new Error('refine returned no workflow')
      // Keep the node selected (by id) across the swap if it still exists.
      const keepId = nodeId
      const prevName = workflow.name
      workflow = (wf.name || !prevName) ? wf : { ...wf, name: prevName }
      plan = null
      validation = null
      // Re-resolve the selected node from the new workflow if it survived.
      const nextNodes = (workflow.flow && Array.isArray(workflow.flow.nodes)) ? workflow.flow.nodes : []
      const stillThere = nextNodes.find((n) => n.id === keepId)
      selectedNode = stillThere || null
      selectedEdge = null
      rebuildGraph()
      scheduleValidate()
      refineState = { loading: false, error: '', message: 'Applied.' }
    } catch (e) {
      refineState = { loading: false, error: e.message || 'refine failed', message: '' }
    }
  }

  // ── M6: lightweight toast (session-only; the sandbox has no storage) ──────
  let toastMsg = ''
  let toastTimer = null
  function toast(msg) {
    toastMsg = msg
    if (toastTimer) clearTimeout(toastTimer)
    toastTimer = setTimeout(() => { toastMsg = '' }, 3500)
  }

  // Framework-written Python: deterministic scaffolds (fetched once) + the
  // framework's own model writing a node's code on demand.
  let scaffolds = []
  onMount(() => {
    bridge.scaffolds().then((d) => { scaffolds = (d && d.scaffolds) || [] }).catch(() => { scaffolds = [] })
  })

  async function generateNodeCode(nodeId) {
    const n = draftNodes.find((x) => x.id === nodeId)
    if (!n) return ''
    try {
      const r = await bridge.generateCode(nodeId, n.description || '', workflow)
      return (r && r.code) || ''
    } catch (e) {
      toast(e.message || 'code generation failed')
      return ''
    }
  }

  // ── Studio model picker (llm.studio) ─────────────────────────────────────
  // Lets the developer choose which IN-FRAMEWORK provider/model Studio uses for
  // its reasoning + code generation, without editing config.yaml by hand.
  let modelPicker = { open: false, provider: '', defaultProvider: '', model: '', models: [], saving: false, error: '' }
  $: modelPickerUsesNVIDIA = modelPicker.provider === 'nvidia' || (!modelPicker.provider && modelPicker.defaultProvider === 'nvidia')
  let studioModelLabel = 'default'

  // Story 9 (Cohort B) — intent-named runtime presets. `studioPresets` holds
  // the catalog fetched from GET /studio/presets; `studioPresetCurrent` is the
  // operator's current choice (empty = model-derived defaults, no override).
  let studioPresets = []
  let studioPresetCurrent = ''
  let studioPresetSaving = false
  let studioPresetError = ''

  // Story 9 M (Cohort C) — build UX preference. `streamed` (default) surfaces
  // a live-transcript panel while the pipeline runs; `wizard` opens the
  // classic stepped modal (refine → confirm → compile) with the strategy
  // review step made explicit. Persisted in llm.studio.build_ux; a
  // per-generation override lives on `buildUXOverride` (nullable).
  let buildUX = 'streamed'
  let buildUXOverride = null
  let buildUXSaving = false
  // Live-transcript state for a streamed generate.
  let pipelineLog = [] // [{phase, status, message, payload}]
  let pipelineRunning = false
  // ST-04 streamed generation: elapsed time, per-node progress, cancellation.
  let pipelineElapsed = 0        // ms
  let pipelineNodes = []         // [{ id, kind, label, index, total }]
  let pipelineCancelled = false
  let genAbort = null            // AbortController for the in-flight stream
  // Progress modal: shown while any generation is in flight so the user can see
  // what's happening instead of staring at a blank/"Compiling…" canvas. It works
  // for BOTH the streamed pipeline (pipelineRunning, with real per-phase events)
  // and the classic refine→compile path (refining/compiling, no sub-events).
  // `pipelineModalHidden` lets them dismiss the overlay and let it run on.
  let pipelineModalHidden = false
  // modalRefining belongs here too: the Describe step's "Refine prompt" is a
  // full builder-model call, and leaving it out of genBusy is what made that
  // button the one place in Studio where a long wait showed nothing at all.
  $: genBusy = pipelineRunning || compiling || refining || modalRefining
  $: pipelineLatest = pipelineLog.length ? pipelineLog[pipelineLog.length - 1] : null
  // Real backend phase names (from internal/studio/generatepipeline.go) — not
  // invented UI copy. Used to label the streamed events the server actually
  // emits, and to derive the current-operation line.
  function phaseLabel(phase) {
    switch (phase) {
      case 'clarify_intent':  return 'Clarifying intent'
      case 'choose_strategy': return 'Choosing strategy'
      case 'build_graph':     return 'Building the graph'
      case 'validate':        return 'Validating'
      case 'repair':          return 'Repairing wiring'
      default:                return phase || 'Working'
    }
  }
  // The one-line "what's happening right now", reflecting the real operation in
  // flight rather than a scripted step. The refine and compile calls ARE real
  // builder-model calls; the streamed path names its actual current phase.
  $: genStatusText = (refining || modalRefining)
      ? 'Refining your prompt into a build-ready spec…'
      : pipelineRunning
        ? (pipelineLatest ? phaseLabel(pipelineLatest.phase) + '…' : 'Starting the build pipeline…')
        : compiling
          ? 'Asking the builder model to write the workflow…'
          : 'Working…'

  $: providerOptions = (catalog && catalog.providers && catalog.providers.providers)
    ? Object.keys(catalog.providers.providers) : []

  function fmtModelLabel(provider, model) {
    if (!provider) return 'default'
    return model ? `${provider} / ${model}` : provider
  }

  onMount(() => {
    bridge.getConfig().then((cfg) => {
      const st = (cfg && cfg.llm && cfg.llm.studio) || {}
      studioModelLabel = fmtModelLabel(st.provider, st.model)
      studioPresetCurrent = st.preset || ''
      if (st.build_ux === 'wizard' || st.build_ux === 'streamed') buildUX = st.build_ux
    }).catch(() => {})
    bridge.presets().then((res) => {
      studioPresets = (res && res.presets) || []
      if (res && typeof res.current === 'string') studioPresetCurrent = res.current
    }).catch(() => { studioPresets = [] })
    refreshModelAdvice()
    refreshStrategyFit()
  })

  async function saveStudioPreset(name) {
    if (studioPresetSaving) return
    studioPresetSaving = true
    studioPresetError = ''
    try {
      await bridge.setStudioPreset(name || '')
      studioPresetCurrent = name || ''
    } catch (e) {
      studioPresetError = (e && e.message) || 'could not save preset'
    } finally {
      studioPresetSaving = false
    }
  }

  async function saveBuildUX(mode) {
    if (buildUXSaving) return
    buildUXSaving = true
    try {
      await bridge.setBuildUX(mode)
      buildUX = mode
    } catch (_) { /* soft-fail — UX preference is non-critical */ }
    finally { buildUXSaving = false }
  }

  // effectiveBuildUX resolves the per-generation override on top of the
  // persisted preference. Toggling the button next to Generate flips this
  // for the next run without touching the saved setting.
  $: effectiveBuildUX = buildUXOverride || buildUX

  // Story 9 M (Cohort C) — streamed generate. Runs the whole pipeline in one
  // SSE call, updating `pipelineLog` per phase. When the stream finishes
  // successfully the compiled draft is applied to the canvas via the same
  // applyCompile path the classic flow uses.
  async function generateStreamed() {
    const text = intentOf(intent, rawPrompt)
    if (!text || compiling || refining || pipelineRunning || cloudGate) return
    if (!ensureCloudOk(generateStreamed)) return
    if (workflow && workflow.flow && (workflow.flow.nodes || []).length) {
      let ok = true
      try { ok = confirmLocal('Regenerate from this prompt? It replaces the current workflow on the canvas.') } catch (_) { ok = true }
      if (!ok) return
    }
    pipelineRunning = true
    pipelineModalHidden = false
    pipelineLog = []
    pipelineNodes = []
    pipelineElapsed = 0
    pipelineCancelled = false
    compileError = ''
    genAbort = new AbortController()
    // Tick locally so elapsed keeps moving during a long phase that emits no
    // events — a frozen counter is exactly what "is this stuck?" looks like.
    const t0 = Date.now()
    const timer = setInterval(() => { pipelineElapsed = Date.now() - t0 }, 200)
    try {
      const light = !!(workflow && workflow.refined)
      if (!light && !rawPrompt.trim()) rawPrompt = text
      const done = await bridge.generateStream(
        intentWithGenerationTrigger(text, generationTrigger),
        { light, auto_repair: true },
        (ev) => {
          if (!ev || !ev.phase) return
          if (typeof ev.elapsed_ms === 'number') pipelineElapsed = ev.elapsed_ms
          if (ev.status === 'cancelled') pipelineCancelled = true
          // Node events build the graph preview progressively rather than
          // revealing it whole at the end.
          if (ev.status === 'node' && ev.payload) {
            pipelineNodes = [...pipelineNodes, ev.payload]
            return
          }
          pipelineLog = [...pipelineLog, ev]
        },
        genAbort.signal,
      )
      const result = done && done.result
      const failed = !!(done && done.error)
      const wf = result && result.compile && result.compile.workflow
      // "Partial draft" only means something if something was actually built.
      // When the pipeline dies in its first phase the draft is an empty husk:
      // no steps, no strategy, no name. Putting that on the canvas presented a
      // failure as a workflow to repair.
      const substantive = !!(wf && (((wf.flow && wf.flow.nodes) || []).length > 0 || wf.strategy))

      if (failed && !substantive) {
        // Nothing was built. Keep the user where they are, with their prompt
        // intact, and say why — rather than handing them an empty draft.
        const why = done.error
        compileError = why
        promptError = why
      } else if (wf) {
        // Adopt the draft whenever there IS one, error or not: a failed
        // generation must preserve the partial work rather than discard it.
        // The blocker dialog stays shut on the error path — the error is the
        // headline, not the contract.
        applyCompile(result.compile, { gate: !failed })
        if (result.refinement) rawPrompt = result.refinement.original || rawPrompt
        if (failed) {
          compileError = `${done.error} — the partial draft is on the canvas.`
        } else if (done && done.blocked) {
          compileError = done.blocked_reason || 'This draft has unresolved blockers.'
        }
      } else if (done && done.blocked) {
        compileError = done.blocked_reason || 'This draft has unresolved blockers.'
      }
    } catch (e) {
      if (genAbort && genAbort.signal.aborted) {
        pipelineCancelled = true
      } else {
        compileError = (e && e.message) || 'streamed generate failed'
      }
    } finally {
      clearInterval(timer)
      genAbort = null
      pipelineRunning = false
      buildUXOverride = null // consume the override
    }
  }

  // Stop a generation. Aborting closes the SSE connection, which the server
  // detects on its next flush and uses to cancel the pipeline — so this stops
  // the work rather than just hiding it.
  function cancelGenerate() {
    if (genAbort) genAbort.abort()
  }

  // Router — invoked from the Generate button. Delegates to the streamed or
  // wizard path based on the effective UX preference.
  function generateOrStream() {
    if (effectiveBuildUX === 'streamed') return generateStreamed()
    return generate()
  }

  // Fetch builder-model strength advice so we can warn before generation.
  async function refreshModelAdvice() {
    try { modelAdvice = await bridge.modelAdvice() } catch (_) { modelAdvice = null }
  }

  // Two different models are in play and conflating them is the whole bug this
  // picker's `target` exists to prevent:
  //   'studio' — the BUILDER model, the one that writes the agent (llm.studio).
  //   'agent'  — the RUNTIME model, the one the saved agent executes on. That is
  //              what lands in SOUL.yaml, and until now Studio gave you no way
  //              to choose it: you got the workspace default or nothing.
  async function openModelPicker() {
    modelPicker = { open: true, target: 'studio', provider: '', defaultProvider: '', model: '', models: [], saving: false, error: '' }
    resetModelChooser()
    try {
      const cfg = await bridge.getConfig()
      const st = (cfg && cfg.llm && cfg.llm.studio) || {}
      modelPicker = { ...modelPicker, provider: st.provider || '', defaultProvider: (cfg && cfg.llm && cfg.llm.default_provider) || '', model: st.model || '' }
      if (st.provider) loadModelsFor(st.provider)
    } catch (e) {
      modelPicker = { ...modelPicker, error: e.message || 'could not load config' }
    }
  }

  // Choose what THIS agent runs on. Seeded from the draft when it already pins
  // something, otherwise from the workspace default so the dialog opens showing
  // what would actually happen rather than an empty box.
  async function openAgentModelPicker() {
    const cur = (workflow && workflow.llm) || {}
    modelPicker = { open: true, target: 'agent', provider: cur.provider || '', defaultProvider: '', model: cur.model || '', models: [], saving: false, error: '' }
    resetModelChooser()
    try {
      if (!cur.provider) {
        const cfg = await bridge.getConfig()
        const def = (cfg && cfg.llm && cfg.llm.default_provider) || ''
        modelPicker = { ...modelPicker, provider: def }
      }
      if (modelPicker.provider) loadModelsFor(modelPicker.provider)
    } catch (e) {
      modelPicker = { ...modelPicker, error: e.message || 'could not load config' }
    }
  }

  async function loadModelsFor(provider) {
    if (!provider) { modelPicker = { ...modelPicker, models: [], modelsLoading: false }; return }
    modelPicker = { ...modelPicker, models: [], modelsLoading: true }
    try {
      const r = await bridge.providerModels(provider)
      const raw = (r && (r.models || r)) || []
      const models = Array.isArray(raw)
        ? raw.map((m) => (typeof m === 'string' ? m : (m.id || m.name || ''))).filter(Boolean)
        : []
      modelPicker = { ...modelPicker, models, modelsLoading: false }
    } catch (_) {
      modelPicker = { ...modelPicker, models: [], modelsLoading: false }
    }
  }

  function pickProvider(p) {
    // Clear the previous provider's models immediately — leaving them on screen
    // (and clickable) while the new list loads offers choices that do not exist.
    modelPicker = { ...modelPicker, provider: p, model: '', models: [], modelsLoading: !!p }
    resetModelChooser()
    loadModelsFor(p)
  }

  // ── model chooser ────────────────────────────────────────────────────────
  // Two earlier attempts got this wrong, both for the same underlying reason:
  // they tried to show the model list in a layer floating ABOVE the field.
  //
  //   1. <input list> + <datalist> — the browser filters a datalist against the
  //      input's current value, so once a model was chosen the list could only
  //      offer that same model back. You had to clear the field to see anything.
  //   2. A hand-rolled absolute dropdown — the modal is `overflow-y: auto`, so
  //      the dropdown was clipped by it, and it covered the Save button.
  //
  // A modal is not a page: there is no room to float things over. So the list
  // is now part of the modal's flow — it takes space instead of stealing it.
  // Nothing can clip it and nothing can hide the buttons.
  let modelFilter = ''   // filters the list; NOT the chosen value
  let manualModel = false

  // Reset per-open so the list never opens pre-filtered from a previous visit.
  function resetModelChooser() {
    modelFilter = ''
    manualModel = false
  }

  $: modelChoices = (() => {
    const all = modelPicker.models || []
    const q = modelFilter.trim().toLowerCase()
    if (!q) return all
    return all.filter((m) => m.toLowerCase().includes(q))
  })()

  function chooseModel(m) {
    modelPicker = { ...modelPicker, model: m }
  }

  // The draft's own provider/model, for the toolbar chip. Blank until the draft
  // pins one — "workspace default" is the honest label for that, not a guess at
  // which model the default happens to resolve to right now.
  $: agentModelLabel = (() => {
    const llm = (workflow && workflow.llm) || {}
    if (!llm.provider && !llm.model) return 'workspace default'
    return fmtModelLabel(llm.provider, llm.model)
  })()

  // Write the choice onto the draft. Reassignment (not mutation) so Svelte sees
  // it and the canvas/YAML views update with it.
  function saveAgentModel() {
    const llm = { ...((workflow && workflow.llm) || {}), provider: modelPicker.provider, model: modelPicker.model }
    workflow = { ...workflow, llm }
    modelPicker = { ...modelPicker, open: false, saving: false }
    toast(`This agent will run on ${fmtModelLabel(llm.provider, llm.model)}.`)
  }

  async function saveStudioModel() {
    if (modelPicker.target === 'agent') return saveAgentModel()
    modelPicker = { ...modelPicker, saving: true, error: '' }
    try {
      await bridge.setStudioModel(modelPicker.provider, modelPicker.model)
      studioModelLabel = fmtModelLabel(modelPicker.provider, modelPicker.model)
      modelPicker = { ...modelPicker, open: false, saving: false }
      toast('Studio model updated.')
      refreshModelAdvice()
    } catch (e) {
      modelPicker = { ...modelPicker, saving: false, error: e.message || 'could not save' }
    }
  }

  onMount(loadCatalog)
  onMount(loadSecrets)
  onMount(refreshPaletteDrafts)

  // ── Persist the working session across screen switches ─────────────────────
  // App.svelte mounts/destroys page components on navigation, so without this
  // the intent, the generated/refined workflow, and the transparency panels are
  // lost when you leave Studio and come back. We snapshot into a module-level
  // store on unmount and restore on mount.
  // Unsaved work carried across a navigation, held UNAPPLIED until the user
  // picks it. Re-entering Studio is a "start something / pick something" action,
  // so the home screen must present exactly two choices — describe something
  // new, or continue existing work — and not silently repopulate the prompt
  // boxes with text from a previous visit.
  let carriedSession = null

  function hydrateSession() {
    const s = get(studioSession)
    if (!s) return
    carriedSession = s
    if (studioResumeRequested(location.hash)) {
      resumeCarriedSession(s.step || STEP_BUILD)
      history.replaceState({}, '', '#studio')
    }
  }

  // The user chose "pick up where you left off": now apply it.
  function resumeCarriedSession(returnStep = STEP_BUILD) {
    const s = carriedSession
    if (!s) return
    carriedSession = null
    // keepStep is essential, not cosmetic: setWorkflow ends with
    // advanceToStep(STEP_BUILD), so restoring a draft would jump straight into
    // Build regardless of anything decided here. Removing the resume-step
    // calculation below was not enough on its own — this was the jump that
    // actually put the user back inside the agent.
    if (s.workflow) setWorkflow(s.workflow, { keepStep: true })
    // setWorkflow runs resetTransientDraftState, which clears loadedAgentId, so
    // this must come AFTER it. Without carrying the id, a restored canvas showed
    // an agent with no identity: deleting that agent left it on screen because
    // clearCanvasIfShowing compares ids and found none, and the next Save would
    // have created a duplicate instead of updating the agent being edited.
    if (s.loadedAgentId) loadedAgentId = s.loadedAgentId
    if (typeof s.intent === 'string') intent = s.intent
    if (typeof s.rawPrompt === 'string') rawPrompt = s.rawPrompt
    if (Array.isArray(s.notes)) notes = s.notes
    if (Array.isArray(s.questions)) questions = s.questions
    if (Array.isArray(s.suggestions)) suggestions = s.suggestions
    if (s.explanation) explanation = s.explanation
    if (s.refinement) { refinement = s.refinement; refineAnswers = s.refineAnswers || {} }
    // Resuming IS the deliberate act of going back into the work, so this one
    // path may land on Build. Arriving in Studio never does.
    if (workflow) advanceToStep(returnStep)
  }

  // A carried session describing an agent that has since been deleted must go
  // with it, exactly as the stored snapshot does.
  function forgetCarriedAgent(id) {
    carriedSession = sessionAfterDelete(carriedSession, id)
  }

  // Label for the resume row. The draft may be an unsaved template/import with
  // no name, so fall back rather than render an empty button.
  $: carriedName = (carriedSession && carriedSession.workflow
    && (carriedSession.workflow.name || '').trim()) || 'Untitled draft'
  onMount(hydrateSession)

  function hydrateRouteIntent() {
    const h = location.hash || ''
    const idx = h.indexOf('?')
    if (idx < 0) return
    const params = new URLSearchParams(h.slice(idx + 1))
    const routeIntent = (params.get('intent') || '').trim()
    if (!routeIntent) return
    intent = routeIntent
    rawPrompt = routeIntent
    refinement = null
    questions = []
    answers = {}
    notes = []
    explanation = null
    compileError = ''
    promptViewer = true
    viewMode = 'canvas'
    codeYaml = ''
    codeOrig = ''
    codeValidation = null
    codeWarnings = []
    setWorkflow(null)
    history.replaceState({}, '', '#studio')
  }
  onMount(hydrateRouteIntent)

  onMount(() => {
    const pending = get(studioDebugRun)
    if (!pending || !pending.agentId || !pending.sessionId) return
    studioDebugRun.set(null)
    loadedAgentId = pending.agentId
    showTests = true
    showFailedRuns = true
    healResult = {
      agentId: pending.agentId,
      sessionId: pending.sessionId,
      error: pending.error || '',
      changed: false,
      pending: true,
    }
    toast(`Debugging ${pending.agentId} from Runs…`)
    // Story 3 AC2c — surface the structured runTrace panel (per-block input/
    // output/duration/error, plus run history picker) alongside the debug
    // flow. Without this the operator only saw the compacted evidence blob
    // inside healResult; loadRunTrace hydrates the same runTrace panel Studio
    // shows for any saved agent so debugging a failed run reuses the exact
    // "where did this run actually go wrong" UI operators already know.
    loadRunTrace(pending.sessionId).catch(() => {})
    healActivityRun(pending.agentId, pending.sessionId)
  })

  onDestroy(() => {
    // Carried work the user never picked up survives another navigation
    // untouched. Without this, snapshotSession sees an empty canvas, returns
    // null, and the unsaved draft is gone on the second visit.
    if (!workflow && carriedSession) {
      studioSession.set(carriedSession)
      return
    }
    // snapshotSession owns the rules (and their tests): it returns null for a
    // committed draft or an empty canvas, so navigating away cannot resurrect
    // something the user just saved, cleared, or deleted.
    studioSession.set(snapshotSession({
      committed: sessionCommitted,
      workflow,
      intent,
      rawPrompt,
      loadedAgentId,
      step,
      notes,
      questions,
      suggestions,
      explanation,
      refinement,
      refineAnswers,
    }))
  })
</script>

<svelte:window on:keydown={(e) => { if (e.key === 'Escape' && promptViewer) promptViewer = false }} />

<div id="studio-app">
  <!-- Studio masthead. Keep one obvious primary action and place infrequent
       authoring utilities behind a compact menu so the creation path remains
       readable even as Studio gains capabilities. -->
  <header class="topbar">
    <div class="studio-heading">
      <span class="studio-kicker">Agent builder</span>
      <h1>Studio</h1>
      <p>Design, refine, test, and deploy intelligent agents.</p>
    </div>
    <div class="studio-header-actions">
      <button class="studio-model-select" type="button" on:click={openModelPicker}
        aria-label={`Choose Studio builder LLM. Current model: ${studioModelLabel}`}
        data-tooltip="Choose the registered provider and model Studio uses to refine and generate agents">
        <span>Builder LLM</span>
        <strong>{studioModelLabel}</strong>
        <span class="studio-model-caret" aria-hidden="true">⌄</span>
      </button>
      {#if workflow}
        <button class="studio-model-select" type="button" on:click={openAgentModelPicker}
          aria-label={`Choose agent runtime LLM. Current model: ${agentModelLabel}`}
          data-tooltip="Choose the registered provider and model this agent uses when it runs">
          <span>Agent LLM</span>
          <strong>{agentModelLabel}</strong>
          <span class="studio-model-caret" aria-hidden="true">⌄</span>
        </button>
      {/if}
      <button class="btn primary studio-new" type="button" on:click={newWorkflow}>+ New workflow</button>
      <details class="studio-more">
        <summary class="btn studio-more-trigger" aria-label="More Studio actions">More</summary>
        <!-- M6: draft management toolbar. These remain fully available without
             competing with the two actions that advance the current task. -->
        <div class="toolbar" role="group" aria-label="Draft management">
      <!-- "+ New agent" and "My Workflows" removed: starting something new IS
           the Describe step's prompt box, and continuing existing work is the
           list beside it. Two toolbar buttons doing what step 1 already does
           made the wizard look like a veneer over the old screen. -->
      <button class="btn" type="button" on:click={openLibrary}
              data-tooltip="Open another agent, workflow or draft">
        Open workflows{#if libCount}<span class="toolbar-count">{libCount}</span>{/if}
      </button>
      <button class="btn" type="button" on:click={openRules} data-tooltip="Edit the SOUL.yaml authoring rules used when generating, validating, and fixing">📋 Rules</button>
      <button class="btn" type="button" on:click={openTemplates} data-tooltip="Start from a template">Templates</button>
      <button class="btn" type="button" on:click={openYamlBrowser} data-tooltip="View the raw SOUL.yaml of any agent (read-only)">Browse SOUL.yaml</button>
      <button class="btn" type="button" on:click={saveDraft} disabled={!workflow || savingDraft} data-tooltip="Save the current draft to the library">
        {savingDraft ? 'Saving…' : 'Save draft'}
      </button>
      <button class="btn" type="button" on:click={exportDraft} disabled={!workflow} data-tooltip="Download the current draft as a .studio.json file">Export</button>
      <button class="btn" type="button" on:click={triggerImport} data-tooltip="Load a .studio.json file from disk">Import</button>
      <button
        class="btn"
        type="button"
        data-tooltip={effectiveBuildUX === 'streamed' ? 'Streamed generation is active. Click to use the stepped Wizard once.' : 'The stepped Wizard is active. Click to use Streamed generation once.'}
        on:click={() => (buildUXOverride = effectiveBuildUX === 'streamed' ? 'wizard' : 'streamed')}
      >
        {effectiveBuildUX === 'streamed' ? '⚡ Streamed build' : '🪄 Wizard build'}
        {#if buildUXOverride}<span class="toolbar-once">(once)</span>{/if}
      </button>
      <TourButton />
      <input
        bind:this={fileInputEl}
        class="hidden-file"
        type="file"
        accept=".json,application/json"
        on:change={onImportFile}
        aria-hidden="true"
        tabindex="-1"
      />
        </div>
      </details>
    </div>

  </header>

  <!-- Step rail: Describe › Build › Test › Save. Sits under the header so the
       order of operations is visible at all times — previously Studio was a
       canvas with tabs, and nothing conveyed that you had, say, never tested
       the thing you were about to deploy. -->
  <div class="steprail">
    <!-- Which agent is being edited. Past Describe the prompt box scrolls out of
         reach and every later step looked identical regardless of what was open
         — with several drafts on the go there was nothing on screen saying which
         one you were about to change. -->
    {#if workflow}
      <div class="steprail-agent" title={workflow.name || 'Untitled agent'}>
        <span class="steprail-agent-name">{workflow.name || 'Untitled agent'}</span>
        {#if loadedAgentId}
          <span class="agent-badge on">saved</span>
        {:else}
          <span class="agent-badge off">unsaved</span>
        {/if}
      </div>
      <span class="steprail-sep" aria-hidden="true">·</span>
    {/if}
    <WizardRail steps={wizardSteps} onGo={goStep} />
    <div class="steprail-right">
      {#if workflow && step !== STEP_DESCRIBE}
        <button class="btn prompt-return" type="button" on:click={editPromptForRegeneration}
          data-tooltip="Return to the original and refined prompts. Your current draft stays intact until you confirm regeneration.">
          ← Edit prompt &amp; regenerate
        </button>
      {/if}
      {#if step === STEP_SAVE && saveBlocked}
        <span class="steprail-block" title={saveBlocked}>⚠ {saveBlocked}</span>
      {/if}
      <!-- "Open…" and "Model:" used to sit here. Both moved out: opening other
           work is a toolbar action like every other navigation control, and the
           model button duplicated the toolbar's "Builds with:" chip, which says
           the same thing with the provider spelled out. Two controls for one
           setting invites the reader to wonder which one is authoritative. -->
    </div>
  </div>

  <!-- M6: import error + toast strips -->
  {#if importError}
    <div class="strip strip-error">⚠ {importError}</div>
  {/if}
  {#if toastMsg}
    <div class="strip strip-ok toast-strip">✓ {toastMsg}</div>
  {/if}
  <main class="body" class:max-canvas={maximizedFrame === 'canvas'} class:max-bench={maximizedFrame === 'bench'} class:max-inspector={maximizedFrame === 'inspector'}
        class:full-width={step === STEP_DESCRIBE || step === STEP_SAVE}>
    <!-- The block palette belongs to Build. Showing it on Describe or Save
         offers drag-and-drop onto a canvas that is not on screen. -->
    {#if step === STEP_BUILD}
    <Palette
      {catalog}
      status={paletteStatus}
      statusKind={paletteStatusKind}
      error={paletteError}
      onBrowse={browseRegistry}
      {browse}
      onInstall={installBrowse}
      onOpenAgent={openAgentOnCanvas}
      onDeleteAgent={deleteAgentFromPalette}
      drafts={paletteDrafts}
      onOpenDraft={openDraftById}
      onDeleteDraft={deleteDraftFromPalette}
      busyId={library.busyId}
    />
    {/if}

    <!-- Center: step-driven. Describe and Save own the full pane; Build and Test
         share the canvas layout below, with Build owning the graph and Test
         owning the bench. -->
    <section class="center">
      {#if step === STEP_DESCRIBE}
        <div class="describe-step">
          <div class="describe-cols">
            <div class="describe-left">
              <section class="studio-card prompt-card">
                <div class="studio-card-head">
                  <div>
                    <span class="studio-card-kicker">Step 1</span>
                    <h2>Your prompt</h2>
                  </div>
                  <button class="icon-btn" type="button" on:click={() => (promptViewer = true)} aria-label="Open full prompt editor" data-tooltip="Open full prompt editor">⤢</button>
                </div>
                <p class="studio-card-copy">Describe the outcome in plain language. Include timing and delivery only when they matter.</p>
                {#if promptError}<div class="strip strip-error">⚠ {promptError}</div>{/if}
                <label class="sr-only" for="d-raw">Your prompt</label>
                <textarea id="d-raw" class="pe-area" bind:value={rawPrompt}
                  placeholder="For example: Watch incoming support requests, summarize urgent issues, and notify the on-call Slack channel."></textarea>
                <div class="studio-card-actions">
                  <button class="text-btn" type="button" disabled={!rawPrompt} on:click={() => (rawPrompt = '')}>Clear</button>
                  <button class="btn primary" type="button" disabled={modalRefining || refining || !effectiveIntent} on:click={refineFromModal}>
                    {modalRefining || refining ? 'Refining…' : '✦ Refine prompt'}
                  </button>
                </div>
              </section>

              <section class="studio-card refined-card">
                <div class="studio-card-head">
                  <div>
                    <span class="studio-card-kicker ready">Build-ready</span>
                    <h2>Refined prompt</h2>
                  </div>
                  <span class="refined-status" class:active={!!intent} aria-label={intent ? 'Refined prompt ready' : 'Waiting for refined prompt'}>{intent ? '✓' : '…'}</span>
                </div>
                <p class="studio-card-copy">Review the detailed specification Studio will use. You can edit it directly.</p>
                <label class="sr-only" for="d-refined">Refined prompt</label>
                <textarea id="d-refined" class="pe-area" bind:value={intent}
                  placeholder="Your refined, build-ready prompt will appear here."></textarea>
                <div class="studio-card-actions">
                  <span class="action-hint">Trigger and delivery stay under your control below.</span>
                  <button class="btn primary" type="button" on:click={generateOrStream}
                    disabled={compiling || refining || pipelineRunning || !!refinement || !effectiveIntent || specUnresolved.length > 0 || !!generationTriggerError}
                    title={!effectiveIntent
                      ? 'Describe what you want built first'
                      : specUnresolved.length
                        ? `Answer ${specUnresolved.length} required detail${specUnresolved.length === 1 ? '' : 's'} first`
                        : generationTriggerError}>
                    {compiling ? 'Generating…' : pipelineRunning ? 'Running pipeline…' : 'Generate workflow →'}
                  </button>
                </div>
              </section>

              <!-- Describing something new is one answer to "what do you want to
                   do"; continuing something you already built is the other, and
                   it was unreachable from here. Shown inline rather than behind
                   the My Workflows dialog, because a user should not have to
                   open a dialog to discover their own agents exist. -->
              {#if carriedSession || libParts.deployed.length || libParts.saved.length || libParts.drafts.length}
                <aside class="d-existing studio-card">
                  <div class="d-existing-head">
                    <div>
                      <span class="studio-card-kicker">Your workspace</span>
                      <h2>Recent work</h2>
                    </div>
                    <button class="btn btn-sm" type="button" on:click={openLibrary}>See all</button>
                  </div>
                  <ul class="d-existing-list">
                    <!-- Work carried across a navigation. It is offered here as a
                         CHOICE rather than restored into the prompt boxes: a
                         prompt that reappears on its own reads as Studio showing
                         a random agent, and it used to describe whatever was
                         last open rather than anything the user asked for. -->
                    {#if carriedSession}
                      <li>
                        <button class="d-existing-item" type="button" on:click={() => resumeCarriedSession()}>
                          <span class="d-existing-name">
                            {carriedName}
                            <span class="agent-badge off">in progress</span>
                          </span>
                          <span class="d-existing-sub">Unsaved work from your last visit — pick up where you left off</span>
                        </button>
                      </li>
                    {/if}
                    {#each [...libParts.deployed, ...libParts.saved].slice(0, 5) as a (a.id)}
                      <li>
                        <button class="d-existing-item" type="button" disabled={!!library.busyId}
                          on:click={() => loadAgentForEdit(a)}>
                          <span class="d-existing-name">
                            {a.name || a.id}
                            <span class="agent-badge {a.enabled ? 'on' : 'off'}">{a.enabled ? 'deployed' : 'saved'}</span>
                          </span>
                          <!-- A reasoning agent has no steps to count, so
                               reporting "0 steps" would read as broken rather
                               than as a different kind of thing. -->
                          <span class="d-existing-sub">
                            {a.description || (a.strategy
                              ? `${recoAgentLabel(a.strategy)} · ${a.trigger || 'manual'}`
                              : `${a.trigger || 'manual'} · ${a.nodes || 0} steps`)}
                          </span>
                        </button>
                      </li>
                    {/each}
                    {#each libParts.drafts.slice(0, 3) as d (d.id)}
                      <li>
                        <button class="d-existing-item" type="button" disabled={!!library.busyId}
                          on:click={() => loadDraft(d)}>
                          <span class="d-existing-name">
                            {d.name || d.id}
                            <span class="agent-badge off">draft</span>
                          </span>
                          {#if draftWhen(d)}<span class="d-existing-sub">updated {draftWhen(d)}</span>{/if}
                        </button>
                      </li>
                    {/each}
                  </ul>
                </aside>
              {:else}
                <aside class="d-existing studio-card">
                  <div class="d-existing-head">
                    <div>
                      <span class="studio-card-kicker">Your workspace</span>
                      <h2>Recent work</h2>
                    </div>
                  </div>
                  <div class="recent-empty">
                    <span aria-hidden="true">✦</span>
                    <strong>Your workflows will appear here</strong>
                    <p>Start with a prompt, then save the result to pick it up later.</p>
                  </div>
                </aside>
              {/if}
            </div>
            <div class="describe-right studio-card settings-card">
              <BuildSpecPanel
                spec={effectiveBuildSpec}
                recommendation={specRecommendation}
                loading={buildSpecLoading}
                error={buildSpecError}
                answers={refineAnswers}
                channels={catalog && catalog.channels}
                {generationTrigger}
                {generationTriggerError}
                onAnswer={(id, v) => (refineAnswers = { ...refineAnswers, [id]: v })}
                onGenerationTrigger={(value) => (generationTrigger = value)}
                onRefine={refineFromModal}
                onGenerate={generateOrStream}
                refining={modalRefining || refining}
                generating={compiling || pipelineRunning}
                showActions={false}
              />
            </div>
          </div>
        </div>

      {:else if step === STEP_SAVE}
        <div class="save-step">
          {#if !workflow}
            <!-- canEnter() should prevent this, but the binding below writes to
                 workflow.name and must never be reachable with a null draft. -->
            <p class="step-sub">There is nothing to save yet. Describe and generate a workflow first.</p>
          {:else}
          <div class="save-cols">
            <div class="save-main">
              <h3 class="step-h">Workflow summary</h3>
              <p class="step-sub">{workflow ? (workflow.name || 'Untitled') : ''}</p>
              {#if explanation && explanation.purpose}
                <p class="save-purpose">{explanation.purpose}</p>
              {/if}
              <ReadinessPanel
                report={readiness}
                loading={readinessLoading}
                error={readinessError}
                busy={saving}
                destinations={readinessDestinationOptions}
                onRecheck={loadReadiness}
                onAction={readinessAction}
                onReveal={(id) => { goStep(STEP_BUILD); revealNode(id) }}
                onDestination={chooseReadinessDestination}
              />
            </div>

            <aside class="save-side">
              <h4 class="step-h">Agent details</h4>
              <label class="save-field">
                <span>Agent name</span>
                <input type="text" bind:value={workflow.name} placeholder="Name this agent"
                       data-agent-name-field />
              </label>

              <div class="save-note">
                <strong>New agents are saved disabled</strong>
                <span>A new agent is staged so you review and deploy it explicitly — from My workflows. Editing an agent you have already enabled leaves it enabled, so a fix does not silently stop its schedule.</span>
              </div>

              {#if deliveryModeOf(workflow) === 'reply'}
                <div class="save-field">
                  <span>Response delivery</span>
                  <div class="save-chips"><span class="sp-chip">Invocation route</span></div>
                </div>
              {:else if deliveryModeOf(workflow) === 'none'}
                <div class="save-field">
                  <span>Response delivery</span>
                  <div class="save-chips"><span class="sp-chip">Direct caller only</span></div>
                </div>
              {:else if (workflow.channels || []).length}
                <div class="save-field">
                  <span>Output channels</span>
                  <div class="save-chips">
                    {#each workflow.channels as ch}<span class="sp-chip">{ch}</span>{/each}
                  </div>
                </div>
              {/if}

              {#if saveError}<div class="strip strip-error">⚠ {saveError}</div>{/if}
              {#if saveMsg}<div class="strip">{saveMsg}</div>{/if}

              <button class="btn primary save-go" type="button"
                disabled={saving || !!saveBlocked}
                data-tooltip={saveBlocked}
                on:click={() => save()}>
                {saving ? 'Saving…' : 'Save agent'}
              </button>
              {#if saveBlocked}<p class="save-blocked">{saveBlocked}</p>{/if}

              <button class="btn" type="button" on:click={() => goStep(STEP_BUILD)}>Back to Build</button>
            </aside>
          </div>
          {/if}
        </div>

      {:else}
      {#if workflow}
        <div class="toolbar-row">
          <div class="view-switch" role="tablist" aria-label="Editor view">
            {#if currentMode === 'workflow'}
              <button class="vs-btn" class:active={viewMode === 'plan'} type="button"
                      role="tab" aria-selected={viewMode === 'plan'} on:click={showPlanView}
                      data-tooltip="Simple plan view — Trigger, Work Plan, Delivery lanes">📋 Plan</button>
            {/if}
            <button class="vs-btn" class:active={viewMode === 'canvas'} type="button"
                    role="tab" aria-selected={viewMode === 'canvas'} on:click={showCanvasView}
                    data-tooltip="Advanced view — the full graph you can edit node by node">{currentMode === 'workflow' ? '⬚ Canvas' : '🧠 Agent'}</button>
            <button class="vs-btn" class:active={viewMode === 'code'} type="button"
                    role="tab" aria-selected={viewMode === 'code'} on:click={showCodeView}>{'</> SOUL.yaml'}</button>
            {#if viewMode === 'code' && codeYaml !== codeOrig}<span class="vs-dirty" data-tooltip="Unsaved YAML edits">●</span>{/if}
          </div>
          <!-- Execution-mode override: a fixed workflow, or an agent. -->
          <div class="mode-switch" role="group" aria-label="Execution mode"
            data-tooltip="How this agent runs. Workflow = a fixed graph. Auto = recommended native tool-calling. Plan-Execute = long multi-step planning. ReAct is an advanced manual override. Switching re-generates from your prompt.">
            <span class="ms-label">mode</span>
            <button class="ms-btn" class:active={currentMode === 'workflow'} type="button"
                    disabled={compiling} on:click={() => switchMode('workflow')}>Workflow</button>
            <button class="ms-btn" class:active={currentMode === 'auto'} type="button"
                    disabled={compiling || isStrategyUnreliable('auto')} on:click={() => switchMode('auto')}>Auto {isStrategyUnreliable('auto') ? '⚠' : ''}</button>
            <button class="ms-btn" class:active={currentMode === 'react'} type="button"
                    disabled={compiling || isStrategyUnreliable('react')} on:click={() => switchMode('react')}>ReAct (advanced) {isStrategyUnreliable('react') ? '⚠' : ''}</button>
            <button class="ms-btn" class:active={currentMode === 'plan_execute'} type="button"
                    disabled={compiling || isStrategyUnreliable('plan_execute')} on:click={() => switchMode('plan_execute')}>Plan-Execute {isStrategyUnreliable('plan_execute') ? '⚠' : ''}</button>
          </div>
        </div>
      {/if}

      {#if needsDelivery && viewMode === 'canvas'}
        <div class="strip strip-delivery" data-tooltip="Where this agent's result is sent">
          <span class="strip-label">Deliver to</span>
          <span class="dlv-hint">This {currentMode === 'workflow' ? 'workflow' : 'agent'} produces a result but has no delivery channel. Send it to:</span>
          {#each channelOptions as ch}
            <button class="dlv-btn" type="button" on:click={() => addDeliveryChannel(ch.id)}>+ {ch.name}</button>
          {/each}
        </div>
      {/if}

      {#if workflow && unsetSecrets.length && viewMode === 'canvas'}
        <Collapsible id="credentials" title="Credentials" sub={`${unsetSecrets.length} key${unsetSecrets.length === 1 ? '' : 's'} not set — your tools/MCP may need them`} open={false}>
          <div class="creds">
            <p class="creds-hint">Set the API keys your tools and MCP servers need, without leaving Studio. Values are stored in the gateway's secret store, never in the agent file.</p>
            {#each unsetSecrets as sec (sec.name)}
              <div class="cred-row">
                <div class="cred-meta">
                  <span class="cred-name">{sec.name}</span>
                  {#if sec.env_var}<code class="cred-env">{sec.env_var}</code>{/if}
                  {#if sec.description}<span class="cred-desc">{sec.description}</span>{/if}
                </div>
                <div class="cred-set">
                  <input class="cred-input" type="password" placeholder="paste value…"
                    bind:value={secretVals[sec.name]}
                    on:keydown={(e) => { if (e.key === 'Enter') setSecretVal(sec.name) }} />
                  <button class="btn btn-sm" type="button" disabled={secretBusy === sec.name || !(secretVals[sec.name] || '').trim()}
                    on:click={() => setSecretVal(sec.name)}>{secretBusy === sec.name ? 'Saving…' : 'Set'}</button>
                </div>
              </div>
            {/each}
            {#if secretMsg}<div class="creds-msg">{secretMsg}</div>{/if}
          </div>
        </Collapsible>
      {/if}

      {#if codeWarnings.length}
        <div class="strip strip-notes" data-tooltip="Things the canvas can't show — they stay in the YAML">
          <span class="strip-label">Kept in YAML</span>
          {#each codeWarnings as w}<span class="note">{w}</span>{/each}
        </div>
      {/if}
      {#if modelAdvice && (modelAdvice.severity === 'block' || modelAdvice.local_complexity_note || modelAdvice.cloud_escalation)}
        <div class="strip strip-modeladvice" class:strip-local={modelAdvice.local} data-tooltip="Builder model">
          <span class="strip-label">
            {#if modelAdvice.severity === 'block'}Pick a builder model
            {:else if modelAdvice.local}🔒 Local-first
            {:else}☁ Cloud builder{/if}
          </span>
          <span>{modelAdvice.local_complexity_note || modelAdvice.message}</span>
          {#if modelAdvice.cloud_escalation}
            <button class="btn ma-btn" type="button" on:click={openModelPicker}>Switch to local</button>
          {:else if modelAdvice.severity === 'block'}
            <button class="btn ma-btn" type="button" on:click={openModelPicker}>Choose model</button>
          {:else if modelAdvice.frontier_available}
            <span class="ma-rec">Optional: use {modelAdvice.frontier_provider} for complex builds —</span>
            <button class="btn ma-btn" type="button" on:click={openModelPicker}>Use {modelAdvice.frontier_provider}</button>
          {/if}
        </div>
      {/if}
      {#if generationProfile}
        <div class="strip strip-modeladvice" class:strip-local={generationProfile.local} data-tooltip="Studio generation guardrails">
          <span class="strip-label">
            {#if generationProfile.local}Local guardrails{:else}Builder guardrails{/if}
          </span>
          <span>
            {generationProfile.provider}/{generationProfile.model}
            {#if generationProfile.compact} · compact local mode{/if}
            {#if generationProfile.plan_matched} · deterministic plan{/if}
            {#if generationProfile.pattern_matched} · proven pattern{/if}
            {#if generationProfile.lessons_applied} · {generationProfile.lessons_applied} lesson{generationProfile.lessons_applied === 1 ? '' : 's'}{/if}
            · confidence {generationProfile.confidence || 'medium'}
          </span>
          {#if generationProfile.next_action === 'build_verify'}
            <span class="ma-rec">Recommended: run Build until it works before saving.</span>
          {:else if generationProfile.next_action === 'ask_clarify'}
            <span class="ma-rec">Recommended: refine the spec or run Build until it works.</span>
          {/if}
        </div>
      {/if}

      {#if compileError}
        <!-- Generation failures carry a multi-line, actionable fix (which builder
             model failed and how to fix it) — render it readably, not squashed
             onto one line. -->
        <div class="strip strip-error strip-multiline">
          <span class="strip-icon" aria-hidden="true">⚠</span>
          <span class="strip-body">{compileError}</span>
          <button class="strip-x" data-tooltip="Dismiss" on:click={() => (compileError = '')}>×</button>
        </div>
      {/if}

      {#if notes.length}
        <div class="strip strip-notes" data-tooltip="What the compiler inferred">
          <span class="strip-label">Inferred</span>
          {#each (notesExpanded ? notes : notes.slice(0, 3)) as n}<span class="note" title={n}>{n}</span>{/each}
          {#if notes.length > 3}
            <button type="button" class="note note-toggle" on:click={() => (notesExpanded = !notesExpanded)}>
              {notesExpanded ? 'Show less' : `+${notes.length - 3} more`}
            </button>
          {/if}
        </div>
      {/if}

      {#if workflow && workflow.recommendation && workflow.recommendation.mode}
        <div class="strip strip-reco" data-tooltip="Suggested execution model for this agent">
          <span class="strip-label">
            Recommended: {recoLabel(workflow.recommendation.mode)}
            {#if strategyAdvice && strategyAdvice.confidence}
              <span class="conf-badge conf-{strategyAdvice.confidence}">{strategyAdvice.confidence} confidence</span>
            {/if}
          </span>
          <span>{workflow.recommendation.rationale}</span>
          {#if workflow.recommendation.mode !== 'workflow' && currentMode === 'workflow'}
            <button class="btn ma-btn" type="button" disabled={compiling}
              on:click={() => switchMode(workflow.recommendation.mode)}>
              Rebuild as {recoLabel(workflow.recommendation.mode)}
            </button>
          {/if}
        </div>
      {/if}

      <!-- The advisor's capability verdict on the SELECTED model. Shown next to
           the mode controls because that is where the override decision is made:
           "you can switch to ReAct" and "this model can't sustain ReAct" are only
           useful together. -->
      {#if strategyAdvice && strategyAdvice.warning}
        <div class="strip strip-capwarn" role="status">
          <span class="strip-label">Model capability</span>
          <span>{strategyAdvice.warning}</span>
          {#if strategyAdvice.capabilities}
            <details class="cap-details">
              <summary>What this model supports</summary>
              <ul class="cap-list">
                <li>Native tool calling: {strategyAdvice.capabilities.native_tools ? 'yes' : 'no'}</li>
                <li>Structured output: {strategyAdvice.capabilities.structured_output ? 'yes' : 'no'}</li>
                {#if strategyAdvice.capabilities.arg_accuracy != null}
                  <li>Argument accuracy: {strategyAdvice.capabilities.arg_accuracy}</li>
                {/if}
                {#if strategyAdvice.capabilities.plan_reliability != null}
                  <li>Planning reliability: {strategyAdvice.capabilities.plan_reliability}</li>
                {/if}
                {#if strategyAdvice.capabilities.context_tokens}
                  <li>Context window: {strategyAdvice.capabilities.context_tokens.toLocaleString()} tokens</li>
                {/if}
                {#if strategyAdvice.capabilities.known === false}
                  <li>This model has no capability profile, so the advisor stayed conservative.</li>
                {/if}
              </ul>
            </details>
          {/if}
        </div>
      {/if}

      {#if explanation}
        <details class="explain-panel" open>
          <summary class="explain-summary">What Studio built — in plain language</summary>
          <div class="explain-body">
            {#if explanation.purpose}<p class="explain-purpose">{explanation.purpose}</p>{/if}
            <div class="explain-meta">
              {#if explanation.trigger}<span><strong>Runs:</strong> {explanation.trigger}</span>{/if}
              {#if explanation.architecture}<span><strong>Type:</strong> {recoLabel(explanation.architecture)}{#if explanation.arch_reason} — {explanation.arch_reason}{/if}</span>{/if}
            </div>
            {#if explanation.steps && explanation.steps.length}
              <div class="explain-heading">Steps</div>
              <ol class="explain-steps">
                {#each explanation.steps as st}<li>{st}</li>{/each}
              </ol>
            {/if}
            <div class="explain-meta">
              {#if explanation.tools && explanation.tools.length}<span><strong>Tools:</strong> {explanation.tools.join(', ')}</span>{/if}
              {#if explanation.agents && explanation.agents.length}<span><strong>Agents:</strong> {explanation.agents.join(', ')}</span>{/if}
              {#if explanation.skills && explanation.skills.length}<span><strong>Skills:</strong> {explanation.skills.join(', ')}</span>{/if}
              {#if explanation.knowledge_bases && explanation.knowledge_bases.length}<span><strong>Knowledge:</strong> {explanation.knowledge_bases.join(', ')}</span>{/if}
              {#if explanation.channels && explanation.channels.length}<span><strong>Delivers to:</strong> {explanation.channels.join(', ')}</span>{/if}
            </div>
            {#if explanation.needs_config && explanation.needs_config.length}
              <div class="explain-heading">Still needs configuration</div>
              <ul class="explain-needs">
                {#each explanation.needs_config as nc}<li>{nc}</li>{/each}
              </ul>
            {/if}
            <div class="explain-actions">
              <button class="btn" type="button" on:click={previewRun} disabled={previewing || testing}>
                {previewing ? 'Previewing…' : 'Preview a run'}
              </button>
              <span class="explain-hint">Dry-runs the steps so you can see what each run does — no tools actually fire.</span>
            </div>
          </div>
        </details>
      {/if}

      <!-- Validation strip (M3): non-blocking ok / N errors / N warnings.
           Hidden in the code view: it judges the CANVAS draft, and the SOUL.yaml
           editor reports on the text actually being edited. Showing both meant a
           broken YAML sat under a green "Valid — No issues found." -->
      {#if workflow && validation && viewMode !== 'code'}
        {#if validation.ok && !validation.warnings.length}
          <div class="strip strip-ok" data-tooltip="Workflow validates">
            <span class="strip-label">Valid</span>
            <span>No issues found.</span>
          </div>
        {:else}
          <div
            class="strip {validation.errors.length ? 'strip-error' : 'strip-warn'}"
            data-tooltip="Validation issues"
          >
            <span class="strip-label">Validation</span>
            {#if validation.errors.length}
              <span class="v-count v-err">{validation.errors.length} error{validation.errors.length === 1 ? '' : 's'}</span>
            {/if}
            {#if validation.warnings.length}
              <span class="v-count v-warn">{validation.warnings.length} warning{validation.warnings.length === 1 ? '' : 's'}</span>
            {/if}
            {#each validation.errors as err}
              <span class="v-msg v-err" data-tooltip={err.nodeId || (err.edgeIndex != null ? 'edge ' + err.edgeIndex : '')}>{err.message}</span>
            {/each}
            {#each validation.warnings as w}
              <span class="v-msg v-warn" data-tooltip={w.nodeId || ''}>{w.message}</span>
            {/each}
          </div>
        {/if}
      {/if}

      <!-- Needs-setup panel (M4): missing capabilities the draft references but
           that are NOT installed. Non-blocking — the draft still renders/tests;
           each item can be discovered + staged via the existing endpoints. -->
      {#if suggestions.length}
        <div class="needs-setup" aria-label="Missing capabilities">
          <div class="ns-head">
            <span class="strip-label">Needs setup</span>
            <span class="ns-sub">These capabilities aren’t installed yet — the draft still works, but won’t run them until you add them.</span>
          </div>
          <ul class="ns-list">
            {#each suggestions as s (s.name)}
              <li class="ns-item">
                <div class="ns-row">
                  <span class="kind-chip kind-{s.kind || 'tool'}">{s.kind || 'tool'}</span>
                  <span class="ns-name">{s.name}</span>
                  {#if s.reason}<span class="ns-reason">{s.reason}</span>{/if}
                  <button
                    class="btn btn-sm"
                    on:click={() => findCapability(s)}
                    disabled={discoverState[s.name] && discoverState[s.name].loading}
                  >
                    {discoverState[s.name] && discoverState[s.name].loading ? 'Finding…' : 'Find'}
                  </button>
                </div>

                {#if discoverState[s.name]}
                  {#if discoverState[s.name].error}
                    <div class="ns-msg ns-err">⚠ {discoverState[s.name].error}</div>
                  {/if}
                  {#if discoverState[s.name].message}
                    <div class="ns-msg ns-ok">{discoverState[s.name].message}</div>
                  {/if}
                  {#if discoverState[s.name].results && discoverState[s.name].results.length}
                    <ul class="ns-results">
                      {#each discoverState[s.name].results as pkg}
                        <li class="ns-result">
                          <div class="nsr-main">
                            <span class="nsr-name">{pkg.slug || pkg.name || '(package)'}</span>
                            {#if pkg.provider}<span class="nsr-src">{pkg.provider}</span>{/if}
                            {#if pkg.version}<span class="nsr-ver">{pkg.version}</span>{/if}
                          </div>
                          {#if pkgDesc(pkg)}<div class="nsr-desc">{pkgDesc(pkg)}</div>{/if}
                          <button
                            class="btn btn-sm primary"
                            on:click={() => installResult(s, pkg)}
                            disabled={discoverState[s.name] && discoverState[s.name].loading}
                            title="Stage this package for install (review & approve in the Plugins page)"
                          >
                            Install
                          </button>
                        </li>
                      {/each}
                    </ul>
                  {/if}
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if viewMode === 'code'}
        <div class="code-view">
          {#if codeError}<div class="strip strip-error">⚠ {codeError}</div>{/if}
          {#if codeLoading}
            <div class="canvas-state">Generating SOUL.yaml…</div>
          {:else}
            <div class="code-editor-wrap">
              <YamlView bind:value={codeYaml} />
            </div>
            {#if codeValidation}
              <div class="code-validation" class:ok={codeValidation.ok}>
                <div class="cv-summary">
                  {#if codeValidation.ok && !codeValidation.warnings}
                    <span class="cv-badge ok">✓ All checks passed</span>
                  {:else}
                    {#if codeValidation.errors}<span class="cv-badge err">{codeValidation.errors} error{codeValidation.errors === 1 ? '' : 's'}</span>{/if}
                    {#if codeValidation.warnings}<span class="cv-badge warn">{codeValidation.warnings} warning{codeValidation.warnings === 1 ? '' : 's'}</span>{/if}
                  {/if}
                  {#if codeValidation.fixes && codeValidation.fixes.length}
                    <button class="btn btn-sm cv-fixbtn" on:click={applyTemplateFixes} disabled={codeValidating || codeFixing}
                            data-tooltip="Instantly rewrite the flagged references to the suggested field">
                      ⚡ Quick-fix {codeValidation.fixes.length}
                    </button>
                  {/if}
                  {#if codeValidation.errors || codeValidation.warnings}
                    <button class="btn btn-sm cv-fixbtn cv-aibtn" on:click={fixWithAI} disabled={codeFixing || codeValidating}
                            data-tooltip="Let the model rewrite the whole SOUL.yaml to fix every issue">
                      {codeFixing ? '✨ Fixing…' : '✨ Fix with AI'}
                    </button>
                  {/if}
                  <button class="icon-btn" data-tooltip="Dismiss" on:click={() => (codeValidation = null)}>✕</button>
                </div>
                {#if codeValidation.items && codeValidation.items.length}
                  <ul class="cv-list">
                    {#each codeValidation.items as it}
                      <li class="cv-item cv-{it.severity}">
                        <span class="cv-dot">{it.severity === 'error' ? '✕' : '!'}</span>
                        <div class="cv-body">
                          <div class="cv-msg">
                            <span class="cv-source">{it.source}</span>
                            {#if it.nodeId}<span class="cv-node">{it.nodeId}</span>{/if}
                            {it.message}
                          </div>
                          {#if it.fix}<div class="cv-fix">→ {it.fix}</div>{/if}
                        </div>
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {/if}
          {/if}
        </div>
      {:else if viewMode === 'plan'}
        <div class="intent-guide">
          <span class="intent-pill">Intent first</span>
          <strong>Review the plan before editing nodes.</strong>
          <span>Confirm when it runs, what work it performs, and where output goes; use Canvas only for advanced rewiring.</span>
        </div>
        <PlanView {workflow} onSelectNode={planSelectNode} onSave={() => save()} onAddPython={addSuggestedPython} onAddStep={addStepFromText} addStepBusy={addStepBusy} addStepMsg={addStepMsg} onUpdateNode={updateNodeConfig} testByNode={stepResultsByNode(testResult)} {saving} />
      {:else}
      <div
        class="canvas"
        role="application"
        aria-label="Workflow canvas — drop palette items here"
        on:dragover={onCanvasDragOver}
        on:drop={onCanvasDrop}
      >
        {#if compiling}
          <div class="canvas-state">Compiling…</div>
        {:else if !workflow}
          <div class="canvas-state empty">
            <div class="glyph" aria-hidden="true">⬚</div>
            <p>Describe what you want above, then press Generate.</p>
            <p class="empty-or">— or —</p>
            <button class="btn primary" type="button" on:click={openTemplates}>Start from a template</button>
          </div>
        {:else if workflow.strategy}
          <!-- ReAct / Plan-Execute AGENT spec (no fixed flow). Editable. -->
          <div class="agent-spec">
            <div class="agent-spec-head">
              <span class="agent-badge on">{recoAgentLabel(workflow.strategy)}</span>
              <span class="agent-spec-name">{workflow.name || 'Untitled agent'}</span>
              <span class="agent-spec-note">Reasons over its tools — no fixed graph. Loops & polls as needed.</span>
              <button class="agent-yaml-link" type="button" on:click={showCodeView}
                data-tooltip="Open the full SOUL.yaml — validate, AI-fix, and edit every field">{'</> Edit SOUL.yaml'}</button>
            </div>

            <!-- The strategy contract: mode, the advisor's verdict on THIS model,
                 and the budgets that actually govern the loop. Before this the
                 mode picker set a string and every rule that decided when the
                 agent stopped was an invisible default. -->
            <StrategyPanel
              mode={currentMode}
              advice={strategyAdvice}
              recommendation={workflow.recommendation}
              policy={workflow.policy}
              tools={workflow.tools || []}
              model={adviceModelLabel}
              busy={compiling}
              warnings={policyWarnings}
              unreliable={unreliableStrategies}
              onSwitchMode={switchMode}
              onUpdate={updatePolicy}
            />

            <TriggerSettings
              {workflow}
              channels={channelOptions}
              onChange={applyFraming}
            />

            <label class="agent-field-label" for="agent-sys">System prompt (how the agent works)</label>
            <textarea id="agent-sys" class="agent-sys" rows="9" bind:value={workflow.system_prompt}></textarea>

            <label class="agent-field-label" for="agent-tools">Tools the agent may call (one per line — exact builtin or mcp__server__tool names)</label>
            <textarea id="agent-tools" class="agent-tools" rows="5"
              value={(workflow.tools || []).join('\n')}
              on:input={(e) => { workflow = { ...workflow, tools: e.target.value.split('\n') } }}
            ></textarea>

            <label class="agent-field-label" for="agent-skills">Skills the agent may use (one per line — deployed skill names)</label>
            <textarea id="agent-skills" class="agent-tools" rows="4"
              value={(workflow.skills || []).join('\n')}
              on:input={(e) => { workflow = { ...workflow, skills: e.target.value.split('\n') } }}
            ></textarea>

            <div class="agent-spec-meta">
              {#if workflow.knowledge && workflow.knowledge.length}<span><strong>Knowledge:</strong> {workflow.knowledge.join(', ')}</span>{/if}
              {#if workflow.new_agents && workflow.new_agents.length}<span><strong>Peer agents:</strong> {workflow.new_agents.map(a => a.name || a.id).join(', ')}</span>{/if}
            </div>

            <!-- Try it: run the unsaved agent against one sample question -->
            <div class="agent-try">
              <label class="agent-field-label" for="agent-try-q">Try it — ask a sample question (runs the agent for real; the reply is shown here, not delivered to a channel)</label>
              <div class="agent-try-row">
                <input id="agent-try-q" class="agent-try-input" type="text" bind:value={tryQuestion}
                  placeholder="e.g. How has AAPL performed this quarter?"
                  on:keydown={(e) => { if (e.key === 'Enter') tryAgent() }} />
                <button class="btn primary" type="button" disabled={trying || !tryQuestion.trim()} on:click={tryAgent}>
                  {trying ? 'Running…' : 'Run'}
                </button>
              </div>
              {#if trying}
                <div class="agent-try-running"><span class="live-dot"></span> Running the agent — reasoning over its skills…</div>
              {/if}
              {#if tryResult}
                <div class="agent-try-result" class:err={!!tryResult.error}>
                  {#if tryResult.error}<div class="agent-try-err">⚠ {tryResult.error}</div>{/if}
                  {#if tryResult.reply}<div class="agent-try-reply">{tryResult.reply}</div>{/if}
                  {#if !tryResult.reply && !tryResult.error}<div class="agent-try-reply muted">(no text reply)</div>{/if}
                </div>
                {#if tryResult.nodeTrace && tryResult.nodeTrace.length}
                  <div class="agent-try-trace">
                    <div class="att-label">Every node that ran ({tryResult.nodeTrace.length}) — click a node for its input/output</div>
                    <ol class="att-list">
                      {#each tryResult.nodeTrace as n, i}
                        <li class="att-item" class:err={n.error}>
                          <button class="att-row" type="button" on:click={() => { n._open = !n._open; tryResult = tryResult }} title="Show input & output">
                            <span class="att-n">{i + 1}</span>
                            <span class="att-dot">{n.skipped ? '⏭' : n.error ? '✕' : '✓'}</span>
                            <span class="node-kind kind-{n.kind}">{n.kind}</span>
                            <span class="att-name">{n.node_id}</span>
                            {#if n.skipped}<span class="node-skip">skipped · needs consent</span>{/if}
                            <span class="att-caret">{n._open ? '▾' : '▸'}</span>
                          </button>
                          {#if n._open}
                            <div class="att-detail-box">
                              {#if n.input}<div class="att-kv"><span class="att-k">input</span><code>{n.input}</code></div>{/if}
                              {#if n.output}<div class="att-kv"><span class="att-k">output</span><code>{n.output}</code></div>{/if}
                              {#if n.error}<div class="att-kv"><span class="att-k">error</span><code>{n.error}</code></div>{/if}
                              {#if !n.input && !n.output && !n.error}<div class="att-kv muted">(no data captured)</div>{/if}
                            </div>
                          {/if}
                        </li>
                      {/each}
                    </ol>
                    <div class="att-hint">Branch nodes evaluate conditions and aren't listed as steps; the path taken is reflected by which nodes ran.</div>
                  </div>
                {/if}
                {#if tryResult.trace && tryResult.trace.length}
                  <div class="agent-try-trace">
                    <div class="att-label">Skills & tools it called ({tryResult.trace.length}) — click a step for details</div>
                    <ol class="att-list">
                      {#each tryResult.trace as t, i}
                        <li class="att-item" class:err={t.error}>
                          <button class="att-row" type="button" on:click={() => { t._open = !t._open; tryResult = tryResult }} title="Show arguments & result">
                            <span class="att-n">{i + 1}</span>
                            <span class="att-dot">{t.error ? '✕' : '✓'}</span>
                            <span class="att-name">{t.name}{#if t.detail} <span class="att-detail">→ {t.detail}</span>{/if}</span>
                            {#if t.args || t.result}<span class="att-caret">{t._open ? '▾' : '▸'}</span>{/if}
                          </button>
                          {#if t._open}
                            <div class="att-detail-box">
                              {#if t.args}<div class="att-kv"><span class="att-k">args</span><code>{t.args}</code></div>{/if}
                              {#if t.result}<div class="att-kv"><span class="att-k">result</span><code>{t.result}</code></div>{/if}
                            </div>
                          {/if}
                        </li>
                      {/each}
                    </ol>
                  </div>
                {:else if !tryResult.error}
                  <div class="att-label muted">No skills or tools were called (answered directly).</div>
                {/if}
              {/if}
            </div>
          </div>
        {:else}
          <SvelteFlow
            {nodes}
            {edges}
            {nodeTypes}
            {edgeTypes}
            fitView
            {isValidConnection}
            onconnect={onConnect}
            on:nodeclick={onNodeClick}
            on:edgeclick={onEdgeClick}
            on:paneclick={() => { selectedNode = null; selectedEdge = null }}
          >
            <Background />
            <Controls />
            <MiniMap pannable zoomable />
          </SvelteFlow>
          <!-- Maximize / restore the canvas frame -->
          <button class="frame-max canvas-max" type="button"
            data-tooltip={maximizedFrame === 'canvas' ? 'Restore layout (Esc)' : 'Maximize canvas'}
            on:click={() => toggleMax('canvas')}>{maximizedFrame === 'canvas' ? '⤡' : '⤢'}</button>
          <!-- Kind legend -->
          <div class="legend">
            {#each kinds as k}
              <span class="legend-item">
                <span class="swatch" style="background: {kindMeta(k).color}"></span>
                {kindMeta(k).label}
              </span>
            {/each}
          </div>
        {/if}
      </div>
      {/if}

      {#if workflow}
        <!-- Action bar -->
        <div class="actions">
          <!-- Workflow-only bench controls. Reasoning agents are exercised via the
               "Try it" panel in the agent editor, not the flow test bench. -->
          {#if viewMode === 'canvas' && currentMode === 'workflow'}
          <input
            class="sample"
            type="text"
            bind:value={sampleInput}
            on:change={persistBench}
            placeholder="sample input"
            aria-label="Sample test input"
          />
          <!-- Mode toggle: Dry run (default) + Live (disabled for unsaved draft) -->
          <div class="mode-toggle" role="group" aria-label="Run mode">
            <button
              class="mode-btn"
              class:active={testMode === 'dry'}
              on:click={() => (testMode = 'dry')}
              type="button"
            >Dry run</button>
            <button
              class="mode-btn"
              disabled
              data-tooltip="Live runs aren’t available for an unsaved draft — save & enable the agent and exercise it via its channel."
              aria-label="Live runs aren’t available for an unsaved draft — save & enable the agent and exercise it via its channel."
              type="button"
            >Live</button>
          </div>
          <button class="btn" on:click={runTest} disabled={testing}>
            {testing ? 'Testing…' : 'Test'}
          </button>
          <!-- Run Live is refused while execution blockers remain (ST-07). The
               server enforces this with a 422; disabling here means the user
               finds out before spending a run rather than after. -->
          <!-- Every disabled reason gets its own tooltip branch. The empty-flow
               case used to fall through to the "this will run the workflow"
               text, so the button was dead while the tooltip described what
               pressing it would do. -->
          <button class="btn" on:click={tryAgent}
            disabled={trying || hasExecutionBlockers || flowIsEmpty}
            data-tooltip={flowIsEmpty
              ? 'Add at least one step before running this workflow.'
              : hasExecutionBlockers
                ? 'Resolve the execution blockers first — this workflow cannot run yet.'
                : 'Run the workflow for real with the sample input and show the result + tool trace'}>
            {trying ? 'Running…' : '▶ Run live'}
          </button>
          <button
            class="btn architect"
            on:click={buildUntilWorks}
            disabled={building || !workflow}
            data-tooltip="Autonomously fill gaps, fix every error, and run it until it works"
          >
            {building ? 'Building…' : '🛠 Build until it works'}
          </button>
          {#if runBlockers.length}
            <div class="run-blockers" role="alert">
              This workflow can’t run yet:
              <ul>
                {#each runBlockers as b}
                  <li>{typeof b === 'string' ? b : (b.message || b.fix || b.id)}</li>
                {/each}
              </ul>
            </div>
          {/if}
          {#if plan && plan.tier}
            <span
              class="tier-chip tier-{plan.tier}"
              data-tooltip={(plan.reasons && plan.reasons.length) ? plan.reasons.join('; ') : 'capability tier'}
            >
              {tierLabel(plan.tier)}
            </span>
          {/if}
          <button class="btn btn-sm view-toggle" type="button" on:click={toggleTests}
                  data-tooltip="Show or hide the test & self-heal panels below the canvas">
            {showTests ? 'Hide tests' : 'Show tests'}
          </button>
          <button class="btn btn-sm view-toggle" type="button" on:click={toggleInspector}
                  data-tooltip="Show or hide the inspector panel">
            {showInspector ? 'Hide inspector' : 'Show inspector'}
          </button>
          <button class="btn btn-sm view-toggle" type="button" on:click={openWorkflowSettings}
                  data-tooltip="Change manual, cron, channel, or webhook start settings">
            ⚙ Trigger &amp; delivery
          </button>
          {/if}
          {#if viewMode === 'code'}
            <button class="btn" on:click={validateCode} disabled={codeValidating}>
              {codeValidating ? 'Validating…' : '✓ Validate'}
            </button>
            <button class="btn" on:click={reviewWithAI} disabled={reviewing}
                    data-tooltip="Ask the model to review the YAML against your rules (catches judgment-call issues the linter can't)">
              {reviewing ? 'Reviewing…' : '🔍 AI review'}
            </button>
          {/if}
          <!-- The bare "Save" is deliberately CODE-VIEW ONLY. On Build/Test the
               wizard still has Test and Save ahead of it, and a Save button at
               the bottom of the canvas invited people to skip both — the step
               rail's "Save" step is the one place that shows whether it is
               safe to save. Authored SOUL.yaml has no such step, so "Save YAML"
               stays. -->
          {#if viewMode === 'code'}
            <button class="btn primary"
                    on:click={() => saveFromCode()}
                    disabled={saving || gateChecking || !!consent || !!preflight || (securityReview && (securityReview.blockers || []).length > 0)}
                    title={securityReview && (securityReview.blockers || []).length > 0 ? 'Fix security blockers to save' : ''}>
              {saving ? 'Saving…' : gateChecking ? 'Checking…' : 'Save YAML'}
            </button>
          {:else}
            <button class="btn primary"
                    on:click={() => goStep(STEP_SAVE)}
                    disabled={!canEnter(STEP_SAVE, wizardCtx).ok}
                    data-tooltip="Review readiness, then save — step 4 of the wizard">
              Review &amp; save →
            </button>
          {/if}
        </div>

        {#if saveMsg}<div class="strip strip-ok">✓ {saveMsg}</div>{/if}
        {#if saveError}<div class="strip strip-error">⚠ {saveError}</div>{/if}
        <!-- F-GUI-3 — visible reminder that mirrors the pre-save contract gate,
             so operators aren't surprised when Save is disabled. -->
        <!-- Not canvas-only: Save is disabled in every view, so hiding the
             reason in Plan and SOUL.yaml left the button dead with no
             explanation anywhere on screen. -->
        {#if securityReview && (securityReview.blockers || []).length > 0}
          <div class="strip strip-error">
            ⚠ Fix security blockers to save — {(securityReview.blockers || []).length} pending.
            {#if viewMode !== 'canvas'}
              <!-- The review panel itself lives on the canvas, so from here the
                   strip has to say where to go, not just that something is wrong. -->
              <button class="btn btn-sm" type="button" on:click={showCanvasView}>Review on canvas</button>
            {/if}
          </div>
        {/if}

        <!-- F-GUI-3 — Security review panel. Always visible while editing a
             workflow so the trust / network / file / channel / privileged /
             confirmation summary is one glance away, and the recommendations
             carry the "Apply" quick-actions for the unambiguous replacements
             (write_file → kb_write is auto-appliable; shell_exec / http_request
             recommendations point at a general fix and surface as a toast).
             Structured to match the Contract panel's visual weight. -->
        {#if viewMode === 'canvas' && workflow}
          <!-- a11y note: refresh is a sibling <button>, NOT nested inside the
               header toggle, because nested interactive elements are invalid
               HTML and cause the Svelte a11y linter to flag click-only
               <span>s. Two flex-row buttons keep both keyboard + screen
               reader semantics correct. -->
          <div class="security-panel" class:has-blockers={(securityReview?.blockers || []).length > 0} class:has-warnings={(securityReview?.warnings || []).length > 0}>
            <div class="security-head-row">
              <button class="security-head" type="button" aria-expanded={securityPanelOpen}
                      on:click={() => securityPanelOpen = !securityPanelOpen}>
                <span class="security-caret">{securityPanelOpen ? '▾' : '▸'}</span>
                <strong>Security review</strong>
                {#if securityLoading}
                  <span class="security-count muted">refreshing…</span>
                {:else if !securityReview}
                  <span class="security-count muted">no report yet</span>
                {:else}
                  <span class="security-count {securityReview.ok ? 'ok' : ''}">
                    {(securityReview.blockers || []).length} blocker{(securityReview.blockers || []).length === 1 ? '' : 's'}
                    · {(securityReview.warnings || []).length} warning{(securityReview.warnings || []).length === 1 ? '' : 's'}
                    · {(securityReview.recommendations || []).length} rec{(securityReview.recommendations || []).length === 1 ? '' : 's'}
                  </span>
                {/if}
              </button>
              <button class="security-refresh" type="button"
                      data-tooltip="Re-run security review"
                      aria-label="Re-run security review"
                      on:click={refreshSecurityReview}>↺</button>
            </div>
            {#if securityPanelOpen}
              {#if securityError}
                <div class="strip strip-error" style="margin-top:.35rem;">⚠ {securityError}</div>
              {/if}
              {#if securityReview}
                {@const s = securityReview.summary || {}}
                <div class="security-summary">
                  <div><span>Untrusted sources</span><code>{(s.untrusted_content_sources || []).join(', ') || '—'}</code></div>
                  <div><span>Network</span><code>{(s.network_tools || []).join(', ') || '—'}</code></div>
                  <div><span>File</span><code>{(s.file_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Channel</span><code>{(s.channel_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Privileged</span><code>{(s.privileged_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Confirm gates</span><code>{(s.confirm_tools || []).join(', ') || '—'}</code></div>
                  <div><span>Intent gate</span><code>{s.intent_gate_mode || 'prompt (default)'}</code></div>
                  <div>
                    <span>Privileged channel exposure</span>
                    <code>{s.privileged_channel_exposure ? 'yes — needs ack' : 'no'}</code>
                  </div>
                </div>
                {#if (securityReview.blockers || []).length}
                  <div class="security-block-list">
                    <div class="security-block-hdr">Blockers ({securityReview.blockers.length})</div>
                    {#each securityReview.blockers as b}
                      <div class="security-item block">
                        <div class="security-cat">{b.category}</div>
                        <div class="security-msg">{b.message}</div>
                        {#if b.fix}<div class="security-fix">→ {b.fix}</div>{/if}
                        {#if b.action_label}
                          <button class="btn btn-sm security-apply" type="button" on:click={() => applyFixAction(b)}>{b.action_label}</button>
                        {/if}
                      </div>
                    {/each}
                  </div>
                {/if}
                {#if (securityReview.warnings || []).length}
                  <div class="security-block-list">
                    <div class="security-block-hdr">Warnings ({securityReview.warnings.length})</div>
                    {#each securityReview.warnings as w}
                      <div class="security-item warn">
                        <div class="security-cat">{w.category}</div>
                        <div class="security-msg">{w.message}</div>
                        {#if w.fix}<div class="security-fix">→ {w.fix}</div>{/if}
                        <div class="security-actions">
                          {#if w.action_label}
                            <button class="btn btn-sm security-apply" type="button" on:click={() => applyFixAction(w)}>{w.action_label}</button>
                          {/if}
                          {#if w.category === 'channel'}
                            <button class="btn btn-sm" type="button" on:click={() => { window.location.hash = '#channels' }}>Open Delivery</button>
                          {/if}
                        </div>
                      </div>
                    {/each}
                  </div>
                {/if}
                {#if (securityReview.recommendations || []).length}
                  <div class="security-block-list">
                    <div class="security-block-hdr">Recommendations ({securityReview.recommendations.length})</div>
                    {#each securityReview.recommendations as rec}
                      <div class="security-item rec">
                        <div class="security-cat">rewrite</div>
                        <div class="security-msg">Replace <code>{rec.from}</code> with <code>{rec.suggest}</code></div>
                        {#if rec.reason}<div class="security-fix">{rec.reason}</div>{/if}
                        <button class="btn btn-sm security-apply" type="button" on:click={() => applySecurityRecommendation(rec)}>Apply</button>
                      </div>
                    {/each}
                  </div>
                {/if}
              {/if}
            {/if}
          </div>
        {/if}

        {#if viewMode === 'canvas'}
          <!-- Drag this splitter to give the bottom workbench more/less height. -->
          <div class="wb-splitter" role="separator" aria-orientation="horizontal"
            data-tooltip="Drag to resize the workbench" on:pointerdown={startBenchResize}></div>
        {/if}
        <div class="workbench" style={maximizedFrame === 'bench' ? '' : `max-height:${benchHeight}px`}>
          {#if viewMode === 'canvas'}
            <div class="wb-bar">
              <span class="wb-title">Workbench</span>
              <button class="frame-max" type="button"
                data-tooltip={maximizedFrame === 'bench' ? 'Restore layout (Esc)' : 'Maximize workbench'}
                on:click={() => toggleMax('bench')}>{maximizedFrame === 'bench' ? '⤡' : '⤢'}</button>
            </div>
          {/if}

        <!-- ── Live run result (real run with the sample input) ───────────── -->
        {#if currentMode === 'workflow' && (trying || tryResult)}
          <div class="panel build-progress">
            {#if trying}
              <div class="agent-try-running"><span class="live-dot"></span> Running the workflow with your sample input…</div>
            {/if}
            {#if tryResult}
              <div class="agent-try-result" class:err={!!tryResult.error}>
                {#if tryResult.error}
                  <div class="agent-try-err">⚠ {tryResult.error}</div>
                  <!-- Plain-English explanation + suggested fix (Story 6). -->
                  {@const ex = explainPythonError(tryResult.error)}
                  <div class="py-explain">
                    <div class="py-explain-what">{ex.summary}</div>
                    <div class="py-explain-fix">💡 {ex.fix}</div>
                  </div>
                  <div class="try-fix-row">
                    <!-- Action-aware fix row. When the error has a concrete
                         resolution (grant consent, raise a timeout, fix a key),
                         lead with THAT — self-correct is the fallback for
                         genuinely broken workflows, and offering it for a
                         permission error just rewrites working code. -->
                    {#if ex.action && ex.action.kind === 'consent'}
                      <button
                        class="btn btn-sm cv-fixbtn primary"
                        type="button"
                        on:click={() => openConsentForRunError(ex.action.nodeId)}
                        disabled={consentLoading || !workflow}
                        data-tooltip="Review exactly what this block does, then approve it and run again"
                      >
                        {consentLoading ? 'Loading…' : '🔓 Review & grant'}
                      </button>
                      <span class="try-fix-hint">Approve this block to run it. Nothing is saved.</span>
                    {:else if ex.action && ex.action.kind === 'timeout'}
                      <button
                        class="btn btn-sm cv-fixbtn primary"
                        type="button"
                        on:click={() => selectNodeById(ex.action.nodeId)}
                        disabled={!workflow}
                        data-tooltip="Open this block so you can raise its timeout"
                      >
                        ⏱ Open “{ex.action.nodeId}”
                      </button>
                      <span class="try-fix-hint">Raise this block's timeout, or change the web-search timeout in Workspace settings.</span>
                    {:else if ex.action && ex.action.kind === 'tools'}
                      <span class="try-fix-hint">Add the missing tool to this agent, or install the MCP server / skill that provides it.</span>
                    {:else if ex.action && ex.action.kind === 'secrets'}
                      <a class="btn btn-sm cv-fixbtn primary" href="#secrets" data-tooltip="Open workspace Secrets to update the credential">🔑 Open Secrets</a>
                      <span class="try-fix-hint">Update this workspace's credential, then run again.</span>
                    {:else if ex.action && ex.action.kind === 'channels'}
                      <a class="btn btn-sm cv-fixbtn primary" href="#delivery" data-tooltip="Open channel settings">📣 Open Delivery</a>
                      <span class="try-fix-hint">Check the destination id and the bot's access.</span>
                    {/if}
                    <button
                      class="btn btn-sm cv-fixbtn cv-aibtn"
                      type="button"
                      on:click={troubleshootLiveRun}
                      disabled={troubleshooting || !workflow}
                      data-tooltip="Ask Studio to repair the workflow using this live error and the steps it observed"
                    >
                      {troubleshooting ? 'Fixing…' : '✨ Self-correct workflow'}
                    </button>
                    {#if !ex.action}
                      <span class="try-fix-hint">Uses this run error and trace, then reloads a repaired draft.</span>
                    {/if}
                  </div>
                {/if}
                {#if tryResult.reply}<div class="agent-try-reply">{tryResult.reply}</div>{/if}
                {#if !tryResult.reply && !tryResult.error}<div class="agent-try-reply muted">(no text result)</div>{/if}
              </div>
              {#if tryResult.nodeTrace && tryResult.nodeTrace.length}
                <div class="agent-try-trace">
                  <div class="att-label">Every node that ran ({tryResult.nodeTrace.length}) — click a node for its input/output</div>
                  <ol class="att-list">
                    {#each tryResult.nodeTrace as n, i}
                      {@const soft = nodeSoftError(n)}
                      <li class="att-item" class:err={n.error} class:soft={!n.error && soft}>
                        <button class="att-row" type="button" on:click={() => { n._open = !n._open; tryResult = tryResult }} title="Show input & output">
                          <span class="att-n">{i + 1}</span>
                          <span class="att-dot">{n.skipped ? '⏭' : n.error ? '✕' : soft ? '⚠' : '✓'}</span>
                          <span class="node-kind kind-{n.kind}">{n.kind}</span>
                          <span class="att-name">{n.node_id}</span>
                          {#if n.skipped}<span class="node-skip">skipped · needs consent</span>{/if}
                          {#if n.adapted}<span class="node-adapted" data-tooltip="The runtime salvaged this node with the LLM because the input shape was unexpected. The output is a reconstruction — consider fixing the node so it parses the real shape.">✦ auto-adapted</span>{/if}
                          {#if !n.error && soft && !n.adapted}<span class="node-skip soft">ran, but output reports an error</span>{/if}
                          <span class="att-caret">{n._open ? '▾' : '▸'}</span>
                        </button>
                        {#if n._open}
                          <div class="att-detail-box">
                            {#if (n.input_full || n.input)}
                              <div class="att-kv">
                                <span class="att-k">input <button class="copy-mini" type="button" on:click|stopPropagation={() => copyValue(n.input_full || n.input, 'in'+i)}>{copiedKey==='in'+i ? 'copied' : 'copy'}</button></span>
                                <code class="full">{n.input_full || n.input}</code>
                              </div>
                            {/if}
                            {#if (n.output_full || n.output)}
                              <div class="att-kv">
                                <span class="att-k">output <button class="copy-mini" type="button" on:click|stopPropagation={() => copyValue(n.output_full || n.output, 'out'+i)}>{copiedKey==='out'+i ? 'copied' : 'copy'}</button></span>
                                <code class="full">{n.output_full || n.output}</code>
                              </div>
                            {/if}
                            {#if soft && !n.error}<div class="att-kv"><span class="att-k">reported error</span><code>{soft}</code></div>{/if}
                            {#if n.error}<div class="att-kv"><span class="att-k">error</span><code>{n.error}</code></div>{/if}
                            {#if !n.input && !n.output && !n.error}<div class="att-kv muted">(no data captured)</div>{/if}
                          </div>
                        {/if}
                      </li>
                    {/each}
                  </ol>
                  <div class="att-hint">Branch nodes evaluate conditions and aren't listed; the path taken is reflected by which nodes ran.</div>
                </div>
              {/if}

              <!-- ── Observe-and-adjust: fix a node against the REAL output ──── -->
              {#if liveHasFailure}
                <div class="live-adjust">
                  <div class="try-fix-row">
                    <button
                      class="btn btn-sm cv-fixbtn"
                      type="button"
                      on:click={adjustToLiveOutput}
                      disabled={repairing || !workflow}
                      data-tooltip="Have Studio observe what each node actually returned and propose a per-node adjustment"
                    >
                      {repairing ? 'Analyzing the run…' : '🔎 Adjust to real output'}
                    </button>
                    <span class="try-fix-hint">Observes the actual node outputs and proposes targeted fixes for review.</span>
                  </div>

                  {#if repairError}<div class="agent-try-err">⚠ {repairError}</div>{/if}
                  {#if repairDone}<div class="adjust-done">✓ {repairDone}</div>{/if}

                  {#each repairProposals as p (p.node_id + p.field)}
                    <div class="adjust-card" class:auto={p.auto}>
                      <div class="adjust-head">
                        <span class="node-kind">{p.node_id}</span>
                        <span class="adjust-class">{(p.class || '').replace('_',' ')}</span>
                        {#if p.auto}<span class="adjust-badge">safe / deterministic</span>{/if}
                      </div>
                      <div class="adjust-rationale">{p.rationale}</div>
                      {#if p.observed_keys && p.observed_keys.length}
                        <div class="adjust-observed">The API actually returned: <code>{p.observed_keys.join(', ')}</code></div>
                      {/if}
                      {#if p.new}
                        <div class="adjust-diff">
                          {#if p.old}<div class="diff-old"><span class="diff-tag">was</span><code>{p.old}</code></div>{/if}
                          <div class="diff-new"><span class="diff-tag">now</span><code>{p.new}</code></div>
                        </div>
                        <div class="adjust-actions">
                          <button class="btn btn-sm btn-primary" type="button" on:click={() => applyProposal(p)} disabled={p._applying}>
                            {p._applying ? 'Applying…' : 'Approve & apply'}
                          </button>
                          <button class="btn btn-sm" type="button" on:click={() => previewProposal(p)} disabled={p._applying || repairDiffBusy}>
                            {repairDiffBusy && repairDiffFor === p ? 'Diffing…' : 'Preview diff'}
                          </button>
                          <button class="btn btn-sm" type="button" on:click={() => rejectProposal(p)} disabled={p._applying}>Dismiss</button>
                        </div>
                        {#if repairDiff && repairDiff.p === p}
                          <div class="repair-diff">
                            <div class="repair-diff-head">
                              SOUL.yaml changes <span class="rd-stat add">+{repairDiff.stats.added}</span> <span class="rd-stat del">−{repairDiff.stats.removed}</span>
                            </div>
                            <pre class="repair-diff-body">{#each repairDiff.lines as l}<span class="rd-{l.op === '+' ? 'add' : l.op === '-' ? 'del' : 'ctx'}">{l.op} {l.text}
</span>{/each}</pre>
                            <div class="adjust-actions">
                              <button class="btn btn-sm btn-primary" type="button" on:click={applyDiff}>Apply this change</button>
                              <button class="btn btn-sm" type="button" on:click={cancelDiff}>Cancel</button>
                            </div>
                          </div>
                        {/if}
                      {:else}
                        <div class="adjust-advisory">No automatic rewrite — adjust this node manually using the observed shape above.</div>
                        <div class="adjust-actions">
                          <button class="btn btn-sm" type="button" on:click={() => rejectProposal(p)}>Dismiss</button>
                        </div>
                      {/if}
                    </div>
                  {/each}
                </div>
              {/if}

              {#if tryResult.trace && tryResult.trace.length}
                <div class="agent-try-trace">
                  <div class="att-label">Tool calls ({tryResult.trace.length}) — click for details</div>
                  <ol class="att-list">
                    {#each tryResult.trace as t, i}
                      <li class="att-item" class:err={t.error}>
                        <button class="att-row" type="button" on:click={() => { t._open = !t._open; tryResult = tryResult }}>
                          <span class="att-n">{i + 1}</span>
                          <span class="att-dot">{t.error ? '✕' : '✓'}</span>
                          <span class="att-name">{t.name}{#if t.detail} <span class="att-detail">→ {t.detail}</span>{/if}</span>
                          {#if t.args || t.result}<span class="att-caret">{t._open ? '▾' : '▸'}</span>{/if}
                        </button>
                        {#if t._open}
                          <div class="att-detail-box">
                            {#if t.args}<div class="att-kv"><span class="att-k">args</span><code>{t.args}</code></div>{/if}
                            {#if t.result}<div class="att-kv"><span class="att-k">result</span><code>{t.result}</code></div>{/if}
                          </div>
                        {/if}
                      </li>
                    {/each}
                  </ol>
                </div>
              {/if}
            {/if}
          </div>
        {/if}

        <!-- ── Architect live progress: streamed while the loop runs ──────── -->
        {#if building || (buildLog.length && !buildReport)}
          <div class="panel build-progress">
            <div class="build-head">
              <span class="spinner" aria-hidden="true"></span>
              <span class="build-summary">Building & verifying — fixing problems as they’re found…</span>
            </div>
            {#if buildLog.length}
              <ul class="build-live">
                {#each buildLog as ev}
                  <li class="live-line live-{ev.kind}">
                    {#if ev.attempt}<span class="live-n">#{ev.attempt}</span>{/if}
                    {ev.message}
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {/if}

        <!-- ── Story 9 M (Cohort C): streamed generate pipeline transcript ──
             Hidden while the progress modal is on screen (it shows the same
             phases); reappears as the post-run summary, or as the live
             transcript if the user ran the modal in the background. -->
        {#if (pipelineRunning || pipelineLog.length) && !(pipelineRunning && !pipelineModalHidden)}
          <div class="panel build-progress">
            <div class="build-head">
              {#if pipelineRunning}<span class="spinner" aria-hidden="true"></span>{/if}
              <span class="build-summary">
                {pipelineRunning ? 'Generate pipeline running…' : 'Generate pipeline finished — see the canvas.'}
              </span>
              {#if !pipelineRunning}
                <button class="icon-btn" data-tooltip="Clear transcript" on:click={() => (pipelineLog = [])} style="margin-left:auto;">✕</button>
              {/if}
            </div>
            {#if pipelineLog.length}
              <ul class="build-live">
                {#each pipelineLog as ev}
                  <li class="live-line live-{ev.status}">
                    <span class="live-n">{ev.phase}</span>
                    <span class="live-status">{ev.status}</span>
                    {#if ev.message}<span>{ev.message}</span>{/if}
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {/if}

        <!-- ── Architect build report: what was wrong, and how it was fixed ── -->
        {#if buildReport}
          <Collapsible id="build-report" title="Build report" sub={buildReport.summary}>
            <svelte:fragment slot="actions">
              <span class="build-badge" class:ok={buildReport.ok}>
                {buildReport.verified ? '✓ Verified by running it' : buildReport.ok ? '✓ Validated' : '⚠ Needs attention'}
              </span>
              <button class="icon-btn" data-tooltip="Inspect every step of this build" on:click={() => (showBuildInspector = true)}>🔍 Inspect</button>
              <button class="icon-btn" data-tooltip="Dismiss" on:click={() => (buildReport = null)}>✕</button>
            </svelte:fragment>
            {#if buildGlue && buildGlue.length}
              <ul class="build-glue">
                {#each buildGlue as g}<li>🧩 {g}</li>{/each}
              </ul>
            {/if}
            {#if buildReport.contract}
              <div class="contract-panel" class:ok={buildReport.contract.ok}>
                <div class="contract-head">
                  <div>
                    <div class="contract-title">Studio contract</div>
                    <div class="contract-summary">{buildReport.contract.summary}</div>
                  </div>
                  <span class="contract-score" class:ok={buildReport.contract.ok}>
                    {buildReport.contract.score}/100
                  </span>
                </div>
                {#if buildReport.contract.checks && buildReport.contract.checks.length}
                  <ul class="contract-checks">
                    {#each buildReport.contract.checks as c}
                      <li class="contract-check" class:pass={c.status === 'pass'} class:warn={c.status === 'warn'} class:block={c.status === 'block'}>
                        <span class="contract-mark">{c.status === 'pass' ? '✓' : c.status === 'warn' ? '!' : '✕'}</span>
                        <div class="contract-copy">
                          <div class="contract-check-title">{c.title}</div>
                          <div class="contract-message">{c.message}</div>
                          {#if c.fix}<div class="contract-fix">{c.fix}</div>{/if}
                        </div>
                        {#if c.nodeId}
                          <button class="contract-open" type="button" on:click={() => revealNode(c.nodeId)}>Open</button>
                        {/if}
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {/if}
            {#if buildReport.needs_external && buildReport.needs_external.length}
              <div class="build-external">
                <strong>Needs your input:</strong>
                <ul>{#each buildReport.needs_external as n}<li>🔑 {n}</li>{/each}</ul>
              </div>
            {/if}
            {#if buildReport.changes && buildReport.changes.length}
              <div class="build-changes">
                <strong>What Studio changed:</strong>
                <ul>{#each buildReport.changes as ch}<li>✎ {ch}</li>{/each}</ul>
              </div>
            {/if}
            {#if buildReport.attempts && buildReport.attempts.length}
              <ol class="build-attempts">
                {#each buildReport.attempts as a}
                  <li class="attempt" class:ok={a.ok}>
                    <span class="attempt-phase">{a.phase}</span>
                    {#if a.problems && a.problems.length}
                      <ul class="attempt-problems">
                        {#each a.problems as p}<li>{p}</li>{/each}
                      </ul>
                    {/if}
                    <span class="attempt-action">{a.changed ? '→ ' : ''}{a.action}</span>
                  </li>
                {/each}
              </ol>
            {/if}
            {#if buildReport.residual && buildReport.residual.length}
              <div class="build-residual">
                <strong>Still unresolved:</strong>
                <ul>{#each buildReport.residual as r}<li>{r}</li>{/each}</ul>
              </div>
            {/if}
            {#if buildReport.diagnosis}
              <div class="build-diagnosis">
                <div class="bd-text">💡 {buildReport.diagnosis}</div>
                {#if buildReport.suggest_mode}
                  <button class="bd-action" type="button" disabled={compiling}
                    on:click={() => switchMode(buildReport.suggest_mode)}>
                    Rebuild as {recoLabel(buildReport.suggest_mode)}
                  </button>
                {/if}
              </div>
            {/if}
          </Collapsible>
        {/if}

        {#if showTests && viewMode === 'canvas'}
        <!-- ── Runtime self-heal: failed (incl. scheduled) runs ──────────── -->
        <div class="panel bench">
          <div class="bench-section">
            <button class="bench-head" type="button" on:click={toggleFailedRuns}>
              <span class="caret">{showFailedRuns ? '▾' : '▸'}</span>
              <h3 class="panel-title">Failed runs — self-heal</h3>
              <span class="bench-sub">Pick up what failed at run time (including scheduled runs) and fix it automatically.</span>
            </button>
            {#if showFailedRuns}
              <!-- Grouped, classified failures (ST-13). The flat list produced
                   one row per run, so an agent failing the same way every
                   morning buried the one genuinely different failure among a
                   dozen identical ones. -->
              <FailedRunsPanel
                runs={failedRuns}
                diagnosis={runDiagnosis}
                loading={loadingFailed}
                busy={!!healing}
                onSelect={(run) => { if (run && run.id) loadRunTrace(run.id, run.agentId).catch(() => {}) }}
                onRepair={(run) => { if (run && run.id) healFailedRun(run.id) }}
                onRetry={(run) => retryFailedRun(run)}
                onReveal={(nodeId) => { goStep(STEP_BUILD); revealNode(nodeId) }}
              />
              <button class="btn small" on:click={loadFailedRuns} disabled={loadingFailed}>Refresh</button>
              {#if healResult}
                <div class="strip {healResult.changed ? 'strip-ok' : 'strip-warn'}">
                  {#if healResult.changed}
                    <span>
                      ✓ Healed “{healResult.agentName || healResult.agentId}”. Preview the change below and click <em>Apply this fix</em> to load it above; Save to persist.
                      {#if healResult.report && healResult.report.verified} The fix was verified by re-running it.{/if}
                    </span>
                  {:else}
                    <span>No automatic fix found for: {healResult.error}</span>
                  {/if}
                </div>
                {#if healDiffBusy}
                  <div class="repair-diff">
                    <div class="repair-diff-head">Computing before/after diff…</div>
                  </div>
                {:else if healDiff}
                  <div class="repair-diff">
                    <div class="repair-diff-head">
                      SOUL.yaml changes <span class="rd-stat add">+{healDiff.stats.added}</span> <span class="rd-stat del">−{healDiff.stats.removed}</span>
                    </div>
                    {#if healDiff.lines.length}
                      <pre class="repair-diff-body">{#each healDiff.lines as l}<span class="rd-{l.op === '+' ? 'add' : l.op === '-' ? 'del' : 'ctx'}">{l.op} {l.text}
</span>{/each}</pre>
                    {:else}
                      <div class="adjust-advisory">Diff service was unavailable, but the healed workflow is ready to apply.</div>
                    {/if}
                    <div class="adjust-actions">
                      <button class="btn btn-sm btn-primary" type="button" on:click={applyHealDiff}>Apply this fix</button>
                      <button class="btn btn-sm" type="button" on:click={cancelHealDiff}>Cancel</button>
                    </div>
                  </div>
                {/if}
                {#if healResult.sessionId || healResult.evidence}
                  <div class="debug-evidence">
                    <div class="debug-evidence-head">
                      <span class="failed-agent">{healResult.agentName || healResult.agentId}</span>
                      {#if healResult.sessionId}<code>{healResult.sessionId}</code>{/if}
                    </div>
                    {#if healResult.error}<div class="debug-evidence-error">{healResult.error}</div>{/if}
                    {#if healResult.evidence}
                      <details>
                        <summary>Action-log evidence used by Studio</summary>
                        <pre>{healResult.evidence}</pre>
                      </details>
                    {/if}
                  </div>
                {/if}
              {/if}
            {/if}
          </div>
        </div>

        <!-- The TEST bench proper is workflow-only: a reasoning agent has no
             fixed graph to mock, assert over or dry-run, and is exercised from
             the "Try it" panel in the agent editor instead. -->
        {#if currentMode === 'workflow'}
        <!-- ── Test bench editors: Mocks + Assertions (M5) ──────────────── -->
        <div class="panel bench">
          <!-- Test inputs (ST-10): Input / Mock Data / Variables / Environment.
               Replaces the mocks-only section — variables, environment and the
               start node had no UI at all, and the mocks were buried down a
               scrolling bench, which made reproducing a specific failure a hunt
               rather than a task. -->
          <div class="bench-section">
            <button class="bench-head" type="button" on:click={() => (showMocks = !showMocks)}>
              <span class="caret">{showMocks ? '▾' : '▸'}</span>
              <h3 class="panel-title">Test inputs</h3>
              <span class="bench-sub">The exact conditions this run starts from — saved with the workflow.</span>
            </button>
            {#if showMocks}
              <TestLabInputs
                {sampleInput}
                {mockText}
                {mockErrors}
                variables={testVariables}
                environment={testEnvironment}
                startNode={testStartNode}
                nodes={draftNodes}
                disabled={testing}
                onSampleInput={(v) => { sampleInput = v; persistBench() }}
                onSetMock={setMockText}
                onVariables={(m) => { testVariables = m; persistBench() }}
                onEnvironment={(m) => { testEnvironment = m; persistBench() }}
                onStartNode={setTestStartNode}
              />
            {/if}
          </div>

          <!-- Assertions (S5.2) -->
          <div class="bench-section">
            <div class="bench-head split">
              <button class="bench-head-toggle" type="button" on:click={() => (showAssertions = !showAssertions)}>
                <span class="caret">{showAssertions ? '▾' : '▸'}</span>
                <h3 class="panel-title">Assertions</h3>
                <span class="bench-sub">Check a node’s output or the final result after a run.</span>
              </button>
              <button class="btn btn-sm" type="button" on:click={addAssertion}>+ Add</button>
            </div>
            {#if showAssertions}
            {#if assertions.length}
              <ul class="assert-list">
                {#each assertions as a, i}
                  <li class="assert-row">
                    <select
                      class="assert-target"
                      value={a.target}
                      on:change={(e) => updateAssertion(i, { target: e.target.value })}
                      aria-label="Assertion target"
                    >
                      {#each assertionTargets as t}
                        <option value={t}>{t}</option>
                      {/each}
                    </select>
                    <select
                      class="assert-op"
                      value={a.op}
                      on:change={(e) => updateAssertion(i, { op: e.target.value })}
                      aria-label="Assertion operator"
                    >
                      <option value="contains">contains</option>
                      <option value="equals">equals</option>
                      <option value="exists">exists</option>
                    </select>
                    {#if a.op !== 'exists'}
                      <input
                        class="assert-value"
                        type="text"
                        value={a.value}
                        on:input={(e) => updateAssertion(i, { value: e.target.value })}
                        placeholder="expected value"
                        aria-label="Assertion value"
                      />
                    {:else}
                      <span class="assert-value muted">(no value)</span>
                    {/if}
                    <button class="btn btn-sm" type="button" on:click={() => removeAssertion(i)} aria-label="Remove assertion">✕</button>
                  </li>
                {/each}
              </ul>
            {:else}
              <p class="muted">No assertions — add one to verify outputs.</p>
            {/if}
            {/if}
          </div>
        </div>

        <!-- Clarify panel -->
        {#if questions.length}
          <Collapsible id="clarify" title="Clarify">
            {#each questions as q (q.id)}
              <div class="q">
                <label for={'q-' + q.id}>{q.text}</label>
                {#if q.options && q.options.length}
                  <select id={'q-' + q.id} bind:value={answers[q.id]}>
                    <option value="" disabled selected={!answers[q.id]}>Choose…</option>
                    {#each q.options as opt}
                      <option value={opt}>{opt}</option>
                    {/each}
                  </select>
                {:else}
                  <input id={'q-' + q.id} type="text" bind:value={answers[q.id]} />
                {/if}
              </div>
            {/each}
            <button class="btn primary" on:click={applyAnswers} disabled={compiling}>
              Apply answers
            </button>
          </Collapsible>
        {/if}

        <!-- Test results -->
        {#if testError}
          <div class="strip strip-error">
            ⚠ {testError}
            <button class="btn btn-sm" type="button" on:click={() => troubleshoot(testError)} disabled={troubleshooting || !workflow} title="Let the builder model rewrite the agent to fix this error">
              {troubleshooting ? 'Fixing…' : '✨ Fix with AI'}
            </button>
          </div>
        {/if}
        {#if testResult}
          <Collapsible id="test-result" title="Test result">
            <!-- Overall pass/fail banner (only when assertions ran) -->
            {#if testResult.assertions && testResult.assertions.length}
              <div class="overall {testResult.passed ? 'overall-pass' : 'overall-fail'}">
                <span class="overall-badge">{testResult.passed ? '✓ PASSED' : '✕ FAILED'}</span>
                <span class="overall-sub">
                  {testResult.assertions.filter((a) => a.pass).length}/{testResult.assertions.length} assertions passed
                </span>
                {#if testResult.mode}<span class="mode-chip">{testResult.mode}</span>{/if}
              </div>
            {/if}

            <!-- Backend warnings (e.g. a live run on an unsaved draft) -->
            {#if testResult.warnings && testResult.warnings.length}
              {#each testResult.warnings as w}
                <div class="strip strip-warn bench-warn">⚠ {typeof w === 'string' ? w : (w.message || fmt(w))}</div>
              {/each}
            {/if}

            <h3 class="panel-title">Test trace</h3>
            {#if testResult.trace && testResult.trace.length}
              <ol class="trace">
                {#each testResult.trace as step, i}
                  {@const toolErr = stepToolError(step)}
                  <li class:step-failed={!!toolErr}>
                    <span class="step-n">{i + 1}</span>
                    <div class="step-body">
                      <div class="step-head">
                        {#if toolErr}<span class="status-dot fail" data-tooltip="This step failed">✕</span>{:else}<span class="status-dot ok" data-tooltip="This step succeeded">✓</span>{/if}
                        <strong>{step.nodeId}</strong>
                        {#if step.kind}<span class="step-kind">{step.kind}</span>{/if}
                        {#if step.wiredPorts}<span class="wired-badge" data-tooltip="Input assembled from typed port wires — no template">⮑ wired</span>{/if}
                        {#if step.mocked}<span class="mock-badge" data-tooltip="Output was mocked, node was not run">mocked</span>{/if}
                        {#if step.durationMs != null}<span class="dur-badge" data-tooltip="Wall-clock duration">{step.durationMs}ms</span>{/if}
                      </div>
                      {#if toolErr}
                        <div class="step-line err">{toolErr}</div>
                      {:else if stepSummary(step)}
                        <div class="step-line ok">{stepSummary(step)}</div>
                      {/if}
                      <details class="step-details">
                        <summary>input / output</summary>
                        <div class="step-io">
                          <span class="io-label">in</span><pre>{prettyJSON(step.input)}</pre>
                          <span class="io-label">out</span><pre>{prettyJSON(step.output)}</pre>
                        </div>
                      </details>
                    </div>
                  </li>
                {/each}
              </ol>
            {:else}
              <p class="muted">No trace returned.</p>
            {/if}
            <div class="result">
              <span class="io-label">result</span>
              <pre>{fmt(testResult.result)}</pre>
            </div>

            <!-- Assertion results (S5.2) -->
            {#if testResult.assertions && testResult.assertions.length}
              <div class="assert-results">
                <h3 class="panel-title">Assertions</h3>
                <ul class="ar-list">
                  {#each testResult.assertions as a}
                    <li class="ar-row {a.pass ? 'ar-pass' : 'ar-fail'}">
                      <span class="ar-badge">{a.pass ? '✓' : '✕'}</span>
                      <span class="ar-expr">
                        <code>{a.target}</code> <em>{a.op}</em>
                        {#if a.op !== 'exists'}<code>{fmt(a.value)}</code>{/if}
                      </span>
                      {#if a.detail}<span class="ar-detail">{a.detail}</span>{/if}
                    </li>
                  {/each}
                </ul>
              </div>
            {/if}
          </Collapsible>
        {/if}

        {/if}

        <!-- ── Live run trace (Phase 1): the saved agent's last REAL run ──── -->
        {#if loadedAgentId}
          <Collapsible id="run-trace" title="Run trace" sub="Every run of this agent — scheduled or on-demand. Pick one to view its per-block trace.">
            <svelte:fragment slot="actions">
              <button class="btn btn-sm" type="button" on:click={() => loadRunTrace()} disabled={runTraceLoading}>
                {runTraceLoading ? 'Loading…' : '↻ Refresh'}
              </button>
            </svelte:fragment>
            {#if runHistory.length}
              <ul class="run-list">
                {#each runHistory as r (r.runId)}
                  <li>
                    <button class="run-row" class:active={selectedRunId === r.runId} type="button"
                      on:click={() => loadRunTrace(r.runId)}>
                      <span class="run-badge {r.ok ? 'ok' : 'fail'}">{r.ok ? '✓' : '✕'}</span>
                      <span class="run-when">{r.startedAt ? new Date(r.startedAt).toLocaleString() : '—'}</span>
                      {#if r.trigger}<span class="run-trigger">{r.trigger}</span>{/if}
                      <span class="run-steps">{r.steps} step{r.steps === 1 ? '' : 's'}</span>
                      {#if !r.ok && r.error}<span class="run-err">{r.error}</span>{/if}
                    </button>
                  </li>
                {/each}
              </ul>
            {/if}
            {#if runTraceErr}
              <p class="muted">{runTraceErr}</p>
            {/if}
            {#if runDiagnosis}
              <div class="diagnosis-card {runDiagnosis.status || 'empty'}">
                <div class="diagnosis-head">
                  <span class="diagnosis-badge">{runDiagnosis.status || 'unknown'}</span>
                  <strong>{runDiagnosis.summary}</strong>
                </div>
                {#if runDiagnosis.rootCause}
                  <div class="diagnosis-row">
                    <span>Likely cause</span>
                    <p>{runDiagnosis.rootCause}</p>
                  </div>
                {/if}
                {#if runDiagnosis.nextAction}
                  <div class="diagnosis-row">
                    <span>Next action</span>
                    <p>{runDiagnosis.nextAction}</p>
                  </div>
                {/if}
                {#if runDiagnosis.failedNode || runDiagnosis.failedKind}
                  <div class="diagnosis-meta">
                    {#if runDiagnosis.failedNode}<span>Node <code>{runDiagnosis.failedNode}</code></span>{/if}
                    {#if runDiagnosis.failedKind}<span>Kind <code>{runDiagnosis.failedKind}</code></span>{/if}
                    {#if runDiagnosis.retryable}<span>Retry may help</span>{:else if runDiagnosis.status === 'failed'}<span>Needs a workflow/config fix</span>{/if}
                  </div>
                {/if}
                {#if runDiagnosis.suggestions && runDiagnosis.suggestions.length}
                  <ul class="diagnosis-list">
                    {#each runDiagnosis.suggestions as s}
                      <li>{s}</li>
                    {/each}
                  </ul>
                {/if}
                {#if runDiagnosis.evidence && runDiagnosis.evidence.length}
                  <details class="diagnosis-evidence">
                    <summary>Evidence</summary>
                    <ul>
                      {#each runDiagnosis.evidence as e}
                        <li>{e}</li>
                      {/each}
                    </ul>
                  </details>
                {/if}
              </div>
            {/if}
            {#if runTrace && runTrace.entries && runTrace.entries.length}
              <ol class="trace">
                {#each runTrace.entries as step, i}
                  {@const toolErr = stepToolError(step)}
                  <li class:step-failed={!!toolErr}>
                    <span class="step-n">{i + 1}</span>
                    <div class="step-body">
                      <div class="step-head">
                        {#if toolErr}<span class="status-dot fail" data-tooltip="This step failed">✕</span>{:else}<span class="status-dot ok" data-tooltip="This step succeeded">✓</span>{/if}
                        <strong>{step.nodeId}</strong>
                        {#if step.kind}<span class="step-kind">{step.kind}</span>{/if}
                        {#if step.wiredPorts}<span class="wired-badge" data-tooltip="Input assembled from typed port wires — no template">⮑ wired</span>{/if}
                        {#if step.durationMs != null}<span class="dur-badge" data-tooltip="Wall-clock duration">{step.durationMs}ms</span>{/if}
                      </div>
                      {#if toolErr}
                        <div class="step-line err">{toolErr}</div>
                      {:else if stepSummary(step)}
                        <div class="step-line ok">{stepSummary(step)}</div>
                      {/if}
                      <details class="step-details">
                        <summary>input / output</summary>
                        <div class="step-io">
                          <span class="io-label">in</span><pre>{prettyJSON(step.input)}</pre>
                          <span class="io-label">out</span><pre>{prettyJSON(step.output)}</pre>
                          {#if step.error}<span class="io-label">err</span><pre class="io-err">{step.error}</pre>{/if}
                        </div>
                      </details>
                    </div>
                  </li>
                {/each}
              </ol>
            {/if}
          </Collapsible>
        {/if}

        <!-- In-memory history of TEST-bench runs, so workflow-only like the
             bench that produces it. -->
        {#if currentMode === 'workflow'}
        <!-- ── Run history (S5.4): last ~10 runs, IN MEMORY only ──────────── -->
        {#if history.length}
          <Collapsible id="run-history" title="Run history" sub="Session-only — cleared on reload (no storage in the sandbox).">
            <svelte:fragment slot="actions">
              <button class="btn btn-sm" type="button" on:click={clearHistory}>Clear</button>
            </svelte:fragment>
            <ul class="hist-list">
              {#each history as h (h.ts)}
                <li class="hist-item" class:active={activeHistoryId === h.ts}>
                  <button class="hist-main" type="button" on:click={() => viewHistory(h)} title="View this run again">
                    <span class="hist-badge {h.passed ? 'hist-pass' : (h.assertionCount ? 'hist-fail' : 'hist-neutral')}">
                      {h.assertionCount ? (h.passed ? 'pass' : 'fail') : 'run'}
                    </span>
                    <span class="hist-time">{new Date(h.ts).toLocaleTimeString()}</span>
                    <span class="hist-input">“{h.inputSummary}”</span>
                    {#if h.assertionCount}<span class="hist-meta">{h.assertionCount} assertion{h.assertionCount === 1 ? '' : 's'}</span>{/if}
                    {#if h.request.mocks}<span class="hist-meta">mocked</span>{/if}
                  </button>
                  <button class="btn btn-sm" type="button" on:click={() => replayHistory(h)} disabled={testing} title="Re-send this exact request">
                    Replay
                  </button>
                </li>
              {/each}
            </ul>
          </Collapsible>
        {/if}
        {/if}
        {/if}
        </div><!-- /.workbench -->
      {/if}
      {/if}<!-- /step -->
    </section>

    {#if step === STEP_BUILD && showInspector && viewMode === 'canvas' && currentMode === 'workflow'}
      <div
        class="insp-splitter"
        role="separator"
        aria-orientation="vertical"
        data-tooltip="Drag to resize the inspector"
        on:pointerdown={startInspResize}
      ></div>
      <div class="insp-host" style={maximizedFrame === 'inspector' ? '' : `width:${inspectorWidth}px`}>
        <button class="frame-max insp-max" type="button"
          data-tooltip={maximizedFrame === 'inspector' ? 'Restore layout (Esc)' : 'Maximize inspector'}
          on:click={() => toggleMax('inspector')}>{maximizedFrame === 'inspector' ? '⤡' : '⤢'}</button>
        <Inspector
          node={selectedNode}
          {selectedEdge}
          {workflow}
          channels={channelOptions}
          onChange={applyFraming}
          onEdgeChange={applyEdgePatch}
          onAddEdge={addEdge}
          onEdgeDelete={deleteEdge}
          onSetEntry={setEntry}
          onSetOutput={setOutput}
          onRefine={refineNode}
          {refineState}
          onDelete={deleteNode}
          onNodeChange={applyNodePatch}
          {toolParams}
          {scaffolds}
          onGenerateCode={generateNodeCode}
          onCompileGate={compileGate}
          onCompileNode={compileNode}
        />
      </div>
    {/if}
  </main>

  <!-- ── Run Live: side-effect confirmation (ST-11) ────────────────────────
       The server refuses an unacknowledged run with 409 and hands back exactly
       what it would touch. Previously Studio auto-approved every confirmation
       so the run "couldn't hang" — which meant real messages, tickets and
       charges could happen with nothing shown to the user first. -->
  {#if runConfirm}
    <div class="modal-backdrop" on:click|self={cancelRun} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="runconf-title">
        <h2 id="runconf-title" class="modal-title">This run has real side effects</h2>
        <p class="modal-body">
          Run Live executes real tools — it is not a simulation. Review what it will
          touch before continuing.
        </p>

        {#if runConfirmTools.length}
          <div class="rc-group">
            <div class="rc-label">You are approving</div>
            <ul class="rc-list">
              {#each runConfirmTools as t}
                <li>{typeof t === 'string' ? t : (t.name || t.tool)}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if (runConfirmSummary.confirm_tools || []).length}
          <div class="rc-group">
            <div class="rc-label">Needs confirmation</div>
            <ul class="rc-list">
              {#each runConfirmSummary.confirm_tools as t}
                <li>{typeof t === 'string' ? t : (t.name || t.tool)}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if (runConfirmSummary.channel_tools || []).length}
          <div class="rc-group">
            <div class="rc-label">Sends to</div>
            <ul class="rc-list">
              {#each runConfirmSummary.channel_tools as t}
                <li>{typeof t === 'string' ? t : (t.name || t.tool)}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if (runConfirmSummary.network_tools || []).length}
          <div class="rc-group">
            <div class="rc-label">Reaches the network</div>
            <ul class="rc-list">
              {#each runConfirmSummary.network_tools as t}
                <li>{typeof t === 'string' ? t : (t.name || t.tool)}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if (runConfirmSummary.file_tools || []).length}
          <div class="rc-group">
            <div class="rc-label">Reads or writes files</div>
            <ul class="rc-list">
              {#each runConfirmSummary.file_tools as t}
                <li>{typeof t === 'string' ? t : (t.name || t.tool)}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if (runConfirmSummary.privileged_tools || []).length}
          <div class="rc-group danger">
            <div class="rc-label">Privileged</div>
            <ul class="rc-list">
              {#each runConfirmSummary.privileged_tools as t}
                <li>{typeof t === 'string' ? t : (t.name || t.tool)}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if (runConfirmSummary.untrusted_content_sources || []).length}
          <p class="rc-warn">
            This run reads untrusted content, so anything it fetches can attempt to
            influence what the agent does next.
          </p>
        {/if}

        <div class="modal-actions">
          <button class="btn" type="button" on:click={cancelRun}>Cancel</button>
          <button class="btn primary" type="button" on:click={confirmRun}>
            Run with these side effects
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- ── M6: Template picker (S6.1) ──────────────────────────────────────── -->
  {#if templatePicker.open}
    <div class="modal-backdrop" on:click|self={closeTemplates} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="tmpl-title">
        <h2 id="tmpl-title" class="modal-title">Start from a template</h2>
        <p class="modal-body">Pick a starting point — it loads as your current draft, ready to edit, test and save.</p>
        {#if templatePicker.loading}
          <p class="muted">Loading templates…</p>
        {:else if templatePicker.error}
          <div class="strip strip-error">⚠ {templatePicker.error}</div>
        {:else if !templatePicker.items.length}
          <p class="muted">No templates available.</p>
        {:else}
          <ul class="picker-list">
            {#each templatePicker.items as t (t.id)}
              <li class="picker-item">
                <button class="picker-main" type="button" on:click={() => chooseTemplate(t)} title="Load this template">
                  <span class="picker-name">{t.name || t.id}</span>
                  {#if t.description}<span class="picker-desc">{t.description}</span>{/if}
                </button>
              </li>
            {/each}
          </ul>
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={closeTemplates}>Close</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- ── My Workflows: published agent workflows + drafts ──────────────────── -->
  {#if library.open}
    <div class="modal-backdrop" on:click|self={closeLibrary} role="presentation">
      <div class="modal library-modal" role="dialog" aria-modal="true" aria-labelledby="lib-title">
        <div class="lib-head">
          <h2 id="lib-title" class="modal-title">My Workflows</h2>
          <button class="btn primary btn-sm" type="button" on:click={newWorkflow}>+ New workflow</button>
        </div>
        {#if library.error}<div class="strip strip-error">⚠ {library.error}</div>{/if}
        {#if library.loading}
          <p class="muted">Loading…</p>
        {:else}
          <!-- Search + facets. Facet options are derived from what is actually
               present, so a dropdown never offers a value that matches nothing. -->
          <div class="lib-filters">
            <input
              class="lib-search"
              type="search"
              placeholder="Search by name, description, integration…"
              bind:value={libQuery.text}
              aria-label="Search workflows"
            />
            <select bind:value={libQuery.status} aria-label="Filter by status">
              <option value="">Any status</option>
              <option value="deployed">Deployed</option>
              <option value="saved">Saved</option>
              <option value="draft">Draft</option>
            </select>
            {#if libFacets.triggers.length}
              <select bind:value={libQuery.trigger} aria-label="Filter by trigger">
                <option value="">Any trigger</option>
                {#each libFacets.triggers as t}<option value={t}>{t}</option>{/each}
              </select>
            {/if}
            {#if libFacets.strategies.length}
              <select bind:value={libQuery.strategy} aria-label="Filter by strategy">
                <option value="">Any strategy</option>
                {#each libFacets.strategies as s}<option value={s}>{s}</option>{/each}
              </select>
            {/if}
            {#if libFacets.integrations.length}
              <select bind:value={libQuery.integration} aria-label="Filter by integration">
                <option value="">Any integration</option>
                {#each libFacets.integrations as i}<option value={i}>{i}</option>{/each}
              </select>
            {/if}
            {#if libFacets.owners.length}
              <select bind:value={libQuery.owner} aria-label="Filter by owner">
                <option value="">Any owner</option>
                {#each libFacets.owners as o}<option value={o}>{o}</option>{/each}
              </select>
            {/if}
            {#if libFiltered}
              <button class="btn btn-sm" type="button" on:click={clearLibFilters}>Clear</button>
            {/if}
          </div>
          {#if libFiltered}
            <p class="muted lib-count">{libMatchCount} match{libMatchCount === 1 ? '' : 'es'}</p>
          {/if}

          {#if libFiltered && libMatchCount === 0}
            <p class="muted">Nothing matches those filters.</p>
          {/if}

          <!-- Deployed: live and running on its trigger. Kept separate from
               merely-saved because the two have opposite operational meaning. -->
          {#if libDeployed.length || (!libFiltered && !library.agents.length)}
            <h3 class="lib-section">Deployed</h3>
          {/if}
          {#if !library.agents.length && !libFiltered}
            <p class="muted">Nothing deployed yet. Build a workflow, Save it, then Deploy.</p>
          {:else if libDeployed.length}
            <ul class="picker-list">
              {#each libDeployed as a (a.id)}
                <li class="picker-item lib-item">
                  <button class="picker-main" type="button" on:click={() => loadAgentForEdit(a)} disabled={!!library.busyId} title="Edit this workflow">
                    <span class="picker-name">
                      {a.name || a.id}
                      <span class="agent-badge on">deployed</span>
                    </span>
                    <span class="picker-desc">{a.description || (a.strategy
                      ? recoAgentLabel(a.strategy) + ' · ' + (a.trigger || 'manual')
                      : (a.trigger + ' · ' + a.nodes + ' step' + (a.nodes === 1 ? '' : 's')))}</span>
                  </button>
                  <div class="lib-actions">
                    <button class="btn btn-sm" type="button" on:click={() => loadAgentForEdit(a)} disabled={!!library.busyId}>
                      {library.busyId === a.id ? '…' : 'Edit'}
                    </button>
                    <button class="btn btn-sm" type="button" on:click={() => cloneFromLibrary(a)} disabled={!!library.busyId} title="Open a copy under a new name">Clone</button>
                    <button class="btn btn-sm" type="button" on:click={() => testFromLibrary(a)} disabled={!!library.busyId} title="Load it and run the test bench">Test</button>
                    <button class="btn btn-sm" type="button" on:click={() => exportFromLibrary(a)} disabled={!!library.busyId} title="Download its SOUL.yaml">Export</button>
                    <button class="btn btn-sm" type="button" on:click={() => setDeployed(a, false)} disabled={!!library.busyId} title="Stop it running on its trigger">Undeploy</button>
                    <button class="btn btn-sm" type="button" on:click={() => deleteAgentWorkflow(a)} disabled={!!library.busyId} title="Delete this agent">Delete</button>
                  </div>
                </li>
              {/each}
            </ul>
          {/if}

          <!-- Saved: a real, stored workflow that is NOT running. -->
          {#if libSaved.length}
            <h3 class="lib-section">Saved workflows</h3>
            <ul class="picker-list">
              {#each libSaved as a (a.id)}
                <li class="picker-item lib-item">
                  <button class="picker-main" type="button" on:click={() => loadAgentForEdit(a)} disabled={!!library.busyId} title="Edit this workflow">
                    <span class="picker-name">
                      {a.name || a.id}
                      <span class="agent-badge off">not deployed</span>
                    </span>
                    <span class="picker-desc">{a.description || (a.strategy
                      ? recoAgentLabel(a.strategy) + ' · ' + (a.trigger || 'manual')
                      : (a.trigger + ' · ' + a.nodes + ' step' + (a.nodes === 1 ? '' : 's')))}</span>
                  </button>
                  <div class="lib-actions">
                    <button class="btn btn-sm" type="button" on:click={() => loadAgentForEdit(a)} disabled={!!library.busyId}>
                      {library.busyId === a.id ? '…' : 'Edit'}
                    </button>
                    <button class="btn btn-sm" type="button" on:click={() => cloneFromLibrary(a)} disabled={!!library.busyId}>Clone</button>
                    <button class="btn btn-sm" type="button" on:click={() => testFromLibrary(a)} disabled={!!library.busyId}>Test</button>
                    <button class="btn btn-sm" type="button" on:click={() => exportFromLibrary(a)} disabled={!!library.busyId}>Export</button>
                    <button class="btn btn-sm primary" type="button" on:click={() => setDeployed(a, true)} disabled={!!library.busyId} title="Start running it on its trigger">Deploy</button>
                    <button class="btn btn-sm" type="button" on:click={() => deleteAgentWorkflow(a)} disabled={!!library.busyId}>Delete</button>
                  </div>
                </li>
              {/each}
            </ul>
          {/if}

          <!-- Work-in-progress drafts -->
          {#if !libFiltered || libDrafts.length}
            <h3 class="lib-section">Drafts</h3>
          {/if}
          {#if !library.drafts.length && !libFiltered}
            <p class="muted">No saved drafts. Use “Save draft” to keep a work in progress.</p>
          {:else if libDrafts.length}
            <ul class="picker-list">
              {#each libDrafts as d (d.id)}
                <li class="picker-item lib-item">
                  <button class="picker-main" type="button" on:click={() => loadDraft(d)} disabled={!!library.busyId} title="Load this draft">
                    <span class="picker-name">{d.name || d.id}</span>
                    {#if draftWhen(d)}<span class="picker-desc">updated {draftWhen(d)}</span>{/if}
                  </button>
                  <div class="lib-actions">
                    <button class="btn btn-sm" type="button" on:click={() => loadDraft(d)} disabled={!!library.busyId}>
                      {library.busyId === d.id ? '…' : 'Load'}
                    </button>
                    <button class="btn btn-sm" type="button" on:click={() => cloneFromLibrary(d)} disabled={!!library.busyId}>Clone</button>
                    <button class="btn btn-sm" type="button" on:click={() => testFromLibrary(d)} disabled={!!library.busyId}>Test</button>
                    <button class="btn btn-sm" type="button" on:click={() => exportFromLibrary(d)} disabled={!!library.busyId}>Export</button>
                    <button class="btn btn-sm" type="button" on:click={() => deleteDraft(d)} disabled={!!library.busyId} title="Delete this draft">Delete</button>
                  </div>
                </li>
              {/each}
            </ul>
          {/if}
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={closeLibrary}>Close</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- ── Browse SOUL.yaml: read-only viewer for every agent ────────────────── -->
  {#if yamlBrowser.open}
    <div class="modal-backdrop" on:click|self={closeYamlBrowser} role="presentation">
      <div class="modal yaml-browser" role="dialog" aria-modal="true" aria-labelledby="yamlb-title">
        <h2 id="yamlb-title" class="modal-title">Browse SOUL.yaml</h2>
        <p class="modal-body">The raw SOUL.yaml of any agent, read straight from disk. Read-only — edit on the canvas or in the code view.</p>
        {#if yamlBrowser.error}<div class="strip strip-error">⚠ {yamlBrowser.error}</div>{/if}
        {#if yamlBrowser.loading}
          <p class="muted">Loading agents…</p>
        {:else if !yamlBrowser.agents.length}
          <p class="muted">No agents registered yet.</p>
        {:else}
          <div class="yamlb-split">
            <ul class="picker-list yamlb-list">
              {#each yamlBrowser.agents as a (a.id)}
                <li class="picker-item">
                  <button class="picker-main" type="button" class:selected={yamlBrowser.selectedId === a.id}
                          on:click={() => viewAgentYaml(a.id)} title="View this agent's SOUL.yaml">
                    <span class="picker-name">
                      {a.name || a.id}
                      <span class="agent-badge {a.enabled ? 'on' : 'off'}">{a.enabled ? 'enabled' : 'disabled'}</span>
                    </span>
                    <span class="picker-desc">{a.id}</span>
                  </button>
                </li>
              {/each}
            </ul>
            <div class="yamlb-view">
              {#if yamlBrowser.yamlError}
                <div class="strip strip-error">⚠ {yamlBrowser.yamlError}</div>
              {:else if yamlBrowser.yamlLoading}
                <p class="muted">Loading SOUL.yaml…</p>
              {:else if yamlBrowser.yaml}
                {#if yamlBrowser.path}<div class="yamlb-path" data-tooltip={yamlBrowser.path}>{yamlBrowser.path}</div>{/if}
                <pre class="yamlb-code">{yamlBrowser.yaml}</pre>
              {:else}
                <p class="muted">Select an agent to view its SOUL.yaml.</p>
              {/if}
            </div>
          </div>
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={closeYamlBrowser}>Close</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Studio model picker: choose the in-framework provider/model for Studio. -->
  {#if modelPicker.open}
    <div class="modal-backdrop" on:click|self={() => modelPicker = { ...modelPicker, open: false }} role="presentation">
      <div class="modal model-modal" role="dialog" aria-modal="true" aria-labelledby="model-title">
        <h2 id="model-title" class="modal-title">
          {modelPicker.target === 'agent' ? 'Model this agent runs on' : 'Studio model'}
        </h2>
        <p class="modal-body">
          {#if modelPicker.target === 'agent'}
            Which provider/model should the saved agent execute on? This is
            written into its SOUL.yaml. It is a different choice from the model
            Studio uses to build it — leave the provider blank to inherit the
            workspace default instead of pinning one.
          {:else}
            Which provider/model should Studio use for its reasoning and code
            generation? Uses your configured providers — leave provider blank to
            use the workspace default.
          {/if}
        </p>
        {#if modelPicker.error}<div class="strip strip-error">⚠ {modelPicker.error}</div>{/if}
        {#if modelPickerUsesNVIDIA}<div class="strip strip-warn nvidia-limit-warning">⚠ NVIDIA’s hosted Developer endpoint is for prototyping and may enforce model- and account-dependent token, rate, or availability limits. Use workspace budgets and an entitled or self-hosted endpoint for production.</div>{/if}
        <label class="field-label" for="mp-provider">provider</label>
        <select id="mp-provider" value={modelPicker.provider} on:change={(e) => pickProvider(e.target.value)}>
          <option value="">(default provider)</option>
          {#each providerOptions as p}<option value={p}>{p}</option>{/each}
        </select>
        <span class="field-label">model</span>

        {#if !modelPicker.provider}
          <p class="mp-hint">Choose a provider above to see its models.</p>
        {:else}
          <!-- Filter only appears once there are enough models to be worth
               filtering; below that it is just a box in the way. -->
          {#if (modelPicker.models || []).length > 6}
            <input
              class="mp-filter"
              type="search"
              autocomplete="off"
              placeholder="Filter {modelPicker.models.length} models…"
              aria-label="Filter models"
              bind:value={modelFilter}
            />
          {/if}

          <div class="mp-list" role="radiogroup" aria-label="Model">
            <!-- Blank model = the provider's own default. It is a real choice,
                 so it belongs in the list rather than hidden in placeholder text. -->
            <button
              type="button"
              class="mp-row"
              class:sel={!modelPicker.model}
              role="radio"
              aria-checked={!modelPicker.model}
              on:click={() => chooseModel('')}
            >
              <span class="mp-tick">{!modelPicker.model ? '✓' : ''}</span>
              <span class="mp-name muted">Provider default</span>
            </button>

            {#each modelChoices as m}
              <button
                type="button"
                class="mp-row"
                class:sel={m === modelPicker.model}
                role="radio"
                aria-checked={m === modelPicker.model}
                on:click={() => chooseModel(m)}
              >
                <span class="mp-tick">{m === modelPicker.model ? '✓' : ''}</span>
                <span class="mp-name">{m}</span>
              </button>
            {/each}

            {#if modelPicker.modelsLoading}
              <p class="mp-empty">Loading models…</p>
            {:else if (modelPicker.models || []).length === 0}
              <p class="mp-empty">This provider reported no models. Enter one by hand below.</p>
            {:else if modelChoices.length === 0}
              <p class="mp-empty">No model matches “{modelFilter.trim()}”.</p>
            {/if}
          </div>

          <!-- Escape hatch: a model the provider does not advertise. Collapsed
               by default so it does not compete with the list. -->
          {#if manualModel}
            <input
              class="mp-manual"
              autocomplete="off"
              placeholder="e.g. llama3.1:70b"
              aria-label="Model name"
              bind:value={modelPicker.model}
            />
          {:else}
            <button type="button" class="mp-link" on:click={() => { manualModel = true }}>
              Model not listed? Enter it manually
            </button>
          {/if}
        {/if}
        <div class="mp-settings-grid">
        {#if studioPresets.length}
          <div class="mp-section">
            <div class="mp-label" style="font-weight:600;margin-bottom:.25rem;">Runtime intent (optional)</div>
            <p class="mp-hint" style="margin:.25rem 0;font-size:.85em;opacity:.75;">
              Overrides the model-derived default timeouts. Leave on "Model default" if you don't need to bias for speed or quality.
            </p>
            <div class="mp-preset-list" style="display:flex;flex-direction:column;gap:.25rem;">
              <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
                <input
                  type="radio"
                  name="studio-preset"
                  value=""
                  checked={studioPresetCurrent === ''}
                  on:change={() => saveStudioPreset('')}
                  disabled={studioPresetSaving}
                />
                <div>
                  <div><strong>Model default</strong></div>
                  <div style="font-size:.85em;opacity:.7;">Studio picks patient timeouts based on the model tier (compact vs capable).</div>
                </div>
              </label>
              {#each studioPresets as p}
                <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
                  <input
                    type="radio"
                    name="studio-preset"
                    value={p.name}
                    checked={studioPresetCurrent === p.name}
                    on:change={() => saveStudioPreset(p.name)}
                    disabled={studioPresetSaving}
                  />
                  <div>
                    <div>
                      <strong>{p.label}</strong>
                      {#if p.default}<span style="font-size:.75em;opacity:.6;margin-left:.35rem;">(recommended)</span>{/if}
                    </div>
                    <div style="font-size:.85em;opacity:.7;">{p.detail}</div>
                  </div>
                </label>
              {/each}
            </div>
            {#if studioPresetError}
              <div class="mp-error" style="color:var(--err, #c33);font-size:.85em;margin-top:.25rem;">{studioPresetError}</div>
            {/if}
          </div>
        {/if}
        <div class="mp-section">
          <div class="mp-label" style="font-weight:600;margin-bottom:.25rem;">Generate presentation</div>
          <p class="mp-hint" style="margin:.25rem 0;font-size:.85em;opacity:.75;">
            How Studio surfaces the generate pipeline. Streamed shows a live
            transcript beside the canvas; Wizard opens a stepped modal that
            pauses between clarify → strategy → build. Per-generation override
            is available on the toggle next to the Generate button.
          </p>
          <div class="mp-preset-list" style="display:flex;flex-direction:column;gap:.25rem;">
            <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
              <input type="radio" name="build-ux" value="streamed" checked={buildUX === 'streamed'} on:change={() => saveBuildUX('streamed')} disabled={buildUXSaving} />
              <div>
                <div><strong>Streamed</strong> <span style="font-size:.75em;opacity:.6;margin-left:.35rem;">(default)</span></div>
                <div style="font-size:.85em;opacity:.7;">One-click generate with a live-transcript panel that shows each pipeline phase as it runs.</div>
              </div>
            </label>
            <label class="mp-preset-row" style="display:flex;gap:.5rem;align-items:flex-start;">
              <input type="radio" name="build-ux" value="wizard" checked={buildUX === 'wizard'} on:change={() => saveBuildUX('wizard')} disabled={buildUXSaving} />
              <div>
                <div><strong>Wizard</strong></div>
                <div style="font-size:.85em;opacity:.7;">Stepped modal that pauses between phases so you can review the clarified intent and chosen strategy before the graph is built.</div>
              </div>
            </label>
          </div>
        </div>
        </div>
        <div class="modal-actions">
          <button class="btn" type="button" on:click={() => modelPicker = { ...modelPicker, open: false }} disabled={modelPicker.saving}>Cancel</button>
          <button class="btn primary" type="button" on:click={saveStudioModel} disabled={modelPicker.saving}>
            {modelPicker.saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Generate progress modal — a centered overlay with a phase stepper so the
       user can see what the (often slow) build pipeline is doing instead of
       staring at an empty canvas. Dismissible to run in the background. -->
  {#if genBusy && !pipelineModalHidden}
    <div class="modal-backdrop" role="presentation">
      <div class="modal gen-progress-modal" role="dialog" aria-modal="true"
           aria-labelledby="gen-progress-title" aria-live="polite">
        <h2 id="gen-progress-title" class="modal-title">
          {(refining || modalRefining) && !compiling && !pipelineRunning
            ? 'Refining your prompt'
            : 'Generating your workflow'}
        </h2>
        <div class="gen-status">
          <span class="spinner" aria-hidden="true"></span>
          <span>{genStatusText}</span>
          <!-- Elapsed ticks locally, so a long phase that emits no events still
               shows time moving. A frozen counter is exactly what "is this
               stuck?" looks like. -->
          <span class="gen-elapsed">{(pipelineElapsed / 1000).toFixed(1)}s</span>
        </div>

        {#if pipelineNodes.length}
          <!-- The graph as it arrives, rather than appearing whole at the end. -->
          <div class="gen-nodes">
            <div class="gen-nodes-head">
              Steps built: {pipelineNodes.length}{#if pipelineNodes[0] && pipelineNodes[0].total} of {pipelineNodes[0].total}{/if}
            </div>
            <ul>
              {#each pipelineNodes as n}
                <li><span class="gen-node-id">{n.id}</span>{#if n.label}<span class="gen-node-label">{n.label}</span>{/if}</li>
              {/each}
            </ul>
          </div>
        {/if}

        {#if pipelineLog.length}
          <!-- Real events streamed by the build pipeline — the actual phase and
               the server's own message for it (refined spec, chosen strategy,
               node/agent counts, validation verdict, repairs). Newest last. -->
          <ul class="gen-events">
            {#each pipelineLog as ev}
              <li class="gen-ev gen-ev-{ev.status}">
                <!-- Who did this step. The transcript rendered planner and model
                     rows identically, so a user could not tell which decisions
                     were rule-based and which a model made. -->
                <span class="gen-ev-src gen-src-{ev.source || 'planner'}">
                  {ev.source === 'llm' ? 'model' : 'rules'}
                </span>
                <span class="gen-ev-phase">{phaseLabel(ev.phase)}</span>
                {#if ev.message}<span class="gen-ev-msg">{ev.message}</span>{/if}
                {#if typeof ev.elapsed_ms === 'number'}
                  <span class="gen-ev-at">{(ev.elapsed_ms / 1000).toFixed(1)}s</span>
                {/if}
              </li>
            {/each}
          </ul>
        {:else}
          <p class="gen-latest">
            This calls the builder model ({studioModelLabel || 'your builder model'}). A
            complex prompt can take a minute — switch to Streamed mode to watch each
            phase as it runs.
          </p>
        {/if}
        <div class="modal-actions">
          <button class="btn" type="button" on:click={() => (pipelineModalHidden = true)}>Run in background</button>
          {#if pipelineRunning}
            <!-- Actually stops the run. Aborting drops the SSE connection, which
                 the server detects and uses to cancel the pipeline — previously
                 the only option was to hide the modal and let it keep going. -->
            <button class="btn" type="button" on:click={cancelGenerate}>Cancel</button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Consent dialog (M2): shown before saving a privileged, channel-bound
       workflow, or on the server's 409 consent fallback. -->
  <!-- Full prompt viewer/editor — the top bar truncates long prompts. -->
  {#if promptViewer}
    <div class="modal-backdrop" on:click|self={() => (promptViewer = false)} role="presentation">
      <!-- Describe step: the prompt on the left, what Studio understood on the
           right. The two panes are side by side because verifying the reading
           against the words that produced it is the whole point — separating
           them means the user checks the spec from memory. -->
      <div class="modal prompt-modal describe-modal" role="dialog" aria-modal="true" aria-labelledby="prompt-title">
        <h2 id="prompt-title" class="modal-title">Describe your workflow</h2>

        {#if promptError}<div class="strip strip-error">⚠ {promptError}</div>{/if}

        <div class="describe-cols">
          <div class="describe-left">
            <label class="pe-label" for="pe-raw">Your prompt (original)</label>
            <p class="pe-hint">What you want, in your own words. Edit it and click <strong>Refine</strong> to turn it into a detailed, build-ready spec.</p>
            <textarea id="pe-raw" class="pe-area" bind:value={rawPrompt}
              placeholder="e.g. Every morning, find the top AI news, make a NotebookLM podcast, and post the link to Telegram."></textarea>
            <div class="pe-row">
              <button class="btn" on:click={refineFromModal} disabled={modalRefining || !rawPrompt.trim()}>
                {modalRefining ? 'Refining…' : '✨ Refine →'}
              </button>
            </div>

            <label class="pe-label" for="pe-refined">Refined prompt</label>
            <p class="pe-hint">The detailed specification Studio builds from. Edit it directly if you like, then <strong>Generate</strong>.</p>
            <textarea id="pe-refined" class="pe-area" bind:value={intent}
              placeholder="Appears here after you Refine — or write/paste a detailed prompt directly."></textarea>
          </div>

          <div class="describe-right">
            <BuildSpecPanel
              spec={effectiveBuildSpec}
              recommendation={specRecommendation}
              loading={buildSpecLoading}
              error={buildSpecError}
              answers={refineAnswers}
              channels={catalog && catalog.channels}
              {generationTrigger}
              {generationTriggerError}
              onAnswer={(id, v) => (refineAnswers = { ...refineAnswers, [id]: v })}
              onGenerationTrigger={(value) => (generationTrigger = value)}
              onRefine={refineFromModal}
              onGenerate={generateFromModal}
              refining={modalRefining || refining}
              generating={compiling || pipelineRunning}
            />
          </div>
        </div>

        <div class="modal-actions">
          <button class="btn" on:click={() => (promptViewer = false)}>Close</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- SOUL.yaml authoring rules editor -->
  {#if rulesOpen}
    <div class="modal-backdrop" on:click|self={() => (rulesOpen = false)} role="presentation">
      <div class="modal prompt-modal" role="dialog" aria-modal="true" aria-labelledby="rules-title">
        <h2 id="rules-title" class="modal-title">SOUL.yaml Rules</h2>
        <p class="pe-hint">
          These rules are auto-injected whenever Studio <strong>generates</strong>, and when you run <strong>Fix with AI</strong>.
          Edit or add rules, and list your tools' input/output shapes under “Tier 2”. Saved to your workspace.
          {#if rulesIsDefault}<em>(currently using the built-in default — saving creates your own copy)</em>{/if}
        </p>
        {#if rulesLoading}
          <div class="canvas-state">Loading…</div>
        {:else}
          <textarea class="pe-area rules-area" bind:value={rulesText} spellcheck="false" placeholder="Authoring rules…"></textarea>
        {/if}
        <div class="modal-actions">
          <button class="btn" on:click={resetRulesToDefault} data-tooltip="Replace the editor contents with the built-in default rules">Reset to default</button>
          <button class="btn" on:click={() => (rulesOpen = false)}>Close</button>
          <button class="btn primary" on:click={saveRules} disabled={rulesSaving || rulesLoading}>{rulesSaving ? 'Saving…' : 'Save'}</button>
          {#if rulesMsg}<span class="save-msg" class:ok={rulesMsg.startsWith('✓')}>{rulesMsg}</span>{/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Name a draft. An in-app dialog rather than window.prompt(), which blocks
       the whole page until dismissed and is disabled outright in some embedded
       contexts. -->
  {#if draftPrompt}
    <div class="modal-backdrop" on:click|self={() => (draftPrompt = null)} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="draftname-title">
        <h2 id="draftname-title" class="modal-title">Save draft as</h2>
        <label class="save-field">
          <span>Draft name</span>
          <!-- svelte-ignore a11y-autofocus -->
          <input type="text" autofocus bind:value={draftPrompt.name}
            on:keydown={(e) => { if (e.key === 'Enter') confirmSaveDraft(); if (e.key === 'Escape') draftPrompt = null }} />
        </label>
        <div class="modal-actions">
          <button class="btn" type="button" on:click={() => (draftPrompt = null)}>Cancel</button>
          <button class="btn primary" type="button" on:click={confirmSaveDraft}
            disabled={savingDraft || !String(draftPrompt.name || '').trim()}>
            {savingDraft ? 'Saving…' : 'Save draft'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Cloud-escalation gate: ask before a prompt leaves the machine. -->
  {#if cloudGate}
    <div class="modal-backdrop" on:click|self={() => (cloudGate = null)} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="cloud-title">
        <h2 id="cloud-title" class="modal-title">Use a cloud model for this build?</h2>
        <p class="modal-body">
          Your Studio builder is <strong>{cloudGate.provider} / {cloudGate.model}</strong>, a cloud
          model. To generate this agent, your prompt and your Soulacy setup context
          (skills, tools, channels, etc.) will be sent to <strong>{cloudGate.provider}</strong>.
          Soulacy is local-first — using the cloud is always your choice.
        </p>
        <div class="modal-actions">
          <button class="btn" on:click={declineCloud}>Keep it local (choose a local model)</button>
          <button class="btn primary" on:click={approveCloud}>Continue with {cloudGate.provider}</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Pre-save validation: blockers must be fixed; warnings can be saved over. -->
  {#if preflight}
    <div class="modal-backdrop" on:click|self={cancelPreflight} role="presentation">
      <div class="modal refine-modal preflight-modal" role="dialog" aria-modal="true" aria-labelledby="preflight-title">
        <h2 id="preflight-title" class="modal-title">
          {#if preflight.blockers && preflight.blockers.length}Fix these before saving{:else}Ready to save — a few warnings{/if}
        </h2>
        <p class="modal-body">
          Studio checked this agent against your live setup and generation contract
          (tools, MCP servers, channels, schedule, required inputs, graph shape,
          and authoring rules).
        </p>
        {#if preflightFixMsg}
          <div class={`pf-fix-status pf-${preflightFixKind}`}>{preflightFixMsg}</div>
        {/if}

        {#if preflight.blockers && preflight.blockers.length}
          <div class="refine-section">
            <div class="refine-heading">Blockers ({preflight.blockers.length})</div>
            <ul class="pf-list">
              {#each preflight.blockers as b}
                <li>
                  {#if b.nodeId}
                    <button class="pf-item pf-block pf-clickable"
                        type="button"
                        on:click={() => revealNode(b.nodeId)}
                        title="Click to open this block on the canvas">
                      <div class="pf-msg">{b.message}</div>
                      {#if b.fix}<div class="pf-fix">→ {b.fix}</div>{/if}
                      <div class="pf-node">block: {b.nodeId} — click to open ↗</div>
                    </button>
                  {:else}
                    <div class="pf-item pf-block">
                      <div class="pf-msg">{b.message}</div>
                      {#if b.fix}<div class="pf-fix">→ {b.fix}</div>{/if}
                    </div>
                  {/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        {#if preflight.warnings && preflight.warnings.length}
          <div class="refine-section">
            <div class="refine-heading">Warnings ({preflight.warnings.length})</div>
            <ul class="pf-list">
              {#each preflight.warnings as w}
                <li>
                  {#if w.nodeId}
                    <button class="pf-item pf-warn pf-clickable"
                        type="button"
                        on:click={() => revealNode(w.nodeId)}
                        title="Click to open this block on the canvas">
                      <div class="pf-msg">{w.message}</div>
                      {#if w.fix}<div class="pf-fix">→ {w.fix}</div>{/if}
                      <div class="pf-node">block: {w.nodeId} — click to open ↗</div>
                    </button>
                  {:else}
                    <div class="pf-item pf-warn">
                      <div class="pf-msg">{w.message}</div>
                      {#if w.fix}<div class="pf-fix">→ {w.fix}</div>{/if}
                    </div>
                  {/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <!-- F-GUI-3 — surface security recommendations inline in the preflight
             modal so operators can accept the rewrite without leaving the save
             flow. Only unambiguous tool-name suggestions get an Apply button
             (see applySecurityRecommendation). -->
        {#if preflight.security && (preflight.security.recommendations || []).length}
          <div class="refine-section">
            <div class="refine-heading">Security recommendations ({(preflight.security.recommendations || []).length})</div>
            <ul class="pf-list">
              {#each preflight.security.recommendations as rec}
                <li>
                  <div class="pf-item pf-warn">
                    <div class="pf-msg">Replace <code>{rec.from}</code> with <code>{rec.suggest}</code></div>
                    {#if rec.reason}<div class="pf-fix">{rec.reason}</div>{/if}
                    <button class="btn btn-sm" type="button" style="margin-top:.35rem;"
                            on:click={() => applySecurityRecommendation(rec)}>Apply</button>
                  </div>
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <!-- Server-side readiness: one verdict over preflight + contract +
             security + consent, including any section that could NOT be
             evaluated. Rendered above the legacy lists during the transition;
             those lists come out once this is verified against a live server. -->
        <ReadinessPanel
          report={readiness}
          loading={readinessLoading}
          error={readinessError}
          busy={saving || fixing}
          destinations={readinessDestinationOptions}
          onRecheck={loadReadiness}
          onAction={readinessAction}
          onReveal={revealNode}
          onDestination={chooseReadinessDestination}
        />

        <!-- Saving past warnings is a deliberate binary acknowledgement. -->
        {#if !preflightHasBlockers && preflightHasWarnings}
          <fieldset class="warning-acceptance" disabled={saving || fixing}>
            <legend>Are these warnings acceptable?</legend>
            <label class:chosen={acceptWarnings === 'yes'}>
              <input type="radio" name="warning-acceptance" value="yes" bind:group={acceptWarnings} />
              Yes
            </label>
            <label class:chosen={acceptWarnings === 'no'}>
              <input type="radio" name="warning-acceptance" value="no" bind:group={acceptWarnings} />
              No
            </label>
            {#if acceptWarnings === 'no'}
              <span class="warning-choice-help">Return to editing or fix the warnings before saving.</span>
            {/if}
          </fieldset>
        {/if}
        <div class="modal-actions">
          <button class="btn" on:click={cancelPreflight} disabled={saving || fixing}>Back to editing</button>
          <button class="btn" on:click={fixAutomatically} disabled={saving || fixing} data-tooltip="Run deterministic Studio repair, then re-check contract, wiring, and security issues">
            {fixing ? 'Fixing…' : 'Fix automatically'}
          </button>
          <button class="btn" on:click={rerunPreflight} disabled={saving || fixing || rechecking}>
            {rechecking ? 'Re-checking…' : 'Re-check'}
          </button>
          {#if !preflightHasBlockers}
            <button class="btn primary" on:click={proceedAfterPreflight}
              disabled={saving || fixing || !canProceedPastPreflight}
              data-tooltip={canProceedPastPreflight ? '' : 'Select Yes to acknowledge these warnings'}>
              <!-- "Save anyway" only makes sense when something is being
                   overridden. With a clean report it is just Save. -->
              {preflightHasWarnings ? 'Save anyway' : 'Save'}
            </button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Pre-generation prompt refinement: confirm what will be built before a
       workflow is generated. Shows the clarified spec (editable), the
       assumptions the analyst made, and any clarifying questions. -->
  {#if refinement}
    <div class="modal-backdrop" on:click|self={cancelRefinement} role="presentation">
      <div class="modal refine-modal" role="dialog" aria-modal="true" aria-labelledby="refine-title">
        <h2 id="refine-title" class="modal-title">
          {effectiveBuildUX === 'wizard' ? 'Wizard — review each phase' : 'Confirm what to build'}
        </h2>
        {#if effectiveBuildUX === 'wizard'}
          <!--
            Story 9 M (Cohort C): wizard mode makes the pipeline phases visible
            in the existing refinement modal. The modal already covers phases
            1 (clarify) and 2 (strategy); phase 3 (build) fires on the Generate
            button. Validate + Repair are shown in the Build Report after
            generation runs. The transcript panel is available for a
            live-view companion in either mode.
          -->
          <div class="wizard-steps" style="display:flex;gap:.5rem;font-size:.85em;opacity:.85;margin-bottom:.5rem;">
            <span><strong>1.</strong> Clarify intent</span>
            <span aria-hidden="true">→</span>
            <span><strong>2.</strong> Choose strategy</span>
            <span aria-hidden="true">→</span>
            <span><strong>3.</strong> Build graph</span>
            <span aria-hidden="true">→</span>
            <span style="opacity:.55;"><strong>4.</strong> Validate</span>
            <span aria-hidden="true">→</span>
            <span style="opacity:.55;"><strong>5.</strong> Repair</span>
          </div>
        {/if}
        <p class="modal-body">
          Here's how your request was understood. Review it, fix anything that's
          off, then generate. A clearer spec means a better workflow with fewer errors.
        </p>

        {#if modelAdvice && modelAdvice.severity && modelAdvice.severity !== 'ok'}
          <div class="refine-modelwarn">
            ⚠ {modelAdvice.message}
            {#if modelAdvice.recommendation}<div class="refine-modelrec">{modelAdvice.recommendation}</div>{/if}
          </div>
        {/if}

        {#if refinement.summary}
          <div class="refine-summary">{refinement.summary}</div>
        {/if}

        {#if refinement.recommended_mode}
          <div class="refine-mode">
            <strong>Recommended build: {recoAgentLabel(generatedMode(refinement.recommended_mode))}</strong>
            {#if refinement.recommended_reason} — {refinement.recommended_reason}{/if}
            {#if refinement.recommended_mode === 'workflow'}
              <div class="refine-mode-sub">Studio will use a safe agent strategy by default. AI-generated workflows are experimental and require an explicit opt-in from the strategy panel.</div>
            {:else}
              <div class="refine-mode-sub">This task reasons over its tools (looping/polling), so Studio will build a reasoning agent instead of a fixed flow canvas.</div>
            {/if}
          </div>
        {/if}

        <label class="refine-label" for="refine-intent">Refined prompt — edit it in the editor below before generating</label>
        <textarea
          id="refine-intent"
          class="refine-textarea refine-editor"
          rows="14"
          bind:value={refinement.refined_intent}
        ></textarea>

        {#if refinement.assumptions && refinement.assumptions.length}
          <div class="refine-section">
            <div class="refine-heading">Assumptions made (edit the spec above to change)</div>
            <ul class="refine-assumptions">
              {#each refinement.assumptions as a}
                <li>{a}</li>
              {/each}
            </ul>
          </div>
        {/if}

        {#if refinement.questions && refinement.questions.length}
          <div class="refine-section">
            <div class="refine-heading">A few questions to get this right</div>
            {#each refinement.questions as q}
              <div class="refine-q">
                <label class="refine-qtext" for={`refine-q-${q.id}`}>{q.text}</label>
                {#if q.options && q.options.length}
                  <select id={`refine-q-${q.id}`} bind:value={refineAnswers[q.id]}>
                    <option value="" disabled selected={!refineAnswers[q.id]}>Choose…</option>
                    {#each q.options as opt}
                      <option value={opt}>{opt}</option>
                    {/each}
                  </select>
                {:else}
                  <input id={`refine-q-${q.id}`} type="text" bind:value={refineAnswers[q.id]} placeholder="Your answer…" />
                {/if}
              </div>
            {/each}
          </div>
        {/if}

        <div class="modal-actions">
          <button class="btn" on:click={cancelRefinement} disabled={compiling}>Cancel</button>
          <button class="btn primary" on:click={confirmRefinement} disabled={compiling || !((refinement.refined_intent || '').trim())}>
            {compiling ? 'Generating…' : `Generate ${recoAgentLabel(generatedMode(refinement.recommended_mode))}`}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if workflowExperiment}
    <div class="modal-backdrop" on:click|self={() => (workflowExperiment = null)} role="presentation">
      <div class="modal workflow-experiment-modal" role="dialog" aria-modal="true" aria-labelledby="workflow-experiment-title">
        <div class="workflow-experiment-kicker">Experimental feature</div>
        <h2 id="workflow-experiment-title" class="modal-title">Generate a fixed workflow?</h2>
        <p class="modal-body">
          Studio will generate a fixed execution graph from your prompt. Generated
          graphs may select incorrect tools, lose inputs between steps, or require
          manual wiring. Review and test every step before deployment.
        </p>
        <div class="workflow-experiment-callout">
          For most conversational, adaptive, and tool-using agents, <strong>Auto</strong>
          or <strong>Plan-Execute</strong> is more reliable. Continuing also replaces
          manual edits to the current {workflowExperiment.from === 'workflow' ? 'workflow' : 'agent'}.
        </div>
        <div class="modal-actions">
          <button class="btn" type="button" on:click={() => (workflowExperiment = null)} disabled={compiling}>Cancel</button>
          <button class="btn primary" type="button" on:click={useAutoInstead} disabled={compiling}>Use Auto instead</button>
          <button class="btn workflow-experiment-continue" type="button" on:click={confirmExperimentalWorkflow} disabled={compiling}>
            Continue with Experimental Workflow
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if agentRoute}
    <div class="modal-backdrop" on:click|self={() => (agentRoute = null)} role="presentation">
      <div class="modal agent-route-modal" role="dialog" aria-modal="true" aria-labelledby="agentroute-title">
        <h2 id="agentroute-title" class="modal-title">Built as {recoArticle(agentRoute.mode)} {recoAgentLabel(agentRoute.mode)}</h2>
        <p class="modal-body">
          This task reasons over its tools — it decides which skill or tool to use
          per request — so it can't be expressed as a fixed workflow graph. Studio
          built it as {recoArticle(agentRoute.mode)} <strong>{recoAgentLabel(agentRoute.mode)}</strong> instead
          and opened its <strong>SOUL.yaml</strong>, where the agent actually lives.
        </p>
        {#if agentRoute.reason}
          <div class="agent-route-reason">{agentRoute.reason}</div>
        {/if}
        <p class="modal-body agent-route-hint">
          Edit the prompt, tools, and skills in the SOUL.yaml below, then Save. To
          build a fixed flow instead, use the <strong>Workflow</strong> toggle.
        </p>
        <div class="modal-actions">
          <button class="btn" on:click={() => { agentRoute = null; switchMode('workflow') }} disabled={compiling}>
            Build as workflow anyway
          </button>
          <button class="btn primary" on:click={() => (agentRoute = null)}>
            Edit the SOUL.yaml
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if consent}
    <div class="modal-backdrop" on:click|self={cancelConsent} role="presentation">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="consent-title">
        <h2 id="consent-title" class="modal-title">Review &amp; {consent.mode === 'run' ? 'grant' : 'consent'}</h2>
        <p class="modal-body">
          {#if consent.mode === 'run'}
            This step was blocked because it goes beyond the read-only guardrails.
            Review exactly what it does below and grant it to run. Nothing is saved —
            the grant applies to this draft so you can keep iterating, and it is bound
            to the exact code shown, so editing it later asks again.
          {:else}
            This workflow needs your explicit consent before it can be saved. Review
            each item below. It is saved as a <strong>DISABLED</strong> agent;
            consent is bound to the exact code shown — editing it later voids the grant.
          {/if}
        </p>
        {#if consent.items.length}
          <ul class="consent-items">
            {#each consent.items as it}
              {#if it.kind === 'code'}
                <li class="consent-code">
                  <div class="consent-row">
                    <span class="consent-name">🐍 {it.name}</span>
                    {#each (it.capabilities || []) as cap}
                      <span class="cap cap-{cap}">{cap}</span>
                    {/each}
                    {#if it.dynamic}<span class="cap cap-dynamic">dynamic</span>{/if}
                  </div>
                  {#if it.reason}<div class="consent-reason">{it.reason}</div>{/if}
                  <pre class="consent-codeblock">{codeForNode(it.name)}</pre>
                  <label class="consent-scope">
                    Grant scope:
                    <select
                      value={consent.scopes[it.name] || 'workflow'}
                      on:change={(e) => setConsentScope(it.name, e.target.value)}
                    >
                      <option value="run">this run only</option>
                      <option value="workflow">this workflow (until the code changes)</option>
                      <option value="until_revoked">until I revoke it</option>
                    </select>
                  </label>
                </li>
              {:else}
                <li>
                  <span class="consent-name">{it.name}</span>
                  {#if it.reason}<span class="consent-reason">{it.reason}</span>{/if}
                  <div class="consent-note">
                    Privileged channel exposure — an operator must still set
                    <code>accept_privileged_exposure</code> in config at deploy time.
                  </div>
                </li>
              {/if}
            {/each}
          </ul>
        {/if}
        <div class="modal-actions">
          <button class="btn" on:click={cancelConsent} disabled={saving || trying}>Cancel</button>
          {#if consent.mode === 'run'}
            <button class="btn primary" on:click={grantAndRerun} disabled={trying}>
              {trying ? 'Running…' : 'Grant & re-run'}
            </button>
          {:else}
            <button class="btn primary" on:click={acknowledgeConsent} disabled={saving}>
              {saving ? 'Saving…' : 'Consent & save'}
            </button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- Build inspector: the durable build transcript, every step made visible. -->
  <BuildInspector
    open={showBuildInspector}
    traceId={buildTraceId}
    on:frame={(e) => (replayOverride = e.detail)}
    on:close={() => { showBuildInspector = false; replayOverride = null }}
  />
</div>
