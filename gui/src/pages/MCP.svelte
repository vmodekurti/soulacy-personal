<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import DeploymentPanel from '../lib/DeploymentPanel.svelte'
  import { deploymentFrom, blockersFor } from '../lib/deployment.js'
  import KeyValueEditor from '../lib/KeyValueEditor.svelte'

  let servers = []
  let loading = true
  let error   = ''
  let info    = ''
  let expanded = {}      // serverID → bool
  let restartNeeded = false
  let restarting = false

  // Soulacy can also be the MCP server. The endpoint is hosted by the gateway,
  // so managed deployments need no sidecar process or shell session.
  let gatewayOrigin = ''
  let remoteEndpoint = '/mcp'
  let copiedRemote = ''

  $: remoteClientConfig = JSON.stringify({
    url: remoteEndpoint,
    headers: { Authorization: 'Bearer <SOULACY_API_KEY>' },
  }, null, 2)
  $: remoteAddCommand = `sy --gateway ${gatewayOrigin || '<SOULACY_URL>'} --api-key <SOULACY_API_KEY> mcp add --name <server-name> --transport http --url https://mcp.example.com/mcp --header 'Authorization=Bearer <MCP_TOKEN>'`

  // Edit / create modal state. `editing` is null when the modal is closed.
  // When `editing.id` is set AND matches a row in `servers`, we're editing;
  // otherwise we're creating.
  let editing = null
  let saving = false
  let testing = false
  let testResult = null  // { ok, message }

  // Glama provisioner state
  let glamaModal = false
  let glamaURL = ''
  let glamaFetching = false
  let glamaSpec = null      // GlamaProvisionSpec from server
  let glamaEnvRequired = [] // env var names still needed
  let glamaEnv = {}         // user-filled env values
  let glamaError = ''
  let glamaSaving = false

  const BLANK_AUTH = () => ({ type: 'none', header: '', scheme: '', secret_ref: '', token_url: '', client_id: '', client_secret_ref: '', scopes: [], audience: '' })
  const BLANK_STDIO = () => ({
    id: '', transport: 'stdio', command: '', args: [], env: {}, env_secret_refs: {}, url: '', headers: {},
    query: {}, auth: BLANK_AUTH(), auth_secret: '', timeout: '60s',
  })

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.mcp.list()
      servers = res.servers || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }
  // What this deployment can do. Fetched alongside the list rather than on
  // demand: the point is to be read before someone fills in a form that
  // cannot work here, not after it fails. A gateway that does not report it
  // simply leaves the panel out.
  let deployment = null
  async function loadDeployment() {
    try {
      deployment = deploymentFrom(await api.providers.doctor())
    } catch {
      deployment = null
    }
  }

  onMount(() => {
    gatewayOrigin = window.location.origin
    remoteEndpoint = `${gatewayOrigin}/mcp`
    load()
    loadDeployment()
  })

  async function copyRemote(value, label) {
    try {
      await navigator.clipboard.writeText(value)
      copiedRemote = label
      setTimeout(() => { if (copiedRemote === label) copiedRemote = '' }, 1800)
    } catch {
      copiedRemote = ''
    }
  }

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
      env_secret_refs: s.env_secret_refs ? { ...s.env_secret_refs } : {},
      url: s.url || '',
      headers: s.headers ? { ...s.headers } : {},
      query: s.query ? { ...s.query } : {},
      auth: { ...BLANK_AUTH(), ...(s.auth || {}) },
      auth_secret: '',
      timeout: s.timeout || '60s',
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

  function scopesToString(scopes) { return (scopes || []).join(' ') }
  function stringToScopes(str) { return str.split(/[\s,]+/).map(v => v.trim()).filter(Boolean) }

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
      const res = isExisting
        ? await api.mcp.update(editing.id, editing)
        : await api.mcp.create(editing)
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
    if (!confirm(`Remove MCP server "${s.id}"? It will stop accepting tool calls after the next gateway restart.`)) return
    error = ''; info = ''
    try {
      const res = await api.mcp.delete(s.id)
      info = res.message || 'Removed.'
      if (res.restart_needed) restartNeeded = true
      await new Promise(r => setTimeout(r, 500))
      await load()
    } catch (e) {
      error = e.message
    }
  }

  async function restartGateway() {
    restarting = true
    error = ''; info = ''
    try {
      await api.admin.restart()
      info = 'Restart requested. Reconnect this page in a few seconds if it does not refresh automatically.'
      restartNeeded = false
    } catch (e) {
      error = e.message
    } finally {
      setTimeout(() => { restarting = false }, 5000)
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
      // Every flag here was a failure first, on a real container:
      //   --browser chromium  without it the server looks for branded Chrome
      //                       at /opt/google/chrome/chrome and exits
      //   --no-sandbox        Chromium cannot sandbox itself in a container
      //                       and dies with "No usable sandbox!"
      //   --isolated          keeps the profile in memory rather than on disk
      // and keeps_processes stops Soulacy's per-call process janitor killing
      // the browser between tool calls, which returns about:blank on the next
      // one with nothing to say why.
      args: ['-y', '@playwright/mcp@latest', '--browser', 'chromium', '--headless', '--isolated', '--no-sandbox'],
      keeps_processes: true,
      requires: ['node_runtime', 'browser_automation'],
      note: 'Runs Chromium without opening windows. Use this for scheduled agents and normal background work.',
    },
    {
      id: 'browser_remote',
      label: 'Browser remote (CDP)',
      command: 'npx',
      args: ['-y', '@playwright/mcp@latest', '--headless', '--cdp-endpoint', 'wss://YOUR-BROWSER-ENDPOINT'],
      keeps_processes: true,
      requires: ['node_runtime'],
      note: 'Drives a browser running somewhere else. Nothing is installed here — no Chromium, no system libraries, no download — so this is the one that works on platforms where you have no shell. Replace the endpoint with your browser service URL.',
    },
    {
      id: 'browser_visible',
      label: 'Browser visible',
      command: 'npx',
      args: ['-y', '@playwright/mcp@latest', '--browser', 'chromium'],
      keeps_processes: true,
      requires: ['node_runtime', 'browser_automation'],
      note: 'Opens a visible Chromium window. Needs a desktop session, so it will not work on a server. Use only when you need to watch or debug browser automation.',
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
      // A server whose child process is its state — a browser — must be
      // exempt from the per-call process janitor. `requires` stays out: it is
      // how this screen decides what to show, not something the gateway
      // stores.
      keeps_processes: !!tpl.keeps_processes,
    }
    testResult = null
  }
