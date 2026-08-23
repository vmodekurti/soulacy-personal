<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { installPrompt, appInstalled, authRequired } from '../lib/stores.js'

  let loading = true
  let error = ''
  let health = null
  let schedule = []
  let agents = []
  let agentInterfaces = {}
  let doctor = null
  let learning = null
  let queues = []
  let readiness = null
  let companionStatus = null
  let channels = []
  let recentErrors = []
  let refreshingError = ''
  let activeRuns = []          // in-progress runs (scheduler running snapshot)
  let installEvt = null        // deferred PWA install prompt
  let installing = false
  let scheduleBusy = ''
  let channelBusy = ''
  let channelDiagnoses = {}
  let recentRuns = []
  let recentRunsError = ''
  let pocketAgentId = ''
  let pocketText = ''
  let pocketBusy = false
  let pocketReply = ''
  let pocketError = ''
  let pocketSessionId = ''

  $: installEvt = $installPrompt
  $: sessionExpired = $authRequired

  async function loadActiveRuns() {
    try {
      const st = await api.schedule.status()
      const running = st?.running || {}
      activeRuns = Object.entries(running).map(([agentId, started]) => ({ agentId, started }))
    } catch { activeRuns = [] }
  }

  async function installApp() {
    if (!installEvt) return
    installing = true
    try {
      installEvt.prompt()
      await installEvt.userChoice
      installPrompt.set(null)
    } catch (_) { /* user dismissed */ } finally { installing = false }
  }

  // Companion: approvals, push, pairing.
  let approvals = []
  let approvalBusy = ''
  let pushState = 'unknown'   // unknown | unsupported | denied | off | on | working
  let pairCode = null
  let pairUrl = ''
  let pairQr = ''
  let redeemCode = ''
  let redeemMsg = ''
  let companionMsg = ''

  async function loadApprovals() {
    try {
      const res = await api.approvals.list()
      approvals = res.approvals || []
    } catch { approvals = [] }
  }

  async function resolveApproval(id, approve) {
    approvalBusy = id
    try {
      if (approve) await api.approvals.approve(id)
      else await api.approvals.deny(id)
      approvals = approvals.filter(a => a.call_id !== id)
    } catch (e) { companionMsg = e.message } finally { approvalBusy = '' }
  }

  function urlBase64ToUint8Array(base64String) {
    const padding = '='.repeat((4 - (base64String.length % 4)) % 4)
    const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
    const raw = atob(base64)
    const arr = new Uint8Array(raw.length)
    for (let i = 0; i < raw.length; i++) arr[i] = raw.charCodeAt(i)
    return arr
  }

  async function detectPush() {
    if (!('serviceWorker' in navigator) || !('PushManager' in window)) { pushState = 'unsupported'; return }
    if (Notification.permission === 'denied') { pushState = 'denied'; return }
    try {
      const reg = await navigator.serviceWorker.getRegistration()
      const sub = reg && await reg.pushManager.getSubscription()
      pushState = sub ? 'on' : 'off'
    } catch { pushState = 'off' }
  }

  async function enablePush() {
    pushState = 'working'
    try {
      const perm = await Notification.requestPermission()
      if (perm !== 'granted') { pushState = 'denied'; return }
      const reg = await navigator.serviceWorker.register('/sw.js')
      await navigator.serviceWorker.ready
      const { public_key } = await api.push.publicKey()
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(public_key),
      })
      await api.push.subscribe(sub.toJSON())
      pushState = 'on'
      companionMsg = 'Notifications enabled on this device.'
    } catch (e) {
      pushState = 'off'
      companionMsg = 'Could not enable notifications: ' + (e.message || e)
    }
  }

  async function makePairCode() {
    try {
      const res = await api.pairing.createToken()
      pairCode = res.code
      pairUrl = res.pair_url || ''
      pairQr = ''
      // Render a scannable QR of the pair URL. qrcode is code-split, so it only
      // loads the first time someone pairs a device.
      try {
        const QR = (await import('qrcode')).default
        pairQr = await QR.toDataURL(pairUrl || pairCode, { margin: 1, width: 200 })
      } catch (_) { /* text code still works as a fallback */ }
    } catch (e) { companionMsg = e.message }
  }

  async function redeemPair() {
    if (!redeemCode.trim()) return
    redeemMsg = ''
    try {
      const res = await api.pairing.redeem(redeemCode.trim())
      redeemMsg = res.paired ? '✓ Paired.' + (res.token ? ' Token issued.' : '') : 'Pairing failed.'
      if (res.token) { try { sessionStorage.setItem('soulacy-mobile-token', res.token); localStorage.removeItem('soulacy-mobile-token') } catch (_) {} }
      redeemCode = ''
    } catch (e) { redeemMsg = e.message }
  }

  async function load() {
    loading = true
    error = ''
    loading = true
    error = ''
    refreshingError = ''
    try {
      const [h, s, a, d, l, q, r, ch, m] = await Promise.allSettled([
        api.health(),
        api.schedule.list(),
        api.agents.list(),
        api.providers.doctor(),
        api.brainMemory.learningSummary(),
        api.queues.names(),
        api.readiness(),
        api.channels.list(),
        api.mobile.status(),
      ])
      if (h.status === 'fulfilled') health = h.value
      if (s.status === 'fulfilled') schedule = s.value?.schedule || []
      if (a.status === 'fulfilled') {
        agents = a.value?.agents || []
        agentInterfaces = a.value?.interfaces || {}
      }
      if (d.status === 'fulfilled') doctor = d.value
      if (l.status === 'fulfilled') learning = l.value
      if (q.status === 'fulfilled') queues = q.value?.queues || []
      if (r.status === 'fulfilled') readiness = r.value
      if (ch.status === 'fulfilled') channels = ch.value?.channels || []
      if (m.status === 'fulfilled') companionStatus = m.value
      const failed = [h, s, a, d, l, q, r, ch, m].find(x => x.status === 'rejected')
      if (failed) error = failed.reason?.message || 'Some mobile data could not be loaded'
      const loadedAgents = a.status === 'fulfilled' ? (a.value?.agents || []) : agents
      await Promise.all([loadRecentErrors(loadedAgents), loadRecentRuns(loadedAgents)])
    } finally {
      loading = false
    }
  }

  async function loadRecentErrors(agentList) {
    const enabled = (agentList || []).filter(a => a.enabled).slice(0, 8)
    const results = await Promise.allSettled(enabled.map(async (agent) => {
      const res = await api.agents.actions(agent.id, 12, 'error', { durable: true })
      return (res.events || []).map(ev => ({ ...ev, agent_id: agent.id, agent_name: agent.name || agent.id }))
    }))
    recentErrors = results
      .filter(r => r.status === 'fulfilled')
      .flatMap(r => r.value)
      .sort((a, b) => String(b.ts || b.time || '').localeCompare(String(a.ts || a.time || '')))
      .slice(0, 6)
    const failed = results.find(r => r.status === 'rejected')
    if (failed) refreshingError = failed.reason?.message || 'Recent errors could not be loaded'
  }

  async function loadRecentRuns(agentList) {
    recentRunsError = ''
    const enabled = (agentList || []).filter(a => a.enabled).slice(0, 10)
    const results = await Promise.allSettled(enabled.map(async (agent) => {
      const res = await api.studio.runHistory(agent.id)
      return (res.runs || []).map(run => ({
        agent_id: agent.id,
        agent_name: agent.name || agent.id,
        run_id: run.runId || run.sessionId || '',
        session_id: run.sessionId || run.runId || '',
        started_at: run.startedAt || run.updatedAt || '',
        updated_at: run.updatedAt || run.startedAt || '',
        trigger: run.trigger || run.source || '',
        status: run.status || (run.ok ? 'success' : 'failed'),
        ok: !!run.ok,
        steps: run.steps || 0,
        error: run.error || '',
        delivery_status: run.deliveryStatus || '',
      }))
    }))
    recentRuns = results
      .filter(r => r.status === 'fulfilled')
      .flatMap(r => r.value)
      .sort((a, b) => String(b.started_at || b.updated_at || '').localeCompare(String(a.started_at || a.updated_at || '')))
      .slice(0, 8)
    const failed = results.find(r => r.status === 'rejected')
    if (failed) recentRunsError = failed.reason?.message || 'Recent runs could not be loaded'
  }

  function go(page) {
    window.location.hash = '#' + page
  }

  function openChat(agentId = '') {
    const q = agentId ? `?agent_id=${encodeURIComponent(agentId)}` : ''
    window.location.hash = `#chat${q}`
  }

  function scheduleAgentId(item) {
    return item?.agent_id || item?.id || item?.agentId || ''
  }

  function openActivity(agentId = '') {
    window.location.hash = agentId ? `#activity?agent_id=${encodeURIComponent(agentId)}` : '#activity'
  }

  function openRun(run) {
    const agentId = run?.agent_id || ''
    const sessionId = run?.session_id || run?.run_id || ''
    if (!agentId) { openActivity(); return }
    const q = new URLSearchParams({ agent_id: agentId })
    if (sessionId) q.set('session_id', sessionId)
    window.location.hash = `#activity?${q.toString()}`
  }

  async function runScheduleNow(item) {
    const id = scheduleAgentId(item)
    if (!id) return
    scheduleBusy = `run:${id}`
    try {
      await api.agents.trigger(id)
      companionMsg = `Started ${item.name || id}.`
      await loadActiveRuns()
    } catch (e) {
      companionMsg = e.message || String(e)
    } finally {
      scheduleBusy = ''
    }
  }

  async function testScheduleDelivery(item) {
    const id = scheduleAgentId(item)
    if (!id) return
    scheduleBusy = `test:${id}`
    try {
      await api.agents.testScheduleOutput(id)
      companionMsg = `Sent a delivery test for ${item.name || id}.`
    } catch (e) {
      companionMsg = e.message || String(e)
    } finally {
      scheduleBusy = ''
    }
  }

  function isChatEligible(agent) {
    const iface = agentInterfaces?.[agent?.id]
    if (iface && typeof iface.chat_eligible === 'boolean') return iface.chat_eligible
    return agent?.trigger !== 'cron' && agent?.trigger !== 'oneshot'
  }

  // `eligible` is passed in by the reactive statement below rather than read
  // from the closure. That is load-bearing: `$: ensurePocketAgent()` with no
  // argument gives Svelte no dependency it can see, so the compiler emits the
  // call once in the instance body instead of inside $$self.$$.update. It then
  // ran during init — before the reactive `chatAgents` declaration had been
  // assigned, so `chatAgents[0]` threw and wedged the whole app — and never ran
  // again when the agent list arrived, so the picker stayed empty. Passing the
  // list makes it a real dependency, which fixes both halves.
  function ensurePocketAgent(eligible) {
    const list = eligible || []
    if (pocketAgentId && list.some(a => a.id === pocketAgentId)) return
    pocketAgentId = list[0]?.id || ''
  }

  async function sendPocketChat() {
    const text = pocketText.trim()
    if (!text || !pocketAgentId || pocketBusy) return
    pocketBusy = true
    pocketError = ''
    pocketReply = ''
    try {
      const res = await api.chat(pocketAgentId, text, 'mobile-user', null, pocketSessionId)
      pocketReply = res?.reply || 'Done.'
      pocketSessionId = res?.session_id || pocketSessionId
      pocketText = ''
      companionMsg = 'Pocket chat finished.'
      await Promise.all([loadActiveRuns(), loadRecentRuns(agents)])
    } catch (e) {
      pocketError = e.message || String(e)
    } finally {
      pocketBusy = false
    }
  }

  function pocketKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      sendPocketChat()
    }
  }

  function adapterIdFor(ch, target = null) {
    return target?.adapter_id || target?._adapter_id || ch?.id || ''
  }

  function channelTargetLabel(ch, target = null) {
    if (target) return target.bot_name || target.agent_id || adapterIdFor(ch, target)
    const to = ch?.settings?.default_output_to
    return to ? `default → ${to}` : 'default output'
  }

  function channelSeverity(ch) {
    const diagnostics = ch?.diagnostics || []
    if (diagnostics.some(d => d.severity === 'fail')) return 'fail'
    if (diagnostics.some(d => d.severity === 'warn')) return 'warn'
    if (ch?.enabled && ch?.registered && ch?.status?.connected) return 'ok'
    if (ch?.enabled || ch?.configured) return 'warn'
    return 'off'
  }

  function channelIssueText(ch) {
    const d = (ch?.diagnostics || []).find(x => x.severity === 'fail') ||
      (ch?.diagnostics || []).find(x => x.severity === 'warn') ||
      (ch?.diagnostics || [])[0]
    if (d) return d.message
    if (ch?.status?.detail) return ch.status.detail
    if (ch?.enabled && ch?.registered && ch?.status?.connected) return 'Ready to deliver.'
    if (!ch?.enabled) return 'Disabled.'
    return 'Needs a check.'
  }

  function channelTargets(ch) {
    const rows = []
    if (ch?.settings?.default_output_to || ch?.registered) {
      rows.push(null)
    }
    for (const bot of ch?.bots || []) rows.push(bot)
    return rows.slice(0, 3)
  }

  async function diagnoseDelivery(ch, target = null, dry = true) {
    const adapterId = adapterIdFor(ch, target)
    const key = `${dry ? 'diag' : 'test'}:${adapterId}`
    channelBusy = key
    try {
      const res = dry
        ? await api.channels.diagnose(ch.id, { adapter_id: adapterId, dry: true })
        : await api.channels.test(ch.id, { adapter_id: adapterId })
      if (dry) {
        channelDiagnoses = { ...channelDiagnoses, [adapterId]: { ...(res.diagnosis || {}), to: res.to } }
        companionMsg = `${ch.name || ch.id}: ${res.diagnosis?.reason || 'delivery checked'}`
      } else {
        companionMsg = `Sent test through ${res.channel}${res.to ? ` to ${res.to}` : ''}.`
      }
    } catch (e) {
      channelDiagnoses = { ...channelDiagnoses, [adapterId]: { ok: false, reason: e.message || String(e), fix: '' } }
      companionMsg = e.message || String(e)
    } finally {
      channelBusy = ''
    }
  }

  $: providerIssues = (doctor?.providers || []).filter(x => x.status !== 'ok')
  $: channelIssues = (doctor?.channels || []).filter(x => x.status !== 'ok')
  $: deliveryChannels = channels.filter(ch => ch.id !== 'http' && (ch.configured || ch.enabled || ch.registered || (ch.bots || []).length)).slice(0, 5)
  $: enabledAgents = agents.filter(a => a.enabled)
  $: chatAgents = enabledAgents.filter(isChatEligible)
  $: ensurePocketAgent(chatAgents)
  $: riskCount = providerIssues.length + channelIssues.length + recentErrors.length
  $: pendingLearning = learning?.pending || 0
  $: readinessSummary = readiness?.summary || {}
  $: readinessStatus = readinessSummary.status || ''
  $: readinessLabel = readinessStatus === 'ready' ? 'Ready' : readinessStatus === 'at_risk' ? 'At risk' : 'Needs setup'
  $: nextActions = readiness?.next_actions || []

  function eventText(ev) {
    if (!ev) return ''
    const p = ev.payload || {}
    if (typeof p === 'string') return p
    return p.error || p.message || p.line || ev.message || JSON.stringify(p)
  }

  function eventTime(ev) {
    const raw = ev.ts || ev.time || ev.created_at || ''
    if (!raw) return ''
    const d = new Date(raw)
    if (Number.isNaN(d.getTime())) return raw
    return d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  }

  function runTime(run) {
    const raw = run?.started_at || run?.updated_at || ''
    if (!raw) return ''
    const d = new Date(raw)
    if (Number.isNaN(d.getTime())) return raw
    return d.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
  }

  function runSummary(run) {
    const bits = []
    if (run?.trigger) bits.push(run.trigger)
    if (run?.steps) bits.push(`${run.steps} step${run.steps === 1 ? '' : 's'}`)
    if (run?.delivery_status) bits.push(`delivery ${run.delivery_status}`)
    if (run?.error) bits.push(run.error)
    return bits.join(' · ') || (run?.status || 'run')
  }

  onMount(() => {
    load()
    loadApprovals()
    loadActiveRuns()
    detectPush()
    const t = setInterval(() => { loadApprovals(); loadActiveRuns() }, 10000)
    return () => clearInterval(t)
  })

  function fmtStarted(iso) {
    if (!iso) return ''
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return ''
    const secs = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000))
    if (secs < 60) return `${secs}s`
    return `${Math.round(secs / 60)}m`
  }
