/*
 * Studio API client (ARCH-6).
 *
 * Replaces the old host-mediated postMessage RPC bridge (bridge.js, relayed by
 * PluginFrame.svelte). Studio now runs as a first-class page of the core
 * dashboard, so it can call the gateway directly through the GUI's
 * authenticated `api` client — inheriting the user's session and auth headers —
 * instead of bouncing every request through a parent frame.
 *
 * The export keeps the EXACT shape and method signatures of the old `bridge`
 * object so the migrated Studio page (pages/Studio.svelte) is unchanged at its
 * call sites. The only behavioural contract we must preserve by hand is the
 * /studio/save 409 consent fallback: the old bridge surfaced `requiresConsent`
 * and `consentItems` as TOP-LEVEL fields on the rejected Error, whereas
 * apiFetch puts the whole error body on `err.body`. We hoist them so the
 * Studio save handler keeps working without changes.
 */

import { api, apiFetch } from '../api.js'

const CATALOG_TIMEOUT_MS = 5000

function emptyCatalogPart(key) {
  if (key === 'agents') return { agents: [] }
  if (key === 'tools') return { python_tools: [], mcp_tools: [], builtins: [] }
  if (key === 'providers') return { providers: {}, default_provider: '' }
  if (key === 'channels') return { channels: [] }
  if (key === 'skills') return { skills: [] }
  if (key === 'mcp') return { servers: [] }
  return {}
}

function timeoutAfter(ms, label) {
  return new Promise((_, reject) => {
    setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms)
  })
}

export async function catalogPart(label, promise, fallback, timeoutMs = CATALOG_TIMEOUT_MS) {
  try {
    return await Promise.race([promise, timeoutAfter(timeoutMs, label)])
  } catch (err) {
    const out = { ...fallback }
    out.error = err && err.message ? err.message : String(err || 'error')
    return out
  }
}

export async function loadCatalogParts(loaders, timeoutMs = CATALOG_TIMEOUT_MS) {
  const entries = await Promise.all(Object.entries(loaders).map(async ([key, load]) => {
    const fallback = emptyCatalogPart(key)
    return [key, await catalogPart(key, load(), fallback, timeoutMs)]
  }))
  return Object.fromEntries(entries)
}

// Hoist structured Studio failure fields apiFetch parked on err.body up to the
// top level, matching the old bridge's rejection shape for consent and letting
// save-time contract failures open the same preflight modal.
function hoistStudioError(err) {
  const b = err && err.body
  if (b && typeof b === 'object') {
    if (b.requiresConsent != null) err.requiresConsent = b.requiresConsent
    if (b.consentItems != null) err.consentItems = b.consentItems
    if (b.contract != null) err.contract = b.contract
    if (b.preflight != null) err.preflight = b.preflight
  }
  return err
}

