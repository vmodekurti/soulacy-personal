<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { sourceKind, needsChecksum, permissionLines, credentialLines, statusInfo, riskSummary, securityVerdict, securityFindingLines, migrationLines } from '../lib/pluginmanage.js'

  let plugins  = []
  let loading  = true
  let error    = ''
  let notice   = ''

  // install form
  let source   = ''
  let checksum = ''
  let staging  = false

  // approval modal
  let preview  = null
  let approving = false

  async function load() {
    loading = true
    error = ''
    try {
      const res = await api.plugins.installed()
      plugins = res.plugins || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function stage() {
    staging = true
    error = ''
    notice = ''
    try {
      const res = await api.plugins.stage(source.trim(), checksum.trim())
      preview = res.preview
    } catch (e) {
      error = e.message
    } finally {
      staging = false
    }
  }

  async function approve() {
    if (!preview) return
    approving = true
    error = ''
    try {
      const res = await api.plugins.approve(preview.staged_id, preview.source, preview.checksum)
      notice = res.note || ''
      preview = null
      source = ''
      checksum = ''
      await load()
    } catch (e) {
      error = e.message
    } finally {
      approving = false
    }
  }

  async function discard() {
    if (!preview) return
    try { await api.plugins.discard(preview.staged_id) } catch { /* staged dirs are disposable */ }
    preview = null
  }

  async function act(fn, id) {
    error = ''
    try {
      const res = await fn(id)
      notice = res.note || ''
      await load()
    } catch (e) {
      error = e.message
    }
  }

  function removePlugin(id) {
    if (!confirm(`Remove plugin "${id}" from disk? This cannot be undone.`)) return
    act(api.plugins.remove, id)
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <h1>Plugins</h1>
    <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
        <TourButton />
    </div>

  {#if error}<div class="msg err">{error}</div>{/if}
  {#if notice}<div class="msg ok">✓ {notice}</div>{/if}

  <!-- Install form -->
  <div class="card install-card">
    <h2>Install a plugin</h2>
    <p class="hint">
      From a git URL, a sha256-checksummed archive (.tar.gz / .zip), or a local directory.
      You'll review everything the plugin requests before anything activates.
    </p>
    <div class="install-row">
      <input
        type="text"
        aria-label="Plugin source (git URL, archive path, or directory)"
        placeholder="https://github.com/acme/soulacy-weather.git"
        bind:value={source}
        disabled={staging}
      />
      {#if needsChecksum(source)}
        <input
          type="text"
          class="checksum"
          aria-label="Archive sha256 checksum"
          placeholder="sha256 checksum (required for archives)"
          bind:value={checksum}
          disabled={staging}
        />
      {/if}
      <button
        class="btn-primary"
        on:click={stage}
        disabled={staging || !source.trim() || (needsChecksum(source) && !checksum.trim())}
      >
        {staging ? 'Fetching…' : sourceKind(source) === 'git' ? '⤓ Clone & review' : '⤓ Fetch & review'}
      </button>
    </div>
  </div>

  <!-- Installed list -->
  <div class="card">
    <h2>Installed plugins</h2>
    {#if loading}
      <p class="hint">Loading…</p>
    {:else if plugins.length === 0}
      <p class="hint">No installer-managed plugins yet. Hand-installed plugins (placed directly in a plugin directory) are managed on disk, not here.</p>
    {:else}
      <table>
        <thead>
          <tr><th>Plugin</th><th>Status</th><th>Source</th><th>Permissions</th><th>Actions</th></tr>
        </thead>
        <tbody>
          {#each plugins as p (p.id)}
            {@const st = statusInfo(p)}
            <tr>
              <td><strong>{p.name || p.id}</strong><div class="sub">{p.id}</div></td>
              <td><span class="badge {st.cls}">{st.label}</span></td>
              <td class="src" title={p.source}>{p.source}</td>
              <td>
                {#each permissionLines(p.permissions) as line}<div class="perm">{line}</div>{/each}
                {#each credentialLines(p.credentials) as line}<div class="perm cred">{line}</div>{/each}
                {#if !p.permissions?.length && !p.credentials?.length}<span class="sub">none</span>{/if}
              </td>
              <td class="actions">
                {#if p.needs_reapproval}
                  <button class="btn-warn" on:click={() => act(api.plugins.reapprove, p.id)}>Re-approve</button>
                {/if}
                {#if p.enabled}
                  <button class="btn-secondary" on:click={() => act(api.plugins.disable, p.id)}>Disable</button>
                {:else}
                  <button class="btn-secondary" on:click={() => act(api.plugins.enable, p.id)}>Enable</button>
                {/if}
                <button class="btn-danger" on:click={() => removePlugin(p.id)}>Remove</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </div>

  <!-- Approval modal -->
  {#if preview}
    <div
      class="modal-backdrop"
      role="button"
      tabindex="0"
      aria-label="Close approval dialog"
      on:click|self={discard}
      on:keydown={(e) => e.key === 'Escape' && discard()}
    >
      <div class="modal">
        <div class="modal-header">
          <h2>Approve “{preview.name || preview.plugin_id}”?</h2>
          <button class="modal-close" on:click={discard}>✕</button>
        </div>

        <p class="risk">{riskSummary(preview)}</p>
        {#if preview.description}<p class="hint">{preview.description}</p>{/if}

        {#if securityVerdict(preview.security)}
          <h3>Safety introspection</h3>
          <p class="sec-badge {securityVerdict(preview.security).cls}">{securityVerdict(preview.security).label}</p>
          {#if preview.security.findings?.length}
            <ul class="sec-findings">
              {#each securityFindingLines(preview.security) as line}<li>{line}</li>{/each}
            </ul>
          {/if}
        {/if}

        {#if preview.permissions?.length}
          <h3>Requested capabilities</h3>
          <ul>{#each permissionLines(preview.permissions) as line}<li>{line}</li>{/each}</ul>
        {/if}
        {#if preview.credentials?.length}
          <h3>Requested credentials</h3>
          <ul>{#each credentialLines(preview.credentials) as line}<li>{line}</li>{/each}</ul>
        {/if}
        {#if preview.migrations?.length}
          <h3>Declared schema migrations</h3>
          <ul class="sec-findings">
            {#each migrationLines(preview.migrations) as line}<li>{line}</li>{/each}
          </ul>
        {/if}
        {#if preview.channels?.length}
          <h3>Sidecar channels</h3>
          <ul>{#each preview.channels as ch}<li>{ch}</li>{/each}</ul>
        {/if}
        {#if preview.providers?.length}
          <h3>LLM providers</h3>
          <ul>{#each preview.providers as pr}<li>{pr}</li>{/each}</ul>
        {/if}

        <p class="hint">
          Nothing is active yet. Approving records exactly these permissions —
          if a future update requests more, the plugin stops loading until you
          re-approve it here.
        </p>

        <div class="modal-footer">
          <button class="btn-secondary" on:click={discard} disabled={approving}>Cancel</button>
          <button class="btn-primary" on:click={approve} disabled={approving}>
            {approving ? 'Installing…' : '✓ Approve & install'}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .page { display: flex; flex-direction: column; gap: 16px; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .card { background: var(--panel, #16161e); border: 1px solid var(--border, #2a2a35); border-radius: 8px; padding: 16px; }
  .card h2 { margin: 0 0 8px; font-size: 1rem; }
  .hint { color: var(--muted, #8888a0); font-size: 0.85rem; margin: 4px 0 10px; }
  .msg { padding: 8px 12px; border-radius: 6px; font-size: 0.9rem; }
  .msg.err { background: #3a1a1a; color: #ff9c9c; }
  .msg.ok  { background: #15301c; color: #7fe09a; }

  .install-row { display: flex; gap: 8px; flex-wrap: wrap; }
  .install-row input { flex: 1 1 320px; padding: 8px 10px; background: var(--bg, #0f0f14);
    border: 1px solid var(--border, #2a2a35); border-radius: 6px; color: inherit; }
  .install-row .checksum { flex-basis: 100%; font-family: monospace; }

  table { width: 100%; border-collapse: collapse; font-size: 0.88rem; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border, #2a2a35); vertical-align: top; }
  .sub { color: var(--muted, #8888a0); font-size: 0.78rem; }
  .src { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .perm { font-family: monospace; font-size: 0.78rem; }
  .perm.cred { color: #d9b86a; }
  .badge { padding: 2px 8px; border-radius: 10px; font-size: 0.75rem; }
  .badge.ok    { background: #15301c; color: #7fe09a; }
  .badge.muted { background: #26262f; color: #8888a0; }
  .badge.warn  { background: #3a2d12; color: #f0c46a; }

  .actions { white-space: nowrap; }
  .actions button { margin-right: 6px; }
  .btn-warn   { background: #3a2d12; color: #f0c46a; border: 1px solid #5a4720; border-radius: 6px; padding: 4px 10px; cursor: pointer; }
  .btn-danger { background: #3a1a1a; color: #ff9c9c; border: 1px solid #5a2727; border-radius: 6px; padding: 4px 10px; cursor: pointer; }

  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex;
    align-items: center; justify-content: center; z-index: 100; }
  .modal { background: var(--panel, #16161e); border: 1px solid var(--border, #2a2a35);
    border-radius: 10px; padding: 18px; width: min(560px, 92vw); max-height: 88vh; overflow-y: auto; }
  .modal-header { display: flex; align-items: center; justify-content: space-between; }
  .modal-header h2 { margin: 0; font-size: 1.05rem; }
  .modal-close { background: none; border: none; color: inherit; cursor: pointer; font-size: 1rem; }
  .modal h3 { font-size: 0.85rem; margin: 12px 0 4px; color: var(--muted, #8888a0); text-transform: uppercase; }
  .modal ul { margin: 0; padding-left: 18px; font-family: monospace; font-size: 0.82rem; }
  .risk { font-weight: 600; }
  .sec-badge { font-weight: 600; padding: 4px 10px; border-radius: 6px; display: inline-block; }
  .sec-badge.ok { background: rgba(76, 175, 130, 0.15); color: #4caf82; }
  .sec-badge.warn { background: rgba(240, 160, 96, 0.15); color: #f0a060; }
  .sec-badge.danger { background: rgba(240, 96, 96, 0.18); color: #f06060; }
  .sec-findings { font-size: 0.85em; max-height: 180px; overflow-y: auto; }
  .modal-footer { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px;
    position: sticky; bottom: 0; background: var(--panel, #16161e); padding-top: 8px; }

  @media (max-width: 768px) {
    table { display: block; overflow-x: auto; }
  }
</style>
