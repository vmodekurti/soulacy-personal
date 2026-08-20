<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { apiKey, authRequired } from '../lib/stores.js'

  let oidcEnabled = false
  let bootstrapKey = ''
  let connected = false
  let required = true
  let blocked = false
  let busy = false
  let error = ''
  let complete = false
  let restarting = false

  let provider = 'google'
  let issuer = 'https://accounts.google.com'
  let clientId = ''
  let clientSecret = ''
  let audience = ''
  let organizationName = ''
  let workspaceName = 'Primary workspace'
  let ownerEmail = ''
  let ownerDisplayName = ''
  const redirectURL = `${location.origin}/api/v1/auth/oidc/callback`

  const providers = {
    google: 'https://accounts.google.com',
    microsoft: 'https://login.microsoftonline.com/common/v2.0',
    okta: '', auth0: '', keycloak: '', custom: '',
  }

  function chooseProvider() { issuer = providers[provider] || '' }

  onMount(async () => {
    try {
      const res = await fetch('/api/v1/auth/oidc/config')
      const cfg = res.ok ? await res.json() : {}
      oidcEnabled = !!cfg.enabled
    } catch (_) {}
  })

  function signIn() {
    $apiKey = ''
    $authRequired = false
    location.assign('/api/v1/auth/oidc/start?client=gui&navigate=true')
  }

  async function unlock() {
    if (!bootstrapKey.trim() || busy) return
    busy = true; error = ''
    const previous = $apiKey
    $apiKey = bootstrapKey.trim()
    try {
      const response = await api.admin.bootstrapStatus()
      connected = true
      required = !!response?.state?.required
      blocked = !!response?.state?.blocked
    } catch (e) {
      $apiKey = previous
      error = e?.status === 401 || e?.status === 403
        ? 'That deployment key was rejected. Use server.api_key from the host configuration.'
        : (e?.message || 'Could not read bootstrap status.')
    } finally { busy = false }
  }

  async function configure() {
    if (busy) return
    busy = true; error = ''
    try {
      if (!issuer.trim() || !clientId.trim() || !ownerEmail.trim() || !ownerDisplayName.trim() || !organizationName.trim() || !workspaceName.trim()) {
        throw new Error('Complete every required field before continuing.')
      }
      await api.config.patch({ auth: {
        mode: 'jwt', jwt_access_ttl: '15m', jwt_refresh_ttl: '168h',
        oidc_issuer: issuer.trim(), oidc_audience: audience.trim(),
        oidc_client_id: clientId.trim(), oidc_client_secret: clientSecret,
        oidc_redirect_url: redirectURL, oidc_scopes: ['openid', 'profile', 'email'],
      }})
      await api.admin.bootstrap({
        organization_name: organizationName.trim(), workspace_name: workspaceName.trim(),
        owner_email: ownerEmail.trim(), owner_display_name: ownerDisplayName.trim(),
      })
      complete = true
    } catch (e) { error = e?.message || 'Administrator setup failed.' }
    finally { busy = false }
  }

  async function restartAndSignIn() {
    if (restarting) return
    restarting = true; error = ''
    try { await api.admin.restart() } catch (e) {
      if (e?.status === 401 || e?.status === 403) { error = 'The deployment key can no longer restart this server.'; restarting = false; return }
    }
    $apiKey = ''
    const started = Date.now()
    const check = async () => {
      try {
        const res = await fetch('/api/v1/auth/oidc/config', { cache: 'no-store' })
        const cfg = res.ok ? await res.json() : {}
        if (cfg.enabled) { signIn(); return }
      } catch (_) {}
      if (Date.now() - started > 60000) { error = 'The gateway did not return within one minute. Refresh this page after it starts.'; restarting = false; return }
      setTimeout(check, 750)
    }
    setTimeout(check, 750)
  }
</script>

<svelte:head><title>Administrator setup · Soulacy</title></svelte:head>

