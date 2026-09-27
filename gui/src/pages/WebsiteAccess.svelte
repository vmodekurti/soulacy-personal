<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import TourButton from '../lib/TourButton.svelte'

  let connections = []
  let loading = true
  let error = ''
  let info = ''
  let showCapture = false
  let saving = false
  let name = ''
  let loginURL = ''
  let domains = ''
  let agentIDs = ''
  let captureConnection = null
  let captureOpened = false
  let companionReady = false
  let companionVersion = ''

  $: captureCommand = loginURL.trim()
    ? `sy --gateway ${window.location.origin} connection capture ${shellQuote(loginURL.trim())}${name.trim() ? ` --name ${shellQuote(name.trim())}` : ''}${domains.trim() ? ` --domains ${shellQuote(domains.trim())}` : ''}${agentIDs.trim() ? ` --agents ${shellQuote(agentIDs.trim())}` : ''}`
    : ''

  async function load() {
    loading = true
    error = ''
    try {
      const response = await api.connections.list()
      connections = response.connections || []
    } catch (e) {
      error = e.message || 'Could not load website sign-ins.'
    } finally {
      loading = false
    }
  }

  function splitValues(value) {
    return String(value || '').split(',').map(item => item.trim()).filter(Boolean)
  }

  function shellQuote(value) {
    return `'${String(value).replaceAll("'", "'\\''")}'`
  }

  function companionRequest(action, payload = {}, timeout = 1500) {
    return new Promise((resolve, reject) => {
      const requestId = crypto.randomUUID()
      const cleanup = () => {
        clearTimeout(timer)
        window.removeEventListener('message', receive)
      }
      const receive = event => {
        if (event.source !== window || event.origin !== window.location.origin) return
        if (event.data?.source !== 'soulacy-session-capture' || event.data?.requestId !== requestId) return
        cleanup()
        if (event.data.ok) resolve(event.data.payload || {})
        else reject(new Error(event.data.error || 'The secure session companion could not complete the request.'))
      }
      const timer = setTimeout(() => {
        cleanup()
        reject(new Error('Soulacy Session Capture companion was not detected.'))
      }, timeout)
      window.addEventListener('message', receive)
      window.postMessage({ source: 'soulacy-web', requestId, action, payload }, window.location.origin)
    })
  }

  async function detectCompanion() {
    try {
      const result = await companionRequest('ping', {}, 600)
      companionReady = true
      companionVersion = result.version || ''
    } catch (_) {
      companionReady = false
      companionVersion = ''
    }
  }

  function openCapture(connection = null) {
    error = ''
    info = ''
    captureConnection = connection
    captureOpened = false
    name = connection?.name || ''
    loginURL = connection?.base_url || ''
    domains = (connection?.allowed_domains || []).join(', ')
    agentIDs = (connection?.agent_ids || []).join(', ')
    showCapture = true
    detectCompanion()
  }

  async function openSecureSignIn() {
    if (!companionReady) {
      error = 'Install or reconnect the Soulacy Session Capture companion first.'
      return
    }
    if (!loginURL.trim()) {
      error = 'Enter the website sign-in URL first.'
      return
    }
    saving = true
    error = ''
    try {
      if (!captureConnection) {
        const parsed = new URL(loginURL.trim())
        const response = await api.connections.create({
          name: name.trim() || `${parsed.hostname} sign-in`,
          scope: 'user',
          kind: 'browser_session',
          base_url: loginURL.trim(),
          allowed_domains: splitValues(domains),
          agent_ids: splitValues(agentIDs),
        })
        captureConnection = response.connection
      }
      await companionRequest('open', {
        connectionId: captureConnection.id,
        baseUrl: captureConnection.base_url,
        allowedDomains: captureConnection.allowed_domains,
      }, 15000)
      captureOpened = true
      info = `Secure sign-in opened for ${captureConnection.name}. Complete the normal login, then return here.`
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function saveSecureSession() {
    if (!captureConnection) return
    saving = true
    error = ''
    try {
      const result = await companionRequest('capture', {
        connectionId: captureConnection.id,
        baseUrl: captureConnection.base_url,
        allowedDomains: captureConnection.allowed_domains,
      }, 15000)
      await api.connections.setSession(captureConnection.id, { storage_state: result.storageState })
      info = `${captureConnection.name} is signed in. Its encrypted session is ready for selected agents.`
      showCapture = false
      await load()
    } catch (e) {
      error = e.message
    } finally {
      saving = false
    }
  }

  async function copyCLICommand() {
    if (!captureCommand) {
      error = 'Enter the website sign-in URL first.'
      return
    }
    await navigator.clipboard.writeText(captureCommand)
    info = 'CLI capture command copied.'
  }

  async function removeConnection(connection) {
    if (!confirm(`Delete ${connection.name}? Its encrypted session and all agent grants will be removed.`)) return
    try {
      await api.connections.delete(connection.id)
      info = 'Website sign-in deleted.'
      await load()
    } catch (e) {
      error = e.message
    }
  }

  onMount(load)
</script>

<svelte:head><title>Website Access — Soulacy</title></svelte:head>

<main class="page">
  <header>
    <div>
      <p class="eyebrow">INTEGRATIONS</p>
      <h1>Website Access</h1>
      <p class="lede">Let selected agents read sites you already subscribe to without giving the model your password or cookies.</p>
    </div>
    <TourButton />
  </header>

  {#if error}<div class="notice error">{error}</div>{/if}
  {#if info}<div class="notice success">{info}</div>{/if}

  <section class="card intro">
    <div>
      <h2>Saved website sign-ins</h2>
      <p>Soulacy opens the real website login in Chrome, where password managers, MFA, CAPTCHA, and passkeys continue to work. It saves only the approved site’s session state, encrypts it locally, and replays the cookies inside a restricted read-only fetch tool.</p>
    </div>
    <button class="primary" on:click={() => openCapture()}>+ Add website sign-in</button>
  </section>

  {#if loading}
    <div class="empty">Loading website sign-ins…</div>
  {:else if connections.length}
    <section class="list">
      {#each connections as connection (connection.id)}
        <article class="connection">
          <div class="lock">🔐</div>
          <div class="copy">
            <strong>{connection.name}</strong>
            <span>{connection.base_url}</span>
            <div class="facts">
              <span>{connection.allowed_domains?.join(', ')}</span>
              <span>{connection.agent_ids?.length || 0} agent grant(s)</span>
              <span>Encrypted</span>
            </div>
          </div>
          <span class:ready={connection.status === 'ready'} class="status">{connection.status}</span>
          <button class="compact" on:click={() => openCapture(connection)}>{connection.status === 'ready' ? 'Refresh sign-in' : 'Reconnect'}</button>
          <button class="danger compact" on:click={() => removeConnection(connection)}>Delete</button>
        </article>
      {/each}
    </section>
  {:else}
    <div class="empty">No saved website sign-ins yet. Add one for a subscription, research portal, or another site that requires your account.</div>
  {/if}

  <section class="guardrail">
    <strong>Session boundary</strong>
    <span>Soulacy accepts HTTPS pages only, blocks private-network targets and off-domain redirects, and marks the sign-in expired when the site returns 401 or 403. Cookie values never enter prompts, tool arguments, logs, or API responses.</span>
  </section>
</main>

{#if showCapture}
  <div class="backdrop" role="presentation" on:click={() => !saving && (showCapture = false)}>
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_noninteractive_element_interactions -->
    <form class="modal" on:submit|preventDefault={captureOpened ? saveSecureSession : openSecureSignIn} on:click|stopPropagation>
      <h2>{captureConnection ? 'Refresh website sign-in' : 'Add authenticated website'}</h2>
      <p>Sign in on the website itself. Soulacy never sees the password you type.</p>
      <label>Display name<input bind:value={name} placeholder="HBR subscription" disabled={!!captureConnection} /></label>
      <label>Website sign-in URL<input type="url" bind:value={loginURL} placeholder="https://example.com/login" required disabled={!!captureConnection} /></label>
      <label>Approved domains<input bind:value={domains} placeholder="Defaults to the website domain" disabled={!!captureConnection} /></label>
      {#if !captureConnection}<label>Agent IDs (optional)<input bind:value={agentIDs} placeholder="research-agent, daily-briefing" /></label>{/if}

      <div class:ready={companionReady} class="companion">
        <strong>{companionReady ? `Session Capture companion ready${companionVersion ? ` · v${companionVersion}` : ''}` : 'Session Capture companion not detected'}</strong>
        {#if !companionReady}
          <span>Download and unzip it. In Chrome, open <code>chrome://extensions</code>, enable Developer mode, and choose <strong>Load unpacked</strong>.</span>
          <div class="actions"><a href="/downloads/soulacy-session-capture.zip">Download companion</a><button type="button" class="compact" on:click={detectCompanion}>Check again</button></div>
        {/if}
      </div>

      {#if captureOpened}
        <ol><li>Complete the normal sign-in in the Chrome tab Soulacy opened.</li><li>Return here and select <strong>Save signed-in session</strong>.</li><li>Open Studio and select this connection for each agent that may use it.</li></ol>
      {/if}

      <details>
        <summary>CLI fallback</summary>
        <p>Use this from a Mac, Linux, or Windows computer with Chrome even when Soulacy itself runs on a platform without shell access.</p>
        {#if captureCommand}<pre>{captureCommand}</pre>{/if}
        <button type="button" class="compact" on:click={copyCLICommand}>Copy CLI command</button>
      </details>

      <div class="modal-actions">
        <button type="button" on:click={() => showCapture = false} disabled={saving}>Cancel</button>
        <button class="primary" disabled={saving || !companionReady}>{saving ? 'Working…' : captureOpened ? 'Save signed-in session' : 'Open secure sign-in'}</button>
      </div>
    </form>
  </div>
{/if}

<style>
  .page{padding:24px;max-width:1180px;margin:0 auto;color:var(--text,#eef)}header{display:flex;justify-content:space-between;gap:24px;align-items:start;margin-bottom:24px}h1{margin:2px 0 6px;font-size:28px}h2{margin:0;font-size:18px}.eyebrow{color:#8c7cff;font-size:11px;font-weight:800;letter-spacing:.12em;margin:0}.lede,.card p,.modal>p,.guardrail span{color:var(--muted,#9ba3c4)}.lede{margin:0;max-width:760px}.card,.connection,.guardrail{background:var(--panel,#121628);border:1px solid var(--border,#2a3152);border-radius:12px}.intro{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:20px}.intro p{max-width:780px;margin:8px 0 0;line-height:1.55}.list{display:grid;gap:10px;margin:16px 0}.connection{display:flex;align-items:center;gap:12px;padding:14px}.lock{font-size:22px}.copy{display:grid;gap:4px;min-width:0;flex:1}.copy>span{color:var(--muted,#9ba3c4);font-size:12px;overflow:hidden;text-overflow:ellipsis}.facts{display:flex;gap:7px;flex-wrap:wrap;margin-top:4px}.facts span{background:#1c2239;border-radius:999px;padding:4px 8px;font-size:10px}.status{padding:5px 9px;border-radius:999px;background:#2a2030;color:#f0b85c;font-size:11px;font-weight:700;text-transform:capitalize}.status.ready{background:#12352d;color:#65e3a7}.empty{margin:16px 0;padding:30px;border:1px dashed #343b5d;border-radius:10px;color:#929bbb;text-align:center}.guardrail{display:grid;gap:6px;margin-top:16px;padding:16px;border-left:3px solid #7868ff}.guardrail span{font-size:12px;line-height:1.5}button,.actions a{border:1px solid #343b60;background:#20263e;color:#eef;border-radius:7px;padding:9px 13px;cursor:pointer;text-decoration:none;font-size:13px}.primary{background:#6959f7;border-color:#6959f7}.danger{color:#ff8d98;border-color:#703943;background:#2d1c27}.compact{padding:6px 9px}.notice{padding:11px 14px;border-radius:8px;margin-bottom:16px}.notice.error{color:#ff8995;background:#341d29}.notice.success{color:#62dfa1;background:#12342c}.backdrop{position:fixed;inset:0;background:#050714c9;display:grid;place-items:center;z-index:100;padding:20px}.modal{width:min(620px,100%);max-height:90vh;overflow:auto;background:#14182b;border:1px solid #394161;border-radius:12px;padding:22px;box-shadow:0 20px 80px #0008}.modal label{display:grid;gap:7px;margin:15px 0;font-size:12px;color:#bbc3e2}.modal input{background:#0f1324;color:#eef;border:1px solid #343b5b;border-radius:7px;padding:10px}.modal input:disabled{opacity:.7}.companion{display:grid;gap:8px;margin:14px 0;padding:12px;border:1px solid #70434b;border-radius:8px;background:#2b1b26;color:#ff9ca6;font-size:12px}.companion.ready{border-color:#245c4d;background:#112e28;color:#65e3a7}.companion span,details p,ol{color:#b8c0dc;line-height:1.45}.actions{display:flex;gap:8px;align-items:center}.modal details{margin-top:14px;border:1px solid #303757;border-radius:8px;padding:11px}.modal summary{cursor:pointer;color:#c8c1ff;font-size:12px;font-weight:700}.modal pre{white-space:pre-wrap;word-break:break-all;background:#090d1b;border-radius:7px;padding:10px;color:#b9f3dc;font-size:11px}.modal-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}@media(max-width:720px){.page{padding:16px}.intro,.connection{align-items:stretch;flex-direction:column}.status{width:max-content}}
</style>
