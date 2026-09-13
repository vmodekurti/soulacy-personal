<script>
  // Adaptive memory: the facts Soulacy has distilled about the signed-in user
  // for one agent (or every agent when agentID is empty). Users can inspect,
  // search, edit, add, delete, export, and purge. Superseded facts stay
  // visible under their own filter as an audit trail.
  import { onMount } from 'svelte'
  import { api, apiFetch } from './api.js'
  import { apiKey } from './stores.js'

  export let agentID = ''

  const CATEGORIES = ['preference', 'identity', 'constraint', 'entity']
  let facts = [], status = 'active', query = '', busy = false, error = '', notice = ''
  let info = { enabled: false, provider: '', active: 0, superseded: 0 }
  let editing = null, editContent = '', editCategory = 'preference'
  let showAdd = false, addContent = '', addCategory = 'preference'
  let showPurge = false, purgeAck = false
  let generation = 0

  $: agentID, reload()

  async function reload() {
    generation++
    const current = generation
    busy = true; error = ''
    try {
      const [st, list] = await Promise.all([
        api.memory.facts.status(agentID).catch(() => ({ enabled: false })),
        api.memory.facts.list({ agentId: agentID, status: query ? '' : status, q: query }),
      ])
      if (current !== generation) return
      info = st
      facts = Array.isArray(list.facts) ? list.facts : []
    } catch (e) {
      if (current === generation) { error = e.message; facts = [] }
    } finally {
      if (current === generation) busy = false
    }
  }

  function startEdit(f) { editing = f.id; editContent = f.content; editCategory = f.category }
  function cancelEdit() { editing = null }
  async function saveEdit(f) {
    if (!editContent.trim()) return
    busy = true; error = ''
    try {
      const res = await api.memory.facts.update(f.id, f.agent_id || agentID, editContent, editCategory)
      facts = facts.map(x => x.id === f.id ? res.fact : x)
      notice = 'Fact updated.'; editing = null
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function remove(f) {
    if (!window.confirm('Delete this fact? Agents will stop seeing it immediately.')) return
    busy = true; error = ''
    try {
      await api.memory.facts.remove(f.id, f.agent_id || agentID)
      facts = facts.filter(x => x.id !== f.id)
      notice = 'Fact deleted.'
      info = await api.memory.facts.status(agentID).catch(() => info)
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function add() {
    if (!addContent.trim()) return
    if (!agentID) { error = 'Choose an agent before adding a fact.'; return }
    busy = true; error = ''
    try {
      const res = await api.memory.facts.add(agentID, addContent, addCategory)
      facts = [res.fact, ...facts]
      addContent = ''; showAdd = false; notice = 'Fact added. It applies to the next turn.'
      info = await api.memory.facts.status(agentID).catch(() => info)
    } catch (e) { error = e.message } finally { busy = false }
  }
  async function purge() {
    if (!purgeAck) return
    busy = true; error = ''
    try {
      const res = await api.memory.facts.purge(agentID)
      facts = []; showPurge = false; purgeAck = false
      notice = `Removed ${res.removed} fact${res.removed === 1 ? '' : 's'}.`
      info = await api.memory.facts.status(agentID).catch(() => info)
    } catch (e) { error = e.message } finally { busy = false }
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
  onMount(reload)
</script>

<div class="facts">
  <div class="tab-toolbar">
    <input class="search-input" bind:value={query} placeholder="Search what Soulacy remembers…" on:keydown={(e) => e.key === 'Enter' && reload()} />
    <select bind:value={status} on:change={reload} disabled={!!query}>
      <option value="active">Active</option>
      <option value="superseded">Superseded</option>
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
      {#if info.provider === 'local'}<span class="dim">{info.active ?? 0} active · {info.superseded ?? 0} superseded</span>{/if}
      <span class="dim">Facts are private to your sign-in. The five most relevant are added to each prompt.</span>
    {:else}
      <span class="dim">Adaptive memory is off. Enable it in Config → Adaptive memory.</span>
    {/if}
  </div>

  {#if error}<div class="banner err">⚠ {error}</div>{/if}
  {#if notice}<div class="banner ok">✓ {notice}</div>{/if}

  {#if !facts.length}
    <div class="empty">
      {#if busy}Loading…{:else if query}No facts match “{query}”.{:else if status === 'superseded'}Nothing has been superseded yet.{:else}Nothing remembered yet. Chat with an agent and facts about you will appear here, or add one by hand.{/if}
    </div>
  {:else}
    <table class="fact-table">
      <thead><tr><th>Fact</th><th>Category</th><th>Status</th><th>Source</th><th>Updated</th><th></th></tr></thead>
      <tbody>
        {#each facts as f (f.id)}
          <tr class={f.status === 'superseded' ? 'superseded' : ''}>
            <td class="content">
              {#if editing === f.id}
                <input class="edit-input" bind:value={editContent} maxlength="400" />
              {:else}
                {f.content}
                {#if f.score !== undefined}<span class="score">{Math.round(f.score * 100)}%</span>{/if}
              {/if}
            </td>
            <td>
              {#if editing === f.id}
                <select bind:value={editCategory}>{#each CATEGORIES as c}<option value={c}>{c}</option>{/each}</select>
              {:else}
                <span class="badge cat-{f.category}">{f.category}</span>
              {/if}
            </td>
            <td><span class="badge st-{f.status}">{f.status}</span>{#if f.superseded_by}<span class="dim tiny" title={f.superseded_by}> → newer</span>{/if}</td>
            <td class="dim">
              {f.source}{#if f.source_session_id}<a class="src" href={`#/chat?agent=${encodeURIComponent(f.agent_id)}&session=${encodeURIComponent(f.source_session_id)}`} title="Open the conversation this came from"> · turn</a>{/if}
              {#if !agentID && f.agent_id}<span class="tiny"> · {f.agent_id}</span>{/if}
            </td>
            <td class="dim nowrap">{when(f.updated_at || f.created_at)}</td>
            <td class="actions nowrap">
              {#if editing === f.id}
                <button class="btn-primary sm" on:click={() => saveEdit(f)} disabled={busy}>Save</button>
                <button class="btn-secondary sm" on:click={cancelEdit}>Cancel</button>
              {:else if f.status === 'active'}
                <button class="btn-secondary sm" on:click={() => startEdit(f)} disabled={busy}>Edit</button>
                <button class="btn-danger-outline sm" on:click={() => remove(f)} disabled={busy}>🗑️ Delete</button>
              {:else}
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
      <select bind:value={addCategory}>{#each CATEGORIES as c}<option value={c}>{c}</option>{/each}</select>
      <div class="modal-actions">
        <button class="btn-secondary" on:click={() => showAdd = false}>Cancel</button>
        <button class="btn-primary" on:click={add} disabled={busy || !addContent.trim()}>Add fact</button>
      </div>
    </div>
  </div>
{/if}

{#if showPurge}
  <div class="modal-bg" role="presentation" on:click|self={() => showPurge = false}>
    <div class="modal">
      <div class="modal-header"><h2>⚠️ Purge all user memory?</h2><button class="modal-close" on:click={() => showPurge = false}>✕</button></div>
      <p>This permanently deletes every fact Soulacy remembers about you{agentID ? ` for ${agentID}` : ' across all agents'}, including the superseded audit trail. Export first if you want a copy.</p>
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
  .dim { opacity: 0.7; }
  .tiny { font-size: 0.75rem; }
  .nowrap { white-space: nowrap; }
  .pill { border: 1px solid currentColor; border-radius: 999px; padding: 0.1rem 0.6rem; font-size: 0.75rem; opacity: 0.85; }
  .empty { padding: 2rem; text-align: center; opacity: 0.7; border: 1px dashed rgba(128,128,128,0.4); border-radius: 0.75rem; }
  .fact-table { width: 100%; border-collapse: collapse; font-size: 0.9rem; }
  .fact-table th { text-align: left; font-weight: 600; opacity: 0.7; padding: 0.4rem 0.5rem; border-bottom: 1px solid rgba(128,128,128,0.3); }
  .fact-table td { padding: 0.45rem 0.5rem; border-bottom: 1px solid rgba(128,128,128,0.15); vertical-align: middle; }
  .fact-table tr.superseded td.content { text-decoration: line-through; opacity: 0.6; }
  .content { max-width: 40ch; }
  .score { margin-left: 0.5rem; font-size: 0.75rem; opacity: 0.6; }
  .badge { display: inline-block; padding: 0.05rem 0.5rem; border-radius: 999px; font-size: 0.75rem; border: 1px solid rgba(128,128,128,0.4); }
  .cat-preference { border-color: #7c9cff; }
  .cat-identity { border-color: #62d2a2; }
  .cat-constraint { border-color: #f6b26b; }
  .cat-entity { border-color: #c58af9; }
  .st-superseded { opacity: 0.6; }
  .src { margin-left: 0.15rem; }
  .actions { display: flex; gap: 0.35rem; justify-content: flex-end; }
  .sm { padding: 0.2rem 0.55rem; font-size: 0.8rem; }
  .edit-input { width: 100%; padding: 0.35rem 0.5rem; }
  .modal-actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 1rem; }
  .ack { display: flex; gap: 0.5rem; align-items: center; margin-top: 0.75rem; }
</style>
