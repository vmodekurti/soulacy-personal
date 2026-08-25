<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onDestroy, onMount, tick } from 'svelte'
  import { slide } from 'svelte/transition'
  import { api, apiFetch, createEventSocket } from '../lib/api.js'
  import { chatActiveThreadId, chatThreads, connected } from '../lib/stores.js'
  import { activeWorkspace } from '../lib/workspace.js'
  import RunMetrics from '../lib/RunMetrics.svelte'
  import { entryIdForMessage, nextBranchLabel, entriesToMessages } from '../lib/chatbranch.js'
  import { deltaMetrics, deltaLabel, deltaTitle } from '../lib/chatmetrics.js'
  import { parseMarkdown, richRenderer } from '../lib/markdown.js'
  import { explainConfirmRequest } from '../lib/explainCommand.js'
  import { searchSkills, parseSlashQuery, applySkillChoice } from '../lib/skillsearch.js'
  import { modelAvailability } from '../lib/agentmodel.js'
  import {
    filterThreads, suggestedPrompts, buildOverrides, tokenBudgetRecovery,
    lastUserText, truncateForRerun, rerunCheckpointStrategy, isLongOutput, isHistoricalFailureResolved,
  } from '../lib/chatactions.js'
  import {
    nextVoiceState, realtimeCallURL, classifyRealtimeEvent,
    addUsage, voiceUsageLabel, voiceHint, updateVoiceActivity, speechChunks,
  } from '../lib/voice.js'

  let metricsRefresh = 0
  let forking = false
  let activeThread = null
  let activeRuns = {}
  let threads = []
  let visibleMessages = []
  let isSending = false

  // Conversation management (search / archive) + model controls + editing state.
  let threadSearch = ''
  let showArchived = false
  let renamingId = ''
  let renameText = ''
  let controlsOpen = false
  let chatMoreOpen = false
  let chatListHidden = false   // collapse the chat sub-menu (thread list)
  function toggleChatList() {
    chatListHidden = !chatListHidden
    try { localStorage.setItem('soulacy-chatlist-hidden', chatListHidden ? '1' : '0') } catch (_) {}
  }
  const controlTips = {
    provider: 'Override the agent provider for this chat turn. Leave blank to use the agent configuration.',
    model: 'Override the model for this chat turn. Useful for retrying the same prompt on a stronger, cheaper, or faster model.',
    temperature: 'Controls randomness. Lower values are more deterministic and better for tool use; higher values are more exploratory.',
    topP: 'Nucleus sampling. Lower values narrow the token pool for more stable output; higher values allow broader phrasing.',
    maxTokens: 'Caps the model response length. Raise for reports or long synthesis; lower to reduce cost and rambling.',
    responseFormat: 'Requests a structured output mode when the provider supports it. JSON is best for extraction and downstream tools.',
    reasoningEffort: 'Hints how much hidden reasoning budget to spend on models that support it. Higher can improve hard tasks but cost more.',
    presencePenalty: 'Positive values encourage introducing new topics instead of repeating already-mentioned concepts.',
    frequencyPenalty: 'Positive values reduce repeated words and phrases. Useful when responses loop or overuse the same wording.',
    toolChoice: 'Constrains the first tool call. Use auto for normal routing, or a specific tool name to force the opening move.',
    runBudgetTokens: 'Total prompt and response tokens available across the entire run, including every tool turn. This is different from Max tokens, which only limits one model response.',
    runBudgetCalls: 'Maximum model calls across the run. Keep the current value when only the token budget needs more room.',
  }
  const emptyControls = () => ({ provider: '', model: '', temperature: '', topP: '', maxTokens: '', responseFormat: '', reasoningEffort: '', presencePenalty: '', frequencyPenalty: '', toolChoice: '', runBudgetTokens: '', runBudgetCalls: '' })
  let controls = emptyControls()
  let providers = []
  let modelsByProv = {}
  let modelsLoading = {}
  let modelsError = {}
  let expanded = {}            // messageKey -> bool (collapse long outputs)
  let editingMsg = -1          // index of a user message being edited
  let editText = ''
  let copiedKey = ''           // transient "Copied!" feedback key
  let feedbackBusy = {}        // run id -> request in flight
  let searchEl, composerEl, fileInputEl
  let artifactPanelOpen = false
  let artifactsByThread = {}
  let artifactLoading = {}
  let artifactError = {}
  let currentArtifacts = []
  let pendingAttachments = []
  let uploadingAttachment = false
  let historySearchOpen = false
  let historyQuery = ''
  let historyResults = []
  let historySearching = false
  let historySearchError = ''
  let chatStatus = null
  let chatStatusOpen = false

  $: activeThread = $chatActiveThreadId ? ($chatThreads[$chatActiveThreadId] || null) : null
  $: threads = filterThreads(Object.values($chatThreads), threadSearch, showArchived, agentName)
  $: visibleMessages = activeThread?.messages || []
  $: isSending = !!activeThread?.sending
  $: canAdjustRunBudget = ['owner', 'admin'].includes(String($activeWorkspace?.role || '').toLowerCase())
  $: currentArtifacts = activeThread ? (artifactsByThread[activeThread.id] || []) : []
  $: enabledProviders = providers.filter(p => p.registered)
  $: activeAgentConfig = agents.find(a => a.id === activeThread?.agentId) || null
  $: activeAgentLLM = activeAgentConfig?.llm || {}
  $: effectiveProvider = String(controls.provider || activeAgentLLM.provider || '').trim()
  $: effectiveModel = String(controls.model || activeAgentLLM.model || '').trim()
  $: if (effectiveProvider) loadModels(effectiveProvider)
  $: chatModelOptions = (() => {
    const list = (modelsByProv[effectiveProvider] || [])
    const cur = controls.model || ''
    const others = list.filter(m => m !== cur && m !== '__custom__')
    return cur ? [cur, ...others] : others
  })()
  $: chatModelStatus = modelAvailability({
    provider: effectiveProvider,
    model: effectiveModel,
    models: modelsByProv[effectiveProvider] || [],
    loading: !!modelsLoading[effectiveProvider],
    error: modelsError[effectiveProvider] || '',
  })

  async function loadChatStatus() {
    try {
      chatStatus = await api.chatStatus()
    } catch (_) {
      chatStatus = null
    }
  }

  function chatStatusTitle(status) {
    if (!status) return 'Chat readiness'
    return `${status.ready || 0}/${status.total || 0} chat capabilities ready`
  }

  function newChatSessionId() {
    return `gui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
  }

  function newThread(agentId = '') {
    const now = Date.now()
    return {
      id: `thread-${now}-${Math.random().toString(36).slice(2, 8)}`,
      agentId,
      sessionId: newChatSessionId(),
      title: agentId || 'New chat',
      messages: [],
      sending: false,
      branches: [],
      branchMessages: {},
      metricsBaseline: {},
      thinking: null,
      activeRunKey: '',
      createdAt: now,
      updatedAt: now,
    }
  }

  function upsertThread(thread) {
    chatThreads.update(ts => ({ ...ts, [thread.id]: thread }))
  }

  // ── persistence: keep the chat list across reloads so each chat stays in the
  //    left column instead of being reset to a single fresh thread ──────────
  const THREADS_KEY = 'soulacy-chat-threads'
  let hydrated = false

  function persistThreads(threads, activeId) {
    if (!hydrated) return
    try {
      const slim = {}
      for (const [id, t] of Object.entries(threads || {})) {
        slim[id] = {
          id: t.id, agentId: t.agentId, sessionId: t.sessionId, title: t.title,
          pinned: !!t.pinned, archived: !!t.archived,
          createdAt: t.createdAt, updatedAt: t.updatedAt,
          branches: t.branches || [],
          messages: (t.messages || []).map(m => ({
            role: m.role, text: m.text, via: m.via || '', agentId: m.agentId || '',
            ts: m.ts instanceof Date ? m.ts.toISOString() : m.ts,
            metrics: m.metrics || null,
            parts: m.parts || null,
            attachments: m.attachments || null,
            runId: m.runId || '', responseId: m.responseId || '', feedback: m.feedback || 0,
          })),
        }
      }
      localStorage.setItem(THREADS_KEY, JSON.stringify({ threads: slim, activeId }))
    } catch (_) { /* storage full or unavailable — best-effort */ }
  }

  function restoreThreads() {
    try {
      const raw = localStorage.getItem(THREADS_KEY)
      if (!raw) return false
      const saved = JSON.parse(raw)
      const entries = Object.entries(saved.threads || {})
      if (!entries.length) return false
      const revived = {}
      for (const [id, t] of entries) {
        revived[id] = {
          ...newThread(t.agentId),
          id: t.id, agentId: t.agentId, sessionId: t.sessionId, title: t.title,
          pinned: !!t.pinned, archived: !!t.archived,
          createdAt: t.createdAt || Date.now(), updatedAt: t.updatedAt || Date.now(),
          branches: t.branches || [],
          messages: (t.messages || []).map(m => ({ ...m, ts: m.ts ? new Date(m.ts) : new Date() })),
        }
      }
      chatThreads.set(revived)
      const active = (saved.activeId && revived[saved.activeId]) ? saved.activeId : entries[0][0]
      chatActiveThreadId.set(active)
      return true
    } catch (_) { return false }
  }

  // Save whenever the threads or the active selection change (after hydration).
  $: persistThreads($chatThreads, $chatActiveThreadId)

  function updateThread(threadId, fn) {
    chatThreads.update(ts => {
      const curr = ts[threadId]
      if (!curr) return ts
      return { ...ts, [threadId]: { ...fn(curr), updatedAt: Date.now() } }
    })
  }

  function updateActiveThread(fn) {
    if (!$chatActiveThreadId) return
    updateThread($chatActiveThreadId, fn)
  }

  function agentName(id) {
    const a = agents.find(x => x.id === id)
    return a?.name || id || 'No agent'
  }

  async function rateResponse(msg, rating) {
    const thread = activeThread
    const runId = msg?.runId || ''
    if (!thread || !runId || feedbackBusy[runId]) return
    feedbackBusy = { ...feedbackBusy, [runId]: true }
    try {
      await api.chatFeedback({
        agent_id: msg.agentId || thread.agentId,
        session_id: thread.sessionId,
        run_id: runId,
        response_id: msg.responseId || '',
        rating,
      })
      updateActiveThread(t => ({
        ...t,
        messages: t.messages.map(m => m === msg ? { ...m, feedback: rating, feedbackError: '' } : m),
      }))
    } catch (e) {
      updateActiveThread(t => ({
        ...t,
        messages: t.messages.map(m => m === msg ? { ...m, feedbackError: e.message || 'Could not save feedback' } : m),
      }))
    } finally {
      const next = { ...feedbackBusy }
      delete next[runId]
      feedbackBusy = next
    }
  }

  // resolveMention parses a leading `@agent` from a typed message and routes
  // that single turn to the mentioned agent (#9). The mention matches an agent
  // by id or by name with spaces removed, case-insensitively, so `@FlightDeals`
  // hits an agent named "Flight Deals". Returns null when there's no match, so
  // an ordinary message that happens to start with "@" is left untouched.
  function resolveMention(text) {
    const m = /^@([A-Za-z0-9._-]+)\b[ \t]*/.exec(text || '')
    if (!m) return null
    const token = m[1].toLowerCase()
    const hit = agents.find(a =>
      (a.id || '').toLowerCase() === token ||
      (a.name || '').toLowerCase().replace(/\s+/g, '') === token,
    )
    if (!hit) return null
    return { agentId: hit.id, name: hit.name || hit.id, cleanText: text.slice(m[0].length).trim() }
  }

  function selectThread(id) {
    if (!id || !$chatThreads[id]) return
    chatActiveThreadId.set(id)
    metricsRefresh++
    const t = $chatThreads[id]
    if (t?.agentId && t?.sessionId) loadArtifacts(id, t.agentId, t.sessionId)
    scrollBottom()
  }

  function startThread(agentId = '') {
    const thread = newThread(agentId || activeThread?.agentId || defaultAgentId())
    upsertThread(thread)
    chatActiveThreadId.set(thread.id)
    metricsRefresh++
    scrollBottom()
  }

  function defaultAgentId() {
    const sys = agents.find(a => a.id === 'system')
    return sys ? sys.id : (agents[0]?.id || '')
  }

  function setActiveAgent(agentId) {
    if (!activeThread) {
      startThread(agentId)
      return
    }
    if (agentId === activeThread.agentId) return
    // Selecting a different agent opens a NEW chat in the left list, so the
    // existing conversation stays as its own item. Exception: if the current
    // chat is still empty, just retarget it (avoids piling up blank chats).
    if ((activeThread.messages || []).length === 0) {
      updateActiveThread(t => ({ ...t, agentId, title: agentName(agentId) }))
    } else {
      startThread(agentId)
    }
  }

  // ── checkpoints & branching (Story 8) ───────────────────────────────
  async function forkAt(mi) {
    if (forking || isSending || !activeThread) return
    forking = true
    error = null
    const threadId = activeThread.id
    try {
      const hist = await api.history.get(activeThread.sessionId)
      const entryId = entryIdForMessage(hist.entries || [], activeThread.messages, mi)
      if (!entryId) {
        error = 'This message has no saved history yet — finish the turn first.'
        return
      }
      const res = await api.history.fork(activeThread.sessionId, {
        agent_id: activeThread.agentId,
        upto_entry_id: entryId,
      })

      // Register branches (current session becomes "main" on first fork).
      let branches = activeThread.branches || []
      if (branches.length === 0) {
        branches = [{ sessionId: activeThread.sessionId, label: 'main' }]
      }
      const label = nextBranchLabel(branches)
      branches = [...branches, { sessionId: res.session_id, label }]

      // Snapshot the current branch, then switch to the fork.
      const forkedView = activeThread.messages.slice(0, mi + 1)
      updateThread(threadId, t => ({
        ...t,
        branches,
        branchMessages: { ...(t.branchMessages || {}), [t.sessionId]: t.messages },
        sessionId: res.session_id,
        messages: forkedView,
      }))
      metricsRefresh++
    } catch (e) {
      error = e.message || 'Fork failed'
    } finally {
      forking = false
    }
  }

  async function switchBranch(sessionId) {
    if (!activeThread || sessionId === activeThread.sessionId || isSending) return
    error = null
    const threadId = activeThread.id
    let msgs = activeThread.branchMessages?.[sessionId]
    if (!msgs) {
      try {
        const hist = await api.history.get(sessionId)
        msgs = entriesToMessages(hist.entries)
      } catch { msgs = [] }
    }
    updateThread(threadId, t => ({
      ...t,
      branchMessages: { ...(t.branchMessages || {}), [t.sessionId]: t.messages },
      sessionId,
      messages: msgs || [],
    }))
    metricsRefresh++
    await scrollBottom()
  }

  let agents    = []
  let input     = ''
  let error     = null
  let shareBusy = false
  let shareLink = ''      // last-created shareable URL (shown in a small toast)
  let shareErr  = ''
  let toolRetry = {}      // toolKey(ev) → { busy, ok, output, error, durationMs }
  let msgListEl

  // Command palette + saved prompts.
  let paletteOpen = false
  let paletteQuery = ''
  let paletteIndex = 0
  let savedPrompts = []
  let promptsOpen = false
  try { savedPrompts = JSON.parse(localStorage.getItem('soulacy-saved-prompts') || '[]') } catch (_) { savedPrompts = [] }

  function persistPrompts() {
    try { localStorage.setItem('soulacy-saved-prompts', JSON.stringify(savedPrompts)) } catch (_) {}
  }
  function saveCurrentPrompt() {
    const text = (input || '').trim()
    if (!text) return
    const title = text.slice(0, 48) + (text.length > 48 ? '…' : '')
    savedPrompts = [{ title, text }, ...savedPrompts.filter(p => p.text !== text)].slice(0, 30)
    persistPrompts()
  }
  function usePrompt(p) { input = p.text; promptsOpen = false }
  function deletePrompt(p) { savedPrompts = savedPrompts.filter(x => x !== p); persistPrompts() }

  $: paletteCommands = [
    { id: 'new', label: 'New chat', run: () => startThread() },
    { id: 'clear', label: 'Clear this chat', run: () => clearChat() },
    { id: 'export-md', label: 'Export chat as Markdown', run: () => exportThreadMarkdown() },
    { id: 'export-json', label: 'Export chat as JSON', run: () => exportThreadJSON() },
    { id: 'share', label: 'Share chat (read-only link)', run: () => shareThread() },
    { id: 'search', label: 'Search chat history', run: () => historySearchOpen = true },
    { id: 'save-prompt', label: 'Save current input as a prompt', run: () => saveCurrentPrompt() },
    { id: 'prompts', label: 'Open saved prompts', run: () => promptsOpen = true },
    ...savedPrompts.map((p, i) => ({ id: 'sp' + i, label: 'Prompt: ' + p.title, run: () => usePrompt(p) })),
  ]
  $: paletteFiltered = paletteCommands.filter(c => c.label.toLowerCase().includes(paletteQuery.toLowerCase()))

  // Focus an element when it mounts (a11y-friendly replacement for autofocus).
  function focusOnMount(node) { node.focus() }
  function openPalette() { paletteOpen = true; paletteQuery = ''; paletteIndex = 0 }
  function runPaletteItem(c) { paletteOpen = false; if (c && c.run) c.run() }
  function paletteKeydown(e) {
    if (e.key === 'Escape') { paletteOpen = false; return }
    if (e.key === 'ArrowDown') { e.preventDefault(); paletteIndex = Math.min(paletteIndex + 1, paletteFiltered.length - 1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); paletteIndex = Math.max(paletteIndex - 1, 0) }
    else if (e.key === 'Enter') { e.preventDefault(); runPaletteItem(paletteFiltered[paletteIndex]) }
  }
  function globalKeydown(e) {
    if (e.key === 'Escape' && voiceSessionOpen) {
      voiceSessionOpen = false
      return
    }
    if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
      e.preventDefault()
      paletteOpen ? (paletteOpen = false) : openPalette()
    }
  }
  let ws        = null
  let stopEvents = false
  let confirmRequest = null
  // Plain-English explanation of what approving the pending tool call would do.
  $: confirmExplain = confirmRequest ? explainConfirmRequest(confirmRequest) : null

  // ── load agents once on mount ────────────────────────────────────────
  async function loadAgents() {
    try {
      const res = await api.agents.list()
      // Interface-aware (Stories #11/#12): only show agents meant for Chat —
      // a cron-only agent is hidden unless it explicitly supports chat. The
      // server reports chat_eligible per agent in `interfaces`.
      const iface = (res && res.interfaces) || {}
      const chatOK = (a) => {
        const m = iface[a.id]
        if (m && typeof m.chat_eligible === 'boolean') return m.chat_eligible
        // Fallback for older servers: hide pure cron/oneshot agents.
        return a.trigger !== 'cron' && a.trigger !== 'oneshot'
      }
      agents = (res.agents || []).filter(a => a.enabled && chatOK(a))
      if (agents.length && Object.keys($chatThreads).length === 0) {
        startThread(defaultAgentId())
      }
    } catch (e) { error = e.message }
  }

  async function loadProviders() {
    try {
      const res = await api.providers.list()
      const registered = res.registered || []
      const ids = new Set([...registered, ...(res.known || []), ...Object.keys(res.providers || {})])
      providers = [...ids].map(id => ({
        id,
        registered: registered.includes(id),
        configured: (res.providers || {})[id] != null,
        defaultModel: (res.providers || {})[id]?.model || '',
      })).sort((a, b) => a.id.localeCompare(b.id))
    } catch (_) {
      providers = []
    }
  }

  async function loadModels(providerId, force = false) {
    if (!providerId) return
    if (!force && (modelsByProv[providerId] || modelsLoading[providerId])) return
    modelsLoading = { ...modelsLoading, [providerId]: true }
    try {
      const res = await api.providers.models(providerId)
      modelsByProv = { ...modelsByProv, [providerId]: res.models || [] }
      modelsError = { ...modelsError, [providerId]: '' }
    } catch (e) {
      modelsByProv = { ...modelsByProv, [providerId]: [] }
      modelsError = { ...modelsError, [providerId]: e.message }
    } finally {
      modelsLoading = { ...modelsLoading, [providerId]: false }
    }
  }

  function onControlProviderChange() {
    const prov = providers.find(p => p.id === controls.provider)
    controls.model = prov?.defaultModel || ''
  }

  function refreshControlModels() {
    if (!effectiveProvider) return
    loadModels(effectiveProvider, true)
  }

  // ── send a message ───────────────────────────────────────────────────
  // NOTE: this function intentionally uses store setters, not local vars.
  // If the component unmounts mid-request, the async continuation still
  // runs and updates the store; the component picks it up on remount.
  async function send(textArg, overridesArg, responseMode = '') {
    const text = (textArg != null ? textArg : input).trim()
    if (!text || !activeThread?.agentId || isSending) return
    const overrides = overridesArg !== undefined ? overridesArg : buildOverrides(controls)
    const threadId = activeThread.id
    const runSessionId = activeThread.sessionId
    // @mention routing (#9): a leading "@agent" sends just this turn to another
    // agent. We keep the user's typed text (mention and all) in the bubble, but
    // send the stripped text to the routed agent and tag the reply with "via".
    const route = resolveMention(text)
    const runAgentId = route ? route.agentId : activeThread.agentId
    const sendText = route ? route.cleanText : text
    const viaName = route ? route.name : ''
    const runKey = `${runAgentId}|${runSessionId}`
    const turnAttachments = textArg == null ? pendingAttachments : []
    const attachmentIds = turnAttachments.map(a => a.id).filter(Boolean)
    const thinking = { open: true, events: [] }
    const runStartedAt = Date.now()
    activeRuns = { ...activeRuns, [runKey]: threadId }
    if (textArg == null) input = ''
    if (textArg == null) pendingAttachments = []
    updateThread(threadId, t => ({
      ...t,
      title: t.messages.length ? t.title : snippet(text, 36),
      messages: [...t.messages, { role: 'user', text, attachments: turnAttachments, ts: new Date() }],
      sending: true,
      thinking,
      streamText: '',       // reset the live-streaming buffer for this turn
      activeRunKey: runKey,
    }))
    await scrollBottom()

    // Pre-turn metrics snapshot for the token delta (Story 9). Cached per
    // session; the first turn fetches (404 → null baseline = "all new").
    let preTurn = activeThread.metricsBaseline?.[runSessionId] ?? null
    if (preTurn === null) {
      preTurn = await api.runs.metrics(runSessionId, runAgentId).catch(() => null)
    }

    let replyText = ''
    let spokenReply = ''
    try {
      const res = await api.chat(runAgentId, sendText, 'gui-user', overrides, runSessionId, attachmentIds, responseMode)
	  replyText = res.reply || ''
	  spokenReply = res.spoken_reply || ''
      const curr = await api.runs.metrics(runSessionId, runAgentId).catch(() => null)
      const delta = deltaMetrics(preTurn, curr)
      updateThread(threadId, t => ({
        ...t,
        // Don't pollute the thread agent's metrics baseline with a routed turn.
        metricsBaseline: (curr && !route) ? { ...(t.metricsBaseline || {}), [runSessionId]: curr } : (t.metricsBaseline || {}),
        streamText: '',   // final reply is authoritative; drop the live preview
        messages: [...t.messages, { role: 'assistant', text: res.reply, via: viaName, agentId: runAgentId, runId: res.run_id || '', responseId: res.response_id || '', feedback: 0, parts: (res.parts || []).filter(p => p && p.type && p.type !== 'text'), ts: new Date(), thinking: t.thinking || thinking, metrics: route ? null : delta }],
      }))
      // A reconnect or a briefly unavailable socket must not turn a completed
      // run into an empty Thinking panel. The action log is the authoritative,
      // workspace-scoped trace; merge it after the run while the live stream
      // remains the fast path.
      await backfillThinkingForTurn(threadId, runAgentId, runSessionId, runStartedAt)
      await loadArtifacts(threadId, runAgentId, runSessionId)
    } catch (e) {
      updateThread(threadId, t => ({
        ...t,
        messages: [...t.messages, { role: 'system', text: '⚠ ' + e.message, ts: new Date(), thinking: t.thinking || thinking }],
      }))
    }
    updateThread(threadId, t => ({ ...t, sending: false, thinking: null, streamText: '', activeRunKey: '' }))
    const { [runKey]: _, ...rest } = activeRuns
    activeRuns = rest
    metricsRefresh++   // re-fetch the session metrics strip (Story 7)
    await scrollBottom()
	return responseMode === 'voice' ? (spokenReply || replyText) : replyText
  }

  // Cancel the active run (Story #22). The run is registered server-side under
  // the session id, so we cancel by that. Best-effort; the in-flight send()
  // continuation will surface the cancellation as a system message.
  async function cancelSend() {
    const sid = activeThread?.sessionId
    if (!sid) return
    try { await api.cancelRun(sid) } catch (_) { /* already finished */ }
  }

  // Resolve a typed message part to a usable src: prefer a URL, else inline the
  // base64 data as a data: URI (Stories #26/#28).
  function partSrc(part) {
    if (!part) return ''
    if (part.url) return part.url
    if (part.data) {
      const mime = part.mime_type || (part.type === 'image' ? 'image/png' : part.type === 'audio' ? 'audio/mpeg' : part.type === 'video' ? 'video/mp4' : 'application/octet-stream')
      return `data:${mime};base64,${part.data}`
    }
    return ''
  }

  async function loadArtifacts(threadId, agentId, sessionId) {
    if (!threadId || !agentId || !sessionId) return
    artifactLoading = { ...artifactLoading, [threadId]: true }
    artifactError = { ...artifactError, [threadId]: '' }
    try {
      const res = await api.chatArtifacts(agentId, sessionId)
      artifactsByThread = { ...artifactsByThread, [threadId]: res.artifacts || [] }
    } catch (e) {
      artifactError = { ...artifactError, [threadId]: e.message || 'Could not load artifacts' }
    } finally {
      artifactLoading = { ...artifactLoading, [threadId]: false }
    }
  }

  async function downloadArtifact(a) {
    if (!activeThread || !a?.path) return
    try {
      const res = await api.downloadChatArtifact(activeThread.agentId, activeThread.sessionId, a.path)
      const href = URL.createObjectURL(res.blob)
      const link = document.createElement('a')
      link.href = href
      link.download = res.filename || a.name || 'artifact'
      document.body.appendChild(link)
      link.click()
      link.remove()
      setTimeout(() => URL.revokeObjectURL(href), 1000)
    } catch (e) {
      artifactError = { ...artifactError, [activeThread.id]: e.message || 'Download failed' }
    }
  }

  // True when an attachment is a video, by mime type or filename extension —
  // used to render an inline <video> player instead of a download chip.
  function isVideoAttachment(a) {
    if (!a) return false
    const mt = (a.mime_type || a.mimeType || a.content_type || '').toLowerCase()
    if (mt.startsWith('video/')) return true
    const name = (a.filename || a.name || '').toLowerCase()
    return /\.(mp4|webm|ogg|ogv|mov|m4v)$/.test(name)
  }

  // Object URLs for playing already-sent video attachments inline (fetched lazily
  // on first play), keyed by attachment id. Revoked on thread teardown.
  let attachmentVideoUrls = {}
  async function playAttachment(a) {
    if (!activeThread || !a?.id || attachmentVideoUrls[a.id]) return
    try {
      const res = await api.downloadChatAttachment(activeThread.agentId, activeThread.sessionId, a.id, a.filename)
      attachmentVideoUrls = { ...attachmentVideoUrls, [a.id]: URL.createObjectURL(res.blob) }
    } catch (e) {
      error = e.message || 'Could not load video'
    }
  }

  async function uploadFiles(files) {
    if (!activeThread?.agentId || !activeThread?.sessionId || !files?.length) return
    uploadingAttachment = true
    error = null
    try {
      for (const file of Array.from(files)) {
        const res = await api.uploadChatAttachment(activeThread.agentId, activeThread.sessionId, file)
        if (res?.attachment) {
          const att = res.attachment
          // Keep a local object URL so the user can preview the video they just
          // attached, before it's ever sent. Revoked in removePendingAttachment.
          if ((file.type || '').startsWith('video/') || isVideoAttachment(att)) {
            att._previewUrl = URL.createObjectURL(file)
          }
          pendingAttachments = [...pendingAttachments, att]
        }
      }
    } catch (e) {
      error = e.message || 'Upload failed'
    } finally {
      uploadingAttachment = false
      if (fileInputEl) fileInputEl.value = ''
    }
  }

  function removePendingAttachment(id) {
    const a = pendingAttachments.find(x => x.id === id)
    if (a && a._previewUrl) { try { URL.revokeObjectURL(a._previewUrl) } catch (_) { /* noop */ } }
    pendingAttachments = pendingAttachments.filter(a => a.id !== id)
  }

  async function downloadAttachment(a) {
    if (!activeThread || !a?.id) return
    const res = await api.downloadChatAttachment(activeThread.agentId, activeThread.sessionId, a.id, a.filename)
    const href = URL.createObjectURL(res.blob)
    const link = document.createElement('a')
    link.href = href
    link.download = res.filename || a.filename || 'attachment'
    document.body.appendChild(link)
    link.click()
    link.remove()
    setTimeout(() => URL.revokeObjectURL(href), 1000)
  }

  function artifactSize(n) {
    if (!Number.isFinite(Number(n))) return ''
    const bytes = Number(n)
    if (bytes < 1024) return `${bytes} B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  }

  function safeFilename(s) {
    return String(s || 'chat')
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 80) || 'chat'
  }

  // extractSources pulls unique http(s) URLs out of an assistant message so the
  // UI can show a compact citation row. Models are prompted (see templates) to
  // cite with Markdown links; this surfaces those as numbered source chips.
  function extractSources(text) {
    if (!text) return []
    const out = []
    const seen = new Set()
    const re = /https?:\/\/[^\s)\]<>"']+/g
    let m
    while ((m = re.exec(text)) !== null) {
      let url = m[0].replace(/[.,;:]+$/, '')
      if (seen.has(url)) continue
      seen.add(url)
      let host = url
      try { host = new URL(url).hostname.replace(/^www\./, '') } catch (_) {}
      out.push({ url, host })
      if (out.length >= 8) break
    }
    return out
  }

  // exportThreadJSON downloads the full conversation as structured JSON — useful
  // for sharing a transcript, archiving, or piping into another tool.
  function exportThreadJSON() {
    if (!activeThread) return
    const title = activeThread.title || agentName(activeThread.agentId) || 'Chat'
    const payload = {
      title,
      agent_id: activeThread.agentId || null,
      agent_name: agentName(activeThread.agentId),
      session_id: activeThread.sessionId || null,
      exported_at: new Date().toISOString(),
      messages: (activeThread.messages || [])
        .filter(m => ['user', 'assistant', 'system'].includes(m.role))
        .map(m => ({
          role: m.role,
          text: m.text || '',
          via: m.via || '',
          ts: m.ts ? new Date(m.ts).toISOString() : null,
          sources: m.role === 'assistant' ? extractSources(m.text).map(s => s.url) : undefined,
          attachments: (m.attachments || []).map(a => a.filename || a.id),
        })),
    }
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json;charset=utf-8' })
    const href = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = href
    link.download = `${safeFilename(title)}-${safeFilename(activeThread.sessionId)}.json`
    document.body.appendChild(link)
    link.click()
    link.remove()
    setTimeout(() => URL.revokeObjectURL(href), 1000)
  }

  function exportThreadMarkdown() {
    if (!activeThread) return
    const title = activeThread.title || agentName(activeThread.agentId) || 'Chat'
    const lines = [
      `# ${title}`,
      '',
      `- Agent: ${agentName(activeThread.agentId)} (${activeThread.agentId || 'none'})`,
      `- Session: ${activeThread.sessionId || 'none'}`,
      `- Exported: ${new Date().toISOString()}`,
      '',
    ]
    for (const msg of activeThread.messages || []) {
      if (msg.role !== 'user' && msg.role !== 'assistant' && msg.role !== 'system') continue
      const label = msg.role === 'user' ? 'User' : msg.role === 'assistant' ? 'Assistant' : 'System'
      lines.push(`## ${label}`)
      if (msg.ts) {
        try { lines.push(`_${new Date(msg.ts).toLocaleString()}_`) } catch (_) {}
      }
      lines.push('', msg.text || '')
      if (msg.attachments?.length) {
        lines.push('', 'Attachments:')
        for (const a of msg.attachments) lines.push(`- ${a.filename || a.id}`)
      }
      lines.push('')
    }
    const blob = new Blob([lines.join('\n')], { type: 'text/markdown;charset=utf-8' })
    const href = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = href
    link.download = `${safeFilename(title)}-${safeFilename(activeThread.sessionId)}.md`
    document.body.appendChild(link)
    link.click()
    link.remove()
    setTimeout(() => URL.revokeObjectURL(href), 1000)
  }

  // shareThread creates a server-side, read-only snapshot of the current
  // conversation and returns a link anyone can open without an API key. The
  // link is copied to the clipboard and shown in a small toast.
  async function shareThread() {
    if (!activeThread || shareBusy) return
    shareBusy = true
    shareErr = ''
    shareLink = ''
    try {
      const title = activeThread.title || agentName(activeThread.agentId) || 'Chat'
      const messages = (activeThread.messages || [])
        .filter(m => ['user', 'assistant', 'system'].includes(m.role))
        .map(m => ({
          role: m.role,
          text: m.text || '',
          via: m.via || '',
          ts: m.ts ? new Date(m.ts).toISOString() : null,
          attachments: (m.attachments || []).map(a => ({ name: a.filename || a.id })),
        }))
      if (!messages.length) { shareErr = 'Nothing to share yet.'; return }
      const res = await api.shareChat({ title, agent_name: agentName(activeThread.agentId), messages })
      shareLink = `${location.origin}/${res.path.replace(/^\//, '')}`
      try { await navigator.clipboard.writeText(shareLink) } catch (_) { /* clipboard blocked — link still shown */ }
    } catch (e) {
      shareErr = (e && e.message) || 'Could not create a share link.'
    } finally {
      shareBusy = false
    }
  }

  // ── per-tool-call retry ──────────────────────────────────────────────
  // Re-run a single tool with the exact arguments it was originally called with,
  // and show the fresh output/error/duration on the card — without re-running
  // the whole turn.
  function toolKey(ev) {
    const p = ev?.payload || {}
    return p.id || `${p.name || 'tool'}:${JSON.stringify(p.arguments || {})}`
  }
  async function retryToolCall(ev) {
    const p = ev?.payload || {}
    const name = p.name
    if (!name) return
    const key = toolKey(ev)
    toolRetry = { ...toolRetry, [key]: { busy: true } }
    try {
      const res = await api.tools.run(name, p.arguments || {})
      const output = res.output == null ? ''
        : (typeof res.output === 'string' ? res.output : JSON.stringify(res.output, null, 2))
      toolRetry = { ...toolRetry, [key]: {
        busy: false, ok: !!res.ok, output, error: res.error || '', durationMs: res.duration_ms,
      } }
    } catch (e) {
      toolRetry = { ...toolRetry, [key]: { busy: false, ok: false, error: (e && e.message) || 'Retry failed.' } }
    }
  }

  async function searchHistory() {
    if (!historyQuery.trim()) return
    historySearching = true
    historySearchError = ''
    try {
      const res = await api.history.search(historyQuery.trim(), activeThread?.agentId || '', 50)
      historyResults = res.hits || []
    } catch (e) {
      historySearchError = e.message || 'Search failed'
    } finally {
      historySearching = false
    }
  }

  async function openHistoryHit(hit) {
    if (!hit?.session_id) return
    try {
      const hist = await api.history.get(hit.session_id)
      const existing = Object.values($chatThreads).find(t => t.sessionId === hit.session_id)
      if (existing) {
        chatActiveThreadId.set(existing.id)
      } else {
        const t = newThread(hit.agent_id || activeThread?.agentId || '')
        t.sessionId = hit.session_id
        t.title = `Search: ${historyQuery.trim().slice(0, 40) || hit.session_id}`
        t.messages = entriesToMessages(hist.entries || [])
        t.createdAt = hit.created_at ? new Date(hit.created_at).getTime() : Date.now()
        t.updatedAt = Date.now()
        upsertThread(t)
        chatActiveThreadId.set(t.id)
      }
      historySearchOpen = false
      metricsRefresh++
      await scrollBottom()
    } catch (e) {
      historySearchError = e.message || 'Could not open session'
    }
  }

  async function scrollBottom() {
    await tick()
    if (msgListEl) msgListEl.scrollTop = msgListEl.scrollHeight
  }

  // ── inline "/" skill picker ──────────────────────────────────────────
  // Typing "/" at the start of the composer opens an autocomplete over the
  // installed skills. Mirrors the existing @agent mention convention.
  let skillList     = []      // installed skills, loaded lazily on first "/"
  let skillsLoaded  = false
  let skillQuery    = null    // null = picker closed
  let skillIndex    = 0

  $: skillMatches = skillQuery === null ? [] : searchSkills(skillList, skillQuery, { limit: 8 })
  $: skillOpen    = skillQuery !== null && skillMatches.length > 0

  async function loadSkillsOnce() {
    if (skillsLoaded) return
    skillsLoaded = true // set first: a failed fetch must not retry on every keystroke
    try {
      const res = await api.skills.list()
      skillList = res.skills || []
    } catch (_) {
      skillList = [] // no skills endpoint / no skills installed → picker simply never opens
    }
  }

  function onComposerInput(e) {
    const el = e.target
    const q = parseSlashQuery(el.value, el.selectionStart)
    if (q !== null) loadSkillsOnce()
    if (q !== skillQuery) skillIndex = 0
    skillQuery = q
  }

  async function chooseSkill(sk) {
    input = applySkillChoice(input, sk.name)
    skillQuery = null
    skillIndex = 0
    await tick()
    composerEl?.focus()
    // Park the caret at the end so the user just keeps typing their request.
    composerEl?.setSelectionRange(input.length, input.length)
  }

  function onKeydown(e) {
    // While the picker is open it owns the navigation keys — otherwise Enter
    // would fire the message off half-typed instead of choosing a skill.
    if (skillOpen) {
      if (e.key === 'ArrowDown') {
        e.preventDefault(); skillIndex = (skillIndex + 1) % skillMatches.length; return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault(); skillIndex = (skillIndex - 1 + skillMatches.length) % skillMatches.length; return
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault(); chooseSkill(skillMatches[skillIndex]); return
      }
      if (e.key === 'Escape') {
        e.preventDefault(); skillQuery = null; return
      }
    }
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
  }

  // ── copy / collapse (rich rendering) ─────────────────────────────────
  async function copyText(text, key) {
    try { await navigator.clipboard.writeText(text || '') } catch (_) { return }
    copiedKey = key
    setTimeout(() => { if (copiedKey === key) copiedKey = '' }, 1200)
  }

  // ── save a reply to the agent's memory (#8) ──────────────────────────
  // Writes the assistant message into the agent's episodic memory so it can be
  // recalled in future runs. Feedback flips the button glyph to a check briefly.
  let savedKey = ''
  async function saveToMemory(text, key) {
    const aid = activeThread?.agentId
    if (!aid || !text || savedKey === key) return
    try {
      await api.brainMemory.writeEpisodic(aid, text, ['saved-from-chat'])
      savedKey = key
      setTimeout(() => { if (savedKey === key) savedKey = '' }, 1500)
    } catch (_) { /* memory may be disabled for this agent; ignore */ }
  }
  function msgKey(mi) { return `${activeThread?.id || ''}:${mi}` }
  function toggleExpand(mi) { const k = msgKey(mi); expanded = { ...expanded, [k]: !expanded[k] } }

  // ── conversation management: rename / pin / archive ──────────────────
  function startRename(t) { renamingId = t.id; renameText = t.title || agentName(t.agentId) }
  function commitRename() {
    if (renamingId) updateThread(renamingId, t => ({ ...t, title: renameText.trim() || t.title }))
    renamingId = ''
  }
  function togglePin(id, e) { if (e) e.stopPropagation(); updateThread(id, t => ({ ...t, pinned: !t.pinned })) }
  function toggleArchive(id, e) { if (e) e.stopPropagation(); updateThread(id, t => ({ ...t, archived: !t.archived })) }

  // ── message actions: regenerate / edit-and-rerun / retry-with-model ──
  // Re-run a turn from a point in the thread. The replay must use a new backend
  // session; otherwise the model would still see the old future turns from the
  // original session even though the UI was truncated.
  async function rerunFrom(mi, text, overrides, { allowFreshFallback = false } = {}) {
    if (isSending || forking || !activeThread) return
    const source = activeThread
    const threadId = source.id
    let kept = truncateForRerun(source.messages, mi)
    forking = true
    error = null
    try {
      let sessionId = newChatSessionId()
      if (kept.length > 0) {
        const hist = await api.history.get(source.sessionId)
        const prevEntryId = entryIdForMessage(hist.entries || [], source.messages, mi - 1)
        const strategy = rerunCheckpointStrategy(true, prevEntryId, allowFreshFallback)
        if (strategy === 'blocked') {
          throw new Error('This turn has no saved checkpoint yet — finish the current reply before rerunning.')
        }
        if (strategy === 'fork') {
          const res = await api.history.fork(source.sessionId, {
            agent_id: source.agentId,
            upto_entry_id: prevEntryId,
          })
          sessionId = res.session_id || sessionId
        } else {
          // The budget pause can happen before the runtime persists a history
          // checkpoint. Restart from the selected user request in a clean
          // session; the original conversation remains available as `main`.
          kept = []
        }
      }

      let branches = source.branches || []
      if (branches.length === 0) {
        branches = [{ sessionId: source.sessionId, label: 'main' }]
      }
      const label = nextBranchLabel(branches)
      updateThread(threadId, t => ({
        ...t,
        branches: [...branches, { sessionId, label }],
        branchMessages: { ...(t.branchMessages || {}), [t.sessionId]: t.messages },
        sessionId,
        messages: kept,
        thinking: null,
        streamText: '',
        activeRunKey: '',
      }))
      metricsRefresh++
      await tick()
      await send(text, overrides)
    } catch (e) {
      error = e.message || 'Rerun failed'
    } finally {
      forking = false
    }
  }

  function editRecommendedBudget(recovery) {
    controls = { ...controls, runBudgetTokens: recovery.recommended, runBudgetCalls: recovery.calls }
    controlsOpen = true
  }

  function editAgentBudgetDefault() {
    if (!activeThread?.agentId) return
    location.hash = `#agents?agent_id=${encodeURIComponent(activeThread.agentId)}&section=budget`
  }

  async function retryWithRecommendedBudget(mi, recovery) {
    let ui = mi - 1
    while (ui >= 0 && visibleMessages[ui]?.role !== 'user') ui--
    if (ui < 0) return
    const overrides = {
      ...(buildOverrides(controls) || {}),
      run_budget: { max_tokens: recovery.recommended, max_llm_calls: recovery.calls },
    }
    await rerunFrom(ui, visibleMessages[ui].text, overrides, { allowFreshFallback: true })
  }

  function budgetRecoveryFor(msg) {
    const structured = tokenBudgetRecovery(msg?.text || '')
    if (structured) return structured
    if (!String(msg?.text || '').includes('token budget cannot fit its prompt')) return null
    const used = Math.max(0, Number(msg?.metrics?.tokens || 0))
    const current = Math.max(1, Number(activeAgentConfig?.budget?.max_tokens || 100000))
    const recommended = Math.min(1000000, Math.ceil(Math.max(current * 1.5, used * 1.5) / 10000) * 10000)
    return { used, current, recommended, calls: Number(activeAgentConfig?.budget?.max_llm_calls ?? 20), ceiling: 1000000 }
  }
  async function regenerate() {
    if (isSending || !activeThread) return
    // Drop the trailing assistant message(s) and replay the last user turn.
    const msgs = activeThread.messages
    let i = msgs.length - 1
    while (i >= 0 && msgs[i].role !== 'user') i--
    if (i < 0) return
    await rerunFrom(i, msgs[i].text)
  }
  function startEdit(mi) { editingMsg = mi; editText = visibleMessages[mi]?.text || '' }
  async function commitEdit() {
    if (editingMsg < 0) return
    const mi = editingMsg, text = editText
    editingMsg = -1
    await rerunFrom(mi, text)
  }
  async function retryWithModel(mi) {
    // Re-run the user turn that produced this assistant message, using the
    // current model controls (lets the user switch model then retry one reply).
    let ui = mi
    while (ui >= 0 && visibleMessages[ui]?.role !== 'user') ui--
    if (ui < 0) return
    await rerunFrom(ui, visibleMessages[ui].text, buildOverrides(controls))
  }

  // ── suggested prompts (per-agent empty state) ────────────────────────
  function promptsFor(agentId) {
    return suggestedPrompts(agents.find(a => a.id === agentId), 4)
  }
  function useSuggestion(s) { input = s; composerEl?.focus() }

  // ── global keyboard shortcuts ────────────────────────────────────────
  function onGlobalKey(e) {
    const meta = e.metaKey || e.ctrlKey
    if (meta && e.key.toLowerCase() === 'k') { e.preventDefault(); searchEl?.focus() }
    else if (meta && e.key.toLowerCase() === 'j') { e.preventDefault(); startThread() }
    else if (e.key === 'Escape' && isSending) { e.preventDefault(); cancelSend() }
  }

  async function resolveConfirm(approved) {
    if (!confirmRequest) return
    try {
      await apiFetch('/chat/confirm', {
        method: 'POST',
        body: JSON.stringify({
          call_id: confirmRequest.call_id,
          approved
        })
      })
    } catch (e) {
      console.error('Failed to confirm tool:', e)
    } finally {
      confirmRequest = null
    }
  }

  function clearChat() {
    if (!activeThread) return
    const replacement = newThread(activeThread.agentId)
    chatThreads.update(ts => {
      const copy = { ...ts }
      delete copy[activeThread.id]
      copy[replacement.id] = replacement
      return copy
    })
    chatActiveThreadId.set(replacement.id)
    metricsRefresh++
  }

  function closeThread(id, e) {
    if (e) e.stopPropagation()
    let nextId = $chatActiveThreadId
    chatThreads.update(ts => {
      const copy = { ...ts }
      delete copy[id]
      if (nextId === id) {
        const remaining = Object.values(copy).sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0))
        nextId = remaining.length > 0 ? remaining[0].id : null
      }
      return copy
    })
    if ($chatActiveThreadId !== nextId) {
      chatActiveThreadId.set(nextId)
      metricsRefresh++
    }
  }

  function fmtTime(d) {
    try { return d.toLocaleTimeString() } catch { return '' }
  }

  function connectEvents() {
    if (stopEvents) return
    try { ws = createEventSocket() } catch { return }
    // App.svelte owns the shell connectivity badge. This socket is scoped to
    // chat activity and closing it during navigation is not a gateway outage.
    ws.onmessage = async (e) => {
      try {
        const ev = JSON.parse(e.data)
        if (ev.type === 'tool_confirm') {
          confirmRequest = ev.payload
          return
        }
        // Live token streaming (real streaming story): the engine relays each
        // reply token as an `assistant.delta`. Accumulate into the thread's
        // streamText so the in-flight bubble renders the answer as it arrives.
        // The authoritative full reply from the POST /chat call replaces it.
        if (ev.type === 'assistant.delta') {
          const tid = threadIdForRunEvent(ev)
          const tok = ev.payload?.text || ''
          if (tid && tok) {
            updateThread(tid, t => ({ ...t, streamText: (t.streamText || '') + tok }))
            await scrollBottom()
          }
          return
        }
        const threadId = threadIdForRunEvent(ev)
        if (ev.type === 'run.artifact' && threadId) {
          const t = $chatThreads[threadId]
          artifactPanelOpen = true
          if (t?.agentId && t?.sessionId) loadArtifacts(threadId, t.agentId, t.sessionId)
          return
        }
        if (!threadId || !isThinkingEvent(ev)) return
        updateThread(threadId, t => {
          if (!t.thinking) return t
          return { ...t, thinking: { ...t.thinking, events: [...t.thinking.events, ev].slice(-80) } }
        })
        await scrollBottom()
      } catch {}
    }
    ws.onclose = () => {
      ws = null
      if (!stopEvents) setTimeout(connectEvents, 3000)
    }
    ws.onerror = () => ws?.close()
  }

  function threadIdForRunEvent(ev) {
    const key = `${ev.agent_id || ''}|${ev.session_id || ''}`
    return activeRuns[key] || ''
  }

  function isThinkingEvent(ev) {
    return ['llm.call', 'llm.result', 'tool.call', 'tool.result', 'tool.log', 'error',
            'reasoning.start', 'reasoning.step', 'reasoning.result'].includes(ev.type || '')
  }

  const THINKING_EVENT_TYPES = 'llm.call,llm.result,tool.call,tool.result,tool.log,error,reasoning.start,reasoning.step,reasoning.result'

  function thinkingEventKey(ev) {
    return `${ev.type || ''}|${ev.timestamp || ''}|${ev.payload?.call_id || ''}|${ev.payload?.name || ''}`
  }

  async function backfillThinkingForTurn(threadId, agentId, sessionId, startedAt, endedAt = Date.now() + 2000) {
    try {
      const res = await api.agents.actions(agentId, 500, THINKING_EVENT_TYPES, { durable: true })
      const recovered = (res.events || []).filter(ev => {
        if (ev.session_id !== sessionId || !isThinkingEvent(ev)) return false
        const at = Date.parse(ev.timestamp || '')
        return Number.isFinite(at) && at >= startedAt - 1000 && at <= endedAt
      })
      if (!recovered.length) return
      updateThread(threadId, t => {
        const current = t.thinking?.events || []
        const seen = new Set(current.map(thinkingEventKey))
        const merged = [...current]
        for (const ev of recovered) {
          const key = thinkingEventKey(ev)
          if (!seen.has(key)) {
            seen.add(key)
            merged.push(ev)
          }
        }
        merged.sort((a, b) => Date.parse(a.timestamp || '') - Date.parse(b.timestamp || ''))
        const nextThinking = { open: true, events: merged.slice(-80) }
        const messages = [...t.messages]
        const last = messages.length - 1
        if (last >= 0 && messages[last].role === 'assistant') {
          messages[last] = { ...messages[last], thinking: nextThinking }
        }
        return { ...t, messages, thinking: nextThinking }
      })
    } catch (_) { /* live events remain available when durable history is disabled */ }
  }

  async function backfillLatestThinking(threadId, agentId, sessionId) {
    const thread = $chatThreads[threadId]
    if (!thread?.messages?.length) return
    let assistantIndex = -1
    for (let i = thread.messages.length - 1; i >= 0; i--) {
      if (thread.messages[i].role === 'assistant') { assistantIndex = i; break }
    }
    if (assistantIndex < 0) return
    let userIndex = -1
    for (let i = assistantIndex - 1; i >= 0; i--) {
      if (thread.messages[i].role === 'user') { userIndex = i; break }
    }
    if (userIndex < 0) return
    const startedAt = new Date(thread.messages[userIndex].ts).getTime()
    const endedAt = new Date(thread.messages[assistantIndex].ts).getTime() + 2000
    await backfillThinkingForTurn(threadId, agentId, sessionId, startedAt, endedAt)
    updateThread(threadId, t => ({
      ...t,
      messages: t.messages.map((m, i) => i === assistantIndex && t.thinking?.events?.length
        ? { ...m, thinking: t.thinking }
        : m),
      thinking: null,
    }))
  }

  function toggleThinking(thinking) {
    if (!thinking) return
    thinking.open = !thinking.open
    updateActiveThread(t => ({ ...t, messages: [...t.messages] }))
  }

  function thinkingSummary(thinking) {
    const n = thinking?.events?.length || 0
    if (n === 0) return $connected ? 'Waiting for activity' : 'Connecting to activity stream'
    const tools = thinking.events.filter(e => (e.type || '').startsWith('tool.')).length
    const llm = thinking.events.filter(e => (e.type || '').startsWith('llm.')).length
    const errors = thinking.events.filter(e => (e.type || '').includes('error')).length
    const recovery = thinking.events.filter(e => e.type === 'reasoning.step' && e.payload?.recovery).length
    const degraded = thinking.events.some(e => e.type === 'reasoning.result' && e.payload?.confident === false)
    return `${n} event${n === 1 ? '' : 's'} · ${llm} LLM · ${tools} tool${tools === 1 ? '' : 's'}${recovery ? ` · ${recovery} recovery` : ''}${errors ? ` · ${errors} error${errors === 1 ? '' : 's'}` : ''}${degraded ? ' · degraded' : ''}`
  }

  function eventTitle(ev) {
    const p = ev.payload || {}
    switch (ev.type) {
      case 'llm.call':    return `Calling ${p.model || 'model'} · turn ${p.turn ?? '?'}`
      case 'llm.result':  return `Model returned ${p.output_tokens ?? 0} output tokens${p.tool_calls ? ` and ${p.tool_calls} tool call${p.tool_calls === 1 ? '' : 's'}` : ''}`
      case 'tool.call':   return `Calling tool ${p.name || 'tool'}`
      case 'tool.result': return `Tool ${p.name || 'tool'} returned`
      case 'tool.log':    return `Tool log`
      case 'reasoning.start':  return `Reasoning loop started (${p.strategy || '?'})`
      case 'reasoning.step':   return `${p.recovery ? 'Recovery step' : 'Step'} ${p.index ?? '?'}${p.tool ? ` → ${p.tool}` : ''}`
      case 'reasoning.result': return `Reasoning finished — ${p.steps ?? 0} step${p.steps === 1 ? '' : 's'}${p.confident === false ? ' · degraded' : ''}`
      case 'error':       return `Error${p.stage ? ` in ${p.stage}` : ''}`
      default:            return ev.type || 'event'
    }
  }

  function eventDetail(ev) {
    const p = ev.payload || {}
    if (ev.type === 'tool.call') return snippet(JSON.stringify(p.arguments || {}), 220)
    if (ev.type === 'tool.result') return snippet(p.content || '', 260)
    if (ev.type === 'tool.log') return snippet(typeof p === 'string' ? p : p.line || JSON.stringify(p), 260)
    if (ev.type === 'error') return snippet(p.error || p.message || JSON.stringify(p), 260)
    if (ev.type === 'llm.result') return `${p.duration_ms ?? 0}ms · ${p.input_tokens ?? 0} in / ${p.output_tokens ?? 0} out`
    if (ev.type === 'reasoning.step') return snippet(p.recovery ? (p.observation || p.thought || '') : (p.thought || ''), 260)
    if (ev.type === 'reasoning.result') return `${p.duration_ms ?? 0}ms · ${p.confident ? 'confident' : 'not confident'}`
    return ''
  }

  function eventClass(type = '', ev = null) {
    if (ev?.type === 'reasoning.step' && ev.payload?.recovery) return 'recovery'
    if (ev?.type === 'reasoning.result' && ev.payload?.confident === false) return 'degraded'
    if (type.includes('error')) return 'err'
    if (type.startsWith('tool.')) return 'tool'
    if (type.startsWith('llm.')) return 'llm'
    return ''
  }

  // ── structured tool-call timeline (#4) ───────────────────────────────
  // fullEventDetail returns the COMPLETE, untruncated payload for an event so
  // the user can expand any tool call / result / error to inspect the raw
  // inputs and outputs (vs. the one-line `eventDetail` summary).
  function fullEventDetail(ev) {
    const p = ev.payload || {}
    if (ev.type === 'tool.call')   return JSON.stringify(p.arguments || {}, null, 2)
    if (ev.type === 'tool.result') return String(p.content ?? '')
    if (ev.type === 'tool.log')    return typeof p === 'string' ? p : (p.line || JSON.stringify(p, null, 2))
    if (ev.type === 'error')       return String(p.error || p.message || JSON.stringify(p, null, 2))
    if (ev.type === 'reasoning.step') return String(p.thought || '')
    if (ev.type === 'llm.call' || ev.type === 'llm.result') return JSON.stringify(p, null, 2)
    return ''
  }
  // eventDuration surfaces how long a step took, when the backend reported it.
  function eventDuration(ev) {
    const ms = (ev.payload || {}).duration_ms
    return ms != null ? `${ms}ms` : ''
  }
  // eventExpandable hides the disclosure toggle when there's nothing useful to
  // reveal (e.g. an empty `{}` argument object).
  function eventExpandable(ev) {
    const full = fullEventDetail(ev)
    return !!full && full !== '{}' && full.trim() !== ''
  }

  // Human-readable label for what the agent is doing *right now*, derived from
  // the most recent runtime event. Drives the pulsing live row while a run is
  // in flight, so the panel reads like a live status instead of a dead log.
  function liveActivity(thinking) {
    const evs = thinking?.events || []
    if (!evs.length) return 'Starting up…'
    const last = evs[evs.length - 1]
    const p = last.payload || {}
    switch (last.type) {
      case 'tool.call':   return `Running ${p.name || 'tool'}…`
      case 'llm.call':    return `Thinking… (${p.model || 'model'}${p.turn ? `, turn ${p.turn}` : ''})`
      case 'tool.result':
      case 'tool.log':    return 'Reading the result…'
      case 'llm.result':  return p.tool_calls ? 'Preparing tool calls…' : 'Writing the answer…'
      case 'reasoning.start':
      case 'reasoning.step': return 'Reasoning…'
      default:            return 'Working…'
    }
  }

  function snippet(s, n = 180) {
    s = String(s ?? '')
    return s.length > n ? s.slice(0, n) + '…' : s
  }

  // ── realtime voice panel (Story 11, docs/VOICE_SPIKE.md) ─────────────
  // Audio flows browser↔provider directly over WebRTC with an ephemeral
  // key minted by the gateway; transcripts attach to this chat session.
  let voiceState  = 'unavailable'
  let voiceDetail = ''
  let voiceModel  = ''
	let voiceProvider = ''
	let voiceEndpoint = ''
	let voiceAcceleration = ''
  let voiceUsage  = null
  let voicePC     = null   // RTCPeerConnection
  let voiceMic    = null   // MediaStream
  let voiceAudio  = null   // <audio> element for remote playback
  let voiceDraftIdx = -1   // index of the streaming assistant bubble
	let voiceRecorder = null
	let voiceChunks = []
	let voiceAudioContext = null
	let voiceAnalyser = null
	let voiceVADFrame = 0
	let voiceVADState = null
	let voiceConversationActive = false
	let voiceThinkingContext = null
	let voiceThinkingTimer = 0
	let sidecarProcessing = false
	let voicePlaybackState = 'idle'
	let voiceSynthesisAbort = null
	let voiceObjectURL = ''
	let voicePlaybackResolve = null
	let voiceSessionOpen = false
	let voiceMuted = false
	let voiceUserTranscript = ''
	let voiceSpokenText = ''
	let voiceSetupOpen = false
	let voiceSetupURL = 'http://127.0.0.1:8081'
	let voiceSetupName = ''
	let voiceSetupRecipe = 'mlx-kokoro'
	let voiceSetupBusy = false
	let voiceSetupError = ''
	let voiceSetupSaved = false
	const voiceRecipes = {
	  'mlx-kokoro': {
		url: 'http://127.0.0.1:8081', voice: 'af_heart',
		detail: 'Apple Silicon native transcription with MLX Whisper and local Kokoro speech. No OpenAI key required.',
		install: 'sy voice enable --recipe auto',
		start: 'sy voice test',
	  },
	  'whisper-kokoro': {
		url: 'http://127.0.0.1:8081', voice: 'af_heart',
		detail: 'Portable transcription through an existing whisper-cli build and ggml model, with local Kokoro speech.',
		install: 'sy voice enable --recipe whisper-kokoro',
		start: 'sy voice test',
	  },
	  custom: {
		url: '', voice: '',
		detail: 'Enter the base URL for any service that implements Soulacy\'s four-endpoint voice contract.',
		install: '', start: '',
	  },
	}
	$: selectedVoiceRecipe = voiceRecipes[voiceSetupRecipe] || voiceRecipes.custom
	$: voiceSessionStatus = voiceState === 'live'
	  ? 'LISTENING'
	  : voicePlaybackState === 'playing'
		? 'SOULACY SPEAKING'
		: voicePlaybackState === 'paused'
		  ? 'VOICE PAUSED'
		  : sidecarProcessing || voiceState === 'connecting'
			? 'SOULACY THINKING'
			: 'VOICE READY'
	$: voiceSessionHeadline = voiceState === 'live'
	  ? 'I\'m listening…'
	  : voicePlaybackState === 'playing' || voicePlaybackState === 'paused'
		? (voiceSpokenText || 'Speaking…')
		: sidecarProcessing || voiceState === 'connecting'
		  ? 'Working on your request…'
		  : 'Start a conversation'

	function selectVoiceRecipe(recipe) {
	  const preset = voiceRecipes[recipe]
	  if (!preset) return
	  voiceSetupRecipe = recipe
	  voiceSetupURL = preset.url
	  voiceSetupName = preset.voice
	  voiceSetupError = ''
	  voiceSetupSaved = false
	}

  async function loadVoiceStatus() {
    try {
      const st = await api.voice.status()
      voiceDetail = st.detail || ''
      voiceModel = st.model || ''
	  voiceProvider = st.provider || ''
	  voiceEndpoint = st.endpoint || ''
	  if (voiceEndpoint) {
		voiceSetupURL = voiceEndpoint
		voiceSetupRecipe = 'custom'
	  }
	  if (st.voice) voiceSetupName = st.voice
      voiceState = nextVoiceState(voiceState, { type: 'status', available: !!st.available })
	  if (st.available && st.provider === 'sidecar') {
		const caps = await api.voice.capabilities().catch(() => null)
		voiceAcceleration = caps
		  ? [caps.stt_accelerator && `STT ${caps.stt_accelerator}`, caps.tts_device && `TTS ${caps.tts_device}`].filter(Boolean).join(' · ')
		  : ''
	  } else {
		voiceAcceleration = ''
	  }
	} catch {
	  voiceAcceleration = ''
	  voiceState = 'unavailable'
    }
  }

  function voicePush(role, text, opts = {}) {
    let idx = -1
    updateActiveThread(t => {
      idx = t.messages.length
      return { ...t, messages: [...t.messages, { role, text, voice: true, ts: new Date(), ...opts }] }
    })
    scrollBottom()
    return idx
  }

  function handleVoiceEvent(raw) {
    let evt
    try { evt = JSON.parse(raw) } catch { return }
    const e = classifyRealtimeEvent(evt)
    if (e.kind === 'user_transcript' && e.text.trim()) {
      voiceUserTranscript = e.text.trim()
      voicePush('user', e.text.trim())
    } else if (e.kind === 'assistant_delta' && e.text) {
	  voiceSpokenText += e.text
      if (voiceDraftIdx < 0) {
        voiceDraftIdx = voicePush('assistant', e.text)
      } else {
        updateActiveThread(t => {
          const copy = [...t.messages]
          copy[voiceDraftIdx] = { ...copy[voiceDraftIdx], text: copy[voiceDraftIdx].text + e.text }
          return { ...t, messages: copy }
        })
      }
    } else if (e.kind === 'assistant_done') {
      if (voiceDraftIdx >= 0 && e.text) {
        updateActiveThread(t => {
          const copy = [...t.messages]
          copy[voiceDraftIdx] = { ...copy[voiceDraftIdx], text: e.text }
          return { ...t, messages: copy }
        })
      }
      voiceDraftIdx = -1
      scrollBottom()
    } else if (e.kind === 'usage') {
      voiceUsage = addUsage(voiceUsage, e.usage)
    }
  }

  async function startVoice() {
    if (voiceState !== 'idle') return
	if (voiceProvider === 'sidecar') return startSidecarVoice()
    voiceState = nextVoiceState(voiceState, { type: 'start' })
    error = null
    try {
      const eph = await api.voice.ephemeral()
      voiceMic = await navigator.mediaDevices.getUserMedia({ audio: true })

      const pc = new RTCPeerConnection()
      voicePC = pc
      for (const track of voiceMic.getTracks()) pc.addTrack(track, voiceMic)
      pc.ontrack = (ev) => {
        if (!voiceAudio) voiceAudio = new Audio()
        voiceAudio.muted = voiceMuted
        voiceAudio.srcObject = ev.streams[0]
        voiceAudio.play().catch(() => {})
      }
      const dc = pc.createDataChannel('oai-events')
      dc.onmessage = (ev) => handleVoiceEvent(ev.data)
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === 'connected') {
          voiceState = nextVoiceState(voiceState, { type: 'connected' })
        } else if (['failed', 'disconnected', 'closed'].includes(pc.connectionState) && voiceState === 'live') {
          stopVoice()
        }
      }

      const offer = await pc.createOffer()
      await pc.setLocalDescription(offer)
      const resp = await fetch(realtimeCallURL(eph.model), {
        method: 'POST',
        headers: { Authorization: `Bearer ${eph.key}`, 'Content-Type': 'application/sdp' },
        body: offer.sdp,
      })
      if (!resp.ok) throw new Error(`provider SDP exchange failed (${resp.status})`)
      await pc.setRemoteDescription({ type: 'answer', sdp: await resp.text() })
      voicePush('system', `🎤 voice session started (${eph.model})`)
    } catch (e) {
      voiceDetail = e.message || 'voice session failed'
      voiceState = nextVoiceState(voiceState, { type: 'fail' })
      teardownVoice()
    }
  }

	async function startSidecarVoice() {
	  if (!voiceConversationActive || voiceRecorder?.state === 'recording' || sidecarProcessing) return
	  error = null
	  try {
		voiceMic = await navigator.mediaDevices.getUserMedia({ audio: true })
		voiceChunks = []
		voiceRecorder = new MediaRecorder(voiceMic)
		voiceRecorder.ondataavailable = (event) => { if (event.data?.size) voiceChunks.push(event.data) }
		voiceRecorder.onstop = processSidecarTurn
		voiceRecorder.start(250)
		startVoiceActivityDetection(voiceMic)
		voiceState = 'live'
	  } catch (e) {
		voiceConversationActive = false
		voiceDetail = e.message || 'microphone capture failed'
		voiceState = 'error'
		teardownVoice()
	  }
	}

	function startVoiceActivityDetection(stream) {
	  stopVoiceActivityDetection()
	  const AudioContextClass = window.AudioContext || window.webkitAudioContext
	  if (!AudioContextClass) return
	  voiceAudioContext = new AudioContextClass()
	  voiceAudioContext.resume().catch(() => {})
	  const source = voiceAudioContext.createMediaStreamSource(stream)
	  voiceAnalyser = voiceAudioContext.createAnalyser()
	  voiceAnalyser.fftSize = 1024
	  source.connect(voiceAnalyser)
	  voiceVADState = { startedAt: performance.now(), heardSpeech: false, silenceSince: 0 }
	  const samples = new Uint8Array(voiceAnalyser.fftSize)
	  const inspect = () => {
		if (!voiceAnalyser || voiceRecorder?.state !== 'recording') return
		voiceAnalyser.getByteTimeDomainData(samples)
		let energy = 0
		for (const sample of samples) {
		  const normalized = (sample - 128) / 128
		  energy += normalized * normalized
		}
		const rms = Math.sqrt(energy / samples.length)
		const result = updateVoiceActivity(voiceVADState, rms, performance.now())
		voiceVADState = result.state
		if (result.action === 'complete') {
		  finishSidecarTurn()
		  return
		}
		if (result.action === 'reset') voiceChunks = []
		voiceVADFrame = requestAnimationFrame(inspect)
	  }
	  voiceVADFrame = requestAnimationFrame(inspect)
	}

	function stopVoiceActivityDetection() {
	  if (voiceVADFrame) cancelAnimationFrame(voiceVADFrame)
	  voiceVADFrame = 0
	  voiceAnalyser = null
	  voiceVADState = null
	  if (voiceAudioContext) voiceAudioContext.close().catch(() => {})
	  voiceAudioContext = null
	}

	function finishSidecarTurn(force = false) {
	  if (voiceRecorder?.state !== 'recording') return
	  if (!force && !voiceVADState?.heardSpeech) return
	  stopVoiceActivityDetection()
	  voiceRecorder.stop()
	}

	function startVoiceThinkingSound() {
	  stopVoiceThinkingSound()
	  if (voiceMuted || !voiceConversationActive) return
	  const AudioContextClass = window.AudioContext || window.webkitAudioContext
	  if (!AudioContextClass) return
	  try {
		voiceThinkingContext = new AudioContextClass()
		voiceThinkingContext.resume().catch(() => {})
		const ping = () => {
		  const context = voiceThinkingContext
		  if (!context || context.state === 'closed') return
		  const start = context.currentTime
		  for (const [frequency, delay] of [[392, 0], [523.25, 0.12]]) {
			const oscillator = context.createOscillator()
			const gain = context.createGain()
			oscillator.type = 'sine'
			oscillator.frequency.value = frequency
			gain.gain.setValueAtTime(0.0001, start + delay)
			gain.gain.exponentialRampToValueAtTime(0.025, start + delay + 0.025)
			gain.gain.exponentialRampToValueAtTime(0.0001, start + delay + 0.24)
			oscillator.connect(gain)
			gain.connect(context.destination)
			oscillator.start(start + delay)
			oscillator.stop(start + delay + 0.26)
		  }
		}
		ping()
		voiceThinkingTimer = window.setInterval(ping, 1450)
	  } catch {
		stopVoiceThinkingSound()
	  }
	}

	function stopVoiceThinkingSound() {
	  if (voiceThinkingTimer) window.clearInterval(voiceThinkingTimer)
	  voiceThinkingTimer = 0
	  const context = voiceThinkingContext
	  voiceThinkingContext = null
	  if (context) context.close().catch(() => {})
	}

	async function processSidecarTurn() {
	  const mime = voiceRecorder?.mimeType || 'audio/webm'
	  const audio = new Blob(voiceChunks, { type: mime })
	  voiceRecorder = null
	  voiceChunks = []
	  if (voiceMic) { for (const track of voiceMic.getTracks()) track.stop(); voiceMic = null }
	  voiceState = 'connecting'
	  sidecarProcessing = true
	  const synthesisAbort = new AbortController()
	  voiceSynthesisAbort = synthesisAbort
	  try {
		const result = await api.voice.transcribe(audio)
		const transcript = String(result?.text || '').trim()
		if (!transcript) return
		voiceUserTranscript = transcript
		startVoiceThinkingSound()
		const reply = await send(transcript, undefined, 'voice')
		stopVoiceThinkingSound()
		if (reply) {
		  for (const chunk of speechChunks(reply)) {
			if (synthesisAbort.signal.aborted) break
			voiceSpokenText = chunk
			voicePlaybackState = 'generating'
			const speech = await api.voice.synthesize(chunk, voiceSetupName, synthesisAbort.signal)
			if (synthesisAbort.signal.aborted) break
			await playVoiceSpeech(speech, synthesisAbort.signal)
		  }
		}
		voiceState = 'idle'
	  } catch (e) {
		if (synthesisAbort.signal.aborted || e?.name === 'AbortError') {
		  voiceState = 'idle'
		} else {
		  voiceDetail = e.message || 'local voice turn failed'
		  voiceState = 'error'
		}
	  } finally {
		stopVoiceThinkingSound()
		if (voiceSynthesisAbort === synthesisAbort) voiceSynthesisAbort = null
		if (voicePlaybackState !== 'paused') voicePlaybackState = 'idle'
		sidecarProcessing = false
		if (voiceConversationActive && voiceState !== 'error') {
		  voiceState = 'idle'
		  setTimeout(() => startSidecarVoice(), 120)
		}
	  }
	}

	function releaseVoiceAudio(resolvePlayback = true) {
	  if (voiceObjectURL) URL.revokeObjectURL(voiceObjectURL)
	  voiceObjectURL = ''
	  if (voiceAudio) {
		voiceAudio.onended = null
		voiceAudio.onerror = null
		voiceAudio.src = ''
	  }
	  const resolve = resolvePlayback ? voicePlaybackResolve : null
	  voicePlaybackResolve = null
	  if (resolve) resolve()
	}

	function playVoiceSpeech(speech, signal) {
	  if (!voiceAudio) voiceAudio = new Audio()
	  releaseVoiceAudio()
	  voiceObjectURL = URL.createObjectURL(speech)
	  voiceAudio.src = voiceObjectURL
	  voiceAudio.muted = voiceMuted
	  voicePlaybackState = 'playing'
	  return new Promise((resolve, reject) => {
		voicePlaybackResolve = resolve
		voiceAudio.onended = () => { releaseVoiceAudio(); voicePlaybackState = 'idle' }
		voiceAudio.onerror = () => { releaseVoiceAudio(false); reject(new Error('Voice playback failed.')) }
		if (signal.aborted) { releaseVoiceAudio(); return }
		signal.addEventListener('abort', () => {
		  voiceAudio?.pause()
		  releaseVoiceAudio()
		  voicePlaybackState = 'idle'
		}, { once: true })
		voiceAudio.play().catch((error) => { releaseVoiceAudio(false); reject(error) })
	  })
	}

	function pauseVoiceResponse() {
	  if (voicePlaybackState !== 'playing' || !voiceAudio) return
	  voiceAudio.pause()
	  voicePlaybackState = 'paused'
	}

	function resumeVoiceResponse() {
	  if (voicePlaybackState !== 'paused' || !voiceAudio) return
	  voiceAudio.play().then(() => voicePlaybackState = 'playing').catch((e) => {
		voiceDetail = e.message || 'Voice playback failed.'
		voiceState = 'error'
	  })
	}

	function toggleVoiceMute() {
	  voiceMuted = !voiceMuted
	  if (voiceAudio) voiceAudio.muted = voiceMuted
	  if (voiceMuted) stopVoiceThinkingSound()
	  else if (sidecarProcessing && voicePlaybackState === 'idle') startVoiceThinkingSound()
	}

	function stopVoiceResponse() {
	  voiceSynthesisAbort?.abort()
	  if (voiceAudio) {
		voiceAudio.pause()
		voiceAudio.currentTime = 0
	  }
	  releaseVoiceAudio()
	  voicePlaybackState = 'idle'
	}

	function endVoiceSession() {
	  voiceConversationActive = false
	  teardownVoice()
	  voiceState = voiceState === 'unavailable' ? 'unavailable' : 'idle'
	  voiceSessionOpen = false
	  voiceUserTranscript = ''
	  voiceSpokenText = ''
	}

	async function saveVoiceSidecar() {
	  voiceSetupBusy = true
	  voiceSetupError = ''
	  voiceSetupSaved = false
	  try {
		await api.config.patch({ voice: {
		  provider: 'sidecar', sidecar_url: voiceSetupURL.trim(), voice: voiceSetupName.trim(),
		  timeout: '60s', allow_remote: false,
		} })
		voiceSetupSaved = true
		voiceDetail = 'Sidecar saved. Restart the gateway, then test voice.'
	  } catch (e) {
		voiceSetupError = e.message || 'Could not save voice configuration.'
	  } finally {
		voiceSetupBusy = false
	  }
	}

  function teardownVoice() {
	voiceConversationActive = false
	stopVoiceResponse()
	stopVoiceThinkingSound()
	stopVoiceActivityDetection()
    if (voicePC) { try { voicePC.close() } catch {} voicePC = null }
	if (voiceRecorder?.state === 'recording') {
	  voiceRecorder.onstop = null
	  try { voiceRecorder.stop() } catch {}
	}
	voiceRecorder = null
	voiceChunks = []
    if (voiceMic) { for (const t of voiceMic.getTracks()) t.stop(); voiceMic = null }
	if (voiceAudio) { voiceAudio.srcObject = null }
    voiceDraftIdx = -1
  }

  function stopVoice() {
	if (voiceProvider === 'sidecar' && voiceRecorder?.state === 'recording') {
	  finishSidecarTurn(true)
	  return
	}
    teardownVoice()
    if (voiceState === 'live' || voiceState === 'connecting') {
      voicePush('system', '🎤 voice session ended')
    }
    voiceState = nextVoiceState(voiceState, { type: 'stop' })
  }

  function voiceClick() {
	if (sidecarProcessing) return
	if (voiceState === 'idle') {
	  voiceSessionOpen = true
	  voiceConversationActive = true
	  voiceSpokenText = ''
	  startVoice()
	}
    else if (voiceState === 'live' || voiceState === 'connecting') stopVoice()
	else if (voiceState === 'unavailable') voiceSetupOpen = true
	else if (voiceState === 'error') voiceState = nextVoiceState(voiceState, { type: 'retry' })
  }

  onMount(async () => {
    try { chatListHidden = localStorage.getItem('soulacy-chatlist-hidden') === '1' } catch (_) {}
    restoreThreads()      // repopulate the chat list from a previous session
    hydrated = true       // now persist future changes
    await Promise.all([loadAgents(), loadProviders()])
    // First-run wizard hands off the freshly created agent so Chat opens with it
    // already selected ("land in Chat with a working agent"). One-shot: consumed
    // then cleared so normal navigation isn't affected.
    try {
      const preselect = localStorage.getItem('soulacy-preselect-agent')
      if (preselect) {
        localStorage.removeItem('soulacy-preselect-agent')
        if (agents.find(a => a.id === preselect)) setActiveAgent(preselect)
      }
    } catch (_) { /* localStorage unavailable — ignore */ }
    if (activeThread?.agentId && activeThread?.sessionId) {
      loadArtifacts(activeThread.id, activeThread.agentId, activeThread.sessionId)
    }
    await Promise.all([loadVoiceStatus(), loadChatStatus()])
    connectEvents()
    if (activeThread?.id && activeThread?.agentId && activeThread?.sessionId) {
      await backfillLatestThinking(activeThread.id, activeThread.agentId, activeThread.sessionId)
    }
    window.addEventListener('keydown', onGlobalKey)
    // Scroll to bottom when returning to a conversation already in progress
    await scrollBottom()
  })

  onDestroy(() => {
    stopEvents = true
    if (ws) ws.close()
    teardownVoice()
    window.removeEventListener('keydown', onGlobalKey)
  })
