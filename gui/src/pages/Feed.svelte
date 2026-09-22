<script>
  // Feed — the home that scrolls. Every card is something an agent produced:
  // a finished run with output, a delivery to the phone, or an approval that
  // needs the person. Newest first; live inserts arrive as a pill, never by
  // shifting what is being read. (soulacy-personal #191)
  import { onMount, onDestroy } from 'svelte'
  import { api, createEventSocket } from '../lib/api.js'
  import { parseMarkdown, richRenderer } from '../lib/markdown.js'
  import { activityAgent } from '../lib/stores.js'
  import TourButton from '../lib/TourButton.svelte'
  import StoryViewer from '../lib/StoryViewer.svelte'

  let agents = []
  let cards = []
  let running = new Set()
  let loading = true
  let error = ''
  let pending = 0          // results that arrived while reading
  let saved = new Set()
  let socket = null
  let refreshTimer = null

  const SAVED_KEY = 'soulacy.feed.saved'
  try { saved = new Set(JSON.parse(localStorage.getItem(SAVED_KEY) || '[]')) } catch { saved = new Set() }

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

  function buildCards({ runs, deliveries, approvals }) {
    const out = []
    for (const a of approvals) out.push({
      kind: 'approval', id: `approval:${a.call_id}`, agent: a.agent_id, at: a.created_at,
      tool: a.tool, args: a.args || {}, reason: a.reason || '', callId: a.call_id, session: a.session_id,
    })
    for (const d of deliveries) out.push({
      kind: 'delivery', id: `delivery:${d.id}`, agent: d.agent_id, at: d.created_at,
      title: d.title || '', body: d.body || '', session: d.session_id, unread: !d.read_at,
    })
    for (const r of runs) {
      if (!r.output || !r.output.trim()) continue
      out.push({
        kind: 'run', id: `run:${r.id}`, agent: r.agentId, at: r.updatedAt || r.startedAt,
        body: r.output, ok: r.ok !== false && r.status !== 'failed', status: r.status,
        session: r.sessionId, trigger: r.trigger || '', steps: r.steps || 0, ms: r.durationMs || 0,
      })
    }
    // Approvals first (they expire), then newest first.
    const rank = (c) => (c.kind === 'approval' ? 0 : 1)
    return out.sort((x, y) => rank(x) - rank(y) || new Date(y.at) - new Date(x.at))
  }

  async function load({ quiet = false } = {}) {
    if (!quiet) { loading = true; error = '' }
    try {
      const [ag, ledger, del, app, run] = await Promise.all([
        api.agents.list().catch(() => ({ agents: [] })),
        api.runs.ledger({ limit: 60 }).catch(() => ({ runs: [] })),
        api.mobile.deliveries(40).catch(() => ({ deliveries: [] })),
        api.approvals.list().catch(() => ({ approvals: [] })),
        api.activity.running().catch(() => ({ sessions: [] })),
      ])
      agents = ag.agents || []
      running = new Set((run.sessions || []).map(s => s.agent_id))
      const deliveries = del.deliveries || []
      cards = buildCards({ runs: ledger.runs || [], deliveries, approvals: app.approvals || [] })
      pending = 0
    } catch (e) {
      error = e.message || String(e)
    } finally {
      loading = false
    }
  }

  function toggleSave(card) {
    if (saved.has(card.id)) saved.delete(card.id); else saved.add(card.id)
    saved = new Set(saved)
    try { localStorage.setItem(SAVED_KEY, JSON.stringify([...saved])) } catch {}
  }
  let lastTap = new Map()
  function tap(card) {
    // Double-tap (or double-click) saves — the one gesture everyone knows.
    const now = Date.now(), prev = lastTap.get(card.id) || 0
    lastTap.set(card.id, now)
    if (now - prev < 350) { toggleSave(card); burst(card.id) }
  }
  let bursting = ''
  // Long outputs are clamped like a caption; 'more' opens the whole thing.
  let expanded = new Set()
  function isLong(card) { return (card.body || '').length > 900 || ((card.body || '').match(/\n/g) || []).length > 14 }
  function toggleMore(card) { if (expanded.has(card.id)) expanded.delete(card.id); else expanded.add(card.id); expanded = new Set(expanded) }
  function burst(id) { bursting = id; setTimeout(() => { if (bursting === id) bursting = '' }, 700) }

  function reply(card) {
    activityAgent.set(card.agent || '')
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
        if (/run\.started|session\.start/i.test(t) && ev?.agent_id) { running.add(ev.agent_id); running = new Set(running) }
        if (/run\.(finished|completed|failed)/i.test(t) && ev?.agent_id) { running.delete(ev.agent_id); running = new Set(running) }
      }
      socket.onclose = () => { socket = null }
    } catch { socket = null }
  }

  onMount(() => { load(); connect(); refreshTimer = setInterval(() => load({ quiet: true }), 60000) })
  onDestroy(() => { if (socket) socket.close(); if (refreshTimer) clearInterval(refreshTimer) })

  // Stories: Today = the newest results across agents; each agent = its own
  // newest results. Opened from the rail.
  let storyOpen = null
  function slidesFor(agentId) {
    const pick = cards.filter(c => c.kind !== 'approval' && (agentId === 'today' || c.agent === agentId)).slice(0, 6)
    return pick.map(c => ({ id: c.id, at: c.at, title: c.title || '', body: c.body }))
  }
  $: storyList = stories.map(s => ({ id: s.id, label: s.label, glyph: s.glyph, hue: s.id === 'today' ? 28 : hue(s.id), slides: slidesFor(s.id) }))
  function openStory(id) { storyOpen = Math.max(0, storyList.findIndex(s => s.id === id)) }

  $: stories = [
    { id: 'today', label: 'Today', glyph: '☀︎', live: false, seen: false },
    ...agents.filter(a => a.enabled !== false).map(a => ({ id: a.id, label: a.name || a.id, glyph: initials(a.id), live: running.has(a.id), seen: !cards.some(c => c.agent === a.id) })),
  ]
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

  <div class="stories" role="list" aria-label="Stories">
    {#each stories as s (s.id)}
      <button class="story" role="listitem" class:live={s.live} class:seen={s.seen} title={s.live ? `${s.label} is running now` : s.label}
        on:click={() => openStory(s.id)}>
        <span class="ring"><span class="av" style="--h:{s.id === 'today' ? 28 : hue(s.id)}">{s.glyph}</span></span>
        <span class="label">{s.label}</span>
      </button>
    {/each}
  </div>

  {#if pending > 0}
    <button class="pill" on:click={() => load()}>{pending} new result{pending === 1 ? '' : 's'} ↑</button>
  {/if}

  {#if loading && cards.length === 0}
    <div class="empty">Loading your feed…</div>
  {:else if error}
    <div class="empty err">{error}</div>
  {:else if cards.length === 0}
    <div class="empty">Nothing yet. When an agent finishes work or sends something to your phone, it shows up here.</div>
  {/if}

  <div class="cards">
    {#each cards as card (card.id)}
      <article class="card" class:needs={card.kind === 'approval'} on:click={() => tap(card)} on:keydown={(e) => e.key === 'Enter' && toggleSave(card)} tabindex="0" aria-label="{agentName(card.agent)} · {card.kind}">
        <div class="head">
          <span class="av" style="--h:{hue(card.agent)}">{initials(card.agent)}</span>
          <div class="who">
            <b>{agentName(card.agent)}</b>
            <span class="meta">
              {#if card.kind === 'approval'}needs you{:else if card.kind === 'delivery'}sent to your phone{:else}{card.trigger || 'finished'}{#if card.steps} · {card.steps} steps{/if}{/if}
              {#if card.at} · {relative(card.at)}{/if}
            </span>
          </div>
          {#if card.kind === 'run' && !card.ok}<span class="chip bad">failed</span>{/if}
          {#if card.kind === 'delivery' && card.unread}<span class="chip new">new</span>{/if}
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
          {#if card.title}<div class="title">{card.title}</div>{/if}
          <div class="body markdown-body" class:clamped={isLong(card) && !expanded.has(card.id)} use:richRenderer={card.body}>{@html parseMarkdown(card.body)}</div>
          {#if isLong(card)}
            <button class="more" on:click|stopPropagation={() => toggleMore(card)}>{expanded.has(card.id) ? 'less' : 'more'}</button>
          {/if}
        {/if}

        <div class="actions">
          <button class="act heart" class:on={saved.has(card.id)} class:burst={bursting === card.id} title="Save" aria-label="Save" on:click|stopPropagation={() => { toggleSave(card); burst(card.id) }}>♥</button>
          <button class="act" title="Reply in Chat" aria-label="Reply" on:click|stopPropagation={() => reply(card)}>◎</button>
          <button class="act" title="Copy" aria-label="Copy" on:click|stopPropagation={() => share(card)}>↗</button>
          <span class="spacer"></span>
          {#if card.ms}<span class="dur">{(card.ms / 1000).toFixed(card.ms < 10000 ? 1 : 0)}s</span>{/if}
        </div>
      </article>
    {/each}
  </div>

  {#if toast}<div class="toast">{toast}</div>{/if}
  {#if storyOpen !== null}
    <StoryViewer stories={storyList} index={storyOpen} on:close={() => storyOpen = null}
      on:reply={(e) => { storyOpen = null; const a = e.detail.story?.id; activityAgent.set(a && a !== 'today' ? a : ''); location.hash = '#chat' }} />
  {/if}
</div>

<style>
  /* Tropical tokens, scoped to the feed until the shell adopts them (#191 phase 1). */
  .feed {
    /* Aliases onto the shell's tokens (App.svelte): the feed follows day/night
       with everything else. */
    --f-bg: var(--sl-bg); --f-bg-2: var(--sl-surface); --f-ink: var(--sl-text); --f-ink-2: var(--sl-text-dim); --f-ink-3: var(--sl-text-faint);
    --f-line: var(--sl-line); --f-accent: var(--sl-accent); --f-accent-ink: var(--sl-accent-ink); --f-coral: var(--sl-coral); --f-mango: var(--sl-mango); --f-leaf: var(--sl-leaf);
    --f-ring: var(--story-ring);
    background: var(--f-bg); color: var(--f-ink); margin: -1rem; padding: 0 0 4rem;
    /* The shell's content area is a column flexbox: grow with the cards, never
       cap at the viewport, or the white surface stops and text runs onto the
       dark shell. */
    flex: 1 0 auto; min-height: calc(100% + 2rem);
    font-family: -apple-system, "SF Pro Text", "Helvetica Neue", "Segoe UI", Arial, sans-serif;
  }
  .top { display: flex; align-items: center; justify-content: space-between; padding: 14px 18px 8px; position: sticky; top: 0; background: var(--f-bg); z-index: 2; border-bottom: 1px solid var(--f-line); }
  .wordmark { font-weight: 800; font-size: 22px; letter-spacing: -.03em; line-height: 1; color: var(--f-ink); }
  .top-actions { display: flex; gap: 14px; }
  .icon { background: none; border: 0; color: var(--f-ink); font-size: 20px; cursor: pointer; text-decoration: none; padding: 2px 4px; }
  .stories { display: flex; gap: 14px; padding: 12px 16px; overflow-x: auto; border-bottom: 1px solid var(--f-line); scrollbar-width: none; }
  .story { background: none; border: 0; padding: 0; display: grid; justify-items: center; gap: 5px; width: 68px; flex: none; color: var(--f-ink); font: inherit; font-size: 11px; cursor: pointer; }
  .story .ring { width: 62px; height: 62px; border-radius: 50%; padding: 2.5px; background: var(--f-ring); display: block; }
  .story.seen .ring { background: var(--f-line); }
  .story.live .ring { animation: pulse 1.6s ease-in-out infinite; }
  .story .label { max-width: 68px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .av { width: 100%; height: 100%; border-radius: 50%; border: 2.5px solid var(--f-bg); display: grid; place-items: center; font-weight: 700; color: #fff; background: linear-gradient(135deg, hsl(var(--h) 70% 45%), hsl(calc(var(--h) + 30) 80% 62%)); }
  .story .av { font-size: 17px; }
  @keyframes pulse { 0%,100% { filter: saturate(1); } 50% { filter: saturate(1.6) brightness(1.08); } }
  .pill { position: sticky; top: 58px; z-index: 2; margin: 10px auto 0; display: block; background: var(--f-accent); color: #fff; border: 0; border-radius: 999px; padding: 6px 14px; font-weight: 600; cursor: pointer; box-shadow: 0 6px 18px var(--sl-accent-soft-strong); }
  .cards { display: grid; justify-items: center; }
  .card { width: 100%; max-width: 560px; border-bottom: 1px solid var(--f-line); padding: 6px 0 8px; outline: none; }
  .card.needs { border: 1px solid var(--f-coral); border-radius: 12px; margin: 12px 16px 6px; width: calc(100% - 32px); }
  .head { display: flex; align-items: center; gap: 10px; padding: 8px 16px; }
  .head .av { width: 32px; height: 32px; border: 0; font-size: 12px; flex: none; }
  .who { display: grid; line-height: 1.25; min-width: 0; }
  .who b { font-weight: 700; }
  .meta { color: var(--f-ink-3); font-size: 12px; }
  .chip { margin-left: auto; font-size: 11px; font-weight: 700; padding: 2px 8px; border-radius: 999px; }
  .chip.bad { background: color-mix(in srgb, var(--f-coral) 12%, transparent); color: var(--f-coral); }
  .chip.new { background: var(--sl-accent-soft); color: var(--f-accent-ink); }
  .title { padding: 0 16px 6px; font-weight: 700; font-size: 15px; }
  .body { padding: 4px 16px 6px; font-size: 14px; line-height: 1.5; color: var(--f-ink); overflow-wrap: anywhere; }
  .body.clamped { max-height: 340px; overflow: hidden; -webkit-mask-image: linear-gradient(#000 78%, transparent); mask-image: linear-gradient(#000 78%, transparent); }
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
  @keyframes burst { 0% { transform: scale(1); } 35% { transform: scale(1.5); } 100% { transform: scale(1); } }
  .spacer { flex: 1; }
  .dur { color: var(--f-ink-3); font-size: 12px; font-variant-numeric: tabular-nums; padding-right: 8px; }
  .empty { padding: 40px 20px; text-align: center; color: var(--f-ink-2); }
  .empty.err { color: var(--f-coral); }
  .toast { position: fixed; bottom: 24px; left: 50%; transform: translateX(-50%); background: var(--f-ink); color: var(--f-bg); padding: 8px 14px; border-radius: 999px; font-size: 13px; }
  @media (prefers-reduced-motion: reduce) { .story.live .ring, .act.heart.burst { animation: none; } }
</style>
