<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount, tick } from 'svelte'
  import { api } from '../lib/api.js'
  import { modelAvailability } from '../lib/agentmodel.js'
  import { parseMarkdown, richRenderer } from '../lib/markdown.js'
  import { apiKey, editAgent, studioSession } from '../lib/stores.js'
  import ChipPicker from '../lib/ChipPicker.svelte'
  import FilePicker from '../lib/FilePicker.svelte'

  let agents   = []
  let selected = null   // the agent currently shown in the editor
  let editing  = null   // deep-copy being modified
  let error    = null
  let saveMsg  = ''
  let saveAudit = null
  let saving   = false
  let deleting = false
  let validating = false
  let validationReport = null
  let tiers = {} // agent id -> { tier, reasons }
  let strategyManualOverride = false
  let strategyAutoNotice = ''
  let lastAutoStrategyKey = ''

  // Capability-ack modal state. When the backend returns 409 with
  // {needs_ack: true, capability_audit}, we open a blocking modal that shows
  // the tier change + affected channel bindings. Confirming retries the save
  // with acknowledgeAudit:true so api.js sets X-Acknowledge-Audit. Story 5
  // AC — Privileged external bindings require explicit approval BEFORE write,
  // not after. See internal/gateway/api.go respondCapabilityAckRequired.
  let ackModal = null      // { audit, retry: () => Promise<void>, from: 'save'|'saveYaml' }
  let ackConfirming = false

  // ── Raw SOUL.yaml view/edit modal ─────────────────────────────────────────
  let showYaml    = false   // modal open
  let yamlText    = ''      // editable buffer
  let yamlOrig    = ''      // last-loaded text (to detect unsaved edits)
  let yamlPath    = ''      // on-disk path (shown for reference)
  let yamlLoading = false
  let yamlSaving  = false
  let yamlError   = ''      // parse/validation error from the server
  let yamlMsg     = ''      // success note
  let execBackendChoice = ''

  // Advanced config snippets appended into the SOUL.yaml editor.
  const POLICY_SNIPPET = `policy:
  enabled: true
  shell: prompt          # allow | prompt | deny
  file: prompt
  network: allow
  allow_domains: []      # e.g. [example.com] — network limited to these
  deny_domains: []
  deny_paths: []         # e.g. ["*.env", "/etc/*"]`

  const DRYRUN_SNIPPET = `dry_run: true            # simulate side-effecting tools; no real actions`

  // appendYamlBlock adds a block to the editor buffer if that top-level key
  // isn't already present, keeping the insert idempotent.
  function appendYamlBlock(block) {
    const topKey = block.split(':')[0].trim()
    const keyRe = new RegExp('^' + topKey + ':', 'm')
    if (keyRe.test(yamlText)) {
      yamlMsg = `"${topKey}" is already in this agent — edit it inline.`
      return
    }
    yamlText = yamlText.replace(/\s*$/, '') + '\n\n' + block + '\n'
    yamlError = ''
    yamlMsg = `Added ${topKey} block — review and Save.`
  }

  // applyExecBackend sets or replaces the execution.backend value.
  function applyExecBackend() {
    if (!execBackendChoice) return
    const block = `execution:\n  backend: ${execBackendChoice}`
    if (/^execution:/m.test(yamlText)) {
      // Replace an existing backend line, or append one under execution:.
      if (/^\s+backend:.*$/m.test(yamlText)) {
        yamlText = yamlText.replace(/^(\s+)backend:.*$/m, `$1backend: ${execBackendChoice}`)
      } else {
        yamlText = yamlText.replace(/^execution:.*$/m, `execution:\n  backend: ${execBackendChoice}`)
      }
    } else {
      yamlText = yamlText.replace(/\s*$/, '') + '\n\n' + block + '\n'
    }
    yamlError = ''
    yamlMsg = `Execution backend set to "${execBackendChoice}" — review and Save.`
  }

  // ── Security Doctor modal (Cohort F S7 / F-GUI-2) ────────────────────────
  // Full report + dry-run simulator for the S1+S2+S3 pipeline.
  // Backend: GET  /api/v1/agents/:id/security_doctor
  //          POST /api/v1/agents/:id/security_doctor/dry_run
  let showDoctor      = false
  let doctorReport    = null
  let doctorLoading   = false
  let doctorError     = ''
  let doctorAgentId   = ''
  let doctorInput     = defaultDryRunInput()
  let doctorResult    = null
  let doctorRunning   = false
  let doctorRunError  = ''
  let doctorResultEl  = null   // ref to the result panel — scroll-into-view after Run simulation

  function defaultDryRunInput() {
    return {
      user_goal:        '',
      injected_content: '',
      injection_source: 'fetch_url',
      followup_tool:    'shell_exec',
      followup_args:    '{}',
    }
  }

  async function openDoctor(agent) {
    const target = agent || selected
    if (!target?.id) return
    showDoctor    = true
    doctorAgentId = target.id
    doctorLoading = true
    doctorError   = ''
    doctorReport  = null
    doctorResult  = null
    doctorRunError = ''
    doctorInput   = defaultDryRunInput()
    try {
      doctorReport = await api.agents.securityDoctor(target.id)
    } catch (e) {
      doctorError = e.message || 'Could not load security doctor report.'
    } finally {
      doctorLoading = false
    }
  }

  function closeDoctor() {
    if (doctorRunning) return
    showDoctor = false
  }

  async function runDoctorDryRun() {
    if (!doctorAgentId || doctorRunning) return
    doctorRunning  = true
    doctorRunError = ''
    doctorResult   = null
    let parsedArgs = {}
    const raw = (doctorInput.followup_args || '').trim()
    if (raw) {
      try {
        parsedArgs = JSON.parse(raw)
      } catch (e) {
        doctorRunError = 'follow-up args must be valid JSON.'
        doctorRunning  = false
        return
      }
    }
    try {
      doctorResult = await api.agents.securityDoctorDryRun(doctorAgentId, {
        user_goal:        doctorInput.user_goal || '',
        injected_content: doctorInput.injected_content || '',
        injection_source: doctorInput.injection_source || 'fetch_url',
        followup_tool:    doctorInput.followup_tool || '',
        followup_args:    parsedArgs,
      })
      // Result panel appears below a large form + report; scroll the modal to
      // reveal it so users don't have to hunt for it. tick() waits for the
      // {#if doctorResult} block to render + bind:this to run.
      await tick()
      doctorResultEl?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
    } catch (e) {
      doctorRunError = e.message || 'Dry-run failed.'
    } finally {
      doctorRunning = false
    }
  }

  function findingClass(sev) {
    const s = (sev || '').toLowerCase()
    if (s === 'critical' || s === 'high') return 'danger'
    if (s === 'warn' || s === 'medium' || s === 'low') return 'warn'
    return 'info'
  }

  function verdictClass(dec) {
    const s = (dec || '').toLowerCase()
    if (s === 'deny') return 'danger'
    if (s === 'prompt') return 'warn'
    return 'info'
  }

  function fmtBool(b) {
    return b ? 'yes' : 'no'
  }

  // ── Agent version history / rollback modal ───────────────────────────────
  let showHistory = false
  let historyLoading = false
  let historySaving = false
  let historyError = ''
  let historyMsg = ''
  let historyVersions = []
  let historySelected = null
  let historyYaml = ''

  async function openYaml() {
    if (!selected) return
    showYaml = true
    yamlLoading = true
    yamlError = ''
    yamlMsg = ''
    try {
      const res = await api.agents.getYaml(selected.id)
      yamlText = (res && res.yaml) || ''
      yamlOrig = yamlText
      yamlPath = (res && res.path) || ''
    } catch (e) {
      yamlError = e.message || 'Could not load YAML'
    }
    yamlLoading = false
  }

  function closeYaml() {
    if (yamlSaving) return
    if (yamlText !== yamlOrig && !window.confirm('Discard your unsaved YAML edits?')) return
    showYaml = false
  }

  async function saveYaml(opts = {}) {
    if (!selected || yamlSaving) return
    yamlSaving = true
    yamlError = ''
    yamlMsg = ''
    try {
      await api.agents.updateYaml(selected.id, yamlText, opts)
      yamlOrig = yamlText
      yamlMsg = '✓ Saved'
      await load()
      const found = agents.find(a => a.id === selected.id)
      if (found) select(found) // refresh the form editor with the new definition
    } catch (e) {
      // Capability escalation gate — same pattern as save(). Open the modal;
      // on confirm we retry with the acknowledgement header.
      if (e.status === 409 && e.body?.needs_ack && e.body?.capability_audit) {
        ackModal = {
          audit: e.body.capability_audit,
          from: 'saveYaml',
          retry: () => saveYaml({ acknowledgeAudit: true }),
        }
      } else {
        // The server returns structured validation findings on a 400; surface the
        // first concrete message so the user can fix syntax/fields in place.
        const v = e.body && e.body.validation
        if (v && Array.isArray(v.findings) && v.findings.length) {
          const f = v.findings.find(x => x.severity === 'error') || v.findings[0]
          yamlError = (f.field ? f.field + ': ' : '') + f.message
        } else {
          yamlError = e.message || 'Save failed'
        }
      }
    }
    yamlSaving = false
  }

  async function openHistory() {
    if (!selected) return
    showHistory = true
    historyLoading = true
    historyError = ''
    historyMsg = ''
    historyVersions = []
    historySelected = null
    historyYaml = ''
    try {
      const res = await api.agents.versions(selected.id)
      historyVersions = res?.versions || []
      if (historyVersions.length) {
        await selectVersion(historyVersions[0])
      }
    } catch (e) {
      historyError = e.message || 'Could not load history'
    }
    historyLoading = false
  }

  function closeHistory() {
    if (historySaving) return
    showHistory = false
  }

  async function selectVersion(version) {
    if (!selected || !version) return
    historySelected = version
    historyError = ''
    historyMsg = ''
    try {
      const res = await api.agents.version(selected.id, version.id)
      historyYaml = res?.yaml || ''
    } catch (e) {
      historyYaml = ''
      historyError = e.message || 'Could not load version'
    }
  }

  async function rollbackVersion() {
    if (!selected || !historySelected || historySaving) return
    const ok = window.confirm(`Restore "${selected.id}" to version ${formatVersionTime(historySelected.created_at)}? The current definition will be snapshotted first.`)
    if (!ok) return
    historySaving = true
    historyError = ''
    historyMsg = ''
    try {
      await api.agents.rollback(selected.id, historySelected.id)
      historyMsg = '✓ Restored'
      await load()
      const found = agents.find(a => a.id === selected.id)
      if (found) select(found)
      await openHistory()
    } catch (e) {
      historyError = e.message || 'Rollback failed'
    }
    historySaving = false
  }

  function formatVersionTime(value) {
    if (!value) return 'unknown time'
    try {
      return new Date(value).toLocaleString()
    } catch {
      return value
    }
  }

  // Tool catalog — fetched once, used by the python_file dropdown
  let catalog = { python_tools: [], mcp_tools: [], builtins: [] }

  // Providers + per-provider model lists (lazy-fetched on demand)
  let providers       = []   // [{ id, registered }]
  let modelsByProv    = {}   // provider id → [model names]
  let modelsLoading   = {}   // provider id → bool
  let modelsError     = {}   // provider id → string

  // Lookup sources for the picker fields. Loaded once on mount so
  // typing errors are impossible — every selection is from a real list.
  let availableChannels  = []  // [{id, name, kind}]
  let availableSkills    = []  // [{name, description}]
  let availableKBs       = []  // [{id, name, description, document_count, chunk_count}]

  const recommendedConfirmTools = [
    'shell_exec',
    'run_script',
    'python_eval',
    'write_file',
    'http_request',
    'fetch_url',
    'web_search',
    'channel.send',
    'download_file',
    'install_library',
    'kb_write',
    'queue_put',
    'queue_take',
    'queue_clear',
  ]

  async function loadLookups() {
    const [chs, sks, kbs] = await Promise.allSettled([
      api.channels.list(),
      api.skills.list(),
      api.knowledge.list(),
    ])
    if (chs.status === 'fulfilled') availableChannels = chs.value?.channels || []
    if (sks.status === 'fulfilled') availableSkills   = sks.value?.skills   || []
    if (kbs.status === 'fulfilled') availableKBs      = kbs.value?.knowledge_bases || []
  }

  // ---- Picker option transforms ---------------------------------------------
  // Each lookup is mapped to ChipPicker's { value, label, description, group }
  // shape so the picker can be generic. Recomputed reactively so freshly-added
  // channels/skills/KBs show up without a reload.
  $: channelOptions = availableChannels.map(c => ({
    value: c.id, label: c.id, description: c.kind || c.name || '',
  }))
  $: skillOptions = availableSkills.map(s => ({
    value: s.name, label: s.name, description: s.description || '',
  }))
  $: kbOptions = availableKBs.map(k => ({
    value: k.name, label: k.name,
    description: `${k.document_count || 0} docs · ${k.chunk_count || 0} chunks${k.description ? ' — ' + k.description : ''}`,
  }))
  $: peerAgentOptions = agents
    .filter(a => !editing || a.id !== editing.id)
    .map(a => ({
      value: a.id, label: a.id,
      description: a.name || a.description || '',
    }))
  $: builtinOptions = (catalog.builtins || []).map(b => ({
    value: b.name, label: b.name, description: b.description || '',
  }))
  $: confirmToolOptions = [
    { value: 'all', label: 'all', description: 'Require confirmation for every tool call', group: 'Policy' },
    ...recommendedConfirmTools.map(name => ({
      value: name,
      label: name,
      description: confirmToolDescription(name),
      group: 'Recommended',
    })),
    ...builtinOptions
      .filter(opt => !recommendedConfirmTools.includes(opt.value))
      .map(opt => ({ ...opt, group: 'Built-ins' })),
  ]
  $: pythonFileOptions = (catalog.python_tools || []).map(pt => ({
    value: pt.path, label: pt.name, description: pt.description || '',
  }))
  // The tool_choice dropdown is a UNION of meta-options + peers + builtins.
  // Computed reactively from editing.agents and editing.builtins so the list
  // updates as the user toggles those fields.
  $: toolChoiceOptions = (() => {
    const opts = [
      { value: '',         label: '(default — auto)', description: 'Model decides freely', group: 'Mode' },
      { value: 'auto',     label: 'auto',             description: 'Same as default',      group: 'Mode' },
      { value: 'none',     label: 'none',             description: 'Disallow tool calls on turn 1', group: 'Mode' },
      { value: 'required', label: 'required',         description: 'Must call some tool on turn 1', group: 'Mode' },
    ]
    if (editing) {
      for (const peerId of (editing.agents || [])) {
        const peer = agents.find(a => a.id === peerId)
        opts.push({
          value: `agent__${peerId}`, label: `agent__${peerId}`,
          description: peer?.name || peer?.description || '(peer agent)',
          group: 'Peer agents',
        })
      }
      // Allow naming specific built-ins too (rare but supported)
      for (const b of (catalog.builtins || [])) {
        opts.push({ value: b.name, label: b.name, description: b.description || '', group: 'Built-ins' })
      }
    }
    return opts
  })()
  // Memory scopes are a fixed enum.
  const memoryScopeOptions = [
    { value: 'session', label: 'session', description: 'Per-conversation history' },
    { value: 'global',  label: 'global',  description: 'Persistent across conversations for this agent' },
    { value: 'agent',   label: 'agent',   description: 'Shared across all sessions of this agent' },
  ]

  const llmTips = {
    executionProfile: 'Applies a tested bundle of model and reasoning settings. Use it as a starting point, then edit individual fields.',
    provider: 'Which configured LLM provider this agent uses by default.',
    model: 'Which model this agent calls. The list is loaded from the selected provider when possible.',
    maxTokens: 'Caps each model response. Raise for long reports or synthesis; lower to reduce cost, latency, and rambling.',
    temperature: 'Controls randomness. Use lower values for reliable tool use and extraction; higher values for creative or exploratory answers.',
    topP: 'Nucleus sampling. Lower values restrict the token pool for stability; higher values allow more varied phrasing.',
    maxTurns: 'Maximum LLM/tool turns before the agent stops. Raise for complex tool workflows; lower to prevent runaway loops.',
    responseFormat: 'Requests structured output when supported. JSON helps extraction, routing, and downstream tool inputs.',
    reasoningEffort: 'Hints how much hidden reasoning budget to spend on supported models. Higher can improve hard tasks but increases cost/latency.',
    presencePenalty: 'Positive values encourage new ideas instead of reusing already-mentioned concepts.',
    frequencyPenalty: 'Positive values reduce repeated words and phrases. Useful when responses loop or sound repetitive.',
    toolChoice: 'Controls the first tool call. Use auto for normal routing, none to answer directly, required to force a tool, or a specific tool name.',
    maxSteps: 'Maximum reasoning-loop steps. Raise for multi-tool research; lower to prevent long or stuck runs.',
    maxPlanSteps: 'Maximum plan items in Plan-Execute mode. Lower keeps plans compact; higher allows more detailed decomposition.',
    stepTimeout: 'Per-step wall-clock timeout. Use shorter values for interactive agents and longer values for slow tools.',
    totalTimeout: 'Whole-run timeout across all reasoning and tool steps.',
    phaseTemperature: 'Phase-specific randomness. Keep Think/Plan low for reliable tool decisions; Reflect can be slightly higher for polished synthesis.',
    phaseTopP: 'Phase-specific nucleus sampling. Lower for deterministic routing; higher for more flexible final wording.',
    phaseMaxTokens: 'Phase-specific response cap. Think/Plan need concise JSON; Reflect often needs more room for the final answer.',
    phaseFormat: 'Phase-specific output format. JSON is safest for internal reasoning phases that the engine must parse.',
    parallelPeerCalls: 'When the model calls two or more different peer agents in one turn, run those peer agents concurrently and return results in the original order. Use for coordinator agents with independent research/review tasks.',
    structuredPeerResults: 'Wrap peer-agent replies in a SOULACY_AGENT_RESULT JSON envelope. Use when a coordinator needs stable fields such as target_agent, content, citations, confidence, or parsed JSON from the peer.',
    confirmTools: 'Tools listed here pause for human approval before running. Use all to approve every tool call, or list only risky tools such as shell_exec, write_file, http_request, channel.send, and python_eval.',
    unattended: 'Allows scheduled or non-interactive runs to auto-approve confirmation gates. Use only for trusted automation after testing manually.',
    toolTimeout: 'Per-tool timeout for this Python tool. Use longer values for slow fetches or exports without weakening every tool in the agent.',
    toolRetries: 'Extra attempts after this Python tool fails. Use for idempotent fetch/read/parse tools; leave blank for side-effecting tools.',
    toolRetryBackoff: 'Delay between retry attempts. Keep short for flaky HTTP reads; increase for rate-limited APIs.',
    playgroundProvider: 'Temporarily override the provider for this playground run only.',
    playgroundModel: 'Temporarily override the model for this playground run only.',
  }

  const executionProfiles = {
    balanced: {
      label: 'Balanced agent',
      llm: { temperature: 0.7, top_p: 0.9, max_tokens: 1024, response_format: '' },
      reasoning: {
        think: { temperature: 0.1, top_p: 0.9, max_tokens: 1024, response_format: 'json' },
        plan: { temperature: 0.1, top_p: 0.9, max_tokens: 1024, response_format: 'json' },
        reflect: { temperature: 0.2, top_p: 0.9, max_tokens: 2048, response_format: 'json' },
      },
    },
    tool_loop: {
      label: 'Reliable tool loop',
      llm: { temperature: 0.2, top_p: 0.8, max_tokens: 1024, response_format: '' },
      reasoning: {
        think: { temperature: 0.1, top_p: 0.7, max_tokens: 1024, response_format: 'json' },
        plan: { temperature: 0.1, top_p: 0.7, max_tokens: 1024, response_format: 'json' },
        reflect: { temperature: 0.15, top_p: 0.8, max_tokens: 2048, response_format: 'json' },
      },
    },
    research: {
      label: 'Deep research',
      llm: { temperature: 0.35, top_p: 0.9, max_tokens: 4096, response_format: '' },
      reasoning: {
        think: { temperature: 0.15, top_p: 0.85, max_tokens: 1536, response_format: 'json' },
        plan: { temperature: 0.2, top_p: 0.9, max_tokens: 1536, response_format: 'json' },
        reflect: { temperature: 0.25, top_p: 0.9, max_tokens: 4096, response_format: 'json' },
      },
    },
    extractor: {
      label: 'JSON extractor',
      llm: { temperature: 0.05, top_p: 0.5, max_tokens: 2048, response_format: 'json' },
      reasoning: {
        think: { temperature: 0.05, top_p: 0.5, max_tokens: 1024, response_format: 'json' },
        plan: { temperature: 0.05, top_p: 0.5, max_tokens: 1024, response_format: 'json' },
        reflect: { temperature: 0.05, top_p: 0.5, max_tokens: 2048, response_format: 'json' },
      },
    },
    creative: {
      label: 'Creative writer',
      llm: { temperature: 0.9, top_p: 0.95, max_tokens: 2048, response_format: '' },
      reasoning: {
        think: { temperature: 0.2, top_p: 0.85, max_tokens: 1024, response_format: 'json' },
        plan: { temperature: 0.4, top_p: 0.9, max_tokens: 1024, response_format: 'json' },
        reflect: { temperature: 0.75, top_p: 0.95, max_tokens: 3072, response_format: 'json' },
      },
    },
  }

  function applyExecutionProfile(key) {
    if (!editing || !executionProfiles[key]) return
    const profile = executionProfiles[key]
    editing.llm = { ...(editing.llm || {}), ...profile.llm }
    editing.reasoning = { ...(editing.reasoning || {}) }
    for (const phase of ['think', 'plan', 'reflect']) {
      editing.reasoning[phase] = { ...(editing.reasoning[phase] || {}), ...profile.reasoning[phase] }
    }
    editing = editing
  }

  const BLANK = () => ({
    id: '', name: '', description: '', version: '1.0',
    trigger: 'channel', channels: ['http'], schedule: { cron: '' },
    webhook: { text_path: '', user_id_path: '', username_path: '', session_id_path: '', thread_id_path: '', include_raw: false },
    system_prompt: '',
    llm: { provider: 'ollama', model: '', temperature: 0.7, top_p: 0.9, max_tokens: 512 },
    memory: { read_scopes: ['session'], write_scopes: ['session'], max_tokens: 20 },
    learning: { enabled: false, min_chars: 160, max_proposals: 3 },
    tools: [], skills: [], knowledge: [], agents: [], parallel_peer_calls: false, structured_peer_results: false, confirm_tools: [], unattended: false, max_turns: 5, stream_reply: false, enabled: true,
  })

  function isSystemAgent(agent) {
    return agent?.id === 'system'
  }

  $: editingProtected = isSystemAgent(selected)
  $: selectedTier = selected?.id ? tiers[selected.id] : null

  async function load() {
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      loadTiers(agents)
      error  = null
      if ($editAgent) {
        const found = agents.find(a => a.id === $editAgent)
        if (found) {
          select(found)
        }
        $editAgent = ''
      }
    } catch (e) { error = e.message }
  }

  async function loadTiers(list) {
    const next = {}
    await Promise.all((list || []).map(async (agent) => {
      if (!agent?.id) return
      try {
        const res = await api.agents.tier(agent.id)
        next[agent.id] = {
          tier: res?.tier || 'unknown',
          reasons: res?.reasons || [],
        }
      } catch (_) {
        next[agent.id] = { tier: 'unknown', reasons: [] }
      }
    }))
    tiers = next
  }

  function tierLabel(tier) {
    switch (tier) {
      case 'read_only': return 'Read-only'
      case 'active': return 'Active'
      case 'privileged': return 'Privileged'
      default: return 'Unknown'
    }
  }

  function tierSummary(tierInfo) {
    const tier = tierInfo?.tier || 'unknown'
    if (tier === 'privileged') return 'Can reach OS-level, file-write, package, wildcard, or privileged peer capabilities.'
    if (tier === 'active') return 'Can use tools, memory writes, MCP, queues, channels, or peer agents with real-world effects.'
    if (tier === 'read_only') return 'Prompt and model only, with no active tool surface detected.'
    return 'Tier could not be determined. Treat channel exposure cautiously.'
  }

  async function loadCatalog() {
    try {
      const res = await api.tools.catalog()
      catalog = {
        python_tools: res?.python_tools || [],
        mcp_tools:    res?.mcp_tools    || [],
        builtins:     res?.builtins     || [],
      }
    } catch (e) {
      catalog = { python_tools: [], mcp_tools: [], builtins: [] }
    }
  }

  // Fetch the list of registered providers (drives the LLM Provider dropdown)
  async function loadProviders() {
    try {
      const res  = await api.providers.list()
      const regs = res.registered || []      // currently registered with the live router
      const ids  = new Set([...regs, ...(res.known || []), ...Object.keys(res.providers || {})])
      providers  = [...ids].map(id => ({
        id,
        registered: regs.includes(id),
        configured: (res.providers || {})[id] != null,
        defaultModel: (res.providers || {})[id]?.model || '',
      }))
    } catch (e) {
      providers = []
    }
  }

  $: enabledProviders = providers.filter(p => p.registered)

  function onProviderChange() {
    if (!editing?.llm) return
    strategyManualOverride = false
    strategyAutoNotice = ''
    const prov = providers.find(p => p.id === editing.llm.provider)
    if (prov && prov.defaultModel) {
      editing.llm.model = prov.defaultModel
    } else {
      editing.llm.model = ''
    }
  }

  function onModelChange() {
    strategyManualOverride = false
    strategyAutoNotice = ''
  }

  // Lazy-fetch the model list for one provider (cached). Called whenever the
  // user selects a provider in the LLM section.
  async function loadModels(providerId, force = false) {
    if (!providerId) return
    if (!force && (modelsByProv[providerId] || modelsLoading[providerId])) return
    modelsLoading = { ...modelsLoading, [providerId]: true }
    try {
      const res = await api.providers.models(providerId)
      modelsByProv = { ...modelsByProv, [providerId]: res.models || [] }
      modelsError  = { ...modelsError,  [providerId]: '' }
    } catch (e) {
      modelsByProv = { ...modelsByProv, [providerId]: [] }
      modelsError  = { ...modelsError,  [providerId]: e.message }
    } finally {
      modelsLoading = { ...modelsLoading, [providerId]: false }
    }
  }

  function refreshModels(providerId) {
    if (!providerId) return
    loadModels(providerId, true)
  }

  // Reactive: any time the editor's provider changes, pull its model list.
  $: if (editing?.llm?.provider) loadModels(editing.llm.provider)

  // Reactive: if the agent model is unset, default to the provider's defaultModel once providers are loaded.
  $: if (editing && providers.length > 0) {
    if (editing.llm && !editing.llm.model && editing.llm.provider) {
      const prov = providers.find(p => p.id === editing.llm.provider)
      if (prov && prov.defaultModel) {
        editing.llm.model = prov.defaultModel
      }
    }
  }

  // Computed: options for the model dropdown.
  // Rules:
  //   1. The current model is ALWAYS at position 0 — it never moves or disappears
  //      while the provider model list is loading, so bind:value never loses its
  //      match and the browser never snaps to option[0].
  //   2. Remaining known models follow, deduplicated.
  // The {#each} below uses a keyed loop (m) so Svelte moves <option> DOM nodes
  // instead of mutating them in-place — avoids the browser reset-to-first bug.
  $: modelOptions = (() => {
    const provId = editing?.llm?.provider
    const list   = (modelsByProv[provId] || [])
    const cur    = editing?.llm?.model
    const others = list.filter(m => m !== cur && m !== '__custom__')
    return cur ? [cur, ...others] : others
  })()

  $: modelStatus = modelAvailability({
    provider: editing?.llm?.provider,
    model: editing?.llm?.model,
    models: modelsByProv[editing?.llm?.provider] || [],
    loading: !!modelsLoading[editing?.llm?.provider],
    error: modelsError[editing?.llm?.provider] || '',
  })

  function nativeToolingConfidence(provider, model) {
    const p = String(provider || '').toLowerCase()
    const m = String(model || '').toLowerCase()
    if (!p || !m) return { capable: false, reason: '' }
    const hosted = ['openai', 'anthropic', 'google', 'gemini', 'ollama_cloud', 'ollama-cloud', 'openrouter', 'openroute', 'groq', 'mistral', 'deepseek', 'together', 'nvidia']
    if (hosted.some(x => p.includes(x))) {
      return { capable: true, reason: 'This provider/model should handle tool calls through the native Auto loop.' }
    }
    if (p.includes('ollama')) {
      const strongLocal = /(70b|72b|34b|32b|30b|27b|22b|20b|14b|13b|qwen3|qwen2\.5|llama3\.1|llama3\.2|llama3\.3|gpt-oss|mixtral|mistral-large|command-r)/.test(m)
      if (strongLocal) {
        return { capable: true, reason: 'This local model looks capable enough for native Auto tool calling.' }
      }
    }
    return { capable: false, reason: '' }
  }

  $: nativeTooling = nativeToolingConfidence(editing?.llm?.provider, editing?.llm?.model)
  $: maybeAutoSwitchStrategy(nativeTooling, editing?.llm?.provider, editing?.llm?.model)

  function maybeAutoSwitchStrategy(capability, provider, model) {
    if (!editing?.reasoning || !capability?.capable) return
    const key = `${provider || ''}/${model || ''}`
    if (lastAutoStrategyKey === key) return
    lastAutoStrategyKey = key
    if ((editing.reasoning.strategy || '') === 'react' && !strategyManualOverride) {
      editing.reasoning.strategy = ''
      strategyAutoNotice = `Switched strategy to Auto for ${key}; ReAct is now an advanced manual override.`
      editing = editing
    }
  }

  function setStrategy(val) {
    if (!editing) return
    editing.reasoning = editing.reasoning || {}
    editing.reasoning.strategy = val
    strategyManualOverride = true
    if (val === 'react' && nativeTooling?.capable) {
      strategyAutoNotice = 'Manual ReAct override kept. Auto is recommended for this model unless you are intentionally testing the classic loop.'
    } else if (val === '') {
      strategyAutoNotice = 'Auto selected. The engine will use the most reliable native tool-calling path for this model.'
    } else {
      strategyAutoNotice = ''
    }
    editing = editing
  }

  // ── Tools editing ─────────────────────────────────────────────────────────
  function addTool() {
    if (!editing) return
    editing.tools = [...(editing.tools || []), {
      name: '', description: '', python_file: '',
      timeout: '',
      parameters: { type: 'object', properties: {} },
    }]
  }
  function removeTool(i) {
    editing.tools = editing.tools.filter((_, idx) => idx !== i)
  }
  function moveTool(i, dir) {
    const arr = [...editing.tools]
    const j = i + dir
    if (j < 0 || j >= arr.length) return
    ;[arr[i], arr[j]] = [arr[j], arr[i]]
    editing.tools = arr
  }
  function onPythonFilePicked(i, path) {
    const meta = catalog.python_tools.find(t => t.path === path)
    editing.tools[i].python_file = path
    if (meta) {
      if (!editing.tools[i].name)        editing.tools[i].name = meta.name
      if (!editing.tools[i].description) editing.tools[i].description = meta.description || ''
    }
    editing = editing
  }
  function paramsJson(t) {
    try { return JSON.stringify(t.parameters || {}, null, 2) } catch { return '{}' }
  }
  function updateParams(i, json) {
    try { editing.tools[i].parameters = JSON.parse(json) } catch { /* mid-edit */ }
  }
  function updateToolRetries(i, value) {
    const raw = String(value || '').trim()
    if (!raw) {
      delete editing.tools[i].retries
    } else {
      editing.tools[i].retries = Math.max(0, Math.min(5, Number(raw) || 0))
    }
    editing = editing
  }
  // ── Channels & skills (CSV inputs) ────────────────────────────────────────
  function csvToArr(s) { return (s || '').split(',').map(x => x.trim()).filter(Boolean) }
  function linesToArr(s) { return (s || '').split('\n').map(x => x.trim()).filter(Boolean) }
  function syncChannels(v)  { if (editing) editing.channels  = csvToArr(v) }
  function syncSkills(v)    { if (editing) editing.skills    = csvToArr(v) }
  function syncKnowledge(v) { if (editing) editing.knowledge = csvToArr(v) }
  function syncAgents(v)    { if (editing) editing.agents    = csvToArr(v) }

  function setWebhookField(key, value) {
    if (!editing) return
    editing.webhook = editing.webhook || {}
    editing.webhook[key] = value
    editing = editing
  }

  // ensurePersona writes a single field into one of the three persona
  // blocks (identity / personality / non_negotiables) while keeping any
  // other fields the operator already typed. We lazily allocate the
  // block so a user who types one Identity field doesn't have an empty
  // Personality block round-trip through YAML.
  //
  // Wiped-clean detection: if every field in the resulting block is
  // empty / falsy, we delete the block so YAML stays minimal. This is
  // what makes the GUI round-trip-safe — a user who clears every
  // Identity field gets `identity:` removed from the saved YAML, not
  // `identity: {role: "", expertise: [], ...}`.
  function ensurePersona(block, patch) {
    if (!editing) return
    const current = editing[block] || {}
    const next = { ...current, ...patch }
    if (isBlockEmpty(block, next)) {
      delete editing[block]
    } else {
      editing[block] = next
    }
    editing = editing  // Svelte reactivity nudge
  }
  function isBlockEmpty(block, obj) {
    if (!obj) return true
    if (block === 'identity') {
      return !obj.role && !obj.audience && !obj.backstory &&
             (!obj.expertise || obj.expertise.length === 0)
    }
    if (block === 'personality') {
      return !obj.tone && !obj.voice &&
             (!obj.prefer || obj.prefer.length === 0) &&
             (!obj.avoid || obj.avoid.length === 0)
    }
    if (block === 'non_negotiables') {
      const oc = obj.output_constraints || {}
      const ocEmpty = !oc.format && !oc.max_length && !oc.min_length
      return (!obj.must || obj.must.length === 0) &&
             (!obj.must_not || obj.must_not.length === 0) &&
             ocEmpty
    }
    return false
  }
  // Built-ins field is tri-state:
  //   "default"  → undefined (no `builtins:` key in YAML, server applies default gating)
  //   "none"     → []        (peer-only orchestrator: no built-ins at all)
  //   "csv,…"    → [csv,…]   (allowlist)
  function syncBuiltins(v) {
    if (!editing) return
    const t = String(v || '').trim().toLowerCase()
    if (t === '' || t === 'default') { delete editing.builtins; return }
    if (t === 'none' || t === '[]')  { editing.builtins = [];   return }
    editing.builtins = csvToArr(v)
  }
  function builtinsDisplay() {
    if (!editing) return 'default'
    if (editing.builtins === undefined || editing.builtins === null) return 'default'
    if (editing.builtins.length === 0) return 'none'
    return editing.builtins.join(', ')
  }
  // Tri-state for the builtins radio: 'default' (field absent), 'none' (empty
  // array), 'restricted' (non-empty array).
  function builtinsMode() {
    if (!editing) return 'default'
    if (editing.builtins === undefined || editing.builtins === null) return 'default'
    if (editing.builtins.length === 0) return 'none'
    return 'restricted'
  }

  function confirmToolDescription(name) {
    switch (name) {
      case 'all': return 'Gate every tool call'
      case 'shell_exec': return 'Runs shell commands'
      case 'run_script': return 'Runs local scripts'
      case 'python_eval': return 'Executes Python code'
      case 'write_file': return 'Writes local files'
      case 'http_request': return 'Makes arbitrary HTTP requests'
      case 'fetch_url': return 'Fetches external URLs'
      case 'web_search': return 'Searches the web'
      case 'channel.send': return 'Sends messages to external channels'
      case 'download_file': return 'Downloads files to disk'
      case 'install_library': return 'Installs packages'
      case 'kb_write': return 'Writes to knowledge bases'
      case 'queue_put': return 'Adds work to queues'
      case 'queue_take': return 'Consumes queued work'
      case 'queue_clear': return 'Clears queue contents'
      default: return 'Tool confirmation gate'
    }
  }

  function uniq(arr) {
    const seen = new Set()
    const out = []
    for (const raw of arr || []) {
      const value = String(raw || '').trim()
      if (!value || seen.has(value)) continue
      seen.add(value)
      out.push(value)
    }
    return out
  }

  function setRecommendedConfirmTools() {
    if (!editing) return
    editing.confirm_tools = uniq([...(editing.confirm_tools || []), ...recommendedConfirmTools])
    editing = editing
  }

  function confirmAllTools() {
    if (!editing) return
    editing.confirm_tools = ['all']
    editing = editing
  }

  function clearConfirmTools() {
    if (!editing) return
    editing.confirm_tools = []
    editing = editing
  }

  function selectedRiskyTools() {
    if (!editing) return []
    const names = new Set()
    if (Array.isArray(editing.builtins)) {
      for (const name of editing.builtins) names.add(name)
    }
    if (editing.system_tools || editing.allow_shell || (editing.capabilities || []).includes('system')) {
      for (const name of ['shell_exec', 'run_script', 'install_library', 'write_file', 'download_file']) names.add(name)
    }
    return [...names].filter(name => recommendedConfirmTools.includes(name))
  }

  function unconfirmedRiskyTools() {
    if (!editing) return []
    const confirms = editing.confirm_tools || []
    if (confirms.includes('all') || confirms.includes('*')) return []
    return selectedRiskyTools().filter(name => !confirms.includes(name))
  }

  function select(agent) {
    selected = agent
    editing  = JSON.parse(JSON.stringify(agent))
    // Ensure nested objects always exist in the editor copy
    editing.llm      = editing.llm      || { provider: 'ollama', model: '', temperature: 0.7, max_tokens: 512 }
    editing.memory   = editing.memory   || { read_scopes: ['session'], write_scopes: ['session'], max_tokens: 20 }
    editing.schedule = editing.schedule || { cron: '' }
    editing.webhook  = editing.webhook  || { text_path: '', user_id_path: '', username_path: '', session_id_path: '', thread_id_path: '', include_raw: false }
    editing.tools    = editing.tools    || []
    editing.skills    = editing.skills    || []
    editing.knowledge = editing.knowledge || []
    editing.agents    = editing.agents    || []
    editing.parallel_peer_calls = !!editing.parallel_peer_calls
    editing.structured_peer_results = !!editing.structured_peer_results
    editing.channels  = editing.channels  || []
    editing.confirm_tools = editing.confirm_tools || []
    editing.unattended = !!editing.unattended
    saveMsg = ''
    saveAudit = null
    validationReport = null
    showHistory = false
    strategyManualOverride = false
    strategyAutoNotice = ''
    lastAutoStrategyKey = ''
  }

  function newAgent() {
    selected = null
    editing  = BLANK()
    saveMsg  = ''
    saveAudit = null
    validationReport = null
    showHistory = false
    strategyManualOverride = false
    strategyAutoNotice = ''
    lastAutoStrategyKey = ''
  }

  function openStudioStarter() {
    const starterIntent = [
      'Build a production-ready Soulacy agent.',
      'Ask for the trigger, required tools, memory, delivery channel, schedule, and safety level if they are missing.',
      'Prefer Studio guided workflow generation, validate the whole workflow, and include a simple live test before saving.',
    ].join(' ')
    studioSession.set(null)
    location.hash = 'studio?intent=' + encodeURIComponent(starterIntent)
  }

  async function validateEditing() {
    if (!editing || validating) return
    validating = true
    saveMsg = ''
    try {
      validationReport = await api.agents.validate(editing)
    } catch (e) {
      validationReport = {
        valid: false,
        errors: 1,
        warnings: 0,
        findings: [{ severity: 'error', field: 'validator', message: e.message }],
      }
    }
    validating = false
  }
  async function save(opts = {}) {
    if (!editing) return
    saving  = true
    saveMsg = ''
    saveAudit = null
    try {
      let res
      if (selected) {
        res = await api.agents.update(selected.id, editing, opts)
      } else {
        res = await api.agents.create(editing, opts)
      }
      saveAudit = res?.capability_audit || null
      await load()
      const found = agents.find(a => a.id === editing.id)
      if (found) select(found)
      saveAudit = res?.capability_audit || null
      saveMsg  = '✓ Saved'
    } catch (e) {
      // Server refuses the write when a save would escalate an agent's
      // capability tier while interactive channel bindings exist. Open the
      // blocking ack modal — confirming will retry the same save with the
      // acknowledgement header set.
      if (e.status === 409 && e.body?.needs_ack && e.body?.capability_audit) {
        ackModal = {
          audit: e.body.capability_audit,
          from: 'save',
          retry: () => save({ acknowledgeAudit: true }),
        }
        saveMsg = ''
      } else {
        saveMsg = '✗ ' + e.message
      }
    }
    saving = false
  }

  // Runs from the ack modal's Confirm button. Any error surfaces on the
  // underlying save flow (saveMsg / yamlError); the modal itself just closes.
  async function confirmCapabilityAck() {
    if (!ackModal || ackConfirming) return
    ackConfirming = true
    const retry = ackModal.retry
    ackModal = null
    try { await retry() }
    finally { ackConfirming = false }
  }

  function cancelCapabilityAck() {
    if (ackConfirming) return
    ackModal = null
  }

  async function toggleEnabled(agent, e) {
    e.stopPropagation()
    if (isSystemAgent(agent)) return
    try {
      agent.enabled ? await api.agents.disable(agent.id)
                    : await api.agents.enable(agent.id)
      await load()
      if (selected?.id === agent.id) {
        const found = agents.find(a => a.id === agent.id)
        if (found) select(found)
      }
    } catch (e) { error = e.message }
  }

  async function deleteAgent() {
    if (!selected) return
    if (editingProtected) {
      error = 'System agent is protected'
      return
    }
    if (!confirm(`Delete agent "${selected.id}"? This cannot be undone.`)) return
    deleting = true
    try {
      await api.agents.delete(selected.id)
      editing = null; selected = null
      await load()
    } catch (e) { error = e.message }
    deleting = false
  }

  // ── Inline playground ─────────────────────────────────────────────────────
  // Right-side panel that chats with the currently-selected agent without
  // forcing the user to navigate to the standalone Chat page. Mirrors
  // Langflow's "Playground" sidebar that opens over the canvas — the win is
  // edit-prompt → save → chat in one screen without losing scroll/context.
  //
  // Conversation state is keyed by agent.id so switching between agents
  // preserves a separate transcript per agent (matches the standalone Chat
  // page's mental model). State is intentionally local (not in a global
  // store) so the inline playground and the full Chat page don't fight over
  // the same buffer.
  let showPlay        = false
  let playByAgent     = {}     // agentId → [{role, text, ts}]
  let playInput       = ''
  let playSending     = false
  let playError       = ''
  let playMsgListEl
  let playOverridesOpen = false
  let playUseOverrides = false
  let playProvider = ''
  let playModel = ''
  let playTemperature = ''
  let playTopP = ''
  let playMaxTokens = ''
  let playMaxTurns = ''
  let playResponseFormat = ''
  let playReasoningEffort = ''
  let playPresencePenalty = ''
  let playFrequencyPenalty = ''
  let playToolChoice = ''

  // Derived: the current agent's transcript. We assign through the map so
  // Svelte sees the reactive change.
  $: playMessages = (selected ? (playByAgent[selected.id] || []) : [])

  function appendPlayMsg(agentId, msg) {
    const prev = playByAgent[agentId] || []
    playByAgent = { ...playByAgent, [agentId]: [...prev, msg] }
  }

  async function playSend() {
    if (!selected) return
    const text = playInput.trim()
    if (!text || playSending) return
    playInput   = ''
    playError   = ''
    const agentId = selected.id
    appendPlayMsg(agentId, { role: 'user', text, ts: new Date() })
    playSending = true
    await scrollPlayBottom()
    try {
      const res = await api.chat(agentId, text, 'gui-playground', playgroundOverrides())
      appendPlayMsg(agentId, { role: 'assistant', text: res.reply, ts: new Date() })
    } catch (e) {
      playError = e.message
      appendPlayMsg(agentId, { role: 'system', text: '⚠ ' + e.message, ts: new Date() })
    }
    playSending = false
    await scrollPlayBottom()
  }

  async function scrollPlayBottom() {
    // Tick is imported below
    await tick()
    if (playMsgListEl) playMsgListEl.scrollTop = playMsgListEl.scrollHeight
  }

  function playKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); playSend() }
  }

  function playgroundOverrides() {
    if (!playUseOverrides) return null
    const overrides = {}
    if (playProvider.trim()) overrides.provider = playProvider.trim()
    if (playModel.trim()) overrides.model = playModel.trim()
    if (playTemperature !== '' && !Number.isNaN(Number(playTemperature))) overrides.temperature = Number(playTemperature)
    if (playTopP !== '' && Number(playTopP) > 0) overrides.top_p = Number(playTopP)
    if (playMaxTokens !== '' && Number(playMaxTokens) > 0) overrides.max_tokens = Number(playMaxTokens)
    if (playMaxTurns !== '' && Number(playMaxTurns) > 0) overrides.max_turns = Number(playMaxTurns)
    if (playResponseFormat.trim()) overrides.response_format = playResponseFormat.trim()
    if (playReasoningEffort.trim()) overrides.reasoning_effort = playReasoningEffort.trim()
    if (playPresencePenalty !== '' && !Number.isNaN(Number(playPresencePenalty))) overrides.presence_penalty = Number(playPresencePenalty)
    if (playFrequencyPenalty !== '' && !Number.isNaN(Number(playFrequencyPenalty))) overrides.frequency_penalty = Number(playFrequencyPenalty)
    if (playToolChoice.trim()) overrides.tool_choice = playToolChoice.trim()
    return Object.keys(overrides).length ? overrides : null
  }

  function useSelectedLLMForPlayground() {
    if (!selected) return
    playProvider = selected.llm?.provider || ''
    playModel = selected.llm?.model || ''
    playTemperature = selected.llm?.temperature ?? ''
    playTopP = selected.llm?.top_p ?? ''
    playMaxTokens = selected.llm?.max_tokens || ''
    playMaxTurns = selected.max_turns || ''
    playResponseFormat = selected.llm?.response_format || ''
    playReasoningEffort = selected.llm?.reasoning_effort || ''
    playPresencePenalty = selected.llm?.presence_penalty ?? ''
    playFrequencyPenalty = selected.llm?.frequency_penalty ?? ''
    playToolChoice = selected.llm?.tool_choice || ''
  }

  function clearPlayChat() {
    if (!selected) return
    playByAgent = { ...playByAgent, [selected.id]: [] }
    playError   = ''
  }

  function fmtTime(d) {
    try { return d.toLocaleTimeString() } catch { return '' }
  }

  // ── API export modal ─────────────────────────────────────────────────────
  // Shows ready-to-paste curl / Python / JS snippets for the selected agent's
  // /api/v1/chat endpoint with the active API key pre-filled. Inspired by
  // Langflow's "API" button — the demo always ends with "here's how to call
  // this from your code." Lowers the activation energy for users who came in
  // through the GUI but want to script against the agent later.
  let showExport   = false
  let exportTab    = 'curl'      // 'curl' | 'python' | 'js'
  let packageFileInput = null
  let showPackageImport = false
  let packageImportLoading = false
  let packageImporting = false
  let packageImportError = ''
  let packageImportMsg = ''
  let packageImportRaw = null
  let packageInspection = null
  let packageImportDisabled = true
  let packageImportOverwrite = false
  // Story 7 Bucket 7A — install-time secret gate. When the inspected
  // package's requirements list has any `required_*` entry that isn't
  // available/configured/built_in, the Import button is disabled unless
  // the operator explicitly acknowledges via this checkbox.
  let packageImportAcknowledgeMissing = false
  $: packageMissingRequirements = (packageInspection?.requirements || [])
    .filter(r => (r.kind || '').startsWith('required_') && !['available','configured','built_in','packaged'].includes(r.status))
  $: packageImportBlocked = packageMissingRequirements.length > 0 && !packageImportAcknowledgeMissing

  $: gatewayOrigin = (typeof window !== 'undefined') ? window.location.origin : 'http://127.0.0.1:18789'
  $: apiKeyValue   = $apiKey || '<your-api-key>'

  $: curlSnippet = selected ? `curl -X POST ${gatewayOrigin}/api/v1/chat \\
  -H 'Content-Type: application/json' \\
  -H 'Authorization: Bearer ${apiKeyValue}' \\
  -d '{
    "agent_id": "${selected.id}",
    "user_id":  "api-user",
    "text":     "hello"
  }'` : ''

  $: pythonSnippet = selected ? `import requests

resp = requests.post(
    "${gatewayOrigin}/api/v1/chat",
    headers={"Authorization": "Bearer ${apiKeyValue}"},
    json={
        "agent_id": "${selected.id}",
        "user_id":  "api-user",
        "text":     "hello",
    },
    timeout=120,
)
resp.raise_for_status()
print(resp.json()["reply"])` : ''

  $: jsSnippet = selected ? `const res = await fetch("${gatewayOrigin}/api/v1/chat", {
  method: "POST",
  headers: {
    "Content-Type":  "application/json",
    "Authorization": "Bearer ${apiKeyValue}",
  },
  body: JSON.stringify({
    agent_id: "${selected.id}",
    user_id:  "api-user",
    text:     "hello",
  }),
});
const { reply } = await res.json();
console.log(reply);` : ''

  $: activeSnippet = exportTab === 'python' ? pythonSnippet
                   : exportTab === 'js'     ? jsSnippet
                   : curlSnippet

  function copySnippet() {
    if (!activeSnippet) return
    navigator.clipboard?.writeText(activeSnippet)
  }

  async function downloadAgentPackage() {
    if (!selected) return
    try {
      const { blob, filename } = await api.agents.package(selected.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename || `${selected.id}.soulacy-agent.json`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (e) {
      error = e.message
    }
  }

  function triggerPackageImport() {
    packageImportError = ''
    packageImportMsg = ''
    packageFileInput?.click()
  }

  async function onPackageFilePicked(event) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) return
    showPackageImport = true
    packageImportLoading = true
    packageImportError = ''
    packageImportMsg = ''
    packageInspection = null
    packageImportRaw = null
    try {
      const raw = JSON.parse(await file.text())
      packageImportRaw = raw
      packageInspection = await api.agents.inspectPackage(raw)
    } catch (e) {
      packageImportError = e.message || 'Could not inspect package'
    }
    packageImportLoading = false
  }

  async function importInspectedPackage() {
    if (!packageImportRaw || packageImporting) return
    if (packageImportBlocked) {
      packageImportError = 'This package has missing requirements. Check the acknowledgement box below to import anyway.'
      return
    }
    packageImporting = true
    packageImportError = ''
    packageImportMsg = ''
    try {
      const res = await api.agents.importPackage(packageImportRaw, {
        disabled: packageImportDisabled,
        overwrite: packageImportOverwrite,
        acknowledge_missing: packageImportAcknowledgeMissing,
      })
      await load()
      const id = res?.agent?.id || packageInspection?.agent?.id
      const found = agents.find(a => a.id === id)
      if (found) select(found)
      packageImportMsg = 'Imported package.'
      showPackageImport = false
      packageImportAcknowledgeMissing = false
    } catch (e) {
      packageImportError = e.message || 'Import failed'
      // Backend returns 409 with {missing, requirements, needs_acknowledgement}
      // when the package has required_* entries that aren't satisfied and no
      // acknowledge_missing flag was sent. Surface the list so the operator
      // can see what's blocking them.
      if (e.body?.needs_acknowledgement) {
        packageInspection = {
          ...packageInspection,
          requirements: e.body.requirements || packageInspection?.requirements || [],
        }
      } else if (e.body?.validation) {
        packageInspection = e.body
      }
    }
    packageImporting = false
  }

  // ── Templates ("New from template" modal) ────────────────────────────────
  // Loaded on demand when the user opens the picker. Each entry has the shape
  // { name, display_name, description, tags, source, definition } from the
  // /api/v1/templates endpoint. The `source` is "embedded" or "user" — we
  // show a small badge so users can tell built-ins from their own.
  let showTemplates    = false
  let templates        = []
  let templatesLoading = false
  let templatesError   = ''
  let instantiating    = ''   // name of the template currently being instantiated

  async function openTemplates() {
    showTemplates    = true
    templatesError   = ''
    if (templates.length === 0) {
      templatesLoading = true
      try {
        const res = await api.templates.list()
        templates = res.templates || []
      } catch (e) {
        templatesError = e.message
      }
      templatesLoading = false
    }
  }

  async function useTemplate(t) {
    instantiating = t.name
    try {
      const def = await api.templates.instantiate(t.name)
      await load()                              // refresh agent list
      const fresh = agents.find(a => a.id === def.id)
      if (fresh) select(fresh)                  // open it in the editor
      showTemplates = false
    } catch (e) {
      templatesError = e.message
    }
    instantiating = ''
  }

  // F-GUI-2 — deep-link support: #agents?agent_id=X&doctor=1 auto-opens the
  // Security Doctor drawer after the agent list resolves. Used by Dashboard's
  // Security readiness row and the channel-editor "learn more" callout.
  function checkDoctorHash() {
    try {
      const hash = window.location.hash || ''
      const idx = hash.indexOf('?')
      if (idx < 0) return
      const params = new URLSearchParams(hash.slice(idx + 1))
      if (params.get('doctor') !== '1') return
      const wantId = params.get('agent_id') || ''
      if (!wantId) return
      const target = agents.find(a => a.id === wantId)
      if (target) {
        select(target)
        // Defer to next tick so `selected` is populated when openDoctor reads it.
        Promise.resolve().then(() => openDoctor(target))
      }
    } catch (_) { /* best-effort */ }
  }

  onMount(async () => {
    await load()
    checkDoctorHash()
    loadCatalog()
    loadProviders()
    loadLookups()
  })
