<script>
  import { onMount } from 'svelte'
  import PublicNav from '../lib/PublicNav.svelte'

  let enabled = false
  let loading = true
  let eligible = false
  let email = ''
  let organizationName = ''
  let workspaceName = ''
  let workspaceSlug = ''
  let displayName = ''
  let saving = false
  let error = ''

  const signInURL = '/api/v1/auth/oidc/start?navigate=true&return_to=%2Fsignup'

  onMount(async () => {
    try {
      const configResponse = await fetch('/api/v1/signup/config')
      const config = configResponse.ok ? await configResponse.json() : null
      enabled = !!config?.enabled
      if (!enabled) return
      const sessionResponse = await fetch('/api/v1/signup')
      if (sessionResponse.ok) {
        const session = await sessionResponse.json()
        eligible = !!session?.eligible
        email = session?.email || ''
        displayName = email
      }
    } catch (_) {
      error = 'Soulacy could not check signup availability.'
    } finally {
      loading = false
    }
  })

  async function createWorkspace() {
    if (saving) return
    saving = true
    error = ''
    try {
      const response = await fetch('/api/v1/signup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          organization_name: organizationName,
          workspace_name: workspaceName,
          workspace_slug: workspaceSlug,
          display_name: displayName,
        }),
      })
      const body = await response.json().catch(() => ({}))
      if (!response.ok) throw new Error(body.error || 'Your workspace could not be created.')

      // The OIDC session began as onboarding-only. Membership now exists, so
      // normal refresh re-resolves it and upgrades the cookie to workspace
      // owner authority before the customer continues.
      await fetch('/api/v1/auth/refresh', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
      })
      location.assign(body?.next?.setup_path || '/')
    } catch (e) {
      error = e?.message || 'Your workspace could not be created.'
      saving = false
    }
  }
</script>

<svelte:head><title>Create your Soulacy workspace</title></svelte:head>

