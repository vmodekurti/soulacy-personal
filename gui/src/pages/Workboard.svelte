<script>
  import TourButton from '../lib/TourButton.svelte'
  import { confirmDestructive } from '../lib/destructive.js'
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { STATUSES, STATUS_LABELS, adjacentStatus, groupByStatus, canRun, runLabel, artifactName, formatBytes, artifactDownloadUrl, PRIORITIES, priorityBadge, parseTags, formatTags, dueInfo } from '../lib/workboard.js'
  import RunMetrics from '../lib/RunMetrics.svelte'
  import { apiKey } from '../lib/stores.js'

  let tasks = []
  let agents = []
  let agentFilter = ''
  let loading = true
  let error = ''

  // Editor modal state. editing === null → closed; editing.id == null → new task.
  let editing = null
  let saving = false

  // Run history shown in the editor modal.
  let runs = []
  let runsLoading = false

  // Artifacts produced by this task's runs (Story 13).
  let artifacts = []
  let artifactsLoading = false

  // Comments & reviewer notes (Story 14).
  let comments = []
  let commentsLoading = false
  let newComment = ''
  let newCommentKind = 'comment'

  $: columns = groupByStatus(
    agentFilter ? tasks.filter(t => t.agent_id === agentFilter) : tasks
  )

  async function load(silent = false) {
    if (!silent) loading = true
    error = ''
    try {
      const [board, agentList] = await Promise.all([
        api.workboard.list(),
        api.agents.list().catch(() => null),
      ])
      // `tasks` has the same hazard as `agents` below and one {#each} of its
      // own — Vasu's fix caught it; keep it.
      tasks = Array.isArray(board?.tasks) ? board.tasks : []
      // Only an array. `agentList?.agents || agentList || []` looks like it is
      // being generous about the response shape, but `{}` is truthy: any answer
      // that is neither {agents:[…]} nor an array — an error envelope, an empty
      // body, a gateway version that renamed the field — put a plain object in
      // `agents`, and `{#each agents}` then threw inside Svelte's flush. Svelte 4
      // has no error boundary, so that did not break the Workboard, it froze the
      // entire app until a reload.
      agents = Array.isArray(agentList?.agents) ? agentList.agents
        : Array.isArray(agentList) ? agentList
        : []
    } catch (e) {
      error = e.message || 'Failed to load workboard'
    } finally {
      loading = false
    }
  }

  // While any task is running, refresh quietly so status flips show up.
  let pollTimer = null
  onMount(() => {
    load()
    pollTimer = setInterval(() => {
      if (tasks.some(t => t.status === 'running')) load(true)
    }, 4000)
  })
  onDestroy(() => clearInterval(pollTimer))

  function newTask() {
    editing = {
      id: null, title: '', description: '', agent_id: agentFilter, status: 'todo',
      owner: '', priority: 'normal', tagsText: '', dueDate: '',
    }
    runs = []
    artifacts = []
    comments = []
  }

  function editTask(t) {
    editing = {
      id: t.id, title: t.title, description: t.description, agent_id: t.agent_id, status: t.status,
      owner: t.owner || '', priority: t.priority || 'normal',
      tagsText: formatTags(t.tags), dueDate: t.due_at ? t.due_at.slice(0, 10) : '',
    }
    loadRuns(t.id)
    loadArtifacts(t.id)
    loadComments(t.id)
  }

  async function loadComments(taskId) {
    commentsLoading = true
    comments = []
    try {
      const resp = await api.workboard.comments(taskId)
      comments = resp?.comments || []
    } catch {
      comments = []
    } finally {
      commentsLoading = false
    }
  }

  async function addComment() {
    if (!editing?.id || !newComment.trim()) return
    error = ''
    try {
      await api.workboard.addComment(editing.id, {
        body: newComment, kind: newCommentKind, author: 'gui-user',
      })
      newComment = ''
      await loadComments(editing.id)
    } catch (e) {
      error = e.message || 'Comment failed'
    }
  }

  async function removeComment(cid) {
    try {
      await api.workboard.deleteComment(cid)
      comments = comments.filter(c => c.id !== cid)
    } catch (e) {
      error = e.message || 'Delete comment failed'
    }
  }

  // The collab payload shared by create and update.
  function collabFields() {
    return {
      owner: editing.owner,
      priority: editing.priority,
      tags: parseTags(editing.tagsText),
      due_at: editing.dueDate ? new Date(editing.dueDate + 'T23:59:59Z').toISOString() : '',
    }
  }

  async function loadArtifacts(taskId) {
    artifactsLoading = true
    artifacts = []
    try {
      const resp = await api.workboard.artifacts(taskId)
      artifacts = resp?.artifacts || []
    } catch {
      artifacts = []
    } finally {
      artifactsLoading = false
    }
  }

  async function loadRuns(taskId) {
    runsLoading = true
    runs = []
    try {
      const resp = await api.workboard.runs(taskId)
      runs = resp?.runs || []
    } catch {
      runs = []
    } finally {
      runsLoading = false
    }
  }

  async function runTask(t) {
    error = ''
    try {
      await api.workboard.run(t.id)
      await load(true)
    } catch (e) {
      error = e.message || 'Run failed to start'
    }
  }

  function fmtTime(iso) {
    if (!iso) return '…'
    const d = new Date(iso)
    return isNaN(d) ? iso : d.toLocaleString()
  }

  async function saveTask() {
    if (!editing || !editing.title.trim()) return
    saving = true
    error = ''
    try {
      if (editing.id == null) {
        await api.workboard.create({
          title: editing.title,
          description: editing.description,
          agent_id: editing.agent_id,
          status: editing.status,
          ...collabFields(),
        })
      } else {
        await api.workboard.update(editing.id, {
          title: editing.title,
          description: editing.description,
          agent_id: editing.agent_id,
          status: editing.status,
          ...collabFields(),
        })
      }
      editing = null
      await load()
    } catch (e) {
      error = e.message || 'Save failed'
    } finally {
      saving = false
    }
  }

  async function move(t, dir) {
    const next = adjacentStatus(t.status, dir)
    if (!next) return
    error = ''
    // Optimistic update; reload on failure.
    const prev = t.status
    t.status = next
    tasks = tasks
    try {
      await api.workboard.update(t.id, { status: next })
    } catch (e) {
      t.status = prev
      tasks = tasks
      error = e.message || 'Move failed'
    }
  }

  async function removeTask(t) {
    if (!confirmDestructive(`Delete task "${t.title}"?`)) return
    error = ''
    try {
      await api.workboard.delete(t.id)
      tasks = tasks.filter(x => x.id !== t.id)
    } catch (e) {
      error = e.message || 'Delete failed'
    }
  }
