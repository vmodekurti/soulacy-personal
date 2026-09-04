<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount, onDestroy } from 'svelte'
  import { api, createEventSocket } from '../lib/api.js'

  let kbs            = []
  let selected       = null
  let documents      = []
  let loading        = true
  let loadingDocs    = false
  let error          = ''
  let info           = ''
  let enabled        = true
  let defaultProvider = 'ollama'
  let defaultModel    = 'nomic-embed-text'
  let embeddingProviders = []

  let showCreate = false
  let newKB = { name: '', description: '', embedding_provider: 'ollama', embedding_model: 'nomic-embed-text' }

  let showIngest = false
  // ingest can be either (a) a single pasted-text doc, or (b) a list of files.
  // When `files.length > 0`, the textarea is disabled and the upload loop runs.
  let ingest = { title: '', content: '', files: [] }
  let ingestProgress = null   // { current, total, currentName, failed: [] }
  let ingesting = false

  let selectedDocs = new Set()  // doc.id values currently checkbox-ticked
  let bulkDeleting = false

  let searchQuery = ''
  let searchTopK  = 5
  let searchHits  = []
  let searching   = false

  async function loadKBs() {
    loading = true
    error   = ''
    try {
      const res = await api.knowledge.list()
      kbs      = res.knowledge_bases || []
      enabled  = res.enabled !== false
      embeddingProviders = res.embedding_providers || []
      if (res.default_embedding_provider) {
        defaultProvider = res.default_embedding_provider
        newKB.embedding_provider = res.default_embedding_provider
      }
      if (res.default_embedding_model) {
        defaultModel = res.default_embedding_model
        newKB.embedding_model = res.default_embedding_model
      }
      if (selected) {
        const stillThere = kbs.find(k => k.name === selected.name)
        if (stillThere) selected = stillThere
        else            selected = null
      }
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  $: selectedEmbeddingProvider = embeddingProviders.find(p => p.id === newKB.embedding_provider)
  $: embeddingModelOptions = selectedEmbeddingProvider?.models || []

  function providerDefaultModel(providerId) {
    const p = embeddingProviders.find(x => x.id === providerId)
    return p?.default_model || p?.models?.[0] || defaultModel
  }

  function openCreateModal() {
    error = ''
    info = ''
    if (!newKB.embedding_provider && embeddingProviders.length > 0) {
      newKB.embedding_provider = defaultProvider || embeddingProviders[0].id
    }
    if (!newKB.embedding_model) {
      newKB.embedding_model = providerDefaultModel(newKB.embedding_provider)
    }
    showCreate = true
  }

  function onEmbeddingProviderChange() {
    newKB.embedding_model = providerDefaultModel(newKB.embedding_provider)
  }

  // ── Ingestion queue (async pipeline) ───────────────────────────────────────
  // Ingestion no longer blocks the upload request: the server returns a job and
  // a background worker does the extract/chunk/embed. These track it live.
  let ingestJobs = []
  let retryingJob = ''

  async function loadIngestJobs() {
    if (!selected) return
    try {
      const res = await api.knowledge.listJobs(selected.name)
      ingestJobs = res.jobs || []
    } catch (_) { /* jobs are supplementary — never break the page */ }
  }

  async function retryIngestJob(job) {
    retryingJob = job.id
    error = ''
    try {
      await api.knowledge.retryJob(job.id)
      await loadIngestJobs()
    } catch (e) {
      error = e.message
    } finally {
      retryingJob = ''
    }
  }

  // Live progress: the worker broadcasts `knowledge.ingest` over the same
  // websocket the rest of the app uses, so the queue updates without polling.
  function onIngestEvent(ev) {
    const p = ev?.payload
    if (!p || !p.id) return
    if (selected && p.kb && p.kb !== selected.name) return
    const i = ingestJobs.findIndex(j => j.id === p.id)
    const next = { ...(i >= 0 ? ingestJobs[i] : {}), ...p, kb_name: p.kb }
    if (i >= 0) {
      ingestJobs[i] = next
      ingestJobs = [...ingestJobs]
    } else {
      ingestJobs = [next, ...ingestJobs]
    }
    // A finished job means the document list changed.
    if (p.status === 'done') loadDocumentsQuietly()
  }

  async function loadDocumentsQuietly() {
    if (!selected) return
    try {
      const res = await api.knowledge.listDocuments(selected.name)
      documents = res.documents || []
    } catch (_) {}
  }

  const jobPctLabel = (j) => (j.status === 'done' ? 100 : (j.progress || 0))

  async function selectKB(kb) {
    selected     = kb
    documents    = []
    searchHits   = []
    searchQuery  = ''
    loadingDocs  = true
    selectedDocs = new Set()
    ingestJobs   = []
    try {
      const res = await api.knowledge.listDocuments(kb.name)
      documents = res.documents || []
      selected  = res.kb || kb
      loadIngestJobs()
    } catch (e) {
      error = e.message
    } finally {
      loadingDocs = false
    }
  }

  function toggleDoc(id) {
    const s = new Set(selectedDocs)
    if (s.has(id)) s.delete(id); else s.add(id)
    selectedDocs = s
  }
  function toggleAllDocs() {
    if (selectedDocs.size === documents.length) {
      selectedDocs = new Set()
    } else {
      selectedDocs = new Set(documents.map(d => d.id))
    }
  }

  async function createKB() {
    error = ''; info = ''
    try {
      const kb = await api.knowledge.create(newKB)
      info = `Created knowledge base "${kb.name}" (dim ${kb.dim}).`
      showCreate = false
      newKB = { name: '', description: '', embedding_provider: defaultProvider, embedding_model: defaultModel }
      await loadKBs()
      const created = kbs.find(k => k.name === kb.name)
      if (created) selectKB(created)
    } catch (e) {
      error = e.message
    }
  }

  async function deleteKB(kb) {
    if (!confirm(`Delete knowledge base "${kb.name}" and all its documents? This cannot be undone.`)) return
    try {
      await api.knowledge.delete(kb.name)
      if (selected?.name === kb.name) selected = null
      await loadKBs()
    } catch (e) {
      error = e.message
    }
  }

  async function ingestDocument() {
    if (!selected) return
    error = ''; info = ''

    // Path A — one or more file uploads. Run sequentially so a 60-page PDF
    // doesn't pile 5 parallel embed jobs onto Ollama at once. Per-file errors
    // are collected and shown at the end instead of aborting the batch.
    if (ingest.files.length > 0) {
      ingesting = true
      ingestProgress = { current: 0, total: ingest.files.length, currentName: '', failed: [] }
      let okCount = 0
      for (let i = 0; i < ingest.files.length; i++) {
        const f = ingest.files[i]
        ingestProgress = { ...ingestProgress, current: i + 1, currentName: f.name }
        try {
          await api.knowledge.upload(selected.name, f, '')
          okCount++
        } catch (e) {
          ingestProgress.failed.push({ name: f.name, error: e.message })
        }
      }
      ingesting = false
      info = `Ingested ${okCount}/${ingest.files.length} file${ingest.files.length === 1 ? '' : 's'}.`
      if (ingestProgress.failed.length > 0) {
        error = `Failed: ${ingestProgress.failed.map(f => `${f.name} (${f.error})`).join('; ')}`
      }
      showIngest = false
      ingest = { title: '', content: '', files: [] }
      ingestProgress = null
      await selectKB(selected)
      await loadKBs()
      return
    }

    // Path B — single pasted-text doc.
    if (!ingest.content.trim()) {
      error = 'Pick one or more files or paste some content.'
      return
    }
    try {
      const doc = await api.knowledge.ingest(selected.name, {
        title: ingest.title || 'Pasted text',
        source: 'paste',
        mime_type: 'text/plain',
        content: ingest.content,
      })
      info = `Ingested "${doc.title}" (${doc.chunk_count} chunks).`
      showIngest = false
      ingest = { title: '', content: '', files: [] }
      await selectKB(selected)
      await loadKBs()
    } catch (e) {
      error = e.message
    }
  }

  async function deleteDoc(doc) {
    if (!confirm(`Delete "${doc.title}"?`)) return
    try {
      await api.knowledge.deleteDocument(selected.name, doc.id)
      await selectKB(selected)
      await loadKBs()
    } catch (e) {
      error = e.message
    }
  }

  async function deleteSelectedDocs() {
    if (selectedDocs.size === 0) return
    const ids = [...selectedDocs]
    if (!confirm(`Delete ${ids.length} document${ids.length === 1 ? '' : 's'}? This cannot be undone.`)) return
    bulkDeleting = true
    error = ''; info = ''
    const failed = []
    for (const id of ids) {
      try {
        await api.knowledge.deleteDocument(selected.name, id)
      } catch (e) {
        const d = documents.find(x => x.id === id)
        failed.push({ name: d?.title || id, error: e.message })
      }
    }
    bulkDeleting = false
    selectedDocs = new Set()
    info = `Deleted ${ids.length - failed.length}/${ids.length} document${ids.length === 1 ? '' : 's'}.`
    if (failed.length > 0) {
      error = `Failed: ${failed.map(f => `${f.name} (${f.error})`).join('; ')}`
    }
    await selectKB(selected)
    await loadKBs()
  }

  async function runSearch() {
    if (!selected || !searchQuery.trim()) return
    searching = true
    searchHits = []
    try {
      const res = await api.knowledge.search(selected.name, searchQuery, searchTopK)
      searchHits = res.hits || []
    } catch (e) {
      error = e.message
    } finally {
      searching = false
    }
  }

  function onFile(e) {
    const fs = Array.from(e.target.files || [])
    ingest.files = fs
    // Title is auto-derived from filename per file when uploading multiples,
    // so we clear the manual title field unless the user typed something.
    if (fs.length > 1) ingest.title = ''
    else if (fs.length === 1 && !ingest.title) ingest.title = fs[0].name
  }
  function removeFile(i) {
    ingest.files = ingest.files.filter((_, j) => j !== i)
  }
  function prettySize(b) {
    if (b < 1024) return `${b} B`
    if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`
    return `${(b / 1024 / 1024).toFixed(1)} MB`
  }

  // Live ingestion progress over the shared event socket.
  let ingestWS = null
  onMount(() => {
    loadKBs()
    try {
      ingestWS = createEventSocket()
      ingestWS.onmessage = (m) => {
        try {
          const ev = JSON.parse(m.data)
          if (ev && ev.type === 'knowledge.ingest') onIngestEvent(ev)
        } catch (_) { /* ignore non-JSON frames */ }
      }
    } catch (_) { /* no live updates — the queue still loads on demand */ }
  })
  onDestroy(() => { try { ingestWS && ingestWS.close() } catch (_) {} })
</script>

<div class="page">
  <div class="page-header">
    <h1>Knowledge</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={loadKBs} disabled={loading}>↺ Refresh</button>
      <button class="btn-primary" on:click={openCreateModal} disabled={!enabled}>+ New KB</button>
    </div>
        <TourButton />
    </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if info}<div class="banner ok">{info}</div>{/if}

  {#if !enabled}
    <div class="banner warn">Knowledge store is disabled. Set <code>knowledge.db_path</code> in <code>config.yaml</code> and restart the gateway.</div>
  {/if}

  <div class="layout">
    <section class="list-panel">
      {#if loading}
        <div class="empty">Loading…</div>
      {:else if kbs.length === 0}
        <div class="empty-state">
          <div class="empty-icon">📚</div>
          <p>No knowledge bases yet.</p>
          <p class="hint">Create one to get started — pick an embedding model and start adding documents.</p>
        </div>
      {:else}
        {#each kbs as kb}
          <button class="kb-row" class:active={selected?.name === kb.name} on:click={() => selectKB(kb)}>
            <div class="kb-row-top">
              <span class="kb-name">{kb.name}</span>
              <span class="kb-counts">{kb.document_count}·{kb.chunk_count}</span>
            </div>
            <p class="kb-desc">{kb.description || kb.embedding_model}</p>
          </button>
        {/each}
      {/if}
    </section>

    <section class="detail-panel">
      {#if !selected}
        <div class="detail-empty">
          <div class="detail-empty-icon">📚</div>
          <p>Select a knowledge base to view its documents.</p>
        </div>
      {:else}
        <div class="detail-header">
          <div>
            <h2 class="detail-name">{selected.name}</h2>
            <p class="detail-meta-text">
              <code>{selected.embedding_provider}/{selected.embedding_model}</code> · dim {selected.dim} ·
              chunks {selected.chunk_size}/{selected.chunk_overlap}
            </p>
          </div>
          <div class="detail-actions">
            <button class="btn-secondary" on:click={() => { showIngest = true; error = ''; info = '' }}>+ Add document</button>
            <button class="btn-danger" on:click={() => deleteKB(selected)}>Delete KB</button>
          </div>
        </div>

        {#if selected.description}
          <p class="detail-description">{selected.description}</p>
        {/if}

        <!-- Ingestion queue: uploads are processed in the background, so show
             what's in flight, how far along it is, and let a failure be retried. -->
        {#if ingestJobs.filter(j => j.status !== 'done').length}
          <div class="docs-section ingest-queue">
            <div class="docs-header">
              <h3>Processing <span class="muted">({ingestJobs.filter(j => j.status !== 'done').length})</span></h3>
              <button class="btn-secondary" on:click={loadIngestJobs}>↻</button>
            </div>
            {#each ingestJobs.filter(j => j.status !== 'done') as j (j.id)}
              <div class="ijob {j.status}">
                <div class="ijob-top">
                  <span class="ijob-title">{j.title || 'Untitled'}</span>
                  <span class="ijob-status">
                    {#if j.status === 'queued'}queued
                    {:else if j.status === 'running'}embedding… {jobPctLabel(j)}%
                    {:else if j.status === 'failed'}failed{#if j.attempt > 1} after {j.attempt} attempts{/if}
                    {/if}
                  </span>
                  {#if j.status === 'failed'}
                    <button class="btn-secondary tiny" on:click={() => retryIngestJob(j)} disabled={retryingJob === j.id}>
                      {retryingJob === j.id ? 'Retrying…' : 'Retry'}
                    </button>
                  {/if}
                </div>
                {#if j.status === 'running' || j.status === 'queued'}
                  <div class="ijob-bar"><div class="ijob-fill" style="width:{jobPctLabel(j)}%"></div></div>
                {/if}
                {#if j.status === 'failed' && j.error}
                  <div class="ijob-err">{j.error}</div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}

        <div class="docs-section">
          <div class="docs-header">
            <h3>Documents <span class="muted">({documents.length})</span></h3>
            {#if selectedDocs.size > 0}
              <div class="bulk-bar">
                <span class="bulk-count">{selectedDocs.size} selected</span>
                <button class="btn-secondary" on:click={() => selectedDocs = new Set()}>Clear</button>
                <button class="btn-danger" on:click={deleteSelectedDocs} disabled={bulkDeleting}>
                  {bulkDeleting ? 'Deleting…' : `Delete ${selectedDocs.size}`}
                </button>
              </div>
            {/if}
          </div>
          {#if loadingDocs}
            <div class="empty">Loading…</div>
          {:else if documents.length === 0}
            <div class="empty">No documents ingested yet.</div>
          {:else}
            <table class="docs-table">
              <thead>
                <tr>
                  <th class="check-col">
                    <input type="checkbox"
                           aria-label="Select all documents"
                           checked={selectedDocs.size === documents.length && documents.length > 0}
                           indeterminate={selectedDocs.size > 0 && selectedDocs.size < documents.length}
                           on:change={toggleAllDocs} />
                  </th>
                  <th>Title</th><th>Source</th><th>Chunks</th><th>Size</th><th>Added</th><th></th>
                </tr>
              </thead>
              <tbody>
                {#each documents as d}
                  <tr class:row-selected={selectedDocs.has(d.id)}>
                    <td class="check-col">
                      <input type="checkbox"
                             aria-label={`Select ${d.title}`}
                             checked={selectedDocs.has(d.id)}
                             on:change={() => toggleDoc(d.id)} />
                    </td>
                    <td class="title-cell">{d.title}</td>
                    <td class="muted">{d.source || '—'}</td>
                    <td>{d.chunk_count}</td>
                    <td>{(d.byte_size / 1024).toFixed(1)} KB</td>
                    <td class="muted">{new Date(d.created_at).toLocaleString()}</td>
                    <td><button class="link-danger" on:click={() => deleteDoc(d)}>Delete</button></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>

        <div class="search-section">
          <h3>Test search</h3>
          <div class="search-row">
            <input
              type="text"
              placeholder="Try a query against this KB…"
              bind:value={searchQuery}
              on:keydown={(e) => e.key === 'Enter' && runSearch()}
            />
            <input type="number" min="1" max="20" bind:value={searchTopK} title="top_k" />
            <button class="btn-primary" on:click={runSearch} disabled={searching || !searchQuery.trim()}>
              {searching ? '…' : 'Search'}
            </button>
          </div>
          {#if searchHits.length > 0}
            <div class="hits">
              {#each searchHits as h, i}
                <div class="hit">
                  <div class="hit-meta">
                    <span class="hit-rank">#{i + 1}</span>
                    <span class="hit-title">{h.doc_title}</span>
                    <span class="hit-distance">d={h.distance.toFixed(4)}</span>
                  </div>
                  <pre class="hit-content">{h.content}</pre>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    </section>
  </div>
</div>

{#if showCreate}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close knowledge base modal"
    on:click|self={() => showCreate = false}
    on:keydown={(e) => e.key === 'Escape' && (showCreate = false)}
  >
    <div class="modal">
      <h2>New knowledge base</h2>
      <label>
        Name
        <input type="text" placeholder="product-docs" bind:value={newKB.name} />
      </label>
      <label>
        Description
        <textarea rows="2" placeholder="What's in this KB?" bind:value={newKB.description}></textarea>
      </label>
      <div class="row">
        <label class="flex">
          Provider
          <select bind:value={newKB.embedding_provider} on:change={onEmbeddingProviderChange}>
            {#if embeddingProviders.length === 0}
              <option value="">No embedding providers</option>
            {:else}
              {#each embeddingProviders as provider}
                <option value={provider.id}>{provider.id}</option>
              {/each}
            {/if}
          </select>
        </label>
        <label class="flex">
          Model
          <select bind:value={newKB.embedding_model} disabled={!newKB.embedding_provider}>
            {#if embeddingModelOptions.length === 0}
              <option value={newKB.embedding_model || ''}>{newKB.embedding_model || 'No models available'}</option>
            {:else}
              {#each embeddingModelOptions as model}
                <option value={model}>{model}</option>
              {/each}
            {/if}
          </select>
        </label>
      </div>
      <p class="hint">Dim is probed automatically from the embedder when you create the KB.</p>
      <div class="modal-row">
        <button class="btn-secondary" on:click={() => showCreate = false}>Cancel</button>
        <button class="btn-primary" on:click={createKB} disabled={!newKB.name.trim() || !newKB.embedding_provider || !newKB.embedding_model}>Create</button>
      </div>
    </div>
  </div>
{/if}

{#if showIngest}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close ingest modal"
    on:click|self={() => { if (!ingesting) showIngest = false }}
    on:keydown={(e) => e.key === 'Escape' && !ingesting && (showIngest = false)}
  >
    <div class="modal wide">
      <h2>Add document{ingest.files.length > 1 ? 's' : ''}</h2>

      {#if ingest.files.length <= 1}
        <label>
          Title <span class="opt">(optional — auto-filled for single uploads)</span>
          <input type="text" placeholder="Auto-filled from filename" bind:value={ingest.title} disabled={ingesting} />
        </label>
      {/if}

      <label>
        Files <span class="opt">(.md / .txt / .pdf / .docx — pick one or many)</span>
        <input type="file" multiple accept=".md,.markdown,.txt,.pdf,.docx" on:change={onFile} disabled={ingesting} />
      </label>

      {#if ingest.files.length > 0}
        <div class="file-list">
          {#each ingest.files as f, i}
            <div class="file-row">
              <span class="file-name">{f.name}</span>
              <span class="file-size">{prettySize(f.size)}</span>
              <button class="link-danger" on:click={() => removeFile(i)} disabled={ingesting}>×</button>
            </div>
          {/each}
          <p class="hint">{ingest.files.length} file{ingest.files.length === 1 ? '' : 's'} queued — they'll be embedded one at a time so Ollama isn't overwhelmed.</p>
        </div>
      {/if}

      {#if ingest.files.length === 0}
        <label>
          or paste text
          <textarea rows="8" placeholder="Paste content here…" bind:value={ingest.content} disabled={ingesting}></textarea>
        </label>
      {/if}

      {#if ingestProgress}
        <div class="progress">
          <div class="progress-bar">
            <div class="progress-fill" style="width: {(ingestProgress.current / ingestProgress.total) * 100}%"></div>
          </div>
          <p class="progress-text">
            Ingesting {ingestProgress.current} / {ingestProgress.total}: <code>{ingestProgress.currentName}</code>
          </p>
          {#if ingestProgress.failed.length > 0}
            <p class="progress-fail">⚠ {ingestProgress.failed.length} failed so far</p>
          {/if}
        </div>
      {/if}

      <div class="modal-row">
        <button class="btn-secondary"
                on:click={() => { showIngest = false; ingest = { title: '', content: '', files: [] }; ingestProgress = null }}
                disabled={ingesting}>Cancel</button>
        <button class="btn-primary" on:click={ingestDocument} disabled={ingesting}>
          {ingesting ? 'Ingesting…' : (ingest.files.length > 1 ? `Ingest ${ingest.files.length} files` : 'Ingest')}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; height: 100%; min-height: 0; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(96,240,160,.08); border: 1px solid rgba(96,240,160,.3); color: #60f0a0; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .banner code { background: rgba(0,0,0,.25); padding: .05rem .3rem; border-radius: 4px; }

  .layout { display: flex; gap: 1rem; flex: 1; min-height: 0; overflow: hidden; }

  .list-panel {
    width: 260px; flex-shrink: 0;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 10px;
    overflow-y: auto; display: flex; flex-direction: column;
  }
  .empty { padding: 2rem 1rem; text-align: center; color: #6b7294; font-size: .85rem; }
  .empty-state {
    padding: 2rem 1.25rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .5rem; color: #6b7294;
  }
  .empty-icon { font-size: 2rem; }
  .hint { font-size: .78rem; color: #6b7294; }

  .kb-row {
    width: 100%; text-align: left; background: none; padding: .8rem 1rem;
    border-bottom: 1px solid #1a1e36; border-radius: 0;
    color: #c8cadf; cursor: pointer; transition: background .1s;
    display: flex; flex-direction: column; gap: .3rem;
  }
  .kb-row:hover  { background: #141626; }
  .kb-row.active { background: rgba(108,99,255,.12); }
  .kb-row-top    { display: flex; align-items: center; justify-content: space-between; }
  .kb-name       { font-weight: 600; font-size: .87rem; font-family: monospace; color: #8b85ff; }
  .kb-counts     { font-size: .7rem; color: #6c63ff; }
  .kb-desc       { font-size: .78rem; color: #7b82a8; line-height: 1.4; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }

  .detail-panel {
    flex: 1; min-width: 0;
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    overflow-y: auto; padding: 1.25rem; display: flex; flex-direction: column; gap: 1rem;
  }
  .detail-empty {
    flex: 1; display: flex; flex-direction: column; align-items: center;
    justify-content: center; gap: .75rem; color: #6b7294; text-align: center;
  }
  .detail-empty-icon { font-size: 2.5rem; }

  .detail-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .detail-name   { font-size: 1.05rem; font-weight: 700; font-family: monospace; color: #8b85ff; }
  .detail-meta-text { font-size: .75rem; color: #7b82a8; margin-top: .2rem; }
  .detail-meta-text code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; color: #b0b5d8; }
  .detail-actions { display: flex; gap: .5rem; }
  .detail-description { font-size: .875rem; color: #c8cadf; line-height: 1.55; }

  .docs-section h3,
  .search-section h3 {
    font-size: .8rem; text-transform: uppercase; letter-spacing: .06em;
    color: #555a7a; font-weight: 600; margin-bottom: .5rem;
  }
  .muted { color: #6b7294; font-weight: normal; }

  .docs-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: .5rem; gap: 1rem; }

  /* Ingestion queue — background processing progress */
  .ingest-queue { border: 1px solid #252a46; border-radius: 10px; padding: .7rem .85rem; background: #10121f; margin-bottom: 1rem; }
  .ijob { padding: .5rem 0; border-top: 1px solid #1a1e36; }
  .ijob:first-of-type { border-top: none; }
  .ijob-top { display: flex; align-items: center; gap: .6rem; }
  .ijob-title { font-size: .82rem; color: #dfe2f5; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ijob-status { font-size: .72rem; color: #8f96bb; white-space: nowrap; }
  .ijob.failed .ijob-status { color: #ff8b9c; }
  .ijob-bar { margin-top: .4rem; height: 4px; border-radius: 999px; background: #1c1f35; overflow: hidden; }
  .ijob-fill { height: 100%; background: linear-gradient(90deg, #6c5cff, #8b85ff); transition: width .3s ease; }
  .ijob-err { margin-top: .35rem; font-size: .72rem; color: #ff9a9a; word-break: break-word; }
  .ijob .tiny { padding: .2rem .55rem; font-size: .7rem; border-radius: 5px; }
  .docs-header h3 { margin-bottom: 0; }
  .bulk-bar { display: flex; align-items: center; gap: .5rem; }
  .bulk-count { font-size: .78rem; color: #8b85ff; font-weight: 600; }

  .docs-table { width: 100%; border-collapse: collapse; font-size: .82rem; }
  .docs-table th, .docs-table td { padding: .5rem .65rem; text-align: left; border-bottom: 1px solid #1a1e36; }
  .docs-table th { color: #555a7a; font-weight: 600; font-size: .72rem; text-transform: uppercase; letter-spacing: .04em; }
  .check-col { width: 1.6rem; padding-right: 0 !important; }
  .check-col input[type="checkbox"] { width: auto; margin: 0; cursor: pointer; }
  .row-selected td { background: rgba(108,99,255,.06); }
  .title-cell { font-weight: 500; color: #c8cadf; }
  .link-danger { background: none; border: none; color: #f06060; cursor: pointer; padding: 0; font-size: .82rem; }
  .link-danger:hover { text-decoration: underline; }
  .link-danger:disabled { opacity: .4; cursor: not-allowed; text-decoration: none; }

  /* File list inside the ingest modal */
  .file-list {
    display: flex; flex-direction: column; gap: .35rem;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .55rem .7rem; max-height: 200px; overflow-y: auto;
  }
  .file-row { display: flex; align-items: center; gap: .65rem; font-size: .82rem; }
  .file-name { flex: 1; color: #c8cadf; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .file-size { color: #6b7294; font-family: monospace; font-size: .75rem; }
  .file-list .link-danger { font-size: 1.1rem; line-height: 1; }
  .opt { color: #555a7a; font-weight: normal; }

  /* Progress bar during multi-file upload */
  .progress { display: flex; flex-direction: column; gap: .35rem; margin-top: .25rem; }
  .progress-bar { height: 6px; background: #1a1e36; border-radius: 999px; overflow: hidden; }
  .progress-fill { height: 100%; background: #6c63ff; transition: width .2s; }
  .progress-text { font-size: .78rem; color: #7b82a8; }
  .progress-text code { background: #1c1f35; padding: .05rem .35rem; border-radius: 4px; color: #b0b5d8; }
  .progress-fail { font-size: .78rem; color: #f0c460; }

  .search-row { display: flex; gap: .5rem; align-items: center; margin-bottom: .8rem; }
  .search-row input[type="text"] { flex: 1; }
  .search-row input[type="number"] { width: 5rem; }
  .hits { display: flex; flex-direction: column; gap: .5rem; }
  .hit { background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px; padding: .7rem .9rem; }
  .hit-meta { display: flex; align-items: center; gap: .75rem; font-size: .75rem; color: #6b7294; margin-bottom: .35rem; }
  .hit-rank { color: #6c63ff; font-weight: 600; }
  .hit-title { color: #c8cadf; font-weight: 500; }
  .hit-distance { margin-left: auto; font-family: monospace; }
  .hit-content {
    font-size: .8rem; color: #b0b5d8; white-space: pre-wrap; word-break: break-word;
    line-height: 1.55; margin: 0;
  }

  /* Inputs (shared with other pages but redeclared for isolation) */
  input[type="text"], input[type="number"], input[type="file"], textarea {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    color: #c8cadf; padding: .5rem .7rem; font-size: .85rem; font-family: inherit; width: 100%;
  }
  textarea { font-family: monospace; resize: vertical; }
  label { display: flex; flex-direction: column; gap: .35rem; font-size: .78rem; color: #7b82a8; }
  .row { display: flex; gap: .75rem; }
  .row .flex { flex: 1; }

  .btn-primary, .btn-secondary, .btn-danger {
    padding: .5rem .85rem; border-radius: 6px; font-size: .82rem; cursor: pointer; border: 1px solid transparent;
  }
  .btn-primary { background: #6c63ff; color: white; border-color: #6c63ff; }
  .btn-primary:disabled { opacity: .5; cursor: not-allowed; }
  .btn-secondary { background: #1a1e36; color: #c8cadf; border-color: #2a2f4a; }
  .btn-danger { background: transparent; color: #f06060; border-color: rgba(240,96,96,.4); }

  .modal-bg {
    position: fixed; inset: 0; background: rgba(5,7,18,.6);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 420px; max-width: 92vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: .75rem;
  }
  .modal.wide { width: 620px; }
  .modal h2 { font-size: 1.05rem; font-weight: 600; margin-bottom: .25rem; }
  .modal-row {
    display: flex; justify-content: flex-end; gap: .5rem; margin-top: .5rem;
    position: sticky; bottom: 0; z-index: 5;
    background: #141626; padding-top: .6rem;
    box-shadow: 0 -10px 12px -10px rgba(0, 0, 0, 0.6);
  }

  /* Story 15: the fixed left column stacks on narrow screens. */
  @media (max-width: 768px) {
    .layout { flex-direction: column; overflow: visible; }
    .layout > :first-child { width: 100%; max-height: 240px; }
  }
</style>
