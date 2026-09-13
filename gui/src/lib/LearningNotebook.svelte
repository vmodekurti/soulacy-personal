<script>
  import { onMount, onDestroy } from 'svelte'
  import { apiFetch } from './api.js'
  import { apiKey } from './stores.js'
  import { validateLesson, validateLessons, lessonStatusLabel } from './learningNotebook.js'
  export let agentID
  let lessons = [], selected = null, previous = null, status = 'pending', note = '', busy = false, enabled = false, automatic = false, error = '', notice = '', acknowledged = false
  let generation = 0, offset = 0, total = 0
  const base = () => `/agents/${encodeURIComponent(agentID)}/learning`
  const post = (url, body) => apiFetch(url, { method: 'POST', body: JSON.stringify(body) })
  function clear() { generation++; lessons = []; selected = null; previous = null; acknowledged = false; busy = false; error = ''; notice = '' }
  async function load(page = 0) {
    if (busy || !agentID) return
    clear(); const current = generation; busy = true
    try {
      const value = await apiFetch(`${base()}/lessons?status=${status}&offset=${typeof page === 'number' ? page : 0}`)
      if (current !== generation) return
      lessons = validateLessons(value, agentID); enabled = value.enabled; automatic = value.auto_propose
      offset = value.offset || 0; total = value.total || lessons.length
    } catch (e) { if (current === generation) error = e.message }
    finally { if (current === generation) busy = false }
  }
  async function open(l) {
    if (busy) return
    const current = ++generation; selected = null; previous = null; acknowledged = false; error = ''; notice = ''; busy = true
    try {
      const value = await apiFetch(`${base()}/lessons/${encodeURIComponent(l.id)}`)
      if (current !== generation) return
      selected = validateLesson(value, agentID, l.id)
      if (selected.base_id) {
        const old = await apiFetch(`${base()}/lessons/${encodeURIComponent(selected.base_id)}`)
        if (current === generation) previous = validateLesson(old, agentID, selected.base_id)
      }
    } catch (e) { if (current === generation) { error = e.message; selected = null } }
    finally { if (current === generation) busy = false }
  }
  async function teach() {
    if (busy || !enabled || !note.trim() || new TextEncoder().encode(note).length > 6000) return
    const current = ++generation; busy = true; error = ''; notice = ''; selected = null; previous = null; acknowledged = false
    try {
      const result = await post(`${base()}/teach`, { note })
      if (current !== generation) return
      if (typeof result?.created !== 'boolean' || (result.created && result.lesson?.status !== 'pending')) throw new Error('The server did not confirm a reviewable draft.')
      if (result.lesson) {
        selected = validateLesson(result.lesson, agentID)
        if (selected.base_id) {
          const old = await apiFetch(`${base()}/lessons/${encodeURIComponent(selected.base_id)}`)
          if (current !== generation) return
          previous = validateLesson(old, agentID, selected.base_id)
        }
        notice = result.created ? 'Draft ready. Review it before adding it to your agent’s memory.' : 'This lesson already exists. Its current status is shown below.'
      } else notice = 'No reusable lesson was found. Try a specific preference, correction, or procedure.'
    } catch (e) { if (current === generation) { selected = null; error = `${e.message} Refresh to check whether a draft was saved.` } }
    finally { if (current === generation) busy = false }
  }
  async function change(action) {
    if (busy || !selected || !acknowledged) return
    const current = ++generation, id = selected.id; busy = true; error = ''; notice = ''; acknowledged = false
    try {
      const value = await post(`${base()}/lessons/${encodeURIComponent(id)}/review`, { action, confirmed: true })
      if (current !== generation) return
      selected = validateLesson(value, agentID, action === 'restore' ? value.id : id)
      const expected = { approve: 'active', reject: 'rejected', archive: 'archived', restore: 'pending' }[action]
      if (selected.status !== expected || (action === 'restore' && selected.id === id)) throw new Error('The server did not confirm this change.')
      lessons = lessons.filter(l => l.id !== id)
      notice = action === 'approve' ? 'Available in future runs. This does not change tool permissions or guarantee success.' : action === 'archive' ? 'Disabled for future runs and reads. An already-running response may still contain the old context.' : action === 'restore' ? 'A restoration draft is ready for review; nothing was reactivated yet.' : 'Draft rejected.'
      if (action === 'restore') {
        previous = null
        if (selected.base_id) {
          const old = await apiFetch(`${base()}/lessons/${encodeURIComponent(selected.base_id)}`)
          if (current === generation) previous = validateLesson(old, agentID, selected.base_id)
        }
      }
    } catch (e) { if (current === generation) { selected = null; previous = null; error = `${e.message} Refresh before trying again.` } }
    finally { if (current === generation) busy = false }
  }
  async function feedback(rating) {
    if (busy || selected?.status !== 'active') return
    const current = ++generation, id = selected.id; busy = true; error = ''
    try { const value = await post(`${base()}/lessons/${encodeURIComponent(id)}/feedback`, { rating }); if (current === generation) selected = validateLesson(value, agentID, id) }
    catch (e) { if (current === generation) error = e.message }
    finally { if (current === generation) busy = false }
  }
  onMount(() => {
    let first = true
    const unsubscribe = apiKey.subscribe(() => { if (first) { first = false; return }; clear(); note = ''; enabled = false; error = 'Sign-in changed. Refresh your private lessons.' })
    load(); return unsubscribe
  })
  onDestroy(clear)
