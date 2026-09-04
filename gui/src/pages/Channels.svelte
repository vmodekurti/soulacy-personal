<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import QRCode from 'qrcode'
  import { api } from '../lib/api.js'
  import { guideFor, renderInline } from '../lib/channelguides.js'

  let channels = []
  let agents   = []
  let tiers    = {}
  let loading  = true
  let error    = ''
  let notice   = ''
  let actionLoading = {}
  let restartNeeded = false
  let restarting = false
  let diagnosis = {}   // adapterId → { ok, category, reason, fix, detail, to }
  let channelMetrics = { inbound: [], outbound: [], inbox_drops: [] }
  let deliveryReadiness = null
  // F-GUI-5 — the "saving without ack fails readiness" callout depends on the
  // active deployment profile (S4 readiness verdict is only `fail` in
  // production; `warn` otherwise). Deployment status is best-effort and the
  // callout still renders without it — we just don't escalate to a red
  // production warning if the profile isn't known.
  let deploymentProfile = ''

  // Edit modal state
  let editing   = null   // the channel being edited
  let form      = {}     // key → value
  let botsForm  = []
  let saving    = false
  let pairing   = false
  let advancedWhatsAppWeb = false
  let guideOpen = false
  let qrDataUrl = ''

  $: activeGuide = editing ? guideFor(editing.id) : null
  let lastQR    = ''
  const wait = (ms) => new Promise(resolve => setTimeout(resolve, ms))

  $: activePairingQR = editing?.id === 'whatsapp_web' ? (editing.status?.qr_code || '') : ''
  $: if (activePairingQR !== lastQR) {
    lastQR = activePairingQR
    renderPairingQR(activePairingQR)
  }

  async function renderPairingQR(value) {
    if (!value) {
      qrDataUrl = ''
      return
    }
    qrDataUrl = await QRCode.toDataURL(value, { width: 256, margin: 2, errorCorrectionLevel: 'M' })
  }

  async function load() {
    loading = true
    error   = ''
    try {
      const [channelRes, agentRes, metricsRes, deliveryRes] = await Promise.all([
        api.channels.list(),
        api.agents.list().catch(() => ({ agents })),
        api.channels.metrics().catch(() => ({ inbound: [], outbound: [], inbox_drops: [] })),
        api.channels.deliveryReadiness().catch(() => null),
      ])
      channels = channelRes.channels || []
      agents = agentRes.agents || []
      channelMetrics = {
        inbound: Array.isArray(metricsRes?.inbound) ? metricsRes.inbound : [],
        outbound: Array.isArray(metricsRes?.outbound) ? metricsRes.outbound : [],
        inbox_drops: Array.isArray(metricsRes?.inbox_drops) ? metricsRes.inbox_drops : [],
      }
      deliveryReadiness = deliveryRes
      loadTiers(agents)
      // Non-blocking — used purely to escalate the privileged callout in
      // production. See resolvePrivilegedContext below.
      api.deploymentStatus().then(res => { deploymentProfile = res?.profile || '' }).catch(() => {})
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function loadTiers(list) {
    const next = {}
    await Promise.all((list || []).map(async (agent) => {
      if (!agent?.id) return
      try {
        const res = await api.agents.tier(agent.id)
        next[agent.id] = { tier: res?.tier || 'unknown', reasons: res?.reasons || [] }
      } catch (_) {
        next[agent.id] = { tier: 'unknown', reasons: [] }
      }
    }))
    tiers = next
  }

  async function toggleChannel(ch) {
    actionLoading = { ...actionLoading, [ch.id]: true }
    notice = ''
    try {
      const res = ch.enabled ? await api.channels.disable(ch.id) : await api.channels.enable(ch.id)
      notice = res.message || 'Saved.'
      restartNeeded = true
      await load()
    } catch (e) {
      error = e.message
    } finally {
      actionLoading = { ...actionLoading, [ch.id]: false }
    }
  }

  async function testChannel(ch, row = null) {
    const adapterId = row?.adapter_id || ch.id
    actionLoading = { ...actionLoading, [`test:${adapterId}`]: true }
    error = ''
    notice = ''
    try {
      const res = await api.channels.test(ch.id, row ? { adapter_id: adapterId } : {})
      diagnosis = { ...diagnosis, [adapterId]: { ok: true, category: 'ok', reason: 'Delivery test reached the destination.', to: res.to } }
      notice = `Delivery test sent through ${res.channel}${res.to ? ` to ${res.to}` : ''}.`
    } catch (e) {
      if (e.body?.diagnosis) {
        diagnosis = { ...diagnosis, [adapterId]: { ...e.body.diagnosis, to: e.body.to } }
      }
      error = e.message
    } finally {
      actionLoading = { ...actionLoading, [`test:${adapterId}`]: false }
    }
  }

  // diagnoseChannel runs the "delivery doctor" for a mapping. Unlike test, it
  // always returns a structured, plain-language diagnosis (never a raw error),
  // which we surface in a per-adapter panel.
  async function diagnoseChannel(ch, row = null) {
    const adapterId = row?.adapter_id || ch.id
    actionLoading = { ...actionLoading, [`diag:${adapterId}`]: true }
    error = ''
    notice = ''
    try {
      const res = await api.channels.diagnose(ch.id, row ? { adapter_id: adapterId } : {})
      diagnosis = { ...diagnosis, [adapterId]: { ...res.diagnosis, to: res.to } }
    } catch (e) {
      // The endpoint returns 200 with a diagnosis; a thrown error here is an
      // unexpected transport/auth problem, so show it plainly.
      diagnosis = { ...diagnosis, [adapterId]: { ok: false, category: 'unknown', reason: e.message, fix: '' } }
    } finally {
      actionLoading = { ...actionLoading, [`diag:${adapterId}`]: false }
    }
  }

  function dismissDiagnosis(adapterId) {
    const next = { ...diagnosis }
    delete next[adapterId]
    diagnosis = next
  }

  function openEdit(ch) {
    editing = ch
    form = {}
    advancedWhatsAppWeb = false
    // First-time setup gets the guide expanded; reconfiguration starts folded.
    guideOpen = !ch.configured
    for (const f of ch.schema || []) {
      // For secrets that are already set, leave the box blank — blank means "keep".
      form[f.key] = f.secret && ch.settings?.[f.key] === '***' ? '' : (ch.settings?.[f.key] || '')
    }
    if (isMessageChannel(ch.id)) {
      if (!form.trigger_phrase) form.trigger_phrase = '!soulacy'
      if (form.ignore_groups === '' || form.ignore_groups === undefined) form.ignore_groups = true
      form.ignore_groups = form.ignore_groups === true || String(form.ignore_groups).toLowerCase() === 'true'
    }
    if (ch.id === 'whatsapp_web' && !form.agent_id && agents.length) {
      form.agent_id = agents[0].id
    }
    botsForm = (ch.bots || []).map(bot => {
      const copy = {}
      for (const f of ch.bot_schema || ch.schema || []) {
        copy[f.key] = f.secret && bot[f.key] === '***' ? '' : (bot[f.key] || '')
      }
      copy._adapter_id = bot._adapter_id || ''
      return copy
    })
  }

  function closeEdit() {
    editing = null
    form = {}
    botsForm = []
    advancedWhatsAppWeb = false
    qrDataUrl = ''
    lastQR = ''
  }

  async function saveEdit() {
    if (!editing) return
    saving = true
    error  = ''
    try {
      const body = { settings: form }
      if (editing.multi_bot) body.bots = botsForm
      const res = await api.channels.update(editing.id, body)
      notice = res.message || 'Channel saved.'
      restartNeeded = true
      closeEdit()
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function startWhatsAppWebPairing() {
    if (!form.agent_id) {
      error = 'Select an agent before pairing WhatsApp Web.'
      return
    }
    pairing = true
    error = ''
    notice = ''
    try {
      const res = await api.channels.pairWhatsAppWeb({
        agent_id: form.agent_id,
        trigger_phrase: form.trigger_phrase || '!soulacy',
        ignore_groups: !!form.ignore_groups,
        allowed_chat_ids: form.allowed_chat_ids || '',
        allowed_sender_ids: form.allowed_sender_ids || '',
      })
      notice = res.message || 'WhatsApp Web pairing started.'
      restartNeeded = false
      for (let i = 0; i < 20; i += 1) {
        await wait(i === 0 ? 300 : 1000)
        await load()
        const fresh = channels.find(ch => ch.id === 'whatsapp_web')
        if (fresh) editing = fresh
        if (fresh?.status?.qr_code || fresh?.status?.connected) break
      }
    } catch (e) {
      error = e.message
    } finally {
      pairing = false
    }
  }

  onMount(load)

  function statusColor(ch) {
    if (ch.status?.connected || connectedInteractiveBots(ch).length > 0) return '#4caf82'
    if (ch.enabled && ch.configured) return '#f0a060'  // should connect but isn't
    return '#555a7a'
  }
  function statusLabel(ch) {
    const aggregate = aggregateChannelLabel(ch)
    if (aggregate) return aggregate
    if (ch.status?.connected) return ch.status.detail || 'connected'
    if (ch.always) return 'always on'
    if (ch.enabled && !ch.configured) return 'needs config'
    if (ch.enabled) return ch.status?.detail || 'restart to connect'
    if (ch.configured) return 'disabled'
    return 'not configured'
  }

  function connectionLabel(ch) {
    if (ch.status?.connected) return '● Live'
    const liveInteractive = connectedInteractiveBots(ch).length
    if (liveInteractive > 0) return `● ${plural(liveInteractive, 'bot')} live`
    return '○ Offline'
  }

  function aggregateChannelLabel(ch) {
    if (ch.id !== 'telegram') return ''
    const interactive = interactiveBots(ch)
    if (!interactive.length) return ''
    const liveInteractive = connectedInteractiveBots(ch)
    const hasDefaultOutbound = isTruthy(ch.settings?.outbound_only) || hasDefaultSender(ch)
    if (hasDefaultOutbound) {
      if (liveInteractive.length) return `default outbound + ${plural(liveInteractive.length, 'live bot')}`
      return `default outbound + ${plural(interactive.length, 'bot mapping')}`
    }
    if (liveInteractive.length) return plural(liveInteractive.length, 'live interactive bot')
    return plural(interactive.length, 'interactive bot mapping')
  }

  const CHANNEL_ICONS = {
    whatsapp: '📱', whatsapp_web: '🔗', telegram: '✈️', slack: '💬', discord: '🎮', http: '🌐', email: '📧', webhook: '🔗',
  }
  function chanIcon(id = '') { return CHANNEL_ICONS[id.toLowerCase()] || '⚡' }
  function isMessageChannel(id = '') {
    return ['telegram', 'discord', 'slack', 'whatsapp', 'whatsapp_web'].includes(id)
  }

  function displayValue(ch, f) {
    const v = ch.settings?.[f.key]
    if (!v) return '—'
    if (f.secret) return '•••• set'
    return v
  }

  function hasDefaultSender(ch) {
    return !!(ch.settings?.token || ch.settings?.default_output_to || ch.settings?.bot_name)
  }

  function canTestChannel(ch) {
    return ch.id !== 'http' && ch.enabled && ch.registered
  }

  function canTestMapping(ch, row) {
    return ch.id !== 'http' && ch.enabled && row?.connected
  }

  function interactiveBots(ch) {
    return (ch.bots || []).filter(bot => !isTruthy(bot.outbound_only))
  }

  function connectedInteractiveBots(ch) {
    return interactiveBots(ch).filter(bot => bot._connected)
  }

  function schemaFields(ch) {
    return Array.isArray(ch?.schema) ? ch.schema : []
  }

  function plural(count, label) {
    return `${count} ${label}${count === 1 ? '' : 's'}`
  }

  function mappingRows(ch) {
    const rows = []
    if (ch.id === 'telegram' && hasDefaultSender(ch)) {
      rows.push({
        adapter_id: ch.id,
        bot_name: ch.settings?.bot_name || 'Default outbound bot',
        agent_id: ch.settings?.agent_id && !isTruthy(ch.settings?.outbound_only) ? ch.settings.agent_id : 'Default sender',
        outbound_only: isTruthy(ch.settings?.outbound_only) || !ch.settings?.agent_id,
        connected: ch.status?.connected,
        detail: ch.status?.detail,
      })
    }
    if (ch.bots?.length) {
      rows.push(...ch.bots.map((bot, i) => ({
        adapter_id: bot._adapter_id || botAdapterID(ch.id, bot, i),
        bot_name: bot.bot_name || bot.name || '',
        agent_id: bot.outbound_only ? 'Send only' : (bot.agent_id || '—'),
        outbound_only: isTruthy(bot.outbound_only),
        connected: bot._connected,
        detail: bot._detail,
        _blocked_reason: bot._blocked_reason || '',
      })))
      return rows
    }
    if (rows.length) return rows
    const agent = ch.settings?.agent_id
    const outboundOnly = isTruthy(ch.settings?.outbound_only)
    return (agent || outboundOnly) ? [{
      adapter_id: ch.id,
      bot_name: ch.settings?.bot_name || '',
      agent_id: outboundOnly ? 'Send only' : agent,
      outbound_only: outboundOnly,
      connected: ch.status?.connected,
      detail: ch.status?.detail,
    }] : []
  }

  function addBotMapping() {
    if (!editing) return
    const row = {}
    for (const f of editing.bot_schema || editing.schema || []) row[f.key] = ''
    botsForm = [...botsForm, row]
  }

  function addOutboundBotMapping() {
    if (!editing) return
    const row = {}
    for (const f of editing.bot_schema || editing.schema || []) row[f.key] = ''
    row.outbound_only = true
    row.bot_name = editing.id === 'telegram' ? 'Daily Stock Screener' : ''
    botsForm = [...botsForm, row]
  }

  function removeBotMapping(index) {
    botsForm = botsForm.filter((_, i) => i !== index)
  }

  function updateBotField(index, key, value) {
    botsForm = botsForm.map((bot, i) => i === index ? { ...bot, [key]: value } : bot)
  }

  function isTruthy(value) {
    return value === true || String(value).toLowerCase() === 'true'
  }

  function sanitizeAdapterSuffix(value = '') {
    const s = String(value || '').replace(/[^A-Za-z0-9_-]/g, '-')
    return s.replace(/-+/g, '-').replace(/^-|-$/g, '')
  }

  function botAdapterID(channelID, bot, index) {
    const defaultReserved = editing?.id === channelID && !!(form.token || form.bot_token)
    if (index === 0 && !defaultReserved) return channelID
    const suffix = sanitizeAdapterSuffix(bot.agent_id) || sanitizeAdapterSuffix(bot.bot_name) || String(index + 1)
    return `${channelID}-${suffix}`
  }

  function botHeaderLabel(channelID, bot, index) {
    return bot._adapter_id || botAdapterID(channelID, bot, index)
  }

  function shouldShowChannelField(f, values = {}) {
    if (editing?.id === 'telegram' && f.key === 'agent_id' && isTruthy(values.outbound_only)) return false
    if (editing?.id === 'telegram' && ['accept_privileged_exposure', 'trigger_phrase', 'ignore_groups', 'allowed_chat_ids', 'allowed_user_ids'].includes(f.key) && isTruthy(values.outbound_only)) return false
    return true
  }

  function checkboxLabel(f) {
    if (f.key === 'outbound_only') return 'Use this bot only for scheduled output'
    if (f.key === 'accept_privileged_exposure') return 'I understand this bot can expose privileged agent capabilities'
    return 'Enabled'
  }

  function botCheckboxLabel(f) {
    if (f.key === 'outbound_only') return 'Send scheduled output only'
    if (f.key === 'accept_privileged_exposure') return 'Allow this bot to expose privileged agent capabilities'
    return 'Enabled'
  }

  function agentLabel(agent) {
    return agent.name && agent.name !== agent.id ? `${agent.id} — ${agent.name}` : agent.id
  }

  function mappingTier(row) {
    if (!row || row.outbound_only) return null
    const id = row.agent_id
    if (!id || id === '—' || id === 'Send only' || id === 'Default sender') return null
    return tiers[id] || { tier: 'unknown', reasons: [] }
  }

  // F-GUI-5 — privileged-exposure context. Returns null when the currently
  // bound agent isn't privileged, so the callout renders only when it's
  // actually informative. Consumed by both the single-form and bot-mapping
  // renderers.
  function privilegedContext(agentId, values = {}) {
    if (!agentId) return null
    const info = tiers[agentId]
    if (!info || info.tier !== 'privileged') return null
    const accepted = isTruthy(values.accept_privileged_exposure)
    return {
      agentId,
      tier: info.tier,
      reasons: info.reasons || [],
      accepted,
      needsAck: !accepted,
      // In production S4 (internal/gateway/securityreadiness.go) escalates an
      // unaccepted privileged exposure from `warn` to `fail`, blocking the
      // production readiness check. Elsewhere it's a warning.
      productionBlock: !accepted && deploymentProfile === 'production',
    }
  }

  function openSecurityDoctorForAgent(agentId) {
    if (!agentId) return
    const params = new URLSearchParams({ agent_id: agentId, doctor: '1' })
    window.location.hash = `agents?${params.toString()}`
  }

  function channelMetricIDs(ch) {
    const ids = new Set([ch.id])
    for (const row of mappingRows(ch)) {
      if (row.adapter_id) ids.add(row.adapter_id)
    }
    return ids
  }

  function metricSum(rows = [], ids, outcome = '') {
    return rows
      .filter(row => ids.has(row.channel) && (!outcome || row.outcome === outcome))
      .reduce((sum, row) => sum + Number(row.count || 0), 0)
  }

  function channelTraffic(ch) {
    const ids = channelMetricIDs(ch)
    return {
      inbound: metricSum(channelMetrics.inbound, ids),
      success: metricSum(channelMetrics.outbound, ids, 'success'),
      error: metricSum(channelMetrics.outbound, ids, 'error'),
      unregistered: metricSum(channelMetrics.outbound, ids, 'unregistered'),
      drops: metricSum(channelMetrics.inbox_drops, ids),
    }
  }

  function hasTraffic(ch) {
    const t = channelTraffic(ch)
    return t.inbound || t.success || t.error || t.unregistered || t.drops
  }

  function tierLabel(tier) {
    switch (tier) {
      case 'read_only': return 'Read-only'
      case 'active': return 'Active'
      case 'privileged': return 'Privileged'
      default: return 'Unknown'
    }
  }

  async function restartGateway() {
    restarting = true
    error = ''
    try {
      await api.admin.restart()
      restartNeeded = false
      notice = 'Restart requested. Reconnect this page in a few seconds if it does not refresh automatically.'
    } catch (e) {
      error = e.message
    } finally {
      setTimeout(() => { restarting = false }, 5000)
    }
  }
</script>

<div class="page">
  <div class="page-header">
    <h1>Delivery</h1>
    <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
        <TourButton />
    </div>

  {#if error}
    <div class="banner err">{error}</div>
  {/if}
  {#if notice}
    <div class="banner ok">{notice}</div>
  {/if}
  {#if restartNeeded}
    <div class="banner warn restart-banner">
      <span>Channel settings were saved. Restart the gateway to reconnect adapters.</span>
      <button class="btn-secondary" on:click={restartGateway} disabled={restarting}>
        {restarting ? 'Restarting…' : 'Restart Gateway'}
      </button>
    </div>
  {/if}

  {#if deliveryReadiness}
    <div class="delivery-summary {deliveryReadiness.status}">
      <div>
        <div class="summary-kicker">Delivery readiness</div>
        <strong>{deliveryReadiness.ready || 0}/{deliveryReadiness.total || 0} targets ready · {deliveryReadiness.score || 0}%</strong>
        <span>
          {#if deliveryReadiness.next_actions?.length}
            {deliveryReadiness.next_actions[0]}
          {:else}
            Defaults and agent-specific bot mappings are ready for outbound delivery.
          {/if}
        </span>
      </div>
      <div class="summary-targets">
        {#each (deliveryReadiness.targets || []).slice(0, 6) as target}
          <span class="target-pill {target.status}" title={target.issue || target.next || 'Ready'}>
            {target.family}:{target.mode} {target.status}
          </span>
        {/each}
      </div>
    </div>
  {/if}

  {#if loading && channels.length === 0}
    <div class="empty">Loading channels…</div>
  {:else}
    <div class="channel-grid">
      {#each channels as ch}
        <div class="ch-card" class:enabled={ch.enabled}>
          <div class="ch-header">
            <span class="ch-icon">{chanIcon(ch.id)}</span>
            <div class="ch-identity">
              <span class="ch-name">{ch.name || ch.id}</span>
              <span class="ch-id">{ch.id}</span>
            </div>
            <div class="ch-badge" style="color:{statusColor(ch)}">{statusLabel(ch)}</div>
          </div>

          <div class="ch-body">
            <div class="ch-row">
              <span class="ch-label">Connection</span>
              <span class="ch-val" style="color:{statusColor(ch)}">
                {connectionLabel(ch)}
              </span>
            </div>
            <div class="ch-row">
              <span class="ch-label">Enabled</span>
              <span class="ch-val">{ch.always ? 'Always' : (ch.enabled ? 'Yes' : 'No')}</span>
            </div>

            {#if ch.id === 'whatsapp_web'}
              <div class="settings">
                <span class="settings-title">Pairing</span>
                <div class="ch-row">
                  <span class="ch-label">Agent</span>
                  <span class="ch-val mono">{ch.settings?.agent_id || 'Select during setup'}</span>
                </div>
                <div class="ch-row">
                  <span class="ch-label">Device</span>
                  <span class="ch-val">{ch.status?.connected ? 'Paired' : (ch.status?.qr_code ? 'QR ready' : 'Not paired')}</span>
                </div>
                <div class="ch-row">
                  <span class="ch-label">Trigger</span>
                  <span class="ch-val mono">{ch.settings?.trigger_phrase || '!soulacy'}</span>
                </div>
                <div class="ch-row">
                  <span class="ch-label">Groups</span>
                  <span class="ch-val">{ch.settings?.ignore_groups === false || ch.settings?.ignore_groups === 'false' ? 'Allowed' : 'Ignored'}</span>
                </div>
              </div>
            {:else if ch.schema?.length}
              <div class="settings">
                <span class="settings-title">Settings</span>
                {#each ch.schema as f}
                  <div class="ch-row">
                    <span class="ch-label">{f.label}</span>
                    <span class="ch-val mono">{displayValue(ch, f)}</span>
                  </div>
                {/each}
              </div>
            {:else}
              <div class="ch-row no-settings">No configurable settings.</div>
            {/if}

            {#if mappingRows(ch).length}
              <div class="settings">
                <span class="settings-title">Agent mappings</span>
                {#each mappingRows(ch) as row}
                  <div class="mapping-row">
                    <div>
                      <code>{row.bot_name || row.adapter_id}</code>
                      {#if row.bot_name}<em>{row.adapter_id}</em>{/if}
                      <span>{row.connected ? 'connected' : (row.detail || 'pending restart')}</span>
                    </div>
                    <div class="mapping-actions">
                      <strong class:send-only={row.outbound_only}>{row.agent_id}</strong>
                      {#if mappingTier(row)}
                        <span class="tier-chip" class:read={mappingTier(row).tier === 'read_only'} class:active-tier={mappingTier(row).tier === 'active'} class:privileged={mappingTier(row).tier === 'privileged'}>
                          {tierLabel(mappingTier(row).tier)}
                        </span>
                      {/if}
                      {#if ch.id !== 'http'}
                        <button
                          class="btn-secondary tiny"
                          disabled={!canTestMapping(ch, row) || actionLoading[`test:${row.adapter_id}`]}
                          title={!row.connected ? 'Restart or reconnect this bot mapping before testing' : 'Send a real test message through this bot mapping'}
                          on:click={() => testChannel(ch, row)}>
                          {actionLoading[`test:${row.adapter_id}`] ? '…' : 'Test'}
                        </button>
                        <button
                          class="btn-secondary tiny"
                          disabled={actionLoading[`diag:${row.adapter_id}`]}
                          title="Diagnose delivery for this bot mapping and explain any failure in plain language"
                          on:click={() => diagnoseChannel(ch, row)}>
                          {actionLoading[`diag:${row.adapter_id}`] ? '…' : 'Diagnose'}
                        </button>
                      {/if}
                    </div>
                    {#if row._blocked_reason}
                      <div class="mapping-warning">
                        This mapping is blocked because {row._blocked_reason}. Open settings, enable privileged exposure for this bot, save, then restart.
                      </div>
                    {:else if mappingTier(row)?.tier === 'privileged'}
                      <div class="mapping-warning">
                        This bot exposes a privileged agent. Keep allowlists and trigger phrases tight, and require explicit exposure approval before restart.
                      </div>
                    {/if}
                    {#if diagnosis[row.adapter_id]}
                      <div class="diag-result {diagnosis[row.adapter_id].ok ? 'ok' : 'bad'}">
                        <div class="diag-head">
                          <strong>{diagnosis[row.adapter_id].ok ? '✓ Delivery OK' : '✗ ' + (diagnosis[row.adapter_id].category || 'failed')}</strong>
                          <button class="diag-x" on:click={() => dismissDiagnosis(row.adapter_id)} title="Dismiss">×</button>
                        </div>
                        <p>{diagnosis[row.adapter_id].reason}</p>
                        {#if diagnosis[row.adapter_id].fix}<p class="diag-fix"><strong>Fix:</strong> {diagnosis[row.adapter_id].fix}</p>{/if}
                        {#if diagnosis[row.adapter_id].detail}<details><summary>Raw error</summary><code>{diagnosis[row.adapter_id].detail}</code></details>{/if}
                      </div>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}

            {#if hasTraffic(ch)}
              {@const traffic = channelTraffic(ch)}
              <div class="settings traffic">
                <span class="settings-title">Traffic</span>
                <div class="traffic-grid">
                  <span title="Inbound messages accepted by Soulacy for this channel or its bot mappings."><strong>{traffic.inbound}</strong><em>in</em></span>
                  <span title="Outbound messages sent successfully."><strong>{traffic.success}</strong><em>sent</em></span>
                  <span class:bad={traffic.error > 0} title="Outbound sends that reached an adapter but failed."><strong>{traffic.error}</strong><em>errors</em></span>
                  <span class:bad={traffic.unregistered > 0} title="Outbound sends attempted before a matching adapter was registered."><strong>{traffic.unregistered}</strong><em>unregistered</em></span>
                  <span class:bad={traffic.drops > 0} title="Inbound messages dropped because the shared inbox was full."><strong>{traffic.drops}</strong><em>dropped</em></span>
                </div>
              </div>
            {/if}

            {#if ch.diagnostics?.length}
              <div class="diagnostics">
                <span class="settings-title">Delivery checks</span>
                {#each ch.diagnostics as d}
                  <div class="diag-row {d.severity || 'info'}">
                    <strong>{d.severity || 'info'}</strong>
                    <span>{d.message}</span>
                    {#if d.remedy}<em>{d.remedy}</em>{/if}
                  </div>
                {/each}
              </div>
            {/if}

            {#if diagnosis[ch.id]}
              <div class="diag-result {diagnosis[ch.id].ok ? 'ok' : 'bad'}">
                <div class="diag-head">
                  <strong>{diagnosis[ch.id].ok ? '✓ Delivery OK' : '✗ ' + (diagnosis[ch.id].category || 'failed')}</strong>
                  <button class="diag-x" on:click={() => dismissDiagnosis(ch.id)} title="Dismiss">×</button>
                </div>
                <p>{diagnosis[ch.id].reason}</p>
                {#if diagnosis[ch.id].fix}<p class="diag-fix"><strong>Fix:</strong> {diagnosis[ch.id].fix}</p>{/if}
                {#if diagnosis[ch.id].detail}<details><summary>Raw error</summary><code>{diagnosis[ch.id].detail}</code></details>{/if}
              </div>
            {/if}

          </div>

          <div class="ch-footer">
            {#if ch.schema?.length}
              <button class="btn-secondary small-btn" on:click={() => openEdit(ch)}>
                {ch.id === 'whatsapp_web' ? 'Connect' : 'Edit'}
              </button>
            {/if}
            {#if ch.id !== 'http'}
              <button
                class="btn-secondary small-btn"
                disabled={!canTestChannel(ch) || actionLoading[`test:${ch.id}`]}
                title={!ch.registered ? 'Save settings and restart the gateway before testing' : !ch.enabled ? 'Enable this channel before testing' : 'Send a real test message through this channel'}
                on:click={() => testChannel(ch)}>
                {actionLoading[`test:${ch.id}`] ? 'Testing…' : 'Test'}
              </button>
              <button
                class="btn-secondary small-btn"
                disabled={actionLoading[`diag:${ch.id}`]}
                title="Diagnose delivery and explain any failure in plain language"
                on:click={() => diagnoseChannel(ch)}>
                {actionLoading[`diag:${ch.id}`] ? 'Diagnosing…' : 'Diagnose'}
              </button>
            {/if}
            {#if !ch.always}
              <button
                class={ch.enabled ? 'btn-danger' : 'btn-primary'}
                disabled={actionLoading[ch.id] || (!ch.enabled && !ch.configured)}
                title={!ch.enabled && !ch.configured ? 'Add settings first' : ''}
                on:click={() => toggleChannel(ch)}>
                {actionLoading[ch.id] ? '…' : ch.enabled ? 'Disable' : 'Enable'}
              </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>

    <div class="info-card">
      <h3>About channels</h3>
      <p>Configure any channel here — settings are written to your <code>config.yaml</code>. Secrets are masked; leave a secret field blank when editing to keep the existing value. <strong>Restart the gateway</strong> after changes for adapters to connect or disconnect.</p>
    </div>
  {/if}
</div>

<!-- Edit modal -->
{#if editing}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close channel modal"
    on:click|self={closeEdit}
    on:keydown={(e) => e.key === 'Escape' && closeEdit()}
  >
    <div class="modal">
      <h2>{chanIcon(editing.id)} Configure {editing.name}</h2>

      {#if activeGuide}
        <div class="guide" class:open={guideOpen}>
          <button type="button" class="guide-toggle" on:click={() => guideOpen = !guideOpen}>
            📖 Setup guide {guideOpen ? '▾' : '▸'}
          </button>
          {#if guideOpen}
            <p class="guide-intro">{@html renderInline(activeGuide.intro)}</p>
            <ol class="guide-steps">
              {#each activeGuide.steps as step}
                <li>{@html renderInline(step)}</li>
              {/each}
            </ol>
            {#if activeGuide.test}
              <p class="guide-test"><strong>Test it:</strong> {@html renderInline(activeGuide.test)}</p>
            {/if}
            {#if activeGuide.warning}
              <p class="guide-warning">⚠ {@html renderInline(activeGuide.warning)}</p>
            {/if}
          {/if}
        </div>
      {/if}

      {#if editing.id === 'whatsapp_web'}
        <div class="pair-panel">
          <label class="field">
            <span class="field-label">Agent<span class="req">*</span></span>
            <select bind:value={form.agent_id}>
              <option value="">Select an agent…</option>
              {#each agents as agent}
                <option value={agent.id}>{agentLabel(agent)}</option>
              {/each}
            </select>
          </label>

          <div class="safety-panel">
            <strong>Message safety</strong>
            <label class="field">
              <span class="field-label">Trigger phrase</span>
              <input type="text" bind:value={form.trigger_phrase} placeholder="!soulacy" />
              <span class="field-help">Only messages that start with this phrase will run the agent.</span>
            </label>
            <label class="check-row">
              <input type="checkbox" bind:checked={form.ignore_groups} />
              <span>Ignore group chats</span>
            </label>
          </div>

          <div class="pair-status" class:live={editing.status?.connected} class:waiting={editing.status?.qr_code && !editing.status?.connected}>
            <strong>{editing.status?.connected ? 'Device paired' : editing.status?.qr_code ? 'QR code ready' : 'Ready to pair'}</strong>
            <span>{editing.status?.detail || (editing.status?.connected ? 'WhatsApp Web is connected.' : 'Click the button below to generate a QR code.')}</span>
          </div>

          {#if qrDataUrl}
            <div class="qr-preview">
              <img src={qrDataUrl} alt="WhatsApp Web pairing QR code" />
              <p>Open WhatsApp, go to Linked devices, choose Link a device, then scan this code.</p>
            </div>
          {/if}

          <div class="pair-actions">
            <button class="btn-primary" on:click={startWhatsAppWebPairing} disabled={pairing || !form.agent_id}>
              {pairing ? 'Generating…' : editing.status?.connected ? 'Refresh pairing' : 'Generate QR code'}
            </button>
            <button class="btn-secondary" type="button" on:click={() => advancedWhatsAppWeb = !advancedWhatsAppWeb}>
              {advancedWhatsAppWeb ? 'Hide advanced' : 'Advanced'}
            </button>
          </div>

          {#if advancedWhatsAppWeb}
            <div class="advanced-panel">
              {#each schemaFields(editing).filter(f => !['agent_id', 'trigger_phrase', 'ignore_groups'].includes(f.key)) as f}
                <label class="field">
                  <span class="field-label">
                    {f.label}{#if f.required}<span class="req">*</span>{/if}
                  </span>
                  <input type="text" bind:value={form[f.key]} placeholder={f.help || ''} />
                  {#if f.help}<span class="field-help">{f.help}</span>{/if}
                </label>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        <!-- F-GUI-5 — privileged-exposure explainer for single-agent bindings.
             When the bound agent is privileged the operator gets a plain-
             language brief of what "privileged" implies plus a red
             production-fail nudge when the ack toggle is off. The learn-more
             link deep-links to the Security Doctor drawer for the same agent. -->
        {@const pc = privilegedContext(form.agent_id, form)}
        {#if pc}
          <div class="privileged-callout" class:danger={pc.productionBlock} class:warn={pc.needsAck && !pc.productionBlock}>
            <strong>
              {pc.needsAck ? 'This channel binds to a privileged agent' : 'Privileged exposure acknowledged'}
            </strong>
            <p>
              {agentLabel(agents.find(a => a.id === pc.agentId) || { id: pc.agentId })}
              is <em>privileged</em> — it can reach OS-level tools, file writes, package installs, or wildcard MCP.
            </p>
            <p>
              Enabling <code>accept_privileged_exposure</code> below records that you understand
              incoming messages on this shared channel can trigger those tools.
              {#if pc.productionBlock}
                <span class="privileged-prod">Production readiness will fail if you save without acknowledging.</span>
              {:else if pc.needsAck}
                <span>Saving without ack will surface as a warning in production readiness.</span>
              {/if}
            </p>
            <button class="btn-secondary tiny" type="button"
                    on:click={() => openSecurityDoctorForAgent(pc.agentId)}>
              Learn more — open Security Doctor
            </button>
          </div>
        {/if}
        <div class="fields">
          {#each schemaFields(editing) as f}
            {#if shouldShowChannelField(f, form)}
              <label class="field">
                <span class="field-label">
                  {f.label}{#if f.required && !(editing.id === 'telegram' && f.key === 'agent_id' && isTruthy(form.outbound_only))}<span class="req">*</span>{/if}
                </span>
                {#if f.type === 'password'}
                  <input type="password" bind:value={form[f.key]}
                    placeholder={editing.settings?.[f.key] === '***' ? '•••• (unchanged)' : (f.help || '')} />
                {:else if f.type === 'checkbox' || f.key === 'ignore_groups'}
                  <label class="check-row compact">
                    <input type="checkbox" bind:checked={form[f.key]} />
                    <span>{checkboxLabel(f)}</span>
                  </label>
                {:else if f.key === 'agent_id' && agents.length}
                  <select bind:value={form[f.key]}>
                    <option value="">Select an agent…</option>
                    {#each agents as agent}
                      <option value={agent.id}>{agentLabel(agent)}</option>
                    {/each}
                  </select>
                {:else}
                  <input type="text" bind:value={form[f.key]} placeholder={f.help || ''} />
                {/if}
                {#if f.help}<span class="field-help">{f.help}</span>{/if}
                {#if activeGuide?.fields?.[f.key]}
                  <span class="field-help guide-hint">💡 {@html renderInline(activeGuide.fields[f.key])}</span>
                {/if}
              </label>
            {/if}
          {/each}
        </div>
      {/if}

      {#if editing.multi_bot}
        <div class="bot-editor">
          <div class="bot-editor-head">
              <div>
                <h3>Bot mappings</h3>
              <p>{editing.id === 'telegram' ? 'The default fields above define the outbound sender for cron jobs and manual scheduled output. Add bot mappings below for agent-specific interactive bots.' : 'Each row creates one channel adapter and routes that bot to its agent.'}</p>
              </div>
              <div class="bot-editor-actions">
                <button class="btn-secondary small-btn" type="button" on:click={addBotMapping}>+ Add bot</button>
              </div>
          </div>

          {#if botsForm.length === 0}
            <div class="bot-empty">No dedicated bot mappings. The single default fields above will be used.</div>
          {:else}
            <div class="bot-list">
              {#each botsForm as bot, i}
                {@const bpc = privilegedContext(bot.agent_id, bot)}
                <div class="bot-card">
                  <div class="bot-card-head">
                    <div>
                      <strong>{botHeaderLabel(editing.id, bot, i)}</strong>
                      <span>{isTruthy(bot.outbound_only) ? 'scheduled output only' : (bot.bot_name || 'interactive bot mapping')}</span>
                    </div>
                    <button class="btn-danger tiny" type="button" on:click={() => removeBotMapping(i)}>Remove</button>
                  </div>
                  <!-- F-GUI-5 — per-bot privileged callout for multi-bot channels
                       (Slack, Discord, Google Chat, Teams). -->
                  {#if bpc}
                    <div class="privileged-callout compact" class:danger={bpc.productionBlock} class:warn={bpc.needsAck && !bpc.productionBlock}>
                      <strong>
                        {bpc.needsAck ? 'Privileged agent — needs ack' : 'Privileged exposure acknowledged'}
                      </strong>
                      <p>
                        {agentLabel(agents.find(a => a.id === bpc.agentId) || { id: bpc.agentId })}
                        can reach OS-level, file-write, or wildcard-MCP tools from messages on this shared bot.
                        {#if bpc.productionBlock}
                          <span class="privileged-prod">Production readiness will fail without ack.</span>
                        {/if}
                      </p>
                      <button class="btn-secondary tiny" type="button"
                              on:click={() => openSecurityDoctorForAgent(bpc.agentId)}>
                        Open Security Doctor
                      </button>
                    </div>
                  {/if}
                  <div class="bot-fields">
                    {#each editing.bot_schema as f}
                      {#if shouldShowChannelField(f, bot)}
                        <label class="field">
                          <span class="field-label">
                            {f.label}{#if f.required && !(editing.id === 'telegram' && f.key === 'agent_id' && isTruthy(bot.outbound_only))}<span class="req">*</span>{/if}
                          </span>
                          {#if f.type === 'password'}
                            <input type="password"
                              value={bot[f.key] || ''}
                              on:input={(e) => updateBotField(i, f.key, e.currentTarget.value)}
                              placeholder={bot[f.key] === '***' ? '•••• (unchanged)' : (f.help || '')} />
                          {:else if f.type === 'checkbox' || f.key === 'ignore_groups'}
                            <label class="check-row compact">
                              <input type="checkbox"
                                checked={isTruthy(bot[f.key])}
                                on:change={(e) => updateBotField(i, f.key, e.currentTarget.checked)} />
                              <span>{botCheckboxLabel(f)}</span>
                            </label>
                          {:else if f.key === 'agent_id' && agents.length}
                            <select
                              value={bot[f.key] || ''}
                              on:change={(e) => updateBotField(i, f.key, e.currentTarget.value)}
                            >
                              <option value="">Select an agent…</option>
                              {#each agents as agent}
                                <option value={agent.id}>{agentLabel(agent)}</option>
                              {/each}
                            </select>
                          {:else}
                            <input type="text"
                              value={bot[f.key] || ''}
                              on:input={(e) => updateBotField(i, f.key, e.currentTarget.value)}
                              placeholder={f.help || ''} />
                          {/if}
                          {#if f.help}<span class="field-help">{f.help}</span>{/if}
                        </label>
                      {/if}
                    {/each}
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/if}

      <div class="modal-row">
        <button class="btn-secondary" on:click={closeEdit} disabled={saving}>Cancel</button>
        {#if editing.id !== 'whatsapp_web' || advancedWhatsAppWeb}
          <button class="btn-primary" on:click={saveEdit} disabled={saving}>
            {saving ? 'Saving…' : editing.id === 'whatsapp_web' ? 'Save advanced' : 'Save'}
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .restart-banner { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .empty  { color: #6b7294; padding: 3rem; text-align: center; }
  .delivery-summary {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    padding: .85rem 1rem;
    border: 1px solid rgba(120,100,255,.22);
    border-radius: 8px;
    background: #12162a;
  }
  .delivery-summary.ok { border-color: rgba(76,175,130,.36); }
  .delivery-summary.warn { border-color: rgba(240,196,96,.36); }
  .delivery-summary.fail { border-color: rgba(240,96,96,.36); }
  .delivery-summary strong { display: block; font-size: .95rem; margin: .15rem 0; color: #f4f2ff; }
  .delivery-summary span { color: #9aa0c8; font-size: .8rem; }
  .summary-kicker { color: #7f8cff; text-transform: uppercase; letter-spacing: .08em; font-size: .68rem; font-weight: 700; }
  .summary-targets { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: .4rem; max-width: 50%; }
  .target-pill { border: 1px solid rgba(130,140,190,.22); background: #171c34; border-radius: 999px; padding: .22rem .5rem; font-size: .72rem; color: #aeb4dc; white-space: nowrap; }
  .target-pill.ok { border-color: rgba(76,175,130,.35); color: #6ee0a6; }
  .target-pill.warn { border-color: rgba(240,196,96,.38); color: #f0c460; }
  .target-pill.fail { border-color: rgba(240,96,96,.42); color: #ff7b7b; }

  .channel-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 1rem; }

  .ch-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; overflow: hidden; transition: border-color .2s;
  }
  .ch-card.enabled { border-color: rgba(108,99,255,.3); }

  .ch-header {
    display: flex; align-items: flex-start; gap: .75rem;
    padding: .9rem 1rem; border-bottom: 1px solid #1a1e36;
  }
  .ch-icon   { font-size: 1.4rem; line-height: 1.2; flex: 0 0 auto; }
  .ch-identity { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; }
  .ch-name   { font-weight: 600; font-size: .9rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ch-id     { font-size: .72rem; color: #555a7a; font-family: monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* A long status detail (e.g. the WhatsApp "scan QR" hint) must stay in its own
     column on the right and wrap within itself instead of overlapping the name. */
  .ch-badge  { flex: 0 1 auto; max-width: 45%; font-size: .7rem; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; text-align: right; line-height: 1.25; overflow-wrap: anywhere; }

  .ch-body   { flex: 1; padding: .75rem 1rem; display: flex; flex-direction: column; gap: .45rem; }
  .ch-row    { display: flex; justify-content: space-between; font-size: .82rem; gap: .5rem; }
  .ch-label  { color: #555a7a; flex-shrink: 0; }
  .ch-val    { color: #c8cadf; text-align: right; word-break: break-all; }
  .ch-val.mono { font-family: monospace; font-size: .78rem; color: #8b85ff; }
  .no-settings { color: #555a7a; font-style: italic; }

  .settings { margin-top: .35rem; padding-top: .5rem; border-top: 1px solid #1a1e36; display: flex; flex-direction: column; gap: .4rem; }
  .settings-title { font-size: .68rem; text-transform: uppercase; letter-spacing: .06em; color: #555a7a; font-weight: 600; }
  .mapping-row {
    display: flex; align-items: center; justify-content: space-between; gap: .55rem .75rem; flex-wrap: wrap;
    padding: .45rem .55rem; background: #101323; border: 1px solid #1a1e36; border-radius: 7px;
  }
  .mapping-row div { min-width: 0; display: flex; flex-direction: column; gap: .15rem; }
  .mapping-row code { color: #8b85ff; font-size: .72rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .mapping-row em { color: #6b7294; font-size: .66rem; font-family: monospace; font-style: normal; }
  .mapping-row span { color: #555a7a; font-size: .68rem; }
  .mapping-row strong { color: #c8cadf; font-size: .78rem; text-align: right; word-break: break-word; }
  .mapping-row strong.send-only { color: #4caf82; }
  .mapping-actions { display: flex; align-items: center; justify-content: flex-end; gap: .35rem; min-width: 7rem; }
  .mapping-actions strong { max-width: 9rem; }
  .mapping-actions .tier-chip {
    display: inline-flex; width: fit-content; padding: .1rem .4rem; border-radius: 999px;
    border: 1px solid #303656; background: #0d1020; color: #9aa2c9;
    font-size: .6rem; font-weight: 800; letter-spacing: .04em; text-transform: uppercase;
  }
  .mapping-actions .tier-chip.read { border-color: rgba(118,224,162,.35); color: #76e0a2; background: rgba(76,175,130,.08); }
  .mapping-actions .tier-chip.active-tier { border-color: rgba(139,133,255,.42); color: #aaa6ff; background: rgba(108,99,255,.1); }
  .mapping-actions .tier-chip.privileged { border-color: rgba(255,128,128,.45); color: #ff9b9b; background: rgba(255,90,90,.1); }
  .mapping-warning {
    flex: 1 0 100%;
    padding: .45rem .55rem;
    border-radius: 6px;
    border: 1px solid rgba(240,96,96,.32);
    background: rgba(240,96,96,.08);
    color: #ffb0b0;
    font-size: .72rem;
    line-height: 1.4;
  }
  .traffic-grid {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: .35rem;
  }
  .traffic-grid span {
    min-width: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: .08rem;
    padding: .35rem .25rem;
    border: 1px solid #1a1e36;
    border-radius: 6px;
    background: #101323;
  }
  .traffic-grid strong {
    color: #dfe2ff;
    font-size: .82rem;
    line-height: 1;
  }
  .traffic-grid em {
    color: #6b7294;
    font-size: .58rem;
    text-transform: uppercase;
    letter-spacing: .04em;
    font-style: normal;
    text-align: center;
  }
  .traffic-grid span.bad {
    border-color: rgba(240,96,96,.35);
    background: rgba(240,96,96,.07);
  }
  .traffic-grid span.bad strong,
  .traffic-grid span.bad em {
    color: #ff9b9b;
  }
  .diagnostics { margin-top: .35rem; padding-top: .5rem; border-top: 1px solid #1a1e36; display: flex; flex-direction: column; gap: .4rem; }
  .diag-row {
    display: grid; grid-template-columns: 3.8rem minmax(0,1fr); gap: .25rem .45rem;
    padding: .45rem .55rem; background: #101323; border: 1px solid #1a1e36; border-radius: 7px;
  }
  .diag-row strong { font-size: .62rem; text-transform: uppercase; letter-spacing: .05em; }
  .diag-row span { color: #c8cadf; font-size: .76rem; line-height: 1.35; }
  .diag-row em { grid-column: 2; color: #7b82a8; font-size: .7rem; font-style: normal; line-height: 1.35; }
  .diag-row.fail { border-color: rgba(240,96,96,.4); background: rgba(240,96,96,.08); }
  .diag-row.fail strong { color: #f06060; }
  .diag-row.warn { border-color: rgba(240,196,96,.36); background: rgba(240,196,96,.07); }
  .diag-row.warn strong { color: #f0c460; }
  .diag-row.info strong { color: #8b85ff; }

  /* Delivery doctor result panel */
  .diag-result { margin-top: .5rem; padding: .55rem .7rem; border-radius: 8px; border: 1px solid #2a2f52; background: #131735; }
  .diag-result.ok { border-color: rgba(96,200,120,.4); background: rgba(96,200,120,.08); }
  .diag-result.bad { border-color: rgba(240,96,96,.4); background: rgba(240,96,96,.08); }
  .diag-head { display: flex; align-items: center; justify-content: space-between; }
  .diag-head strong { font-size: .72rem; letter-spacing: .02em; }
  .diag-result.ok .diag-head strong { color: #60c878; }
  .diag-result.bad .diag-head strong { color: #f06060; text-transform: capitalize; }
  .diag-result p { margin: .35rem 0 0; color: #c8cadf; font-size: .78rem; line-height: 1.4; }
  .diag-result p.diag-fix { color: #a7d3ff; }
  .diag-result details { margin-top: .4rem; }
  .diag-result summary { cursor: pointer; color: #7b82a8; font-size: .7rem; }
  .diag-result code { display: block; margin-top: .3rem; color: #9aa0c2; font-size: .7rem; word-break: break-word; }
  .diag-x { background: none; border: none; color: #7b82a8; font-size: 1rem; cursor: pointer; line-height: 1; padding: 0 .2rem; }
  .diag-x:hover { color: #c8cadf; }

  .ch-footer { padding: .75rem 1rem; border-top: 1px solid #1a1e36; display: flex; gap: .5rem; justify-content: flex-end; }
  .small-btn { padding: .35rem .9rem; font-size: .8rem; border-radius: 6px; }
  .ch-footer button { padding: .35rem .9rem; font-size: .8rem; border-radius: 6px; }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .5rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  /* Modal */
  .modal-bg {
    position: fixed; inset: 0; background: rgba(0,0,0,.65);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 760px; max-width: 92vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .fields { display: flex; flex-direction: column; gap: .85rem; }
  .field  { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .8rem; color: #c8cadf; }
  .field select {
    background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a;
    border-radius: 6px; padding: .5rem .65rem; font-size: .85rem;
  }
  .pair-panel { display: flex; flex-direction: column; gap: 1rem; }
  .safety-panel {
    display: flex; flex-direction: column; gap: .75rem;
    background: rgba(240,196,96,.07); border: 1px solid rgba(240,196,96,.28);
    border-radius: 8px; padding: .85rem;
  }
  .safety-panel strong { color: #f0c460; font-size: .86rem; }
  .check-row { display: flex; align-items: center; gap: .55rem; color: #c8cadf; font-size: .84rem; }
  .check-row.compact { background: #1c1f35; border: 1px solid #2a2f4a; border-radius: 6px; padding: .5rem .65rem; }
  .check-row input { width: 1rem; height: 1rem; accent-color: #6c63ff; }
  .pair-status {
    display: flex; flex-direction: column; gap: .25rem;
    background: #101323; border: 1px solid #2a2f4a; border-radius: 8px;
    padding: .85rem;
  }
  .pair-status strong { color: #e8eaf6; font-size: .9rem; }
  .pair-status span { color: #7b82a8; font-size: .8rem; line-height: 1.45; }
  .pair-status.live { border-color: rgba(76,175,130,.45); background: rgba(76,175,130,.08); }
  .pair-status.waiting { border-color: rgba(240,196,96,.45); background: rgba(240,196,96,.08); }
  .qr-preview {
    display: grid; grid-template-columns: 256px minmax(0, 1fr); align-items: center; gap: 1rem;
    background: #101323; border: 1px solid #1a1e36; border-radius: 8px; padding: 1rem;
  }
  .qr-preview img { width: 256px; height: 256px; border-radius: 8px; background: #fff; }
  .qr-preview p { margin: 0; color: #c8cadf; font-size: .88rem; line-height: 1.55; }
  .pair-actions { display: flex; gap: .75rem; align-items: center; flex-wrap: wrap; }
  .advanced-panel {
    border-top: 1px solid #2a2f4a; padding-top: 1rem;
    display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .85rem;
  }
  .req { color: #f06060; margin-left: .15rem; }
  .field-help { font-size: .72rem; color: #555a7a; }
  .modal-row {
    display: flex; gap: .75rem; justify-content: flex-end;
    position: sticky; bottom: 0; z-index: 5;
    background: #141626; padding-top: .6rem;
    box-shadow: 0 -10px 12px -10px rgba(0, 0, 0, 0.6);
  }
  .tiny { padding: .22rem .5rem; font-size: .72rem; border-radius: 5px; }
  .bot-editor { border-top: 1px solid #2a2f4a; padding-top: 1rem; display: flex; flex-direction: column; gap: .85rem; }
  .bot-editor-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .bot-editor-actions { display: flex; gap: .5rem; flex-wrap: wrap; justify-content: flex-end; }
  .bot-editor h3 { font-size: .9rem; font-weight: 600; }
  .bot-editor p { font-size: .76rem; color: #7b82a8; margin-top: .2rem; }
  .bot-empty { color: #6b7294; background: #101323; border: 1px dashed #2a2f4a; border-radius: 8px; padding: .8rem; font-size: .8rem; }
  .bot-list { display: flex; flex-direction: column; gap: .8rem; }
  .bot-card { background: #101323; border: 1px solid #1a1e36; border-radius: 8px; padding: .85rem; display: flex; flex-direction: column; gap: .75rem; }
  .bot-card-head { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .bot-card-head div { display: flex; flex-direction: column; gap: .1rem; min-width: 0; }
  .bot-card-head strong { font-family: monospace; color: #8b85ff; font-size: .8rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bot-card-head span { color: #555a7a; font-size: .68rem; }
  .bot-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .75rem; }

  @media (max-width: 720px) {
    .qr-preview { grid-template-columns: 1fr; justify-items: center; text-align: center; }
    .advanced-panel { grid-template-columns: 1fr; }
  }

  /* Inline setup guides */
  .guide {
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 8px;
    padding: .35rem .7rem; margin-bottom: .9rem;
  }
  .guide.open { padding-bottom: .7rem; }
  .guide-toggle {
    background: none; border: none; cursor: pointer;
    color: #8b85ff; font-size: .85rem; font-weight: 600; padding: .25rem 0;
  }
  .guide-intro { color: #c8cadf; font-size: .82rem; line-height: 1.5; margin: .3rem 0 .5rem; }
  .guide-steps { margin: 0 0 .5rem 1.1rem; padding: 0; }
  .guide-steps li { color: #b0b5d8; font-size: .8rem; line-height: 1.55; margin-bottom: .3rem; }
  .guide-test { color: #4caf82; font-size: .78rem; line-height: 1.5; margin: .4rem 0 0; }
  .guide-warning {
    color: #f0a060; font-size: .78rem; line-height: 1.5; margin: .45rem 0 0;
    border-left: 2px solid #f0a060; padding-left: .55rem;
  }
  .guide-hint { color: #7b82a8; }

  /* F-GUI-5 — privileged-exposure callout styling. Warn = amber, production
     unaccepted = red (matches S4 verdict escalation). */
  .privileged-callout {
    background: rgba(245,167,66,.08);
    border: 1px solid rgba(245,167,66,.3);
    border-radius: 8px;
    padding: .7rem .85rem;
    margin: .5rem 0 1rem;
    color: #e7e8f5;
    display: flex; flex-direction: column; gap: .35rem;
    font-size: .84rem; line-height: 1.5;
  }
  .privileged-callout.compact { margin: .35rem 0 .55rem; padding: .55rem .7rem; font-size: .78rem; }
  .privileged-callout strong { color: #f5bd67; font-weight: 600; }
  .privileged-callout.warn { border-color: rgba(245,167,66,.4); }
  .privileged-callout.danger {
    background: rgba(240,96,96,.08);
    border-color: rgba(240,96,96,.4);
  }
  .privileged-callout.danger strong { color: #f06060; }
  .privileged-callout p { margin: 0; color: #c8cadf; }
  .privileged-callout code { color: #ada8ff; }
  .privileged-callout em { color: #f5bd67; font-style: normal; }
  .privileged-callout.danger em { color: #f06060; }
  .privileged-prod { color: #f06060; font-weight: 600; margin-left: .3rem; }
  .privileged-callout .btn-secondary { align-self: flex-start; }
</style>