</script>

<div class="mobile-page">
  <header>
    <div>
      <span>Companion</span>
      <h1>Operations</h1>
    </div>
    <button class="btn-secondary" on:click={() => { load(); loadApprovals() }} disabled={loading}>↻</button>
    <TourButton />
  </header>

  {#if sessionExpired}
    <div class="m-banner err">
      Your session expired. <button class="linkish" on:click={() => location.reload()}>Sign in again</button>
    </div>
  {/if}

  {#if installEvt && !$appInstalled}
    <div class="m-banner">
      <span>Install Soulacy for quick access and notifications.</span>
      <button class="btn-primary tiny" on:click={installApp} disabled={installing}>{installing ? 'Installing…' : 'Install app'}</button>
    </div>
  {/if}

  {#if companionMsg}<button class="companion-note" type="button" on:click={() => companionMsg = ''}>{companionMsg}</button>{/if}

  {#if companionStatus}
    <section class="companion-readiness {companionStatus.status || 'warn'}">
      <div class="companion-readiness-top">
        <div>
          <span class="kicker">Companion Readiness</span>
          <h2>{companionStatus.score || 0}% · {companionStatus.ready || 0}/{companionStatus.total || 0} checks ready</h2>
        </div>
        <button class="btn-secondary small" on:click={() => go('channels')}>Delivery</button>
      </div>
      <div class="companion-pills">
        <span>{companionStatus.push_subscriptions || 0} devices</span>
        <span>{companionStatus.chat_agents || 0} chat agents</span>
        <span>{companionStatus.scheduled_agents || 0} schedules</span>
        <span>{companionStatus.delivery_channels || 0} channels</span>
        <span>{companionStatus.recent_runs || 0} runs</span>
        <span>{companionStatus.installable ? 'installable' : 'browser only'}</span>
      </div>
      {#if companionStatus.next_actions?.length}
        <div class="companion-next">{companionStatus.next_actions[0]}</div>
      {/if}
      <div class="companion-checks">
        {#each (companionStatus.checks || []).slice(0, 6) as check}
          <button class="companion-check {check.status}" type="button" on:click={() => check.key === 'delivery' ? go('channels') : check.key === 'schedules' ? go('schedule') : check.key === 'chat_agents' ? go('agents') : check.key === 'run_review' ? go('activity') : null}>
            <span>{check.label}</span>
            <small>{check.detail}</small>
          </button>
        {/each}
      </div>
    </section>
  {/if}

  <section class="m-card pocket-chat">
    <div class="m-card-hd split">
      <span>Pocket chat</span>
      <button class="btn-secondary small" type="button" on:click={() => openChat(pocketAgentId)}>Full chat</button>
    </div>
    {#if !chatAgents.length}
      <p class="m-empty">No chat-ready agents are enabled.</p>
    {:else}
      <div class="pocket-controls">
        <select bind:value={pocketAgentId} aria-label="Pocket chat agent">
          {#each chatAgents as agent}
            <option value={agent.id}>{agent.name || agent.id}</option>
          {/each}
        </select>
        <textarea
          bind:value={pocketText}
          on:keydown={pocketKeydown}
          rows="2"
          placeholder="Ask an agent or send a quick instruction…"
          aria-label="Pocket chat message"
        ></textarea>
        <button class="btn-primary" type="button" on:click={sendPocketChat} disabled={pocketBusy || !pocketText.trim() || !pocketAgentId}>
          {pocketBusy ? 'Sending…' : 'Send'}
        </button>
      </div>
      {#if pocketError}
        <button class="pocket-result err" type="button" on:click={() => openActivity(pocketAgentId)}>
          <strong>Needs attention</strong>
          <small>{pocketError}</small>
        </button>
      {:else if pocketReply}
        <button class="pocket-result" type="button" on:click={() => openChat(pocketAgentId)}>
          <strong>Latest reply</strong>
          <small>{pocketReply}</small>
        </button>
      {/if}
    {/if}
  </section>

  <!-- Active runs -->
  <section class="m-card" class:active={activeRuns.length}>
    <div class="m-card-hd">
      <span>Active runs</span>
      {#if activeRuns.length}<span class="m-badge">{activeRuns.length}</span>{/if}
    </div>
    {#if activeRuns.length === 0}
      <p class="m-empty">No runs in progress.</p>
    {:else}
      {#each activeRuns as r}
        <button class="run-row" type="button" on:click={() => openActivity(r.agentId)}>
          <span class="run-dot" aria-hidden="true"></span>
          <strong>{r.agentId}</strong>
          <span class="run-age">running {fmtStarted(r.started)}</span>
        </button>
      {/each}
    {/if}
  </section>

  <!-- Approvals -->
  <section class="m-card approvals-card" class:active={approvals.length}>
    <div class="m-card-hd">
      <span>Approvals</span>
      {#if approvals.length}<span class="m-badge urgent">{approvals.length}</span>{/if}
    </div>
    {#if approvals.length === 0}
      <p class="m-empty">Nothing waiting on you.</p>
    {:else}
      {#each approvals as a}
        <div class="approval">
          <div class="approval-info">
            <div class="approval-tool">{a.tool}</div>
            <div class="approval-reason">{a.reason || 'wants to run'}{a.agent_id ? ' · ' + a.agent_id : ''}</div>
          </div>
          <div class="approval-actions">
            <button class="approve" on:click={() => resolveApproval(a.call_id, true)} disabled={approvalBusy === a.call_id}>Approve</button>
            <button class="deny" on:click={() => resolveApproval(a.call_id, false)} disabled={approvalBusy === a.call_id}>Deny</button>
          </div>
        </div>
      {/each}
    {/if}
  </section>

  <!-- Notifications + pairing -->
  <section class="m-card">
    <div class="m-card-hd"><span>This device</span></div>
    <div class="device-row">
      <div>
        <div class="device-label">Push notifications</div>
        <div class="device-sub">
          {#if pushState === 'on'}Enabled{:else if pushState === 'unsupported'}Not supported in this browser{:else if pushState === 'denied'}Blocked in browser settings{:else}Off{/if}
        </div>
      </div>
      {#if pushState === 'off'}
        <button class="btn-primary small" on:click={enablePush}>Enable</button>
      {:else if pushState === 'working'}
        <button class="btn-secondary small" disabled>…</button>
      {:else if pushState === 'on'}
        <button class="btn-secondary small" on:click={() => api.push.test()}>Test</button>
      {/if}
    </div>

    <div class="device-row">
      <div>
        <div class="device-label">Pair a phone</div>
        <div class="device-sub">Generate a code, then enter it on the other device below.</div>
      </div>
      <button class="btn-secondary small" on:click={makePairCode}>Get code</button>
    </div>
    {#if pairCode}
      {#if pairQr}<img class="pair-qr" src={pairQr} alt="Pairing QR code" />{/if}
      <div class="pair-code">{pairCode}</div>
      {#if pairUrl}<div class="pair-url">{pairUrl}</div>{/if}
    {/if}

    <div class="device-row redeem-row">
      <input class="redeem-input" placeholder="Enter pairing code" bind:value={redeemCode} on:keydown={(e) => e.key === 'Enter' && redeemPair()} />
      <button class="btn-secondary small" on:click={redeemPair} disabled={!redeemCode.trim()}>Pair</button>
    </div>
    {#if redeemMsg}<div class="redeem-msg">{redeemMsg}</div>{/if}
  </section>

  {#if error}<div class="banner err">{error}</div>{/if}

  <section class="hero">
    <div>
      <strong>{riskCount ? `${riskCount} item${riskCount === 1 ? '' : 's'} need attention` : health ? 'Gateway online' : loading ? 'Checking gateway…' : 'Gateway unknown'}</strong>
      <small>{enabledAgents.length} enabled agents · {schedule.length} scheduled jobs · {queues.length} queues</small>
    </div>
    <span class:ok={health && !riskCount} class:warn={health && riskCount}>{health ? (riskCount ? 'Review' : 'Live') : 'Check'}</span>
  </section>

  {#if readiness}
    <section class="launch-card">
      <div class="launch-top">
        <div>
          <span class="kicker">Launch Readiness</span>
          <h2>{readinessSummary.score || 0}% · {readinessLabel}</h2>
        </div>
        <button class="btn-secondary small" on:click={() => go('dashboard')}>Details</button>
      </div>
      <div class="launch-stats">
        <span>{readinessSummary.providers_ready || 0} providers</span>
        <span>{readinessSummary.enabled_agents || 0} agents</span>
        <span>{readinessSummary.channels_ready || 0} channels</span>
      </div>
      {#if nextActions.length}
        <div class="list compact">
          {#each nextActions.slice(0, 3) as action}
            <button on:click={() => action.href ? (window.location.hash = action.href) : go('dashboard')}>
              <strong>{action.label}</strong>
              <small>{action.detail}</small>
            </button>
          {/each}
        </div>
      {:else}
        <p class="empty good">Core launch checks are passing.</p>
      {/if}
    </section>
  {/if}

  <div class="quick-grid">
    <button on:click={() => go('chat')}>Chat</button>
    <button on:click={() => go('activity')}>Runs</button>
    <button on:click={() => go('schedule')}>Automations</button>
    <button on:click={() => go('channels')}>Delivery</button>
    <button on:click={() => go('queues')}>Queues</button>
    <button on:click={() => go('memory')}>Learning</button>
    <button on:click={() => go('providers')}>Providers</button>
    <button on:click={() => go('studio')}>Studio</button>
  </div>

  <section>
    <div class="section-title-row">
      <h2>Delivery health</h2>
      <button class="btn-secondary small" on:click={() => go('channels')}>Configure</button>
    </div>
    {#if loading}
      <p class="empty">Loading…</p>
    {:else if !deliveryChannels.length}
      <p class="empty">No configured delivery channels yet.</p>
    {:else}
      <div class="list delivery-list">
        {#each deliveryChannels as ch}
          <div class="delivery-card">
            <button class="delivery-main" type="button" on:click={() => go('channels')}>
              <span class="status-dot {channelSeverity(ch)}" aria-hidden="true"></span>
              <span>
                <strong>{ch.name || ch.id}</strong>
                <small>{channelIssueText(ch)}</small>
              </span>
            </button>
            <div class="delivery-targets">
              {#each channelTargets(ch) as target}
                {@const adapterId = adapterIdFor(ch, target)}
                <div class="delivery-target">
                  <button class="target-main" type="button" on:click={() => diagnoseDelivery(ch, target, true)}>
                    <strong>{channelTargetLabel(ch, target)}</strong>
                    {#if channelDiagnoses[adapterId]}
                      <small class:good={channelDiagnoses[adapterId].ok}>
                        {channelDiagnoses[adapterId].ok ? 'Ready' : (channelDiagnoses[adapterId].category || 'Needs fix')}
                        {#if channelDiagnoses[adapterId].to} · {channelDiagnoses[adapterId].to}{/if}
                      </small>
                    {:else}
                      <small>{target?._connected === false ? 'mapping offline' : 'tap Check before sending'}</small>
                    {/if}
                  </button>
                  <div class="target-actions">
                    <button type="button" on:click={() => diagnoseDelivery(ch, target, true)} disabled={channelBusy === `diag:${adapterId}`}>
                      {channelBusy === `diag:${adapterId}` ? 'Checking…' : 'Check'}
                    </button>
                    <button type="button" on:click={() => diagnoseDelivery(ch, target, false)} disabled={channelBusy === `test:${adapterId}`}>
                      {channelBusy === `test:${adapterId}` ? 'Sending…' : 'Send test'}
                    </button>
                  </div>
                  {#if channelDiagnoses[adapterId]?.fix && !channelDiagnoses[adapterId]?.ok}
                    <p class="delivery-fix">{channelDiagnoses[adapterId].fix}</p>
                  {/if}
                </div>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <section>
    <h2>Learning loop</h2>
    <div class="metrics">
      <button on:click={() => go('memory')}>
        <strong>{pendingLearning}</strong>
        <small>pending proposals</small>
      </button>
      <button on:click={() => go('queues')}>
        <strong>{queues.length}</strong>
        <small>active queues</small>
      </button>
      <button on:click={() => go('activity')}>
        <strong>{recentErrors.length}</strong>
        <small>recent errors</small>
      </button>
    </div>
  </section>

  <section>
    <div class="section-title-row">
      <h2>Recent runs</h2>
      <button class="btn-secondary small" on:click={() => go('activity')}>Activity</button>
    </div>
    {#if recentRunsError}
      <p class="empty">{recentRunsError}</p>
    {:else if loading}
      <p class="empty">Loading…</p>
    {:else if !recentRuns.length}
      <p class="empty">No retained runs yet.</p>
    {:else}
      <div class="list run-list">
        {#each recentRuns as run}
          <button class="history-run {run.ok ? 'success' : 'failed'}" on:click={() => openRun(run)}>
            <span class="history-run-top">
              <strong>{run.agent_name || run.agent_id}</strong>
              <span>{runTime(run)}</span>
            </span>
            <small>{run.ok ? 'success' : 'failed'} · {runSummary(run)}</small>
          </button>
        {/each}
      </div>
    {/if}
  </section>

  <section>
    <h2>Today’s automations</h2>
    {#if loading}
      <p class="empty">Loading…</p>
    {:else if !schedule.length}
      <p class="empty">No active schedules.</p>
    {:else}
      <div class="list">
        {#each schedule.slice(0, 6) as item}
          <div class="schedule-row">
            <button class="schedule-main" type="button" on:click={() => openActivity(scheduleAgentId(item))}>
              <strong>{item.name || item.agent_id || item.id}</strong>
              <small>{item.next_run || item.next || item.status || 'scheduled'}</small>
            </button>
            <div class="schedule-actions">
              <button type="button" on:click={() => runScheduleNow(item)} disabled={scheduleBusy === `run:${scheduleAgentId(item)}`}>
                {scheduleBusy === `run:${scheduleAgentId(item)}` ? 'Starting…' : 'Run'}
              </button>
              <button type="button" on:click={() => testScheduleDelivery(item)} disabled={scheduleBusy === `test:${scheduleAgentId(item)}`}>
                {scheduleBusy === `test:${scheduleAgentId(item)}` ? 'Testing…' : 'Test output'}
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <section>
    <h2>Recent failures</h2>
    {#if refreshingError}
      <p class="empty">{refreshingError}</p>
    {:else if loading}
      <p class="empty">Loading…</p>
    {:else if !recentErrors.length}
      <p class="empty good">No recent agent errors found.</p>
    {:else}
      <div class="list">
        {#each recentErrors as ev}
          <button on:click={() => go('activity')}>
            <strong>{ev.agent_name || ev.agent_id}</strong>
            <small>{eventTime(ev)} · {eventText(ev)}</small>
          </button>
        {/each}
      </div>
    {/if}
  </section>

  <section>
    <h2>Needs attention</h2>
    {#if providerIssues.length === 0 && channelIssues.length === 0}
      <p class="empty good">No provider or channel blockers found.</p>
    {:else}
      <div class="list">
        {#each providerIssues as p}
          <button on:click={() => go('providers')}>
            <strong>{p.id}</strong>
            <small>{p.detail}</small>
          </button>
        {/each}
        {#each channelIssues as c}
          <button on:click={() => go('channels')}>
            <strong>{c.id}</strong>
            <small>{c.detail}</small>
          </button>
        {/each}
      </div>
    {/if}
  </section>
</div>

<style>
  .mobile-page { max-width: 760px; margin: 0 auto; display: flex; flex-direction: column; gap: 14px; }
  .companion-note { display: block; width: 100%; text-align: left; background: rgba(108,99,255,.12); border: 1px solid rgba(108,99,255,.35); color: #b9bcf0; border-radius: 9px; padding: .55rem .8rem; font-size: .82rem; cursor: pointer; }
  .companion-readiness { border: 1px solid #20243d; background: #10121f; border-radius: 10px; padding: 14px; display: flex; flex-direction: column; gap: .65rem; }
  .companion-readiness.ok { border-color: rgba(96,200,120,.38); }
  .companion-readiness.warn { border-color: rgba(240,176,112,.35); }
  .companion-readiness.fail { border-color: rgba(255,90,90,.4); }
  .companion-readiness-top { display: flex; align-items: flex-start; justify-content: space-between; gap: .75rem; }
  .companion-readiness-top h2 { margin: .15rem 0 0; font-size: 1rem; }
  .companion-pills { display: flex; flex-wrap: wrap; gap: .4rem; }
  .companion-pills span { border: 1px solid #252a46; background: #15182a; color: #b9bcf0; border-radius: 999px; padding: .2rem .55rem; font-size: .72rem; }
  .companion-next { border-left: 3px solid #8b85ff; background: rgba(108,99,255,.1); color: #c5c9e8; border-radius: 6px; padding: .45rem .6rem; font-size: .78rem; }
  .companion-checks { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .5rem; }
  .companion-check { text-align: left; border: 1px solid #252a46; background: #15182a; border-radius: 8px; padding: .55rem .65rem; color: inherit; cursor: pointer; }
  .companion-check.ok { border-color: rgba(96,200,120,.28); }
  .companion-check.warn { border-color: rgba(240,176,112,.3); }
  .companion-check.fail { border-color: rgba(255,90,90,.35); }
  .companion-check span, .companion-check small { display: block; }
  .companion-check span { color: #dfe2f5; font-size: .8rem; font-weight: 700; }
  .companion-check small { color: #8a91b8; margin-top: .2rem; font-size: .72rem; line-height: 1.35; }
  .m-card { background: #141626; border: 1px solid #1a1e36; border-radius: 12px; padding: .8rem .95rem; }
  .m-card.active { border-color: rgba(240,160,96,.4); }
  .m-card-hd { display: flex; align-items: center; gap: .5rem; font-size: .8rem; font-weight: 700; color: #c5c9e8; margin-bottom: .6rem; }
  .m-card-hd.split { justify-content: space-between; }
  .m-badge { font-size: .68rem; padding: .1rem .45rem; border-radius: 999px; background: #1c1f35; color: #8a91b8; }
  .m-badge.urgent { background: rgba(240,160,96,.18); color: #f0b070; }
  .m-empty { color: #6b7294; font-size: .82rem; margin: 0; }
  .m-banner { display: flex; align-items: center; justify-content: space-between; gap: .6rem; background: rgba(108,99,255,.1); border: 1px solid rgba(108,99,255,.3); color: #b9bcf0; border-radius: 9px; padding: .55rem .8rem; font-size: .82rem; }
  .m-banner.err { background: rgba(255,90,90,.12); border-color: rgba(255,90,90,.4); color: #ff9a9a; }
  .m-banner .tiny { padding: .28rem .7rem; font-size: .74rem; border-radius: 6px; white-space: nowrap; }
  .linkish { background: none; border: none; color: inherit; text-decoration: underline; cursor: pointer; font: inherit; padding: 0; }
  .run-row { width: 100%; display: flex; align-items: center; gap: .5rem; padding: .45rem 0; border: 0; border-top: 1px solid #1a1e36; background: transparent; color: inherit; font-size: .82rem; text-align: left; cursor: pointer; }
  .run-row:first-of-type { border-top: none; }
  .run-row strong { color: #dfe2f5; }
  .run-age { margin-left: auto; color: #8a91b8; font-size: .74rem; }
  .run-dot { width: 8px; height: 8px; border-radius: 50%; background: #60c878; box-shadow: 0 0 0 0 rgba(96,200,120,.5); animation: runpulse 1.6s infinite; }
  @keyframes runpulse { 0% { box-shadow: 0 0 0 0 rgba(96,200,120,.5); } 70% { box-shadow: 0 0 0 6px rgba(96,200,120,0); } 100% { box-shadow: 0 0 0 0 rgba(96,200,120,0); } }
  .approval { display: flex; align-items: center; justify-content: space-between; gap: .6rem; padding: .55rem 0; border-top: 1px solid #1a1e36; }
  .approval:first-of-type { border-top: 0; }
  .approval-tool { font-size: .86rem; font-weight: 650; color: #c5c9e8; }
  .approval-reason { font-size: .74rem; color: #8a91b8; margin-top: .15rem; }
  .approval-actions { display: flex; gap: .4rem; }
  .approval-actions button { border: 0; border-radius: 7px; padding: .38rem .7rem; font-size: .78rem; font-weight: 650; cursor: pointer; }
  .approval-actions .approve { background: #2e7d5b; color: #eafff4; }
  .approval-actions .deny { background: #3a2030; color: #f0a0a0; }
  .pocket-chat { display: flex; flex-direction: column; gap: .65rem; }
  .pocket-controls { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: .55rem; align-items: stretch; }
  .pocket-controls select,
  .pocket-controls textarea {
    min-width: 0;
    background: #0e1020;
    color: #d7dcf5;
    border: 1px solid #252a46;
    border-radius: 8px;
    padding: .55rem .65rem;
    font: inherit;
    font-size: .82rem;
  }
  .pocket-controls select { grid-column: 1 / -1; }
  .pocket-controls textarea { resize: vertical; line-height: 1.35; }
  .pocket-controls button { min-width: 82px; border-radius: 8px; }
  .pocket-result {
    display: block;
    width: 100%;
    text-align: left;
    background: #15182a;
    border: 1px solid #252a46;
    color: inherit;
    border-radius: 8px;
    padding: .65rem .75rem;
    cursor: pointer;
  }
  .pocket-result.err { border-color: rgba(255,90,90,.42); background: rgba(255,90,90,.1); }
  .pocket-result strong,
  .pocket-result small { display: block; }
  .pocket-result strong { color: #dfe2f5; font-size: .82rem; }
  .pocket-result small {
    color: #aeb4d2;
    margin-top: .25rem;
    font-size: .76rem;
    line-height: 1.35;
    overflow: hidden;
    display: -webkit-box;
    -webkit-line-clamp: 5;
    -webkit-box-orient: vertical;
    word-break: break-word;
  }
  .device-row { display: flex; align-items: center; justify-content: space-between; gap: .6rem; padding: .5rem 0; border-top: 1px solid #1a1e36; }
  .device-row:first-of-type { border-top: 0; }
  .device-label { font-size: .84rem; color: #c5c9e8; font-weight: 600; }
  .device-sub { font-size: .72rem; color: #6b7294; margin-top: .15rem; }
  .btn-primary.small, .btn-secondary.small { padding: .35rem .7rem; font-size: .78rem; border-radius: 7px; }
  .pair-qr { display: block; margin: .5rem auto .2rem; width: 180px; height: 180px; border-radius: 10px; background: #fff; padding: 8px; }
  .pair-code { font-family: ui-monospace, Menlo, monospace; font-size: 1.3rem; letter-spacing: .12em; color: #c5c9e8; text-align: center; padding: .5rem; background: #0e1020; border-radius: 8px; margin-top: .4rem; }
  .pair-url { font-size: .68rem; color: #6b7294; text-align: center; margin-top: .3rem; word-break: break-all; }
  .redeem-row { gap: .5rem; }
  .redeem-input { flex: 1; background: #0e1020; color: #d7dcf5; border: 1px solid #1a1e36; border-radius: 7px; padding: .4rem .55rem; font-size: .82rem; }
  .redeem-msg { font-size: .76rem; color: #8a91b8; margin-top: .35rem; }
  header { display: flex; align-items: center; justify-content: space-between; }
  header span { color: #8b85ff; text-transform: uppercase; letter-spacing: .08em; font-size: .68rem; }
  h1 { margin: .1rem 0 0; font-size: 1.35rem; }
  h2 { margin: .2rem 0 .5rem; font-size: .92rem; color: #c8cbe8; }
  .banner { border-radius: 8px; padding: .65rem .8rem; }
  .banner.err { color: #ff9a9a; background: rgba(255, 90, 90, .12); }
  .hero { border: 1px solid #252a46; border-radius: 8px; background: #111426; padding: 16px; display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .hero strong, .hero small { display: block; }
  .hero small { color: #8f96bb; margin-top: 2px; }
  .hero span { border-radius: 999px; padding: .2rem .55rem; background: rgba(240, 176, 112, .12); color: #f0b070; font-size: .75rem; }
  .hero span.ok { background: rgba(76, 175, 130, .12); color: #72d9aa; }
  .hero span.warn { background: rgba(240, 176, 112, .12); color: #f0b070; }
  .launch-card { display: flex; flex-direction: column; gap: .65rem; }
  .launch-top { display: flex; align-items: flex-start; justify-content: space-between; gap: .75rem; }
  .launch-top h2 { margin: .15rem 0 0; font-size: 1rem; }
  .kicker { color: #8b85ff; text-transform: uppercase; letter-spacing: .08em; font-size: .68rem; font-weight: 700; }
  .launch-stats { display: flex; flex-wrap: wrap; gap: .4rem; }
  .launch-stats span { border: 1px solid #252a46; background: #15182a; color: #b9bcf0; border-radius: 999px; padding: .2rem .55rem; font-size: .72rem; }
  .section-title-row { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .section-title-row h2 { margin-bottom: .2rem; }
  .quick-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; }
  .quick-grid button, .metrics button { background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a; border-radius: 8px; padding: 13px 8px; }
  section { border: 1px solid #20243d; background: #10121f; border-radius: 8px; padding: 14px; }
  .metrics { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
  .metrics button { text-align: left; }
  .metrics strong { display: block; font-size: 1.35rem; }
  .metrics small { color: #8f96bb; }
  .list { display: flex; flex-direction: column; gap: 8px; }
  .list.compact { gap: 6px; }
  .list button { text-align: left; background: #15182a; border: 1px solid #252a46; color: inherit; border-radius: 8px; padding: 11px; }
  .list strong, .list small { display: block; }
  .list small, .empty { color: #8f96bb; font-size: .8rem; line-height: 1.4; }
  .empty.good { color: #72d9aa; }
  .run-list { gap: 7px; }
  .history-run { border-left: 3px solid #5f6688 !important; }
  .history-run.success { border-left-color: #60c878 !important; }
  .history-run.failed { border-left-color: #ff7d7d !important; }
  .history-run-top { display: flex; align-items: center; justify-content: space-between; gap: .6rem; }
  .history-run-top span { color: #8f96bb; font-size: .72rem; white-space: nowrap; }
  .delivery-list { gap: 10px; }
  .delivery-card { background: #15182a; border: 1px solid #252a46; border-radius: 8px; padding: 10px; }
  .delivery-main { width: 100%; display: grid; grid-template-columns: auto 1fr; align-items: start; gap: .55rem; background: transparent; border: 0; color: inherit; text-align: left; padding: 0; cursor: pointer; }
  .delivery-main strong { color: #e8eaf6; }
  .status-dot { width: 9px; height: 9px; border-radius: 50%; margin-top: .25rem; background: #5f6688; }
  .status-dot.ok { background: #60c878; }
  .status-dot.warn { background: #f0c460; }
  .status-dot.fail { background: #ff7d7d; }
  .delivery-targets { display: flex; flex-direction: column; gap: 7px; margin-top: 9px; }
  .delivery-target { display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 8px; align-items: center; border-top: 1px solid #20243d; padding-top: 8px; }
  .target-main { min-width: 0; text-align: left; background: transparent; border: 0; color: inherit; padding: 0; cursor: pointer; }
  .target-main strong { font-size: .82rem; color: #dfe2f5; }
  .target-main small.good { color: #72d9aa; }
  .target-actions { display: flex; gap: 5px; }
  .target-actions button { white-space: nowrap; background: #1c1f35; color: #e8eaf6; border: 1px solid #2a2f4a; border-radius: 7px; padding: .35rem .55rem; font-size: .72rem; }
  .delivery-fix { grid-column: 1 / -1; margin: -2px 0 0; color: #f0c460; font-size: .75rem; line-height: 1.35; }
  .schedule-row { display: grid; grid-template-columns: 1fr auto; gap: 8px; align-items: stretch; }
  .schedule-main { min-width: 0; }
  .schedule-actions { display: flex; gap: 6px; }
  .schedule-actions button { white-space: nowrap; font-size: .74rem; }
  @media (max-width: 540px) {
    .quick-grid { grid-template-columns: repeat(2, 1fr); }
    .companion-checks { grid-template-columns: 1fr; }
    .metrics { grid-template-columns: 1fr; }
    .schedule-row { grid-template-columns: 1fr; }
    .schedule-actions { display: grid; grid-template-columns: 1fr 1fr; }
    .delivery-target { grid-template-columns: 1fr; }
    .target-actions { display: grid; grid-template-columns: 1fr 1fr; }
    .pocket-controls { grid-template-columns: 1fr; }
  }
</style>