</script>

<div class="page">
  <div class="page-header">
    <h1>Workboard</h1>
    <div class="header-controls">
      <select bind:value={agentFilter} aria-label="Filter by agent">
        <option value="">All agents</option>
        {#each agents as a}
          <option value={a.id}>{a.name || a.id}</option>
        {/each}
      </select>
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
      <button class="btn-primary" on:click={newTask}>+ New Task</button>
    </div>
        <TourButton />
    </div>

  {#if error}
    <div class="error-banner">{error}</div>
  {/if}

  {#if loading}
    <p class="muted">Loading board…</p>
  {:else}
    <div class="board">
      {#each STATUSES as status}
        <div class="column col-{status}">
          <div class="col-header">
            <span class="col-title">{STATUS_LABELS[status]}</span>
            <span class="col-count">{columns[status].length}</span>
          </div>
          <div class="col-body">
            {#each columns[status] as t (t.id)}
              <div class="task-card">
                <button class="task-main" on:click={() => editTask(t)}
                        title="Edit task">
                  <span class="task-title">{t.title}</span>
                  {#if t.description}
                    <span class="task-desc">{t.description}</span>
                  {/if}
                  {#if t.agent_id}
                    <span class="task-agent">⊕ {t.agent_id}</span>
                  {/if}
                  {#if priorityBadge(t.priority) || t.due_at || t.owner}
                    <span class="task-badges">
                      {#if priorityBadge(t.priority)}<span class="badge prio-{t.priority}" title="Priority: {t.priority}">{priorityBadge(t.priority)} {t.priority}</span>{/if}
                      {#if t.due_at}{@const d = dueInfo(t.due_at)}<span class="badge due" class:overdue={d?.overdue}>{d?.label}</span>{/if}
                      {#if t.owner}<span class="badge owner" title="Owner">@{t.owner}</span>{/if}
                    </span>
                  {/if}
                </button>
                <div class="task-actions">
                  {#if t.agent_id}
                    <button class="mini run" on:click={() => runTask(t)}
                            disabled={!canRun(t)}
                            aria-label="{runLabel(t)} {t.title}">
                      {t.status === 'running' ? '⏳' : '▶'} {t.status === 'running' ? 'Running' : runLabel(t)}
                    </button>
                  {/if}
                  <span class="spacer"></span>
                  <button class="mini" on:click={() => move(t, -1)}
                          disabled={!adjacentStatus(t.status, -1)}
                          aria-label="Move {t.title} left">◀</button>
                  <button class="mini" on:click={() => move(t, +1)}
                          disabled={!adjacentStatus(t.status, +1)}
                          aria-label="Move {t.title} right">▶</button>
                  <button class="mini danger" on:click={() => removeTask(t)}
                          aria-label="Delete {t.title}">✕</button>
                </div>
              </div>
            {/each}
            {#if columns[status].length === 0}
              <p class="empty">No tasks</p>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Task editor modal -->
{#if editing}
  <div class="modal-bg" role="button" tabindex="0" aria-label="Close task editor"
       on:click|self={() => editing = null}
       on:keydown={(e) => e.key === 'Escape' && (editing = null)}>
    <div class="modal">
      <h2>{editing.id == null ? 'New Task' : `Edit Task #${editing.id}`}</h2>

      <label for="wb-title">Title</label>
      <input id="wb-title" bind:value={editing.title} placeholder="What needs doing?"
             on:keydown={(e) => e.key === 'Enter' && saveTask()} />

      <label for="wb-desc">Description</label>
      <textarea id="wb-desc" rows="4" bind:value={editing.description}
                placeholder="Optional details"></textarea>

      <div class="modal-grid">
        <div>
          <label for="wb-agent">Agent</label>
          <select id="wb-agent" bind:value={editing.agent_id}>
            <option value="">— none —</option>
            {#each agents as a}
              <option value={a.id}>{a.name || a.id}</option>
            {/each}
          </select>
        </div>
        <div>
          <label for="wb-status">Status</label>
          <select id="wb-status" bind:value={editing.status}>
            {#each STATUSES as s}
              <option value={s}>{STATUS_LABELS[s]}</option>
            {/each}
          </select>
        </div>
        <div>
          <label for="wb-owner">Owner</label>
          <input id="wb-owner" bind:value={editing.owner} placeholder="who reviews this" />
        </div>
        <div>
          <label for="wb-priority">Priority</label>
          <select id="wb-priority" bind:value={editing.priority}>
            {#each PRIORITIES as p}
              <option value={p}>{p}</option>
            {/each}
          </select>
        </div>
        <div>
          <label for="wb-tags">Tags</label>
          <input id="wb-tags" bind:value={editing.tagsText} placeholder="comma, separated" />
        </div>
        <div>
          <label for="wb-due">Due date</label>
          <input id="wb-due" type="date" bind:value={editing.dueDate} />
        </div>
      </div>

      {#if editing.id != null}
        <div class="runs">
          <h3>Run history</h3>
          {#if runsLoading}
            <p class="muted">Loading runs…</p>
          {:else if runs.length === 0}
            <p class="muted">No runs yet.</p>
          {:else}
            {#each runs as r (r.id)}
              <div class="run-row">
                <div class="run-head">
                  <span class="run-attempt">#{r.attempt}</span>
                  <span class="run-badge run-{r.status}">{r.status}</span>
                  <span class="run-time">{fmtTime(r.started_at)} → {fmtTime(r.ended_at)}</span>
                </div>
                {#if r.session_id}
                  <RunMetrics sessionId={r.session_id} agentId={editing.agent_id} />
                {/if}
                {#if r.result}<div class="run-result">{r.result}</div>{/if}
                {#if r.failure_reason}<div class="run-fail">{r.failure_reason}</div>{/if}
                <div class="run-meta">
                  session: {r.session_id}{r.action_log_path ? ` · log: ${r.action_log_path}` : ''}
                </div>
              </div>
            {/each}
          {/if}
        </div>

        <div class="artifacts">
          <h3>Artifacts</h3>
          {#if artifactsLoading}
            <p class="muted">Loading artifacts…</p>
          {:else if artifacts.length === 0}
            <p class="muted">No files produced yet. Files written by the agent during a run appear here.</p>
          {:else}
            {#each artifacts as a (a.id)}
              <div class="artifact-row">
                <span class="artifact-name" title={a.path}>📄 {artifactName(a.path)}</span>
                <span class="artifact-meta">{formatBytes(a.size_bytes)} · {new Date(a.created_at).toLocaleString()} · {a.tool} · run #{a.run_id}</span>
                <a class="artifact-dl" href={artifactDownloadUrl(a.id, $apiKey)} download
                   title="Download {artifactName(a.path)}">⬇ Download</a>
              </div>
            {/each}
          {/if}
        </div>

        <div class="comments">
          <h3>Comments & review notes</h3>
          {#if commentsLoading}
            <p class="muted">Loading comments…</p>
          {:else if comments.length === 0}
            <p class="muted">No comments yet.</p>
          {:else}
            {#each comments as cm (cm.id)}
              <div class="comment-row" class:review={cm.kind === 'review'}>
                <div class="comment-head">
                  <span class="comment-author">{cm.kind === 'review' ? '🔍' : '💬'} {cm.author}</span>
                  <span class="comment-time">{new Date(cm.created_at).toLocaleString()}</span>
                  <button class="mini danger" on:click={() => removeComment(cm.id)}
                          aria-label="Delete comment by {cm.author}">✕</button>
                </div>
                <div class="comment-body">{cm.body}</div>
              </div>
            {/each}
          {/if}
          <div class="comment-compose">
            <select bind:value={newCommentKind} aria-label="Comment kind">
              <option value="comment">💬 comment</option>
              <option value="review">🔍 review note</option>
            </select>
            <input bind:value={newComment} placeholder="Add a note…"
                   aria-label="New comment"
                   on:keydown={(e) => e.key === 'Enter' && addComment()} />
            <button class="btn-secondary" on:click={addComment} disabled={!newComment.trim()}>Add</button>
          </div>
        </div>
      {/if}

      <div class="modal-row">
        <button class="btn-secondary" on:click={() => editing = null} disabled={saving}>Cancel</button>
        <button class="btn-primary" on:click={saveTask}
                disabled={saving || !editing.title.trim()}>
          {saving ? 'Saving…' : (editing.id == null ? 'Create' : 'Save')}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page { padding: 1.5rem 2rem; flex: 1; display: flex; flex-direction: column; min-height: 0; }
  .page-header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 1.1rem; gap: 0.8rem;
  }
  h1 { font-size: 1.3rem; font-weight: 600; }
  .header-controls { display: flex; gap: 0.6rem; align-items: center; flex-wrap: wrap; }
  .header-controls select { width: min(200px, 100%); }

  .error-banner {
    background: rgba(159, 40, 40, 0.18); border: 1px solid #7f2020;
    color: #ff9a9a; border-radius: 8px;
    padding: 0.55rem 0.9rem; margin-bottom: 0.9rem; font-size: 0.85rem;
  }
  .muted { color: #6b7294; }

  /* ── Board ──────────────────────────────────────────────────────── */
  .board {
    display: grid;
    grid-template-columns: repeat(5, minmax(215px, 1fr));
    gap: 0.8rem;
    flex: 1; min-height: 0;
    overflow-x: auto;
    align-items: start;
  }

  .column {
    background: #10121f; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column;
    max-height: 100%; min-width: 0;
  }
  .col-header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.6rem 0.8rem;
    border-bottom: 1px solid #1a1e36;
    border-top: 2px solid transparent;
    border-radius: 10px 10px 0 0;
  }
  .col-todo         .col-header { border-top-color: #6b7294; }
  .col-running      .col-header { border-top-color: #6c63ff; }
  .col-needs_review .col-header { border-top-color: #f0a060; }
  .col-done         .col-header { border-top-color: #4caf82; }
  .col-failed       .col-header { border-top-color: #c75050; }

  .col-title { font-size: 0.78rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: #c8cadf; }
  .col-count {
    font-size: 0.72rem; font-family: monospace; color: #6b7294;
    background: #1c1f35; border-radius: 999px; padding: 0.05rem 0.5rem;
  }
  .col-body { padding: 0.6rem; display: flex; flex-direction: column; gap: 0.55rem; overflow-y: auto; }
  .empty { color: #3d4360; font-size: 0.78rem; text-align: center; padding: 0.6rem 0; }

  /* ── Cards ──────────────────────────────────────────────────────── */
  .task-card {
    background: #141626; border: 1px solid #232746; border-radius: 8px;
    display: flex; flex-direction: column;
  }
  .task-card:hover { border-color: #2f3560; }
  .task-main {
    background: none; color: inherit; text-align: left;
    display: flex; flex-direction: column; gap: 0.3rem;
    padding: 0.65rem 0.75rem; border-radius: 8px 8px 0 0;
  }
  .task-title { font-size: 0.86rem; font-weight: 500; color: #e8eaf6; overflow-wrap: anywhere; }
  .task-desc {
    font-size: 0.76rem; color: #7b82a8; line-height: 1.45;
    display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden;
  }
  .task-agent { font-size: 0.7rem; font-family: monospace; color: #8b85ff; }

  .task-actions {
    display: flex; gap: 0.25rem; justify-content: flex-end;
    padding: 0.3rem 0.5rem;
    border-top: 1px solid #1a1e36;
  }
  .mini {
    background: none; color: #6b7294;
    font-size: 0.72rem; padding: 0.2rem 0.45rem; border-radius: 5px;
  }
  .mini:hover:not(:disabled) { background: #1c1f35; color: #e8eaf6; }
  .mini.danger:hover:not(:disabled) { background: rgba(159, 40, 40, 0.25); color: #ff9a9a; }
  .mini.run { color: #8b85ff; font-weight: 500; }
  .mini.run:hover:not(:disabled) { background: rgba(108, 99, 255, 0.15); color: #a8a3ff; }
  .spacer { flex: 1; }

  /* ── Run history (modal) ────────────────────────────────────────── */
  .runs { border-top: 1px solid #1a1e36; padding-top: 0.7rem; margin-top: 0.5rem; }
  .runs h3 { font-size: 0.8rem; font-weight: 600; color: #7b82a8; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.5rem; }
  .run-row {
    background: #10121f; border: 1px solid #1a1e36; border-radius: 8px;
    padding: 0.55rem 0.7rem; margin-bottom: 0.5rem;
    display: flex; flex-direction: column; gap: 0.3rem;
  }
  .run-head { display: flex; align-items: center; gap: 0.55rem; flex-wrap: wrap; }
  .run-attempt { font-family: monospace; font-size: 0.78rem; color: #6b7294; }
  .run-badge {
    font-size: 0.68rem; font-weight: 600; text-transform: uppercase;
    padding: 0.08rem 0.5rem; border-radius: 999px;
  }
  .run-badge.run-running { background: rgba(108, 99, 255, 0.18); color: #8b85ff; }
  .run-badge.run-done    { background: rgba(76, 175, 130, 0.18); color: #4caf82; }
  .run-badge.run-failed  { background: rgba(199, 80, 80, 0.18);  color: #ff9a9a; }
  .run-time { font-size: 0.72rem; color: #6b7294; }
  .run-result {
    font-size: 0.78rem; color: #c8cadf; line-height: 1.5;
    white-space: pre-wrap; overflow-wrap: anywhere;
    max-height: 7.5em; overflow-y: auto;
  }
  .run-fail { font-size: 0.78rem; color: #ff9a9a; overflow-wrap: anywhere; }
  .run-meta { font-size: 0.68rem; font-family: monospace; color: #4d5478; overflow-wrap: anywhere; }
  .artifacts { margin-top: 1rem; }
  .artifacts h3 { font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.06em; color: #8a91b4; margin-bottom: 0.5rem; }
  .artifact-row { display: flex; align-items: baseline; gap: 0.6rem; flex-wrap: wrap; padding: 0.35rem 0; border-bottom: 1px solid #1d2138; }
  .artifact-name { font-weight: 600; overflow-wrap: anywhere; }
  .artifact-meta { font-size: 0.7rem; color: #555a7a; font-family: monospace; }
  .artifact-dl { margin-left: auto; font-size: 0.75rem; color: #7aa2ff; text-decoration: none; white-space: nowrap; }
  .artifact-dl:hover { text-decoration: underline; }
  .task-badges { display: flex; gap: 0.35rem; flex-wrap: wrap; margin-top: 0.25rem; }
  .badge { font-size: 0.65rem; padding: 0.05rem 0.35rem; border-radius: 8px; border: 1px solid #2a2f4a; color: #8a91b4; }
  .badge.prio-high { color: #e8b14e; border-color: #6b5320; }
  .badge.prio-urgent { color: #ff7676; border-color: #6b2a2a; }
  .badge.prio-low { color: #6f86bf; }
  .badge.due.overdue { color: #ff7676; border-color: #6b2a2a; font-weight: 600; }
  .comments { margin-top: 1rem; }
  .comments h3 { font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.06em; color: #8a91b4; margin-bottom: 0.5rem; }
  .comment-row { padding: 0.4rem 0.5rem; border-left: 2px solid #2a2f4a; margin-bottom: 0.4rem; }
  .comment-row.review { border-left-color: #c9a227; background: #16182a; }
  .comment-head { display: flex; gap: 0.6rem; align-items: baseline; }
  .comment-author { font-weight: 600; font-size: 0.78rem; }
  .comment-time { font-size: 0.68rem; color: #555a7a; margin-right: auto; }
  .comment-body { font-size: 0.82rem; white-space: pre-wrap; overflow-wrap: anywhere; }
  .comment-compose { display: flex; gap: 0.5rem; margin-top: 0.5rem; }
  .comment-compose input { flex: 1; }

  /* ── Modal ──────────────────────────────────────────────────────── */
  .modal-bg {
    position: fixed; inset: 0; background: rgba(0, 0, 0, 0.65);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 460px; max-width: 92vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: 0.55rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; margin-bottom: 0.4rem; }
  .modal label { font-size: 0.78rem; color: #7b82a8; margin-top: 0.3rem; }
  .modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.8rem; }
  .modal-grid label { display: block; margin-bottom: 0.25rem; }
  .modal-row {
    display: flex; gap: 0.75rem; justify-content: flex-end;
    position: sticky; bottom: 0; background: #141626;
    padding-top: 0.8rem; margin-top: 0.4rem;
    box-shadow: 0 -8px 12px -8px rgba(0, 0, 0, 0.6);
  }

  /* ── Responsive ─────────────────────────────────────────────────── */
  @media (max-width: 1100px) {
    .board {
      grid-template-columns: repeat(5, minmax(230px, 280px));
    }
  }
  @media (max-width: 768px) {
    .board {
      grid-template-columns: 1fr;
      overflow-x: visible;
    }
    .column { max-height: none; }
    .header-controls { width: 100%; }
  }
  @media (max-width: 640px) {
    .modal-grid { grid-template-columns: 1fr; }
  }
</style>
