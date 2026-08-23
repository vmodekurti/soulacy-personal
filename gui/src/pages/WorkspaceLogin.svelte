<script>
  import PublicNav from '../lib/PublicNav.svelte'

  let workspace = ''
  let checking = false
  let error = ''
  let recent = []

  try { recent = JSON.parse(localStorage.getItem('soulacy.recentWorkspaces') || '[]').slice(0, 4) } catch (_) {}

  function destination(value) {
    const raw = String(value || '').trim()
    if (!raw) return null
    try {
      const parsed = new URL(raw, location.origin)
      const match = parsed.pathname.match(/^\/w\/([^/]+)\/?$/)
      if (match) return { id: decodeURIComponent(match[1]), href: `${parsed.pathname}${parsed.search}` }
    } catch (_) {}
    if (/^[A-Za-z0-9._:-]+$/.test(raw)) return { id: raw, href: `/w/${encodeURIComponent(raw.toLowerCase())}` }
    return null
  }

  function remember(config) {
    const item = { id: config.workspace_id, slug: config.workspace_slug || config.workspace_id, name: config.workspace_name, organization: config.organization_name, logo: config.workspace_logo || config.organization_logo || '' }
    recent = [item, ...recent.filter((entry) => entry.id !== item.id)].slice(0, 4)
    localStorage.setItem('soulacy.recentWorkspaces', JSON.stringify(recent))
  }

  async function continueToWorkspace() {
    const target = destination(workspace)
    if (!target || checking) {
      error = 'Enter your short workspace address or paste the invitation link.'
      return
    }
    checking = true
    error = ''
    try {
      const response = await fetch(`/api/v1/auth/workspaces/${encodeURIComponent(target.id)}/config`)
      if (!response.ok) throw new Error('That workspace could not be found. Check the workspace address with your administrator.')
      const body = await response.json()
      remember(body.workspace)
      const query = new URL(target.href, location.origin).search
      location.assign(`/w/${encodeURIComponent(body.workspace.workspace_slug || body.workspace.workspace_id)}${query}`)
    } catch (cause) {
      error = cause?.message || 'The workspace could not be opened.'
    } finally {
      checking = false
    }
  }
</script>

<svelte:head><title>Workspace login · Soulacy</title></svelte:head>

<div class="discovery-shell">
  <PublicNav current="workspace" helpPage="workspace-discovery" />
  <main>
    <section class="discovery-card">
      <div class="mark" aria-hidden="true">⬡</div>
      <p class="eyebrow">WORKSPACE ACCESS</p>
      <h1>Find your workspace</h1>
      <p class="lead">Enter the short workspace address your administrator shared. We’ll open its branded sign-in page before connecting to the identity provider.</p>
      {#if recent.length}
        <div class="recent" aria-label="Recent workspaces">
          <span class="recent-label">Recent workspaces</span>
          {#each recent as item (item.id)}
            <a href={`/w/${encodeURIComponent(item.slug)}`}>
              {#if item.logo}<img src={item.logo} alt="" />{:else}<span class="recent-mark">⬡</span>{/if}
              <span><strong>{item.name}</strong><small>{item.organization}</small></span><b aria-hidden="true">→</b>
            </a>
          {/each}
        </div>
      {/if}
      <form on:submit|preventDefault={continueToWorkspace}>
        <label for="workspace">Workspace address</label>
        <div class="address"><span>{location.host}/w/</span><input id="workspace" bind:value={workspace} autocomplete="organization" placeholder="acme-agents" /></div>
        <button type="submit" disabled={checking || !workspace.trim()}>{checking ? 'Finding workspace…' : 'Continue to workspace'}</button>
      </form>
      {#if error}<div class="error" role="alert">{error}</div>{/if}
      <div class="guidance"><div><strong>New member?</strong><span>Just click the invitation link sent by your workspace administrator—there is nothing to type.</span></div><div><strong>Returning member?</strong><span>Select a recent workspace above or enter its short address.</span></div></div>
      <a class="back" href="/">← Back to home</a>
    </section>
  </main>
</div>

<style>
  :global(body){margin:0;background:#090b14;color:#f2f3ff;font-family:Inter,system-ui,sans-serif}.discovery-shell{min-height:100vh;display:flex;flex-direction:column;background:radial-gradient(circle at 12% 8%,#34237366,transparent 36%),radial-gradient(circle at 88% 92%,#07654a55,transparent 36%)}main{flex:1;display:grid;place-items:center;padding:48px 24px 72px}.discovery-card{box-sizing:border-box;width:min(620px,100%);padding:42px;border:1px solid #ffffff18;border-radius:22px;background:#111522f2;box-shadow:0 24px 80px #0007}.mark{display:grid;place-items:center;width:46px;height:46px;border:1px solid #8e7bff66;border-radius:13px;background:#8e7bff14;color:#9d8cff;font-size:27px}.eyebrow{margin:24px 0 7px;color:#70ddb0;font-size:10px;font-weight:850;letter-spacing:.16em}h1{margin:0;font-size:38px;letter-spacing:-.04em}.lead{margin:13px 0 25px;color:#a1a8bf;line-height:1.6}form{display:grid;gap:9px}label,.recent-label{color:#d6d9e8;font-size:12px;font-weight:750}.address{display:flex;align-items:center;border:1px solid #ffffff22;border-radius:10px;background:#090c16;overflow:hidden}.address:focus-within{border-color:#8e7bff;outline:2px solid #8e7bff2c}.address>span{padding-left:13px;color:#757e99;font-size:13px;white-space:nowrap}.address input{min-width:0;width:100%;padding:13px 13px 13px 2px;border:0;background:transparent;color:#fff;font:inherit;outline:0}button{margin-top:4px;padding:13px;border:0;border-radius:10px;background:linear-gradient(135deg,#795cff,#24bd7c);color:#fff;font:inherit;font-weight:800}button:disabled{opacity:.5}.error{margin-top:14px;padding:12px;border-radius:9px;background:#ef5b6818;color:#ffabb4;font-size:12px}.recent{display:grid;gap:8px;margin:0 0 22px}.recent a{display:flex;align-items:center;gap:11px;padding:11px;border:1px solid #ffffff12;border-radius:10px;background:#090c1688;color:inherit;text-decoration:none}.recent a:hover{border-color:#8e7bff77;background:#171a2b}.recent img,.recent-mark{width:34px;height:34px;border-radius:8px;object-fit:cover}.recent-mark{display:grid;place-items:center;background:#8e7bff18;color:#9d8cff}.recent a>span:nth-child(2){display:grid;gap:2px;flex:1}.recent small{color:#929ab3}.recent b{color:#70ddb0}.guidance{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin:24px 0}.guidance div{display:grid;gap:5px;padding:13px;border:1px solid #ffffff10;border-radius:10px;background:#090c1688}.guidance strong{font-size:12px}.guidance span{color:#929ab3;font-size:11px;line-height:1.45}.back{color:#9d91e8;text-decoration:none;font-size:12px}@media(max-width:620px){.discovery-card{padding:26px}.guidance{grid-template-columns:1fr}h1{font-size:32px}.address>span{display:none}.address input{padding-left:13px}}
</style>