<main class="setup-shell">
  <section class="setup-card">
    <div class="brand">⬡ <span>Soulacy</span></div>
    <p class="eyebrow">TEAM &amp; SCALE</p>
    <h1>Administrator access</h1>
    <p class="lead">Sign in normally with your organization. On a new deployment, use the host’s one-time setup key to create the first owner.</p>

    {#if oidcEnabled && !connected && !complete}
      <button class="primary" on:click={signIn}>Continue with your organization</button>
      <div class="divider"><span>first-time setup</span></div>
    {/if}

    {#if !connected && !complete}
      <label>Deployment setup key
        <input type="password" autocomplete="current-password" placeholder="Value of server.api_key" bind:value={bootstrapKey} on:keydown={(e) => e.key === 'Enter' && unlock()} />
      </label>
      <button class="secondary" disabled={busy || !bootstrapKey.trim()} on:click={unlock}>{busy ? 'Checking…' : 'Open administrator setup'}</button>
      <p class="hint">This key stays a deployment credential; it is never turned into a workspace super-admin.</p>
    {:else if blocked}
      <div class="notice bad">Tenant data is partially initialized. Setup stopped safely; repair the catalog before creating an owner.</div>
    {:else if connected && !required && !complete}
      <div class="notice">The first owner already exists.</div>
      {#if oidcEnabled}<button class="primary" on:click={signIn}>Sign in as administrator</button>{/if}
    {:else if connected && required && !complete}
      <div class="steps"><span class="active">1 Identity provider</span><span>2 First owner</span><span>3 Sign in</span></div>
      <div class="grid two">
        <label>Provider
          <select bind:value={provider} on:change={chooseProvider}>
            <option value="google">Google Workspace</option><option value="microsoft">Microsoft Entra ID</option>
            <option value="okta">Okta</option><option value="auth0">Auth0</option><option value="keycloak">Keycloak</option><option value="custom">Custom OIDC</option>
          </select>
        </label>
        <label>Issuer URL<input type="url" bind:value={issuer} placeholder="https://id.example.com" /></label>
        <label>Client ID<input bind:value={clientId} /></label>
        <label>Client secret <span>(if required)</span><input type="password" bind:value={clientSecret} /></label>
        <label>Audience <span>(optional)</span><input bind:value={audience} placeholder="Defaults to client ID" /></label>
        <label>Redirect URL<input value={redirectURL} readonly /></label>
      </div>
      <p class="hint">Register the redirect URL above with your identity provider. GitHub OAuth and Sign in with Apple need provider-specific flows and are not presented as compatible OIDC options.</p>
      <h2>First workspace owner</h2>
      <div class="grid two">
        <label>Organization name<input bind:value={organizationName} /></label>
        <label>Workspace name<input bind:value={workspaceName} /></label>
        <label>Owner name<input bind:value={ownerDisplayName} autocomplete="name" /></label>
        <label>Owner email<input type="email" bind:value={ownerEmail} autocomplete="email" /></label>
      </div>
      <p class="hint">The email must match the verified email returned by your identity provider.</p>
      <button class="primary" disabled={busy} on:click={configure}>{busy ? 'Creating administrator…' : 'Save and create first owner'}</button>
    {:else if complete}
      <div class="success"><strong>Your administrator is ready.</strong><span>Restart once to activate OIDC, then sign in with {ownerEmail}.</span></div>
      <button class="primary" disabled={restarting} on:click={restartAndSignIn}>{restarting ? 'Restarting gateway…' : 'Restart and sign in'}</button>
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <a class="back" href="/">← Back to Soulacy</a>
  </section>
</main>

<style>
  :global(body){margin:0;background:#090b14;color:#eef0ff;font-family:Inter,ui-sans-serif,system-ui,sans-serif}.setup-shell{min-height:100vh;display:grid;place-items:center;padding:32px 18px;background:radial-gradient(circle at 15% 10%,#32236b66,transparent 35%),radial-gradient(circle at 90% 90%,#0b654766,transparent 35%)}.setup-card{width:min(820px,100%);box-sizing:border-box;padding:36px;border:1px solid #ffffff18;border-radius:22px;background:#111522e8;box-shadow:0 28px 90px #0008}.brand{display:flex;gap:10px;align-items:center;color:#9b86ff;font-size:28px}.brand span{font-size:18px;font-weight:700;color:#fff}.eyebrow{margin:28px 0 8px;color:#7fe0b3;font-size:12px;font-weight:800;letter-spacing:.14em}h1{margin:0;font-size:38px;letter-spacing:-.04em}h2{margin:28px 0 14px;font-size:18px}.lead{color:#aeb5cc;line-height:1.6;max-width:650px}.grid{display:grid;gap:14px}.two{grid-template-columns:repeat(2,minmax(0,1fr))}label{display:grid;gap:7px;color:#cbd0e2;font-size:13px;font-weight:650}label span{color:#737b96;font-weight:400}input,select{box-sizing:border-box;width:100%;border:1px solid #ffffff1f;border-radius:10px;background:#090c16;color:#f6f7ff;padding:12px 13px;font:inherit;outline:none}input:focus,select:focus{border-color:#8e73ff;box-shadow:0 0 0 3px #795cff22}input[readonly]{color:#9098b0}.primary,.secondary{width:100%;margin-top:18px;border:0;border-radius:11px;padding:13px 18px;font-weight:750;cursor:pointer}.primary{background:linear-gradient(135deg,#795cff,#24bd7c);color:white}.secondary{background:#22283a;color:#eef0ff;border:1px solid #ffffff1b}.primary:disabled,.secondary:disabled{opacity:.55;cursor:wait}.divider{display:flex;align-items:center;gap:12px;margin:20px 0;color:#777f98;font-size:12px}.divider:before,.divider:after{content:'';height:1px;background:#ffffff16;flex:1}.hint{color:#858da7;font-size:12px;line-height:1.5}.steps{display:flex;gap:10px;margin:24px 0 20px;color:#747c95;font-size:12px}.steps span{padding:7px 10px;border-radius:20px;background:#ffffff08}.steps .active{color:#dcd5ff;background:#795cff2a}.notice,.success{display:flex;flex-direction:column;gap:5px;margin:20px 0;padding:14px;border-radius:10px;background:#22bd7b18;color:#b9f4d7}.notice.bad,.error{background:#ef5b6815;color:#ff9da7}.error{padding:11px;border-radius:9px;font-size:13px}.back{display:block;margin-top:24px;color:#8992ad;text-align:center;text-decoration:none}@media(max-width:680px){.setup-card{padding:24px}.two{grid-template-columns:1fr}h1{font-size:31px}}
</style>
