<script>
  // Feed — the home that scrolls. Every card is something an agent produced:
  // a finished run with output, a delivery to the phone, or an approval that
  // needs the person. Newest first; live inserts arrive as a pill, never by
  // shifting what is being read. (soulacy-personal #191)
  import { onMount, onDestroy } from 'svelte'
  import { api, createEventSocket } from '../lib/api.js'
  import { parseMarkdown, richRenderer } from '../lib/markdown.js'
  import { genieAsk } from '../lib/stores.js'
  import { applyRemoteFeedState, buildFeedCards, filterFeedCards, groupResults, initialReadState } from '../lib/feedstate.js'
  import TourButton from '../lib/TourButton.svelte'
  import Presentation from '../lib/Presentation.svelte'

  let agents = []
  let cards = []
  let loading = true
  let error = ''
  let pending = 0          // results that arrived while reading
  let saved = new Set()
  let read = new Set()
  let archived = new Set()
  let scope = 'home'
  let agentFilter = ''
  let homePrompt = ''
  let socket = null
  let refreshTimer = null

  const SAVED_KEY = 'soulacy.feed.saved'
  const READ_KEY = 'soulacy.feed.read'
  const ARCHIVED_KEY = 'soulacy.feed.archived'
  const READ_INITIALIZED_KEY = 'soulacy.feed.read.initialized'
  try { saved = new Set(JSON.parse(localStorage.getItem(SAVED_KEY) || '[]')) } catch { saved = new Set() }
  try { read = new Set(JSON.parse(localStorage.getItem(READ_KEY) || '[]')) } catch { read = new Set() }
  try { archived = new Set(JSON.parse(localStorage.getItem(ARCHIVED_KEY) || '[]')) } catch { archived = new Set() }

  function agentName(id) { return agents.find(a => a.id === id)?.name || id || 'Soulacy' }
  function initials(id) { const n = agentName(id); return n.split(/[\s-]+/).map(w => w[0]).join('').slice(0, 2).toUpperCase() }
  // Deterministic tropical hue per agent so an avatar looks the same everywhere.
  function hue(id) { let h = 0; for (const ch of String(id)) h = (h * 31 + ch.charCodeAt(0)) % 360; return h }

  function relative(iso) {
    const t = new Date(iso).getTime(); if (!t) return ''
    const s = Math.max(0, (Date.now() - t) / 1000)
    if (s < 60) return 'now'
    if (s < 3600) return `${Math.floor(s / 60)}m`
    if (s < 86400) return `${Math.floor(s / 3600)}h`
    return `${Math.floor(s / 86400)}d`
  }

  async function load({ quiet = false } = {}) {
    if (!quiet) { loading = true; error = '' }
    try {
      const [ag, ledger, del, app, remoteState] = await Promise.all([
        api.agents.list().catch(() => ({ agents: [] })),
        api.runs.ledger({ limit: 60 }).catch(() => ({ runs: [] })),
        api.mobile.deliveries(40).catch(() => ({ deliveries: [] })),
        api.approvals.list().catch(() => ({ approvals: [] })),
        api.mobile.feedState().catch(() => null),
      ])
      agents = ag.agents || []
      const deliveries = del.deliveries || []
      cards = buildFeedCards({ runs: ledger.runs || [], deliveries, approvals: app.approvals || [] })
      try {
        const rows = remoteState?.cards || []
        const hasLocalState = !!localStorage.getItem(READ_INITIALIZED_KEY)
        if (rows.length > 0) {
          const state = applyRemoteFeedState(cards, rows)
          read = state.read
          saved = state.saved
          archived = state.archived
          saveLocalState()
        } else if (!hasLocalState) {
          read = initialReadState(cards)
          saveLocalState()
        } else if (remoteState) {
          // First load after upgrading: preserve meaningful browser state and
          // copy it to the account so the next client sees the same Feed.
          const defaults = initialReadState(cards)
          for (const card of cards) {
            if (card.kind === 'approval') continue
            if (saved.has(card.id) || archived.has(card.id) || read.has(card.id) !== defaults.has(card.id)) {
              persistCardState(card)
            }
          }
        }
      } catch {}
      pending = 0
    } catch (e) {
      error = e.message || String(e)
    } finally {
      loading = false
    }
  }

  function saveLocalState() {
    try {
      localStorage.setItem(SAVED_KEY, JSON.stringify([...saved]))
      localStorage.setItem(READ_KEY, JSON.stringify([...read]))
      localStorage.setItem(ARCHIVED_KEY, JSON.stringify([...archived]))
      localStorage.setItem(READ_INITIALIZED_KEY, '1')
    } catch {}
  }

  function persistCardState(card) {
    if (!card || card.kind === 'approval') return
    api.mobile.saveFeedState({
      card_id: card.id,
      read: read.has(card.id),
      saved: saved.has(card.id),
      archived: archived.has(card.id),
    }).catch(() => {})
  }

  function toggleSave(card) {
    if (saved.has(card.id)) saved.delete(card.id); else saved.add(card.id)
    saved = new Set(saved)
    saveLocalState()
    persistCardState(card)
  }
  function setRead(card, value, persist = true) {
    if (card.kind === 'approval') return
    if (value) read.add(card.id); else read.delete(card.id)
    read = new Set(read)
    saveLocalState()
    if (persist) persistCardState(card)
  }
  function toggleRead(card) {
    const next = !read.has(card.id)
    setRead(card, next)
    toast = next ? 'Marked read' : 'Marked unread'
    setTimeout(() => (toast = ''), 1500)
  }
  function archive(card) {
    if (card.kind === 'approval') return
    archived.add(card.id)
    setRead(card, true, false)
    archived = new Set(archived)
    saveLocalState()
    persistCardState(card)
    toast = 'Archived'
    setTimeout(() => (toast = ''), 1500)
  }
  function restore(card) {
    archived.delete(card.id)
    archived = new Set(archived)
    saveLocalState()
    persistCardState(card)
    toast = 'Restored'
    setTimeout(() => (toast = ''), 1500)
  }
  let lastTap = new Map()
  function tap(card) {
    const now = Date.now(), previous = lastTap.get(card.id) || 0
    lastTap.set(card.id, now)
    if (now - previous < 350) { toggleSave(card); burst(card.id) }
  }
  let bursting = ''
  // A post, not a transcript: a headline (first heading or first sentence) and
  // a short caption. Redaction markers are hidden; a table whose separator row
  // the gateway redacted (#197) is repaired so it renders as a table.
  function headlineOf(body) {
    for (const raw of String(body || '').split('\n')) {
      const line = raw.trim()
      if (!line || line.startsWith('|') || line.startsWith('```') || line.startsWith('[REDACTED')) continue
      if (line.startsWith('#')) return line.replace(/^#+\s*/, '').slice(0, 110)
      const plain = line.replace(/\*\*|__|`/g, '')
      const m = plain.match(/^(.{25,}?[.!?])(\s|$)/)
      return (m ? m[1] : plain).slice(0, 110)
    }
    return ''
  }
  function captionOf(body, headline) {
    let lines = String(body || '').split('\n')
    if (headline) {
      const i = lines.findIndex(l => l.trim().replace(/^#+\s*/, '').replace(/\*\*|__|`/g, '').startsWith(headline.slice(0, 20)))
      if (i >= 0) {
        const t = lines[i].trim()
        const plain = t.replace(/^#+\s*/, '').replace(/\*\*|__|`/g, '')
        if (t.startsWith('#') || plain.length <= headline.length + 2) lines.splice(i, 1)
        else lines[i] = plain.slice(headline.length).trim()
      }
    }
    const out = []
    for (let i = 0; i < lines.length; i++) {
      const t = lines[i].trim()
      if (t.startsWith('|') && i + 1 < lines.length && lines[i + 1].trim().startsWith('[REDACTED')) {
        const cols = Math.max(1, t.split('|').length - 2)
        out.push(lines[i], '|---'.repeat(cols) + '|'); i += 1; continue
      }
      out.push(lines[i])
    }
    return out.join('\n').replace(/\s*\[REDACTED:[0-9a-f]+\]/g, '').trim()
  }
  // Long outputs are clamped like a caption; 'more' opens the whole thing.
  let expanded = new Set()
  function captionFor(card) { return captionOf(card.body, card.title ? '' : headlineOf(card.body)) }
  // The gateway's presentation, when it sent one, decides the visual (#199);
  // the local headline/caption split is the fallback for older gateways.
  function titleFor(card) { return card.title || (card.presentation && card.presentation.headline) || headlineOf(card.body) }
  function blocksFor(card) { return card.presentation && Array.isArray(card.presentation.blocks) ? card.presentation.blocks : null }
  function isLong(card) { if (blocksFor(card)) { const bs = blocksFor(card); return bs.length > 1 || (card.presentation.summary || '').length > 160 || bs.some(b => b.kind === 'markdown' && (b.text || '').length > 280) } const c = captionFor(card); return c.length > 320 || (c.match(/\n/g) || []).length > 5 }
  function toggleMore(card) { if (expanded.has(card.id)) expanded.delete(card.id); else expanded.add(card.id); expanded = new Set(expanded) }
  function burst(id) { bursting = id; setTimeout(() => { if (bursting === id) bursting = '' }, 700) }

  function reply(card) {
    setRead(card, true)
    genieAsk.set({
      type: 'result',
      text: '',
      context: {
        resultId: card.id,
        sourceAgent: agentName(card.agent),
        title: titleFor(card),
        body: card.body || '',
        session: card.session || '',
      },
      at: Date.now(),
    })
    location.hash = '#chat'
  }

  function startWithGenie() {
    const text = homePrompt.trim()
    if (!text) return
    genieAsk.set({ type: 'request', text, context: {}, at: Date.now() })
    location.hash = '#chat'
  }

  function focusAttention() {
    scope = 'home'
    setTimeout(() => document.querySelector('.card.needs')?.scrollIntoView({ behavior: 'smooth', block: 'center' }))
  }
  async function share(card) {
    const text = card.body || card.title || ''
    try { await navigator.clipboard.writeText(text); toast = 'Copied' } catch { toast = 'Could not copy' }
    setTimeout(() => (toast = ''), 1500)
  }
  let toast = ''
  async function decide(card, ok) {
    try {
      await (ok ? api.approvals.approve(card.callId) : api.approvals.deny(card.callId))
      cards = cards.filter(c => c.id !== card.id)
    } catch (e) { error = e.message }
  }

  function connect() {
    try {
      socket = createEventSocket()
      socket.onmessage = (m) => {
        let ev = null
        try { ev = JSON.parse(m.data) } catch { return }
        const t = String(ev?.type || ev?.event || '')
        if (/run\.(finished|completed|failed)|delivery|approval/i.test(t)) pending += 1
      }
      socket.onclose = () => { socket = null }
    } catch { socket = null }
  }

  onMount(() => { load(); connect(); refreshTimer = setInterval(() => load({ quiet: true }), 60000) })
  onDestroy(() => { if (socket) socket.close(); if (refreshTimer) clearInterval(refreshTimer) })

  $: displayedCards = filterFeedCards(cards, scope, { read, saved, archived })
  $: resultGroups = groupResults(filterFeedCards(cards, scope === 'home' ? 'results' : scope, { read, saved, archived }), agentName, read)
  $: visibleResults = agentFilter ? displayedCards.filter(card => card.agent === agentFilter || card.kind === 'approval') : displayedCards
  $: unreadCount = filterFeedCards(cards, 'unread', { read, saved, archived }).length
  $: resultCount = filterFeedCards(cards, 'results', { read, saved, archived }).length
  $: archivedCount = filterFeedCards(cards, 'archived', { read, saved, archived }).length
  $: savedCount = filterFeedCards(cards, 'saved', { read, saved, archived }).length
  $: attentionCount = cards.filter(card => card.kind === 'approval').length
  $: activeAgents = agents.filter(agent => agent.enabled).length
  $: recentResults = cards.filter(card => card.kind !== 'approval').slice(0, 4)
  const greeting = new Date().getHours() < 12 ? 'Good morning' : new Date().getHours() < 18 ? 'Good afternoon' : 'Good evening'
</script>

<div class="feed">
  <header class="workspace-header">
    <div>
      <span class="eyebrow">Personal workspace</span>
      <h1>{greeting}</h1>
      <p>Your agents, results, and next actions in one calm place.</p>
    </div>
    <div class="header-actions">
      <TourButton page="feed" />
      <button class="icon-button" title="Refresh" on:click={() => load()} aria-label="Refresh workspace">↻</button>
    </div>
  </header>

  {#if scope === 'home'}
    <section class="genie-launcher" aria-labelledby="genie-launcher-title">
      <div class="genie-mark" aria-hidden="true">✦</div>
      <div class="launcher-copy">
        <span>Genie</span>
        <h2 id="genie-launcher-title">What would you like to get done?</h2>
        <p>Ask a question, start a task, or hand off something that needs several steps.</p>
      </div>
      <form class="launcher-composer" on:submit|preventDefault={startWithGenie}>
        <input bind:value={homePrompt} aria-label="Ask Genie" placeholder="Tell Genie what you want to accomplish" />
        <button type="submit" disabled={!homePrompt.trim()}>Ask Genie <span>→</span></button>
      </form>
      <div class="launcher-suggestions" aria-label="Suggested requests">
        <button type="button" on:click={() => homePrompt = 'Help me plan my day'}>Plan my day</button>
        <button type="button" on:click={() => homePrompt = 'Summarize what my agents completed recently'}>Catch me up</button>
        <button type="button" on:click={() => homePrompt = 'Create a new automation'}>Create an automation</button>
      </div>
    </section>

    <section class="overview" aria-label="Workspace overview">
      <a href="#agents" class="metric">
        <span class="metric-icon violet">◎</span>
        <span><small>Active agents</small><strong>{activeAgents}</strong></span>
        <em>Manage</em>
      </a>
      <button class="metric" on:click={() => scope = 'results'}>
        <span class="metric-icon blue">▤</span>
        <span><small>Agent results</small><strong>{resultCount}</strong></span>
        <em>{unreadCount} new</em>
      </button>
      <button class="metric" on:click={focusAttention}>
        <span class="metric-icon amber">✓</span>
        <span><small>Needs attention</small><strong>{attentionCount}</strong></span>
        <em>{attentionCount ? 'Review' : 'Clear'}</em>
      </button>
      <button class="metric" on:click={() => scope = 'saved'}>
        <span class="metric-icon green">♡</span>
        <span><small>Saved for later</small><strong>{savedCount}</strong></span>
        <em>Open</em>
      </button>
    </section>
  {/if}

  <nav class="view-tabs" aria-label="Feed views">
    <button class:active={scope === 'home'} on:click={() => { scope = 'home'; agentFilter = '' }}>For you</button>
    <button class:active={scope === 'results' || scope === 'unread'} on:click={() => scope = 'results'}>Results <span>{resultCount}</span></button>
    <button class:active={scope === 'saved'} on:click={() => scope = 'saved'}>Saved <span>{savedCount}</span></button>
    <button class:active={scope === 'archived'} on:click={() => scope = 'archived'}>Archive <span>{archivedCount}</span></button>
  </nav>

  {#if scope === 'results' || scope === 'unread'}
    <section class="library-header">
      <div><h2>{scope === 'unread' ? 'Unread results' : 'All agent results'}</h2><p>Finished work from scheduled and manual runs.</p></div>
      <button class="filter-toggle" on:click={() => scope = scope === 'unread' ? 'results' : 'unread'}>{scope === 'unread' ? 'Show all' : `Unread ${unreadCount || ''}`}</button>
    </section>
    <div class="agent-filters" aria-label="Filter results by agent">
      <button class:active={!agentFilter} on:click={() => agentFilter = ''}>All agents</button>
      {#each resultGroups as group (group.id)}
        <button class:active={agentFilter === group.id} on:click={() => agentFilter = group.id}>
          <span class="mini-av" style="--h:{hue(group.id)}">{initials(group.id)}</span>
          {group.name}{#if group.unread}<em>{group.unread}</em>{/if}
        </button>
      {/each}
    </div>
  {/if}

  {#if pending > 0}
    <button class="new-results" on:click={() => load()}>{pending} new result{pending === 1 ? '' : 's'} ready</button>
  {/if}

  <div class:home-grid={scope === 'home'} class:library-grid={scope !== 'home'}>
    <main class="work-stream">
      {#if scope === 'home'}
        <header class="section-title">
          <div><span>For you</span><h2>Latest from your agents</h2></div>
          <button on:click={() => scope = 'results'}>View all <span>→</span></button>
        </header>
      {/if}

      {#if loading && cards.length === 0}
        <div class="empty">Loading your workspace...</div>
      {:else if error}
        <div class="empty err">{error}</div>
      {:else if visibleResults.length === 0}
        <div class="empty-state">
          <span>✓</span>
          <h3>{scope === 'home' || scope === 'unread' ? 'You are all caught up' : scope === 'archived' ? 'No archived results' : scope === 'saved' ? 'Nothing saved yet' : 'No results yet'}</h3>
          <p>{scope === 'saved' ? 'Save useful results and they will stay easy to find here.' : 'New agent work will appear here when it is ready.'}</p>
        </div>
      {/if}

      <div class="cards" class:library={scope !== 'home'}>
        {#each visibleResults as card (card.id)}
          <!-- svelte-ignore a11y_no_noninteractive_tabindex a11y_no_noninteractive_element_interactions -->
          <article class="card" class:needs={card.kind === 'approval'} class:unread={card.kind !== 'approval' && !read.has(card.id)}
            on:click={() => tap(card)} on:keydown={(event) => event.key === 'Enter' && toggleSave(card)} tabindex="0"
            aria-label="{agentName(card.agent)} · {card.kind}">
            <div class="head">
              <span class="av" style="--h:{hue(card.agent)}">{initials(card.agent)}</span>
              <div class="who">
                <b>{agentName(card.agent)}</b>
                <span class="meta">
                  {#if card.kind === 'approval'}needs you{:else}{card.trigger === 'cron' ? 'Scheduled run' : (card.trigger || 'Finished')}{#if card.steps} · {card.steps} steps{/if}{/if}
                  {#if card.at} · {relative(card.at)}{/if}
                </span>
              </div>
              {#if card.kind === 'run' && !card.ok}<span class="chip bad">Failed</span>{/if}
              {#if card.kind !== 'approval' && !read.has(card.id)}<span class="unread-dot" aria-label="Unread"></span>{/if}
            </div>

            {#if card.kind === 'approval'}
              <div class="body approval">
                <div class="tag">Needs your approval</div>
                <p>Wants to run <code>{card.tool}</code>{#if card.reason}: {card.reason}{/if}</p>
                {#if Object.keys(card.args).length}<pre class="args">{JSON.stringify(card.args, null, 2)}</pre>{/if}
                <div class="btns">
                  <button class="btn" on:click|stopPropagation={() => decide(card, false)}>Deny</button>
                  <button class="btn primary" on:click|stopPropagation={() => decide(card, true)}>Approve</button>
                </div>
              </div>
            {:else}
              {#if titleFor(card)}<div class="title">{titleFor(card)}</div>{/if}
              {#if expanded.has(card.id) && blocksFor(card)}
                {#if card.presentation.summary}<div class="summary">{card.presentation.summary}</div>{/if}
                <div class="blocks"><Presentation blocks={blocksFor(card)} compact={false} /></div>
              {:else if expanded.has(card.id)}
                <div class="body markdown-body" use:richRenderer={captionFor(card)}>{@html parseMarkdown(captionFor(card))}</div>
              {:else}
                <div class="preview">{captionFor(card).replace(/[#*_`>|\n]+/g, ' ').trim()}</div>
              {/if}
              <button class="more" on:click|stopPropagation={() => toggleMore(card)}>{expanded.has(card.id) ? 'Close result' : 'Open result'}</button>
            {/if}

            <div class="actions">
              <button class="act heart" class:on={saved.has(card.id)} class:burst={bursting === card.id} title="Save" aria-label="Save" on:click|stopPropagation={() => { toggleSave(card); burst(card.id) }}>♥</button>
              <button class="continue" title="Continue in Genie" aria-label="Continue in Genie" on:click|stopPropagation={() => reply(card)}>Continue in Genie</button>
              <button class="act" title="Copy" aria-label="Copy" on:click|stopPropagation={() => share(card)}>↗</button>
              {#if card.kind !== 'approval'}
                <button class="act state" title={read.has(card.id) ? 'Mark unread' : 'Mark read'} aria-label={read.has(card.id) ? 'Mark unread' : 'Mark read'} on:click|stopPropagation={() => toggleRead(card)}>{read.has(card.id) ? '◉' : '○'}</button>
                <button class="act state" title={scope === 'archived' ? 'Restore' : 'Archive'} aria-label={scope === 'archived' ? 'Restore' : 'Archive'} on:click|stopPropagation={() => scope === 'archived' ? restore(card) : archive(card)}>{scope === 'archived' ? '↩' : '⌑'}</button>
              {/if}
              <span class="spacer"></span>
              {#if card.ms}<span class="dur">{(card.ms / 1000).toFixed(card.ms < 10000 ? 1 : 0)}s</span>{/if}
            </div>
          </article>
        {/each}
      </div>
    </main>

    {#if scope === 'home'}
      <aside class="context-rail" aria-label="Workspace context">
        <section class="side-panel agents-panel">
          <header><div><span>Agents</span><h2>Your team</h2></div><a href="#agents">Manage</a></header>
          <div class="agent-list">
            {#each agents.filter(agent => agent.enabled).slice(0, 6) as agent (agent.id)}
              <a href="#agents" class="agent-row">
                <span class="mini-av large" style="--h:{hue(agent.id)}">{initials(agent.id)}</span>
                <span><b>{agent.name || agent.id}</b><small>{agent.trigger === 'cron' ? 'Scheduled' : 'Ready'}</small></span>
                <i class:scheduled={agent.trigger === 'cron'}></i>
              </a>
            {/each}
            {#if activeAgents === 0}<p class="side-empty">No active agents yet.</p>{/if}
          </div>
        </section>

        <section class="side-panel activity-panel">
          <header><div><span>Recent activity</span><h2>What just happened</h2></div><button on:click={() => scope = 'results'}>View all</button></header>
          <div class="activity-list">
            {#each recentResults as item (item.id)}
              <button on:click={() => { scope = 'results'; agentFilter = item.agent }}>
                <span class="activity-icon">{item.ok === false ? '!' : '✓'}</span>
                <span><b>{titleFor(item) || `Result from ${agentName(item.agent)}`}</b><small>{agentName(item.agent)} · {relative(item.at)}</small></span>
              </button>
            {/each}
            {#if recentResults.length === 0}<p class="side-empty">No recent activity.</p>{/if}
          </div>
        </section>

        <section class="quick-links" aria-label="Quick links">
          <a href="#schedule"><span>⌁</span>Automations</a>
          <a href="#websites"><span>◎</span>Website access</a>
          <a href="#agents"><span>✦</span>Create an agent</a>
        </section>
      </aside>
    {/if}
  </div>

  {#if toast}<div class="toast">{toast}</div>{/if}
</div>

<style>
  .feed {
    --f-bg: var(--sl-bg); --f-surface: var(--sl-surface); --f-raised: var(--sl-surface-raised);
    --f-ink: var(--sl-text); --f-dim: var(--sl-text-dim); --f-faint: var(--sl-text-faint);
    --f-line: var(--sl-line); --f-accent: var(--sl-accent); --f-accent-ink: var(--sl-accent-ink);
    min-height: 100%; flex: 1 0 auto; padding: 0 0 5rem; color: var(--f-ink); background: var(--f-bg);
    font-family: -apple-system, "SF Pro Text", "Helvetica Neue", "Segoe UI", Arial, sans-serif;
  }
  .workspace-header, .genie-launcher, .overview, .view-tabs, .library-header, .agent-filters, .home-grid, .library-grid {
    width: min(1660px, calc(100% - 64px)); margin-inline: auto;
  }
  .workspace-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 2rem; padding: 38px 0 24px; }
  .eyebrow, .launcher-copy > span, .section-title > div > span, .side-panel header span { color: var(--f-accent-ink); font-size: .7rem; font-weight: 800; letter-spacing: .09em; text-transform: uppercase; }
  .workspace-header h1 { margin: .2rem 0 .25rem; font-size: clamp(2rem, 2.7vw, 3rem); line-height: 1.05; letter-spacing: -.045em; }
  .workspace-header p { margin: 0; color: var(--f-dim); font-size: 1rem; }
  .header-actions { display: flex; align-items: center; gap: .6rem; }
  .icon-button { width: 42px; height: 42px; border: 1px solid var(--f-line); border-radius: 13px; color: var(--f-dim); background: var(--f-surface); box-shadow: var(--sl-shadow-sm); font-size: 1.2rem; cursor: pointer; }

  .genie-launcher { display: grid; grid-template-columns: auto minmax(220px, .7fr) minmax(420px, 1.3fr); align-items: center; gap: 20px; padding: 26px 28px; overflow: hidden; border: 1px solid color-mix(in srgb, var(--f-accent) 22%, var(--f-line)); border-radius: 24px; background: radial-gradient(circle at 90% -20%, var(--sl-accent-soft-strong), transparent 42%), var(--f-surface); box-shadow: var(--sl-shadow-lg); }
  .genie-mark { width: 58px; height: 58px; display: grid; place-items: center; border-radius: 18px; color: var(--sl-on-accent); background: linear-gradient(145deg, var(--sl-accent-fill), var(--sl-accent-fill-hover)); box-shadow: 0 12px 28px var(--sl-accent-soft-strong); font-size: 1.7rem; }
  .launcher-copy h2 { margin: .25rem 0 .3rem; font-size: clamp(1.35rem, 1.65vw, 1.8rem); letter-spacing: -.025em; }
  .launcher-copy p { margin: 0; color: var(--f-dim); font-size: .9rem; line-height: 1.45; }
  .launcher-composer { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: .65rem; padding: .55rem; border: 1px solid var(--f-line); border-radius: 17px; background: var(--f-raised); }
  .launcher-composer input { min-width: 0; padding: .65rem .75rem; border: 0; outline: 0; color: var(--f-ink); background: transparent; font: inherit; font-size: .96rem; }
  .launcher-composer button { display: flex; align-items: center; gap: .5rem; padding: .72rem 1rem; border: 0; border-radius: 12px; color: var(--sl-on-accent); background: var(--sl-accent-fill); font-weight: 750; cursor: pointer; }
  .launcher-composer button:disabled { opacity: .48; cursor: default; }
  .launcher-suggestions { grid-column: 3; display: flex; flex-wrap: wrap; gap: .45rem; }
  .launcher-suggestions button { padding: .38rem .7rem; border: 1px solid var(--f-line); border-radius: 999px; color: var(--f-dim); background: var(--f-surface); font-size: .75rem; cursor: pointer; }
  .launcher-suggestions button:hover { color: var(--f-accent-ink); border-color: color-mix(in srgb, var(--f-accent) 42%, var(--f-line)); }

  .overview { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; padding: 18px 0 6px; }
  .metric { min-width: 0; display: grid; grid-template-columns: 44px minmax(0, 1fr) auto; align-items: center; gap: .75rem; padding: 14px 15px; border: 1px solid var(--f-line); border-radius: 17px; color: var(--f-ink); background: var(--f-surface); box-shadow: var(--sl-shadow-sm); text-align: left; text-decoration: none; cursor: pointer; }
  .metric-icon { width: 42px; height: 42px; display: grid; place-items: center; border-radius: 13px; font-size: 1.1rem; font-style: normal; }
  .metric-icon.violet { color: var(--f-accent-ink); background: var(--sl-accent-soft); }
  .metric-icon.blue { color: #1769aa; background: color-mix(in srgb, #45a9f5 15%, transparent); }
  .metric-icon.amber { color: #9a5b00; background: color-mix(in srgb, #f7b84b 18%, transparent); }
  .metric-icon.green { color: #087b52; background: color-mix(in srgb, #26b77a 15%, transparent); }
  :global(:root[data-theme="dark"]) .metric-icon.blue { color: #83c8ff; }
  :global(:root[data-theme="dark"]) .metric-icon.amber { color: #ffd083; }
  :global(:root[data-theme="dark"]) .metric-icon.green { color: #7ee2b8; }
  .metric > span:nth-child(2) { min-width: 0; display: grid; }
  .metric small { color: var(--f-faint); font-size: .72rem; font-weight: 650; }
  .metric strong { margin-top: .15rem; font-size: 1.25rem; }
  .metric em { color: var(--f-accent-ink); font-size: .72rem; font-style: normal; font-weight: 750; }

  .view-tabs { display: flex; align-items: center; gap: .3rem; margin-top: 24px; padding: 0 0 9px; border-bottom: 1px solid var(--f-line); }
  .view-tabs button { display: flex; align-items: center; gap: .4rem; padding: .55rem .8rem; border: 0; border-radius: 10px; color: var(--f-faint); background: transparent; font: inherit; font-size: .82rem; font-weight: 700; cursor: pointer; }
  .view-tabs button.active { color: var(--f-accent-ink); background: var(--sl-accent-soft); }
  .view-tabs span { min-width: 19px; padding: .12rem .3rem; border-radius: 999px; color: inherit; background: color-mix(in srgb, currentColor 10%, transparent); font-size: .65rem; text-align: center; }

  .home-grid { display: grid; grid-template-columns: minmax(0, 1fr) 330px; align-items: start; gap: 22px; padding-top: 20px; }
  .library-grid { padding-top: 20px; }
  .work-stream { min-width: 0; }
  .section-title { display: flex; align-items: end; justify-content: space-between; gap: 1rem; margin-bottom: 12px; }
  .section-title h2, .side-panel h2, .library-header h2 { margin: .18rem 0 0; font-size: 1.15rem; letter-spacing: -.02em; }
  .section-title > button, .side-panel header > a, .side-panel header > button { display: flex; align-items: center; gap: .4rem; padding: .35rem; border: 0; color: var(--f-accent-ink); background: transparent; font: inherit; font-size: .75rem; font-weight: 750; text-decoration: none; cursor: pointer; }
  .cards { display: grid; grid-template-columns: 1fr; gap: 12px; }
  .cards.library { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }

  .card { min-width: 0; overflow: hidden; border: 1px solid var(--f-line); border-radius: 18px; outline: none; background: var(--f-surface); box-shadow: var(--sl-shadow-sm); }
  .card:hover { border-color: color-mix(in srgb, var(--f-accent) 26%, var(--f-line)); box-shadow: 0 10px 26px color-mix(in srgb, var(--f-ink) 7%, transparent); }
  .card.unread { box-shadow: inset 3px 0 var(--f-accent), var(--sl-shadow-sm); }
  .card.needs { border-color: color-mix(in srgb, var(--sl-coral) 65%, var(--f-line)); }
  .head { display: flex; align-items: center; gap: 10px; padding: 15px 16px 10px; }
  .av, .mini-av { display: grid; place-items: center; flex: none; border-radius: 50%; color: #fff; font-weight: 800; background: linear-gradient(135deg, hsl(var(--h) 70% 43%), hsl(calc(var(--h) + 30) 76% 60%)); }
  .head .av { width: 36px; height: 36px; font-size: .72rem; }
  .mini-av { width: 23px; height: 23px; font-size: .58rem; }
  .mini-av.large { width: 34px; height: 34px; font-size: .66rem; }
  .who { min-width: 0; display: grid; gap: .05rem; line-height: 1.25; }
  .who b { overflow: hidden; font-size: .86rem; text-overflow: ellipsis; white-space: nowrap; }
  .meta { color: var(--f-faint); font-size: .7rem; }
  .chip { margin-left: auto; padding: .2rem .5rem; border-radius: 999px; font-size: .64rem; font-weight: 800; }
  .chip.bad { color: var(--sl-coral); background: color-mix(in srgb, var(--sl-coral) 12%, transparent); }
  .unread-dot { width: 8px; height: 8px; margin-left: auto; border-radius: 50%; background: var(--f-accent); box-shadow: 0 0 0 4px var(--sl-accent-soft); }
  .title { padding: 0 16px 5px; font-size: 1rem; font-weight: 750; line-height: 1.35; }
  .preview { display: -webkit-box; padding: 0 16px 8px; overflow: hidden; color: var(--f-dim); font-size: .84rem; line-height: 1.5; overflow-wrap: anywhere; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
  .summary, .body, .blocks { padding: 0 16px 8px; color: var(--f-dim); font-size: .86rem; line-height: 1.55; overflow-wrap: anywhere; }
  .body :global(p) { margin: 0 0 .65em; }
  .body :global(pre) { overflow-x: auto; }
  .body :global(a) { color: var(--f-accent-ink); }
  .more { padding: 0 16px 10px; border: 0; color: var(--f-accent-ink); background: transparent; font: inherit; font-size: .73rem; font-weight: 700; cursor: pointer; }
  .approval .tag { margin-bottom: .3rem; color: var(--sl-coral); font-size: .66rem; font-weight: 800; letter-spacing: .08em; text-transform: uppercase; }
  .approval code { padding: .1rem .35rem; border-radius: 5px; background: var(--f-raised); }
  .args { max-height: 160px; overflow: auto; padding: .6rem; border: 1px solid var(--f-line); border-radius: 9px; background: var(--f-raised); font-size: .72rem; }
  .btns { display: flex; gap: .5rem; margin-top: .7rem; }
  .btn { flex: 1; padding: .55rem; border: 1px solid var(--f-line); border-radius: 9px; color: var(--f-ink); background: var(--f-raised); font-weight: 700; cursor: pointer; }
  .btn.primary { color: var(--sl-on-accent); border-color: var(--sl-accent-fill); background: var(--sl-accent-fill); }
  .actions { display: flex; align-items: center; gap: .2rem; padding: 4px 10px 10px; border-top: 1px solid color-mix(in srgb, var(--f-line) 70%, transparent); }
  .act, .continue { border: 0; background: transparent; cursor: pointer; }
  .act { padding: .4rem; color: var(--f-faint); font-size: 1rem; }
  .act.heart.on { color: var(--sl-coral); }
  .act.heart.burst { animation: burst .6s ease-out; }
  .continue { padding: .4rem .5rem; color: var(--f-accent-ink); font-size: .72rem; font-weight: 750; }
  .spacer { flex: 1; }
  .dur { padding-right: .35rem; color: var(--f-faint); font-size: .68rem; font-variant-numeric: tabular-nums; }
  @keyframes burst { 0% { transform: scale(1); } 35% { transform: scale(1.5); } 100% { transform: scale(1); } }

  .context-rail { display: grid; gap: 14px; position: sticky; top: 18px; }
  .side-panel, .quick-links { overflow: hidden; border: 1px solid var(--f-line); border-radius: 18px; background: var(--f-surface); box-shadow: var(--sl-shadow-sm); }
  .side-panel > header { display: flex; align-items: end; justify-content: space-between; gap: .5rem; padding: 15px 15px 10px; }
  .agent-list, .activity-list { display: grid; padding: 0 8px 8px; }
  .agent-row, .activity-list button { min-width: 0; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: .65rem; padding: .62rem .55rem; border: 0; border-radius: 11px; color: var(--f-ink); background: transparent; text-align: left; text-decoration: none; cursor: pointer; }
  .agent-row:hover, .activity-list button:hover { background: var(--f-raised); }
  .agent-row > span:nth-child(2), .activity-list button > span:nth-child(2) { min-width: 0; display: grid; gap: .12rem; }
  .agent-row b, .activity-list b { overflow: hidden; font-size: .77rem; text-overflow: ellipsis; white-space: nowrap; }
  .agent-row small, .activity-list small { color: var(--f-faint); font-size: .65rem; }
  .agent-row i { width: 8px; height: 8px; border-radius: 50%; background: var(--sl-leaf); }
  .agent-row i.scheduled { background: var(--f-accent); }
  .activity-icon { width: 29px; height: 29px; display: grid; place-items: center; border-radius: 9px; color: var(--f-accent-ink); background: var(--sl-accent-soft); font-size: .72rem; font-weight: 800; }
  .quick-links { display: grid; grid-template-columns: repeat(3, 1fr); padding: 8px; }
  .quick-links a { display: grid; place-items: center; gap: .3rem; min-width: 0; padding: .65rem .25rem; border-radius: 10px; color: var(--f-dim); font-size: .65rem; font-weight: 650; text-align: center; text-decoration: none; }
  .quick-links a:hover { color: var(--f-accent-ink); background: var(--sl-accent-soft); }
  .quick-links span { color: var(--f-accent-ink); font-size: 1rem; }
  .side-empty { margin: .6rem; color: var(--f-faint); font-size: .75rem; }

  .library-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 24px 0 10px; }
  .library-header p { margin: .25rem 0 0; color: var(--f-dim); font-size: .8rem; }
  .filter-toggle { padding: .5rem .75rem; border: 1px solid var(--f-line); border-radius: 999px; color: var(--f-ink); background: var(--f-surface); cursor: pointer; }
  .agent-filters { display: flex; flex-wrap: wrap; gap: .45rem; padding-bottom: 2px; }
  .agent-filters button { display: flex; align-items: center; gap: .4rem; padding: .35rem .65rem; border: 1px solid var(--f-line); border-radius: 999px; color: var(--f-dim); background: var(--f-surface); cursor: pointer; }
  .agent-filters button.active { color: var(--f-accent-ink); border-color: color-mix(in srgb, var(--f-accent) 40%, var(--f-line)); background: var(--sl-accent-soft); }
  .agent-filters em { min-width: 17px; height: 17px; display: grid; place-items: center; border-radius: 999px; color: var(--sl-on-accent); background: var(--sl-accent-fill); font-size: .57rem; font-style: normal; }
  .new-results { position: sticky; top: 16px; z-index: 2; display: block; margin: 12px auto 0; padding: .45rem .8rem; border: 0; border-radius: 999px; color: var(--sl-on-accent); background: var(--sl-accent-fill); box-shadow: var(--sl-shadow-lg); font-weight: 750; cursor: pointer; }
  .empty, .empty-state { padding: 60px 20px; color: var(--f-dim); text-align: center; }
  .empty.err { color: var(--sl-coral); }
  .empty-state { border: 1px dashed var(--f-line); border-radius: 18px; background: var(--f-surface); }
  .empty-state > span { width: 40px; height: 40px; display: grid; place-items: center; margin: 0 auto .7rem; border-radius: 50%; color: #087b52; background: color-mix(in srgb, #26b77a 15%, transparent); }
  .empty-state h3 { margin: 0; font-size: 1rem; }
  .empty-state p { margin: .35rem 0 0; color: var(--f-faint); font-size: .8rem; }
  .toast { position: fixed; z-index: 20; bottom: 24px; left: 50%; transform: translateX(-50%); padding: .55rem .9rem; border-radius: 999px; color: var(--f-bg); background: var(--f-ink); font-size: .78rem; }

  @media (prefers-reduced-motion: reduce) { .act.heart.burst { animation: none; } }
  @media (max-width: 1220px) {
    .genie-launcher { grid-template-columns: auto 1fr; }
    .launcher-composer, .launcher-suggestions { grid-column: 1 / -1; }
    .overview { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .home-grid { grid-template-columns: minmax(0, 1fr) 300px; }
  }
  @media (max-width: 920px) {
    .workspace-header, .genie-launcher, .overview, .view-tabs, .library-header, .agent-filters, .home-grid, .library-grid { width: min(100% - 32px, 760px); }
    .home-grid { grid-template-columns: 1fr; }
    .context-rail { position: static; grid-template-columns: 1fr 1fr; }
    .quick-links { grid-column: 1 / -1; }
    .cards.library { grid-template-columns: 1fr; }
  }
  @media (max-width: 620px) {
    .workspace-header { padding-top: 24px; }
    .workspace-header h1 { font-size: 1.75rem; }
    .workspace-header p { display: none; }
    .genie-launcher { grid-template-columns: auto minmax(0, 1fr); padding: 20px; border-radius: 20px; }
    .genie-mark { width: 48px; height: 48px; border-radius: 15px; }
    .launcher-copy p { display: none; }
    .launcher-composer { grid-template-columns: 1fr; }
    .launcher-composer button { justify-content: center; }
    .launcher-suggestions { flex-wrap: nowrap; overflow-x: auto; }
    .overview { grid-template-columns: 1fr 1fr; gap: 8px; }
    .metric { grid-template-columns: 36px minmax(0, 1fr); padding: 10px; }
    .metric-icon { width: 36px; height: 36px; }
    .metric em { display: none; }
    .view-tabs { overflow-x: auto; }
    .context-rail { grid-template-columns: 1fr; }
    .quick-links { grid-column: auto; }
    .actions .act.state { display: none; }
  }
</style>
