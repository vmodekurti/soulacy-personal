<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let queues = []
  let selected = 'default'
  let items = []
  let loading = true
  let itemsLoading = false
  let error = ''
  let newQueue = ''
  let putJSON = '{ "type": "url", "content": "https://example.com" }'

  async function load() {
    loading = true
    error = ''
    try {
      const res = await api.queues.names()
      queues = res?.queues || []
      if (!queues.some(q => q.queue === selected) && queues.length) selected = queues[0].queue
      await loadItems()
    } catch (e) {
      error = e.message || 'Failed to load queues'
    } finally {
      loading = false
    }
  }

  async function loadItems() {
    itemsLoading = true
    try {
      const res = await api.queues.list(selected, 100)
      items = res?.items || []
    } catch (e) {
      error = e.message || 'Failed to load queue items'
    } finally {
      itemsLoading = false
    }
  }

  async function createQueue() {
    const queue = newQueue.trim() || 'default'
    error = ''
    try {
      await api.queues.create(queue)
      selected = queue
      newQueue = ''
      await load()
    } catch (e) {
      error = e.message || 'Could not create queue'
    }
  }

  async function putItem() {
    error = ''
    let item
    try {
      item = JSON.parse(putJSON)
    } catch (e) {
      error = 'Item must be valid JSON.'
      return
    }
    try {
      await api.queues.put(selected, item)
      await load()
    } catch (e) {
      error = e.message || 'Could not enqueue item'
    }
  }

  async function takeItem() {
    error = ''
    try {
      await api.queues.take(selected)
      await load()
    } catch (e) {
      error = e.message || 'Could not take item'
    }
  }

  async function clearQueue() {
    error = ''
    try {
      await api.queues.clear(selected)
      await load()
    } catch (e) {
      error = e.message || 'Could not clear queue'
    }
  }

  function fmtTime(v) {
    if (!v) return ''
    const d = new Date(v)
    return Number.isNaN(d.getTime()) ? v : d.toLocaleString()
  }

  function pretty(v) {
    try {
      return JSON.stringify(v, null, 2)
    } catch {
      return String(v)
    }
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <div>
      <h1>Queues</h1>
      <p>Ephemeral in-memory handoff buffers for live agents and Studio workflows.</p>
    </div>
    <button class="btn-secondary" on:click={load} disabled={loading}>↻ Refresh</button>
        <TourButton />
    </div>

  {#if error}<div class="banner err">{error}</div>{/if}

  <section class="panel">
    <div class="create-row">
      <input bind:value={newQueue} placeholder="Queue name, e.g. pending_resources" on:keydown={(e) => e.key === 'Enter' && createQueue()} />
      <button on:click={createQueue}>Create</button>
    </div>
    <div class="queue-grid">
      {#if loading && !queues.length}
        <div class="empty">Loading queues...</div>
      {:else if !queues.length}
        <button class="queue-card active" on:click={() => { selected = 'default'; loadItems() }}>
          <strong>default</strong>
          <small>0 items</small>
        </button>
      {:else}
        {#each queues as q}
          <button class="queue-card" class:active={selected === q.queue} on:click={() => { selected = q.queue; loadItems() }}>
            <strong>{q.queue}</strong>
            <small>{q.count || 0} item{q.count === 1 ? '' : 's'}</small>
          </button>
        {/each}
      {/if}
    </div>
  </section>

  <section class="panel split">
    <div>
      <div class="panel-title">
        <h2>{selected}</h2>
        <span>{items.length} shown</span>
      </div>
      <div class="actions">
        <button class="btn-secondary" on:click={loadItems} disabled={itemsLoading}>Reload</button>
        <button class="btn-secondary" on:click={takeItem}>Take oldest</button>
        <button class="danger" on:click={clearQueue}>Clear</button>
      </div>

      <div class="items">
        {#if itemsLoading}
          <div class="empty">Loading items...</div>
        {:else if !items.length}
          <div class="empty">No queued items.</div>
        {:else}
          {#each items as item}
            <article class="item-card">
              <header>
                <strong>{item.id}</strong>
                <small>expires {fmtTime(item.expires_at)}</small>
              </header>
              <pre>{pretty(item.item)}</pre>
            </article>
          {/each}
        {/if}
      </div>
    </div>

    <aside>
      <h2>Add JSON Item</h2>
      <textarea bind:value={putJSON} spellcheck="false"></textarea>
      <button on:click={putItem}>Put item</button>
      <p>Queues reset when the gateway restarts. Use Knowledge or Workboard for durable records.</p>
    </aside>
  </section>
</div>

<style>
  .page { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; height: 100%; min-height: 0; }
  .page-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
  h1, h2, p { margin: 0; }
  h1 { font-size: 1.2rem; }
  h2 { font-size: .95rem; }
  p { color: #858caf; font-size: .82rem; line-height: 1.45; }
  .banner { border-radius: 8px; padding: .7rem .85rem; }
  .err { color: #ff9a9a; background: rgba(255, 90, 90, .12); border: 1px solid rgba(255, 90, 90, .25); }
  .panel { border: 1px solid #20243d; background: #10121f; border-radius: 8px; padding: 14px; }
  .create-row { display: flex; gap: .5rem; margin-bottom: .75rem; }
  .create-row input { flex: 1; }
  .queue-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: .6rem; }
  .queue-card { text-align: left; background: #15182a; border: 1px solid #252a46; border-radius: 8px; padding: .8rem; color: #e8eaf6; }
  .queue-card.active { border-color: #6c63ff; background: rgba(108, 99, 255, .16); }
  .queue-card strong, .queue-card small { display: block; }
  .queue-card small { margin-top: .25rem; color: #8f96bb; }
  .split { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(0, 1fr) 340px; gap: 1rem; }
  .panel-title, .actions, .item-card header { display: flex; align-items: center; justify-content: space-between; gap: .75rem; }
  .panel-title span, .item-card small { color: #777fa5; font-size: .75rem; }
  .actions { justify-content: flex-start; margin: .75rem 0; }
  .danger { background: rgba(184, 65, 65, .22); border-color: rgba(255, 90, 90, .3); color: #ff9a9a; }
  .items { height: calc(100vh - 390px); min-height: 260px; overflow: auto; display: flex; flex-direction: column; gap: .65rem; }
  .item-card { border: 1px solid #252a46; background: #0d1020; border-radius: 8px; padding: .8rem; }
  pre { white-space: pre-wrap; word-break: break-word; color: #cbd0ef; font-size: .78rem; margin: .65rem 0 0; }
  aside { border-left: 1px solid #20243d; padding-left: 1rem; display: flex; flex-direction: column; gap: .7rem; }
  textarea { min-height: 190px; resize: vertical; font-family: monospace; }
  .empty { color: #8f96bb; padding: .8rem; }
  @media (max-width: 980px) {
    .split { grid-template-columns: 1fr; }
    aside { border-left: 0; border-top: 1px solid #20243d; padding: 1rem 0 0; }
    .items { height: auto; max-height: 420px; }
  }
</style>
