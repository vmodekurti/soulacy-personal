<script>
  import { onMount, onDestroy } from 'svelte'
  import { apiFetch } from './api.js'
  import { apiKey } from './stores.js'
  import { validateJobs, validateJob, validateReview, directions, statusLabel, displayValue } from './safeUndo.js'
  export let agentID
  let jobs = [], job = null, review = null, resources = [], busy = false, error = '', notice = '', acknowledged = false, now = Date.now()
  let generation = 0, timer
  const path = () => `/agents/${encodeURIComponent(agentID)}/safe-undo`
  const title = d => ({ apply: 'Apply changes', undo: 'Undo changes', reconcile: 'Accept observed state' }[d])
  const post = (url, body) => apiFetch(url, { method: 'POST', body: JSON.stringify(body) })
  function clear() { generation++; jobs = []; job = null; review = null; resources = []; busy = false; acknowledged = false; notice = '' }
  async function load() {
    if (busy) return
    clear(); const current = generation; busy = true; error = ''
    try {
      const [list, config] = await Promise.all([apiFetch(`${path()}/jobs`), apiFetch(`${path()}/resources`)])
      if (current !== generation) return
      jobs = validateJobs(list, agentID)
      if (!Array.isArray(config?.resources) || config.resources.length > 64 || config.resources.some(r => r.agent_id !== agentID || !r.id || !r.name)) throw new Error('Invalid Safe Undo resource configuration.')
      resources = config.resources
    } catch (e) { if (current === generation) error = e.message || 'Safe Undo is unavailable.' }
    finally { if (current === generation) busy = false }
  }
  async function open(id) {
    if (busy) return
    const current = ++generation; busy = true; job = null; review = null; error = ''; notice = ''; acknowledged = false
    try {
      const value = await apiFetch(`${path()}/jobs/${encodeURIComponent(id)}`)
      if (current === generation) job = validateJob(value, agentID, id)
    } catch (e) { if (current === generation) error = e.message || 'Could not load the receipt.' }
    finally { if (current === generation) busy = false }
  }
  async function preview(direction) {
    if (busy || !job) return
    const current = ++generation, id = job.id; busy = true; review = null; acknowledged = false; error = ''; notice = ''
    try {
      const value = await post(`${path()}/jobs/${encodeURIComponent(id)}/preview`, { direction })
      if (current === generation) { review = validateReview(value, agentID, id, direction); job = review.job; now = Date.now() }
    } catch (e) { if (current === generation) error = e.message || 'Could not prepare a current preview.' }
    finally { if (current === generation) busy = false }
  }
  async function execute() {
    if (busy || !review || !acknowledged || Date.parse(review.expires_at) <= Date.now()) return
    const current = ++generation, approved = review, id = review.job.id
    busy = true; review = null; acknowledged = false; error = ''; notice = ''
    try {
      const value = await post(`${path()}/jobs/${encodeURIComponent(id)}/execute`, { token: approved.token, confirmed: true })
      if (current === generation) {
        job = validateJob(value, agentID, id)
        jobs = jobs.map(j => j.id === id ? { ...j, status: job.status, updated_at: job.updated_at } : j)
        notice = job.notice || 'Receipt updated. Review each resource’s status below.'
      }
    } catch (e) {
      if (current === generation) {
        // A lost response is not proof of failure. Do not offer another write
        // against the old in-memory receipt; fetch canonical state first.
        job = null
        error = `${e.message || 'The response was lost.'} No automatic retry was made. Reopen the receipt to check what happened.`
      }
    } finally { if (current === generation) busy = false }
  }
  onMount(() => {
    let first = true
    const unsubscribe = apiKey.subscribe(() => {
      if (first) { first = false; return }
      clear(); error = 'Gateway sign-in changed. Refresh to load Safe Undo.'
    })
    timer = setInterval(() => { now = Date.now() }, 1000)
    load()
    return unsubscribe
  })
  onDestroy(() => { clear(); clearInterval(timer) })
</script>