</script>

<svelte:window on:keydown={globalKeydown} />

{#if voiceSetupOpen}
  <div class="voice-setup-backdrop" role="presentation" on:click|self={() => voiceSetupOpen = false}>
    <div class="voice-setup" role="dialog" aria-modal="true" aria-labelledby="voice-setup-title">
      <div class="voice-setup-head">
        <div>
          <div class="eyebrow">OPTIONAL LOCAL CAPABILITY</div>
          <h2 id="voice-setup-title">Connect a voice sidecar</h2>
        </div>
        <button class="voice-setup-close" on:click={() => voiceSetupOpen = false} aria-label="Close">×</button>
      </div>
      <p>Speech runs separately from your agent model, so this works with Ollama, Google, NVIDIA, Anthropic, OpenAI, and future providers.</p>

	  <label>Local voice recipe
		<select value={voiceSetupRecipe} on:change={(event) => selectVoiceRecipe(event.currentTarget.value)}>
		  <option value="mlx-kokoro">MLX Whisper + Kokoro — Apple Silicon</option>
		  <option value="whisper-kokoro">whisper.cpp + Kokoro — portable</option>
		  <option value="custom">Custom HTTP sidecar — advanced</option>
		</select>
	  </label>

      <div class="voice-recipes" role="radiogroup" aria-label="Voice sidecar recipe">
		<button type="button" class="voice-recipe" class:selected={voiceSetupRecipe === 'mlx-kokoro'}
				role="radio" aria-checked={voiceSetupRecipe === 'mlx-kokoro'} on:click={() => selectVoiceRecipe('mlx-kokoro')}>
		  <strong>MLX Whisper + Kokoro</strong><span>Apple Silicon</span>
		  <small>Native Metal transcription and natural local speech.</small>
		</button>
        <button type="button" class="voice-recipe" class:selected={voiceSetupRecipe === 'whisper-kokoro'}
                role="radio" aria-checked={voiceSetupRecipe === 'whisper-kokoro'} on:click={() => selectVoiceRecipe('whisper-kokoro')}>
		  <strong>whisper.cpp + Kokoro</strong><span>Portable</span>
		  <small>Uses your whisper-cli binary and ggml model.</small>
        </button>
        <button type="button" class="voice-recipe" class:selected={voiceSetupRecipe === 'custom'}
                role="radio" aria-checked={voiceSetupRecipe === 'custom'} on:click={() => selectVoiceRecipe('custom')}>
          <strong>Custom HTTP sidecar</strong><span>Advanced</span>
          <small>Implement the four-endpoint Soulacy voice contract.</small>
        </button>
      </div>
	  <p class="voice-recipe-detail">{selectedVoiceRecipe.detail}</p>
	  {#if selectedVoiceRecipe.install}
		<div class="voice-install-steps">
		  <strong>Enable local voice from Terminal</strong>
		  <code>{selectedVoiceRecipe.install}</code>
		  <code>{selectedVoiceRecipe.start}</code>
		  <small>The first command installs only missing components, configures Soulacy, and starts a background user service. Restart Soulacy, then run the test command and refresh Chat.</small>
		</div>
	  {/if}

      <label>Sidecar URL
        <input bind:value={voiceSetupURL} placeholder="http://127.0.0.1:8081" />
      </label>
      <label>Default voice <small>(optional)</small>
        <input bind:value={voiceSetupName} placeholder="af_heart" />
      </label>
      <div class="voice-contract"><code>GET /health</code><code>GET /capabilities</code><code>POST /transcribe</code><code>POST /synthesize</code></div>
      <p class="voice-local-note">For safety, GUI setup accepts loopback sidecars. Remote endpoints require an explicit <code>allow_remote</code> setting in config.yaml.</p>
      {#if voiceSetupError}<div class="voice-setup-message err">{voiceSetupError}</div>{/if}
      {#if voiceSetupSaved}<div class="voice-setup-message ok">Saved. Restart the gateway, then return here and click the microphone to test.</div>{/if}
      <div class="voice-setup-actions">
        <span>CLI: <code>sy voice providers</code></span>
        <button class="btn-secondary" on:click={() => voiceSetupOpen = false}>Cancel</button>
        <button class="btn-primary" on:click={saveVoiceSidecar} disabled={voiceSetupBusy || !voiceSetupURL.trim()}>{voiceSetupBusy ? 'Saving…' : 'Save sidecar'}</button>
      </div>
    </div>
  </div>
{/if}

{#if paletteOpen}
  <div class="palette-backdrop" role="button" tabindex="0"
       on:click|self={() => paletteOpen = false}
       on:keydown={(e) => e.key === 'Escape' && (paletteOpen = false)}>
    <div class="palette">
      <input class="palette-input" placeholder="Type a command…" use:focusOnMount
             bind:value={paletteQuery} on:keydown={paletteKeydown} on:input={() => paletteIndex = 0} />
      <div class="palette-list">
        {#each paletteFiltered as c, i}
          <button class="palette-item" class:active={i === paletteIndex}
                  on:mouseenter={() => paletteIndex = i} on:click={() => runPaletteItem(c)}>{c.label}</button>
        {:else}
          <div class="palette-empty">No matching commands</div>
        {/each}
      </div>
      <div class="palette-hint">↑↓ to move · Enter to run · Esc to close</div>
    </div>
  </div>
{/if}

{#if promptsOpen}
  <div class="palette-backdrop" role="button" tabindex="0"
       on:click|self={() => promptsOpen = false}
       on:keydown={(e) => e.key === 'Escape' && (promptsOpen = false)}>
    <div class="palette">
      <div class="prompts-head">
        <span>Saved prompts</span>
        <button class="prompts-save" on:click={saveCurrentPrompt} disabled={!input.trim()}>Save current input</button>
      </div>
      <div class="palette-list">
        {#each savedPrompts as p}
          <div class="prompt-row">
            <button class="prompt-use" on:click={() => usePrompt(p)} title="Insert">{p.title}</button>
            <button class="prompt-del" on:click={() => deletePrompt(p)} title="Delete">×</button>
          </div>
        {:else}
          <div class="palette-empty">No saved prompts yet. Type something, then “Save current input”.</div>
        {/each}
      </div>
    </div>
  </div>
{/if}

{#if confirmRequest}
  <div class="confirm-modal-backdrop">
    <div class="confirm-modal">
      <div class="confirm-head">
        <h2>Action Required</h2>
        <p>The agent wants to use the tool <strong>{confirmRequest.tool}</strong>.</p>
      </div>

      <div class="confirm-content">
        {#if confirmRequest.reason}
          <div class="reason-box">
            <strong>Reason:</strong> {confirmRequest.reason}
          </div>
        {/if}

        {#if confirmExplain && confirmExplain.summary}
          <div class="explain-box">
            <strong>What this does:</strong>
            <p class="explain-summary">{confirmExplain.summary}</p>
            {#if confirmExplain.steps && confirmExplain.steps.length}
              <ul class="explain-steps">
                {#each confirmExplain.steps as step}
                  <li>{step}</li>
                {/each}
              </ul>
            {/if}
            {#if confirmExplain.timeout}
              <p class="explain-meta">{confirmExplain.timeout}</p>
            {/if}
          </div>
        {/if}

        <div class="args-box">
          <strong>Arguments:</strong>
          <pre>{JSON.stringify(confirmRequest.args, null, 2)}</pre>
        </div>
      </div>

      <div class="confirm-actions">
        <button class="btn btn-danger" on:click={() => resolveConfirm(false)}>Deny</button>
        <button class="btn btn-primary" on:click={() => resolveConfirm(true)}>Approve</button>
      </div>
    </div>
  </div>
{/if}

{#if voiceSessionOpen}
  <div class="voice-session" role="dialog" aria-modal="true" aria-label="Voice conversation">
    <header class="voice-session-head">
      <div class="voice-session-brand"><span class="voice-brand-mark">S</span><strong>Soulacy Voice</strong></div>
      <div class="voice-session-agent"><span class="agent-presence"></span>{activeThread?.agentId ? agentName(activeThread.agentId) : 'Agent'}</div>
      <button class="voice-minimize" on:click={() => voiceSessionOpen = false} title="Return to text chat" aria-label="Minimize voice session">—</button>
    </header>
	  <div class="voice-session-stage">
		<span class="voice-session-status">{voiceSessionStatus}</span>
		{#if voiceAcceleration}<small class="voice-session-backend">{voiceAcceleration}</small>{/if}
		<h2>{voiceSessionHeadline}</h2>
      {#if voiceUserTranscript && voiceState !== 'live'}
        <p class="voice-session-transcript">“{voiceUserTranscript}”</p>
      {:else}
        <p class="voice-session-transcript">Speak naturally. Your transcript and the answer will also appear in Chat.</p>
      {/if}
      <button class="voice-orb" class:active={voiceState === 'live' || voicePlaybackState === 'playing'} on:click={voiceClick} disabled={sidecarProcessing && voicePlaybackState === 'idle'} aria-label={voiceState === 'live' ? 'Finish speaking' : 'Start speaking'}>
        <span></span><span></span><span></span><span></span><span></span>
      </button>
      <div class="voice-session-controls">
        <button class="voice-round" class:on={voiceMuted} on:click={toggleVoiceMute} title={voiceMuted ? 'Unmute voice' : 'Mute voice'} aria-label={voiceMuted ? 'Unmute voice' : 'Mute voice'}>{voiceMuted ? '🔇' : '🔊'}</button>
        <button class="voice-end" on:click={endVoiceSession}>☎ <span>END SESSION</span></button>
        {#if voicePlaybackState === 'playing'}
          <button class="voice-round" on:click={pauseVoiceResponse} title="Pause response" aria-label="Pause response">Ⅱ</button>
        {:else if voicePlaybackState === 'paused'}
          <button class="voice-round" on:click={resumeVoiceResponse} title="Resume response" aria-label="Resume response">▶</button>
        {:else if voicePlaybackState === 'generating'}
          <button class="voice-round" on:click={stopVoiceResponse} title="Stop preparing response" aria-label="Stop preparing response">■</button>
        {:else}
          <button class="voice-round" disabled title="Voice controls" aria-label="Voice controls">≛</button>
        {/if}
      </div>
      {#if voicePlaybackState === 'playing' || voicePlaybackState === 'paused'}
        <button class="voice-stop-text" on:click={stopVoiceResponse}>Stop this response</button>
      {/if}
    </div>
  </div>
{/if}

<div class="page modern-chat">
  <div class="page-header">
    <div class="chat-brand">
      <button class="chat-list-toggle" on:click={toggleChatList} title={chatListHidden ? 'Show conversations' : 'Hide conversations'} aria-pressed={chatListHidden}>{chatListHidden ? '☰' : '‹'}</button>
      <div><h1>Soulacy Chat</h1><span>Work with your agents</span></div>
    </div>
    <div class="controls primary-controls">
      {#if activeThread}<RunMetrics sessionId={activeThread.sessionId} agentId={activeThread.agentId} refreshKey={metricsRefresh} />{/if}
      <label class="agent-picker"><span class="agent-presence"></span>
        <select value={activeThread?.agentId || ''} on:change={(e) => setActiveAgent(e.currentTarget.value)} disabled={!agents.length} aria-label="Active agent">
          {#if !agents.length}<option value="">No enabled agents</option>{:else}{#each agents as a}<option value={a.id}>{a.name || a.id}</option>{/each}{/if}
        </select>
      </label>
      <button class="top-icon top-search" class:active={historySearchOpen} on:click={() => historySearchOpen = !historySearchOpen} title="Search conversations" aria-label="Search conversations">⌘ K</button>
      <button class="voice-btn {voiceState}" on:click={() => { if (voiceState === 'live' || sidecarProcessing || voicePlaybackState !== 'idle') voiceSessionOpen = true; else voiceClick() }} title={voiceHint(voiceState, voiceDetail)} aria-label="Open voice conversation">🎤</button>
      <button class="new-chat-btn" on:click={() => startThread()} disabled={!agents.length}>＋ New chat</button>
      <div class="header-tour"><TourButton /></div>
      <div class="chat-more-wrap">
        <button class="top-icon" class:active={chatMoreOpen} on:click={() => chatMoreOpen = !chatMoreOpen} title="More chat actions" aria-label="More chat actions">•••</button>
        {#if chatMoreOpen}
          <div class="chat-more-menu">
            {#if chatStatus}<button on:click={() => { chatStatusOpen = !chatStatusOpen; chatMoreOpen = false }}>{chatStatus.score || 0}% Chat readiness</button>{/if}
            <button on:click={() => { controlsOpen = !controlsOpen; chatMoreOpen = false }}>Model &amp; generation controls</button>
            <button on:click={() => { artifactPanelOpen = !artifactPanelOpen; if (artifactPanelOpen && activeThread) loadArtifacts(activeThread.id, activeThread.agentId, activeThread.sessionId); chatMoreOpen = false }} disabled={!activeThread?.agentId}>Artifacts {currentArtifacts.length ? `(${currentArtifacts.length})` : ''}</button>
            <button on:click={() => { exportThreadMarkdown(); chatMoreOpen = false }} disabled={!activeThread?.messages?.length}>Export Markdown</button>
            <button on:click={() => { exportThreadJSON(); chatMoreOpen = false }} disabled={!activeThread?.messages?.length}>Export JSON</button>
            <button on:click={() => { shareThread(); chatMoreOpen = false }} disabled={!activeThread?.messages?.length || shareBusy}>{shareBusy ? 'Sharing…' : 'Share conversation'}</button>
            <button class="danger" on:click={() => { clearChat(); chatMoreOpen = false }}>Clear conversation</button>
          </div>
        {/if}
      </div>
    </div>
  </div>

  {#if error}
    <div class="banner err">⚠ {error}</div>
  {/if}

  {#if chatStatusOpen && chatStatus}
    <div class="chat-status-panel" transition:slide|local={{ duration: 160 }}>
      <div class="chat-status-head">
        <div>
          <strong>Chat Experience</strong>
          <span>{chatStatus.ready}/{chatStatus.total} ready · {chatStatus.chat_agents} agent(s) · {chatStatus.providers} provider(s)</span>
        </div>
        <button class="mini-link" on:click={loadChatStatus}>Re-check</button>
      </div>
      <div class="chat-status-grid">
        {#each (chatStatus.checks || []) as check}
          <div class="chat-check {check.status}">
            <span>{check.label}</span>
            <strong>{check.status}</strong>
            <p>{check.detail}</p>
          </div>
        {/each}
      </div>
      {#if chatStatus.next_actions?.length}
        <div class="chat-next">
          <strong>Next</strong>
          <span>{chatStatus.next_actions[0]}</span>
        </div>
      {/if}
    </div>
  {/if}

  {#if controlsOpen}
    <div class="controls-panel" transition:slide|local={{ duration: 160 }}>
      <div class="cp-row">
        <label class="cp-field" title={controlTips.provider}><span>Provider</span>
          <select bind:value={controls.provider} on:change={onControlProviderChange} title={controlTips.provider}>
            <option value="">agent default{activeAgentLLM.provider ? ` (${activeAgentLLM.provider})` : ''}</option>
            {#if controls.provider && !enabledProviders.some(p => p.id === controls.provider)}
              <option value={controls.provider}>{controls.provider} (unregistered)</option>
            {/if}
            {#each enabledProviders as p}
              <option value={p.id}>{p.id}</option>
            {/each}
          </select></label>
        <label class="cp-field" title={controlTips.model}><span>Model</span>
          <select bind:value={controls.model} title={controlTips.model}>
            <option value="">agent default{activeAgentLLM.model ? ` (${activeAgentLLM.model})` : ''}</option>
            {#if controls.model && !chatModelOptions.includes(controls.model)}
              <option value={controls.model}>{controls.model} (custom)</option>
            {/if}
            {#each chatModelOptions as m (m)}
              <option value={m}>{m}</option>
            {/each}
          </select>
          <input class="model-custom" bind:value={controls.model} placeholder="custom model override" title="Optional custom model name; leave blank to use the agent default." />
        </label>
        <label class="cp-field" title={controlTips.temperature}><span>Temperature</span>
          <input type="number" step="0.1" min="0" max="2" bind:value={controls.temperature} placeholder="—" title={controlTips.temperature} /></label>
        <label class="cp-field" title={controlTips.topP}><span>Top P</span>
          <input type="number" step="0.05" min="0" max="1" bind:value={controls.topP} placeholder="—" title={controlTips.topP} /></label>
        <label class="cp-field" title={controlTips.maxTokens}><span>Max tokens</span>
          <input type="number" min="1" bind:value={controls.maxTokens} placeholder="—" title={controlTips.maxTokens} /></label>
        <label class="cp-field" title={controlTips.responseFormat}><span>Format</span>
          <select bind:value={controls.responseFormat} title={controlTips.responseFormat}>
            <option value="">default</option>
            <option value="json">json</option>
            <option value="json_schema">json_schema</option>
          </select></label>
        <label class="cp-field" title={controlTips.reasoningEffort}><span>Reasoning</span>
          <select bind:value={controls.reasoningEffort} title={controlTips.reasoningEffort}>
            <option value="">default</option>
            <option value="low">low</option>
            <option value="medium">medium</option>
            <option value="high">high</option>
          </select></label>
        <label class="cp-field" title={controlTips.presencePenalty}><span>Presence</span>
          <input type="number" step="0.1" min="-2" max="2" bind:value={controls.presencePenalty} placeholder="—" title={controlTips.presencePenalty} /></label>
        <label class="cp-field" title={controlTips.frequencyPenalty}><span>Frequency</span>
          <input type="number" step="0.1" min="-2" max="2" bind:value={controls.frequencyPenalty} placeholder="—" title={controlTips.frequencyPenalty} /></label>
        <label class="cp-field" title={controlTips.toolChoice}><span>Tool choice</span>
          <input bind:value={controls.toolChoice} placeholder="auto" title={controlTips.toolChoice} /></label>
        <label class="cp-field" title={controlTips.runBudgetTokens}><span>Run token budget</span>
          <input type="number" min="1" bind:value={controls.runBudgetTokens} placeholder="agent default" title={controlTips.runBudgetTokens} /></label>
        <label class="cp-field" title={controlTips.runBudgetCalls}><span>Run model calls</span>
          <input type="number" min="0" bind:value={controls.runBudgetCalls} placeholder="agent default" title={controlTips.runBudgetCalls} /></label>
        <button class="mini-btn" on:click={() => controls = emptyControls()}>Reset</button>
      </div>
      {#if effectiveProvider || effectiveModel}
        <div class="cp-status {chatModelStatus.kind}" title={chatModelStatus.detail}>
          <strong>{chatModelStatus.label}</strong>
          <span>{chatModelStatus.detail}</span>
          {#if effectiveProvider}
            <button class="mini-link" on:click={refreshControlModels} disabled={!!modelsLoading[effectiveProvider]}>Refresh models</button>
          {/if}
        </div>
      {/if}
      <p class="cp-hint">Blank fields use the agent's own config. Overrides apply to new messages and per-message “Retry”.</p>
    </div>
  {/if}

  <div class="chat-body">
    {#if Object.keys($chatThreads).length > 0 && !chatListHidden}
    <aside class="chat-sidebar">
    <div class="chat-sidebar-head">
      <div><strong>Conversations</strong><span>Continue recent work</span></div>
      <button class="sidebar-new" on:click={() => startThread()} title="New chat" aria-label="New chat">＋</button>
    </div>
    <div class="thread-bar">
      <input class="thread-search" type="search" bind:this={searchEl}
             bind:value={threadSearch} placeholder="Search chats… (⌘K)" aria-label="Search chats" />
      <button class="ghost-btn" class:on={showArchived} on:click={() => showArchived = !showArchived}
              title="Show archived chats">{showArchived ? 'Hide archived' : 'Archived'}</button>
    </div>
    <div class="threads" role="tablist" aria-label="Parallel chats">
      {#each threads as t (t.id)}
        <div class="thread-chip" class:active={t.id === $chatActiveThreadId} class:pinned={t.pinned} class:archived={t.archived}
             role="tab" tabindex="0" aria-selected={t.id === $chatActiveThreadId}
             on:click={() => selectThread(t.id)}
             on:keydown={(e) => { if(e.key === 'Enter') selectThread(t.id) }}
             on:dblclick={() => startRename(t)}
             title="{agentName(t.agentId)} · double-click to rename">
          {#if t.pinned}<span class="thread-pin" aria-hidden="true">📌</span>{/if}
          {#if renamingId === t.id}
            <!-- svelte-ignore a11y-autofocus -->
            <input class="thread-rename" autofocus bind:value={renameText}
                   on:click|stopPropagation
                   on:blur={commitRename}
                   on:keydown={(e) => { if(e.key==='Enter'){e.preventDefault();commitRename()} else if(e.key==='Escape'){renamingId=''} }} />
          {:else}
            <span class="thread-title">{t.title || agentName(t.agentId)}</span>
          {/if}
          <span class="thread-agent">{agentName(t.agentId)}</span>
          {#if t.sending}<span class="thread-dot" aria-label="Running"></span>{/if}
          <button class="thread-act" on:click|stopPropagation={(e) => togglePin(t.id, e)} title={t.pinned ? 'Unpin' : 'Pin'} aria-label="Pin chat">{t.pinned ? '★' : '☆'}</button>
          <button class="thread-act" on:click|stopPropagation={(e) => toggleArchive(t.id, e)} title={t.archived ? 'Unarchive' : 'Archive'} aria-label="Archive chat">🗄</button>
          <button class="thread-close" on:click|stopPropagation={(e) => closeThread(t.id, e)} aria-label="Close tab">✕</button>
        </div>
      {/each}
      {#if threads.length === 0}
        <span class="thread-empty">No chats match “{threadSearch}”.</span>
      {/if}
    </div>
    </aside>
    {/if}

    <div class="chat-main">
  {#if activeThread?.branches?.length > 0}
    <div class="branches" role="tablist" aria-label="Conversation branches">
      {#each activeThread.branches as b (b.sessionId)}
        <button class="branch-chip" class:active={b.sessionId === activeThread.sessionId}
                role="tab" aria-selected={b.sessionId === activeThread.sessionId}
                on:click={() => switchBranch(b.sessionId)}
                title="Switch to {b.label}">
          ⑂ {b.label}
        </button>
      {/each}
    </div>
  {/if}

  <div class="chat-workspace">
  <div class="chat-wrap">
    <!-- Message list -->
    <div class="messages" bind:this={msgListEl}>
      {#if visibleMessages.length === 0}
        <div class="empty">
          {#if activeThread?.agentId}
            <div class="empty-avatar" aria-hidden="true">✦</div>
            <h2 class="empty-title">Chat with {agentName(activeThread.agentId)}</h2>
            <p class="empty-sub">Ask anything, or start with one of these:</p>
            <div class="suggestions">
              {#each promptsFor(activeThread.agentId) as s}
                <button class="suggestion" on:click={() => useSuggestion(s)}>{s}</button>
              {/each}
            </div>
          {:else}
            <div class="empty-avatar" aria-hidden="true">✦</div>
            <h2 class="empty-title">Select an agent to start</h2>
            <p class="empty-sub">Choose an agent from the menu above.</p>
          {/if}
        </div>
      {:else}
        {#each visibleMessages as msg, mi}
          {@const k = msgKey(mi)}
          {@const long = msg.role === 'assistant' && isLongOutput(msg.text) && !expanded[k]}
          {@const resolvedFailure = isHistoricalFailureResolved(visibleMessages, mi)}
          <div class="msg-row" class:user={msg.role==='user'} class:sys={msg.role==='system'} class:resolved={resolvedFailure}>
            <div class="bubble">
              {#if msg.role === 'user' && editingMsg === mi}
                <textarea class="edit-area" bind:value={editText}
                          on:keydown={(e) => { if(e.key==='Enter' && !e.shiftKey){e.preventDefault();commitEdit()} else if(e.key==='Escape'){editingMsg=-1} }}></textarea>
                <div class="edit-actions">
                  <button class="mini-btn" on:click={() => editingMsg = -1}>Cancel</button>
                  <button class="mini-btn primary" on:click={commitEdit}>Save &amp; rerun</button>
                </div>
              {:else}
                {#if msg.role === 'user'}
                  <div class="btext">{msg.text}</div>
                  {#if msg.attachments && msg.attachments.length}
                    <div class="attachment-chips sent">
                      {#each msg.attachments as a (a.id)}
                        {#if isVideoAttachment(a)}
                          {#if attachmentVideoUrls[a.id]}
                            <!-- svelte-ignore a11y-media-has-caption -->
                            <video class="attachment-video" controls preload="metadata" src={attachmentVideoUrls[a.id]}></video>
                          {:else}
                            <button class="attachment-chip" on:click={() => playAttachment(a)} title="Play {a.filename}">
                              <span class="attachment-name">▶ {a.filename}</span>
                              <span class="attachment-size">{artifactSize(a.size_bytes)}</span>
                            </button>
                          {/if}
                        {:else}
                          <button class="attachment-chip" on:click={() => downloadAttachment(a)} title="Download {a.filename}">
                            <span class="attachment-name">{a.filename}</span>
                            <span class="attachment-size">{artifactSize(a.size_bytes)}</span>
                          </button>
                        {/if}
                      {/each}
                    </div>
                  {/if}
                {:else if msg.role === 'system'}
                  <div class="failure-state">
                    <span class="failure-state-icon">{resolvedFailure ? '✓' : '!'}</span>
                    <span>{resolvedFailure ? 'Earlier turn failed · resolved by a later reply' : 'This turn failed'}</span>
                  </div>
                  <details class="failure-detail" open={!resolvedFailure}>
                    <summary>Technical details</summary>
                    <div class="btext">{msg.text}</div>
                  </details>
                {:else}
                  <div class="btext markdown-body" class:clamped={long} use:richRenderer={msg.text}>{@html parseMarkdown(msg.text)}</div>
                  {@const budgetRecovery = budgetRecoveryFor(msg)}
                  {#if budgetRecovery}
                    <div class="budget-recovery" role="status">
                      <div>
                        <strong>This run needs more working room</strong>
                        <span>{budgetRecovery.current.toLocaleString()} tokens was too small. Soulacy recommends {budgetRecovery.recommended.toLocaleString()} for the retry.</span>
                        <small>This changes only the next run and stays below the deployment ceiling of {budgetRecovery.ceiling.toLocaleString()}.</small>
                      </div>
                      <div class="budget-recovery-actions">
                        {#if canAdjustRunBudget}
                          <button class="mini-btn primary" disabled={isSending || budgetRecovery.recommended <= 0} on:click={() => retryWithRecommendedBudget(mi, budgetRecovery)}>Retry with {budgetRecovery.recommended.toLocaleString()}</button>
                          <button class="mini-btn" on:click={() => editRecommendedBudget(budgetRecovery)}>Adjust manually</button>
                          <button class="mini-btn" on:click={editAgentBudgetDefault}>Make it the agent default</button>
                        {:else}
                          <span class="budget-admin-note">Ask a workspace owner or admin to increase this agent's run budget.</span>
                        {/if}
                      </div>
                    </div>
                  {/if}
                  {#if msg.role === 'assistant' && isLongOutput(msg.text)}
                    <button class="show-more" on:click={() => toggleExpand(mi)}>{expanded[k] ? 'Show less ▲' : 'Show more ▼'}</button>
                  {/if}
                  {#if msg.role === 'assistant'}
                    {@const cites = extractSources(msg.text)}
                    {#if cites.length}
                      <div class="citations" aria-label="Sources">
                        <span class="cite-label">Sources</span>
                        {#each cites as c, ci}
                          <a class="cite-chip" href={c.url} target="_blank" rel="noopener noreferrer" title={c.url}>{ci + 1}. {c.host}</a>
                        {/each}
                      </div>
                    {/if}
                  {/if}
                {/if}
              {/if}
              {#if msg.parts && msg.parts.length}
                <div class="msg-parts">
                  {#each msg.parts as part}
                    {#if part.type === 'image'}
                      <img class="part-image" src={partSrc(part)} alt={part.name || 'image'} />
                    {:else if part.type === 'audio'}
                      <audio class="part-audio" controls src={partSrc(part)}></audio>
                    {:else if part.type === 'video'}
                      <!-- svelte-ignore a11y-media-has-caption -->
                      <video class="part-video" controls preload="metadata" src={partSrc(part)}></video>
                    {:else if part.type === 'file'}
                      <a class="part-file" href={partSrc(part)} target="_blank" rel="noopener" download>
                        📎 {part.name || part.url || 'Download file'}
                      </a>
                    {/if}
                  {/each}
                </div>
              {/if}
              {#if msg.thinking && !resolvedFailure}
                <div class="thinking" class:open={msg.thinking.open}>
                  <button class="thinking-head" type="button" on:click={() => toggleThinking(msg.thinking)}>
                    <span class="chev">{msg.thinking.open ? '▾' : '▸'}</span>
                    <span class="thinking-title">Thinking</span>
                    <span class="thinking-meta">{thinkingSummary(msg.thinking)}</span>
                  </button>
                  {#if msg.thinking.open}
                    <div class="thinking-body">
                      {#if msg.thinking.events.length === 0}
                        <div class="thinking-empty">No activity captured for this run.</div>
                      {:else}
                        {#each msg.thinking.events as ev}
                          {#if eventExpandable(ev)}
                            <details class="think-event {eventClass(ev.type, ev)}">
                              <summary class="think-main">
                                <span class="think-type">{ev.type}</span>
                                <span class="think-text">{eventTitle(ev)}</span>
                                {#if eventDuration(ev)}<span class="think-dur">{eventDuration(ev)}</span>{/if}
                              </summary>
                              <pre class="think-full">{fullEventDetail(ev)}</pre>
                              {#if ev.type === 'tool.call'}
                                {@const rr = toolRetry[toolKey(ev)]}
                                <div class="tool-retry">
                                  <button class="tool-retry-btn" on:click|preventDefault={() => retryToolCall(ev)} disabled={rr?.busy}
                                          title="Re-run this tool with the same arguments">
                                    {rr?.busy ? 'Running…' : '↻ Retry tool'}
                                  </button>
                                  {#if rr && !rr.busy}
                                    <div class="tool-retry-result {rr.ok ? 'ok' : 'bad'}">
                                      <span class="trr-head">{rr.ok ? '✓ Succeeded' : '✕ Failed'}{rr.durationMs != null ? ` · ${rr.durationMs}ms` : ''}</span>
                                      <pre class="trr-body">{rr.ok ? rr.output : rr.error}</pre>
                                    </div>
                                  {/if}
                                </div>
                              {/if}
                            </details>
                          {:else}
                            <div class="think-event {eventClass(ev.type, ev)}">
                              <div class="think-main">
                                <span class="think-type">{ev.type}</span>
                                <span class="think-text">{eventTitle(ev)}</span>
                                {#if eventDuration(ev)}<span class="think-dur">{eventDuration(ev)}</span>{/if}
                              </div>
                              {#if eventDetail(ev)}
                                <div class="think-detail">{eventDetail(ev)}</div>
                              {/if}
                            </div>
                          {/if}
                        {/each}
                      {/if}
                    </div>
                  {/if}
                </div>
              {/if}
              <div class="bmeta">
                <span class="btime">{fmtTime(msg.ts)}</span>
                {#if msg.via}
                  <span class="via-badge" title="Routed to this agent via an @mention">via {msg.via}</span>
                {/if}
                {#if msg.metrics && deltaLabel(msg.metrics)}
                  <span class="tok-delta" title={deltaTitle(msg.metrics)}>{deltaLabel(msg.metrics)}</span>
                {/if}
                {#if (msg.role === 'user' || msg.role === 'assistant') && editingMsg !== mi}
                  <div class="msg-actions">
                    <button class="act" on:click={() => copyText(msg.text, 'm'+k)} title="Copy message">{copiedKey === 'm'+k ? '✓' : '⧉'}</button>
                    {#if msg.role === 'user'}
                      <button class="act" on:click={() => startEdit(mi)} disabled={isSending || forking} title="Edit & rerun">✎</button>
                    {:else}
                      {#if msg.runId}
                        <button class="act feedback-act" class:selected={msg.feedback === 1} aria-pressed={msg.feedback === 1}
                          on:click={() => rateResponse(msg, 1)} disabled={!!feedbackBusy[msg.runId]} title="Mark this response helpful">👍</button>
                        <button class="act feedback-act" class:selected={msg.feedback === -1} aria-pressed={msg.feedback === -1}
                          on:click={() => rateResponse(msg, -1)} disabled={!!feedbackBusy[msg.runId]} title="Mark this response unhelpful">👎</button>
                      {/if}
                      <button class="act" on:click={regenerate} disabled={isSending || forking} title="Regenerate">↻</button>
                      <button class="act" on:click={() => retryWithModel(mi)} disabled={isSending || forking} title="Retry with the model selected in Controls">⤺</button>
                      <button class="act" on:click={() => saveToMemory(msg.text, 'm'+k)} title="Save this reply to the agent's memory">{savedKey === 'm'+k ? '✓' : '✚'}</button>
                    {/if}
                    <button class="act" on:click={() => forkAt(mi)} disabled={forking || isSending} title="Fork from here">⑂</button>
                  </div>
                {/if}
              </div>
              {#if msg.feedbackError}<div class="feedback-error">⚠ {msg.feedbackError}</div>{/if}
            </div>
          </div>
        {/each}
        {#if isSending}
          <div class="msg-row">
            <div class="bubble">
              {#if activeThread?.streamText}
                <div class="btext markdown-body streaming" use:richRenderer={activeThread.streamText}>{@html parseMarkdown(activeThread.streamText)}</div>
              {:else}
                <div class="typing"><span></span><span></span><span></span></div>
              {/if}
              {#if activeThread?.thinking}
                <div class="thinking open live">
                  <button class="thinking-head" type="button" on:click={() => toggleThinking(activeThread.thinking)}>
                    <span class="chev">{activeThread.thinking.open ? '▾' : '▸'}</span>
                    <span class="live-dot" aria-hidden="true"></span>
                    <span class="thinking-title">Thinking</span>
                    <span class="thinking-meta">{thinkingSummary(activeThread.thinking)}</span>
                  </button>
                  {#if activeThread.thinking.open}
                    <div class="thinking-body">
                      {#each activeThread.thinking.events as ev (ev)}
                        {#if eventExpandable(ev)}
                          <details class="think-event {eventClass(ev.type, ev)}" transition:slide|local={{ duration: 220 }}>
                            <summary class="think-main">
                              <span class="think-type">{ev.type}</span>
                              <span class="think-text">{eventTitle(ev)}</span>
                              {#if eventDuration(ev)}<span class="think-dur">{eventDuration(ev)}</span>{/if}
                            </summary>
                            <pre class="think-full">{fullEventDetail(ev)}</pre>
                          </details>
                        {:else}
                          <div class="think-event {eventClass(ev.type, ev)}" transition:slide|local={{ duration: 220 }}>
                            <div class="think-main">
                              <span class="think-type">{ev.type}</span>
                              <span class="think-text">{eventTitle(ev)}</span>
                              {#if eventDuration(ev)}<span class="think-dur">{eventDuration(ev)}</span>{/if}
                            </div>
                            {#if eventDetail(ev)}
                              <div class="think-detail">{eventDetail(ev)}</div>
                            {/if}
                          </div>
                        {/if}
                      {/each}
                      <div class="think-live">
                        <span class="think-spinner" aria-hidden="true"></span>
                        <span class="think-live-text">{liveActivity(activeThread.thinking)}</span>
                      </div>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        {/if}
      {/if}
    </div>

    <!-- Input -->
    {#if pendingAttachments.length}
      <div class="pending-attachments">
        {#each pendingAttachments as a (a.id)}
          {#if isVideoAttachment(a) && a._previewUrl}
            <div class="pending-chip pending-video">
              <!-- svelte-ignore a11y-media-has-caption -->
              <video class="pending-video-el" controls preload="metadata" src={a._previewUrl}></video>
              <div class="pending-video-row">
                <span class="attachment-name">{a.filename}</span>
                <button class="pending-remove" on:click={() => removePendingAttachment(a.id)} title="Remove attachment">×</button>
              </div>
            </div>
          {:else}
            <div class="pending-chip">
              <span class="attachment-name">{a.filename}</span>
              <span class="attachment-size">{artifactSize(a.size_bytes)}</span>
              <button class="pending-remove" on:click={() => removePendingAttachment(a.id)} title="Remove attachment">×</button>
            </div>
          {/if}
        {/each}
      </div>
    {/if}
    <div class="input-row">
      <input class="file-input" bind:this={fileInputEl} type="file" multiple on:change={(e) => uploadFiles(e.currentTarget.files)} />
      <button class="attach-btn" on:click={() => fileInputEl?.click()} disabled={isSending || uploadingAttachment || !activeThread?.agentId} title="Attach files">
        {uploadingAttachment ? '…' : '+'}
      </button>
      <button class="attach-btn" on:click={() => promptsOpen = !promptsOpen} title="Saved prompts (⌘K for commands)">≣</button>
      {#if skillOpen}
        <div class="skill-pop" role="listbox" aria-label="Skills">
          <div class="skill-pop-head">Skills · ↑↓ to move, Enter to insert, Esc to dismiss</div>
          {#each skillMatches as sk, i}
            <button
              class="skill-pop-row"
              class:sel={i === skillIndex}
              role="option"
              aria-selected={i === skillIndex}
              on:mouseenter={() => skillIndex = i}
              on:click={() => chooseSkill(sk)}
            >
              <span class="skill-pop-name">/{sk.name}</span>
              {#if sk.description}
                <span class="skill-pop-desc">{sk.description}</span>
              {/if}
            </button>
          {/each}
        </div>
      {/if}
      <textarea
        bind:this={composerEl}
        bind:value={input}
        on:keydown={onKeydown}
        on:input={onComposerInput}
        on:blur={() => setTimeout(() => skillQuery = null, 120)}
        placeholder="Message {activeThread?.agentId ? agentName(activeThread.agentId) : 'the agent'}…  (Enter to send, / for skills)"
        rows="2"
        disabled={isSending || !activeThread?.agentId}
      ></textarea>
      <button class="composer-voice" on:click={() => { if (voiceState === 'live' || sidecarProcessing || voicePlaybackState !== 'idle') voiceSessionOpen = true; else voiceClick() }} disabled={isSending} title="Start a voice conversation" aria-label="Start a voice conversation">🎤</button>
      {#if isSending}
        <button class="send-btn btn-danger" on:click={cancelSend} title="Stop this run">■</button>
      {:else}
        <button class="send-btn btn-primary"
                on:click={send}
                disabled={!activeThread?.agentId || !input.trim()}>
          ↑
        </button>
      {/if}
    </div>
  </div>
  {#if artifactPanelOpen}
    <aside class="artifact-panel" transition:slide|local={{ duration: 160 }} aria-label="Chat artifacts">
      <div class="artifact-head">
        <div>
          <h2>Artifacts</h2>
          <p>{activeThread ? agentName(activeThread.agentId) : ''}</p>
        </div>
        <button class="ghost-icon" on:click={() => activeThread && loadArtifacts(activeThread.id, activeThread.agentId, activeThread.sessionId)} title="Refresh artifacts">↻</button>
      </div>
      {#if artifactLoading[activeThread?.id]}
        <div class="artifact-empty">Loading outputs…</div>
      {:else if artifactError[activeThread?.id]}
        <div class="artifact-error">{artifactError[activeThread.id]}</div>
      {:else if currentArtifacts.length === 0}
        <div class="artifact-empty">No files produced in this chat yet.</div>
      {:else}
        <div class="artifact-list">
          {#each currentArtifacts as a (a.path)}
            <div class="artifact-item">
              <div class="artifact-main">
                <span class="artifact-name" title={a.path}>{a.name || a.path}</span>
                <span class="artifact-meta">{a.tool || 'tool'} · {artifactSize(a.size_bytes)}</span>
              </div>
              <button class="mini-btn" on:click={() => downloadArtifact(a)} title="Download artifact">Download</button>
            </div>
          {/each}
        </div>
      {/if}
    </aside>
  {/if}
  {#if historySearchOpen}
    <aside class="history-panel" transition:slide|local={{ duration: 160 }} aria-label="Chat history search">
      <div class="artifact-head">
        <div>
          <h2>Search</h2>
          <p>{activeThread?.agentId ? agentName(activeThread.agentId) : 'All chats'}</p>
        </div>
      </div>
      <form class="history-search-form" on:submit|preventDefault={searchHistory}>
        <input type="search" bind:value={historyQuery} placeholder="Search old conversations" />
        <button class="mini-btn" disabled={historySearching || !historyQuery.trim()}>{historySearching ? 'Searching...' : 'Go'}</button>
      </form>
      {#if historySearchError}
        <div class="artifact-error">{historySearchError}</div>
      {:else if historySearching}
        <div class="artifact-empty">Searching...</div>
      {:else if historyResults.length === 0}
        <div class="artifact-empty">No matching history yet.</div>
      {:else}
        <div class="history-results">
          {#each historyResults as hit (hit.id)}
            <button class="history-hit" on:click={() => openHistoryHit(hit)}>
              <span class="history-hit-head">{agentName(hit.agent_id)} · {hit.role} · {new Date(hit.created_at).toLocaleString()}</span>
              <span class="history-hit-snippet">{hit.snippet || hit.content}</span>
              <span class="history-hit-session">{hit.session_id}</span>
            </button>
          {/each}
        </div>
      {/if}
    </aside>
  {/if}
  </div>
    </div>
  </div>
</div>

{#if shareLink || shareErr}
  <div class="share-toast" class:err={!!shareErr}>
    {#if shareErr}
      <span>{shareErr}</span>
    {:else}
      <span class="share-toast-title">Shareable link created — copied to clipboard</span>
      <input class="share-toast-link" readonly value={shareLink}
             on:focus={(e) => e.currentTarget.select()} />
      <div class="share-toast-note">Anyone with this link can view a read-only copy of this conversation.</div>
    {/if}
    <button class="share-toast-x" on:click={() => { shareLink = ''; shareErr = '' }} title="Dismiss">×</button>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; height: 100%; }
  .share-toast { position: fixed; right: 20px; bottom: 20px; z-index: 60; max-width: 420px;
    background: #12162a; border: 1px solid #2a2f52; border-radius: 10px; padding: 12px 14px;
    box-shadow: 0 8px 30px rgba(0,0,0,.4); display: flex; flex-direction: column; gap: 6px; }
  .share-toast.err { border-color: rgba(255,90,90,.5); color: #ff9a9a; }
  .share-toast-title { font-size: .82rem; color: #72d9aa; font-weight: 600; }
  .share-toast-link { width: 100%; font-size: .78rem; padding: .4rem .5rem; border-radius: 6px;
    border: 1px solid #2a2f52; background: #0d0f1c; color: #e6e8f5; }
  .share-toast-note { font-size: .7rem; color: #8f96bb; }
  .share-toast-x { position: absolute; top: 6px; right: 8px; background: none; border: none;
    color: #7b82a8; font-size: 1rem; cursor: pointer; line-height: 1; }
  .share-toast-x:hover { color: #c8cadf; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .controls    { display: flex; gap: .75rem; align-items: center; flex-wrap: wrap; min-width: 0; }

  /* ── voice panel (Story 11) ─────────────────────────────────────── */
  .voice-btn {
    background: #1a1f35; border: 1px solid #2a2f4a; border-radius: 6px;
    padding: .4rem .7rem; cursor: pointer; font-size: .95rem; color: #e8eaf6;
  }
  .voice-btn:hover:not(:disabled) { border-color: #4a5380; }
  .voice-btn:disabled { opacity: .45; cursor: not-allowed; }
  .voice-btn.live { border-color: #e05656; background: #2a1520; animation: voicepulse 1.6s infinite; }
  .voice-btn.connecting { border-color: #c9a227; }
  .voice-btn.error { border-color: #e05656; }
	.voice-control { border: 1px solid #394164; border-radius: 7px; padding: .38rem .55rem; background: #161b30; color: #dce0f7; cursor: pointer; font-size: .72rem; }
	.voice-control:hover { border-color: #6973b4; background: #1c2340; }
	.voice-control.stop { border-color: rgba(224,86,86,.55); color: #ffaaaa; }
	.voice-response-status { color: #aeb4d2; font-size: .72rem; }
  @keyframes voicepulse { 0%,100% { box-shadow: 0 0 0 0 rgba(224,86,86,.35); } 50% { box-shadow: 0 0 0 5px rgba(224,86,86,0); } }
  .voice-usage {
    font-family: ui-monospace, monospace; font-size: .75rem; color: #8a91b4;
    white-space: nowrap;
  }
	.voice-setup-backdrop { position: fixed; inset: 0; z-index: 100; display: grid; place-items: center;
	  padding: 1rem; background: rgba(4,6,14,.76); backdrop-filter: blur(4px); }
	.voice-setup { width: min(720px, 96vw); max-height: 90vh; overflow: auto; padding: 1.25rem;
	  border: 1px solid #353b68; border-radius: 14px; background: #111526; box-shadow: 0 24px 80px rgba(0,0,0,.55);
	  display: flex; flex-direction: column; gap: .9rem; }
	.voice-setup h2 { margin: .15rem 0 0; font-size: 1.2rem; }
	.voice-setup p { margin: 0; color: #aeb4d2; font-size: .82rem; line-height: 1.5; }
	.voice-setup-head, .voice-setup-actions { display: flex; align-items: center; gap: .7rem; justify-content: space-between; }
	.voice-setup-close { border: 0; background: none; color: #9ca3c7; font-size: 1.5rem; cursor: pointer; }
	.voice-setup .eyebrow { color: #7f86ff; font-size: .63rem; font-weight: 700; letter-spacing: .12em; }
	.voice-recipes { display: grid; grid-template-columns: repeat(3, 1fr); gap: .65rem; }
	.voice-recipe { display: flex; flex-direction: column; align-items: flex-start; text-align: left; gap: .35rem;
	  min-height: 100px; padding: .75rem; color: #dfe2f4; cursor: pointer; font-family: inherit;
	  border: 1px solid #292e50; border-radius: 9px; background: #171b30; }
	.voice-recipe:hover { border-color: #4d5689; background: #1b2038; }
	.voice-recipe:focus-visible { outline: 2px solid #8a90ff; outline-offset: 2px; }
	.voice-recipe.selected { border-color: #777fff; background: #191c3a; box-shadow: inset 0 0 0 1px #777fff; }
	.voice-recipe.selected::after { content: '✓ Selected'; margin-top: auto; color: #91e2c1; font-size: .66rem; font-weight: 700; }
	.voice-recipe span { color: #8d94ff; font-size: .65rem; text-transform: uppercase; letter-spacing: .06em; }
	.voice-recipe small, .voice-setup label small { color: #8f96b8; line-height: 1.35; }
	.voice-setup .voice-recipe-detail { padding: .55rem .65rem; border-left: 2px solid #626ae0; background: #15192d; color: #bbc1df; }
	.voice-setup label { display: flex; flex-direction: column; gap: .35rem; color: #d8dbef; font-size: .78rem; }
	.voice-setup input, .voice-setup select { padding: .62rem .7rem; border: 1px solid #303656; border-radius: 7px; background: #0d1020; color: #f0f1fb; }
	.voice-install-steps { display: grid; gap: .4rem; padding: .7rem; border: 1px solid #303656; border-radius: 8px; background: #101426; }
	.voice-install-steps strong { color: #e8eaff; font-size: .76rem; }
	.voice-install-steps code { display: block; overflow-x: auto; padding: .42rem .5rem; border-radius: 5px; background: #080b16; color: #91e2c1; user-select: all; }
	.voice-install-steps small { color: #8f96b8; }
	.voice-contract { display: flex; flex-wrap: wrap; gap: .45rem; }
	.voice-contract code { padding: .25rem .4rem; border-radius: 5px; background: #0b0e1b; color: #87dcc1; }
	.voice-setup-message { padding: .6rem .7rem; border-radius: 7px; font-size: .78rem; }
	.voice-setup-message.ok { color: #8be2b7; background: rgba(60,190,130,.1); border: 1px solid rgba(60,190,130,.3); }
	.voice-setup-actions { padding-top: .25rem; color: #8f96b8; font-size: .72rem; }
	@media (max-width: 760px) { .voice-recipes { grid-template-columns: 1fr; } .voice-setup-actions { flex-wrap: wrap; } }
  .banner      { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err         { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  /* Two-column chat body: chat-list sub-menu column + conversation. */
  .chat-body { flex: 1; min-height: 0; display: flex; gap: 0; }
  .chat-sidebar {
    flex: 0 0 264px; min-width: 0;
    display: flex; flex-direction: column; gap: .6rem;
    border-right: 1px solid #1a1e36;
    padding-right: 1rem; margin-right: 1rem;
    overflow: hidden;
  }
  .chat-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }

  .threads {
    display: flex;
    flex-direction: column;
    gap: .5rem;
    overflow-y: auto;
    flex: 1;
    padding-right: .15rem;
  }
  .thread-chip {
    width: 100%;
    min-height: 42px;
    display: grid;
    grid-template-columns: minmax(0, 1fr) 10px 24px;
    grid-template-rows: auto auto;
    column-gap: .45rem;
    align-items: center;
    padding: .45rem .45rem .45rem .65rem;
    background: #171a2c;
    border: 1px solid #262b48;
    border-radius: 8px;
    color: #c8cadf;
    text-align: left;
    cursor: pointer;
  }
  .thread-chip:hover:not(.active) { background: #1c2036; border-color: #343a5f; }
  .thread-chip.active {
    background: rgba(108, 99, 255, .14);
    border-color: rgba(108, 99, 255, .45);
  }
  .thread-close {
    grid-column: 3;
    grid-row: 1 / span 2;
    background: transparent;
    border: none;
    color: #7f86ab;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 12px;
    width: 20px;
    height: 20px;
    border-radius: 4px;
  }
  .thread-close:hover {
    color: #fff;
    background: rgba(255, 255, 255, 0.1);
  }
  .thread-title,
  .thread-agent {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .thread-title {
    grid-column: 1;
    font-size: .8rem;
    font-weight: 650;
  }
  .thread-agent {
    grid-column: 1;
    color: #7f86ab;
    font-size: .68rem;
  }
  .thread-dot {
    grid-column: 2;
    grid-row: 1 / span 2;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #36d399;
    box-shadow: 0 0 0 3px rgba(54, 211, 153, .12);
  }

  .chat-workspace {
    flex: 1;
    min-height: 0;
    display: flex;
    gap: .75rem;
  }

  .chat-wrap {
    flex: 1; min-height: 0;
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; overflow: hidden;
  }

  .artifact-panel,
  .history-panel {
    flex: 0 0 300px;
    min-width: 0;
    background: #141626;
    border: 1px solid #1a1e36;
    border-radius: 10px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .artifact-head {
    min-height: 58px;
    padding: .75rem .85rem;
    border-bottom: 1px solid #1a1e36;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: .75rem;
  }
  .artifact-head h2 {
    margin: 0;
    color: #eef0fb;
    font-size: .9rem;
    font-weight: 700;
  }
  .artifact-head p {
    margin: .12rem 0 0;
    color: #7f86ab;
    font-size: .72rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 210px;
  }
  .ghost-icon {
    width: 30px;
    height: 30px;
    display: grid;
    place-items: center;
    border-radius: 7px;
    border: 1px solid #2a2f4a;
    background: #171a2c;
    color: #9aa0c3;
    cursor: pointer;
  }
  .ghost-icon:hover { border-color: rgba(108,99,255,.5); color: #fff; }
  .artifact-list {
    padding: .65rem;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: .5rem;
  }
  .artifact-item {
    min-height: 58px;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: .6rem;
    align-items: center;
    padding: .55rem;
    border-radius: 8px;
    border: 1px solid #252a45;
    background: #171a2c;
  }
  .artifact-main { min-width: 0; display: flex; flex-direction: column; gap: .18rem; }
  .artifact-name {
    min-width: 0;
    color: #e6e8f4;
    font-size: .8rem;
    font-weight: 650;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .artifact-meta {
    color: #7f86ab;
    font-size: .68rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .artifact-empty,
  .artifact-error {
    margin: .75rem;
    padding: .75rem;
    border-radius: 8px;
    color: #8f95ba;
    background: rgba(255,255,255,.03);
    font-size: .78rem;
    line-height: 1.45;
  }
  .artifact-error {
    color: #ff9aa7;
    background: rgba(240,96,96,.08);
    border: 1px solid rgba(240,96,96,.18);
  }
  .history-search-form {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: .45rem;
    padding: .65rem;
    border-bottom: 1px solid #1a1e36;
  }
  .history-search-form input {
    min-width: 0;
    background: #0e1020;
    border: 1px solid #252a45;
    border-radius: 7px;
    color: #e6e8f4;
    font-size: .8rem;
    padding: .48rem .6rem;
  }
  .history-results {
    padding: .65rem;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: .5rem;
  }
  .history-hit {
    text-align: left;
    display: flex;
    flex-direction: column;
    gap: .28rem;
    padding: .6rem;
    border-radius: 8px;
    border: 1px solid #252a45;
    background: #171a2c;
    color: inherit;
    cursor: pointer;
  }
  .history-hit:hover { border-color: rgba(108,99,255,.5); background: #1b1f35; }
  .history-hit-head {
    color: #9da3c0;
    font-size: .68rem;
  }
  .history-hit-snippet {
    color: #e6e8f4;
    font-size: .78rem;
    line-height: 1.45;
    display: -webkit-box;
    -webkit-line-clamp: 4;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .history-hit-session {
    color: #6f769b;
    font-family: ui-monospace, monospace;
    font-size: .66rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .messages {
    flex: 1; overflow-y: auto;
    padding: 1rem; display: flex; flex-direction: column; gap: .75rem;
  }
  .empty { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
           color: #6b7294; text-align: center; line-height: 1.7; gap: .25rem; }

  .msg-row       { display: flex; justify-content: flex-start; }
  .msg-row.user  { justify-content: flex-end; }

  .feedback-act.selected {
    color: #fff;
    background: rgba(109, 94, 252, .35);
    border-color: rgba(139, 126, 255, .75);
  }
  .feedback-error { margin-top: .3rem; color: #ff9aa9; font-size: .72rem; }
  .msg-row.sys   { justify-content: center; }

  .bubble {
    max-width: 66%; padding: .6rem .9rem; border-radius: 12px;
    display: flex; flex-direction: column; gap: .25rem;
    background: #1c1f35; border: 1px solid #2a2f4a;
    border-bottom-left-radius: 3px;
  }
  .user .bubble {
    background: #5b52ef; border-color: transparent;
    color: #fff; border-bottom-left-radius: 12px; border-bottom-right-radius: 3px;
  }
  .sys .bubble  { background: rgba(240,96,96,.1); border-color: rgba(240,96,96,.3); color: #f06060; }
  .sys.resolved .bubble { background: rgba(70,196,151,.08); border-color: rgba(70,196,151,.24); color: #91d9bd; }
  .failure-state { display: flex; align-items: center; gap: .45rem; font-size: .8rem; font-weight: 650; }
  .failure-state-icon { display: inline-flex; width: 1.15rem; height: 1.15rem; align-items: center; justify-content: center; border: 1px solid currentColor; border-radius: 999px; font-size: .68rem; }
  .failure-detail { margin-top: .2rem; color: inherit; opacity: .88; }
  .failure-detail summary { cursor: pointer; font-size: .72rem; user-select: none; }
  .failure-detail .btext { margin-top: .45rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .72rem; }

  .btext { font-size: .88rem; white-space: pre-wrap; word-break: break-word; line-height: 1.5; }
  .msg-parts { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; }
  .palette-backdrop { position: fixed; inset: 0; background: rgba(8,10,20,.6); display: flex; align-items: flex-start; justify-content: center; padding-top: 12vh; z-index: 200; }
  .palette { width: min(560px, 92vw); background: #141626; border: 1px solid #2a2f52; border-radius: 12px; box-shadow: 0 20px 60px rgba(0,0,0,.5); overflow: hidden; }
  .palette-input { width: 100%; box-sizing: border-box; background: #0e1020; color: #e6e9ff; border: 0; border-bottom: 1px solid #1a1e36; padding: .8rem 1rem; font-size: .95rem; outline: none; }
  .palette-list { max-height: 46vh; overflow: auto; padding: .3rem; }
  .palette-item { display: block; width: 100%; text-align: left; background: transparent; color: #c5c9e8; border: 0; border-radius: 7px; padding: .5rem .7rem; font-size: .84rem; cursor: pointer; }
  .palette-item.active { background: #1e2340; color: #fff; }
  .palette-empty { color: #6b7294; font-size: .82rem; padding: .8rem 1rem; }
  .palette-hint { border-top: 1px solid #1a1e36; padding: .45rem 1rem; font-size: .68rem; color: #6b7294; }
  .prompts-head { display: flex; align-items: center; justify-content: space-between; padding: .7rem 1rem; border-bottom: 1px solid #1a1e36; font-size: .85rem; font-weight: 650; color: #c5c9e8; }
  .prompts-save { background: #1e2340; color: #b9bcf0; border: 1px solid #2a2f52; border-radius: 7px; padding: .3rem .6rem; font-size: .74rem; cursor: pointer; }
  .prompts-save:disabled { opacity: .5; cursor: default; }
  .prompt-row { display: flex; align-items: center; gap: .4rem; }
  .prompt-use { flex: 1; text-align: left; background: transparent; color: #c5c9e8; border: 0; border-radius: 7px; padding: .5rem .7rem; font-size: .82rem; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .prompt-use:hover { background: #1e2340; }
  .prompt-del { background: transparent; border: 0; color: #6b7294; font-size: 1rem; cursor: pointer; padding: 0 .5rem; }
  .prompt-del:hover { color: #f06060; }
  .citations { margin-top: 8px; display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
  .cite-label { font-size: .64rem; text-transform: uppercase; letter-spacing: .05em; color: #6b7294; font-weight: 700; }
  .cite-chip { font-size: .7rem; color: #9aa0c8; background: #171a2e; border: 1px solid #232847; border-radius: 999px; padding: .1rem .5rem; text-decoration: none; max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cite-chip:hover { color: #c5c9e8; border-color: #3a406a; }
  .part-image { max-width: 100%; max-height: 360px; border-radius: 8px; align-self: flex-start; }
  .part-audio { width: 100%; max-width: 420px; }
  .part-video { width: 100%; max-width: 480px; max-height: 360px; border-radius: 8px; background: #000; align-self: flex-start; }
  .attachment-video { width: 100%; max-width: 360px; max-height: 300px; border-radius: 8px; background: #000; }
  .pending-chip.pending-video { display: flex; flex-direction: column; gap: 4px; padding: 4px; }
  .pending-video-el { width: 220px; max-width: 100%; max-height: 160px; border-radius: 6px; background: #000; }
  .pending-video-row { display: flex; align-items: center; gap: 6px; }
  .part-file { font-size: .85rem; color: var(--accent, #6c8cff); text-decoration: none; }
  .part-file:hover { text-decoration: underline; }
  .btime { font-size: .68rem; opacity: .55; align-self: flex-end; }

  .thinking {
    margin-top: .45rem;
    border: 1px solid rgba(108, 99, 255, .26);
    background: rgba(10, 12, 24, .34);
    border-radius: 8px;
    overflow: hidden;
  }
  .thinking-head {
    width: 100%;
    min-height: 32px;
    padding: .35rem .5rem;
    display: grid;
    grid-template-columns: 16px auto 1fr;
    gap: .35rem;
    align-items: center;
    border: 0;
    color: #d6d8ef;
    background: transparent;
    cursor: pointer;
    text-align: left;
  }
  .thinking-head:hover { background: rgba(255,255,255,.04); }
  .chev { color: #8b85ff; font-size: .8rem; line-height: 1; }
  .thinking-title { font-size: .76rem; font-weight: 650; }
  .thinking-meta {
    min-width: 0;
    color: #8f95ba;
    font-size: .72rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    justify-self: end;
  }
  .thinking-body {
    padding: .35rem .45rem .45rem;
    display: flex;
    flex-direction: column;
    gap: .32rem;
    border-top: 1px solid rgba(108, 99, 255, .18);
  }
  .thinking-empty {
    color: #8f95ba;
    font-size: .75rem;
    padding: .25rem .15rem;
  }
  .think-event {
    padding: .38rem .45rem;
    border-radius: 6px;
    background: rgba(255,255,255,.035);
    border-left: 2px solid #6b7294;
  }
  .think-event.llm  { border-left-color: #8b85ff; }
  .think-event.tool { border-left-color: #f0a060; }
  .think-event.err  { border-left-color: #f06060; }
  .think-event.recovery {
    border-left-color: #f0c060;
    background: rgba(240, 192, 96, .07);
  }
  .think-event.degraded {
    border-left-color: #f09060;
    background: rgba(240, 144, 96, .08);
  }
  .think-main {
    display: flex;
    gap: .45rem;
    align-items: baseline;
    min-width: 0;
  }
  .think-type {
    flex: 0 0 auto;
    color: #8f95ba;
    font-size: .66rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  .think-text {
    min-width: 0;
    color: #e4e6f7;
    font-size: .75rem;
    line-height: 1.35;
    word-break: break-word;
  }
  .think-detail {
    margin-top: .18rem;
    color: #aeb3d4;
    font-size: .72rem;
    line-height: 1.35;
    white-space: pre-wrap;
    word-break: break-word;
  }
  /* expandable structured event (#4) */
  details.think-event > summary {
    cursor: pointer;
    list-style: none;
  }
  details.think-event > summary::-webkit-details-marker { display: none; }
  details.think-event > summary::before {
    content: '▸';
    flex: 0 0 auto;
    color: #8f95ba;
    font-size: .6rem;
    transition: transform .12s;
  }
  details.think-event[open] > summary::before { transform: rotate(90deg); }
  .think-dur {
    flex: 0 0 auto;
    margin-left: auto;
    color: #8f95ba;
    font-size: .66rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  .think-full {
    margin: .35rem 0 .1rem;
    padding: .45rem .55rem;
    max-height: 280px;
    overflow: auto;
    border-radius: 6px;
    background: rgba(0,0,0,.28);
    color: #c9cef0;
    font-size: .7rem;
    line-height: 1.4;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    white-space: pre-wrap;
    word-break: break-word;
  }

  /* ── Per-tool-call retry ─────────────────────────────────────────────── */
  .tool-retry { margin: .35rem 0 .1rem; display: flex; flex-direction: column; gap: .35rem; }
  .tool-retry-btn { align-self: flex-start; background: rgba(139,133,255,.12); border: 1px solid rgba(139,133,255,.4);
    color: #b9b4ff; font-size: .68rem; padding: .2rem .6rem; border-radius: 6px; cursor: pointer; }
  .tool-retry-btn:hover:not(:disabled) { background: rgba(139,133,255,.2); }
  .tool-retry-btn:disabled { opacity: .6; cursor: default; }
  .tool-retry-result { border-radius: 6px; padding: .4rem .55rem; border: 1px solid #2a2f52; }
  .tool-retry-result.ok { border-color: rgba(96,200,120,.4); background: rgba(96,200,120,.07); }
  .tool-retry-result.bad { border-color: rgba(240,96,96,.4); background: rgba(240,96,96,.07); }
  .trr-head { font-size: .68rem; font-weight: 600; }
  .tool-retry-result.ok .trr-head { color: #60c878; }
  .tool-retry-result.bad .trr-head { color: #f06060; }
  .trr-body { margin: .3rem 0 0; max-height: 220px; overflow: auto; font-size: .68rem; line-height: 1.4;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; white-space: pre-wrap; word-break: break-word; color: #c9cef0; }

  /* ── Live in-progress indicator ──────────────────────────────────────── */
  .thinking.live { border-color: rgba(139, 133, 255, .4); }
  .thinking.live .thinking-head { grid-template-columns: 16px 8px auto 1fr; }
  .live-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #8b85ff;
    animation: live-pulse 1.5s ease-out infinite;
  }
  @keyframes live-pulse {
    0%   { box-shadow: 0 0 0 0 rgba(139, 133, 255, .55); }
    70%  { box-shadow: 0 0 0 6px rgba(139, 133, 255, 0); }
    100% { box-shadow: 0 0 0 0 rgba(139, 133, 255, 0); }
  }
  .think-live {
    display: flex;
    align-items: center;
    gap: .5rem;
    padding: .4rem .45rem;
    border-radius: 6px;
    background: linear-gradient(90deg, rgba(139, 133, 255, .13), rgba(139, 133, 255, .03));
    border-left: 2px solid #8b85ff;
  }
  .think-spinner {
    width: 12px;
    height: 12px;
    flex: 0 0 auto;
    border-radius: 50%;
    border: 2px solid rgba(139, 133, 255, .28);
    border-top-color: #8b85ff;
    animation: think-spin .7s linear infinite;
  }
  @keyframes think-spin { to { transform: rotate(360deg); } }
  .think-live-text {
    color: #cfd2ee;
    font-size: .75rem;
    line-height: 1.3;
    animation: live-breathe 1.9s ease-in-out infinite;
  }
  @keyframes live-breathe { 0%, 100% { opacity: .72; } 50% { opacity: 1; } }

  @media (prefers-reduced-motion: reduce) {
    .live-dot, .think-spinner, .think-live-text { animation: none; }
    .think-spinner { border-top-color: #8b85ff; }
  }

  @media (max-width: 980px) {
    .chat-workspace { flex-direction: column; }
    .artifact-panel { flex: 0 0 auto; max-height: 260px; }
  }

  /* Typing indicator */
  .typing { display: flex; gap: 4px; align-items: center; height: 1.1rem; }
  .typing span {
    width: 6px; height: 6px; border-radius: 50%;
    background: #6b7294; animation: bounce 1.1s infinite;
  }
  .typing span:nth-child(2) { animation-delay: .18s; }
  .typing span:nth-child(3) { animation-delay: .36s; }
  @keyframes bounce {
    0%, 80%, 100% { transform: scale(.65); opacity: .4; }
    40%           { transform: scale(1);   opacity: 1;   }
  }

  .input-row {
    display: flex; gap: .65rem; align-items: flex-end;
    padding: .7rem; border-top: 1px solid #1a1e36; flex-shrink: 0;
    position: relative; /* anchors the "/" skill popup */
  }
  .input-row textarea { flex: 1; resize: none; }

  /* Inline "/" skill picker. Sits ABOVE the composer: the composer is at the
     bottom of the viewport, so a dropdown below it would be off-screen. */
  .skill-pop {
    position: absolute; bottom: calc(100% - .2rem); left: .7rem; right: .7rem;
    max-height: 260px; overflow-y: auto; z-index: 20;
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 10px;
    box-shadow: 0 -8px 28px rgba(0,0,0,.45);
    display: flex; flex-direction: column;
  }
  .skill-pop-head {
    padding: .45rem .7rem; font-size: .7rem; color: #6b7294;
    border-bottom: 1px solid #1a1e36; position: sticky; top: 0; background: #0e1020;
  }
  .skill-pop-row {
    display: flex; flex-direction: column; gap: .15rem; align-items: flex-start;
    width: 100%; text-align: left; background: none; border: none; border-radius: 0;
    padding: .5rem .7rem; cursor: pointer; color: #c8cadf;
  }
  .skill-pop-row.sel { background: rgba(108,99,255,.16); }
  .skill-pop-name { font-size: .84rem; font-weight: 600; color: #a6a0ff; }
  .skill-pop-desc {
    font-size: .74rem; color: #6b7294;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 100%;
  }
  .send-btn { height: 40px; padding: 0 1rem; font-size: 1rem; align-self: flex-end; flex-shrink: 0; }
  .file-input { display: none; }
  .attach-btn {
    width: 40px;
    height: 40px;
    flex: 0 0 40px;
    align-self: flex-end;
    border-radius: 8px;
    border: 1px solid #2a2f4a;
    background: #171a2c;
    color: #d6d8ef;
    font-size: 1.15rem;
    line-height: 1;
    cursor: pointer;
  }
  .attach-btn:hover:not(:disabled) { border-color: rgba(108,99,255,.55); color: #fff; }
  .attach-btn:disabled { opacity: .45; cursor: default; }
  .pending-attachments {
    display: flex;
    flex-wrap: wrap;
    gap: .45rem;
    padding: .55rem .7rem 0;
    border-top: 1px solid #1a1e36;
    flex-shrink: 0;
  }
  .pending-chip,
  .attachment-chip {
    min-width: 0;
    max-width: 260px;
    min-height: 30px;
    display: inline-flex;
    align-items: center;
    gap: .45rem;
    border-radius: 7px;
    border: 1px solid #2a2f4a;
    background: #171a2c;
    color: #d8dbef;
    padding: .25rem .45rem;
    font-size: .75rem;
  }
  .attachment-chip { cursor: pointer; text-align: left; }
  .attachment-chip:hover { border-color: rgba(108,99,255,.5); color: #fff; }
  .attachment-chips {
    display: flex;
    flex-wrap: wrap;
    gap: .4rem;
    margin-top: .35rem;
  }
  .attachment-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .attachment-size {
    flex: 0 0 auto;
    color: #8f95ba;
    font-family: ui-monospace, monospace;
    font-size: .67rem;
  }
  .pending-remove {
    flex: 0 0 20px;
    width: 20px;
    height: 20px;
    border: 0;
    border-radius: 5px;
    background: transparent;
    color: #8f95ba;
    cursor: pointer;
  }
  .pending-remove:hover { background: rgba(255,255,255,.08); color: #fff; }

  /* ── Branching (Story 8) ─────────────────────────────────────────── */
  .branches { display: flex; gap: .4rem; flex-wrap: wrap; padding: 0 0 .15rem; }
  .branch-chip {
    background: #1c1f35; border: 1px solid #2a2f4a; color: #7b82a8;
    font-size: .72rem; font-family: monospace;
    padding: .2rem .65rem; border-radius: 999px;
  }
  .branch-chip:hover:not(.active) { background: #252840; color: #c8cadf; }
  .branch-chip.active {
    background: rgba(108, 99, 255, .15); border-color: rgba(108, 99, 255, .4);
    color: #8b85ff; cursor: default;
  }
  .bubble { position: relative; }
  .fork-btn {
    position: absolute; top: .25rem; right: .35rem;
    background: none; color: #4d5478; font-size: .8rem;
    padding: .05rem .3rem; border-radius: 5px;
    opacity: 0; transition: opacity .12s;
  }
  .bubble:hover .fork-btn { opacity: 1; }
  .fork-btn:hover:not(:disabled) { background: rgba(108, 99, 255, .18); color: #8b85ff; }

  .bmeta { display: flex; align-items: center; gap: .55rem; flex-wrap: wrap; }
  .tok-delta {
    font-size: .68rem; font-family: monospace; color: #5a5f82;
    cursor: help; white-space: nowrap;
  }
  .tok-delta:hover { color: #8b85ff; }
  .via-badge {
    font-size: .62rem; padding: .05rem .35rem; border-radius: 999px;
    background: rgba(139,133,255,.16); color: #b3aeff;
    white-space: nowrap; align-self: center;
  }
  /* live-streaming reply: blinking caret at the end of the generated text */
  .btext.streaming::after {
    content: '▋';
    display: inline-block;
    margin-left: 2px;
    color: #8b85ff;
    animation: caret-blink 1s steps(2, start) infinite;
  }
  @keyframes caret-blink { 50% { opacity: 0; } }

  /* ── Confirm Modal ─────────────────────────────────────────────── */
  .confirm-modal-backdrop {
    position: fixed; top: 0; left: 0; right: 0; bottom: 0;
    background: rgba(10, 12, 24, 0.8); backdrop-filter: blur(4px);
    z-index: 1000; display: flex; align-items: center; justify-content: center;
    padding: 1rem;
    box-sizing: border-box;
  }
  .confirm-modal {
    background: #1c1f35; border: 1px solid #2a2f4a; border-radius: 8px;
    padding: 1.5rem; max-width: 560px; width: min(100%, 560px);
    max-height: calc(100vh - 2rem);
    max-height: min(760px, calc(100dvh - 2rem));
    box-shadow: 0 10px 30px rgba(0,0,0,0.5);
    display: grid;
    grid-template-rows: auto minmax(0, 1fr) auto;
    gap: 1rem;
    overflow: hidden;
  }
  .confirm-modal h2 { margin: 0; color: #fff; font-size: 1.25rem; }
  .confirm-modal p { margin: 0; color: #a1a7c4; font-size: 0.95rem; }
  .confirm-head {
    flex: 0 0 auto;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    min-width: 0;
  }
  .confirm-content {
    min-height: 0;
    max-height: calc(100vh - 13rem);
    max-height: calc(100dvh - 13rem);
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    padding-right: 0.15rem;
  }
  .reason-box, .args-box {
    background: #131526; padding: 0.75rem; border-radius: 6px;
    font-size: 0.85rem; color: #c8cadf; border: 1px solid #1a1e36;
    overflow-wrap: anywhere;
  }
  .args-box pre {
    margin: 0.5rem 0 0 0; white-space: pre-wrap; word-wrap: break-word;
    overflow-wrap: anywhere; max-height: 28vh; max-height: 28dvh; overflow: auto;
    color: #a3d9a5; font-family: monospace; font-size: 0.8rem;
  }
  .explain-box {
    background: #131526; padding: 0.75rem; border-radius: 6px;
    font-size: 0.85rem; color: #c8cadf;
    border: 1px solid #2a2f4a; border-left: 3px solid #6c7bff;
    overflow-wrap: anywhere;
  }
  .explain-box strong { color: #fff; }
  .explain-summary { margin: 0.4rem 0 0 0; color: #c8cadf; font-size: 0.85rem; }
  .explain-steps {
    margin: 0.5rem 0 0 0; padding-left: 1.1rem; display: flex;
    flex-direction: column; gap: 0.2rem;
  }
  .explain-steps li { color: #b7bbd6; font-size: 0.82rem; }
  .explain-meta { margin: 0.5rem 0 0 0; color: #8a90b0; font-size: 0.78rem; font-style: italic; }
  .confirm-actions {
    display: flex; gap: 0.75rem; justify-content: flex-end;
    margin-top: 0.25rem;
    padding-top: 0.75rem;
    border-top: 1px solid rgba(255,255,255,.06);
  }
  .btn-danger { background: rgba(220, 53, 69, 0.2); color: #ff6b81; border: 1px solid rgba(220, 53, 69, 0.4); }
  .btn-danger:hover { background: rgba(220, 53, 69, 0.3); }

  /* ── Modern Chat Workspace ─────────────────────────────────────────── */
  /* Center the conversation in a readable column (Claude/ChatGPT feel). */
  .messages { padding: 1.25rem 0; }
  .msg-row { width: 100%; max-width: 820px; margin: 0 auto; padding: 0 1rem; }
  .bubble { max-width: 100%; }
  .msg-row.user .bubble { max-width: 80%; }
  .msg-row:not(.user):not(.sys) .bubble {
    background: transparent; border: none; padding-left: 0; padding-right: 0;
    border-radius: 0;
  }
  .msg-row:not(.user):not(.sys) .bubble:hover { background: transparent; }
  .btext { font-size: .92rem; line-height: 1.65; }

  /* Rich markdown styling for assistant messages (injected HTML → :global). */
  :global(.btext.markdown-body h1),
  :global(.btext.markdown-body h2),
  :global(.btext.markdown-body h3) {
    margin: 1.1rem 0 .5rem; line-height: 1.3; font-weight: 700; color: #f2f3fb;
  }
  :global(.btext.markdown-body h1) { font-size: 1.3rem; }
  :global(.btext.markdown-body h2) { font-size: 1.12rem; }
  :global(.btext.markdown-body h3) { font-size: 1rem; }
  :global(.btext.markdown-body p) { margin: .5rem 0; }
  :global(.btext.markdown-body ul),
  :global(.btext.markdown-body ol) { margin: .5rem 0; padding-left: 1.35rem; }
  :global(.btext.markdown-body li) { margin: .28rem 0; }
  :global(.btext.markdown-body a) { color: #8b85ff; text-decoration: none; }
  :global(.btext.markdown-body a:hover) { text-decoration: underline; }
  :global(.btext.markdown-body strong) { color: #f2f3fb; font-weight: 700; }
  :global(.btext.markdown-body code) {
    background: rgba(139,133,255,.12); color: #c9cef0;
    padding: .1rem .35rem; border-radius: 5px; font-size: .86em;
  }
  :global(.btext.markdown-body pre) { margin: .6rem 0; }
  :global(.btext.markdown-body pre code) { background: none; padding: 0; }
  /* Tables — the biggest polish win (STEP / TOOL / WHAT HAPPENED). */
  :global(.btext.markdown-body table) {
    width: 100%; border-collapse: collapse; margin: .75rem 0; font-size: .88rem;
  }
  :global(.btext.markdown-body th) {
    text-align: left; padding: .5rem .7rem; color: #8a90b0;
    font-size: .72rem; text-transform: uppercase; letter-spacing: .04em;
    border-bottom: 1px solid #2a2f4a; font-weight: 600;
  }
  :global(.btext.markdown-body td) {
    padding: .6rem .7rem; border-bottom: 1px solid #1e2238; vertical-align: top;
  }
  :global(.btext.markdown-body tr:last-child td) { border-bottom: none; }
  :global(.btext.markdown-body blockquote) {
    margin: .6rem 0; padding: .3rem 0 .3rem .9rem;
    border-left: 3px solid #3a3f68; color: #aeb3d4;
  }

  /* Header control toggles */
  .btn-secondary.on { background: rgba(108,99,255,.18); border-color: rgba(108,99,255,.5); color: #b3adff; }
  .chat-status-pill.ok { border-color: rgba(96, 200, 120, .45); color: #8ee2a2; }
  .chat-status-pill.warn { border-color: rgba(240, 192, 96, .45); color: #f0c060; }
  .chat-status-pill.fail { border-color: rgba(255, 107, 129, .45); color: #ff8c9d; }
  .chat-status-panel {
    background: #171a2c; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: .75rem .9rem; flex-shrink: 0;
  }
  .chat-status-head {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem;
    margin-bottom: .65rem;
  }
  .chat-status-head div { display: flex; flex-direction: column; gap: .18rem; }
  .chat-status-head strong { color: #f2f3fb; font-size: .9rem; }
  .chat-status-head span { color: #8a90b0; font-size: .75rem; }
  .chat-status-grid {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: .55rem;
  }
  .chat-check {
    border: 1px solid #2a2f4a; border-radius: 8px; padding: .55rem .65rem;
    background: rgba(15, 17, 32, .72);
  }
  .chat-check span { color: #cfd2e8; font-size: .78rem; font-weight: 600; }
  .chat-check strong {
    float: right; font-size: .65rem; text-transform: uppercase; letter-spacing: .05em;
  }
  .chat-check p { clear: both; margin: .35rem 0 0; color: #8a90b0; font-size: .72rem; line-height: 1.35; }
  .chat-check.ok { border-color: rgba(96, 200, 120, .3); }
  .chat-check.ok strong { color: #60c878; }
  .chat-check.warn { border-color: rgba(240, 192, 96, .34); }
  .chat-check.warn strong { color: #f0c060; }
  .chat-check.fail { border-color: rgba(255, 107, 129, .38); }
  .chat-check.fail strong { color: #ff6b81; }
  .chat-next {
    margin-top: .65rem; display: flex; gap: .45rem; align-items: baseline;
    font-size: .75rem; color: #aeb3d4;
  }
  .chat-next strong { color: #f0c060; text-transform: uppercase; letter-spacing: .04em; font-size: .66rem; }

  /* Model controls panel */
  .controls-panel {
    background: #171a2c; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: .75rem .9rem; flex-shrink: 0;
  }
  .cp-row { display: flex; flex-wrap: wrap; gap: .6rem; align-items: flex-end; }
  .cp-field { display: flex; flex-direction: column; gap: .2rem; font-size: .7rem; color: #8a90b0; }
  .cp-field span { text-transform: uppercase; letter-spacing: .03em; }
  .cp-field input,
  .cp-field select { width: 150px; padding: .35rem .5rem; border-radius: 7px;
    background: #0f1120; border: 1px solid #2a2f4a; color: #e6e8f4; font-size: .82rem; }
  .cp-field select { cursor: pointer; }
  .cp-field .model-custom { width: 180px; }
  .cp-field input[type=number] { width: 90px; }
  .cp-status {
    margin-top: .65rem;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: .45rem;
    border-radius: 8px;
    padding: .45rem .55rem;
    font-size: .74rem;
    border: 1px solid #2a2f4a;
    background: rgba(15, 17, 32, .72);
    color: #aeb3d4;
  }
  .cp-status strong { font-size: .7rem; text-transform: uppercase; letter-spacing: .04em; }
  .cp-status.ok { border-color: rgba(96, 200, 120, .35); background: rgba(96, 200, 120, .07); }
  .cp-status.ok strong { color: #60c878; }
  .cp-status.warn { border-color: rgba(240, 192, 96, .35); background: rgba(240, 192, 96, .08); }
  .cp-status.warn strong { color: #f0c060; }
  .cp-status.info { border-color: rgba(108, 153, 255, .32); background: rgba(108, 153, 255, .08); }
  .cp-status.info strong { color: #8fb0ff; }
  .mini-link {
    margin-left: auto;
    border: 0;
    background: transparent;
    color: #b3adff;
    font-size: .72rem;
    cursor: pointer;
    padding: .15rem .25rem;
  }
  .mini-link:hover:not(:disabled) { color: #fff; text-decoration: underline; }
  .mini-link:disabled { opacity: .55; cursor: default; }
  .cp-hint { margin: .5rem 0 0; font-size: .72rem; color: #6b7294; }

  /* Thread search bar */
  .thread-bar { display: flex; gap: .5rem; align-items: center; flex-shrink: 0; }
  .thread-search {
    flex: 1; max-width: 320px; padding: .4rem .7rem; border-radius: 8px;
    background: #14172a; border: 1px solid #2a2f4a; color: #e6e8f4; font-size: .82rem;
  }
  .ghost-btn {
    background: transparent; border: 1px solid #2a2f4a; color: #8a90b0;
    padding: .35rem .65rem; border-radius: 8px; font-size: .78rem; cursor: pointer;
  }
  .ghost-btn.on, .ghost-btn:hover { color: #b3adff; border-color: rgba(108,99,255,.5); }
  .thread-empty { font-size: .8rem; color: #6b7294; padding: .25rem .5rem; }
  .thread-chip.pinned { border-color: rgba(108,99,255,.45); }
  .thread-chip.archived { opacity: .55; }
  .thread-pin { font-size: .7rem; }
  .thread-act {
    background: none; border: none; color: #5a608a; font-size: .8rem; cursor: pointer;
    padding: 0 .1rem; opacity: 0; transition: opacity .12s;
  }
  .thread-chip:hover .thread-act, .thread-chip.active .thread-act { opacity: 1; }
  .thread-act:hover { color: #b3adff; }
  .thread-rename {
    background: #0f1120; border: 1px solid rgba(108,99,255,.5); color: #fff;
    border-radius: 5px; padding: .1rem .35rem; font-size: .82rem; width: 130px;
  }

  /* Empty state with suggestions */
  .empty-avatar {
    width: 52px; height: 52px; border-radius: 50%; display: grid; place-items: center;
    font-size: 1.4rem; color: #fff; margin-bottom: .25rem;
    background: linear-gradient(135deg, #6c63ff, #9b6cff);
  }
  .empty-title { font-size: 1.25rem; font-weight: 600; color: #e6e8f4; margin: 0; }
  .empty-sub { font-size: .9rem; color: #8a90b0; margin: 0 0 .5rem; }
  .suggestions { display: flex; flex-wrap: wrap; gap: .5rem; justify-content: center; max-width: 560px; }
  .suggestion {
    background: #171a2c; border: 1px solid #2a2f4a; color: #cfd2e8;
    padding: .55rem .8rem; border-radius: 10px; font-size: .82rem; cursor: pointer;
    transition: border-color .12s, transform .08s; text-align: left;
  }
  .suggestion:hover { border-color: rgba(108,99,255,.55); transform: translateY(-1px); color: #fff; }

  /* Collapsible long output */
  .markdown-body.clamped { max-height: 560px; overflow: hidden;
    -webkit-mask-image: linear-gradient(180deg, #000 85%, transparent); mask-image: linear-gradient(180deg, #000 85%, transparent); }
  .show-more {
    background: none; border: none; color: #8b85ff; font-size: .78rem; cursor: pointer;
    padding: .25rem 0; align-self: flex-start;
  }
  .show-more:hover { text-decoration: underline; }

  /* Per-message action toolbar */
  .msg-actions { display: flex; gap: .15rem; margin-left: auto; opacity: 0; transition: opacity .12s; }
  .msg-row:hover .msg-actions, .bubble:focus-within .msg-actions { opacity: 1; }
  .act {
    background: none; border: none; color: #6b7294; font-size: .82rem; cursor: pointer;
    padding: .15rem .35rem; border-radius: 6px; line-height: 1;
  }
  .act:hover:not(:disabled) { background: rgba(108,99,255,.16); color: #b3adff; }
  .act:disabled { opacity: .35; cursor: default; }

  /* Inline edit of a user message */
  .edit-area {
    width: 100%; min-height: 64px; resize: vertical; border-radius: 8px;
    background: rgba(0,0,0,.18); border: 1px solid rgba(255,255,255,.25); color: #fff;
    padding: .5rem .6rem; font-size: .9rem; font-family: inherit;
  }
  .edit-actions { display: flex; gap: .4rem; justify-content: flex-end; margin-top: .4rem; }
  .mini-btn {
    background: #14172a; border: 1px solid #2a2f4a; color: #cfd2e8;
    padding: .3rem .6rem; border-radius: 7px; font-size: .78rem; cursor: pointer;
  }
  .mini-btn:hover { border-color: rgba(108,99,255,.5); color: #fff; }
  .mini-btn.primary { background: #5b52ef; border-color: transparent; color: #fff; }

  /* Code-block copy button (from richRenderer) */
  :global(.code-copy-btn) {
    position: absolute; top: .4rem; right: .4rem; z-index: 2;
    background: rgba(20,23,42,.85); border: 1px solid #2a2f4a; color: #b7bbd6;
    font-size: .72rem; padding: .2rem .5rem; border-radius: 6px; cursor: pointer;
    opacity: 0; transition: opacity .12s;
  }
  :global(pre:hover .code-copy-btn) { opacity: 1; }
  :global(.code-copy-btn:hover) { color: #fff; border-color: rgba(108,99,255,.5); }

  /* ── 2026 focused chat shell ─────────────────────────────────────── */
  .modern-chat {
    padding: 0; gap: 0; overflow: hidden;
    color: #eef0ff;
    background:
      radial-gradient(circle at 72% 12%, rgba(86, 78, 190, .07), transparent 32%),
      #090f20;
  }
  .modern-chat .page-header {
    min-height: 68px; padding: 0 1.1rem; gap: 1rem;
    background: rgba(10, 16, 35, .96); border-bottom: 1px solid #202943;
  }
  .chat-brand { display: flex; align-items: center; gap: .75rem; min-width: max-content; }
  .chat-brand h1 { margin: 0; font-size: 1rem; letter-spacing: -.01em; }
  .chat-brand span { display: block; margin-top: .12rem; color: #77809f; font-size: .66rem; }
  .chat-list-toggle, .top-icon, .sidebar-new, .voice-minimize {
    display: grid; place-items: center; border: 1px solid transparent; color: #aab1ce;
    background: transparent; cursor: pointer; font-family: inherit;
  }
  .chat-list-toggle { width: 30px; height: 30px; border-radius: 8px; font-size: 1.1rem; }
  .chat-list-toggle:hover, .top-icon:hover, .top-icon.active { color: #fff; background: #19213a; border-color: #2b3657; }
  .primary-controls { justify-content: flex-end; gap: .45rem; flex-wrap: nowrap; }
  .agent-picker {
    min-width: 160px; height: 36px; display: flex; align-items: center; gap: .4rem;
    padding-left: .65rem; border: 1px solid #283250; border-radius: 9px; background: #111a30;
  }
  .agent-picker select { min-width: 0; width: 100%; border: 0; padding: 0 .6rem 0 0; background: transparent; color: #eef0ff; font-weight: 650; }
  .agent-presence { display: inline-block; width: 7px; height: 7px; flex: 0 0 7px; border-radius: 50%; background: #59d7b0; box-shadow: 0 0 0 3px rgba(89,215,176,.1); }
  .top-icon, .modern-chat .voice-btn { width: 36px; height: 36px; padding: 0; border-radius: 9px; }
  .top-icon.top-search { width: 48px; color: #8f98b8; font-family: ui-monospace, monospace; font-size: .68rem; }
  .modern-chat .voice-btn { background: #151d34; border-color: #283250; }
  .new-chat-btn {
    height: 36px; padding: 0 .9rem; border: 1px solid #9d99ff; border-radius: 9px;
    color: #10142a; background: #b7b5ff; font-size: .76rem; font-weight: 750; cursor: pointer;
  }
  .new-chat-btn:hover:not(:disabled) { background: #cbc9ff; }
  .new-chat-btn:disabled { opacity: .45; }
  .header-tour :global(button) { height: 36px; padding-inline: .65rem; }
  .chat-more-wrap { position: relative; }
  .chat-more-menu {
    position: absolute; z-index: 45; top: calc(100% + .5rem); right: 0; width: 230px;
    display: grid; gap: .2rem; padding: .4rem; border: 1px solid #2c3658; border-radius: 11px;
    background: #121a2f; box-shadow: 0 18px 48px rgba(0,0,0,.45);
  }
  .chat-more-menu button { padding: .58rem .65rem; border: 0; border-radius: 7px; background: transparent; color: #cdd2e8; text-align: left; cursor: pointer; }
  .chat-more-menu button:hover:not(:disabled) { color: #fff; background: #202943; }
  .chat-more-menu button.danger { color: #ff9fa9; }
  .chat-more-menu button:disabled { opacity: .38; cursor: default; }

  .modern-chat > .banner, .modern-chat > .chat-status-panel, .modern-chat > .controls-panel { margin: .7rem 1rem 0; }
  .modern-chat .chat-body { background: #090f20; }
  .modern-chat .chat-sidebar {
    flex-basis: 270px; margin: 0; padding: 1rem .85rem; gap: .85rem;
    border-right: 1px solid #202943; background: #121a2f;
  }
  .chat-sidebar-head { display: flex; align-items: center; justify-content: space-between; }
  .chat-sidebar-head div { display: grid; gap: .18rem; }
  .chat-sidebar-head strong { font-size: .86rem; color: #eef0ff; }
  .chat-sidebar-head span { color: #7d86a5; font-size: .66rem; }
  .sidebar-new { width: 28px; height: 28px; border-radius: 7px; background: #1d2742; border-color: #303c60; }
  .modern-chat .thread-search { max-width: none; border-color: #283250; background: #0c1428; }
  .modern-chat .ghost-btn { padding-inline: .5rem; }
  .modern-chat .threads { gap: .42rem; padding: 0; }
  .modern-chat .thread-chip {
    min-height: 58px; padding: .58rem .5rem .58rem .72rem; border-color: transparent;
    border-radius: 10px; background: transparent;
  }
  .modern-chat .thread-chip:hover:not(.active) { background: #18223a; border-color: #263251; }
  .modern-chat .thread-chip.active { background: #212b46; border-color: #3c4770; box-shadow: inset 3px 0 #8d88ff; }
  .modern-chat .thread-title { font-size: .78rem; color: #eef0ff; }
  .modern-chat .thread-agent { color: #8992b1; }
  .modern-chat .chat-main { background: #090f20; }
  .modern-chat .chat-workspace { gap: 0; }
  .modern-chat .chat-wrap { border: 0; border-radius: 0; background: transparent; }
  .modern-chat .messages { padding: 2.2rem 0 1rem; gap: 1.15rem; }
  /* Use the desktop canvas. Rich answers need enough room for financial tables
     and artifacts on ultrawide displays; prose keeps its own readable measure. */
  .modern-chat .msg-row {
    width: min(1800px, calc(100% - 4rem)); max-width: none; padding: 0;
  }
  .modern-chat .bubble { padding: .85rem 1rem; border-radius: 14px; background: #172139; border-color: #263250; box-shadow: 0 7px 24px rgba(0,0,0,.08); }
  .modern-chat .msg-row:not(.user):not(.sys) .bubble {
    width: 100%; max-width: 100%; padding: 1rem 1.15rem;
    border: 1px solid #202b47; border-radius: 14px; background: #141d33;
  }
  .modern-chat .msg-row.user .bubble { max-width: min(70%, 880px); background: #3a435f; border-color: #47516f; border-bottom-right-radius: 4px; }
  .modern-chat .btext { font-size: .9rem; line-height: 1.65; }
  /* Prose keeps a comfortable measure, while tables, charts, code, thinking
     traces, and other wide artifacts can occupy the full response card. */
  .modern-chat :global(.markdown-body > p),
  .modern-chat :global(.markdown-body > h1),
  .modern-chat :global(.markdown-body > h2),
  .modern-chat :global(.markdown-body > h3),
  .modern-chat :global(.markdown-body > ul),
  .modern-chat :global(.markdown-body > ol),
  .modern-chat :global(.markdown-body > blockquote) { max-width: 92ch; }
  .modern-chat :global(.markdown-body table) { display: table; width: 100%; }
  .modern-chat .bmeta { margin-top: .25rem; color: #727c9c; }
  .budget-recovery { margin-top: 1rem; padding: .9rem 1rem; display: flex; align-items: center; justify-content: space-between; gap: 1rem; border: 1px solid #74642d; border-radius: 12px; background: linear-gradient(135deg, rgba(124,99,35,.18), rgba(83,74,137,.12)); }
  .budget-recovery > div:first-child { display: grid; gap: .25rem; }
  .budget-recovery strong { color: #fff0b8; }
  .budget-recovery span { color: #d9dcef; font-size: .82rem; }
  .budget-recovery small { color: #949dbb; font-size: .72rem; }
  .budget-recovery-actions { display: flex; flex: 0 0 auto; gap: .5rem; }
  .budget-admin-note { max-width: 34ch; color: #fff0b8; font-size: .78rem; }
  .modern-chat .pending-attachments { width: min(1500px, calc(100% - 4rem)); margin: 0 auto; }
  .modern-chat .input-row {
    width: min(1500px, calc(100% - 4rem)); margin: .5rem auto 1rem; padding: .55rem;
    align-items: center; border: 1px solid #2b3658; border-radius: 16px; background: #1a243b;
    box-shadow: 0 18px 48px rgba(0,0,0,.25);
  }
  .modern-chat .input-row textarea { min-height: 48px; max-height: 160px; border: 0; background: transparent; box-shadow: none; line-height: 1.45; }
  .modern-chat .input-row textarea:focus { outline: none; }
  .composer-voice { width: 35px; height: 35px; flex: 0 0 35px; border: 0; border-radius: 50%; color: #cbd0e8; background: #28334f; cursor: pointer; }
  .composer-voice:hover:not(:disabled) { color: #fff; background: #354261; }
  .modern-chat .send-btn { width: 38px; height: 38px; padding: 0; border-radius: 50%; }

  /* Immersive voice mode. It overlays Chat but can be minimized without ending the turn. */
  .voice-session {
    position: fixed; inset: 0; z-index: 90; display: flex; flex-direction: column; overflow: hidden;
    color: #f0f2ff; background:
      radial-gradient(circle at 52% 48%, rgba(89, 112, 193, .18), transparent 26%),
      radial-gradient(circle at 52% 48%, rgba(103, 92, 226, .08), transparent 48%), #081022;
  }
  .voice-session::before { content: ''; position: absolute; inset: 64px 0 0; pointer-events: none; backdrop-filter: blur(1px); }
  .voice-session-head { position: relative; z-index: 1; min-height: 64px; display: grid; grid-template-columns: 1fr auto 1fr; align-items: center; padding: 0 1.2rem; border-bottom: 1px solid #1b2742; background: rgba(8,15,32,.78); }
  .voice-session-brand, .voice-session-agent { display: flex; align-items: center; gap: .55rem; font-size: .8rem; }
  .voice-brand-mark { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; color: #b9b6ff; background: #171b40; font-weight: 800; }
  .voice-session-agent { color: #aeb5d1; }
  .voice-minimize { justify-self: end; width: 34px; height: 34px; border-radius: 9px; border-color: #293553; background: #151e35; font-size: 1.2rem; }
  .voice-session-stage { position: relative; z-index: 1; flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 2rem; text-align: center; }
  .voice-session-status { padding: .35rem 1.2rem; border-radius: 999px; color: #94ceff; background: rgba(105,145,205,.16); font-family: ui-monospace, monospace; font-size: .65rem; font-weight: 750; letter-spacing: .14em; }
  .voice-session-backend { margin-top: .55rem; color: #697797; font-size: .68rem; letter-spacing: .04em; }
  .voice-session-stage h2 { width: min(680px, 90vw); min-height: 2.8em; margin: 2.2rem 0 .45rem; color: #edf1ff; font-size: clamp(1.45rem, 3vw, 2.25rem); font-weight: 600; line-height: 1.35; text-shadow: 0 4px 28px rgba(0,0,0,.45); }
  .voice-session-transcript { width: min(600px, 86vw); min-height: 2.8em; margin: 0; color: #9ca7c5; font-size: .9rem; line-height: 1.55; }
  .voice-orb { position: relative; width: 78px; height: 78px; margin: 4rem 0 3.4rem; display: flex; align-items: center; justify-content: center; gap: 3px; border: 1px solid #5e65aa; border-radius: 50%; color: #fff; background: linear-gradient(145deg, #313466, #171b43); cursor: pointer; box-shadow: 0 0 0 16px rgba(109,124,230,.06), 0 0 52px 8px rgba(112,158,255,.3); }
  .voice-orb::before, .voice-orb::after { content: ''; position: absolute; z-index: -1; width: min(38vw, 440px); height: 1px; background: linear-gradient(90deg, transparent, rgba(126,142,230,.55), transparent); }
  .voice-orb::after { transform: scaleY(16); opacity: .12; filter: blur(2px); }
  .voice-orb span { width: 3px; height: 16px; border-radius: 3px; background: #bec4ff; }
  .voice-orb span:nth-child(2), .voice-orb span:nth-child(4) { height: 27px; }
  .voice-orb span:nth-child(3) { height: 36px; }
  .voice-orb.active span { animation: voiceBars .75s ease-in-out infinite alternate; }
  .voice-orb.active span:nth-child(2) { animation-delay: -.3s; } .voice-orb.active span:nth-child(3) { animation-delay: -.55s; } .voice-orb.active span:nth-child(4) { animation-delay: -.15s; }
  @keyframes voiceBars { to { transform: scaleY(.38); opacity: .65; } }
  .voice-session-controls { display: flex; align-items: center; justify-content: center; gap: 1.2rem; }
  .voice-round { width: 44px; height: 44px; border: 1px solid #2d3957; border-radius: 50%; color: #c8cee5; background: #1b263e; cursor: pointer; }
  .voice-round:hover:not(:disabled), .voice-round.on { color: #fff; background: #293653; }
  .voice-round:disabled { opacity: .5; }
  .voice-end { min-width: 164px; height: 44px; display: flex; align-items: center; justify-content: center; gap: .55rem; border: 0; border-radius: 999px; color: #4c1d27; background: #ffaaa8; cursor: pointer; font-size: .72rem; font-weight: 800; letter-spacing: .08em; }
  .voice-end:hover { background: #ffb9b7; }
  .voice-stop-text { margin-top: 1.2rem; border: 0; color: #9ba5c4; background: transparent; cursor: pointer; text-decoration: underline; text-underline-offset: 3px; }

  /* Responsive — narrow / mobile */
  @media (max-width: 720px) {
    .modern-chat { padding: 0; gap: 0; }
    .modern-chat .page-header { min-height: 58px; padding: 0 .65rem; flex-direction: row; align-items: center; }
    .chat-brand span, .primary-controls :global(.run-metrics), .new-chat-btn, .header-tour, .agent-picker .agent-presence { display: none; }
    .agent-picker { min-width: 112px; }
    .modern-chat .chat-sidebar { position: absolute; z-index: 30; top: 58px; bottom: 0; width: min(280px, 86vw); }
    .modern-chat .msg-row { width: 100%; padding: 0 .75rem; }
    .modern-chat .msg-row.user .bubble, .modern-chat .msg-row:not(.user):not(.sys) .bubble { max-width: 92%; }
    .modern-chat .input-row { width: calc(100% - 1rem); margin-bottom: .5rem; }
    .voice-session-head { grid-template-columns: 1fr auto; }
    .voice-session-agent { display: none; }
    .voice-session-stage { padding: 1rem; }
    .voice-orb { margin: 2.6rem 0 2.4rem; }
    .msg-row, .msg-row.user .bubble { max-width: 100%; }
    .bubble { max-width: 100%; }
    .cp-field input, .cp-field select, .cp-field .model-custom, .cp-field input[type=number] { width: 100%; }
    .cp-field { flex: 1 1 45%; }
    .thread-search { max-width: none; }
    .budget-recovery { align-items: stretch; flex-direction: column; }
    .budget-recovery-actions { flex-wrap: wrap; }
  }
</style>