</script>

<div class="page">
  <div class="page-header">
    <h1>MCP Servers</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
      <button class="btn-glama"     on:click={openGlamaModal}>⚡ Glama</button>
      <button class="btn-primary"   on:click={openNew}>+ New Server</button>
    </div>
        <TourButton />
    </div>

  <DeploymentPanel report={deployment} />

  <section class="remote-card" aria-labelledby="remote-mcp-title">
    <div class="remote-heading">
      <div>
        <span class="eyebrow">Soulacy as an MCP server</span>
        <h2 id="remote-mcp-title">Connect without shell access</h2>
        <p>Use this Streamable HTTP endpoint from an MCP client. The gateway runs it directly, including on Railway and other managed platforms.</p>
      </div>
      <span class="remote-badge">● Available</span>
    </div>

    <div class="remote-field">
      <span>Endpoint</span>
      <div class="copy-row">
        <code>{remoteEndpoint}</code>
        <button class="btn-secondary tiny" on:click={() => copyRemote(remoteEndpoint, 'endpoint')}>
          {copiedRemote === 'endpoint' ? 'Copied' : 'Copy URL'}
        </button>
      </div>
    </div>

    <div class="remote-grid">
      <div>
        <h3>Connect an MCP client</h3>
        <p>Choose Streamable HTTP and send your Soulacy key as a Bearer token.</p>
        <pre>{remoteClientConfig}</pre>
        <button class="btn-secondary tiny" on:click={() => copyRemote(remoteClientConfig, 'config')}>
          {copiedRemote === 'config' ? 'Copied' : 'Copy config'}
        </button>
      </div>
      <div>
        <h3>Add a server to this deployment</h3>
        <p>Run this from any computer with <code>sy</code>. The server is registered on the remote gateway; no host shell is used.</p>
        <pre>{remoteAddCommand}</pre>
        <button class="btn-secondary tiny" on:click={() => copyRemote(remoteAddCommand, 'command')}>
          {copiedRemote === 'command' ? 'Copied' : 'Copy CLI command'}
        </button>
      </div>
    </div>
    <p class="remote-footnote">Clients that only support local stdio can still use <code>sy --gateway {gatewayOrigin || '<SOULACY_URL>'} mcp serve</code> as a compatibility bridge.</p>
  </section>

  {#if restartNeeded}
    <div class="banner warn">
      <span>
        <strong>Restart needed.</strong> MCP config was modified and persisted in <code>config.yaml</code>.
      </span>
      <button class="btn-secondary" on:click={restartGateway} disabled={restarting}>
        {restarting ? 'Restarting…' : 'Restart Gateway'}
      </button>
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
      <p class="hint">Click <strong>+ New Server</strong> to add one — choose from a template or define your own.</p>
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
              <button class="btn-secondary tiny" on:click={() => openEdit(s)}>Edit</button>
              <button class="btn-danger tiny"    on:click={() => remove(s)}>Delete</button>
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
    <p>Changes here are written to <code>config.yaml</code>; the gateway must be restarted to pick them up.</p>
  </div>
</div>

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
            {@const blocked = blockersFor(tpl, deployment)}
            <button
              class="template-chip"
              class:blocked={blocked.length > 0}
              title={blocked.length > 0
                ? blocked.map(b => b.name + ': ' + b.detail + (b.workaround ? ' — instead: ' + b.workaround : '')).join('\n')
                : (tpl.note || '')}
              on:click={() => applyTemplate(tpl)}
            >{tpl.label}{#if blocked.length > 0}<span class="warn-dot" aria-hidden="true">!</span>{/if}</button>
          {/each}
          {#if deployment}
            {@const anyBlocked = TEMPLATES.some(tpl => blockersFor(tpl, deployment).length > 0)}
            {#if anyBlocked}
              <p class="template-note">
                Templates marked <strong>!</strong> need something this deployment does not have — hover to see what, and what to use instead.
                They can still be saved; they will not run.
              </p>
            {/if}
          {/if}
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

        <div class="connection-section">
          <div class="section-heading">
            <div>
              <strong>Authentication</strong>
              <span>Credentials are stored in Soulacy's encrypted secret vault.</span>
            </div>
          </div>
          <div class="field">
            <span class="field-label">Method</span>
            <select bind:value={editing.auth.type}>
              <option value="none">No authentication</option>
              <option value="bearer">Bearer token</option>
              <option value="api_key">API key</option>
              <option value="oauth_client_credentials">OAuth 2.0 client credentials</option>
            </select>
          </div>

          {#if editing.auth.type === 'bearer' || editing.auth.type === 'api_key'}
            <div class="row-2">
              <div class="field">
                <span class="field-label">Header</span>
                <input type="text" bind:value={editing.auth.header}
                  placeholder={editing.auth.type === 'bearer' ? 'Authorization' : 'X-API-Key'} />
              </div>
              <div class="field">
                <span class="field-label">Scheme <span class="optional">(optional)</span></span>
                <input type="text" bind:value={editing.auth.scheme}
                  placeholder={editing.auth.type === 'bearer' ? 'Bearer' : 'Leave blank'} />
              </div>
            </div>
            <div class="field">
              <span class="field-label">Credential</span>
              <input type="password" bind:value={editing.auth_secret}
                placeholder={editing.auth.secret_ref ? 'Saved — enter only to replace' : 'Paste token or API key'} />
            </div>
            <div class="field">
              <span class="field-label">Saved secret name <span class="optional">(advanced)</span></span>
              <input type="text" bind:value={editing.auth.secret_ref} placeholder={`mcp.${editing.id || 'server'}.credential`} />
              <span class="field-help">Use an existing secret by name, or leave blank to create one automatically from the credential above.</span>
            </div>
          {:else if editing.auth.type === 'oauth_client_credentials'}
            <div class="field">
              <span class="field-label">Token URL <span class="req">*</span></span>
              <input type="url" bind:value={editing.auth.token_url} placeholder="https://auth.example.com/oauth/token" />
            </div>
            <div class="row-2">
              <div class="field">
                <span class="field-label">Client ID <span class="req">*</span></span>
                <input type="text" bind:value={editing.auth.client_id} placeholder="client-id" />
              </div>
              <div class="field">
                <span class="field-label">Client secret <span class="req">*</span></span>
                <input type="password" bind:value={editing.auth_secret}
                  placeholder={editing.auth.client_secret_ref ? 'Saved — enter only to replace' : 'Paste client secret'} />
              </div>
            </div>
            <div class="row-2">
              <div class="field">
                <span class="field-label">Scopes <span class="optional">(space-separated)</span></span>
                <input type="text" value={scopesToString(editing.auth.scopes)}
                  on:input={(e) => editing.auth.scopes = stringToScopes(e.target.value)}
                  placeholder="mcp.read mcp.execute" />
              </div>
              <div class="field">
                <span class="field-label">Audience <span class="optional">(optional)</span></span>
                <input type="text" bind:value={editing.auth.audience} placeholder="https://mcp.example.com" />
              </div>
            </div>
            <div class="field">
              <span class="field-label">Saved client-secret name <span class="optional">(advanced)</span></span>
              <input type="text" bind:value={editing.auth.client_secret_ref} placeholder={`mcp.${editing.id || 'server'}.client_secret`} />
              <span class="field-help">Use an existing secret by name, or leave blank to create one automatically.</span>
            </div>
          {/if}
        </div>

        <div class="field">
          <span class="field-label">Request headers <span class="optional">(metadata sent on every request)</span></span>
          <KeyValueEditor
            value={editing.headers || {}}
            keyLabel="Header" valueLabel="Value"
            keyPlaceholder="X-Workspace-ID" valuePlaceholder="workspace-123"
            maskValues={false}
            on:change={(e) => editing.headers = e.detail}
          />
          <span class="field-help">Use Authentication above for tokens and API keys.</span>
        </div>

        <div class="field">
          <span class="field-label">Query parameters <span class="optional">(non-sensitive values only)</span></span>
          <KeyValueEditor
            value={editing.query || {}}
            keyLabel="Parameter" valueLabel="Value"
            keyPlaceholder="version" valuePlaceholder="2026-01-01"
            maskValues={false}
            on:change={(e) => editing.query = e.detail}
          />
          <span class="field-help">Credentials are blocked here because URLs can appear in logs and browser history.</span>
        </div>

        <div class="field compact-field">
          <span class="field-label">Request timeout</span>
          <input type="text" bind:value={editing.timeout} placeholder="60s" />
          <span class="field-help">Between 1s and 10m.</span>
        </div>
      {/if}

      {#if testResult}
        <div class="test-result" class:ok={testResult.ok}>
          {#if testResult.ok}
            ✓ {testResult.message || 'Reachable'}{#if testResult.resolved_command} · resolved to <code>{testResult.resolved_command}</code>{/if}
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

  .remote-card {
    background: linear-gradient(135deg, rgba(82,72,180,.14), rgba(15,18,39,.92));
    border: 1px solid rgba(139,133,255,.35); border-radius: 12px;
    padding: 1.15rem 1.25rem; display: flex; flex-direction: column; gap: 1rem;
  }
  .remote-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .remote-heading h2 { margin: .15rem 0 .35rem; font-size: 1rem; color: #eef0ff; }
  .remote-heading p, .remote-grid p, .remote-footnote { margin: 0; color: #8f96b8; font-size: .78rem; line-height: 1.55; }
  .eyebrow { color: #8b85ff; font-size: .68rem; font-weight: 700; text-transform: uppercase; letter-spacing: .08em; }
  .remote-badge { color: #60f0a0; font-size: .72rem; font-weight: 600; white-space: nowrap; }
  .remote-field { display: flex; flex-direction: column; gap: .35rem; }
  .remote-field > span { color: #686f94; font-size: .67rem; font-weight: 600; text-transform: uppercase; letter-spacing: .06em; }
  .copy-row { display: flex; align-items: center; gap: .5rem; }
  .copy-row code { flex: 1; min-width: 0; overflow-x: auto; background: #0b0e1d; border: 1px solid #252a45; border-radius: 7px; padding: .55rem .7rem; color: #c8c5ff; font-size: .78rem; }
  .remote-grid { display: grid; grid-template-columns: 1fr 1fr; gap: .8rem; }
  .remote-grid > div { min-width: 0; background: rgba(8,10,24,.5); border: 1px solid #252a45; border-radius: 9px; padding: .85rem; display: flex; flex-direction: column; align-items: flex-start; gap: .55rem; }
  .remote-grid h3 { margin: 0; color: #dfe2f5; font-size: .82rem; }
  .remote-grid pre { box-sizing: border-box; width: 100%; margin: 0; padding: .65rem; border-radius: 7px; background: #090b18; color: #b9bde0; font: .7rem/1.5 monospace; white-space: pre-wrap; overflow-wrap: anywhere; }
  .remote-grid p code, .remote-footnote code { color: #aaa5ff; }

  @media (max-width: 820px) {
    .remote-grid { grid-template-columns: 1fr; }
    .remote-heading { flex-direction: column; }
    .copy-row { align-items: stretch; flex-direction: column; }
  }

  .template-chip.blocked { opacity: .65; border-style: dashed; }
  .warn-dot { margin-left: .3rem; color: orange; font-weight: 700; }
  .template-note { margin: .4rem 0 0; font-size: .75rem; color: var(--sl-text-faint); flex-basis: 100%; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .banner.warn { display: flex; align-items: center; justify-content: space-between; gap: .75rem; flex-wrap: wrap; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok     { background: rgba(96,240,160,.08); border: 1px solid rgba(96,240,160,.3); color: #60f0a0; }
  .warn   { background: rgba(240,196,96,.08); border: 1px solid rgba(240,196,96,.3); color: #f0c460; }
  .banner code { background: rgba(0,0,0,.25); padding: .05rem .3rem; border-radius: 4px; }

  .empty  { color: var(--sl-text-faint); padding: 3rem; text-align: center; }
  .empty-card {
    background: var(--sl-surface); border: 1px solid var(--sl-line); border-radius: 10px;
    padding: 3rem 2rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .75rem; color: var(--sl-text-faint);
  }
  .empty-icon { font-size: 2.5rem; }
  .hint { font-size: .82rem; max-width: 540px; }

  .server-list { display: flex; flex-direction: column; gap: .65rem; }
  .srv { background: var(--sl-surface); border: 1px solid var(--sl-line); border-radius: 10px; overflow: hidden; }
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

  .srv-body { padding: .25rem 1rem 1rem; border-top: 1px solid var(--sl-line); }
  .srv-error  { font-size: .8rem; color: #f06060; padding: .5rem .7rem; background: rgba(240,96,96,.08); border-radius: 6px; word-break: break-word; }
  .srv-empty  { font-size: .8rem; color: var(--sl-text-faint); padding: .8rem 0; font-style: italic; }

  .tbl { width: 100%; border-collapse: collapse; font-size: .8rem; margin-top: .5rem; }

  @media (max-width: 768px) {
    /* Wide tables scroll horizontally instead of overflowing the viewport */
    .tbl { display: block; overflow-x: auto; }
  }
  .tbl th { padding: .5rem .8rem; text-align: left; color: #555a7a; font-weight: 500; font-size: .7rem; text-transform: uppercase; letter-spacing: .04em; border-bottom: 1px solid var(--sl-line); }
  .tbl td { padding: .55rem .8rem; border-bottom: 1px solid #0e1020; vertical-align: top; }
  .td-name { font-family: monospace; color: #c8cadf; font-weight: 500; white-space: nowrap; }
  .td-mono { font-family: monospace; font-size: .75rem; color: #8b85ff; white-space: nowrap; }
  .td-desc { color: #7b82a8; }

  .info-card {
    background: var(--sl-surface); border: 1px solid var(--sl-line); border-radius: 10px;
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
    background: var(--sl-surface); border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 560px; max-width: 92vw; max-height: 90vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: .75rem;
  }
  .modal.wide { width: 680px; }
  .modal h2 { font-size: 1.05rem; font-weight: 600; margin-bottom: .25rem; }
  .modal-row {
    display: flex; justify-content: flex-end; gap: .5rem; margin-top: .5rem;
    position: sticky; bottom: 0; z-index: 5;
    background: var(--sl-surface); padding-top: .6rem;
    box-shadow: 0 -10px 12px -10px rgba(0, 0, 0, 0.6);
  }

  .row-2 { display: grid; grid-template-columns: 1fr 1fr; gap: .75rem; }
  .field { display: flex; flex-direction: column; gap: .3rem; }
  .field-label { font-size: .72rem; color: var(--sl-text-faint); text-transform: uppercase; letter-spacing: .06em; font-weight: 600; }
  .field input, .field select {
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 6px;
    color: #e8eaf6; font-size: .85rem; padding: .45rem .65rem; font-family: monospace;
  }
  .field-help { color: #666d91; font-size: .7rem; line-height: 1.45; }
  .compact-field { max-width: 180px; }
  .connection-section {
    border: 1px solid #252a45; border-radius: 9px; background: rgba(10,12,28,.45);
    padding: .85rem; display: flex; flex-direction: column; gap: .7rem;
  }
  .section-heading div { display: flex; flex-direction: column; gap: .18rem; }
  .section-heading strong { font-size: .84rem; color: #dfe2f5; }
  .section-heading span { font-size: .72rem; color: var(--sl-text-faint); }
  .req      { color: #f06060; margin-left: .15rem; }
  .optional { color: #555a7a; text-transform: none; font-weight: 400; font-size: .68rem; letter-spacing: 0; margin-left: .25rem; }

  .templates { display: flex; flex-wrap: wrap; align-items: center; gap: .35rem; padding-bottom: .25rem; }
  .templates-label { font-size: .72rem; color: var(--sl-text-faint); text-transform: uppercase; letter-spacing: .06em; font-weight: 600; margin-right: .25rem; }
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
  .btn-primary   { background: var(--sl-accent); color: white; border-color: var(--sl-accent); }
  .btn-primary:disabled { opacity: .5; cursor: not-allowed; }
  .btn-secondary { background: var(--sl-line); color: #c8cadf; border-color: #2a2f4a; }
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
    font-size: .72rem; color: var(--sl-text-faint); text-transform: uppercase; letter-spacing: .06em;
    font-weight: 600; border-top: 1px solid var(--sl-line); padding-top: .75rem;
  }

  @media (max-width: 640px) {
    .modal-bg { align-items: flex-end; }
    .modal, .modal.wide {
      width: 100%; max-width: 100vw; max-height: 92dvh; border-radius: 16px 16px 0 0;
      padding: 1rem; padding-bottom: max(1rem, env(safe-area-inset-bottom));
    }
    .row-2 { grid-template-columns: 1fr; }
    .compact-field { max-width: none; }
    .modal-row { display: grid; grid-template-columns: 1fr 1fr; }
    .modal-row .btn-primary, .modal-row .btn-glama { grid-column: 1 / -1; grid-row: 1; }
    .modal-row button { min-height: 44px; }
  }
</style>