<section class="safe-undo" aria-label="Safe Undo" aria-busy={busy}>
  <header><div><h2>Safe Undo</h2><p>Review a change. Keep its receipt. Undo supported changes when it is safe.</p></div><button class="btn" disabled={busy} on:click={load}>Refresh receipts</button></header>
  <p class="boundary">Only configured JSON records and text documents are covered. Sending messages, payments, file creation, and other tool actions are not undone.</p>
  {#if busy}<p role="status">Checking current state…</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if !busy && !error && !resources.length}<p>The server owner must configure a conditional-write resource before Safe Undo can be used.</p>{/if}
  {#if resources.length}<details><summary>{resources.length} supported resource{resources.length === 1 ? '' : 's'}</summary><ul>{#each resources as r}<li>{r.name} · {r.kind === 'json_record' ? 'Selected record fields' : 'Text document'}</li>{/each}</ul></details>{/if}
  <div class="workspace">
    <div class="receipts"><h3>Recent receipts</h3><p>Your latest 100 jobs for this agent.</p>
      {#each jobs as j (j.id)}<button class:active={job?.id === j.id} class="receipt" disabled={busy} on:click={() => open(j.id)}><strong>{j.title}</strong><span>{statusLabel(j.status)} · {j.action_count} change{j.action_count === 1 ? '' : 's'}</span></button>{/each}
      {#if !jobs.length && !busy}<p>Ask this agent to prepare a change with Safe Undo. Nothing is applied until you review and confirm it here.</p>{/if}
    </div>
    {#if job}<article aria-label="Change receipt">
      <h3>{job.title}</h3><p>{statusLabel(job.status)} · <code>{job.id}</code></p>
      {#if job.notice}<p class="boundary">{job.notice}</p>{/if}
      {#if review}<div class="review"><h3>{title(review.direction)} — fresh preview</h3><p>{review.warning}</p>
        {#if review.direction === 'reconcile'}<p>Confirm these observations only after inspecting the external resources:</p>{/if}
        <ol>{#each review.steps as step}<li>{job.actions[step.index].name} → {statusLabel(step.result)}</li>{/each}</ol>
        <p>{review.direction === 'undo' ? 'The recorded “After” values below will be restored to “Before”.' : review.direction === 'apply' ? 'Only the pending changes listed above will be applied.' : 'No external writes will be made.'}</p>
        <p>{now >= Date.parse(review.expires_at) ? 'Preview expired. Request a fresh preview.' : 'This preview expires in five minutes and will be checked again before writing.'}</p>
        <label class="ack"><input type="checkbox" bind:checked={acknowledged} disabled={busy}/>{review.direction === 'reconcile' ? 'I inspected the resources and accept the observations above.' : 'I reviewed these changes and understand that a partial failure can stop the remaining steps.'}</label>
        <div class="actions"><button class="btn" disabled={busy || !acknowledged || now >= Date.parse(review.expires_at)} on:click={execute}>Confirm: {title(review.direction)}</button><button class="btn" disabled={busy} on:click={() => { review = null; acknowledged = false }}>Cancel preview</button></div>
      </div>{:else}<div class="actions">{#each directions(job) as direction}<button class="btn" disabled={busy} on:click={() => preview(direction)}>Preview: {title(direction)}</button>{/each}</div>{/if}
      {#each job.actions as a}<section class="change"><h4>{a.name}</h4><p>{statusLabel(a.status)}{a.reconciled ? ' · State accepted by an operator; not proof of the original request' : ''}</p>
        {#if a.kind === 'json_record'}{#each a.fields as f}<h5>{f.name}</h5><div class="diff"><div><small>Before</small><pre>{displayValue(f.before)}</pre></div><div><small>After</small><pre>{displayValue(f.after)}</pre></div></div>{/each}
        {:else}<div class="diff"><div><small>Before</small><pre>{a.before_text}</pre></div><div><small>After</small><pre>{a.after_text}</pre></div></div>{/if}
      </section>{/each}
      <p class="boundary">JSON undo preserves unrelated fields but cannot detect outside edits that later return a field to the same value. Text documents require the exact recorded version; no automatic merge is attempted.</p>
    </article>{/if}
  </div>
</section>

<style>
  .safe-undo { padding:20px; color:var(--text); } header { display:flex; align-items:center; justify-content:space-between; gap:16px; flex-wrap:wrap; } h2,h3,h4 { margin:0 0 8px; } h5 { margin:12px 0 6px; } p,li { font-size:.85rem; line-height:1.55; color:var(--text-secondary); } .boundary { padding:12px; border-left:3px solid var(--border); background:var(--bg-secondary); } .workspace { display:grid; grid-template-columns:minmax(220px,1fr) minmax(0,2.5fr); gap:24px; margin-top:24px; } .receipt { display:flex; flex-direction:column; gap:8px; width:100%; text-align:left; padding:14px; margin-bottom:8px; background:none; color:inherit; border:1px solid var(--border); border-radius:10px; cursor:pointer; } .receipt.active { border-color:var(--accent,#777cea); } .receipt span,small { color:var(--text-secondary); font-size:.75rem; } .receipt strong, code, h3,h4,h5 { overflow-wrap:anywhere; } .review { background:var(--bg-secondary); border:1px solid var(--accent,#777cea); border-radius:12px; padding:18px; margin:16px 0; } .ack { display:flex; align-items:flex-start; gap:8px; font-size:.85rem; line-height:1.5; } .ack input { margin-top:4px; } .actions { display:flex; gap:8px; flex-wrap:wrap; margin-top:16px; } .btn { background:var(--bg-secondary); border:1px solid var(--border); color:inherit; border-radius:8px; padding:9px 12px; cursor:pointer; } button:disabled { opacity:.5; cursor:default; } .change { border-top:1px solid var(--border); padding-top:18px; margin-top:20px; } .diff { display:grid; grid-template-columns:1fr 1fr; gap:12px; } .diff>div { min-width:0; } pre { white-space:pre-wrap; overflow-wrap:anywhere; overflow:auto; max-height:340px; padding:12px; background:var(--bg-secondary); border:1px solid var(--border); border-radius:8px; font-size:.8rem; } @media(max-width:800px) { .workspace,.diff { grid-template-columns:1fr; } .safe-undo { padding:12px; } }
</style>
