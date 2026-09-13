<script>
  import { onMount, onDestroy } from 'svelte'
  import { apiFetch } from './api.js'
  import { apiKey } from './stores.js'
  export let agentID
  let path = '', listing = null, preview = null, error = '', loading = false
  let generation = 0

  async function load(next = '') {
    const current = ++generation
    path = next; listing = null; preview = null; error = ''; loading = true
    try {
      const result = await apiFetch(`/agents/${encodeURIComponent(agentID)}/files?${new URLSearchParams({path: next})}`)
      if (generation !== current) return
      if (result?.path !== next || result.read_only !== true || !Array.isArray(result.entries) || result.entries.length > 512 ||
          new Set(result.entries.map(e => e.path)).size !== result.entries.length ||
          result.entries.some(e => !e.name || e.name.startsWith('.') || e.name.includes('/') || e.name.includes('\\') ||
            e.path !== (next ? `${next}/${e.name}` : e.name) || !['directory', 'file'].includes(e.kind))) {
        throw new Error('The gateway returned an invalid folder listing.')
      }
      listing = result
    } catch (e) { if (generation === current) error = e.message || 'Could not load this folder.' }
    finally { if (generation === current) loading = false }
  }

  async function open(entry) {
    if (entry.kind === 'directory') return load(entry.path)
    if (!entry.previewable) return
    const current = ++generation
    preview = null; error = ''; loading = true
    try {
      const result = await apiFetch(`/agents/${encodeURIComponent(agentID)}/files/preview?${new URLSearchParams({path: entry.path})}`)
      if (generation !== current) return
      if (result?.path !== entry.path || result.read_only !== true || typeof result.content !== 'string' ||
          new TextEncoder().encode(result.content).length > 131072) throw new Error('The gateway returned an invalid preview.')
      preview = result
    } catch (e) { if (generation === current) error = e.message || 'Could not preview this file.' }
    finally { if (generation === current) loading = false }
  }

  onMount(() => {
    let first = true
    const unsubscribe = apiKey.subscribe(() => {
      if (first) { first = false; return }
      generation++; path = ''; listing = null; preview = null; loading = false
      error = 'Gateway sign-in changed. Refresh to load this folder.'
    })
    load()
    return unsubscribe
  })
  onDestroy(() => { generation++; listing = null; preview = null })
</script>

<section class="published-files" aria-label="Published files" aria-busy={loading}>
  <header>
    <div><h2>Published files</h2><p>Read-only · only folders explicitly shared by the server owner.</p></div>
    <button class="btn-secondary" on:click={() => load(path)} disabled={loading}>Refresh files</button>
  </header>
  <nav aria-label="Published folder">
    <span>{path || 'Published folder'}</span>
    {#if path}<button class="btn-secondary" disabled={loading} on:click={() => load(path.split('/').slice(0, -1).join('/'))}>Up one folder</button>{/if}
  </nav>
  {#if loading}<p role="status">Loading published files…</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
  {#if preview}
    <article aria-label="File preview">
      <h3>{preview.path}</h3>
      <p>{preview.size_bytes} bytes · read-only text preview</p>
      <!-- Never use @html here: HTML, SVG and source files must stay inert. -->
      <pre>{preview.content}</pre>
      <button class="btn-secondary" on:click={() => { generation++; preview = null; loading = false }}>Close preview</button>
    </article>
  {/if}
  {#if listing}
    <ul>
      {#each listing.entries as entry (entry.path)}
        <li><button class="file-row" disabled={loading || (entry.kind !== 'directory' && !entry.previewable)} on:click={() => open(entry)}>
          <span>{entry.kind === 'directory' ? '📁' : '📄'} {entry.name}</span>
          <small>{entry.kind === 'directory' ? 'Folder' : entry.previewable ? `${entry.size_bytes} bytes` : 'Preview unavailable (type or size)'}</small>
        </button></li>
      {/each}
    </ul>
    {#if listing.truncated}<p>This folder is too large to show in full. Ask the owner to split it into smaller folders.</p>
    {:else if !listing.entries.length}<p>No published files in this folder.</p>{/if}
  {/if}
</section>

<style>
  .published-files { margin: 0 20px 20px; padding: 18px; border: 1px solid var(--border, #313448); border-radius: 12px; }
  header, nav { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
  h2 { margin:0; font-size:1rem; } h3 { font-size:.95rem; overflow-wrap:anywhere; }
  p, small { color:var(--text-secondary, #989bac); font-size:.8rem; line-height:1.5; }
  nav { margin:12px 0; font-size:.85rem; overflow-wrap:anywhere; }
  ul { list-style:none; padding:0; margin:12px 0; }
  .file-row { display:flex; align-items:center; justify-content:space-between; gap:12px; width:100%; text-align:left; padding:12px 4px; background:none; color:inherit; border:0; border-bottom:1px solid var(--border,#313448); cursor:pointer; }
  .file-row span { overflow-wrap:anywhere; } button:disabled { opacity:.5; cursor:default; }
  article { margin:12px 0; } pre { max-height:460px; overflow:auto; padding:12px; background:var(--bg-secondary,#11131c); border-radius:8px; font-size:.8rem; }
</style>
