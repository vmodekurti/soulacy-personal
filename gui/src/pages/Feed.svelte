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
</script>

<div class="feed">
  <header class="top">
    <div class="wordmark">soulacy</div>
    <div class="top-actions">
      <TourButton page="feed" />
      <button class="icon" title="Refresh" on:click={() => load()} aria-label="Refresh">↻</button>
      <a class="icon" href="#chat" title="Chat" aria-label="Chat">◎</a>
    </div>
  </header>

  <nav class="scopes" aria-label="Feed views">
    <button class:active={scope === 'home'} on:click={() => { scope = 'home'; agentFilter = '' }}>Home{#if unreadCount}<span>{unreadCount}</span>{/if}</button>
    <button class:active={scope === 'results' || scope === 'unread'} on:click={() => scope = 'results'}>Results{#if resultCount}<span>{resultCount}</span>{/if}</button>
    <button class:active={scope === 'saved'} on:click={() => scope = 'saved'}>Saved{#if savedCount}<span>{savedCount}</span>{/if}</button>
    <button class:active={scope === 'archived'} on:click={() => scope = 'archived'}>Archive{#if archivedCount}<span>{archivedCount}</span>{/if}</button>
  </nav>

  {#if scope === 'home'}
    <section class="results-summary">
      <button on:click={() => scope = 'results'}>
        <span class="summary-icon">▤</span>
        <span><b>Agent results</b><small>{resultCount} results from {resultGroups.length} agents</small></span>
        {#if unreadCount}<strong>{unreadCount} new</strong>{/if}
        <i>›</i>
      </button>
    </section>
    <div class="section-heading"><b>New for you</b><span>{unreadCount || 'Caught up'}</span></div>
  {:else if scope === 'results' || scope === 'unread'}
    <section class="results-head">
      <div><h2>{scope === 'unread' ? 'Unread results' : 'All results'}</h2><p>Scheduled work, organized by agent</p></div>
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
    <button class="pill" on:click={() => load()}>{pending} new result{pending === 1 ? '' : 's'} ↑</button>
  {/if}

  {#if loading && cards.length === 0}
    <div class="empty">Loading your feed…</div>
  {:else if error}
    <div class="empty err">{error}</div>
  {:else if visibleResults.length === 0}
    <div class="empty">{scope === 'archived' ? 'No archived results.' : scope === 'unread' ? 'You are all caught up.' : scope === 'saved' ? 'Save a result to keep it here.' : scope === 'home' ? 'You are caught up. New results will appear here.' : 'No results yet.'}</div>
  {/if}

  <div class="cards">
    {#each visibleResults as card (card.id)}
      <!-- The card keeps the familiar double-click save gesture and exposes
           the same action to keyboard users through Enter. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex a11y_no_noninteractive_element_interactions -->
      <article class="card" class:needs={card.kind === 'approval'} class:unread={card.kind !== 'approval' && !read.has(card.id)} class:result-row={card.kind !== 'approval'}
        on:click={() => tap(card)} on:keydown={(event) => event.key === 'Enter' && toggleSave(card)} tabindex="0"
        aria-label="{agentName(card.agent)} · {card.kind}">
        <div class="head">
          <span class="av" style="--h:{hue(card.agent)}">{initials(card.agent)}</span>
          <div class="who">
            <b>{agentName(card.agent)}</b>
            <span class="meta">
              {#if card.kind === 'approval'}needs you{:else}{card.trigger === 'cron' ? 'scheduled' : (card.trigger || 'finished')}{#if card.steps} · {card.steps} steps{/if}{/if}
              {#if card.at} · {relative(card.at)}{/if}
            </span>
          </div>
          {#if card.kind === 'run' && !card.ok}<span class="chip bad">failed</span>{/if}
          {#if card.kind !== 'approval' && !read.has(card.id)}<span class="unread-dot" aria-label="Unread"></span>{/if}
        </div>

        {#if card.kind === 'approval'}
          <div class="body approval">
            <div class="tag">Needs you</div>
            <p>Wants to run <code>{card.tool}</code>{#if card.reason} — {card.reason}{/if}</p>
            {#if Object.keys(card.args).length}<pre class="args">{JSON.stringify(card.args, null, 2)}</pre>{/if}
            <div class="btns">
              <button class="btn" on:click|stopPropagation={() => decide(card, false)}>Deny</button>
              <button class="btn primary" on:click|stopPropagation={() => decide(card, true)}>Approve</button>
            </div>
          </div>
        {:else}
          {#if titleFor(card)}<div class="title">{titleFor(card)}</div>{/if}
          {#if expanded.has(card.id) && blocksFor(card)}
            {#if card.presentation.summary}<div class="summary" class:clamped={!expanded.has(card.id)}>{card.presentation.summary}</div>{/if}
            <div class="blocks"><Presentation blocks={blocksFor(card)} compact={!expanded.has(card.id)} /></div>
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

  {#if toast}<div class="toast">{toast}</div>{/if}
</div>

<style>
  /* Tropical tokens, scoped to the feed until the shell adopts them (#191 phase 1). */
  .feed {
    /* Aliases onto the shell's tokens (App.svelte): the feed follows day/night
       with everything else. */
    --f-bg: var(--sl-bg); --f-bg-2: var(--sl-surface); --f-ink: var(--sl-text); --f-ink-2: var(--sl-text-dim); --f-ink-3: var(--sl-text-faint);
    --f-line: var(--sl-line); --f-accent: var(--sl-accent); --f-accent-ink: var(--sl-accent-ink); --f-coral: var(--sl-coral); --f-mango: var(--sl-mango); --f-leaf: var(--sl-leaf);
    --f-ring: var(--story-ring);
    background: var(--f-bg); color: var(--f-ink); margin: 0; padding: 0 0 4rem;
    /* The shell's content area is a column flexbox: grow with the cards, never
       cap at the viewport, or the white surface stops and text runs onto the
       dark shell. */
    flex: 1 0 auto; min-height: 100%;
    font-family: -apple-system, "SF Pro Text", "Helvetica Neue", "Segoe UI", Arial, sans-serif;
  }
  .top { display: flex; align-items: center; justify-content: space-between; padding: 14px 18px 8px; position: sticky; top: 0; background: var(--f-bg); z-index: 2; border-bottom: 1px solid var(--f-line); }
  /* The logotype: Grand Hotel, the script from the brand — nowhere else. */
  .wordmark { font-family: 'Grand Hotel', 'Snell Roundhand', cursive; font-weight: 400; font-size: 30px; letter-spacing: 0; line-height: 1; color: var(--f-ink); }
  .top-actions { display: flex; gap: 14px; }
  .icon { background: none; border: 0; color: var(--f-ink); font-size: 20px; cursor: pointer; text-decoration: none; padding: 2px 4px; }
  .scopes { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 4px; width: min(560px, calc(100% - 32px)); padding: 10px 0; margin: 0 auto; }
  .scopes button { min-width: 0; display: flex; align-items: center; justify-content: center; gap: 5px; border: 1px solid var(--f-line); background: transparent; color: var(--f-ink-2); border-radius: 999px; padding: 7px 10px; font: inherit; font-size: 12px; font-weight: 650; cursor: pointer; white-space: nowrap; }
  .scopes button.active { background: var(--f-ink); border-color: var(--f-ink); color: var(--f-bg); }
  .scopes span { display: inline-grid; place-items: center; min-width: 17px; height: 17px; padding: 0 3px; border-radius: 999px; background: var(--sl-accent-soft); color: var(--f-accent-ink); font-size: 10px; }
  .av { width: 100%; height: 100%; border-radius: 50%; border: 2.5px solid var(--f-bg); display: grid; place-items: center; font-weight: 700; color: #fff; background: linear-gradient(135deg, hsl(var(--h) 70% 45%), hsl(calc(var(--h) + 30) 80% 62%)); }
  .results-summary, .results-head, .agent-filters, .section-heading { width: min(560px, calc(100% - 32px)); margin-inline: auto; }
  .results-summary button { width: 100%; display: flex; align-items: center; gap: 12px; padding: 14px; border: 1px solid var(--f-line); border-radius: 16px; background: var(--f-bg-2); color: var(--f-ink); text-align: left; cursor: pointer; }
  .results-summary button > span:nth-child(2) { display: grid; gap: 2px; flex: 1; min-width: 0; }
  .results-summary small { color: var(--f-ink-2); font-size: 12px; }
  .results-summary strong { flex: none; color: var(--f-accent-ink); font-size: 12px; }
  .results-summary i { color: var(--f-ink-3); font-size: 24px; font-style: normal; }
  .summary-icon { width: 38px; height: 38px; display: grid; place-items: center; border-radius: 50%; background: var(--sl-accent-soft); color: var(--f-accent-ink); font-size: 20px; }
  .section-heading { display: flex; justify-content: space-between; padding: 18px 2px 8px; }
  .section-heading span { color: var(--f-ink-3); font-size: 12px; }
  .results-head { display: flex; align-items: center; justify-content: space-between; padding: 10px 0 8px; }
  .results-head h2 { margin: 0; font-size: 18px; }
  .results-head p { margin: 3px 0 0; color: var(--f-ink-2); font-size: 12px; }
  .filter-toggle { border: 1px solid var(--f-line); border-radius: 999px; background: transparent; color: var(--f-ink); padding: 7px 11px; cursor: pointer; }
  .agent-filters { display: flex; flex-wrap: wrap; gap: 7px; padding-bottom: 10px; }
  .agent-filters button { min-width: 0; max-width: 100%; display: flex; align-items: center; gap: 6px; border: 1px solid var(--f-line); border-radius: 999px; background: transparent; color: var(--f-ink-2); padding: 5px 10px; cursor: pointer; overflow-wrap: anywhere; text-align: left; }
  .agent-filters button.active { border-color: var(--f-accent); color: var(--f-ink); background: var(--sl-accent-soft); }
  .mini-av { width: 22px; height: 22px; display: grid; place-items: center; border-radius: 50%; color: #fff; font-size: 9px; font-weight: 700; background: linear-gradient(135deg, hsl(var(--h) 70% 45%), hsl(calc(var(--h) + 30) 80% 62%)); }
  .agent-filters em { min-width: 16px; height: 16px; display: grid; place-items: center; border-radius: 999px; background: var(--f-accent); color: #fff; font-size: 9px; font-style: normal; }
  .pill { position: sticky; top: 58px; z-index: 2; margin: 10px auto 0; display: block; background: var(--f-accent); color: #fff; border: 0; border-radius: 999px; padding: 6px 14px; font-weight: 600; cursor: pointer; box-shadow: 0 6px 18px var(--sl-accent-soft-strong); }
  .cards { display: grid; justify-items: center; }
  .card { width: 100%; max-width: 560px; border-bottom: 1px solid var(--f-line); padding: 6px 0 8px; outline: none; }
  .card.result-row { margin: 5px 16px; width: calc(100% - 32px); border: 1px solid var(--f-line); border-radius: 14px; background: var(--f-bg-2); }
  .card.unread { background: linear-gradient(90deg, var(--sl-accent-soft), transparent 36%); }
  .card.needs { border: 1px solid var(--f-coral); border-radius: 12px; margin: 12px 16px 6px; width: calc(100% - 32px); }
  .head { display: flex; align-items: center; gap: 10px; padding: 8px 16px; }
  .head .av { width: 32px; height: 32px; border: 0; font-size: 12px; flex: none; }
  .who { display: grid; line-height: 1.25; min-width: 0; }
  .who b { font-weight: 700; }
  .meta { color: var(--f-ink-3); font-size: 12px; }
  .chip { margin-left: auto; font-size: 11px; font-weight: 700; padding: 2px 8px; border-radius: 999px; }
  .chip.bad { background: color-mix(in srgb, var(--f-coral) 12%, transparent); color: var(--f-coral); }
  .unread-dot { margin-left: auto; width: 8px; height: 8px; border-radius: 50%; background: var(--f-accent); box-shadow: 0 0 0 3px var(--sl-accent-soft); }
  .title { padding: 0 16px 4px; font-weight: 600; font-size: 15px; line-height: 1.3; }
  .summary { padding: 0 16px 6px; font-size: 13.5px; color: var(--f-ink-2); line-height: 1.45; }
  .summary.clamped { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .preview { display: -webkit-box; padding: 0 16px 4px; color: var(--f-ink-2); font-size: 13px; line-height: 1.4; white-space: normal; overflow: hidden; overflow-wrap: anywhere; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
  .blocks { padding: 2px 16px 6px; }
  .body { padding: 2px 16px 6px; font-size: 13.5px; line-height: 1.45; color: var(--f-ink-2); overflow-wrap: anywhere; }
  .more { background: none; border: 0; color: var(--f-ink-3); font: inherit; font-size: 13px; padding: 0 16px 6px; cursor: pointer; }
  .more:hover { color: var(--f-accent-ink); }
  .body :global(p) { margin: 0 0 .6em; }
  .body :global(pre) { overflow-x: auto; }
  .body :global(img), .body :global(video) { max-width: calc(100% + 32px); margin: 6px -16px; display: block; }
  .body :global(a) { color: var(--f-accent-ink); }
  .body :global(table) { font-size: 13px; }
  .approval .tag { color: var(--f-coral); font-weight: 700; font-size: 11px; letter-spacing: .08em; text-transform: uppercase; margin-bottom: 4px; }
  .approval code { background: var(--f-bg-2); padding: 1px 5px; border-radius: 4px; }
  .args { background: var(--f-bg-2); border: 1px solid var(--f-line); border-radius: 8px; padding: 8px 10px; font-size: 12px; max-height: 160px; overflow: auto; }
  .btns { display: flex; gap: 8px; margin-top: 8px; }
  .btn { flex: 1; padding: 8px 0; border-radius: 8px; font-weight: 600; font-size: 13px; border: 1px solid var(--f-line); background: var(--f-bg); color: var(--f-ink); cursor: pointer; }
  .btn.primary { background: var(--f-accent); border-color: var(--f-accent); color: #fff; }
  .actions { display: flex; align-items: center; gap: 4px; padding: 2px 10px 0; }
  .act { background: none; border: 0; font-size: 20px; color: var(--f-ink); cursor: pointer; padding: 4px 6px; line-height: 1; }
  .act.heart.on { color: var(--f-coral); }
  .act.heart.burst { animation: burst .6s ease-out; }
  .act.state { font-size: 18px; color: var(--f-ink-3); }
  .continue { border: 0; background: transparent; color: var(--f-accent-ink); padding: 5px 8px; font: inherit; font-size: 12px; font-weight: 700; cursor: pointer; }
  @keyframes burst { 0% { transform: scale(1); } 35% { transform: scale(1.5); } 100% { transform: scale(1); } }
  .spacer { flex: 1; }
  .dur { color: var(--f-ink-3); font-size: 12px; font-variant-numeric: tabular-nums; padding-right: 8px; }
  .empty { padding: 40px 20px; text-align: center; color: var(--f-ink-2); }
  .empty.err { color: var(--f-coral); }
  .toast { position: fixed; bottom: 24px; left: 50%; transform: translateX(-50%); background: var(--f-ink); color: var(--f-bg); padding: 8px 14px; border-radius: 999px; font-size: 13px; }
  @media (prefers-reduced-motion: reduce) { .act.heart.burst { animation: none; } }
  @media (max-width: 520px) {
    .scopes { grid-template-columns: repeat(2, 1fr); }
    .results-head { align-items: flex-start; }
    .card.result-row { margin-inline: 12px; width: calc(100% - 24px); }
  }
</style>