<main class="signup-screen">
  <PublicNav current="signup" workspaceHref="/workspace-login" helpPage="access" />
  <section class="signup-shell">
    <div class="intro">
      <p class="eyebrow">SELF-SERVICE AGENT OPERATIONS</p>
      <h1>Your AI team starts in its own secure workspace.</h1>
      <p>Build and operate production agents with isolated data, governed access, auditable actions, and subscription controls from day one.</p>
      <div class="proof"><span>✓ Verified identity</span><span>✓ Dedicated tenant boundary</span><span>✓ Owner-controlled SSO</span></div>
    </div>

    <div class="signup-card">
      {#if loading}
        <p class="state">Checking signup availability…</p>
      {:else if !enabled}
        <h2>Self-service signup is not available</h2>
        <p class="muted">Ask the Soulacy deployment administrator to provision your organization.</p>
        <a class="secondary" href="/">Return home</a>
      {:else if !eligible}
        <h2>Create your workspace</h2>
        <p class="muted">Continue with the platform identity provider. Soulacy requires a verified email and never accepts an owner address typed into a signup form.</p>
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <a class="primary" href={signInURL}>Continue with secure sign-in <span>→</span></a>
        <small>Already have a workspace? <a href="/workspace-login">Sign in here</a>.</small>
      {:else}
        <h2>Name your organization</h2>
        <p class="identity">Creating as <strong>{email}</strong></p>
        <form on:submit|preventDefault={createWorkspace}>
          <label>Organization name<input bind:value={organizationName} maxlength="120" autocomplete="organization" placeholder="Acme, Inc." required /></label>
          <label>First workspace<input bind:value={workspaceName} maxlength="120" placeholder="AI Operations" required /></label>
          <label>Workspace address <span>optional</span><div class="slug"><small>/w/</small><input bind:value={workspaceSlug} maxlength="63" pattern="[a-z0-9]+(?:-[a-z0-9]+)*" placeholder="acme-ai" /></div></label>
          <label>Your display name<input bind:value={displayName} maxlength="120" autocomplete="name" required /></label>
          {#if error}<p class="error" role="alert">{error}</p>{/if}
          <button class="primary" type="submit" disabled={saving}>{saving ? 'Creating secure workspace…' : 'Create workspace'}</button>
        </form>
      {/if}
    </div>
  </section>
</main>

<style>
  :global(body){margin:0;background:#080a12;color:#f4f3ff;font-family:Inter,ui-sans-serif,system-ui,-apple-system,sans-serif}
  .signup-screen{min-height:100vh;background:radial-gradient(circle at 18% 20%,#6648d72b,transparent 34%),radial-gradient(circle at 82% 80%,#1fc1841f,transparent 32%),#090b14}
  .signup-shell{display:grid;grid-template-columns:minmax(0,1fr) minmax(360px,480px);align-items:center;gap:80px;width:min(1080px,calc(100% - 48px));min-height:calc(100vh - 86px);margin:auto;padding:42px 0}.intro{max-width:590px}.eyebrow{color:#6ee0ad;font-size:11px;font-weight:850;letter-spacing:.16em}.intro h1{margin:16px 0 22px;font-size:clamp(40px,5vw,64px);line-height:1.04;letter-spacing:-.045em}.intro>p:not(.eyebrow){color:#a5acc3;font-size:17px;line-height:1.65}.proof{display:flex;flex-wrap:wrap;gap:10px 20px;margin-top:28px;color:#8e97af;font-size:12px}.proof span::first-letter{color:#54d99e}
  .signup-card{padding:30px;border:1px solid #ffffff1d;border-radius:20px;background:#111522e8;box-shadow:0 28px 80px #0007}.signup-card h2{margin:0 0 9px;font-size:25px;letter-spacing:-.025em}.muted,.state{color:#969eb6;font-size:13px;line-height:1.55}.identity{margin:0 0 22px;padding:11px 13px;border:1px solid #56dca52e;border-radius:10px;background:#30c38910;color:#9ba7b8;font-size:12px}.identity strong{color:#c7f5e0}form{display:grid;gap:15px}label{display:grid;gap:7px;color:#c8cddd;font-size:12px;font-weight:700}label>span{color:#747d96;font-weight:500}input{box-sizing:border-box;width:100%;padding:12px 13px;border:1px solid #ffffff20;border-radius:10px;outline:none;background:#090c15;color:#f5f4ff;font:inherit;font-weight:500}input:focus{border-color:#846dff;box-shadow:0 0 0 3px #765cff1c}.slug{display:flex;align-items:center;border:1px solid #ffffff20;border-radius:10px;background:#090c15}.slug:focus-within{border-color:#846dff;box-shadow:0 0 0 3px #765cff1c}.slug small{padding-left:13px;color:#687189}.slug input{border:0;box-shadow:none}.primary,.secondary{display:flex;align-items:center;justify-content:space-between;box-sizing:border-box;width:100%;margin-top:18px;padding:13px 15px;border:0;border-radius:10px;background:linear-gradient(110deg,#765cff,#26bd80);color:white;font-size:13px;font-weight:800;text-decoration:none;cursor:pointer}.primary:disabled{opacity:.58;cursor:wait}.secondary{justify-content:center;background:#23283a}.signup-card>small{display:block;margin-top:16px;color:#737c95;text-align:center}.signup-card a{color:#a79aff}.error{padding:10px 12px;border:1px solid #ff7b8d3d;border-radius:9px;background:#e94f6114;color:#ffc5cc;font-size:12px}
  @media(max-width:850px){.signup-shell{grid-template-columns:1fr;gap:38px;padding:55px 0 80px}.intro h1{font-size:42px}.intro>p:not(.eyebrow){font-size:15px}}
  @media(max-width:520px){.signup-shell{width:min(100% - 28px)}.signup-card{padding:22px}.proof{display:grid}}
</style>