</script>

<section class="notebook" aria-label="Learning notebook" aria-busy={busy}>
  <header><div><h2>Gets better with you</h2><p>Keep useful preferences, facts, and procedures. Review each lesson before it shapes future work.</p></div><button disabled={busy} on:click={load}>Refresh lessons</button></header>
  <p class="boundary">Private to your sign-in and this agent. {automatic ? 'Automatic proposals are on.' : 'Learning is proposed when you ask.'} Reading a lesson is not proof that it improved a result.</p>
  {#if !enabled}<p>Enable the learning notebook in this agent’s settings to teach it and use its lessons.</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if busy}<p role="status">Working…</p>{/if}
  <details><summary>Teach a preference or workflow</summary><label for="learning-note">What should this agent learn?</label><textarea id="learning-note" rows="4" bind:value={note} placeholder="For release checks, run the migration against a staging copy first…"></textarea><p>Uses one model call to draft a lesson. Do not include passwords or other secrets.</p><button disabled={busy || !enabled || !note.trim() || new TextEncoder().encode(note).length > 6000} on:click={teach}>Create learning draft</button></details>
  <label class="filter">Show <select bind:value={status} disabled={busy} on:change={load}><option value="pending">Needs review</option><option value="active">In use</option><option value="archived">Disabled</option><option value="superseded">Version history</option><option value="rejected">Rejected</option></select></label>
  {#if total > 100}<div class="actions"><button disabled={busy || offset === 0} on:click={() => load(Math.max(0, offset - 100))}>Previous lessons</button><span>{offset + 1}–{Math.min(offset + 100, total)} of {total}</span><button disabled={busy || offset + 100 >= total} on:click={() => load(offset + 100)}>Next lessons</button></div>{/if}
  <div class="layout"><nav aria-label="Lessons">{#each lessons as l (l.id)}<button class:chosen={selected?.id === l.id} disabled={busy} on:click={() => open(l)}><strong>{l.title}</strong><span>{l.kind} · v{l.version} · {lessonStatusLabel(l.status)}</span></button>{:else}<p>No lessons in this view.</p>{/each}</nav>
  {#if selected}<article>
    <h3>{selected.title}</h3><p>{selected.kind} · Version {selected.version} · {lessonStatusLabel(selected.status)}</p>
    <h4>When to use</h4><p>{selected.trigger}</p>
    {#if previous}<details open><summary>Previous version · {previous.version}</summary><pre>{previous.content}</pre><p>Verification: {previous.verification}</p>{#if previous.pitfalls}<p>Limitations: {previous.pitfalls}</p>{/if}</details>{/if}
    <h4>{previous ? 'Proposed guidance' : 'Guidance'}</h4><pre>{selected.content}</pre>
    {#if selected.pitfalls}<h4>Limitations</h4><p>{selected.pitfalls}</p>{/if}
    <h4>How to check it</h4><p>{selected.verification}</p>
    <details open><summary>Evidence to review</summary><p>A matching quote proves the source exists—not that the lesson’s interpretation is correct.</p>{#each selected.sources as source}<blockquote><small>{source.kind === 'user' ? 'User statement' : `${source.tool} · ${source.failed ? 'failed' : 'returned a result'}`}</small><pre>{source.text}</pre></blockquote>{/each}</details>
    <p>Loaded {selected.uses} time{selected.uses === 1 ? '' : 's'} · Your feedback: {selected.helpful ? 'Helpful' : selected.unhelpful ? 'Needs improvement' : 'Not rated'}</p>
    {#if selected.status === 'active'}<div class="actions"><button disabled={busy} on:click={() => feedback(1)}>Helpful</button><button disabled={busy} on:click={() => feedback(-1)}>Needs improvement</button></div>{/if}
    {#if selected.status !== 'rejected'}<label class="ack"><input type="checkbox" bind:checked={acknowledged} disabled={busy} /> I reviewed this lesson, its sources and limitations.</label><div class="actions">
      {#if selected.status === 'pending'}<button disabled={busy || !acknowledged} on:click={() => change('approve')}>Approve lesson</button><button disabled={busy || !acknowledged} on:click={() => change('reject')}>Reject draft</button>
      {:else if selected.status === 'active'}<button disabled={busy || !acknowledged} on:click={() => change('archive')}>Disable lesson</button>
      {:else}<button disabled={busy || !acknowledged} on:click={() => change('restore')}>Draft restoration</button>{/if}
    </div>{/if}
  </article>{:else}<div class="empty">Select a lesson to see its exact guidance and where it came from.</div>{/if}</div>
</section>

<style>
  .notebook{display:flex;flex-direction:column;gap:16px;min-height:0;overflow:auto;color:var(--text,#d5d8e8)}header{display:flex;justify-content:space-between;align-items:start;gap:20px}h2,h3,h4,p{margin:0}p{line-height:1.6;font-size:.88rem}h4{font-size:.85rem}header p,.boundary,.empty,small{color:var(--text-muted,#949ab5)}button,select,textarea{font:inherit;color:inherit;background:var(--bg-secondary,#161a2d);border:1px solid var(--border,#303752);border-radius:9px;padding:10px 13px}button{cursor:pointer}button:disabled{opacity:.45;cursor:default}button:focus-visible,select:focus-visible,textarea:focus-visible{outline:2px solid #9991ff;outline-offset:2px}textarea{display:block;width:100%;box-sizing:border-box;margin:10px 0;resize:vertical}.layout{display:grid;grid-template-columns:minmax(170px,28%) 1fr;gap:24px}.layout nav{display:flex;flex-direction:column;gap:8px}.layout nav button{text-align:left;display:flex;flex-direction:column;gap:6px}.chosen{border-color:#a79fff}nav span{font-size:.75rem;color:#a5adc6}article{display:flex;flex-direction:column;gap:14px;padding:20px;background:var(--bg-secondary,#121728);border:1px solid var(--border,#303752);border-radius:14px}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:inherit;font-size:.87rem;line-height:1.6;margin:0}details{border:1px solid var(--border,#303752);border-radius:10px;padding:14px}summary{cursor:pointer;margin-bottom:10px}blockquote{border-left:2px solid #6a70a0;margin:12px 0;padding-left:12px}.actions{display:flex;flex-wrap:wrap;gap:10px}.ack{display:flex;gap:10px;align-items:center;font-size:.85rem}.filter{display:flex;align-items:center;gap:10px}[role=alert]{color:#f2a4a4}[role=status]{color:#a4d9c3}.empty{padding:24px} @media(max-width:760px){.layout{grid-template-columns:1fr}header{flex-direction:column}.layout nav{max-height:200px;overflow:auto}}
</style>