</script>

<div class="page">
  <div class="page-header">
    <h1>Deployed Agents</h1>
    <div class="hdr-actions">
      <button class="btn-secondary" on:click={openTemplates}>📋 From template…</button>
      <button class="btn-secondary" on:click={triggerPackageImport} data-tooltip="Inspect and import a .soulacy-agent.json package">Import package…</button>
      <button class="btn-primary"   on:click={newAgent}>+ New Agent</button>
      <input
        bind:this={packageFileInput}
        type="file"
        accept=".json,.soulacy-agent.json,application/json"
        style="display:none"
        on:change={onPackageFilePicked}
      />
    </div>
        <TourButton />
    </div>

  {#if error}
    <div class="banner err">⚠ {error}</div>
  {/if}

  <div class="split">
    <!-- ── Agent list ── -->
    <div class="list-col">
      {#if agents.length === 0}
        <div class="empty empty-onboard">
          <div class="empty-kicker">No deployed agents yet</div>
          <p>Start in Studio for a guided build, deploy a proven template, or import an agent package.</p>
          <button class="empty-primary" type="button" on:click={openStudioStarter}>Open Studio</button>
          <button class="empty-secondary" type="button" on:click={openTemplates}>Use a template</button>
          <button class="empty-secondary" type="button" on:click={triggerPackageImport}>Import package</button>
        </div>
      {/if}
      {#each agents as agent}
        <div class="agent-card" class:active={selected?.id === agent.id}
             role="button"
             tabindex="0"
             on:click={() => select(agent)}
             on:keydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), select(agent))}>
          <div>
            <div class="agent-name">{agent.name || agent.id}</div>
            <div class="agent-meta">
              {agent.trigger} · {agent.llm?.provider || 'ollama'}/{agent.llm?.model || '?'}
            </div>
            <div class="tier-pill" class:read={tiers[agent.id]?.tier === 'read_only'} class:active-tier={tiers[agent.id]?.tier === 'active'} class:privileged={tiers[agent.id]?.tier === 'privileged'} data-tooltip="Security capability tier: read_only, active, or privileged">
              {tierLabel(tiers[agent.id]?.tier)}
            </div>
          </div>
          <button class="toggle" class:on={agent.enabled}
                  data-tooltip={agent.enabled ? 'Click to disable' : 'Click to enable'}
                  disabled={isSystemAgent(agent)}
                  on:click={(e) => toggleEnabled(agent, e)}>
            {agent.enabled ? '●' : '○'}
          </button>
        </div>
      {/each}
    </div>

    <!-- ── Editor ── -->
    <div class="editor-col">
      {#if !editing}
        <div class="empty-panel">Select an agent or create a new one.</div>
      {:else}
        <div class="editor">
          <div class="editor-hdr">
            <span>{selected ? 'Editing: ' + selected.id : 'New Agent'}</span>
            <div class="hdr-actions">
              {#if selected}
                <button class="btn-secondary" on:click={() => showExport = true}
                        data-tooltip="Show API snippets for this agent">&lt;/&gt; API</button>
                <button class="btn-secondary" on:click={downloadAgentPackage}
                        data-tooltip="Download a shareable package with SOUL.yaml, local tool files, and a setup checklist">Package</button>
                <button class="btn-secondary" on:click={() => showPlay = !showPlay}
                        class:on={showPlay}
                        data-tooltip="Toggle inline chat panel">
                  {showPlay ? '× Close' : '💬 Test'}
                </button>
                <button class="btn-danger" on:click={deleteAgent} disabled={deleting || editingProtected} data-tooltip="Permanently delete this agent config">
                  {deleting ? '…' : 'Delete'}
                </button>
              {/if}
              {#if selected}
                <button class="btn-secondary" on:click={openYaml} data-tooltip="View and edit the raw SOUL.yaml">
                  View YAML
                </button>
                <button class="btn-secondary" on:click={openHistory} data-tooltip="View saved versions and restore a prior SOUL.yaml">
                  History
                </button>
                <!-- F-GUI-2 — full doctor report + adversarial dry-run for the
                     S1+S2+S3 pipeline. Deep-linkable via
                     #agents?agent_id=X&doctor=1 from Dashboard's Security row
                     and other cohort surfaces. -->
                <button class="btn-secondary" on:click={() => openDoctor(selected)} data-tooltip="Full security posture + simulate a prompt-injection dry-run">
                  🛡 Security Doctor
                </button>
              {/if}
              <button class="btn-secondary" on:click={validateEditing} disabled={validating} data-tooltip="Verify that the agent manifest is valid and secure">
                {validating ? 'Checking…' : 'Validate'}
              </button>
              <button class="btn-primary" on:click={save} disabled={saving} data-tooltip="Save changes to the agent manifest">
                {saving ? 'Saving…' : 'Save'}
              </button>
              {#if saveMsg}
                <span class="save-msg" class:ok={saveMsg.startsWith('✓')}>{saveMsg}</span>
              {/if}
            </div>
          </div>

          <div class="fields">
            {#if saveAudit?.warnings?.length}
              <div class="save-audit" class:danger={saveAudit.requires_ack}>
                <strong>{saveAudit.requires_ack ? 'Review channel exposure' : 'Capability tier changed'}</strong>
                {#each saveAudit.warnings as warning}
                  <p>{warning}</p>
                {/each}
              </div>
            {/if}
            {#if selectedTier}
              <div class="tier-panel" class:read={selectedTier.tier === 'read_only'} class:active-tier={selectedTier.tier === 'active'} class:privileged={selectedTier.tier === 'privileged'}>
                <div class="tier-panel-head">
                  <span class="tier-pill" class:read={selectedTier.tier === 'read_only'} class:active-tier={selectedTier.tier === 'active'} class:privileged={selectedTier.tier === 'privileged'}>
                    {tierLabel(selectedTier.tier)}
                  </span>
                  <strong>Capability exposure</strong>
                </div>
                <p>{tierSummary(selectedTier)}</p>
                {#if selectedTier.reasons?.length}
                  <ul>
                    {#each selectedTier.reasons.slice(0, 4) as reason}
                      <li>{reason}</li>
                    {/each}
                  </ul>
                {:else}
                  <p class="tier-muted">No privileged reasons detected.</p>
                {/if}
              </div>
            {/if}

            {#if validationReport}
              <div class="validation-panel" class:ok={validationReport.valid && validationReport.warnings === 0}
                   class:warn={validationReport.valid && validationReport.warnings > 0}
                   class:fail={!validationReport.valid}>
                <div class="validation-head">
                  <span>
                    {validationReport.valid ? (validationReport.warnings ? 'Validation warnings' : 'Validation passed') : 'Validation failed'}
                  </span>
                  <span>{validationReport.errors || 0} errors · {validationReport.warnings || 0} warnings</span>
                </div>
                {#if (validationReport.findings || []).length === 0}
                  <div class="validation-empty">No findings.</div>
                {:else}
                  <div class="validation-list">
                    {#each validationReport.findings as finding}
                      <div class="validation-item" class:error={finding.severity === 'error'}>
                        <div class="validation-line">
                          <span class="severity">{finding.severity}</span>
                          <code>{finding.field}</code>
                          <span>{finding.message}</span>
                        </div>
                        {#if finding.suggestion}
                          <div class="validation-suggestion">{finding.suggestion}</div>
                        {/if}
                        {#if finding.alternatives?.length}
                          <div class="validation-alts">
                            {#each finding.alternatives as alt}<span>{alt}</span>{/each}
                          </div>
                        {/if}
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
            {/if}

            <div class="row-2">
              <div class="field">
                <span class="field-label">ID <span class="req">*</span></span>
                <input bind:value={editing.id} placeholder="my-agent"
                       disabled={!!selected} />
              </div>
              <div class="field">
                <span class="field-label">Name</span>
                <input bind:value={editing.name} placeholder="My Agent" />
              </div>
            </div>

            <div class="field">
              <span class="field-label">Description</span>
              <input bind:value={editing.description} placeholder="What this agent does" />
            </div>

            <div class="row-2">
              <div class="field">
                <span class="field-label">Trigger</span>
                <select bind:value={editing.trigger}>
                  <option value="channel">channel (HTTP / Slack / Telegram…)</option>
                  <option value="cron">cron (scheduled)</option>
                  <option value="oneshot">oneshot (run once at startup)</option>
                  <option value="webhook">webhook</option>
                </select>
              </div>
              <div class="field">
                <span class="field-label">Enabled</span>
                <select bind:value={editing.enabled} disabled={editingProtected}>
                  <option value={true}>Yes</option>
                  <option value={false}>No</option>
                </select>
              </div>
            </div>

            {#if editing.trigger === 'cron'}
              <div class="field">
                <span class="field-label">Cron expression</span>
                <input bind:value={editing.schedule.cron}
                       placeholder="0 9 * * *  (9 AM every day)" />
              </div>
            {/if}

            {#if editing.trigger === 'webhook'}
              <div class="sep">Webhook request mapping</div>
              <div class="webhook-card">
                <div class="webhook-endpoint">
                  <span class="webhook-method">POST</span>
                  <code>/api/v1/webhooks/{editing.id || '<agent-id>'}</code>
                </div>
                <div class="webhook-grid">
                  <div class="field">
                    <span class="field-label">Text path</span>
                    <input
                      value={editing.webhook?.text_path || ''}
                      on:input={(e) => setWebhookField('text_path', e.target.value)}
                      placeholder="message.text" />
                  </div>
                  <div class="field">
                    <span class="field-label">User ID path</span>
                    <input
                      value={editing.webhook?.user_id_path || ''}
                      on:input={(e) => setWebhookField('user_id_path', e.target.value)}
                      placeholder="sender.id" />
                  </div>
                  <div class="field">
                    <span class="field-label">Username path</span>
                    <input
                      value={editing.webhook?.username_path || ''}
                      on:input={(e) => setWebhookField('username_path', e.target.value)}
                      placeholder="sender.name" />
                  </div>
                  <div class="field">
                    <span class="field-label">Thread ID path</span>
                    <input
                      value={editing.webhook?.thread_id_path || ''}
                      on:input={(e) => setWebhookField('thread_id_path', e.target.value)}
                      placeholder="conversation.id" />
                  </div>
                  <div class="field">
                    <span class="field-label">Session ID path</span>
                    <input
                      value={editing.webhook?.session_id_path || ''}
                      on:input={(e) => setWebhookField('session_id_path', e.target.value)}
                      placeholder="session.id" />
                  </div>
                  <label class="webhook-check">
                    <input
                      type="checkbox"
                      checked={!!editing.webhook?.include_raw}
                      on:change={(e) => setWebhookField('include_raw', e.target.checked)} />
                    <span>Attach raw payload metadata</span>
                  </label>
                </div>
              </div>
            {/if}

            <div class="field">
              <span class="field-label">System Prompt</span>
              <textarea bind:value={editing.system_prompt} rows="7"
                        placeholder="You are a helpful Soulacy agent…"></textarea>
            </div>

            <!-- ── Persona blocks (identity / personality / non-negotiables) ───────
                 Optional structured sections that are wrapped into the
                 system prompt with consistent framing across every agent.
                 Operators can leave them empty for a "classic" agent.    -->
            <div class="sep">Persona <span class="sep-hint">(optional — leave empty for a classic agent)</span></div>

            <details class="persona-block" open={!!editing.identity}>
              <summary>
                <span class="persona-title">Identity</span>
                <span class="persona-sub">Who is the agent? Used at the top of the prompt so the model has a clear self-concept.</span>
              </summary>
              <div class="persona-body">
                <div class="field">
                  <span class="field-label">
                    Role
                    <small class="field-hint">Their job title or function. Example: "senior research analyst"</small>
                  </span>
                  <input type="text"
                         value={editing.identity?.role || ''}
                         on:input={(e) => ensurePersona('identity', { role: e.target.value })}
                         placeholder="senior research analyst" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Audience
                    <small class="field-hint">Who they're talking to. Helps the model pitch vocabulary and depth.</small>
                  </span>
                  <input type="text"
                         value={editing.identity?.audience || ''}
                         on:input={(e) => ensurePersona('identity', { audience: e.target.value })}
                         placeholder="institutional investors" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Expertise
                    <small class="field-hint">Comma-separated topics. Example: "macroeconomics, monetary policy"</small>
                  </span>
                  <input type="text"
                         value={(editing.identity?.expertise || []).join(', ')}
                         on:input={(e) => ensurePersona('identity', { expertise: csvToArr(e.target.value) })}
                         placeholder="macroeconomics, monetary policy" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Backstory <small class="field-optional">(rarely needed)</small>
                    <small class="field-hint">Only fill this if a backstory materially changes behavior.</small>
                  </span>
                  <textarea rows="2"
                            value={editing.identity?.backstory || ''}
                            on:input={(e) => ensurePersona('identity', { backstory: e.target.value })}
                            placeholder=""></textarea>
                </div>
              </div>
            </details>

            <details class="persona-block" open={!!editing.personality}>
              <summary>
                <span class="persona-title">Personality</span>
                <span class="persona-sub">How they speak. Tone, voice, and soft style preferences. Not enforced — for hard rules use Non-Negotiables.</span>
              </summary>
              <div class="persona-body">
                <div class="field">
                  <span class="field-label">
                    Tone
                    <small class="field-hint">Emotional/professional register. Example: "concise, slightly dry"</small>
                  </span>
                  <input type="text"
                         value={editing.personality?.tone || ''}
                         on:input={(e) => ensurePersona('personality', { tone: e.target.value })}
                         placeholder="concise, slightly dry" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Voice
                    <small class="field-hint">Structural style. Example: "third-person observations, never 'I think'"</small>
                  </span>
                  <input type="text"
                         value={editing.personality?.voice || ''}
                         on:input={(e) => ensurePersona('personality', { voice: e.target.value })}
                         placeholder="third-person observations, never 'I think'" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Prefer
                    <small class="field-hint">Comma-separated. Things to lean into. Example: "active voice, named sources"</small>
                  </span>
                  <input type="text"
                         value={(editing.personality?.prefer || []).join(', ')}
                         on:input={(e) => ensurePersona('personality', { prefer: csvToArr(e.target.value) })}
                         placeholder="active voice, named sources" />
                </div>
                <div class="field">
                  <span class="field-label">
                    Avoid
                    <small class="field-hint">Comma-separated. Things to skip. Example: "exclamation marks, hedging, emojis"</small>
                  </span>
                  <input type="text"
                         value={(editing.personality?.avoid || []).join(', ')}
                         on:input={(e) => ensurePersona('personality', { avoid: csvToArr(e.target.value) })}
                         placeholder="exclamation marks, hedging, emojis" />
                </div>
              </div>
            </details>

            <details class="persona-block persona-rules" open={!!editing.non_negotiables}>
              <summary>
                <span class="persona-title">Non-negotiables <span class="persona-tag">HARD RULES</span></span>
                <span class="persona-sub">Rules the agent must follow even if the user asks it to do otherwise. Rendered with extra weight in the prompt.</span>
              </summary>
              <div class="persona-body">
                <div class="field">
                  <span class="field-label persona-must-label">
                    MUST
                    <small class="field-hint">Things the agent must ALWAYS do. One per line.</small>
                  </span>
                  <textarea rows="3"
                            value={(editing.non_negotiables?.must || []).join('\n')}
                            on:input={(e) => ensurePersona('non_negotiables', { must: linesToArr(e.target.value) })}
                            placeholder={`cite every numeric claim with [n]\nrespond in the same language as the most recent user message`}></textarea>
                </div>
                <div class="field">
                  <span class="field-label persona-mustnot-label">
                    MUST NOT
                    <small class="field-hint">Things the agent must NEVER do. One per line.</small>
                  </span>
                  <textarea rows="3"
                            value={(editing.non_negotiables?.must_not || []).join('\n')}
                            on:input={(e) => ensurePersona('non_negotiables', { must_not: linesToArr(e.target.value) })}
                            placeholder={`reveal any environment variable\ngive legal or medical advice\nclaim to be human if asked directly`}></textarea>
                </div>
                <div class="field-row3">
                  <div class="field">
                    <span class="field-label">
                      Format
                      <small class="field-hint">Mechanical output format.</small>
                    </span>
                    <select value={editing.non_negotiables?.output_constraints?.format || ''}
                            on:change={(e) => ensurePersona('non_negotiables', { output_constraints: { ...(editing.non_negotiables?.output_constraints || {}), format: e.target.value } })}>
                      <option value="">(any)</option>
                      <option value="markdown">markdown</option>
                      <option value="plain">plain</option>
                      <option value="json">json</option>
                      <option value="code">code</option>
                    </select>
                  </div>
                  <div class="field">
                    <span class="field-label">
                      Max words
                      <small class="field-hint">0 = no limit.</small>
                    </span>
                    <input type="number" min="0" max="10000"
                           value={editing.non_negotiables?.output_constraints?.max_length || 0}
                           on:input={(e) => ensurePersona('non_negotiables', { output_constraints: { ...(editing.non_negotiables?.output_constraints || {}), max_length: parseInt(e.target.value || 0) } })} />
                  </div>
                  <div class="field">
                    <span class="field-label">
                      Min words
                      <small class="field-hint">0 = no minimum.</small>
                    </span>
                    <input type="number" min="0" max="10000"
                           value={editing.non_negotiables?.output_constraints?.min_length || 0}
                           on:input={(e) => ensurePersona('non_negotiables', { output_constraints: { ...(editing.non_negotiables?.output_constraints || {}), min_length: parseInt(e.target.value || 0) } })} />
                  </div>
                </div>
                <div class="persona-note">
                  <strong>Note:</strong> in this release these rules become extra-weighted text in the system prompt. Post-LLM validation (auto-rewrite on violation) is on the roadmap.
                </div>
              </div>
            </details>

            <div class="sep">LLM</div>

            <div class="field">
              <span class="field-label" data-tooltip={llmTips.executionProfile}>Execution profile <span class="optional">(applies a tuned starting point; fields remain editable)</span></span>
              <select data-tooltip={llmTips.executionProfile} on:change={(e) => { if (e.target.value) { applyExecutionProfile(e.target.value); e.target.value = '' } }}>
                <option value="">Choose a profile…</option>
                {#each Object.entries(executionProfiles) as [key, profile]}
                  <option value={key}>{profile.label}</option>
                {/each}
              </select>
            </div>

            <div class="row-3">
              <div class="field">
                <span class="field-label" data-tooltip={llmTips.provider}>Provider</span>
                <select bind:value={editing.llm.provider} on:change={onProviderChange} data-tooltip={llmTips.provider}>
                  {#if enabledProviders.length === 0}
                    <option value={editing.llm.provider}>{editing.llm.provider || 'ollama'}</option>
                  {:else}
                    {#if editing.llm.provider && !enabledProviders.some(p => p.id === editing.llm.provider)}
                      <option value={editing.llm.provider}>{editing.llm.provider} (disabled/unregistered)</option>
                    {/if}
                    {#each enabledProviders as p}
                      <option value={p.id}>
                        {p.id}
                      </option>
                    {/each}
                  {/if}
                </select>
              </div>
              <div class="field">
                <span class="field-label field-label-row" data-tooltip={llmTips.model}>
                  <span>
                    Model
                    {#if modelsLoading[editing.llm.provider]}
                      <span class="mloading">loading…</span>
                    {:else if modelsError[editing.llm.provider]}
                      <span class="merror" data-tooltip={modelsError[editing.llm.provider]}>(can't reach provider)</span>
                    {:else if modelOptions.length > 0}
                      <span class="mhint">{modelOptions.length} options</span>
                    {/if}
                  </span>
                  <button type="button"
                          class="mini-link"
                          data-tooltip="Reload this provider's model list after changing credentials, OmniRoute routing, or provider settings."
                          on:click={() => refreshModels(editing.llm.provider)}>
                    Refresh
                  </button>
                </span>
                <!-- Keyed {#each (m)} prevents Svelte from mutating <option> values
                     in-place during list updates. Current model is always at [0]
                     (from modelOptions), so bind:value always has a match and the
                     browser never resets to the first option. -->
                <select bind:value={editing.llm.model} on:change={onModelChange} data-tooltip={llmTips.model}>
                  {#if !editing.llm.model}
                    <option value="">— pick a model —</option>
                  {/if}
                  {#each modelOptions as m (m)}
                    <option value={m}>{m}</option>
                  {/each}
                  <option value="__custom__">Custom (type below)…</option>
                </select>
                {#if editing.llm.model === '__custom__'}
                  <input bind:value={editing.llm.model}
                         placeholder="Enter model name"
                         on:focus={() => editing.llm.model = ''} />
                {/if}
                {#if modelStatus}
                  <div class:ok={modelStatus.kind === 'ok'}
                       class:warn={modelStatus.kind === 'warn'}
                       class:info={modelStatus.kind === 'info'}
                       class="model-status"
                       data-tooltip={modelStatus.detail}>
                    <b>{modelStatus.label}</b>
                    <span>{modelStatus.detail}</span>
                  </div>
                {/if}
              </div>
              <div class="field">
                <span class="field-label" data-tooltip={llmTips.maxTokens}>Max tokens</span>
                <input type="number" bind:value={editing.llm.max_tokens} min="64" max="8192" data-tooltip={llmTips.maxTokens} />
              </div>
            </div>

            <div class="row-3">
              <div class="field">
                <span class="field-label" data-tooltip={llmTips.temperature}>Temperature</span>
                <input type="number" bind:value={editing.llm.temperature}
                       min="0" max="2" step="0.05" data-tooltip={llmTips.temperature} />
              </div>
              <div class="field">
                <span class="field-label" data-tooltip={llmTips.topP}>Top P</span>
                <input type="number" bind:value={editing.llm.top_p}
                       min="0" max="1" step="0.05" placeholder="provider default" data-tooltip={llmTips.topP} />
              </div>
              <div class="field">
                <span class="field-label" data-tooltip={llmTips.maxTurns}>Max turns</span>
                <input type="number" bind:value={editing.max_turns} min="1" max="50" data-tooltip={llmTips.maxTurns} />
              </div>
            </div>

            <details class="persona-box">
              <summary>Advanced LLM tuning</summary>
              <div class="persona-inner">
                <div class="field-row3">
                  <div class="field">
                    <span class="field-label" data-tooltip={llmTips.responseFormat}>Response format</span>
                    <select bind:value={editing.llm.response_format} data-tooltip={llmTips.responseFormat}>
                      <option value="">provider default</option>
                      <option value="json">json</option>
                      <option value="json_schema">json_schema</option>
                    </select>
                  </div>
                  <div class="field">
                    <span class="field-label" data-tooltip={llmTips.reasoningEffort}>Reasoning effort</span>
                    <select bind:value={editing.llm.reasoning_effort} data-tooltip={llmTips.reasoningEffort}>
                      <option value="">provider default</option>
                      <option value="low">low</option>
                      <option value="medium">medium</option>
                      <option value="high">high</option>
                    </select>
                  </div>
                  <div class="field">
                    <span class="field-label" data-tooltip={llmTips.presencePenalty}>Presence penalty</span>
                    <input type="number" bind:value={editing.llm.presence_penalty} min="-2" max="2" step="0.1" placeholder="0" data-tooltip={llmTips.presencePenalty} />
                  </div>
                </div>
                <div class="field-row3">
                  <div class="field">
                    <span class="field-label" data-tooltip={llmTips.frequencyPenalty}>Frequency penalty</span>
                    <input type="number" bind:value={editing.llm.frequency_penalty} min="-2" max="2" step="0.1" placeholder="0" data-tooltip={llmTips.frequencyPenalty} />
                  </div>
                </div>
              </div>
            </details>

            <div class="field">
              <span class="field-label" data-tooltip={llmTips.toolChoice}>Tool choice <span class="optional">(controls turn-1 tool selection — agent__&lt;peer&gt; triggers the engine's auto-delegate path)</span></span>
              <ChipPicker
                value={editing.llm.tool_choice ? [editing.llm.tool_choice] : []}
                options={toolChoiceOptions}
                placeholder="(default — auto)"
                single={true}
                allowFreeform={true}
                on:change={(e) => editing.llm.tool_choice = e.detail[0] || ''}
              />
            </div>

            <div class="sep">Memory</div>
            <div class="row-2">
              <div class="field">
                <span class="field-label">Read scopes <span class="optional">(which memory tiers the agent loads as context)</span></span>
                <ChipPicker
                  value={editing.memory?.read_scopes || []}
                  options={memoryScopeOptions}
                  placeholder="Pick scopes (typically: session)"
                  on:change={(e) => { editing.memory = editing.memory || {}; editing.memory.read_scopes = e.detail }}
                />
              </div>
              <div class="field">
                <span class="field-label">Write scopes <span class="optional">(which memory tiers the agent persists into)</span></span>
                <ChipPicker
                  value={editing.memory?.write_scopes || []}
                  options={memoryScopeOptions}
                  placeholder="Pick scopes (typically: session)"
                  on:change={(e) => { editing.memory = editing.memory || {}; editing.memory.write_scopes = e.detail }}
                />
              </div>
            </div>
            <div class="field">
              <span class="field-label">Memory max tokens <span class="optional">(how many recent entries to inject into context)</span></span>
              <input type="number" bind:value={editing.memory.max_tokens} min="0" max="100000" />
            </div>

            <!-- ── Reasoning loop ── -->
            <div class="sep">Execution strategy <span class="optional">(how the agent runs its tools)</span></div>
            <div class="field">
              <span class="field-label" data-tooltip="Controls whether the agent uses native tool-calling, an advanced/manual ReAct loop, or a plan-then-execute loop. Capable models should usually stay on Auto.">Strategy</span>
              <div class="strategy-cards">
                {#each [
                  { val: '',             icon: '✨', title: 'Auto',          desc: 'Recommended — native tool-calling; engine picks the loop' },
                  { val: 'react',        icon: '🔄', title: 'ReAct',         desc: 'Advanced/manual — force think → act → observe' },
                  { val: 'plan_execute', icon: '📋', title: 'Plan-Execute',  desc: 'Force decompose → execute plan steps' },
                ] as s}
                  <button
                    class="strategy-card {(editing.reasoning?.strategy||'') === s.val ? 'active' : ''}"
                    data-tooltip={s.desc}
                    on:click={() => setStrategy(s.val)}
                  >
                    <span class="sc-icon">{s.icon}</span>
                    <span class="sc-title">{s.title}</span>
                    <span class="sc-desc">{s.desc}</span>
                  </button>
                {/each}
              </div>
              {#if nativeTooling?.capable}
                <div class="strategy-advice info" data-tooltip={nativeTooling.reason}>
                  Auto recommended for {editing.llm.provider}/{editing.llm.model}. {nativeTooling.reason}
                </div>
              {/if}
              {#if strategyAutoNotice}
                <div class="strategy-advice ok">{strategyAutoNotice}</div>
              {/if}
            </div>

            {#if editing.reasoning?.strategy}
              <div class="field-row">
                <div class="field">
                  <span class="field-label" data-tooltip={llmTips.maxSteps}>Max steps <span class="optional">(default 8)</span></span>
                  <input type="number" min="1" max="50"
                    value={editing.reasoning?.max_steps || ''}
                    data-tooltip={llmTips.maxSteps}
                    on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.max_steps = Number(e.target.value)||0 }} />
                </div>
                {#if editing.reasoning?.strategy === 'plan_execute'}
                  <div class="field">
                    <span class="field-label" data-tooltip={llmTips.maxPlanSteps}>Max plan steps <span class="optional">(default 6)</span></span>
                    <input type="number" min="1" max="20"
                      value={editing.reasoning?.max_plan_steps || ''}
                      data-tooltip={llmTips.maxPlanSteps}
                      on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.max_plan_steps = Number(e.target.value)||0 }} />
                  </div>
                {/if}
                <div class="field">
                  <span class="field-label" data-tooltip={llmTips.stepTimeout}>Step timeout <span class="optional">(e.g. 30s)</span></span>
                  <input type="text" placeholder="30s"
                    value={editing.reasoning?.step_timeout || ''}
                    data-tooltip={llmTips.stepTimeout}
                    on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.step_timeout = e.target.value }} />
                </div>
                <div class="field">
                  <span class="field-label" data-tooltip={llmTips.totalTimeout}>Total timeout <span class="optional">(e.g. 180s)</span></span>
                  <input type="text" placeholder="180s"
                    value={editing.reasoning?.total_timeout || ''}
                    data-tooltip={llmTips.totalTimeout}
                    on:input={e => { editing.reasoning = editing.reasoning||{}; editing.reasoning.total_timeout = e.target.value }} />
                </div>
              </div>

              <details class="persona-box">
                <summary>Reasoning phase tuning</summary>
                <div class="persona-inner">
                  {#each [
                    { key: 'think', label: 'Think', hint: 'tool-selection step' },
                    { key: 'plan', label: 'Plan', hint: 'plan_execute decomposition' },
                    { key: 'reflect', label: 'Reflect', hint: 'final synthesis' },
                  ] as phase}
                    {@const cfg = editing.reasoning?.[phase.key] || {}}
                    <div class="sep mini-sep">{phase.label} <span class="optional">({phase.hint})</span></div>
                    <div class="field-row3">
                      <div class="field">
                        <span class="field-label" data-tooltip={llmTips.phaseTemperature}>Temperature</span>
                        <input type="number" min="0" max="2" step="0.05"
                          value={cfg.temperature ?? ''}
                          data-tooltip={llmTips.phaseTemperature}
                          on:input={e => {
                            editing.reasoning = editing.reasoning || {}
                            editing.reasoning[phase.key] = editing.reasoning[phase.key] || {}
                            editing.reasoning[phase.key].temperature = Number(e.target.value) || 0
                          }} />
                      </div>
                      <div class="field">
                        <span class="field-label" data-tooltip={llmTips.phaseTopP}>Top P</span>
                        <input type="number" min="0" max="1" step="0.05"
                          value={cfg.top_p ?? ''}
                          data-tooltip={llmTips.phaseTopP}
                          on:input={e => {
                            editing.reasoning = editing.reasoning || {}
                            editing.reasoning[phase.key] = editing.reasoning[phase.key] || {}
                            editing.reasoning[phase.key].top_p = Number(e.target.value) || 0
                          }} />
                      </div>
                      <div class="field">
                        <span class="field-label" data-tooltip={llmTips.phaseMaxTokens}>Max tokens</span>
                        <input type="number" min="64" max="8192"
                          value={cfg.max_tokens || ''}
                          data-tooltip={llmTips.phaseMaxTokens}
                          on:input={e => {
                            editing.reasoning = editing.reasoning || {}
                            editing.reasoning[phase.key] = editing.reasoning[phase.key] || {}
                            editing.reasoning[phase.key].max_tokens = Number(e.target.value) || 0
                          }} />
                      </div>
                    </div>
                  {/each}
                  <div class="sep mini-sep">Format <span class="optional">(internal response shape)</span></div>
                  <div class="field-row3">
                    {#each [
                      { key: 'think', label: 'Think' },
                      { key: 'plan', label: 'Plan' },
                      { key: 'reflect', label: 'Reflect' },
                    ] as phase}
                      {@const cfg = editing.reasoning?.[phase.key] || {}}
                      <div class="field">
                        <span class="field-label" data-tooltip={llmTips.phaseFormat}>{phase.label} format</span>
                        <select
                          value={cfg.response_format || ''}
                          data-tooltip={llmTips.phaseFormat}
                          on:change={e => {
                            editing.reasoning = editing.reasoning || {}
                            editing.reasoning[phase.key] = editing.reasoning[phase.key] || {}
                            editing.reasoning[phase.key].response_format = e.target.value
                            editing = editing
                          }}>
                          <option value="">provider default</option>
                          <option value="json">json</option>
                          <option value="json_schema">json_schema</option>
                        </select>
                      </div>
                    {/each}
                  </div>
                </div>
              </details>
            {/if}

            <!-- ── Brain memory ── -->
            <div class="sep">Brain memory <span class="optional">(long-term episodic / procedural / semantic)</span></div>
            <div class="brain-mem-grid">
              {#each [
                { key: 'episodic',   icon: '🕐', label: 'Episodic',   desc: 'Task history. Injected as "Recent task history".' },
                { key: 'semantic',   icon: '🔍', label: 'Semantic',   desc: 'Knowledge chunks (vector search).' },
                { key: 'procedural', icon: '📋', label: 'Procedural', desc: 'Operating rules the agent learns over time.' },
              ] as layer}
                {@const cfg = editing.brain_memory?.[layer.key] || {}}
                <div class="bm-card {cfg.enabled ? 'enabled' : ''}">
                  <div class="bm-header">
                    <span class="bm-icon">{layer.icon}</span>
                    <span class="bm-label">{layer.label}</span>
                    <label class="toggle-sm">
                      <input type="checkbox" checked={!!cfg.enabled}
                        on:change={e => {
                          editing.brain_memory = editing.brain_memory || {}
                          editing.brain_memory[layer.key] = editing.brain_memory[layer.key] || {}
                          editing.brain_memory[layer.key].enabled = e.target.checked
                          editing = editing
                        }} />
                      <span class="toggle-track-sm"></span>
                    </label>
                  </div>
                  <div class="bm-desc">{layer.desc}</div>
                  {#if cfg.enabled}
                    <div class="bm-fields">
                      <div class="bm-field">
                        <span class="bm-field-lbl">Max inject</span>
                        <input class="bm-num" type="number" min="1" max="50"
                          value={cfg.max_inject || ''}
                          on:input={e => {
                            editing.brain_memory[layer.key].max_inject = Number(e.target.value)||0
                            editing = editing
                          }} />
                      </div>
                      {#if layer.key === 'procedural'}
                        <label class="bm-check">
                          <input type="checkbox" checked={!!cfg.auto_update}
                            on:change={e => {
                              editing.brain_memory[layer.key].auto_update = e.target.checked
                              editing = editing
                            }} />
                          <span>Auto-update after each task</span>
                        </label>
                      {/if}
                    </div>
                  {/if}
                </div>
              {/each}
            </div>

            <div class="sep">Learning loop <span class="optional">(reviewable post-run proposals)</span></div>
            <div class="learning-card {editing.learning?.enabled ? 'enabled' : ''}">
              <div class="bm-header">
                <span class="bm-icon">✨</span>
                <span class="bm-label">Create learning proposals after successful runs</span>
                <label class="toggle-sm">
                  <input type="checkbox" checked={!!editing.learning?.enabled}
                    on:change={e => {
                      editing.learning = editing.learning || {}
                      editing.learning.enabled = e.target.checked
                      if (!editing.learning.min_chars) editing.learning.min_chars = 160
                      if (!editing.learning.max_proposals) editing.learning.max_proposals = 3
                      editing = editing
                    }} />
                  <span class="toggle-track-sm"></span>
                </label>
              </div>
              <div class="bm-desc">Soulacy stores proposed memories and procedures for human review in Learning before applying them.</div>
              {#if editing.learning?.enabled}
                <div class="learning-fields">
                  <div class="field">
                    <span class="field-label">Minimum run text</span>
                    <input type="number" min="80" max="5000"
                      value={editing.learning?.min_chars || 160}
                      on:input={e => {
                        editing.learning = editing.learning || {}
                        editing.learning.min_chars = Number(e.target.value)||160
                        editing = editing
                      }} />
                  </div>
                  <div class="field">
                    <span class="field-label">Max proposals per run</span>
                    <input type="number" min="1" max="4"
                      value={editing.learning?.max_proposals || 3}
                      on:input={e => {
                        editing.learning = editing.learning || {}
                        editing.learning.max_proposals = Number(e.target.value)||3
                        editing = editing
                      }} />
                  </div>
                </div>
              {/if}
            </div>

            {#if editing.trigger === 'channel'}
              <div class="sep">Delivery channels</div>
              <div class="field">
                <span class="field-label">Bound channels — pick from registered channel adapters</span>
                <ChipPicker
                  value={editing.channels || []}
                  options={channelOptions}
                  placeholder="Type to search (http, telegram, slack…)"
                  allowFreeform={true}
                  on:change={(e) => editing.channels = e.detail}
                />
              </div>
            {/if}

            <div class="sep">
              Tools
              <button class="add-btn" type="button" on:click={addTool}>+ Add tool</button>
            </div>

            {#if (editing.tools || []).length === 0}
              <div class="tools-empty">
                No tools wired. Click <strong>+ Add tool</strong> to give this agent a Python script, MCP tool, or built-in.
                {#if catalog.python_tools.length > 0}
                  &nbsp;Available Python scripts: <em>{catalog.python_tools.map(t => t.name).join(', ')}</em>
                {/if}
              </div>
            {:else}
              {#each editing.tools as tool, i (i)}
                <div class="tool-card">
                  <div class="tool-hdr">
                    <span class="tool-idx">#{i + 1}</span>
                    <input class="tool-name-input"
                           bind:value={tool.name}
                           placeholder="tool_name (snake_case)" />
                    <div class="tool-ops">
                      <button data-tooltip="Move up"   on:click={() => moveTool(i, -1)} disabled={i === 0}>↑</button>
                      <button data-tooltip="Move down" on:click={() => moveTool(i, +1)} disabled={i === editing.tools.length - 1}>↓</button>
                      <button data-tooltip="Remove" class="rm" on:click={() => removeTool(i)}>✕</button>
                    </div>
                  </div>

                  <div class="field">
                    <span class="field-label">Python file — type a path or 📂 Browse the tool catalog</span>
                    <FilePicker
                      value={tool.python_file || ''}
                      options={pythonFileOptions}
                      placeholder="~/.soulacy/tools/your_tool.py"
                      on:change={(e) => { editing.tools[i].python_file = e.detail; editing = editing }}
                      on:pick={(e) => onPythonFilePicked(i, e.detail.value)}
                    />
                  </div>

                  <div class="field">
                    <span class="field-label">Description (shown to the LLM)</span>
                    <input bind:value={tool.description}
                           placeholder="What this tool does. The LLM picks tools by description." />
                  </div>

                  <div class="tool-grid">
                    <div class="field">
                      <span class="field-label" data-tooltip={llmTips.toolTimeout}>Timeout <span class="optional">(optional — overrides global; e.g. 30m, 1h)</span></span>
                      <input bind:value={tool.timeout}
                             placeholder="30s (defaults to runtime.tool_timeout)" />
                    </div>

                    <div class="field">
                      <span class="field-label" data-tooltip={llmTips.toolRetries}>Retries <span class="optional">(0-5)</span></span>
                      <input type="number"
                             min="0"
                             max="5"
                             step="1"
                             value={tool.retries ?? ''}
                             placeholder="0"
                             on:input={(e) => updateToolRetries(i, e.target.value)} />
                    </div>

                    <div class="field">
                      <span class="field-label" data-tooltip={llmTips.toolRetryBackoff}>Retry backoff <span class="optional">(e.g. 1s)</span></span>
                      <input bind:value={tool.retry_backoff}
                             placeholder="1s" />
                    </div>
                  </div>

                  <div class="field">
                    <span class="field-label">Parameters schema (JSON)</span>
                    <textarea rows="5"
                              value={paramsJson(tool)}
                              on:input={(e) => updateParams(i, e.target.value)}
                              spellcheck="false"></textarea>
                  </div>
                </div>
              {/each}
            {/if}

            {#if (catalog.mcp_tools || []).length > 0}
              <div class="catalog-hint">
                <strong>MCP tools auto-injected:</strong>
                {#each catalog.mcp_tools.slice(0, 8) as t}
                  <span class="chip">{t.full_name}</span>
                {/each}
                {#if catalog.mcp_tools.length > 8}<span class="more">+{catalog.mcp_tools.length - 8} more</span>{/if}
              </div>
            {/if}

            <div class="sep">Skills</div>
            <div class="field">
              <span class="field-label">Skills available to this agent — pick from installed skills</span>
              <ChipPicker
                value={editing.skills || []}
                options={skillOptions}
                placeholder={availableSkills.length === 0 ? 'No skills installed' : 'Type to search…'}
                allowFreeform={true}
                on:change={(e) => editing.skills = e.detail}
              />
            </div>

            <div class="sep">Knowledge bases</div>
            <div class="field">
              <span class="field-label">KBs the agent may search via <code>kb_search</code> — pick from created KBs</span>
              <ChipPicker
                value={editing.knowledge || []}
                options={kbOptions}
                placeholder={availableKBs.length === 0 ? 'No knowledge bases yet — create one on the Knowledge page' : 'Type to search…'}
                allowFreeform={true}
                on:change={(e) => editing.knowledge = e.detail}
              />
            </div>

            <div class="sep">Peer agents</div>
            <div class="field">
              <span class="field-label">Other agents this agent may invoke as <code>agent__&lt;id&gt;</code> tools — pick from loaded agents</span>
              <ChipPicker
                value={editing.agents || []}
                options={peerAgentOptions}
                placeholder={peerAgentOptions.length === 0 ? 'No other agents loaded' : 'Type to search…'}
                allowFreeform={true}
                on:change={(e) => editing.agents = e.detail}
              />
            </div>
            <div class="field-row">
              <label class="check-row peer-toggle" data-tooltip={llmTips.parallelPeerCalls}>
                <input type="checkbox" bind:checked={editing.parallel_peer_calls} />
                <span>Run independent peer calls in parallel</span>
              </label>
              <label class="check-row peer-toggle" data-tooltip={llmTips.structuredPeerResults}>
                <input type="checkbox" bind:checked={editing.structured_peer_results} />
                <span>Return structured peer-result envelopes</span>
              </label>
            </div>

            <div class="sep">Built-in tools <span class="optional">(advanced)</span></div>
            <div class="field">
              <div class="radio-row">
                <label class="radio-opt">
                  <input type="radio" name="builtins-mode" value="default"
                         checked={builtinsMode() === 'default'}
                         on:change={() => { delete editing.builtins; editing = editing }} />
                  Default <span class="optional">(all gated built-ins auto-injected)</span>
                </label>
                <label class="radio-opt">
                  <input type="radio" name="builtins-mode" value="none"
                         checked={builtinsMode() === 'none'}
                         on:change={() => { editing.builtins = []; editing = editing }} />
                  None <span class="optional">(peer-only orchestrator)</span>
                </label>
                <label class="radio-opt">
                  <input type="radio" name="builtins-mode" value="restricted"
                         checked={builtinsMode() === 'restricted'}
                         on:change={() => { editing.builtins = editing.builtins?.length ? editing.builtins : []; editing = editing }} />
                  Restricted to:
                </label>
              </div>
              {#if builtinsMode() === 'restricted'}
                <ChipPicker
                  value={editing.builtins || []}
                  options={builtinOptions}
                  placeholder="Pick which built-ins this agent can use"
                  placement="up"
                  on:change={(e) => editing.builtins = e.detail}
                />
              {/if}
            </div>

            <div class="sep">Safety policy <span class="optional">(confirm before risky actions)</span></div>
            <div class="safety-card">
              <div class="field">
                <span class="field-label" data-tooltip={llmTips.confirmTools}>Confirm tools</span>
                <ChipPicker
                  value={editing.confirm_tools || []}
                  options={confirmToolOptions}
                  placeholder="Pick tools that require approval before running"
                  placement="up"
                  allowFreeform={true}
                  on:change={(e) => editing.confirm_tools = e.detail}
                />
                <span class="field-hint">
                  {llmTips.confirmTools}
                </span>
              </div>
              <div class="safety-actions">
                <button class="btn-secondary small" type="button" on:click={setRecommendedConfirmTools}>Use recommended</button>
                <button class="btn-secondary small" type="button" on:click={confirmAllTools}>Confirm all tools</button>
                <button class="btn-secondary small" type="button" on:click={clearConfirmTools}>Clear</button>
              </div>
              <label class="check-row safety-toggle" data-tooltip={llmTips.unattended}>
                <input type="checkbox" bind:checked={editing.unattended} />
                <span>Unattended runs may auto-approve confirmation gates</span>
              </label>
              {#if unconfirmedRiskyTools().length}
                <div class="safety-warn">
                  Risky selected tools are not confirmation-gated:
                  {#each unconfirmedRiskyTools() as tool}
                    <code>{tool}</code>
                  {/each}
                </div>
              {/if}
            </div>
          </div>
        </div>
      {/if}
    </div>

    <!-- ── Inline playground (right column) ── -->
    {#if showPlay && selected}
      <div class="play-col">
        <div class="play-hdr">
          <span>Playground · <code>{selected.id}</code></span>
          <div class="play-hdr-actions">
            <button class="btn-secondary small" class:on={playOverridesOpen} on:click={() => playOverridesOpen = !playOverridesOpen}>Params</button>
            <button class="btn-secondary small" on:click={clearPlayChat}
                    disabled={playSending || playMessages.length === 0}>Clear</button>
          </div>
        </div>

        {#if playOverridesOpen}
          <div class="play-params">
            <label class="check-row">
              <input type="checkbox" bind:checked={playUseOverrides} />
              Override this run
            </label>
            <div class="param-actions">
              <button class="btn-secondary small" type="button" on:click={useSelectedLLMForPlayground}>Use agent defaults</button>
            </div>
            <div class="param-grid">
              <label data-tooltip={llmTips.playgroundProvider}>
                <span>Provider</span>
                <select bind:value={playProvider} disabled={!playUseOverrides} data-tooltip={llmTips.playgroundProvider}>
                  <option value="">unchanged</option>
                  {#if playProvider && !enabledProviders.some(p => p.id === playProvider)}
                    <option value={playProvider}>{playProvider} (disabled/unregistered)</option>
                  {/if}
                  {#each enabledProviders as p}
                    <option value={p.id}>{p.id}</option>
                  {/each}
                </select>
              </label>
              <label data-tooltip={llmTips.playgroundModel}>
                <span>Model</span>
                <input bind:value={playModel} placeholder="unchanged" disabled={!playUseOverrides} data-tooltip={llmTips.playgroundModel} />
              </label>
              <label data-tooltip={llmTips.temperature}>
                <span>Temperature</span>
                <input type="number" step="0.1" min="0" max="2" bind:value={playTemperature} placeholder="unchanged" disabled={!playUseOverrides} data-tooltip={llmTips.temperature} />
              </label>
              <label data-tooltip={llmTips.topP}>
                <span>Top P</span>
                <input type="number" step="0.05" min="0" max="1" bind:value={playTopP} placeholder="unchanged" disabled={!playUseOverrides} data-tooltip={llmTips.topP} />
              </label>
              <label data-tooltip={llmTips.maxTokens}>
                <span>Max tokens</span>
                <input type="number" min="1" bind:value={playMaxTokens} placeholder="unchanged" disabled={!playUseOverrides} data-tooltip={llmTips.maxTokens} />
              </label>
              <label data-tooltip={llmTips.maxTurns}>
                <span>Max turns</span>
                <input type="number" min="1" max="100" bind:value={playMaxTurns} placeholder="unchanged" disabled={!playUseOverrides} data-tooltip={llmTips.maxTurns} />
              </label>
              <label data-tooltip={llmTips.responseFormat}>
                <span>Format</span>
                <select bind:value={playResponseFormat} disabled={!playUseOverrides} data-tooltip={llmTips.responseFormat}>
                  <option value="">unchanged</option>
                  <option value="json">json</option>
                  <option value="json_schema">json_schema</option>
                </select>
              </label>
              <label data-tooltip={llmTips.reasoningEffort}>
                <span>Reasoning</span>
                <select bind:value={playReasoningEffort} disabled={!playUseOverrides} data-tooltip={llmTips.reasoningEffort}>
                  <option value="">unchanged</option>
                  <option value="low">low</option>
                  <option value="medium">medium</option>
                  <option value="high">high</option>
                </select>
              </label>
              <label data-tooltip={llmTips.presencePenalty}>
                <span>Presence</span>
                <input type="number" step="0.1" min="-2" max="2" bind:value={playPresencePenalty} placeholder="unchanged" disabled={!playUseOverrides} data-tooltip={llmTips.presencePenalty} />
              </label>
              <label data-tooltip={llmTips.frequencyPenalty}>
                <span>Frequency</span>
                <input type="number" step="0.1" min="-2" max="2" bind:value={playFrequencyPenalty} placeholder="unchanged" disabled={!playUseOverrides} data-tooltip={llmTips.frequencyPenalty} />
              </label>
              <label data-tooltip={llmTips.toolChoice}>
                <span>Tool choice</span>
                <input bind:value={playToolChoice} placeholder="auto, none, required, tool name" disabled={!playUseOverrides} data-tooltip={llmTips.toolChoice} />
              </label>
            </div>
          </div>
        {/if}

        <div class="play-messages" bind:this={playMsgListEl}>
          {#if playMessages.length === 0}
            <div class="play-empty">
              Saved changes? Send a message to test this agent.<br>
              <span class="hint">Edits aren't picked up until you click Save above.</span>
            </div>
          {:else}
            {#each playMessages as msg}
              <div class="msg-row" class:user={msg.role==='user'} class:sys={msg.role==='system'}>
                <div class="bubble">
                  {#if msg.role === 'user'}
                    <div class="btext">{msg.text}</div>
                  {:else}
                    <div class="btext markdown-body" use:richRenderer={msg.text}>{@html parseMarkdown(msg.text)}</div>
                  {/if}
                  <div class="btime">{fmtTime(msg.ts)}</div>
                </div>
              </div>
            {/each}
            {#if playSending}
              <div class="msg-row">
                <div class="bubble">
                  <div class="typing"><span/><span/><span/></div>
                </div>
              </div>
            {/if}
          {/if}
        </div>

        <div class="play-input">
          <textarea
            bind:value={playInput}
            on:keydown={playKeydown}
            placeholder="Message {selected.id}… (Enter to send)"
            rows="2"
            disabled={playSending}
          ></textarea>
          <button class="send-btn btn-primary"
                  on:click={playSend}
                  disabled={playSending || !playInput.trim()}>
            {playSending ? '…' : '↑'}
          </button>
        </div>
      </div>
    {/if}
  </div>
</div>

{#if ackModal}
  <!--
    Story 5 (Capability Exposure Safety): the server returned 409 needs_ack
    because saving would escalate this agent's tier while it is already bound
    to interactive channel mappings. Show the peeked audit and require an
    explicit Acknowledge & Save before proceeding. Cancelling leaves the
    unsaved edits in place so the operator can lower the tier instead.
  -->
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close capability acknowledgement modal"
    on:click|self={cancelCapabilityAck}
    on:keydown={(e) => e.key === 'Escape' && cancelCapabilityAck()}
  >
    <div class="modal">
      <h2>Acknowledge capability change</h2>
      <div class="modal-sub">
        This save would change the agent's capability tier from
        <strong>{ackModal.audit?.old_tier || 'unknown'}</strong> to
        <strong>{ackModal.audit?.new_tier || 'unknown'}</strong>.
        The agent is already reachable through interactive channel bindings, so
        the escalation needs an explicit acknowledgement before the write.
      </div>

      {#if ackModal.audit?.bindings?.length}
        <div class="save-audit danger" style="margin-top:.75rem;">
          <strong>Affected channel bindings</strong>
          <ul style="margin:.25rem 0 0 1rem;padding:0;">
            {#each ackModal.audit.bindings as binding}
              <li><code>{binding}</code></li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if ackModal.audit?.warnings?.length}
        <div class="save-audit danger" style="margin-top:.5rem;">
          {#each ackModal.audit.warnings as warning}
            <p style="margin:.25rem 0;">{warning}</p>
          {/each}
        </div>
      {/if}

      {#if ackModal.audit?.reasons?.length}
        <details style="margin-top:.5rem;">
          <summary>Why this tier</summary>
          <ul style="margin:.25rem 0 0 1rem;padding:0;">
            {#each ackModal.audit.reasons.slice(0, 6) as reason}
              <li>{reason}</li>
            {/each}
          </ul>
        </details>
      {/if}

      <div class="modal-row" style="display:flex;justify-content:flex-end;gap:.5rem;margin-top:1rem;">
        <button class="btn-secondary" on:click={cancelCapabilityAck} disabled={ackConfirming}>Cancel</button>
        <button class="btn-primary" on:click={confirmCapabilityAck} disabled={ackConfirming}>
          {ackConfirming ? 'Saving…' : 'Acknowledge & Save'}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if showExport && selected}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close API snippets modal"
    on:click|self={() => showExport = false}
    on:keydown={(e) => e.key === 'Escape' && (showExport = false)}
  >
    <div class="modal wide">
      <h2>API · {selected.id}</h2>
      <div class="modal-sub">
        Call this agent from your own code. The Authorization header uses the
        API key you're logged in with — anyone holding it gets the same access.
      </div>

      <div class="tab-row">
        <button class="tab" class:active={exportTab==='curl'}   on:click={() => exportTab = 'curl'}>cURL</button>
        <button class="tab" class:active={exportTab==='python'} on:click={() => exportTab = 'python'}>Python</button>
        <button class="tab" class:active={exportTab==='js'}     on:click={() => exportTab = 'js'}>JavaScript</button>
        <div style="flex:1"></div>
        <button class="btn-secondary small" on:click={copySnippet}>Copy</button>
      </div>

      <pre class="snippet">{activeSnippet}</pre>

      <div class="modal-row" style="display:flex;justify-content:flex-end;">
        <button class="btn-secondary" on:click={() => showExport = false}>Close</button>
      </div>
    </div>
  </div>
{/if}

{#if showPackageImport}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close package import modal"
    on:click|self={() => showPackageImport = false}
    on:keydown={(e) => e.key === 'Escape' && (showPackageImport = false)}
  >
    <div class="modal wide">
      <h2>Import Agent Package</h2>
      <div class="modal-sub">
        Soulacy checks the package before writing anything, including required providers, channels, peer agents, local tool files, and SOUL.yaml validity.
      </div>

      {#if packageImportLoading}
        <div class="empty-panel">Inspecting package…</div>
      {:else if packageImportError}
        <div class="banner err">⚠ {packageImportError}</div>
      {/if}

      {#if packageInspection}
        <div class="pkg-summary">
          <div>
            <div class="agent-name">{packageInspection.manifest?.name || packageInspection.agent?.name || packageInspection.agent?.id}</div>
            <div class="agent-meta">
              {packageInspection.agent?.id} · {packageInspection.agent?.trigger} · {packageInspection.agent?.llm?.provider || 'default'}/{packageInspection.agent?.llm?.model || '?'}
            </div>
          </div>
          <span class:ok={packageInspection.importable} class:bad={!packageInspection.importable}>
            {packageInspection.importable ? 'Importable' : 'Needs fixes'}
          </span>
        </div>

        {#if packageInspection.validation?.findings?.length}
          <div class="validation-list">
            {#each packageInspection.validation.findings as f}
              <div class:ferr={f.severity === 'error'} class:fwarn={f.severity !== 'error'}>
                <b>{f.severity}</b> {f.field}: {f.message}
              </div>
            {/each}
          </div>
        {/if}

        {#if packageInspection.requirements?.length}
          <h3>Requirements</h3>
          <div class="req-grid">
            {#each packageInspection.requirements as r}
              <div class="req-row">
                <span>{r.kind}</span>
                <b>{r.name}</b>
                <em class:ok={['available','configured','built_in','packaged','declared'].includes(r.status)}
                    class:warn={['verify','conflict'].includes(r.status)}
                    class:bad={r.status === 'missing'}>
                  {r.status}
                </em>
              </div>
            {/each}
          </div>
        {/if}

        {#if packageInspection.manifest?.eval_suites?.length || packageInspection.manifest?.sample_prompts?.length}
          <h3>Validation Harness</h3>
          <div class="req-grid">
            {#each packageInspection.manifest?.eval_suites || [] as f}
              <div class="req-row">
                <span>eval</span>
                <b>{f}</b>
                <em class="ok">packaged</em>
              </div>
            {/each}
            {#each packageInspection.manifest?.sample_prompts || [] as f}
              <div class="req-row">
                <span>sample</span>
                <b>{f}</b>
                <em class="ok">packaged</em>
              </div>
            {/each}
          </div>
        {/if}

        {#if packageInspection.warnings?.length}
          <h3>Warnings</h3>
          <ul class="warn-list">
            {#each packageInspection.warnings as w}
              <li>{w}</li>
            {/each}
          </ul>
        {/if}

        <div class="check-row">
          <label>
            <input type="checkbox" bind:checked={packageImportDisabled} />
            Import disabled so I can review before it runs
          </label>
          <label>
            <input type="checkbox" bind:checked={packageImportOverwrite} />
            Overwrite existing agent with the same ID
          </label>
        </div>

        <!--
          Story 7 Bucket 7A — install-time secret gate. When the v2 manifest
          declares `requires.secrets` / providers / channels / peer_agents
          and any of them isn't satisfied on this workspace, the Import
          button is disabled until the operator explicitly acknowledges.
          Cheaper than blocking silently; safer than proceeding silently.
        -->
        {#if packageMissingRequirements.length}
          <div class="banner err" style="margin-top:.5rem;">
            <strong>Missing requirements ({packageMissingRequirements.length})</strong> — this package
            declares hard requirements that aren't satisfied on this workspace:
            <ul style="margin:.25rem 0 .5rem 1rem;padding:0;">
              {#each packageMissingRequirements as r}
                <li><code>{r.kind}</code>: <strong>{r.name}</strong>
                  {#if r.description}<span style="opacity:.75;"> — {r.description}</span>{/if}
                </li>
              {/each}
            </ul>
            <label style="display:inline-flex;gap:.4rem;align-items:center;">
              <input type="checkbox" bind:checked={packageImportAcknowledgeMissing} />
              I understand — import anyway
            </label>
          </div>
        {/if}
      {/if}

      {#if packageImportMsg}
        <div class="banner ok">✓ {packageImportMsg}</div>
      {/if}

      <div class="modal-row" style="display:flex;justify-content:flex-end;gap:8px;">
        <button class="btn-secondary" on:click={() => showPackageImport = false}>Cancel</button>
        <button class="btn-primary"
                on:click={importInspectedPackage}
                disabled={!packageInspection?.importable || packageImporting || packageImportBlocked}>
          {packageImporting ? 'Importing…' : (packageImportBlocked ? 'Missing requirements' : 'Import')}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if showTemplates}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close template modal"
    on:click|self={() => { if (!instantiating) showTemplates = false }}
    on:keydown={(e) => e.key === 'Escape' && !instantiating && (showTemplates = false)}
  >
    <div class="modal">
      <h2>Start from a template</h2>
      <div class="modal-sub">
        Each template creates a working agent you can chat with immediately,
        then tweak. Drop your own templates into <code>~/.soulacy/templates/</code>
        to extend this list.
      </div>

      {#if templatesError}
        <div class="banner err">⚠ {templatesError}</div>
      {/if}

      {#if templatesLoading}
        <div class="tpl-empty">Loading templates…</div>
      {:else if templates.length === 0}
        <div class="tpl-empty">No templates available.</div>
      {:else}
        <div class="tpl-list">
          {#each templates as t (t.name)}
            <div class="tpl-card">
              <div class="tpl-body">
                <div class="tpl-title">
                  {t.display_name || t.name}
                  <span class="tpl-source-badge {t.source}">{t.source}</span>
                </div>
                {#if t.description}
                  <div class="tpl-desc">{t.description}</div>
                {/if}
                {#if t.tags?.length}
                  <div class="tpl-tags">
                    {#each t.tags as tag}<span class="tpl-tag">{tag}</span>{/each}
                  </div>
                {/if}
              </div>
              <button class="tpl-use"
                      disabled={!!instantiating}
                      on:click={() => useTemplate(t)}>
                {instantiating === t.name ? 'Creating…' : 'Use this'}
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <div class="modal-row" style="display:flex;justify-content:flex-end;gap:.5rem;">
        <button class="btn-secondary" on:click={() => showTemplates = false}
                disabled={!!instantiating}>Cancel</button>
      </div>
    </div>
  </div>
{/if}

{#if showYaml}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close YAML editor"
    on:click|self={closeYaml}
    on:keydown={(e) => e.key === 'Escape' && closeYaml()}
  >
    <div class="modal wide">
      <h2>Edit SOUL.yaml — {selected ? selected.id : ''}</h2>
      <div class="modal-sub">
        The raw agent definition. Fix syntax, template references (e.g. use
        <code>{'{{ .notebook.id }}'}</code> not <code>{'{{ .notebook }}'}</code>),
        or fields the form doesn't expose. Saving parses and validates before
        writing to disk.
        {#if yamlPath}<br><span class="hint">{yamlPath}</span>{/if}
      </div>

      {#if yamlError}
        <div class="banner err">⚠ {yamlError}</div>
      {/if}
      {#if yamlMsg}
        <div class="banner ok-banner">{yamlMsg}</div>
      {/if}

      {#if yamlLoading}
        <div class="tpl-empty">Loading…</div>
      {:else}
        <div class="yaml-tools">
          <span class="yaml-tools-label">Insert:</span>
          <select bind:value={execBackendChoice} class="yaml-select" data-tooltip="Execution backend">
            <option value="">execution backend…</option>
            <option value="local">local</option>
            <option value="docker">docker</option>
            <option value="ssh">ssh</option>
            <option value="modal">modal</option>
            <option value="runpod">runpod</option>
            <option value="daytona">daytona</option>
          </select>
          <button class="yaml-chip" type="button" on:click={applyExecBackend} disabled={!execBackendChoice}>Set backend</button>
          <button class="yaml-chip" type="button" on:click={() => appendYamlBlock(POLICY_SNIPPET)}>+ policy</button>
          <button class="yaml-chip" type="button" on:click={() => appendYamlBlock(DRYRUN_SNIPPET)}>+ dry-run</button>
        </div>
        <textarea
          class="yaml-area"
          spellcheck="false"
          bind:value={yamlText}
          on:input={() => { yamlError = ''; yamlMsg = '' }}
        ></textarea>
      {/if}

      <div class="modal-row" style="display:flex;justify-content:flex-end;gap:.5rem;">
        <button class="btn-secondary" on:click={closeYaml} disabled={yamlSaving}>Cancel</button>
        <button class="btn-primary" on:click={saveYaml} disabled={yamlSaving || yamlLoading || yamlText === yamlOrig}>
          {yamlSaving ? 'Saving…' : 'Save YAML'}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if showHistory}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close agent history"
    on:click|self={closeHistory}
    on:keydown={(e) => e.key === 'Escape' && closeHistory()}
  >
    <div class="modal wide">
      <h2>Version History — {selected ? selected.id : ''}</h2>
      <div class="modal-sub">
        Snapshots are captured automatically before updates, YAML saves, enable/disable changes, deletes, and rollbacks.
      </div>

      {#if historyError}
        <div class="banner err">⚠ {historyError}</div>
      {/if}
      {#if historyMsg}
        <div class="banner ok-banner">{historyMsg}</div>
      {/if}

      {#if historyLoading}
        <div class="tpl-empty">Loading…</div>
      {:else if historyVersions.length === 0}
        <div class="tpl-empty">No saved versions yet. Make and save a change to create the first snapshot.</div>
      {:else}
        <div class="history-grid">
          <div class="history-list">
            {#each historyVersions as version}
              <button
                class="history-item"
                class:on={historySelected?.id === version.id}
                on:click={() => selectVersion(version)}
              >
                <span>{formatVersionTime(version.created_at)}</span>
                <small>{version.bytes || 0} bytes</small>
              </button>
            {/each}
          </div>
          <textarea
            class="yaml-area history-yaml"
            spellcheck="false"
            readonly
            value={historyYaml}
          ></textarea>
        </div>
      {/if}

      <div class="modal-row" style="display:flex;justify-content:flex-end;gap:.5rem;">
        <button class="btn-secondary" on:click={closeHistory} disabled={historySaving}>Close</button>
        <button class="btn-primary" on:click={rollbackVersion} disabled={historySaving || !historySelected}>
          {historySaving ? 'Restoring…' : 'Restore Selected'}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if showDoctor}
  <!-- F-GUI-2 — Security Doctor drawer. Follows the same modal-bg + .modal.wide
       pattern as YAML/History for consistency. Renders the S7 report grouped
       into risky-combos banner + report sections + dry-run panel. -->
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close Security Doctor"
    on:click|self={closeDoctor}
    on:keydown={(e) => e.key === 'Escape' && closeDoctor()}
  >
    <div class="modal wide scrollable">
      <h2>Security Doctor — {doctorAgentId}</h2>
      <div class="modal-sub">
        Live report on this agent's trust boundary, sandboxing, and privileged surface.
        The dry-run panel below simulates the S1+S2+S3 pipeline against operator-supplied
        adversarial content — <em>no tools are actually executed</em>.
      </div>

      {#if doctorError}
        <div class="banner err">⚠ {doctorError}</div>
      {/if}

      {#if doctorLoading}
        <div class="tpl-empty">Loading…</div>
      {:else if doctorReport}
        {@const critical = (doctorReport.findings || []).filter(f => f.severity === 'critical')}
        {@const warns    = (doctorReport.findings || []).filter(f => f.severity === 'warn')}
        {@const infos    = (doctorReport.findings || []).filter(f => f.severity === 'info')}

        {#if critical.length}
          <div class="doctor-banner danger">
            <strong>Risky combination — {critical.length} critical finding{critical.length === 1 ? '' : 's'}</strong>
            {#each critical as f}
              <p><span class="doctor-cat">{f.category}</span> {f.message}{#if f.fix} · <em>fix:</em> {f.fix}{/if}</p>
            {/each}
          </div>
        {/if}
        {#if warns.length}
          <div class="doctor-banner warn">
            <strong>{warns.length} warning{warns.length === 1 ? '' : 's'}</strong>
            {#each warns as f}
              <p><span class="doctor-cat">{f.category}</span> {f.message}{#if f.fix} · <em>fix:</em> {f.fix}{/if}</p>
            {/each}
          </div>
        {/if}
        {#if infos.length}
          <div class="doctor-banner info">
            <strong>{infos.length} recommendation{infos.length === 1 ? '' : 's'}</strong>
            {#each infos as f}
              <p><span class="doctor-cat">{f.category}</span> {f.message}{#if f.fix} · <em>fix:</em> {f.fix}{/if}</p>
            {/each}
          </div>
        {/if}

        <div class="doctor-grid">
          <div class="doctor-card">
            <h3>Tier & capabilities</h3>
            <div class="doctor-kv"><span>Tier</span><strong>{doctorReport.tier || 'unknown'}</strong></div>
            {#if doctorReport.tier_reasons?.length}
              <ul>
                {#each doctorReport.tier_reasons.slice(0, 6) as r}
                  <li>{r}</li>
                {/each}
              </ul>
            {/if}
            {#if doctorReport.capabilities?.length}
              <div class="doctor-kv"><span>Capabilities</span><code>{doctorReport.capabilities.join(', ')}</code></div>
            {/if}
            <div class="doctor-kv"><span>Sandbox</span><code>{doctorReport.sandbox_backend || 'unknown'}</code></div>
            <div class="doctor-kv"><span>Unattended</span><code>{fmtBool(doctorReport.unattended)}</code></div>
            <div class="doctor-kv"><span>Intent gate</span><code>{doctorReport.intent_gate_mode || 'prompt (default)'}</code></div>
          </div>

          <div class="doctor-card">
            <h3>Tools ({(doctorReport.tools || []).length})</h3>
            {#if (doctorReport.tools || []).length}
              <ul class="doctor-tool-list">
                {#each doctorReport.tools as t}
                  <li>
                    <code>{t.name}</code>
                    <span class="doctor-pill {t.trust === 'untrusted' ? 'warn' : ''}">{t.trust}</span>
                    <span class="doctor-pill info">{t.category}</span>
                    {#if t.high_risk}<span class="doctor-pill danger">high-risk</span>{/if}
                    {#if t.confirm}<span class="doctor-pill info">confirm</span>{/if}
                  </li>
                {/each}
              </ul>
            {:else}
              <p class="doctor-empty">No tools declared.</p>
            {/if}
          </div>

          <div class="doctor-card">
            <h3>Channels</h3>
            {#if (doctorReport.channels || []).length}
              <ul class="doctor-tool-list">
                {#each doctorReport.channels as ch}
                  <li>
                    <code>{ch.name}</code>
                    {#if ch.shared}<span class="doctor-pill warn">shared</span>{/if}
                    {#if ch.accepted}<span class="doctor-pill info">exposure acked</span>{:else if ch.shared}<span class="doctor-pill danger">no ack</span>{/if}
                  </li>
                {/each}
              </ul>
            {:else}
              <p class="doctor-empty">No channel bindings.</p>
            {/if}
          </div>

          <div class="doctor-card">
            <h3>Policy & confirms</h3>
            <div class="doctor-kv"><span>Policy enabled</span><code>{fmtBool(doctorReport.policy_enabled)}</code></div>
            {#if doctorReport.policy_rules?.length}
              <ul>
                {#each doctorReport.policy_rules as r}<li><code>{r}</code></li>{/each}
              </ul>
            {/if}
            {#if doctorReport.confirm_tools?.length}
              <div class="doctor-kv"><span>Confirm</span><code>{doctorReport.confirm_tools.join(', ')}</code></div>
            {/if}
            {#if doctorReport.env_vars?.length}
              <div class="doctor-kv"><span>Env vars</span><code>{doctorReport.env_vars.slice(0, 8).join(', ')}{doctorReport.env_vars.length > 8 ? ` (+${doctorReport.env_vars.length - 8})` : ''}</code></div>
            {/if}
            {#if doctorReport.mcp_servers?.length}
              <div class="doctor-kv"><span>MCP servers</span><code>{doctorReport.mcp_servers.join(', ')}</code></div>
            {/if}
          </div>
        </div>

        <div class="doctor-card doctor-dryrun">
          <h3>Dry-run adversarial content</h3>
          <p class="doctor-hint">
            Feed simulated attack content through the S1 trust + S2 injection scanner + S3 intent gate,
            without touching the actual tool. Useful for exercising your intent-gate policy against a
            realistic prompt-injection sample.
          </p>
          <div class="doctor-dr-fields">
            <label>
              <span>User goal (optional)</span>
              <input type="text" bind:value={doctorInput.user_goal}
                     placeholder='e.g. "Summarize this URL"' />
            </label>
            <label>
              <span>Injection source</span>
              <input type="text" bind:value={doctorInput.injection_source}
                     placeholder='fetch_url | kb_search | mcp__server__tool' />
            </label>
            <label>
              <span>Follow-up tool</span>
              <input type="text" bind:value={doctorInput.followup_tool}
                     placeholder='shell_exec | write_file | http_request' />
            </label>
            <label class="doctor-dr-wide">
              <span>Follow-up args (JSON)</span>
              <input type="text" bind:value={doctorInput.followup_args}
                     placeholder={'{"path": "/etc/passwd"}'} />
            </label>
            <label class="doctor-dr-wide">
              <span>Injected content</span>
              <textarea rows="4" bind:value={doctorInput.injected_content}
                        placeholder='Paste the untrusted sample the tool would return.'></textarea>
            </label>
          </div>
          {#if doctorRunError}
            <div class="banner err">⚠ {doctorRunError}</div>
          {/if}
          <div class="doctor-dr-actions">
            <button class="btn-primary" on:click={runDoctorDryRun} disabled={doctorRunning}>
              {doctorRunning ? 'Simulating…' : 'Run simulation'}
            </button>
          </div>
          {#if doctorResult}
            <div class="doctor-result" bind:this={doctorResultEl}>
              <div class="doctor-result-row">
                <span class="doctor-pill {findingClass(doctorResult.injection_severity)}">injection · {doctorResult.injection_severity || 'none'}</span>
                <span class="doctor-pill {verdictClass(doctorResult.intent_decision)}">intent · {doctorResult.intent_decision || '?'}</span>
                <span class="doctor-pill info">goal matched · {fmtBool(doctorResult.goal_matched)}</span>
                <span class="doctor-pill {doctorResult.injection_influenced ? 'warn' : 'info'}">injection influenced · {fmtBool(doctorResult.injection_influenced)}</span>
              </div>
              <p class="doctor-verdict">{doctorResult.verdict || ''}</p>
              {#if doctorResult.intent_reason}
                <p class="doctor-reason"><em>Intent reason:</em> {doctorResult.intent_reason}</p>
              {/if}
              {#if (doctorResult.injection_findings || []).length}
                <details>
                  <summary>{doctorResult.injection_findings.length} injection finding(s)</summary>
                  <ul class="doctor-tool-list">
                    {#each doctorResult.injection_findings as f}
                      <li>
                        <span class="doctor-pill {findingClass(f.severity)}">{f.severity}</span>
                        <code>{f.family || '?'}</code>
                        {#if f.pattern}<span class="doctor-pattern">{f.pattern}</span>{/if}
                        {#if f.snippet}<div class="doctor-snippet">{f.snippet}</div>{/if}
                      </li>
                    {/each}
                  </ul>
                </details>
              {/if}
            </div>
          {/if}
        </div>
      {/if}

      <div class="modal-row" style="display:flex;justify-content:flex-end;gap:.5rem;">
        <button class="btn-secondary" on:click={closeDoctor} disabled={doctorRunning}>Close</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; height: 100%; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }

  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok-banner { background: rgba(76,175,130,.12); border: 1px solid rgba(76,175,130,.35); color: #4caf82; }

  .yaml-tools { display: flex; align-items: center; flex-wrap: wrap; gap: .4rem; margin-bottom: .5rem; }
  .yaml-tools-label { font-size: .72rem; color: #6b7294; font-weight: 600; }
  .yaml-select { background: #0e1020; color: #d7dcf5; border: 1px solid #1a1e36; border-radius: 6px; padding: .25rem .4rem; font-size: .74rem; }
  .yaml-chip { background: #171a2e; color: #9aa0c8; border: 1px solid #232847; border-radius: 6px; padding: .25rem .55rem; font-size: .74rem; cursor: pointer; }
  .yaml-chip:hover:not(:disabled) { color: #c5c9e8; border-color: #3a406a; }
  .yaml-chip:disabled { opacity: .5; cursor: default; }
  .yaml-area {
    width: 100%; min-height: 420px; resize: vertical;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: .8rem; line-height: 1.5; tab-size: 2;
    background: #0e1020; color: #d7dcf5;
    border: 1px solid #1a1e36; border-radius: 8px; padding: .75rem .85rem;
    white-space: pre; overflow: auto;
  }
  .history-grid {
    display: grid; grid-template-columns: minmax(220px, 280px) 1fr;
    gap: .75rem; min-height: 420px;
  }
  .history-list {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .45rem; display: flex; flex-direction: column; gap: .35rem; overflow-y: auto;
  }
  .history-item {
    width: 100%; text-align: left; border: 1px solid transparent; border-radius: 6px;
    background: transparent; color: #c8cadf; padding: .55rem .6rem;
    display: flex; flex-direction: column; gap: .2rem;
  }
  .history-item:hover, .history-item.on { border-color: #6c63ff; background: rgba(108,99,255,.12); }
  .history-item small { color: #6b7294; font-size: .7rem; }
  .history-yaml { min-height: 420px; resize: none; }

  .split    { display: flex; gap: 1rem; flex: 1; min-height: 0; }

  @media (max-width: 900px) {
    /* Stack list / editor / playground vertically on tablet & mobile */
    .split    { flex-direction: column; overflow-y: auto; }
    .list-col { width: 100%; max-height: 220px; flex-shrink: 0; }
    .play-col { width: 100%; flex-shrink: 0; }
  }
  @media (max-width: 640px) {
    .row-2, .row-3 { grid-template-columns: 1fr; }
    .param-grid    { grid-template-columns: 1fr; }
    .history-grid  { grid-template-columns: 1fr; }
  }

  /* List */
  .list-col { width: 250px; flex-shrink: 0; overflow-y: auto; display: flex; flex-direction: column; gap: .45rem; }
  .agent-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .7rem .85rem; cursor: pointer;
    display: flex; align-items: center; justify-content: space-between;
    transition: border-color .12s;
  }
  .agent-card:hover, .agent-card.active { border-color: #6c63ff; }
  .agent-name  { font-weight: 500; font-size: .875rem; }
  .agent-meta  { color: #6b7294; font-size: .72rem; margin-top: .15rem; }
  .tier-pill {
    display: inline-flex; align-items: center; width: fit-content;
    margin-top: .4rem; padding: .12rem .45rem; border-radius: 999px;
    border: 1px solid #303656; background: #101426; color: #9aa2c9;
    font-size: .64rem; font-weight: 700; letter-spacing: .04em;
    text-transform: uppercase;
  }
  .tier-pill.read { border-color: rgba(118,224,162,.35); color: #76e0a2; background: rgba(76,175,130,.08); }
  .tier-pill.active-tier { border-color: rgba(139,133,255,.42); color: #aaa6ff; background: rgba(108,99,255,.1); }
  .tier-pill.privileged { border-color: rgba(255,128,128,.45); color: #ff9b9b; background: rgba(255,90,90,.1); }
  .toggle      { background: none; font-size: 1rem; color: #6b7294; padding: .15rem; }
  .toggle.on   { color: #4caf82; }
  .empty       { color: #6b7294; text-align: center; padding: 2rem .5rem; font-size: .875rem; line-height: 1.6; }
  .empty-onboard {
    text-align: left;
    padding: .9rem;
    border: 1px solid #24294a;
    border-radius: 8px;
    background: #141626;
    color: #9ca3c8;
  }
  .empty-onboard p {
    margin: .35rem 0 .75rem;
    font-size: .78rem;
    line-height: 1.45;
  }
  .empty-kicker {
    color: #d7dcf5;
    font-weight: 700;
    font-size: .86rem;
  }
  .empty-primary,
  .empty-secondary {
    width: 100%;
    display: block;
    border-radius: 6px;
    padding: .5rem .65rem;
    margin-top: .4rem;
    font-size: .78rem;
    font-weight: 700;
    cursor: pointer;
  }
  .empty-primary {
    background: #6c63ff;
    color: white;
    border: 1px solid #8178ff;
  }
  .empty-secondary {
    background: #171a2e;
    color: #c8cadf;
    border: 1px solid #2a2f4a;
  }
  .empty-primary:hover,
  .empty-secondary:hover {
    border-color: #8b85ff;
  }

  /* Editor */
  .editor-col  { flex: 1; overflow-y: auto; min-width: 0; }
  .empty-panel { display: flex; align-items: center; justify-content: center; height: 200px; color: #6b7294; }

  .editor      { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; }
  .editor-hdr  {
    display: flex; align-items: center; justify-content: space-between;
    padding: .8rem 1rem; border-bottom: 1px solid #1a1e36;
    font-weight: 600; font-size: .875rem; flex-shrink: 0;
  }
  .hdr-actions { display: flex; align-items: center; gap: .65rem; }
  .save-msg    { font-size: .8rem; color: #f06060; }
  .save-msg.ok { color: #4caf82; }

  .fields  { padding: 1rem; display: flex; flex-direction: column; gap: .8rem; }
  .field   { display: flex; flex-direction: column; gap: .3rem; }
  .field-label {
    font-size: .72rem; color: #6b7294; text-transform: uppercase;
    letter-spacing: .06em; font-weight: 600;
  }
  .field-label-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: .55rem;
  }
  .tier-panel {
    border-radius: 8px; padding: .8rem; display: flex; flex-direction: column; gap: .45rem;
    background: #0e1020; border: 1px solid #2a2f4a; color: #c8cadf;
  }
  .tier-panel.read { border-color: rgba(76,175,130,.26); background: rgba(76,175,130,.05); }
  .tier-panel.active-tier { border-color: rgba(108,99,255,.32); background: rgba(108,99,255,.06); }
  .tier-panel.privileged { border-color: rgba(255,90,90,.34); background: rgba(255,90,90,.07); }
  .tier-panel-head { display: flex; align-items: center; gap: .55rem; font-size: .84rem; }
  .tier-panel .tier-pill { margin-top: 0; }
  .tier-panel p { margin: 0; color: #9ca3c8; font-size: .78rem; line-height: 1.45; }
  .tier-panel ul { margin: .1rem 0 0 1rem; padding: 0; color: #aeb4d5; font-size: .76rem; line-height: 1.45; }
  .tier-muted { color: #737a9c !important; }
  .save-audit {
    border-radius: 8px; padding: .8rem; display: flex; flex-direction: column; gap: .35rem;
    background: rgba(240,192,96,.08); border: 1px solid rgba(240,192,96,.36); color: #f2d18a;
    font-size: .8rem; line-height: 1.45;
  }
  .save-audit.danger { background: rgba(240,96,96,.08); border-color: rgba(240,96,96,.38); color: #f1a0a0; }
  .save-audit p { margin: 0; color: inherit; }
  .validation-panel {
    border-radius: 8px; padding: .8rem; display: flex; flex-direction: column; gap: .6rem;
    background: #0e1020; border: 1px solid #2a2f4a;
  }
  .validation-panel.ok   { border-color: rgba(76,175,130,.35); background: rgba(76,175,130,.07); }
  .validation-panel.warn { border-color: rgba(240,192,96,.35); background: rgba(240,192,96,.07); }
  .validation-panel.fail { border-color: rgba(240,96,96,.35); background: rgba(240,96,96,.07); }
  .validation-head { display: flex; align-items: center; justify-content: space-between; font-size: .8rem; color: #c8cadf; font-weight: 600; }
  .validation-head span:last-child { color: #6b7294; font-weight: 500; }
  .validation-empty { color: #4caf82; font-size: .8rem; }
  .validation-list { display: flex; flex-direction: column; gap: .55rem; }
  .validation-item { border-top: 1px solid rgba(255,255,255,.06); padding-top: .55rem; }
  .validation-item:first-child { border-top: none; padding-top: 0; }
  .validation-line { display: flex; gap: .45rem; align-items: baseline; color: #c8cadf; font-size: .8rem; line-height: 1.45; }
  .validation-line code { font-size: .75rem; color: #8b85ff; background: #1a1e36; padding: .08rem .28rem; border-radius: 4px; }
  .severity { color: #f0c060; text-transform: uppercase; font-size: .66rem; font-weight: 700; min-width: 42px; }
  .validation-item.error .severity { color: #f06060; }
  .validation-suggestion { margin-left: 48px; margin-top: .25rem; color: #8a90b8; font-size: .76rem; line-height: 1.45; }
  .validation-alts { margin-left: 48px; margin-top: .35rem; display: flex; flex-wrap: wrap; gap: .3rem; }
  .validation-alts span { font-family: monospace; font-size: .68rem; color: #b0b5d8; background: #1a1e36; padding: .12rem .4rem; border-radius: 4px; }
  label    { font-size: .72rem; color: #6b7294; text-transform: uppercase; letter-spacing: .05em; }
  .mloading { color: #8b85ff; text-transform: none; font-weight: 500; margin-left: .35rem; }
  .merror   { color: #f0c060; text-transform: none; font-weight: 500; margin-left: .35rem; cursor: help; }
  .mhint    { color: #4caf82; text-transform: none; font-weight: 500; margin-left: .35rem; }
  .mini-link {
    background: transparent;
    border: 0;
    color: #8b85ff;
    cursor: pointer;
    font-size: .68rem;
    font-weight: 700;
    letter-spacing: 0;
    padding: 0;
    text-transform: none;
  }
  .mini-link:hover { color: #b7b3ff; text-decoration: underline; }
  .model-status {
    border: 1px solid #252b49;
    border-radius: 6px;
    padding: .42rem .52rem;
    background: #101324;
    color: #8f96ba;
    display: flex;
    flex-direction: column;
    gap: .18rem;
    line-height: 1.35;
  }
  .model-status b {
    color: #cfd3ee;
    font-size: .72rem;
    text-transform: none;
    letter-spacing: 0;
  }
  .model-status span {
    font-size: .72rem;
    color: #8f96ba;
  }
  .model-status.ok { border-color: rgba(76,175,130,.28); background: rgba(76,175,130,.06); }
  .model-status.ok b { color: #76e0a2; }
  .model-status.warn { border-color: rgba(240,192,96,.3); background: rgba(240,192,96,.07); }
  .model-status.warn b { color: #f0c060; }
  .model-status.info { border-color: rgba(139,133,255,.25); background: rgba(139,133,255,.07); }
  .model-status.info b { color: #aaa6ff; }
  .optional { color: #555a7a; text-transform: none; font-weight: 400; font-size: .68rem; letter-spacing: 0; margin-left: .25rem; }
  .req     { color: #f06060; }
  .row-2   { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }
  .row-3   { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: .75rem; }
  .sep {
    font-size: .7rem; color: #6b7294; text-transform: uppercase; letter-spacing: .08em;
    border-bottom: 1px solid #1a1e36; padding-bottom: .3rem; margin-top: .25rem;
    display: flex; align-items: center; justify-content: space-between;
  }
  .add-btn {
    background: rgba(108,99,255,.12); color: #8b85ff; border: 1px solid rgba(108,99,255,.35);
    padding: .25rem .6rem; border-radius: 6px; font-size: .72rem; font-weight: 600;
    text-transform: none; letter-spacing: 0;
  }
  .add-btn:hover { background: rgba(108,99,255,.2); }

  /* Radio rows for tri-state fields (e.g. builtins: default/none/restricted) */
  .radio-row { display: flex; flex-wrap: wrap; gap: .9rem; padding: .15rem 0 .3rem; }
  .radio-opt {
    display: inline-flex; align-items: center; gap: .35rem;
    font-size: .82rem; color: #c8cadf; cursor: pointer; user-select: none;
  }
  .radio-opt input[type="radio"] { width: auto; margin: 0; cursor: pointer; }
  .peer-toggle {
    background: rgba(45, 212, 191, .05); border: 1px solid rgba(45, 212, 191, .18);
    border-radius: 6px; padding: .5rem .6rem; text-transform: none; letter-spacing: 0;
  }
  .safety-card {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .8rem; display: flex; flex-direction: column; gap: .6rem;
  }
  .safety-actions { display: flex; flex-wrap: wrap; gap: .4rem; }
  .safety-toggle {
    background: rgba(108,99,255,.05); border: 1px solid rgba(108,99,255,.16);
    border-radius: 6px; padding: .5rem .6rem; text-transform: none; letter-spacing: 0;
  }
  .safety-warn {
    display: flex; flex-wrap: wrap; align-items: center; gap: .35rem;
    background: rgba(240,192,96,.08); border: 1px solid rgba(240,192,96,.28);
    color: #d8c28a; border-radius: 6px; padding: .55rem .65rem;
    font-size: .76rem; line-height: 1.45;
  }
  .safety-warn code {
    color: #f0c060; background: rgba(240,192,96,.12);
    border-radius: 4px; padding: .08rem .3rem; font-size: .7rem;
  }
  .tools-empty {
    background: #0e1020; border: 1px dashed #2a2f4a; border-radius: 8px;
    padding: .8rem 1rem; color: #6b7294; font-size: .8rem; line-height: 1.55;
  }
  .tools-empty em { color: #8b85ff; font-style: normal; }
  .tool-card {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .8rem; display: flex; flex-direction: column; gap: .55rem;
  }
  .tool-hdr { display: flex; align-items: center; gap: .5rem; }
  .tool-grid {
    display: grid;
    grid-template-columns: minmax(180px, 1fr) minmax(90px, .45fr) minmax(130px, .6fr);
    gap: .6rem;
  }
  @media (max-width: 900px) {
    .tool-grid { grid-template-columns: 1fr; }
  }
  .tool-idx { color: #555a7a; font-size: .72rem; font-weight: 600; }
  .tool-name-input { flex: 1; font-family: monospace; font-size: .85rem; }
  .tool-ops { display: flex; gap: .25rem; }
  .tool-ops button {
    background: #1c1f35; color: #c8cadf; border: 1px solid #2a2f4a;
    width: 26px; height: 26px; border-radius: 5px; font-size: .8rem;
  }
  .tool-ops button:hover:not(:disabled) { background: #252840; }
  .tool-ops .rm { color: #f06060; border-color: rgba(240,96,96,.3); }
  .tool-ops .rm:hover { background: rgba(240,96,96,.12); }

  .catalog-hint {
    font-size: .72rem; color: #6b7294; padding: .5rem .6rem;
    background: rgba(76,175,130,.06); border: 1px solid rgba(76,175,130,.18);
    border-radius: 6px; display: flex; flex-wrap: wrap; align-items: center; gap: .4rem;
  }
  .catalog-hint strong { color: #4caf82; font-weight: 600; }
  .chip {
    font-family: monospace; font-size: .68rem; color: #b0b5d8;
    background: #1a1e36; padding: .1rem .4rem; border-radius: 4px;
  }
  .more { color: #555a7a; font-size: .72rem; }

  /* ── Inline playground ─────────────────────────────────────────────── */
  .play-col {
    width: 360px; flex-shrink: 0;
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; min-height: 0; overflow: hidden;
  }
  .play-hdr {
    display: flex; align-items: center; justify-content: space-between;
    padding: .6rem .85rem; border-bottom: 1px solid #1a1e36; flex-shrink: 0;
    font-size: .82rem; color: #c8cadf;
  }
  .play-hdr-actions { display: flex; gap: .35rem; align-items: center; }
  .play-hdr code { font-family: monospace; color: #4caf82; }
  .play-params {
    border-bottom: 1px solid #1a1e36; padding: .65rem .75rem;
    display: flex; flex-direction: column; gap: .55rem; background: #101323;
  }
  .check-row { display: flex; align-items: center; gap: .45rem; font-size: .75rem; color: #c8cadf; }
  .param-actions { display: flex; justify-content: flex-end; }
  .param-grid { display: grid; grid-template-columns: 1fr 1fr; gap: .5rem; }
  .param-grid label { display: flex; flex-direction: column; gap: .25rem; min-width: 0; }
  .param-grid span { font-size: .68rem; color: #7b82a8; }
  .param-grid input, .param-grid select {
    width: 100%; min-width: 0; background: #1c1f35; color: #e8eaf6;
    border: 1px solid #2a2f4a; border-radius: 5px; padding: .38rem .45rem; font-size: .75rem;
  }
  .param-grid input:disabled, .param-grid select:disabled { opacity: .55; }
  .play-messages {
    flex: 1; overflow-y: auto; padding: .85rem;
    display: flex; flex-direction: column; gap: .65rem;
  }
  .play-empty {
    flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
    color: #6b7294; text-align: center; line-height: 1.6; font-size: .82rem;
  }
  .play-empty .hint { color: #555a7a; font-size: .72rem; margin-top: .35rem; }
  .play-input {
    display: flex; gap: .55rem; align-items: flex-end;
    padding: .55rem; border-top: 1px solid #1a1e36; flex-shrink: 0;
  }
  .play-input textarea {
    flex: 1; resize: none; font-size: .85rem;
    background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a;
    border-radius: 5px; padding: .45rem .55rem;
  }
  .send-btn {
    height: 38px; min-width: 44px; padding: 0; font-size: 1rem;
    align-self: flex-end; flex-shrink: 0; border-radius: 6px;
  }

  .msg-row       { display: flex; justify-content: flex-start; }
  .msg-row.user  { justify-content: flex-end; }
  .msg-row.sys   { justify-content: center; }
  .bubble {
    max-width: 80%; padding: .55rem .8rem; border-radius: 10px;
    display: flex; flex-direction: column; gap: .25rem;
    background: #1c1f35; border: 1px solid #2a2f4a;
    border-bottom-left-radius: 3px;
  }
  .msg-row.user .bubble {
    background: #5b52ef; border-color: transparent; color: #fff;
    border-bottom-left-radius: 10px; border-bottom-right-radius: 3px;
  }
  .msg-row.sys .bubble {
    background: rgba(240,96,96,.1); border-color: rgba(240,96,96,.3); color: #f06060;
  }
  .btext { font-size: .85rem; white-space: pre-wrap; word-break: break-word; line-height: 1.45; }
  .btime { font-size: .65rem; opacity: .55; align-self: flex-end; }

  .typing { display: flex; gap: 4px; align-items: center; height: 1.1rem; }
  .typing span {
    width: 5px; height: 5px; border-radius: 50%;
    background: #6b7294; animation: bounce 1.1s infinite;
  }
  .typing span:nth-child(2) { animation-delay: .18s; }
  .typing span:nth-child(3) { animation-delay: .36s; }
  @keyframes bounce {
    0%, 80%, 100% { transform: scale(.65); opacity: .4; }
    40%           { transform: scale(1);   opacity: 1;   }
  }

  /* Small variant for header buttons */
  :global(.btn-secondary.small) { padding: .25rem .55rem; font-size: .72rem; }
  /* "On" state for the toggle so users can tell at a glance that the
     playground is open. Picks up the global btn-secondary style and tints it. */
  :global(.btn-secondary.on) { background: #4caf82; color: #0a0d1a; border-color: transparent; }
  :global(.btn-secondary.on:hover:not(:disabled)) { background: #5ec092; }

  /* ── API export modal ──────────────────────────────────────────────── */
  .modal.wide { width: 720px; }
  .tab-row {
    display: flex; gap: .35rem; align-items: center;
    border-bottom: 1px solid #2a2f4a; padding-bottom: .4rem;
  }
  .tab {
    background: transparent; color: #8a90b8; border: none;
    padding: .35rem .7rem; font-size: .78rem; cursor: pointer;
    border-radius: 5px 5px 0 0; border-bottom: 2px solid transparent;
  }
  .tab:hover  { color: #c8cadf; }
  .tab.active { color: #4caf82; border-bottom-color: #4caf82; }
  .snippet {
    background: #0a0d1a; border: 1px solid #2a2f4a; border-radius: 6px;
    padding: .85rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: .76rem; color: #c8cadf; white-space: pre-wrap; word-break: break-all;
    max-height: 360px; overflow-y: auto; line-height: 1.5;
  }

  /* ── Templates modal ───────────────────────────────────────────────── */
  .modal-bg {
    position: fixed; inset: 0; background: rgba(5,7,18,.6);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 680px; max-width: 92vw; max-height: 86vh;
    display: flex; flex-direction: column; gap: .9rem; overflow: hidden;
  }
  /* Modals with variable-height content (Security Doctor, others that opt in)
     need the modal itself to scroll when content overflows max-height. Kept
     as an opt-in modifier so existing modals whose inner regions manage their
     own scroll (Templates .tpl-list, editor split panes, etc.) don't change. */
  .modal.scrollable { overflow-y: auto; }
  .modal h2 { font-size: 1.05rem; font-weight: 600; margin-bottom: .15rem; }
  .modal-sub { font-size: .78rem; color: #8a90b8; margin-top: -.2rem; }
  .tpl-list { display: flex; flex-direction: column; gap: .55rem; overflow-y: auto; padding-right: .25rem; }
  .tpl-card {
    background: #181b30; border: 1px solid #2a2f4a; border-radius: 8px;
    padding: .85rem .95rem; display: flex; gap: .9rem; align-items: flex-start;
    transition: border-color .12s, background .12s;
  }
  .tpl-card:hover { border-color: #4caf82; background: #1c2138; }
  .tpl-body { flex: 1; min-width: 0; }
  .tpl-title {
    font-weight: 600; color: #e8ebf8; font-size: .92rem;
    display: flex; align-items: center; gap: .5rem;
  }
  .tpl-source-badge {
    font-size: .62rem; padding: .08rem .35rem; border-radius: 3px;
    text-transform: uppercase; letter-spacing: .04em; font-weight: 600;
  }
  .tpl-source-badge.embedded { background: rgba(76,175,130,.18); color: #4caf82; }
  .tpl-source-badge.user     { background: rgba(110,150,255,.18); color: #8aa6ff; }
  .tpl-desc {
    font-size: .78rem; color: #a0a6cc; margin-top: .25rem;
    line-height: 1.4; white-space: pre-wrap;
  }
  .tpl-tags { margin-top: .35rem; display: flex; gap: .3rem; flex-wrap: wrap; }
  .tpl-tag {
    font-family: monospace; font-size: .65rem; color: #b0b5d8;
    background: #1a1e36; padding: .05rem .35rem; border-radius: 3px;
  }
  .tpl-use {
    align-self: center; padding: .45rem .9rem; font-size: .8rem;
    background: #4caf82; color: #0a0d1a; border: none; border-radius: 5px;
    font-weight: 600; cursor: pointer; white-space: nowrap;
  }
  .tpl-use:disabled { opacity: .55; cursor: wait; }
  .tpl-empty { color: #6b7294; font-size: .8rem; text-align: center; padding: 2rem 0; }

  /* ── Reasoning strategy picker ── */
  .strategy-cards { display: flex; gap: .55rem; flex-wrap: wrap; }
  .strategy-card {
    flex: 1; min-width: 130px; max-width: 200px;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 9px;
    padding: .75rem .9rem; cursor: pointer; display: flex; flex-direction: column;
    gap: .2rem; text-align: left; transition: border-color .15s, background .15s;
  }
  .strategy-card:hover { border-color: #6c63ff44; background: #141626; }
  .strategy-card.active { border-color: #6c63ff; background: rgba(108,99,255,.1); }
  .sc-icon  { font-size: 1.2rem; }
  .sc-title { font-size: .83rem; font-weight: 600; color: #c5c9e8; }
  .sc-desc  { font-size: .72rem; color: #6b7294; line-height: 1.4; }
  .strategy-advice {
    margin-top: .45rem; padding: .5rem .65rem; border-radius: 6px;
    border: 1px solid #1f2743; background: #101528; color: #8f96bb;
    font-size: .74rem; line-height: 1.4;
  }
  .strategy-advice.info { border-color: #2e4578; color: #9db8ff; background: rgba(59, 95, 180, .1); }
  .strategy-advice.ok { border-color: #245d46; color: #8de3b0; background: rgba(37, 145, 95, .1); }
  .field-row { display: flex; gap: .6rem; flex-wrap: wrap; }
  .field-row .field { flex: 1; min-width: 120px; }

  /* ── Brain memory cards ── */
  .brain-mem-grid { display: flex; gap: .6rem; flex-wrap: wrap; }
  .bm-card {
    flex: 1; min-width: 160px; background: #0e1020; border: 1px solid #1a1e36;
    border-radius: 9px; padding: .75rem .9rem; display: flex; flex-direction: column; gap: .4rem;
    transition: border-color .15s;
  }
  .bm-card.enabled { border-color: #6c63ff44; background: rgba(108,99,255,.04); }
  .bm-header { display: flex; align-items: center; gap: .45rem; }
  .bm-icon  { font-size: 1rem; }
  .bm-label { font-size: .82rem; font-weight: 600; color: #c5c9e8; flex: 1; }
  .bm-desc  { font-size: .73rem; color: #6b7294; line-height: 1.4; }
  .bm-fields { display: flex; flex-direction: column; gap: .4rem; margin-top: .25rem; }
  .bm-field { display: flex; align-items: center; gap: .5rem; }
  .bm-field-lbl { font-size: .7rem; color: #6b7294; white-space: nowrap; }
  .bm-num { width: 64px; padding: .25rem .4rem; font-size: .8rem; }
  .bm-check { display: flex; align-items: center; gap: .4rem; font-size: .73rem; color: #9da3c0; cursor: pointer; }
  .bm-check input { cursor: pointer; }
  .learning-card {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 9px;
    padding: .75rem .9rem; display: flex; flex-direction: column; gap: .45rem;
  }
  .learning-card.enabled { border-color: #4caf8244; background: rgba(76,175,130,.04); }
  .learning-fields { display: grid; grid-template-columns: repeat(2, minmax(140px, 1fr)); gap: .6rem; margin-top: .2rem; }
  .webhook-card {
    background: #0e1020;
    border: 1px solid #1a1e36;
    border-radius: 8px;
    padding: .8rem .9rem;
    display: flex;
    flex-direction: column;
    gap: .7rem;
  }
  .webhook-endpoint {
    display: flex;
    align-items: center;
    gap: .5rem;
    min-width: 0;
    color: #aeb4d5;
    font-size: .78rem;
  }
  .webhook-endpoint code {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: #d7dbff;
  }
  .webhook-method {
    flex: 0 0 auto;
    border: 1px solid #4caf8244;
    background: rgba(76,175,130,.08);
    color: #8ee0b6;
    border-radius: 5px;
    padding: .14rem .34rem;
    font-size: .68rem;
    font-weight: 700;
  }
  .webhook-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(160px, 1fr));
    gap: .65rem;
  }
  .webhook-check {
    display: flex;
    align-items: center;
    gap: .45rem;
    color: #aeb4d5;
    font-size: .78rem;
    min-height: 36px;
  }
  .webhook-check input { cursor: pointer; }

  /* ── Small toggle ── */
  .toggle-sm { position: relative; display: inline-flex; align-items: center; cursor: pointer; }
  .toggle-sm input { opacity: 0; width: 0; height: 0; position: absolute; }
  .toggle-track-sm {
    width: 28px; height: 15px; background: #1a1e36; border-radius: 999px;
    transition: background .2s; border: 1px solid #2a2f4a;
  }
  .toggle-sm input:checked ~ .toggle-track-sm { background: #6c63ff; border-color: #6c63ff; }
  .toggle-track-sm::after {
    content: ''; position: absolute; top: 2px; left: 3px;
    width: 11px; height: 11px; border-radius: 50%; background: #fff;
    transition: transform .2s; transform: translateX(0);
  }
  .toggle-sm input:checked ~ .toggle-track-sm::after { transform: translateX(13px); }

  /* ── Persona blocks (Identity / Personality / Non-Negotiables) ───────── */
  .sep-hint {
    font-weight: 400;
    color: #8b90a8;
    font-size: .78rem;
    margin-left: .4rem;
    letter-spacing: 0;
    text-transform: none;
  }
  .persona-block {
    margin: .55rem 0;
    background: rgba(108, 99, 255, 0.04);
    border: 1px solid rgba(108, 99, 255, 0.18);
    border-radius: 8px;
    overflow: hidden;
  }
  .persona-block > summary {
    cursor: pointer;
    list-style: none;
    padding: .55rem .85rem;
    display: flex;
    flex-direction: column;
    gap: .15rem;
    user-select: none;
  }
  .persona-block > summary::-webkit-details-marker { display: none; }
  .persona-block > summary::before {
    content: '▸';
    display: inline-block;
    margin-right: .4rem;
    color: #8b90a8;
    transition: transform .2s ease;
  }
  .persona-block[open] > summary::before { transform: rotate(90deg); }
  .persona-title {
    font-weight: 600;
    color: #e8eaf2;
    font-size: .88rem;
  }
  .persona-sub {
    color: #8b90a8;
    font-size: .76rem;
    line-height: 1.45;
    padding-left: 1rem;
  }
  .persona-tag {
    display: inline-block;
    background: rgba(255, 90, 90, 0.18);
    color: #ff8a8a;
    font-size: .65rem;
    padding: .08rem .42rem;
    border-radius: 4px;
    margin-left: .5rem;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  .persona-rules {
    background: rgba(255, 90, 90, 0.04);
    border-color: rgba(255, 90, 90, 0.22);
  }
  .persona-body {
    padding: .35rem .85rem .85rem .85rem;
    border-top: 1px solid rgba(108, 99, 255, 0.12);
  }
  .persona-rules .persona-body { border-top-color: rgba(255, 90, 90, 0.16); }
  .field-hint {
    display: block;
    color: #8b90a8;
    font-size: .72rem;
    font-weight: 400;
    margin-top: .12rem;
    line-height: 1.4;
  }
  .field-optional {
    color: #6c728c;
    font-weight: 400;
    font-style: italic;
    font-size: .72rem;
  }
  .persona-must-label { color: #6dd09a; }
  .persona-mustnot-label { color: #ff8a8a; }
  .field-row3 {
    display: flex;
    gap: .65rem;
    margin-top: .35rem;
  }
  .field-row3 .field { flex: 1; min-width: 0; }
  .persona-note {
    margin-top: .65rem;
    padding: .5rem .75rem;
    background: rgba(255, 200, 100, 0.06);
    border-left: 3px solid rgba(255, 200, 100, 0.4);
    border-radius: 4px;
    color: #d8d4ad;
    font-size: .76rem;
    line-height: 1.5;
  }
  .pkg-summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: .85rem;
    margin: .85rem 0;
    padding: .75rem .85rem;
    background: #111425;
    border: 1px solid #242947;
    border-radius: 8px;
  }
  .pkg-summary span {
    border-radius: 999px;
    padding: .18rem .55rem;
    font-size: .72rem;
    font-weight: 700;
    white-space: nowrap;
  }
  .ok { color: #76e0a2; }
  .bad { color: #ff8080; }
  .warn { color: #ffd27a; }
  .validation-list {
    display: flex;
    flex-direction: column;
    gap: .35rem;
    margin: .65rem 0;
  }
  .validation-list > div {
    padding: .5rem .65rem;
    border-radius: 6px;
    font-size: .78rem;
    line-height: 1.35;
  }
  .validation-list .ferr {
    background: rgba(255, 90, 90, .1);
    border: 1px solid rgba(255, 90, 90, .28);
    color: #ffc1c1;
  }
  .validation-list .fwarn {
    background: rgba(255, 210, 122, .08);
    border: 1px solid rgba(255, 210, 122, .22);
    color: #ffe2a4;
  }
  .req-grid {
    display: grid;
    grid-template-columns: 1fr;
    gap: .3rem;
    max-height: 260px;
    overflow: auto;
    margin: .45rem 0 .8rem;
  }
  .req-row {
    display: grid;
    grid-template-columns: 110px 1fr 92px;
    gap: .6rem;
    align-items: center;
    padding: .42rem .55rem;
    background: #0e1020;
    border: 1px solid #202541;
    border-radius: 6px;
    font-size: .76rem;
  }
  .req-row span {
    color: #7f86a6;
    text-transform: uppercase;
    font-size: .66rem;
    letter-spacing: .04em;
  }
  .req-row b {
    color: #dfe3ff;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .req-row em {
    font-style: normal;
    text-align: right;
  }
  .warn-list {
    color: #d9c990;
    font-size: .78rem;
    line-height: 1.45;
  }
  .check-row {
    display: flex;
    flex-wrap: wrap;
    gap: .8rem 1rem;
    margin: .8rem 0;
    color: #aeb4d5;
    font-size: .8rem;
  }
  .check-row label {
    display: flex;
    align-items: center;
    gap: .42rem;
  }

  /* F-GUI-2 — Security Doctor modal styling. Colors mirror the deployment
     severity palette (info/warn/danger) already used across the dashboard. */
  .doctor-banner {
    padding: .7rem .9rem; border-radius: 8px;
    display: flex; flex-direction: column; gap: .25rem;
    font-size: .8rem; margin-top: .5rem;
  }
  .doctor-banner strong { display: block; margin-bottom: .1rem; }
  .doctor-banner p { margin: 0; color: #c8cadf; font-size: .74rem; }
  .doctor-banner em { color: #7b82a8; font-style: normal; }
  .doctor-banner.info   { background: rgba(139,220,255,.08); border: 1px solid rgba(139,220,255,.3); color: #8bdcff; }
  .doctor-banner.warn   { background: rgba(245,167,66,.10);  border: 1px solid rgba(245,167,66,.35); color: #f5bd67; }
  .doctor-banner.danger { background: rgba(240,96,96,.10);   border: 1px solid rgba(240,96,96,.35); color: #f06060; }
  .doctor-cat {
    display: inline-block; padding: .06rem .35rem; border-radius: 3px;
    font-size: .66rem; text-transform: uppercase; letter-spacing: .05em;
    background: rgba(255,255,255,.06); color: inherit; margin-right: .3rem;
  }

  .doctor-grid {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: .75rem; margin-top: .8rem;
  }
  .doctor-card {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    padding: .75rem .9rem;
  }
  .doctor-card h3 {
    margin: 0 0 .5rem 0; font-size: .82rem; color: #ada8ff;
    text-transform: uppercase; letter-spacing: .05em;
  }
  .doctor-kv {
    display: flex; justify-content: space-between; gap: .5rem;
    padding: .3rem 0; border-bottom: 1px dashed #1a1e36;
    font-size: .76rem;
  }
  .doctor-kv:last-of-type { border-bottom: none; }
  .doctor-kv span { color: #7b82a8; }
  .doctor-kv code, .doctor-kv strong { color: #dfe2ff; font-size: .76rem; }
  .doctor-card ul { margin: .35rem 0 0 1rem; padding: 0; color: #c8cadf; font-size: .75rem; }
  .doctor-card ul li { margin: .15rem 0; }
  .doctor-tool-list { list-style: none; margin: 0 !important; padding: 0 !important; }
  .doctor-tool-list li {
    display: flex; flex-wrap: wrap; align-items: center; gap: .35rem;
    padding: .3rem 0; border-bottom: 1px dashed #1a1e36;
  }
  .doctor-tool-list li code { color: #e7e8f5; font-size: .74rem; }
  .doctor-pill {
    padding: .04rem .38rem; border-radius: 3px;
    font-size: .66rem; text-transform: uppercase; letter-spacing: .04em; font-weight: 600;
    background: rgba(139,220,255,.15); color: #8bdcff;
  }
  .doctor-pill.info   { background: rgba(139,220,255,.15); color: #8bdcff; }
  .doctor-pill.warn   { background: rgba(245,167,66,.18);  color: #f5bd67; }
  .doctor-pill.danger { background: rgba(240,96,96,.18);   color: #f06060; }
  .doctor-empty { color: #7b82a8; font-size: .74rem; margin: 0; }

  .doctor-dryrun { margin-top: .8rem; }
  .doctor-hint { color: #7b82a8; font-size: .74rem; margin: .2rem 0 .6rem 0; }
  .doctor-dr-fields {
    display: grid; grid-template-columns: repeat(2, 1fr); gap: .5rem;
    margin-bottom: .5rem;
  }
  .doctor-dr-fields label { display: flex; flex-direction: column; gap: .2rem; font-size: .72rem; color: #7b82a8; }
  .doctor-dr-fields input, .doctor-dr-fields textarea {
    background: #0a0c17; color: #d7dcf5; border: 1px solid #1a1e36;
    border-radius: 6px; padding: .35rem .5rem; font-size: .78rem;
  }
  .doctor-dr-fields textarea { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .doctor-dr-wide { grid-column: 1 / -1; }
  .doctor-dr-actions { display: flex; justify-content: flex-end; margin: .3rem 0; }
  .doctor-result {
    margin-top: .6rem; padding: .65rem .8rem;
    background: rgba(139,220,255,.05); border: 1px solid rgba(139,220,255,.25);
    border-radius: 8px;
  }
  .doctor-result-row { display: flex; flex-wrap: wrap; gap: .35rem; }
  .doctor-verdict { color: #e7e8f5; font-size: .82rem; margin: .55rem 0 .3rem 0; }
  .doctor-reason  { color: #c8cadf; font-size: .74rem; margin: 0 0 .35rem 0; }
  .doctor-pattern { color: #ada8ff; font-size: .72rem; }
  .doctor-snippet {
    width: 100%; margin-top: .2rem;
    padding: .3rem .5rem; background: #0a0c17; border: 1px solid #1a1e36;
    border-radius: 5px; color: #7b82a8;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: .68rem; white-space: pre-wrap; word-break: break-word;
  }
</style>
