<script>
  // Adaptive memory: the facts and relationships Soulacy has distilled about
  // the signed-in user for one agent (or every agent when agentID is empty).
  // Users can inspect, search, edit, add, delete, expire, export, and purge,
  // and open each fact's change history. Superseded and retracted facts stay
  // visible under their own filters as an audit trail.
  import { onMount } from 'svelte'
  import { api, apiFetch } from './api.js'

  export let agentID = ''

  let facts = [], relations = [], status = 'active', query = '', busy = false, error = '', notice = ''
  let info = { enabled: false, provider: '', active: 0, superseded: 0, retracted: 0, relations: 0, categories: [], graph_enabled: true }
  let editing = null, editContent = '', editCategory = 'preference', editExpires = ''
  let showAdd = false, addContent = '', addCategory = 'preference', addExpires = ''
  let showPurge = false, purgeAck = false
  let history = null, historyFor = null
  let view = 'facts'
  let generation = 0

  $: categories = info.categories?.length ? info.categories : ['preference', 'identity', 'constraint', 'entity']
  $: agentID, reload()

  async function reload() {
    generation++
    const current = generation
    busy = true; error = ''
    try {
      const [st, list, rels] = await Promise.all([
        api.memory.facts.status(agentID).catch(() => ({ enabled: false })),
        api.memory.facts.list({ agentId: agentID, status: query ? '' : status, q: query }),
        api.memory.facts.relations(agentID, 'active').catch(() => ({ relations: [] })),
      ])
      if (current !== generation) return
      info = st
      facts = Array.isArray(list.facts) ? list.facts : []
      relations = Array.isArray(rels.relations) ? rels.relations : []
    } catch (e) {
      if (current === generation) { error = e.message; facts = [] }
    } finally {
      if (current === generation) busy = false
    }
  }

  function dateOnly(ts) {
    if (!ts) return ''
    const d = new Date(ts)
    return isNaN(d) ? '' : d.toISOString().slice(0, 10)
  }
  function startEdit(f) { editing = f.id; editContent = f.content; editCategory = f.category; editExpires = dateOnly(f.expires_at) }
  function cancelEdit() { editing = null }
  async function saveEdit(f) {
    if (!editContent.trim()) return
    busy = true; error = ''
    try {
      const res = await api.memory.facts.update(f.id, f.agent_id || agentID, editContent, editCategory, editExpires || 'none')
      facts = facts.map(x => x.id === f.id ? res.fact : x)
      notice = 'Fact updated.'; editing = null
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function remove(f) {
    if (!window.confirm('Delete this fact? Agents will stop seeing it immediately. Its history is kept.')) return
    busy = true; error = ''
    try {
      await api.memory.facts.remove(f.id, f.agent_id || agentID)
      facts = facts.filter(x => x.id !== f.id)
      notice = 'Fact deleted.'
      info = await api.memory.facts.status(agentID).catch(() => info)
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function removeRelation(r) {
    if (!window.confirm(`Forget that ${r.subject} ${r.predicate} ${r.object}?`)) return
    busy = true; error = ''
    try {
      await api.memory.facts.removeRelation(r.id, r.agent_id || agentID)
      relations = relations.filter(x => x.id !== r.id)
      notice = 'Relationship deleted.'
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function add() {
    if (!addContent.trim()) return
    if (!agentID) { error = 'Choose an agent before adding a fact.'; return }
    busy = true; error = ''
    try {
      const res = await api.memory.facts.add(agentID, addContent, addCategory, addExpires)
      facts = [res.fact, ...facts]
      addContent = ''; addExpires = ''; showAdd = false; notice = 'Fact added. It applies to the next turn.'
      info = await api.memory.facts.status(agentID).catch(() => info)
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function purge() {
    if (!purgeAck) return
    busy = true; error = ''
    try {
      const res = await api.memory.facts.purge(agentID)
      facts = []; relations = []; showPurge = false; purgeAck = false
      notice = `Removed ${res.removed} fact${res.removed === 1 ? '' : 's'} and their relationships and history.`
      info = await api.memory.facts.status(agentID).catch(() => info)
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function openHistory(f) {
    historyFor = f; history = null; error = ''
    try {
      const res = await api.memory.facts.history(f.id, f.agent_id || agentID)
      history = res.events || []
    } catch (e) { error = e.message; historyFor = null }
  }
  async function exportJSON() {
    // Authenticated download: fetch with the bearer token, then hand the
    // blob to the browser. Credentials never go in the URL.
    busy = true; error = ''
    try {
      const data = await apiFetch(api.memory.facts.exportPath(agentID).replace('/api/v1', ''))
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url; a.download = `soulacy-memory${agentID ? '-' + agentID : ''}.json`
      document.body.appendChild(a); a.click(); a.remove()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (e) { error = e.message } finally { busy = false }
  }
  function when(ts) {
    if (!ts) return ''
    const d = new Date(ts)
    return isNaN(d) ? '' : d.toLocaleString()
  }
  function expired(f) { return f.expires_at && new Date(f.expires_at) < new Date() }
  onMount(reload)
</script>

<div class="facts">
  <div class="tab-toolbar">
    <input class="search-input" bind:value={query} placeholder="Search what Soulacy remembers…" on:keydown={(e) => e.key === 'Enter' && reload()} />
    <select bind:value={status} on:change={reload} disabled={!!query}>
      <option value="active">Active</option>
      <option value="superseded">Superseded</option>
      <option value="retracted">Retracted</option>
      <option value="">All</option>
    </select>
    <button class="btn-secondary" on:click={reload} disabled={busy}>↺</button>
    <span class="spacer"></span>
    <button class="btn-secondary" on:click={() => showAdd = true} disabled={busy || !info.enabled}>➕ Add Fact</button>
    <button class="btn-secondary" on:click={exportJSON} disabled={busy || !info.enabled}>📥 Export Memory (JSON)</button>
    <button class="btn-danger-outline" on:click={() => showPurge = true} disabled={busy || !info.enabled}>⚠️ Purge All User Memory</button>
  </div>

  <div class="meta">
    {#if info.enabled}
      <span class="pill">{info.provider === 'mem0' ? 'External provider' : 'Local engine'}</span>
      {#if info.provider === 'local'}<span class="dim">{info.active ?? 0} active · {info.superseded ?? 0} superseded · {info.retracted ?? 0} retracted · {info.relations ?? 0} relationships</span>{/if}
      <span class="dim">Facts are private to your sign-in. The most relevant few, plus related entities, ride along in each prompt.</span>
    {:else}
      <span class="dim">Adaptive memory is off. Enable it in Config → Adaptive memory.</span>
    {/if}
    <span class="spacer"></span>
    <div class="view-switch">
      <button class="tab-mini {view === 'facts' ? 'active' : ''}" on:click={() => view = 'facts'}>Facts {#if facts.length}<span class="count">{facts.length}</span>{/if}</button>
      <button class="tab-mini {view === 'relations' ? 'active' : ''}" on:click={() => view = 'relations'}>Relationships {#if relations.length}<span class="count">{relations.length}</span>{/if}</button>
    </div>
  </div>

  {#if error}<div class="banner err">⚠ {error}</div>{/if}
  {#if notice}<div class="banner ok">✓ {notice}</div>{/if}

  {#if view === 'relations'}
    {#if !relations.length}
      <div class="empty">{#if !info.graph_enabled}Relationship memory is off in Config → Adaptive memory.{:else}No relationships yet. They appear when you mention people, places, projects, or things and how they connect to you.{/if}</div>
    {:else}
      <table class="fact-table">
        <thead><tr><th>Subject</th><th>Relationship</th><th>Object</th><th>Updated</th><th></th></tr></thead>
        <tbody>
          {#each relations as r (r.id)}
            <tr>
              <td>{r.subject}</td>
              <td class="dim">{r.predicate}</td>
              <td>{r.object}</td>
              <td class="dim nowrap">{when(r.updated_at || r.created_at)}</td>
              <td class="actions nowrap"><button class="btn-danger-outline sm" on:click={() => removeRelation(r)} disabled={busy || info.provider !== 'local'}>🗑️</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {:else if !facts.length}
    <div class="empty">
      {#if busy}Loading…{:else if query}No facts match “{query}”.{:else if status === 'superseded'}Nothing has been superseded yet.{:else if status === 'retracted'}Nothing has been retracted yet. A fact is retracted when you say it is no longer true.{:else}Nothing remembered yet. Chat with an agent and facts about you will appear here, or add one by hand.{/if}
    </div>
  {:else}
    <table class="fact-table">
      <thead><tr><th>Fact</th><th>Category</th><th>Status</th><th>Source</th><th>Expires</th><th>Updated</th><th></th></tr></thead>
      <tbody>
        {#each facts as f (f.id)}
          <tr class={f.status !== 'active' ? 'inactive' : ''}>
            <td class="content">
              {#if editing === f.id}
                <input class="edit-input" bind:value={editContent} maxlength="400" />
              {:else}
                {f.content}
                {#if f.score !== undefined}<span class="score">{Math.round(f.score * 100)}%</span>{/if}
                {#if f.session_scoped}<span class="badge tiny" title="Only used inside the conversation it came from">this conversation</span>{/if}
              {/if}
            </td>
            <td>
              {#if editing === f.id}
                <select bind:value={editCategory}>{#each categories as c}<option value={c}>{c}</option>{/each}</select>
              {:else}
                <span class="badge cat-{f.category}">{f.category}</span>
              {/if}
            </td>
            <td><span class="badge st-{f.status}">{f.status}</span>{#if f.superseded_by}<span class="dim tiny" title={f.superseded_by}> → newer</span>{/if}</td>
            <td class="dim">
              {f.source}{#if f.source_session_id}<a class="src" href={`#/chat?agent=${encodeURIComponent(f.agent_id)}&session=${encodeURIComponent(f.source_session_id)}`} title="Open the conversation this came from"> · turn</a>{/if}
              {#if !agentID && f.agent_id}<span class="tiny"> · {f.agent_id}</span>{/if}
            </td>
            <td class="dim nowrap">
              {#if editing === f.id}
                <input type="date" bind:value={editExpires} title="Leave empty for no expiry" />
              {:else if f.expires_at}
                <span class={expired(f) ? 'expired' : ''}>{dateOnly(f.expires_at)}</span>
              {:else}—{/if}
            </td>
            <td class="dim nowrap">{when(f.updated_at || f.created_at)}</td>
            <td class="actions nowrap">
              {#if editing === f.id}
                <button class="btn-primary sm" on:click={() => saveEdit(f)} disabled={busy}>Save</button>
                <button class="btn-secondary sm" on:click={cancelEdit}>Cancel</button>
              {:else}
                <button class="btn-secondary sm" title="Change history" on:click={() => openHistory(f)} disabled={busy}>🕘</button>
                {#if f.status === 'active'}
                  <button class="btn-secondary sm" on:click={() => startEdit(f)} disabled={busy}>Edit</button>
                {/if}
                <button class="btn-danger-outline sm" on:click={() => remove(f)} disabled={busy}>🗑️</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

{#if showAdd}
  <div class="modal-bg" role="presentation" on:click|self={() => showAdd = false}>
    <div class="modal">
      <div class="modal-header"><h2>➕ Add a fact</h2><button class="modal-close" on:click={() => showAdd = false}>✕</button></div>
      <p class="dim">One short sentence about you, written in the third person, for example “User prefers metric units”.</p>
      <input class="edit-input" bind:value={addContent} maxlength="400" placeholder="User prefers…" />
      <div class="row">
        <select bind:value={addCategory}>{#each categories as c}<option value={c}>{c}</option>{/each}</select>
        <label class="dim">Expires <input type="date" bind:value={addExpires} /></label>
      </div>
      <div class="modal-actions">
        <button class="btn-secondary" on:click={() => showAdd = false}>Cancel</button>
        <button class="btn-primary" on:click={add} disabled={busy || !addContent.trim()}>Add fact</button>
      </div>
    </div>
  </div>
{/if}

{#if historyFor}
  <div class="modal-bg" role="presentation" on:click|self={() => historyFor = null}>
    <div class="modal">
      <div class="modal-header"><h2>🕘 History</h2><button class="modal-close" on:click={() => historyFor = null}>✕</button></div>
      <p class="dim">{historyFor.content}</p>
      {#if history === null}
        <div class="empty">Loading…</div>
      {:else if !history.length}
        <div class="empty">No recorded changes.</div>
      {:else}
        <ul class="history">
          {#each history as e (e.id)}
            <li>
              <span class="badge ev-{e.event}">{e.event}</span>
              <span class="dim tiny">{when(e.created_at)} · {e.actor}</span>
              <div>{e.content}</div>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>
{/if}

{#if showPurge}
  <div class="modal-bg" role="presentation" on:click|self={() => showPurge = false}>
    <div class="modal">
      <div class="modal-header"><h2>⚠️ Purge all user memory?</h2><button class="modal-close" on:click={() => showPurge = false}>✕</button></div>
      <p>This permanently deletes every fact, relationship, and history entry Soulacy remembers about you{agentID ? ` for ${agentID}` : ' across all agents'}. Export first if you want a copy.</p>
      <label class="ack"><input type="checkbox" bind:checked={purgeAck} /> I understand this cannot be undone.</label>
      <div class="modal-actions">
        <button class="btn-secondary" on:click={() => { showPurge = false; purgeAck = false }}>Cancel</button>
        <button class="btn-danger" on:click={purge} disabled={busy || !purgeAck}>Purge everything</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .facts { display: flex; flex-direction: column; gap: 0.75rem; }
  .tab-toolbar { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; }
  .spacer { flex: 1; }
  .meta { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: center; font-size: 0.85rem; }
  .view-switch { display: flex; gap: 0.25rem; }
  .tab-mini { background: none; border: 1px solid rgba(128,128,128,0.35); border-radius: 999px; padding: 0.15rem 0.7rem; cursor: pointer; color: inherit; font-size: 0.8rem; }
  .tab-mini.active { border-color: currentColor; font-weight: 600; }
  .count { opacity: 0.6; margin-left: 0.25rem; }
  .dim { opacity: 0.7; }
  .tiny { font-size: 0.75rem; }
  .nowrap { white-space: nowrap; }
  .expired { text-decoration: line-through; }
  .pill { border: 1px solid currentColor; border-radius: 999px; padding: 0.1rem 0.6rem; font-size: 0.75rem; opacity: 0.85; }
  .empty { padding: 2rem; text-align: center; opacity: 0.7; border: 1px dashed rgba(128,128,128,0.4); border-radius: 0.75rem; }
  .fact-table { width: 100%; border-collapse: collapse; font-size: 0.9rem; }
  .fact-table th { text-align: left; font-weight: 600; opacity: 0.7; padding: 0.4rem 0.5rem; border-bottom: 1px solid rgba(128,128,128,0.3); }
  .fact-table td { padding: 0.45rem 0.5rem; border-bottom: 1px solid rgba(128,128,128,0.15); vertical-align: middle; }
  .fact-table tr.inactive td.content { text-decoration: line-through; opacity: 0.6; }
  .content { max-width: 40ch; }
  .score { margin-left: 0.5rem; font-size: 0.75rem; opacity: 0.6; }
  .badge { display: inline-block; padding: 0.05rem 0.5rem; border-radius: 999px; font-size: 0.75rem; border: 1px solid rgba(128,128,128,0.4); margin-left: 0.25rem; }
  .cat-preference { border-color: #7c9cff; }
  .cat-identity { border-color: #62d2a2; }
  .cat-constraint { border-color: #f6b26b; }
  .cat-entity { border-color: #c58af9; }
  .st-superseded, .st-retracted { opacity: 0.6; }
  .ev-retracted, .ev-deleted { border-color: #f08080; }
  .ev-created { border-color: #62d2a2; }
  .src { margin-left: 0.15rem; }
  .actions { display: flex; gap: 0.35rem; justify-content: flex-end; }
  .sm { padding: 0.2rem 0.55rem; font-size: 0.8rem; }
  .edit-input { width: 100%; padding: 0.35rem 0.5rem; }
  .row { display: flex; gap: 0.75rem; align-items: center; margin-top: 0.5rem; flex-wrap: wrap; }
  .modal-actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 1rem; }
  .ack { display: flex; gap: 0.5rem; align-items: center; margin-top: 0.75rem; }
  .history { list-style: none; padding: 0; margin: 0.5rem 0 0; display: flex; flex-direction: column; gap: 0.6rem; max-height: 50vh; overflow: auto; }
  .history li { border-left: 2px solid rgba(128,128,128,0.35); padding-left: 0.6rem; }
</style>