export const bridge = {
  // Read-only catalog: agents + tools + providers + channels + skills + mcp,
  // fetched in parallel with the user's own session (no more host relay).
  // Each source is independently time-boxed so a restarting MCP server or slow
  // optional integration cannot blank the whole Studio palette.
  catalog: async () => {
    return loadCatalogParts({
      agents: () => api.agents.list(),
      tools: () => api.tools.catalog(),
      providers: () => api.workspaceProviders.list(),
      channels: () => api.channels.list(),
      skills: () => api.skills.list(),
      mcp: () => api.mcp.list(),
    })
  },

  // Mandatory pre-generation refine pass: clarify a rough intent before it is
  // compiled into a workflow.
  // `light` requests a fast touch-up pass (used when re-generating from an
  // already-refined, user-edited prompt) instead of a full re-refine.
  refinePrompt: (intent, catalog, light) =>
    api.studio.refinePrompt({ intent, catalog, light }),

  compile: (intent, answers, catalog, rawIntent, forceWorkflow) =>
    api.studio.compile({ intent, answers, catalog, rawIntent, forceWorkflow }),

  // Try an unsaved reasoning agent against one sample question.
  // `ack` is the user's acknowledgement of the run preview the server returned
  // with a 409: { confirm_side_effects, acknowledged_tools }.
  tryAgent: (workflow, question, ack) =>
    api.studio.tryAgent({
      workflow,
      question,
      confirm_side_effects: !!(ack && ack.confirm_side_effects),
      acknowledged_tools: (ack && ack.acknowledged_tools) || undefined,
    }),

  // Learn from Run Live: propose repairs from the node trace, apply one approved.
  repairLive: (workflow, node_trace) => api.studio.repairLive({ workflow, node_trace }),
  // The trace is optional on the wire but not optional in practice: without it
  // the server cannot replay the fix and can only report it as unproven.
  applyRepair: (workflow, proposal, failing_input, node_trace, preview) =>
    api.studio.applyRepair({ workflow, proposal, failing_input, node_trace, preview }),

  // Story surfaces: structured spec, readable plan, model cards, run preview.
  buildSpec: (intent, previous_intent) => api.studio.buildSpec({ intent, previous_intent }),
  planView: (workflow) => api.studio.planView({ workflow }),
  modelCapabilities: (model, provider) => api.studio.modelCapabilities({ model, provider }),
  strategyFit: (model, provider) => api.studio.strategyFit({ model, provider }),
  runPreview: (workflow) => api.studio.runPreview({ workflow }),
  readiness: (workflow, acceptPrivilegedExposure) =>
    api.studio.readiness({ workflow, acceptPrivilegedExposure }),

  // Credentials: list configured secrets (with a `set` flag) and set one inline.
  listSecrets: () => api.secrets.list(),
  setSecret: (name, value) => api.secrets.set(name, value),

  // Canvas⇄Code (SOUL.yaml) view: serialize a draft to YAML, parse edited YAML
  // back to a draft (+ warnings), and save authored YAML straight to disk.
  toYaml: (workflow) => api.studio.yaml({ workflow }),
  fromYaml: (yaml) => api.studio.fromYaml({ yaml }),
  saveYaml: (yaml, agentId = '') => api.studio.saveYaml({ yaml, agentId }),
  validateYaml: (yaml) => api.studio.validateYaml({ yaml }),
  fixYaml: (yaml) => api.studio.fixYaml({ yaml }),
  reviewYaml: (yaml) => api.studio.reviewYaml({ yaml }),
  getRules: () => api.studio.getRules(),
  saveRules: (rules) => api.studio.saveRules({ rules }),

  // Phase B: compile a plain-language connector gate into a flow predicate.
  compileGate: (phrase, vars) => api.studio.compileGate({ phrase, vars }),
  // Phase C: compile ONE node from its plain-language intent into config.
  compileNode: (req) => api.studio.compileNode(req),
  // Phase 2: the coarse composite-block catalog.
  compositeBlocks: () => api.studio.compositeBlocks(),

  // Generate a ReAct/Plan-Execute agent (no fixed flow).
  compileAgent: (intent, strategy, answers, catalog, rawIntent = '') =>
    api.studio.compileAgent({ intent, rawIntent, strategy, answers, catalog }),

  // M5 test bench: only forward present optional fields; the backend defaults
  // the rest.
  test: (workflow, input, opts = {}) =>
    api.studio.test({
      workflow,
      input,
      ...(opts.mocks ? { mocks: opts.mocks } : {}),
      ...(opts.assertions ? { assertions: opts.assertions } : {}),
      ...(opts.mode ? { mode: opts.mode } : {}),
      ...(opts.variables ? { variables: opts.variables } : {}),
      ...(opts.environment ? { environment: opts.environment } : {}),
      ...(opts.startNode ? { startNode: opts.startNode } : {}),
    }),

  // Consolidated pre-save validation (missing capabilities, empty required
  // args, invalid schedule, unconfigured channels).
  preflight: (workflow) => api.studio.preflight({ workflow }),

  // Studio generation contract: graph + runtime preflight + authoring hygiene.
  contract: (workflow) => api.studio.contract({ workflow }),

  // Cohort F S6 — security preflight. Trust boundaries, prompt-injection
  // exposure, network/file/channel/privileged/confirmation checks, plus
  // structured recommendations (write_file → kb_write, shell_exec → python_file,
  // http_request → MCP). Backend: internal/studio/security_preflight.go.
  securityReview: (workflow) => api.studio.securityReview({ workflow }),

  // Deterministic + iterative repair (contract structure + wiring + blockers).
  autowire: (workflow) => api.studio.autowire({ workflow }),

  // AI troubleshoot of a runtime error.
  troubleshoot: (workflow, error, opts = {}) => api.studio.troubleshoot({ workflow, error, ...opts }),

  // Architect: autonomous build-verify-repair loop ("Build until it works").
  build: (workflow, intent, verify) => api.studio.build({ workflow, intent, verify }),
  // Streaming variant: onEvent gets live progress frames; resolves with the report.
  buildStream: (workflow, intent, verify, onEvent) =>
    api.studio.buildStream({ workflow, intent, verify }, onEvent),

  // Runtime self-heal: list failed runs + diagnose/heal one.
  failedRuns: () => api.studio.failedRuns(),
  diagnoseRun: (id) => api.studio.diagnoseRun({ id }),
  diagnoseSession: (agentId, sessionId) => api.studio.diagnoseSession({ agentId, sessionId }),

  // Per-block run trace of a live flow run (input/output/duration/error),
  // by runId or the agent's most recent run.
  runTrace: (agentId, runId) => api.studio.runTrace(agentId, runId),
  runDiagnosis: (agentId, runId) => api.studio.runDiagnosis(agentId, runId),

  // Complete run history for an agent (every run, scheduled or on-demand).
  runHistory: (agentId) => api.studio.runHistory(agentId),

  // Full structured trace of an autonomous build (the build inspector source):
  // a specific build by id, or the most recent when omitted; and the list of
  // recent builds for the picker.
  buildTrace: (id) => api.studio.buildTrace(id),
  buildTraces: () => api.studio.buildTraces(),

  // Builder-model strength advice (warn before generating on a weak model).
  modelAdvice: () => api.studio.modelAdvice(),

  plan: (workflow) => api.studio.plan({ workflow }),

  validate: (workflow) => api.studio.validate({ workflow }),

  // On the 409 consent fallback, re-throw with the consent fields hoisted to
  // the top level so the save handler can open the consent dialog unchanged.
  // grants (optional) is the per-node code-consent array collected from the
  // consent dialog: [{ nodeId, hash, capabilities, scope }].
  // acceptWarningsReason is the operator's recorded justification for saving
  // past a warnings-only report. Empty on a clean save.
  save: async (workflow, acceptPrivilegedExposure, grants, acceptWarningsReason, initialWorkflow) => {
    try {
      return await api.studio.save({
        workflow, initialWorkflow, acceptPrivilegedExposure, grants, acceptWarningsReason,
      })
    } catch (e) {
      throw hoistStudioError(e)
    }
  },

  // M4 discover: relay the existing skill-source search; pass packages through
  // verbatim under `results`, matching the old bridge contract.
  discover: async (query, kind) => {
    const res = await api.registries.search(query || '')
    const results = res && Array.isArray(res.packages) ? res.packages : []
    return { results, count: (res && res.count) || results.length }
  },

  // M4 install: STAGE the package (a real, review-bearing op that does NOT
  // activate anything; the operator must still Approve it in the Plugins page).
  // We report that honestly (multiStep:true) — same shape the host produced.
  install: async ({ source, checksum, name } = {}) => {
    const res = await api.plugins.stage(source, checksum || '')
    const preview = (res && res.preview) || null
    return {
      staged:
        (preview &&
          (preview.StagedID || preview.stagedID || preview.staged_id)) ||
        '',
      multiStep: true,
      preview,
      security: (preview && (preview.Security || preview.security)) || null,
      note:
        (res && res.note) ||
        'Staged for review — approve it in the Plugins page to activate.',
    }
  },

  // M6: templates, draft library, per-node refine.
  templates: () => api.studio.templates(),
  draftSave: (name, workflow) => api.studio.draftSave({ name, workflow }),
  draftsList: () => api.studio.draftsList(),
  draftLoad: (id) => api.studio.draftLoad(id),
  draftDelete: (id) => api.studio.draftDelete(id),
  refine: (workflow, nodeId, instruction) =>
    api.studio.refine({ workflow, nodeId, instruction }),

  // "My Workflows": published agent workflows (list / load-as-draft / delete)
  // alongside the draft library already exposed above.
  agentWorkflows: () => api.studio.agents.list(),
  loadAgentWorkflow: (id) => api.studio.agents.get(id),
  deleteAgent: (id) => api.agents.delete(id),

  // Read-only SOUL.yaml browsing for EVERY agent (not just workflow-bearing
  // ones): allAgents lists all registered agents; agentYaml returns the raw
  // on-disk SOUL.yaml ({ id, path, yaml }) so Studio can show it without going
  // through the lossy draft round-trip.
  allAgents: () => api.agents.list(),
  agentYaml: (id) => api.agents.getYaml(id),

  // Deployment control from the library (ST-15). `enabled` is what decides
  // whether a schedule actually fires, so this is the deploy/undeploy switch.
  agentEnable: (id) => api.agents.enable(id),
  agentDisable: (id) => api.agents.disable(id),
  // Version history + rollback for a deployed agent.
  agentVersions: (id) => api.agents.versions(id),
  agentRollback: (id, version) => api.agents.rollback(id, version),

  // Framework-written Python for a node: deterministic scaffolds, or the
  // framework's own configured model writing the code (no external service).
  scaffolds: () => api.studio.scaffolds(),
  generateCode: (nodeId, description, workflow) =>
    api.studio.codegen({ nodeId, description, workflow }),

  // Studio model picker: use the workspace-scoped, author-safe selection API.
  // This lets developers choose approved providers/models without granting
  // access to credentials, deployment settings, or the full config editor.
  getConfig: () => api.workspaceLLMSelection.get(),
  providerModels: (id) => api.workspaceProviders.models(id),
  setStudioModel: (provider, model) =>
    api.workspaceLLMSelection.patch({ llm: { studio: { provider, model } } }),
  // Story 9 (Cohort B): intent-named preset catalog — "fast local" / "reliable
  // local" / "cloud quality". Persisted via the same config patch pipeline.
  presets: () => apiFetch('/studio/presets'),
  setStudioPreset: (preset) =>
    api.workspaceConfig.patch({ llm: { studio: { preset } } }),
  // Story 9 M (Cohort C): default generate UX ('streamed' | 'wizard').
  setBuildUX: (mode) =>
    api.workspaceConfig.patch({ llm: { studio: { build_ux: mode } } }),
  // Streamed generate pipeline — emits one PipelineEvent per phase-boundary
  // and a terminating `done` frame with the full PipelineResult.
  // `signal` aborts the stream; the server cancels the run when the connection
  // drops, so this stops the work rather than only hiding it.
  generateStream: (intent, opts, onEvent, signal) =>
    api.studio.generateStream({ intent, ...opts }, onEvent, signal),
}
