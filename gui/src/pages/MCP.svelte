<script>
  import TourButton from '../lib/TourButton.svelte'
  import { confirmDestructive } from '../lib/destructive.js'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import KeyValueEditor from '../lib/KeyValueEditor.svelte'
  import { activeWorkspace } from '../lib/workspace.js'

  $: workspaceScoped = ['team', 'scale'].includes(String($activeWorkspace?.deploymentMode || '').toLowerCase())
  $: workspaceAdmin = ['owner', 'admin'].includes(String($activeWorkspace?.role || '').toLowerCase())

  let servers = []
  let loading = true
  let error   = ''
  let info    = ''
  let expanded = {}      // serverID → bool
  let restartNeeded = false

  // Edit / create modal state. `editing` is null when the modal is closed.
  // When `editing.id` is set AND matches a row in `servers`, we're editing;
  // otherwise we're creating.
  let editing = null
  let saving = false
  let testing = false
  let testResult = null  // { ok, message }

  // Safe workspace installer: URL -> review -> explicit approval. The token
  // and fingerprint are issued by the gateway and cannot be edited here.
  let installModal = false
  let installURL = ''
  let installReview = null
  let installToken = ''
  let inspecting = false
  let approving = false
  let installError = ''

  function openInstall() {
    installModal = true
    installURL = ''
    installReview = null
    installToken = ''
    installError = ''
  }
  function closeInstall() {
    if (inspecting || approving) return
    installModal = false
  }
  async function inspectInstall() {
    inspecting = true
    installError = ''
    installReview = null
    try {
      const res = await api.mcp.inspectInstall(installURL.trim())
      installToken = res.approval_token
      installReview = res.report
    } catch (e) {
      installError = e.message
    } finally {
      inspecting = false
    }
  }
  async function approveInstall() {
    approving = true
    installError = ''
    try {
      const res = await api.mcp.approveInstall(installToken, installReview.fingerprint)
      info = res.message || 'MCP server installed.'
      installModal = false
      await load()
    } catch (e) {
      installError = e.message
    } finally {
      approving = false
    }
  }

  // Glama provisioner state
  let glamaModal = false
  let glamaURL = ''
  let glamaFetching = false
  let glamaSpec = null      // GlamaProvisionSpec from server
  let glamaEnvRequired = [] // env var names still needed
  let glamaEnv = {}         // user-filled env values
  let glamaError = ''
  let glamaSaving = false

  const BLANK_STDIO = () => ({ id: '', transport: 'stdio', command: '', args: [], env: {}, url: '', headers: {} })

  async function load() {
    loading = true
    error   = ''
    try {
      const res = workspaceScoped ? await api.mcp.ownList() : await api.mcp.list()
      servers = res.servers || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }
  onMount(load)

  function toggle(id) { expanded = { ...expanded, [id]: !expanded[id] } }

  function openNew() {
    editing = BLANK_STDIO()
    testResult = null
    error = ''; info = ''
  }
  function openEdit(s) {
    // Prefill all connection parameters returned from the list endpoint
    editing = {
      ...BLANK_STDIO(),
      id: s.id,
      transport: s.transport || 'stdio',
      command: s.command || '',
      args: s.args ? [...s.args] : [],
      env: s.env ? { ...s.env } : {},
      url: s.url || '',
      headers: s.headers ? { ...s.headers } : {},
    }
    testResult = null
    error = ''; info = ''
  }
  function closeModal() {
    editing = null
    testResult = null
    saving = false
    testing = false
  }

  // Args is an array; expose it as a single text input the user can type
  // space-separated into. Cheap and avoids needing yet another picker.
  function argsToString(arr) { return (arr || []).join(' ') }
  function stringToArgs(str) {
    // Crude split that respects double-quoted segments so `npx -y "@scope/pkg" /path` works.
    const out = []
    const re = /"([^"]*)"|(\S+)/g
    let m
    while ((m = re.exec(str)) !== null) out.push(m[1] !== undefined ? m[1] : m[2])
    return out
  }

  async function testConnection() {
    if (!editing) return
    testing = true
    testResult = null
    try {
      const res = await api.mcp.test(editing)
      testResult = res
    } catch (e) {
      testResult = { ok: false, error: e.message }
    } finally {
      testing = false
    }
  }

  async function save() {
    if (!editing) return
    saving = true
    error = ''; info = ''
    const isExisting = servers.some(s => s.id === editing.id)
    try {
      const res = workspaceScoped
        ? await api.mcp.ownPut(editing.id, editing)
        : (isExisting ? await api.mcp.update(editing.id, editing) : await api.mcp.create(editing))
      info = res.message || 'Saved.'
      if (res.restart_needed) restartNeeded = true
      closeModal()
      // Brief pause so the server process has time to start before we poll.
      await new Promise(r => setTimeout(r, 1500))
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function remove(s) {
    if (!confirmDestructive(`Remove MCP server "${s.id}"?`)) return
    error = ''; info = ''
    try {
      const res = workspaceScoped ? await api.mcp.ownDelete(s.id) : await api.mcp.delete(s.id)
      info = res.message || 'Removed.'
      if (res.restart_needed) restartNeeded = true
      await new Promise(r => setTimeout(r, 500))
      await load()
    } catch (e) {
      error = e.message
    }
  }

  // ── Glama provisioner ─────────────────────────────────────────────────────
  function openGlamaModal() {
    glamaModal = true
    glamaURL = ''
    glamaSpec = null
    glamaEnvRequired = []
    glamaEnv = {}
    glamaError = ''
    glamaFetching = false
    glamaSaving = false
  }
  function closeGlamaModal() {
    glamaModal = false
  }

  async function fetchFromGlama() {
    if (!glamaURL.trim()) return
    glamaFetching = true
    glamaError = ''
    glamaSpec = null
    glamaEnvRequired = []
    try {
      const res = await api.mcp.provisionGlama({ glama_url: glamaURL.trim(), env: {} })
      if (res.ok) {
        // No env required — already saved!
        info = res.message || 'Installed from Glama.'
        if (res.restart_needed) restartNeeded = true
        closeGlamaModal()
        await new Promise(r => setTimeout(r, 1500))
        await load()
      } else {
        // env_required: show credential fields
        glamaSpec = res.spec
        glamaEnvRequired = res.env_required || []
        glamaEnv = {}
        for (const k of glamaEnvRequired) glamaEnv[k] = ''
      }
    } catch (e) {
      glamaError = e.message
    } finally {
      glamaFetching = false
    }
  }

  async function saveGlama() {
    glamaSaving = true
    glamaError = ''
    try {
      const res = await api.mcp.provisionGlama({ glama_url: glamaURL.trim(), env: glamaEnv })
      if (res.ok) {
        info = res.message || 'Installed from Glama.'
        if (res.restart_needed) restartNeeded = true
        closeGlamaModal()
        await new Promise(r => setTimeout(r, 1500))
        await load()
      } else {
        glamaError = 'Some required credentials are still missing.'
        glamaEnvRequired = res.env_required || glamaEnvRequired
      }
    } catch (e) {
      glamaError = e.message
    } finally {
      glamaSaving = false
    }
  }
  // ──────────────────────────────────────────────────────────────────────────

  const TRANSPORT_ICON = { stdio: '⎙', http: '🌐', https: '🌐' }

  // Quick-add templates for common stdio MCP servers. Clicking one prefills
  // the editor; user still has to set paths/tokens.
  const TEMPLATES = [
    { id: 'filesystem', label: 'Filesystem',  command: 'npx', args: ['-y', '@modelcontextprotocol/server-filesystem', '/Users/YOU/Documents'] },
    { id: 'github',     label: 'GitHub',      command: 'npx', args: ['-y', '@modelcontextprotocol/server-github'],     env: { GITHUB_TOKEN: '' } },
    { id: 'slack',      label: 'Slack',       command: 'npx', args: ['-y', '@modelcontextprotocol/server-slack'],      env: { SLACK_BOT_TOKEN: '', SLACK_TEAM_ID: '' } },
    { id: 'postgres',   label: 'Postgres',    command: 'npx', args: ['-y', '@modelcontextprotocol/server-postgres', 'postgresql://localhost/dbname'] },
    { id: 'puppeteer',  label: 'Puppeteer',   command: 'npx', args: ['-y', '@modelcontextprotocol/server-puppeteer'] },
    {
      id: 'browser',
      label: 'Browser headless',
      command: 'npx',
      args: ['-y', '@playwright/mcp@latest', '--browser', 'chromium', '--headless'],
      note: 'Runs Chromium without opening windows. Use this for scheduled agents and normal background work.',
    },
    {
      id: 'browser_visible',
      label: 'Browser visible',
      command: 'npx',
      args: ['-y', '@playwright/mcp@latest', '--browser', 'chromium'],
      note: 'Opens a visible Chromium window. Use only when you need to watch or debug browser automation.',
    },
    { id: 'fetch',      label: 'Web Fetch',   command: 'uvx', args: ['mcp-server-fetch'] },
  ]
  function applyTemplate(tpl) {
    editing = {
      ...BLANK_STDIO(),
      id: tpl.id,
      transport: 'stdio',
      command: tpl.command,
      args: [...tpl.args],
      env: { ...(tpl.env || {}) },
    }
    testResult = null
  }
</script>

<div class="page">
  <div class="page-header">
    <h1>MCP Servers</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
      {#if !workspaceScoped}<button class="btn-glama" on:click={openGlamaModal}>⚡ Glama</button>{/if}
      {#if workspaceScoped && workspaceAdmin}
        <button class="btn-primary" on:click={openInstall}>+ Install from GitHub</button>
      {:else if !workspaceScoped}
        <button class="btn-primary" on:click={openNew}>+ New Server</button>
      {/if}
    </div>
        <TourButton />
    </div>

  {#if restartNeeded && !workspaceScoped}
    <div class="banner warn">
      <span>
        <strong>Restart needed.</strong> MCP config was modified and persisted in <code>config.yaml</code>.
      </span>
      <small>A deployment administrator must restart the gateway from the platform control plane.</small>
    </div>
  {/if}
  {#if error}<div class="banner err">{error}</div>{/if}
  {#if info}<div class="banner ok">{info}</div>{/if}

  {#if loading && servers.length === 0}
    <div class="empty">Loading…</div>
  {:else if servers.length === 0}
    <div class="empty-card">
      <div class="empty-icon">🔌</div>
      <p>No MCP servers configured.</p>
      <p class="hint">{workspaceScoped ? (workspaceAdmin ? 'Paste a GitHub repository URL to inspect and install an isolated MCP server.' : 'A workspace owner or admin can install an MCP server after a security review.') : 'Click + New Server to add one — choose from a template or define your own.'}</p>
    </div>
  {:else}
    <div class="server-list">
      {#each servers as s}
        <div class="srv" class:ok={s.connected}>
          <div class="srv-head">
            <button class="srv-expand" on:click={() => toggle(s.id)}>
              <span class="srv-icon">{TRANSPORT_ICON[s.transport] || '🔌'}</span>
              <div class="srv-identity">
                <span class="srv-name">{s.id}</span>
                <span class="srv-tx">{s.transport}</span>
              </div>
              <span class="srv-tools-count">{s.tools?.length || 0} tool{s.tools?.length === 1 ? '' : 's'}</span>
              <span class="srv-badge" class:bad={!s.connected}>{s.connected ? '● Connected' : '○ ' + (s.detail || 'Disconnected')}</span>
              <span class="srv-chevron">{expanded[s.id] ? '▾' : '▸'}</span>
            </button>
            <div class="srv-actions">
              {#if !workspaceScoped}<button class="btn-secondary tiny" on:click={() => openEdit(s)}>Edit</button>{/if}
              {#if !workspaceScoped || workspaceAdmin}<button class="btn-danger tiny" on:click={() => remove(s)}>Delete</button>{/if}
            </div>
          </div>

          {#if expanded[s.id]}
            <div class="srv-body">
              {#if !s.connected}
                <div class="srv-error">{s.detail || 'Connection failed'}</div>
              {:else if !s.tools || s.tools.length === 0}
                <div class="srv-empty">This server exposes no tools.</div>
              {:else}
                <table class="tbl">
                  <thead>
                    <tr><th>Tool</th><th>LLM-facing name</th><th>Description</th></tr>
                  </thead>
                  <tbody>
                    {#each s.tools as t}
                      <tr>
                        <td class="td-name">{t.name}</td>
                        <td class="td-mono">{t.full_name}</td>
                        <td class="td-desc">{t.description}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}

  <div class="info-card">
    <h3>About MCP</h3>
    <p>
      MCP (<a href="https://spec.modelcontextprotocol.io/" target="_blank" rel="noopener">Model Context Protocol</a>)
      lets Soulacy consume tools from external servers — filesystem, GitHub, Slack, Postgres, web fetch, and many others.
      Tools from connected servers are <strong>auto-injected into every agent</strong> with namespaced names
      (<code>mcp__&lt;server&gt;__&lt;tool&gt;</code>) and routed transparently by the engine.
    </p>
    <p>
      Browser automation should usually use the <strong>Browser headless</strong> quick-start. Soulacy also runs
      a process janitor around MCP tool calls so short-lived browser children are cleaned up after the call returns.
      Use <strong>Browser visible</strong> only for live debugging.
    </p>
    <p>{workspaceScoped ? 'Only workspace owners and admins can install or remove servers. Every install requires a review and explicit approval, and runs from an immutable image without host mounts or inherited gateway secrets.' : 'Changes here are written to config.yaml; the gateway must be restarted to pick them up.'}</p>
  </div>
</div>

{#if installModal}
  <div class="modal-bg" role="button" tabindex="0" aria-label="Close MCP installer" on:click|self={closeInstall} on:keydown={(e) => e.key === 'Escape' && closeInstall()}>
    <div class="modal wide install-modal">
      <h2>Install MCP server</h2>
      <p class="glama-hint">Paste a public GitHub repository. Soulacy inspects its MCP manifest and source first; nothing is executed during inspection.</p>

      <div class="field">
        <span class="field-label">GitHub repository URL <span class="req">*</span></span>
        <input type="url" bind:value={installURL} placeholder="https://github.com/wshobson/maverick-mcp" disabled={inspecting || approving || !!installReview} on:keydown={(e) => e.key === 'Enter' && !installReview && inspectInstall()} />
      </div>

      {#if installError}<div class="banner err">{installError}</div>{/if}

      {#if installReview}
        <div class="review-head">
          <div><strong>{installReview.name}</strong><p>{installReview.description}</p></div>
          <span class="review-ready">Ready for approval</span>
        </div>
        <div class="review-grid">
          <div><span>Source commit</span><code>{installReview.revision}</code></div>
          <div><span>Container image</span><code>{installReview.image}</code></div>
          <div><span>Server ID</span><code>{installReview.server_id}</code></div>
          <div><span>Approval fingerprint</span><code>{installReview.fingerprint}</code></div>
        </div>
        <h3 class="review-title">Security and permissions</h3>
        <div class="finding-list">
          {#each installReview.findings || [] as finding}
            <div class="finding {finding.severity}"><span>{finding.severity === 'warning' ? '⚠' : '✓'}</span><div><strong>{finding.title}</strong><p>{finding.detail}</p></div></div>
          {/each}
        </div>
        <div class="isolation-row">
          <span>Read-only filesystem</span><span>No host mounts</span><span>No Docker socket</span><span>Capabilities dropped</span><span>CPU/RAM/PID limits</span>
        </div>
        {#if installReview.environment?.length}
          <details class="env-review"><summary>Optional/required configuration requested ({installReview.environment.length})</summary>
            {#each installReview.environment as item}<div><code>{item.name}</code> {item.required ? 'required' : 'optional'}{item.secret ? ' · secret' : ''}</div>{/each}
          </details>
        {/if}
        <div class="approval-note"><strong>Explicit approval:</strong> approving pulls the publisher image, pins it by SHA-256 digest, and starts it only inside this workspace’s isolated runtime.</div>
      {/if}

      <div class="modal-row">
        <button class="btn-secondary" on:click={closeInstall} disabled={inspecting || approving}>Cancel</button>
        {#if !installReview}
          <button class="btn-primary" on:click={inspectInstall} disabled={inspecting || !installURL.trim()}>{inspecting ? 'Inspecting…' : 'Inspect repository'}</button>
        {:else}
          <button class="btn-secondary" on:click={() => { installReview = null; installToken = ''; installError = '' }} disabled={approving}>Use another URL</button>
          <button class="btn-primary" on:click={approveInstall} disabled={approving}>{approving ? 'Installing image…' : 'Approve & install'}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}

{#if editing}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close MCP server modal"
    on:click|self={closeModal}
    on:keydown={(e) => e.key === 'Escape' && closeModal()}
  >
    <div class="modal wide">
      <h2>{servers.some(s => s.id === editing.id) ? 'Edit' : 'New'} MCP server</h2>

      {#if !servers.some(s => s.id === editing.id)}
        <div class="templates">
          <span class="templates-label">Quick start:</span>
          {#each TEMPLATES as tpl}
            <button class="template-chip" title={tpl.note || ''} on:click={() => applyTemplate(tpl)}>{tpl.label}</button>
          {/each}
        </div>
      {/if}

      <div class="row-2">
        <div class="field">
          <span class="field-label">Server ID <span class="req">*</span></span>
          <input type="text" bind:value={editing.id}
                 placeholder="filesystem"
                 disabled={servers.some(s => s.id === editing.id)} />
        </div>
        <div class="field">
          <span class="field-label">Transport</span>
          <select bind:value={editing.transport}>
            <option value="stdio">stdio (spawn local process)</option>
            <option value="http">http (remote endpoint)</option>
          </select>
        </div>
      </div>

      {#if editing.transport === 'stdio'}
        <div class="field">
          <span class="field-label">Command <span class="req">*</span></span>
          <input type="text" bind:value={editing.command} placeholder="npx" />
        </div>
        <div class="field">
          <span class="field-label">Arguments <span class="optional">(space-separated, double-quote to group)</span></span>
          <input type="text"
                 value={argsToString(editing.args)}
                 on:input={(e) => editing.args = stringToArgs(e.target.value)}
                 placeholder='-y "@modelcontextprotocol/server-filesystem" /Users/you/Documents' />
        </div>
        <div class="field">
          <span class="field-label">Environment variables <span class="optional">(merged onto os.Environ when the process starts)</span></span>
          <KeyValueEditor
            value={editing.env || {}}
            keyLabel="Variable" valueLabel="Value"
            keyPlaceholder="GITHUB_TOKEN" valuePlaceholder="ghp_..."
            maskValues={true}
            on:change={(e) => editing.env = e.detail}
          />
        </div>
      {:else}
        <div class="field">
          <span class="field-label">URL <span class="req">*</span></span>
          <input type="text" bind:value={editing.url} placeholder="https://example.com/mcp" />
        </div>
        <div class="field">
          <span class="field-label">Headers <span class="optional">(sent on every request)</span></span>
          <KeyValueEditor
            value={editing.headers || {}}
            keyLabel="Header" valueLabel="Value"
            keyPlaceholder="Authorization" valuePlaceholder="Bearer ..."
            maskValues={true}
            on:change={(e) => editing.headers = e.detail}
          />
        </div>
      {/if}

      {#if testResult}
        <div class="test-result" class:ok={testResult.ok}>
          {#if testResult.ok}
            ✓ Reachable{#if testResult.resolved_command} · resolved to <code>{testResult.resolved_command}</code>{/if}{#if testResult.status_code} · HTTP {testResult.status_code}{/if}
          {:else}
            ✗ {testResult.error}
          {/if}
        </div>
      {/if}

      <div class="modal-row">
        <button class="btn-secondary" on:click={closeModal} disabled={saving}>Cancel</button>
        <button class="btn-secondary" on:click={testConnection} disabled={testing || saving}>
          {testing ? 'Testing…' : 'Test connection'}
        </button>
        <button class="btn-primary" on:click={save} disabled={saving || !editing.id.trim()}>
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if glamaModal}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close Glama install modal"
    on:click|self={closeGlamaModal}
    on:keydown={(e) => e.key === 'Escape' && closeGlamaModal()}
  >
    <div class="modal wide">
      <h2>⚡ Install from Glama</h2>
      <p class="glama-hint">
        Paste any <a href="https://glama.ai/mcp/servers" target="_blank" rel="noopener">Glama MCP server</a> URL.
        Soulacy will fetch the config and fill in everything automatically.
      </p>

      <div class="field">
        <span class="field-label">Glama URL <span class="req">*</span></span>
        <input
          type="text"
          bind:value={glamaURL}
          placeholder="https://glama.ai/mcp/servers/adamzaidi/icloud-mcp"
          on:keydown={(e) => e.key === 'Enter' && !glamaSpec && fetchFromGlama()}
        />
      </div>

      {#if glamaError}
        <div class="banner err">{glamaError}</div>
      {/if}

      {#if glamaSpec}
        <div class="glama-spec-card">
          <div class="glama-spec-name">{glamaSpec.name}</div>
          <div class="glama-spec-desc">{glamaSpec.description}</div>
          <div class="glama-spec-cmd"><code>npx {glamaSpec.args?.join(' ')}</code></div>
        </div>

        {#if glamaEnvRequired.length > 0}
          <div class="glama-creds-label">Required credentials</div>
          {#each glamaEnvRequired as envKey}
            <div class="field">
              <span class="field-label">{envKey} <span class="req">*</span></span>
              {#if envKey.toLowerCase().includes('password') || envKey.toLowerCase().includes('token') || envKey.toLowerCase().includes('secret')}
                <input type="password" bind:value={glamaEnv[envKey]}
                  placeholder={glamaSpec.env_schema?.find(e => e.name === envKey)?.description || envKey} />
              {:else}
                <input type="text" bind:value={glamaEnv[envKey]}
                  placeholder={glamaSpec.env_schema?.find(e => e.name === envKey)?.description || envKey} />
              {/if}
            </div>
          {/each}
        {/if}
      {/if}

      <div class="modal-row">
        <button class="btn-secondary" on:click={closeGlamaModal} disabled={glamaFetching || glamaSaving}>Cancel</button>
        {#if !glamaSpec}
          <button class="btn-glama" on:click={fetchFromGlama} disabled={glamaFetching || !glamaURL.trim()}>
            {glamaFetching ? 'Fetching…' : 'Fetch config'}
          </button>
        {:else}
          <button class="btn-glama" on:click={saveGlama}
            disabled={glamaSaving || glamaEnvRequired.some(k => !glamaEnv[k]?.trim())}>
            {glamaSaving ? 'Installing…' : '⚡ Install'}
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }

  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .banner.warn { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(96,240,160,.08); border: 1px solid rgba(96,240,160,.3); color: #60f0a0; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .banner code { background: rgba(0,0,0,.25); padding: .05rem .3rem; border-radius: 4px; }

  .empty  { color: #6b7294; padding: 3rem; text-align: center; }
  .empty-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 3rem 2rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .75rem; color: #6b7294;
  }
  .empty-icon { font-size: 2.5rem; }
  .hint { font-size: .82rem; max-width: 540px; }
  .review-head { display:flex; align-items:flex-start; justify-content:space-between; gap:1rem; padding:.8rem; background:#111426; border:1px solid #252b4b; border-radius:8px; }
  .review-head p, .finding p { margin:.2rem 0 0; color:#8d94b8; font-size:.78rem; }
  .review-ready { color:#60f0a0; border:1px solid rgba(96,240,160,.3); background:rgba(96,240,160,.08); padding:.3rem .55rem; border-radius:999px; white-space:nowrap; font-size:.72rem; }
  .review-grid { display:grid; grid-template-columns:1fr 1fr; gap:.6rem; margin:.75rem 0; }
  .review-grid > div { min-width:0; display:flex; flex-direction:column; gap:.25rem; padding:.55rem; background:#101322; border-radius:6px; }
  .review-grid span { color:#777f9f; font-size:.68rem; text-transform:uppercase; letter-spacing:.05em; }
  .review-grid code { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:.7rem; }
  .review-title { font-size:.82rem; margin:.8rem 0 .45rem; }
  .finding-list { display:flex; flex-direction:column; gap:.4rem; max-height:210px; overflow:auto; }
  .finding { display:flex; gap:.6rem; padding:.5rem .6rem; border:1px solid rgba(96,240,160,.18); border-radius:6px; color:#60f0a0; }
  .finding.warning { color:#f0c460; border-color:rgba(240,196,96,.25); }
  .finding strong { font-size:.78rem; }
  .isolation-row { display:flex; flex-wrap:wrap; gap:.35rem; margin:.65rem 0; }
  .isolation-row span { font-size:.67rem; color:#aeb5d3; background:#20253d; border-radius:999px; padding:.25rem .45rem; }
  .env-review { color:#aeb5d3; font-size:.76rem; margin:.6rem 0; }
  .env-review div { padding:.2rem 0 .2rem .7rem; }
  .approval-note { padding:.65rem; border-radius:7px; background:rgba(139,133,255,.08); border:1px solid rgba(139,133,255,.25); color:#bdb9ff; font-size:.76rem; }

  .server-list { display: flex; flex-direction: column; gap: .65rem; }
  .srv { background: #141626; border: 1px solid #1a1e36; border-radius: 10px; overflow: hidden; }
  .srv.ok { border-color: rgba(76,175,130,.3); }

  .srv-head { display: flex; align-items: center; }
  .srv-expand {
    flex: 1; background: none; color: #e8eaf6; text-align: left;
    display: grid; grid-template-columns: 28px 1fr 110px 200px 24px; align-items: center; gap: .8rem;
    padding: .85rem 1rem; border-radius: 0; cursor: pointer; border: none;
  }
  .srv-expand:hover { background: rgba(255,255,255,.02); }
  .srv-actions { display: flex; gap: .35rem; padding-right: 1rem; }
  .tiny { padding: .25rem .55rem !important; font-size: .72rem !important; }

  .srv-icon       { font-size: 1.1rem; }
  .srv-identity   { display: flex; flex-direction: column; min-width: 0; }
  .srv-name       { font-weight: 600; font-size: .9rem; font-family: monospace; color: #8b85ff; overflow: hidden; text-overflow: ellipsis; }
  .srv-tx         { font-size: .68rem; color: #555a7a; text-transform: uppercase; letter-spacing: .04em; }
  .srv-tools-count{ font-size: .75rem; color: #7b82a8; text-align: right; }
  .srv-badge      { font-size: .72rem; font-weight: 600; color: #4caf82; text-align: right; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .srv-badge.bad  { color: #f06060; font-weight: 500; }
  .srv-chevron    { color: #555a7a; font-size: .9rem; text-align: center; }

  .srv-body { padding: .25rem 1rem 1rem; border-top: 1px solid #1a1e36; }
  .srv-error  { font-size: .8rem; color: #f06060; padding: .5rem .7rem; background: rgba(240,96,96,.08); border-radius: 6px; word-break: break-word; }
  .srv-empty  { font-size: .8rem; color: #6b7294; padding: .8rem 0; font-style: italic; }

  .tbl { width: 100%; border-collapse: collapse; font-size: .8rem; margin-top: .5rem; }

  @media (max-width: 768px) {
    /* Wide tables scroll horizontally instead of overflowing the viewport */
    .tbl { display: block; overflow-x: auto; }
  }
  .tbl th { padding: .5rem .8rem; text-align: left; color: #555a7a; font-weight: 500; font-size: .7rem; text-transform: uppercase; letter-spacing: .04em; border-bottom: 1px solid #1a1e36; }
  .tbl td { padding: .55rem .8rem; border-bottom: 1px solid #0e1020; vertical-align: top; }
  .td-name { font-family: monospace; color: #c8cadf; font-weight: 500; white-space: nowrap; }
  .td-mono { font-family: monospace; font-size: .75rem; color: #8b85ff; white-space: nowrap; }
  .td-desc { color: #7b82a8; }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .6rem;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card a  { color: #8b85ff; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }

  /* Modal */
  .modal-bg {
    position: fixed; inset: 0; background: rgba(5,7,18,.6);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 560px; max-width: 92vw; max-height: 90vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: .75rem;
  }
  .modal.wide { width: 680px; }
  .modal h2 { font-size: 1.05rem; font-weight: 600; margin-bottom: .25rem; }
  .modal-row {
    display: flex; justify-content: flex-end; gap: .5rem; margin-top: .5rem;
    position: sticky; bottom: 0; z-index: 5;
    background: #141626; padding-top: .6rem;
    box-shadow: 0 -10px 12px -10px rgba(0, 0, 0, 0.6);
  }

  .row-2 { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }
  .field { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .72rem; color: #6b7294; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; }
  .field input, .field select {
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 6px;
    color: #e8eaf6; font-size: .85rem; padding: .45rem .65rem; font-family: monospace;
  }
  .req      { color: #f06060; margin-left: .15rem; }
  .optional { color: #555a7a; text-transform: none; font-weight: 400; font-size: .68rem; letter-spacing: 0; margin-left: .25rem; }

  .templates { display: flex; flex-wrap: wrap; align-items: center; gap: .35rem; padding-bottom: .25rem; }
  .templates-label { font-size: .72rem; color: #6b7294; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; margin-right: .25rem; }
  .template-chip {
    background: rgba(108,99,255,.12); color: #8b85ff;
    border: 1px solid rgba(108,99,255,.35); padding: .25rem .6rem;
    border-radius: 999px; font-size: .72rem; font-weight: 600; cursor: pointer;
  }
  .template-chip:hover { background: rgba(108,99,255,.2); }

  .test-result {
    padding: .55rem .75rem; border-radius: 6px; font-size: .8rem;
    background: rgba(240,96,96,.08); border: 1px solid rgba(240,96,96,.3); color: #f06060;
  }
  .test-result.ok { background: rgba(96,240,160,.08); border-color: rgba(96,240,160,.3); color: #60f0a0; }
  .test-result code { background: rgba(0,0,0,.25); padding: .05rem .3rem; border-radius: 4px; }

  .btn-primary, .btn-secondary, .btn-danger, .btn-glama {
    padding: .5rem .85rem; border-radius: 6px; font-size: .82rem; cursor: pointer; border: 1px solid transparent;
  }
  .btn-primary   { background: #6c63ff; color: white; border-color: #6c63ff; }
  .btn-primary:disabled { opacity: .5; cursor: not-allowed; }
  .btn-secondary { background: #1a1e36; color: #c8cadf; border-color: #2a2f4a; }
  .btn-danger    { background: transparent; color: #f06060; border-color: rgba(240,96,96,.4); }
  .btn-glama     { background: rgba(255,180,0,.12); color: #ffc533; border-color: rgba(255,180,0,.35); font-weight: 600; }
  .btn-glama:hover { background: rgba(255,180,0,.2); }
  .btn-glama:disabled { opacity: .5; cursor: not-allowed; }

  .glama-hint { font-size: .82rem; color: #7b82a8; line-height: 1.6; margin: -.25rem 0 .25rem; }
  .glama-hint a { color: #ffc533; }

  .glama-spec-card {
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 8px;
    padding: .85rem 1rem; display: flex; flex-direction: column; gap: .35rem;
  }
  .glama-spec-name { font-weight: 600; color: #e8eaf6; font-size: .9rem; }
  .glama-spec-desc { font-size: .8rem; color: #7b82a8; line-height: 1.5; }
  .glama-spec-cmd  { font-size: .78rem; }
  .glama-spec-cmd code { background: #1c1f35; padding: .15rem .4rem; border-radius: 4px; color: #ffc533; font-family: monospace; }

  .glama-creds-label {
    font-size: .72rem; color: #6b7294; text-transform: uppercase; letter-spacing: .06em;
    font-weight: 600; border-top: 1px solid #1a1e36; padding-top: .75rem;
  }
</style>
