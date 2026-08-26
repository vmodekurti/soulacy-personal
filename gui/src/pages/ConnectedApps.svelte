<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { activeWorkspace, can, permissions } from '../lib/workspace.js'
  import TourButton from '../lib/TourButton.svelte'
  import { confirmDestructive } from '../lib/destructive.js'

  let servers = []
  let loading = true
  let error = ''
  let info = ''
  let showConnect = false
  let saving = false
  let url = ''
  let headerName = 'Authorization'
  let headerValue = ''
  let acknowledgeScope = false
  let showNango = false
  let nangoURL = 'https://api.nango.dev/mcp'
  let nangoSecret = ''
  let nangoConnection = ''
  let nangoIntegration = ''
  let acknowledgeActions = false

  $: composio = servers.find(server => server.id === 'composio')
  $: nango = servers.find(server => server.id === 'nango')
  $: multiUser = ['team', 'scale'].includes(String($activeWorkspace?.deploymentMode || '').toLowerCase())
  $: workspaceAdmin = ['owner', 'admin'].includes(String($activeWorkspace?.role || '').toLowerCase())
  $: canWrite = ($permissions, can('mcp', 'write')) && (!multiUser || workspaceAdmin)
  $: canDelete = ($permissions, can('mcp', 'delete')) && (!multiUser || workspaceAdmin)

  async function load() {
    loading = true
    error = ''
    try {
      const response = await api.mcp.ownList()
      servers = response.servers || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  function openConnect() {
    error = ''
    info = ''
    url = composio?.url || ''
    headerName = 'Authorization'
    headerValue = ''
    acknowledgeScope = false
    showConnect = true
  }

  async function connect() {
    if (!url.trim()) { error = 'Paste the Composio session MCP URL.'; return }
    if (!composio && !headerValue.trim()) { error = 'Paste the session header value.'; return }
    if (!acknowledgeScope) { error = 'Confirm that the session is limited to the apps and actions this workspace needs.'; return }
    saving = true
    error = ''
    try {
      const headers = { [headerName]: headerValue.trim() }
      await api.mcp.ownPut('composio', { transport: 'http', url: url.trim(), headers })
      info = 'Composio connected. Its safe app actions are now discoverable in Studio.'
      showConnect = false
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function disconnect() {
    if (!confirmDestructive('Disconnect Composio? Existing agent grants will stop working.')) return
    error = ''
    try {
      await api.mcp.ownDelete('composio')
      info = 'Composio disconnected from this workspace.'
      await load()
    } catch (e) {
      error = e.message
    }
  }

  function openNango() {
    error = ''; info = ''
    nangoURL = nango?.url || 'https://api.nango.dev/mcp'
    nangoSecret = ''; nangoConnection = ''; nangoIntegration = ''; acknowledgeActions = false
    showNango = true
  }

  async function connectNango() {
    if (!nango && (!nangoSecret.trim() || !nangoConnection.trim() || !nangoIntegration.trim())) {
      error = 'Environment key, connection ID, and integration ID are required.'; return
    }
    if (!acknowledgeActions) { error = 'Confirm that only reviewed Nango action functions are enabled.'; return }
    saving = true; error = ''
    try {
      await api.mcp.ownPut('nango', {
        transport: 'http', url: nangoURL.trim(), headers: {
          Authorization: nangoSecret.trim() ? `Bearer ${nangoSecret.trim().replace(/^Bearer\s+/i, '')}` : '',
          'connection-id': nangoConnection.trim(),
          'provider-config-key': nangoIntegration.trim(),
        },
      })
      info = 'Nango connected. Enabled action functions are now discoverable in Studio.'
      showNango = false
      await load()
    } catch (e) { error = e.message } finally { saving = false }
  }

  async function disconnectNango() {
    if (!confirmDestructive('Disconnect Nango? Existing agent grants will stop working.')) return
    try { await api.mcp.ownDelete('nango'); info = 'Nango disconnected.'; await load() }
    catch (e) { error = e.message }
  }

  onMount(load)
</script>

<svelte:head><title>Connected Apps — Soulacy</title></svelte:head>

<main class="page">
  <header>
    <div>
      <p class="eyebrow">INTEGRATIONS</p>
      <h1>Connected Apps</h1>
      <p class="lede">Give agents narrowly scoped access to SaaS actions without placing app credentials in prompts or agent files.</p>
    </div>
    <TourButton />
  </header>

  {#if error}<div class="notice error">{error}</div>{/if}
  {#if info}<div class="notice success">{info}</div>{/if}

  <section class="grid">
    <article class="card">
      <div class="card-head">
        <div class="brand">C</div>
        <div><h2>Composio</h2><p>Authenticated actions across business apps</p></div>
        <span class:online={composio?.connected} class="status">{composio?.connected ? 'Connected' : composio ? 'Needs attention' : 'Not connected'}</span>
      </div>
      <p>Connect a constrained Composio MCP session. Soulacy discovers its tools, namespaces them, and makes them selectable per agent in Studio.</p>
      {#if composio}
        <div class="facts"><span>{composio.tools || 0} safe tools discovered</span><span>Workspace scoped</span><span>Secrets encrypted</span></div>
        {#if composio.detail && !composio.connected}<div class="inline-error">{composio.detail}</div>{/if}
      {/if}
      <div class="guardrail">Remote workbench, bash, and shell-execution tools are blocked even if a session exposes them.</div>
      <details class="setup-guide">
        <summary>How to connect Composio</summary>
        <ol>
          <li>In Composio, create a session for the user or organization that will own the app connection.</li>
          <li>Enable MCP and select only the apps and actions this workspace needs.</li>
          <li>Copy <code>session.mcp.url</code> and the accompanying <code>Authorization</code> or <code>x-api-key</code> header value.</li>
          <li>Select <strong>Connect Composio</strong> below, paste those values, and let Soulacy discover the tools.</li>
          <li>Open Studio and grant the discovered <code>mcp__composio__…</code> tools only to the agents that need them.</li>
        </ol>
        <a href="https://docs.composio.dev/docs/sessions-via-mcp" target="_blank" rel="noopener noreferrer">Open Composio MCP guide ↗</a>
      </details>
      <div class="actions">
        {#if canWrite}<button class="primary" on:click={openConnect}>{composio ? 'Update connection' : 'Connect Composio'}</button>{/if}
        {#if composio && canDelete}<button class="danger" on:click={disconnect}>Disconnect</button>{/if}
        {#if !canWrite}<span class="muted">A workspace owner or admin manages this connection.</span>{/if}
      </div>
    </article>

    <article class="card native">
      <div class="card-head">
        <div class="brand">↳</div>
        <div><h2>Structured Reasoning</h2><p>Sequential Thinking, native to Soulacy</p></div>
        <span class="status online">Ready</span>
      </div>
      <p>Soulacy includes a safe native equivalent of the Sequential Thinking MCP server. It works in every deployment mode with no Node.js process or container to maintain.</p>
      <div class="facts"><span>Tool: <code>structured_reasoning</code></span><span>Branch and revise</span><span>Auditable checkpoints</span></div>
      <div class="guardrail">It records concise decisions and evidence—not hidden chain-of-thought—and can be granted or removed like any other Studio capability.</div>
      <details class="setup-guide">
        <summary>How to enable it for an agent</summary>
        <ol>
          <li>Open Studio and select or create an agent.</li>
          <li>In the agent's available capabilities, add <code>structured_reasoning</code>.</li>
          <li>Tell the agent when to use checkpoints—for example, before committing to a multi-step plan or when revising an earlier assumption.</li>
          <li>Review and save the agent. No external account, API key, MCP server, or container is required.</li>
        </ol>
      </details>
      <a class="button-link" href="#studio">Open Studio</a>
    </article>

    <article class="card">
      <div class="card-head">
        <div class="brand nango">N</div>
        <div><h2>Nango</h2><p>Per-user auth, custom actions, syncs, and webhooks</p></div>
        <span class:online={nango?.connected} class="status">{nango?.connected ? 'Connected' : nango ? 'Needs attention' : 'Not connected'}</span>
      </div>
      <p>Expose one Nango connection's enabled action functions through its hosted MCP server. OAuth tokens stay in Nango; Soulacy stores only the environment key and connection selectors in its encrypted vault.</p>
      {#if nango}<div class="facts"><span>{nango.tools || 0} actions discovered</span><span>Per-connection identity</span><span>Execution logs in Nango</span></div>{/if}
      {#if nango?.detail && !nango?.connected}<div class="inline-error">{nango.detail}</div>{/if}
      <div class="guardrail">Only enabled, reviewed Nango action functions are exposed. Use idempotent functions for writes that may be retried.</div>
      <details class="setup-guide">
        <summary>How to connect Nango</summary>
        <ol>
          <li>In Nango, create an integration and enable the reviewed <strong>Action Functions</strong> the agents may call.</li>
          <li>Open <strong>Connections</strong>, authorize the external account, and copy its <strong>Connection ID</strong>.</li>
          <li>Copy the integration's unique key as the <strong>Integration ID</strong> and the selected environment's <strong>secret API key</strong>. Do not use Nango's public key.</li>
          <li>Keep the hosted URL as <code>https://api.nango.dev/mcp</code>, select <strong>Connect Nango</strong>, and enter the three values.</li>
          <li>After discovery, open Studio and grant the resulting <code>mcp__nango__…</code> tools to selected agents.</li>
        </ol>
        <a href="https://nango.dev/docs/guides/functions/tool-calling" target="_blank" rel="noopener noreferrer">Open Nango MCP guide ↗</a>
      </details>
      <div class="actions">
        {#if canWrite}<button class="primary" on:click={openNango}>{nango ? 'Update connection' : 'Connect Nango'}</button>{/if}
        {#if nango && canDelete}<button class="danger" on:click={disconnectNango}>Disconnect</button>{/if}
        {#if !canWrite}<span class="muted">A workspace owner or admin manages this connection.</span>{/if}
      </div>
    </article>
  </section>

  <section class="how">
    <h2>How connected tools reach agents</h2>
    <ol><li>A workspace owner or admin connects the service using the instructions on its card.</li><li>Soulacy encrypts connection values in the workspace vault and discovers the service's tools.</li><li>An agent builder explicitly grants selected discovered tools to each agent in Studio.</li></ol>
  </section>
</main>

{#if showConnect}
  <div class="backdrop" role="presentation" on:click={() => !saving && (showConnect = false)}>
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_noninteractive_element_interactions -->
    <form class="modal" on:submit|preventDefault={connect} on:click|stopPropagation>
      <h2>Connect Composio</h2>
      <p>In Composio, create a session with MCP enabled and an exact tool preset. Paste <code>session.mcp.url</code> and its secret header below.</p>
      <label>Session MCP URL<input type="url" bind:value={url} placeholder="https://…composio.dev/…" required /></label>
      <div class="header-row">
        <label>Secret header<select bind:value={headerName}><option>Authorization</option><option>x-api-key</option><option>X-API-Key</option></select></label>
        <label>Value<input type="password" bind:value={headerValue} placeholder={composio ? 'Leave blank to keep existing value' : 'Required'} /></label>
      </div>
      <label class="check"><input type="checkbox" bind:checked={acknowledgeScope} /> I limited this session to the apps and actions this workspace needs.</label>
      <p class="fine">The secret is written directly to the encrypted workspace vault and is never returned to this browser.</p>
      <div class="modal-actions"><button type="button" on:click={() => showConnect = false} disabled={saving}>Cancel</button><button class="primary" disabled={saving}>{saving ? 'Connecting…' : 'Connect & discover'}</button></div>
    </form>
  </div>
{/if}

{#if showNango}
  <div class="backdrop" role="presentation" on:click={() => !saving && (showNango = false)}>
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_noninteractive_element_interactions -->
    <form class="modal" on:submit|preventDefault={connectNango} on:click|stopPropagation>
      <h2>Connect Nango</h2>
      <p>Enable reviewed action functions for an integration in Nango, authorize the user or organization, then paste the resulting connection details.</p>
      <label>Nango MCP URL<input type="url" bind:value={nangoURL} required /></label>
      <label>Environment secret key<input type="password" bind:value={nangoSecret} placeholder={nango ? 'Leave blank to keep existing value' : 'Required'} /></label>
      <div class="header-row">
        <label>Connection ID<input bind:value={nangoConnection} placeholder={nango ? 'Leave blank to keep existing value' : 'customer-connection-id'} /></label>
        <label>Integration ID<input bind:value={nangoIntegration} placeholder={nango ? 'Leave blank to keep existing value' : 'github-production'} /></label>
      </div>
      <label class="check"><input type="checkbox" bind:checked={acknowledgeActions} /> I enabled only reviewed action functions for this connection.</label>
      <p class="fine">All three header values are encrypted in the Soulacy workspace vault and never returned by the API.</p>
      <div class="modal-actions"><button type="button" on:click={() => showNango = false} disabled={saving}>Cancel</button><button class="primary" disabled={saving}>{saving ? 'Connecting…' : 'Connect & discover'}</button></div>
    </form>
  </div>
{/if}

<style>
  .page{padding:24px;max-width:1280px;margin:0 auto;color:var(--text,#eef)} header{display:flex;justify-content:space-between;gap:24px;align-items:start;margin-bottom:24px}h1{margin:2px 0 6px;font-size:28px}h2{margin:0;font-size:17px}.eyebrow{color:#8c7cff;font-size:11px;font-weight:800;letter-spacing:.12em;margin:0}.lede,.card p,.how li,.fine,.muted{color:var(--muted,#9ba3c4)}.lede{margin:0}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(390px,1fr));gap:16px}.card,.how{background:var(--panel,#121628);border:1px solid var(--border,#2a3152);border-radius:12px;padding:20px}.card-head{display:flex;align-items:center;gap:12px}.brand{width:38px;height:38px;border-radius:10px;display:grid;place-items:center;background:#6857f5;color:white;font-weight:900;font-size:20px}.brand.nango{background:#ff6b35}.card-head p{margin:3px 0 0;font-size:12px}.status{margin-left:auto;padding:5px 9px;border-radius:999px;background:#2a2030;color:#f0b85c;font-size:11px;font-weight:700}.status.online{background:#12352d;color:#65e3a7}.facts{display:flex;gap:8px;flex-wrap:wrap;margin:16px 0}.facts span{background:#1c2239;border-radius:999px;padding:5px 9px;font-size:11px}.guardrail{border-left:3px solid #7868ff;background:#191b31;padding:10px 12px;font-size:12px;color:#bdc4e0}.setup-guide{margin-top:14px;border:1px solid #303757;border-radius:8px;background:#101426}.setup-guide summary{padding:11px 12px;cursor:pointer;color:#c8c1ff;font-size:12px;font-weight:750}.setup-guide[open] summary{border-bottom:1px solid #303757}.setup-guide ol{margin:12px 14px 10px;padding-left:20px;display:grid;gap:8px;color:#b6beda;font-size:12px;line-height:1.45}.setup-guide a{display:inline-block;margin:0 14px 13px;color:#9e91ff;font-size:12px}.actions{display:flex;align-items:center;gap:9px;margin-top:18px}button,.button-link{border:1px solid #343b60;background:#20263e;color:#eef;border-radius:7px;padding:9px 13px;cursor:pointer;text-decoration:none;font-size:13px}.primary{background:#6959f7;border-color:#6959f7}.danger{color:#ff8d98;border-color:#703943;background:#2d1c27}.button-link{display:inline-block;margin-top:18px}.inline-error,.notice.error{color:#ff8995;background:#341d29}.inline-error{padding:9px;margin-top:10px;border-radius:7px}.notice{padding:11px 14px;border-radius:8px;margin-bottom:16px}.notice.success{color:#62dfa1;background:#12342c}.how{margin-top:16px}.how ol{margin:12px 0 0;padding-left:20px;display:grid;gap:8px}.backdrop{position:fixed;inset:0;background:#050714c9;display:grid;place-items:center;z-index:100;padding:20px}.modal{width:min(600px,100%);background:#14182b;border:1px solid #394161;border-radius:12px;padding:22px;box-shadow:0 20px 80px #0008}.modal>p{color:#aab2d0}.modal label{display:grid;gap:7px;margin:15px 0;font-size:12px;color:#bbc3e2}.modal input,.modal select{background:#0f1324;color:#eef;border:1px solid #343b5b;border-radius:7px;padding:10px}.header-row{display:grid;grid-template-columns:180px 1fr;gap:10px}.check{display:flex!important;grid-template-columns:auto 1fr!important;align-items:center}.modal-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}.fine{font-size:11px}@media(max-width:650px){.grid{grid-template-columns:1fr}.header-row{grid-template-columns:1fr}.page{padding:16px}}
</style>
