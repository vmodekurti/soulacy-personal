<script>
  import { onMount } from 'svelte'
  import BrandMark from './lib/BrandMark.svelte'
  import { apiKey, connected, authRequired } from './lib/stores.js'
  import ShareView from './pages/ShareView.svelte'
  import PersonalLanding from './pages/PersonalLanding.svelte'
  import { pageTitle } from './lib/pagetitle.js'
  import { api } from './lib/api.js'
  import { pluginNavEntries, isPluginPage, pluginIdFromPage } from './lib/pluginui.js'
  import { waitForGateway, waitingMessage, timeoutMessage, RESTART_BUDGET } from './lib/gatewaywait.js'
  import { looksLikeStaleAssetError, recoverFromStaleAssets } from './lib/stalerecovery.js'
	import { navPages, navGroups, navAnchor } from './lib/nav.js'
  import Walkthrough from './lib/walkthrough/Walkthrough.svelte'
  import {
    loadWalkthroughState, startWalkthrough, shouldAutoStart,
  } from './lib/walkthrough/store.js'

  let page = 'dashboard'
  let shareToken = ''   // set from #share/<token> — renders the public read-only view
  let pluginPages = []   // nav entries for mounted plugin UIs (E8)
  let showKeyModal = false
  let keyInput = ''
  let keyError = ''
  let keySaving = false
  let sidebarOpen = false   // mobile drawer state (≤768px)
  let navCollapsed = false   // desktop: collapse the left nav to an icon rail
  let PageComponent = null
  let loadedPage = ''
  // Authentication is asynchronous. Do not mount a page behind the login
  // screen: page-local error state from that unauthenticated mount would still
  // be there when a successful login merely revealed the shell again.
  let sessionChecked = false
  let pageLoadError = ''
  let pageLoadStale = false
  let pageLoadSeq = 0

  function toggleNav() {
    navCollapsed = !navCollapsed
    try { localStorage.setItem('soulacy-nav-collapsed', navCollapsed ? '1' : '0') } catch (_) {}
  }

  // Gateway restart (main-menu action): confirm modal + a blocking overlay
  // that polls /health until the replacement process answers, then reloads.
  let showRestartModal = false
  let restarting = false
  let restartError = ''
  let restartMessage = ''

  const pages = navPages
  $: currentPageEntry = pages.find(p => p.id === page) || pluginPages.find(p => p.id === page)
  $: currentPageLabel = currentPageEntry?.label || 'Soulacy'
  const currentWorkspaceLabel = 'Personal'
  $: mobilePrimaryPages = pages.filter(p => ['dashboard', 'studio', 'agents', 'chat'].includes(p.id))
  $: mobileMoreActive = !mobilePrimaryPages.some(p => p.id === page)

  const retiredPages = {
    builder: 'studio',
    build: 'studio',
  }

  const pageLoaders = {
    autopilot: () => import('./pages/Autopilot.svelte'),
    dashboard: () => import('./pages/Dashboard.svelte'),
    onboarding: () => import('./pages/Onboarding.svelte'),
    studio: () => import('./pages/Studio.svelte'),
    agents: () => import('./pages/Agents.svelte'),
    templates: () => import('./pages/Templates.svelte'),
    chat: () => import('./pages/Chat.svelte'),
    memory: () => import('./pages/Memory.svelte'),
    knowledge: () => import('./pages/Knowledge.svelte'),
    queues: () => import('./pages/Queues.svelte'),
    workboard: () => import('./pages/Workboard.svelte'),
    channels: () => import('./pages/Channels.svelte'),
    schedule: () => import('./pages/Schedule.svelte'),
    skills: () => import('./pages/Skills.svelte'),
    mcp: () => import('./pages/MCP.svelte'),
    pluginmgr: () => import('./pages/PluginManager.svelte'),
    providers: () => import('./pages/Providers.svelte'),
    secrets: () => import('./pages/Secrets.svelte'),
    activity: () => import('./pages/Activity.svelte'),
    browser: () => import('./pages/BrowserTrace.svelte'),
    config: () => import('./pages/Config.svelte'),
    mobile: () => import('./pages/Mobile.svelte'),
    logs: () => import('./pages/Logs.svelte'),
  }

  // Keep the browser tab title in sync with the active page (Story 15).
  $: if (typeof document !== 'undefined' && !$authRequired) document.title = pageTitle(page, pages, pluginPages)
  $: if (!shareToken && sessionChecked && !$authRequired && page !== loadedPage) loadPageComponent(page)

  function navigate(p) {
    p = retiredPages[p] || p
    page = p
    sidebarOpen = false
    history.pushState({}, '', '#' + p)
  }

  function openRestartModal() {
    restartError = ''
    showRestartModal = true
    sidebarOpen = false
  }

  async function loadPageComponent(nextPage) {
    const seq = ++pageLoadSeq
    loadedPage = nextPage
    pageLoadError = ''
    pageLoadStale = false
    PageComponent = null
    try {
      const mod = isPluginPage(nextPage)
        ? await import('./pages/PluginFrame.svelte')
        : await (pageLoaders[nextPage] || pageLoaders.dashboard)()
      if (seq !== pageLoadSeq) return
      PageComponent = mod.default
    } catch (e) {
      if (seq !== pageLoadSeq) return
      if (looksLikeStaleAssetError(e)) {
        pageLoadStale = true
        recoverFromStaleAssets()
      }
      pageLoadError = e?.message || `Could not load ${nextPage}.`
    }
  }

  async function restartGateway() {
    if (restarting) return
    restarting = true
    restartError = ''
    try {
      await api.admin.restart()
    } catch (e) {
      // The server exits ~250ms after responding, so the fetch itself may
      // fail with a network error even though the restart was accepted.
      // Only a real auth/permission error should stop us.
      if (e?.status === 401 || e?.status === 403) {
        restarting = false
        showRestartModal = false
        restartError = 'You are not authorized to restart the gateway.'
        return
      }
    }
    showRestartModal = false
    waitForGatewayBack()
  }

  // Poll /health until the re-exec'd gateway answers, then hard-reload the
  // SPA so every store/stream reconnects to the fresh process.
  async function waitForGatewayBack() {
    // Shared with the two upgrade buttons. This loop was the correct one all
    // along; the upgrade paths each had their own five-second guess instead,
    // which is how "upgrade successful" ended up sitting above "Failed to
    // fetch". One implementation now, so there is nothing left to drift.
    const outcome = await waitForGateway(api.health, {
      ...RESTART_BUDGET,
      onAttempt: (n, total) => { restartMessage = waitingMessage(n, total) },
    })
    if (outcome.ok) { location.reload(); return }
    restarting = false
    restartMessage = ''
    restartError = timeoutMessage('restart', outcome.waitedMs)
  }

  onMount(() => {
    const applyHash = () => {
      const h = location.hash.slice(1)
      const route = h.split('?')[0]
      // Public read-only shared conversation: /#share/<token>. Rendered outside
      // the app shell and the login gate, so it needs no API key.
      if (h.startsWith('share/')) { shareToken = h.slice('share/'.length); return }
      shareToken = ''
      if (retiredPages[route]) {
        navigate(retiredPages[route])
        return
      }
      if (route && (pages.find(p => p.id === route) || isPluginPage(route))) { page = route; return }
      // Path-based entry (ARCH-6): the SPA fallback serves index.html for any
      // unmatched path, so a deep link / refresh on e.g. /studio lands here
      // with an empty hash. Map the last path segment to a page id when it
      // matches a known route so /studio opens the Studio editor directly.
      if (!h) {
        const seg = location.pathname.replace(/\/+$/, '').split('/').pop()
        if (retiredPages[seg]) {
          navigate(retiredPages[seg])
          return
        }
        if (seg && pages.find(p => p.id === seg)) page = seg
        return
      }
      // A hash that names no screen we have. Falling through here used to
      // leave the PREVIOUS page mounted while the address bar showed the bad
      // route, so a stale bookmark or a renamed route put you on Browser Trace
      // with a URL claiming otherwise — and every in-page control, the tour
      // included, then spoke about a screen you were not looking at.
      //
      // isPluginPage is a prefix test, not a lookup, so an unknown route is
      // unambiguous even before the plugin mounts have loaded.
      //
      // replaceState, not pushState: the broken URL should not become a place
      // the back button can return to.
      page = 'dashboard'
      sidebarOpen = false
      history.replaceState({}, '', '#dashboard')
    }
    applyHash()
    try { navCollapsed = localStorage.getItem('soulacy-nav-collapsed') === '1' } catch (_) {}
    window.addEventListener('popstate', applyHash)
    window.addEventListener('hashchange', applyHash)

    // A public shared view needs no auth probe, no first-run redirect, and no
    // plugin nav — it renders standalone. Skip the rest of setup.
    if (shareToken) return

    // First-run: if the user landed on the default page with no explicit route
    // and setup isn't done yet, guide them into the wizard. One-shot — once
    // they've seen it (or completed setup) we never auto-redirect again.
    let setupWizardOpened = false
    let onboardingDecided = Promise.resolve()
    try {
      const seen = localStorage.getItem('soulacy-onboarding-seen') === '1'
      if (!seen && !location.hash && page === 'dashboard') {
        onboardingDecided = api.onboarding.status()
          .then((st) => {
            const provider = (st?.steps || []).find(s => s.key === 'provider')
            if (!st?.complete && provider && provider.status === 'todo') {
              setupWizardOpened = true
              navigate('onboarding')
            } else {
              localStorage.setItem('soulacy-onboarding-seen', '1')
            }
          })
          .catch(() => {}) // older gateway without onboarding status: skip silently
      }
    } catch (_) { /* localStorage unavailable — skip first-run redirect */ }

    // Orientation tour: auto-opens once per install. The setup wizard wins on a
    // brand-new install — being walked through 22 screens before you have a model
    // connected is the wrong first experience — so the tour waits for the next
    // load, by which point the wizard no longer auto-opens.
    Promise.all([onboardingDecided, loadWalkthroughState()])
      .then(([, state]) => {
        // Not while the login screen is up: the shell renders underneath it,
        // so a tour started here would drive navigation behind a modal the user
        // cannot see past.
        if (!setupWizardOpened && !$authRequired && shouldAutoStart(state)) {
          navCollapsed = false
          startWalkthrough(0)
        }
      })
      .catch(() => {})

    // Auth probe: hit an authenticated endpoint. apiFetch flips $authRequired
    // true on 401/403 (→ login screen) and false on success (→ dashboard).
    api.agents.list()
      .then(() => { $authRequired = false })
      .catch(() => {})
      .finally(() => { sessionChecked = true })

    // Plugin GUI mounts (E8): populate the Plugins nav group.
    api.plugins.ui()
      .then((res) => { pluginPages = pluginNavEntries(res?.mounts) })
      .catch(() => { pluginPages = [] }) // older gateways: no route, no nav group
  })

  // ── Login screen (shown full-screen while $authRequired) ──────────────────
  let loginKey = ''
  let loginError = ''
  let loginChecking = false

  async function submitLogin() {
    const key = loginKey.trim()
    if (!key || loginChecking) return
    loginChecking = true
    loginError = ''
    const prev = $apiKey
    try {
      const session = await api.auth.login(key)
      $apiKey = session.accessToken
      await api.agents.list() // validate the key
      // An expired session can leave the currently mounted page holding a
      // local auth error. Remount it after login so every screen starts from
      // the newly established browser session.
      PageComponent = null
      loadedPage = ''
      pageLoadError = ''
      sessionChecked = true
      $authRequired = false
      loginKey = ''
      api.plugins.ui()
        .then((res) => { pluginPages = pluginNavEntries(res?.mounts) })
        .catch(() => { pluginPages = [] })
    } catch (e) {
      $apiKey = prev          // never persist a rejected key
      loginError = (e && (e.status === 401 || e.status === 403))
        ? 'That key was rejected. Double-check it and try again.'
        : (e?.message || 'Could not reach the gateway. Is it running?')
    } finally {
      loginChecking = false
    }
  }

  async function saveKey() {
    const key = keyInput.trim()
    if (!key || keySaving) return
    keySaving = true
    keyError = ''
    try {
      const session = await api.auth.login(key)
      $apiKey = session.accessToken
      await api.agents.list()
      showKeyModal = false
      keyInput = ''
      window.location.reload()
    } catch (e) {
      keyError = (e && (e.status === 401 || e.status === 403))
        ? 'That key was rejected. Double-check it and try again.'
        : (e?.message || 'Could not create a browser session.')
    } finally {
      keySaving = false
    }
  }

  function openKeyModal() {
    keyInput = ''
    keyError = ''
    showKeyModal = true
  }
</script>

<!-- Public read-only shared conversation — rendered before (and instead of) the
     login gate and the app shell, so it needs no API key. -->
{#if shareToken}
  <ShareView token={shareToken} />
{:else if !sessionChecked}
  <div class="session-check" role="status" aria-live="polite">
    <BrandMark size={42} />
    <strong>Restoring your session…</strong>
  </div>
{:else if $authRequired}
  <PersonalLanding
    bind:loginKey
    {loginError}
    {loginChecking}
    on:login={submitLogin}
  />
{/if}

<!-- API Key modal -->
{#if showRestartModal}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close restart dialog"
    on:click|self={() => showRestartModal = false}
    on:keydown={(e) => e.key === 'Escape' && (showRestartModal = false)}
  >
    <div class="modal">
      <h2>Restart Gateway</h2>
      <p>This stops the running gateway and starts a fresh process. In-flight
         requests are dropped and the UI reconnects automatically once it's back
         (usually a few seconds).</p>
      <div class="modal-row">
        <button class="btn-secondary" on:click={() => showRestartModal = false}>Cancel</button>
        <button class="btn-danger" on:click={restartGateway}>Restart</button>
      </div>
    </div>
  </div>
{/if}

{#if restarting}
  <div class="restart-overlay" aria-live="polite">
    <div class="restart-card">
      <span class="restart-spinner" aria-hidden="true">⟳</span>
      <p>Restarting gateway…</p>
      <small>{restartMessage || 'Reconnecting as soon as the new process answers.'}</small>
    </div>
  </div>
{/if}

{#if restartError}
  <div class="modal-bg" role="button" tabindex="0" aria-label="Dismiss error"
       on:click|self={() => restartError = ''}
       on:keydown={(e) => e.key === 'Escape' && (restartError = '')}>
    <div class="modal">
      <h2>Restart</h2>
      <p>{restartError}</p>
      <div class="modal-row">
        <button class="btn-primary" on:click={() => restartError = ''}>OK</button>
      </div>
    </div>
  </div>
{/if}

{#if showKeyModal}
  <div
    class="modal-bg"
    role="button"
    tabindex="0"
    aria-label="Close API key modal"
    on:click|self={() => showKeyModal = false}
    on:keydown={(e) => e.key === 'Escape' && (showKeyModal = false)}
  >
    <div class="modal">
      <h2>API Key</h2>
      <p>Replace this browser's Soulacy login. The deployment key is exchanged for a secure session and is not saved in browser storage.</p>
      <input type="password" bind:value={keyInput} disabled={keySaving}
             placeholder="sy_…"
             on:keydown={(e) => e.key === 'Enter' && saveKey()} />
      {#if keyError}<p class="login-error" role="alert">{keyError}</p>{/if}
      <div class="modal-row">
        <button class="btn-secondary" on:click={() => showKeyModal = false} disabled={keySaving}>Cancel</button>
        <button class="btn-primary" on:click={saveKey} disabled={keySaving || !keyInput.trim()}>{keySaving ? 'Verifying…' : 'Save & Reload'}</button>
      </div>
    </div>
  </div>
{/if}

<svelte:window on:keydown={(e) => e.key === 'Escape' && (sidebarOpen = false)} />

{#if !shareToken && sessionChecked && !$authRequired}
<div class="layout" class:chat-route={page === 'chat'}>
  <!-- Mobile command bar. The current destination and workspace remain visible
       even when the full navigation is off canvas. -->
  <header class="topbar">
    <button class="hamburger" on:click={() => sidebarOpen = !sidebarOpen}
            aria-label={sidebarOpen ? 'Close navigation' : 'Open navigation'} aria-expanded={sidebarOpen}>
      <span aria-hidden="true">{sidebarOpen ? '×' : '☰'}</span>
    </button>
    <BrandMark size={28} alt="Soulacy" />
    <div class="mobile-context">
      <strong>{currentPageLabel}</strong>
      <span>{currentWorkspaceLabel || 'Soulacy'}</span>
    </div>
    <span class="mobile-connection" class:live={$connected} aria-label={$connected ? 'Connected' : 'Reconnecting'}></span>
  </header>

  <!-- Backdrop behind the mobile drawer -->
  {#if sidebarOpen}
    <button class="backdrop" aria-label="Close navigation" on:click={() => sidebarOpen = false}></button>
  {/if}

  <!-- Sidebar -->
  <aside class="sidebar" class:open={sidebarOpen} class:collapsed={navCollapsed}>
    <div class="brand">
      <span class="brand-logo" aria-hidden="true">
        <BrandMark size={30} />
      </span>
      <span class="brand-name">soulacy</span>
      <button class="nav-toggle" on:click={toggleNav}
              title={navCollapsed ? 'Expand menu' : 'Collapse menu'}
              aria-label={navCollapsed ? 'Expand menu' : 'Collapse menu'}>
        {navCollapsed ? '»' : '«'}
      </button>
      <button class="drawer-close" on:click={() => sidebarOpen = false} aria-label="Close navigation">×</button>
    </div>


    <nav>
      {#each navGroups as grp}
        {@const groupPages = pages.filter(p => p.group === grp.key)}
        {#if groupPages.length}
          {#if grp.label}<div class="nav-section" aria-hidden="true">{grp.label}</div>{/if}
          {#each groupPages as p}
            <button class="nav-item" class:active={page === p.id} on:click={() => navigate(p.id)} title={p.label}
                    aria-current={page === p.id ? 'page' : undefined} data-tour={navAnchor(p.id)}>
              <span class="nav-icon">{p.icon}</span>
              <span class="nav-label">{p.label}</span>
            </button>
          {/each}
        {/if}
      {/each}
      {#if pluginPages.length > 0}
        <div class="nav-section" aria-hidden="true">Plugins</div>
        {#each pluginPages as p}
          <button class="nav-item" class:active={page === p.id} on:click={() => navigate(p.id)} title={p.label}>
            <span class="nav-icon">{p.icon}</span>
            <span class="nav-label">{p.label}</span>
          </button>
        {/each}
      {/if}
    </nav>

    <button class="nav-item nav-action" on:click={openRestartModal}
            title="Restart the gateway server">
      <span class="nav-icon restart-dot">●</span>
      <span class="nav-label">Restart Gateway</span>
    </button>

    <div class="sidebar-footer">
      {#if $authRequired}
        <button class="conn-dot auth-required" on:click={openKeyModal}
                title="The gateway rejected your API key — click to set it">
          🔒 Authentication required
        </button>
      {:else}
        <span class="conn-dot" class:live={$connected} title={$connected ? 'Event stream live' : 'Disconnected from event stream'}>
          {$connected ? '● Live' : '○ Offline'}
        </span>
      {/if}
      <button class="icon-btn" on:click={openKeyModal} title="Change browser login">🔑</button>
    </div>
  </aside>

  {#if !$authRequired}
    <Walkthrough on:navigate={(e) => navigate(e.detail)} />
  {/if}

  <!-- Main content -->
  <main class="content">
    {#if pageLoadError}
      <div class="page-loading err">
        <strong>{pageLoadStale ? 'Soulacy updated while this tab was open.' : 'Could not load this page.'}</strong>
        <span>{pageLoadError}</span>
        {#if pageLoadStale}
          <button class="btn-primary" on:click={() => recoverFromStaleAssets({ force: true })}>Reload fresh UI</button>
        {/if}
      </div>
    {:else if PageComponent && isPluginPage(page)}
      {@const mount = pluginPages.find(p => p.id === page)}
      <svelte:component
        this={PageComponent}
        pluginId={pluginIdFromPage(page)}
        label={mount?.label || pluginIdFromPage(page)}
        url={mount?.url || ''}
      />
    {:else if PageComponent}
      <svelte:component this={PageComponent} />
    {:else}
      <div class="page-loading">Loading {pageTitle(page, pages, pluginPages)}…</div>
    {/if}
  </main>

  <nav class="mobile-tabs" aria-label="Primary navigation">
    {#each mobilePrimaryPages as p}
      <button class:active={page === p.id} on:click={() => navigate(p.id)} aria-current={page === p.id ? 'page' : undefined}>
        <span aria-hidden="true">{p.icon}</span><small>{p.label}</small>
      </button>
    {/each}
    <button class:active={mobileMoreActive} on:click={() => sidebarOpen = true} aria-expanded={sidebarOpen}>
      <span aria-hidden="true">•••</span><small>More</small>
    </button>
  </nav>
</div>
{/if}


<style>
  /* ── Reset & globals ────────────────────────────────────────────── */
  :global(*, *::before, *::after) { box-sizing: border-box; margin: 0; padding: 0; }
  :global(html, body) { height: 100%; }
  :global(html) { -webkit-text-size-adjust: 100%; text-size-adjust: 100%; }
  :global(body) {
    background: #0c0e1a;
    color: #e8eaf6;
    font-family: 'Inter', system-ui, -apple-system, sans-serif;
    font-size: 14px;
    line-height: 1.5;
  }

  .session-check {
    min-height: 100dvh;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.85rem;
    color: #a9afcf;
    background: radial-gradient(circle at 50% 35%, rgba(92, 79, 196, 0.14), transparent 34%), #0c0e1a;
  }

  /* ── Form elements ──────────────────────────────────────────────── */
  :global(input:not([type="radio"]):not([type="checkbox"])), :global(textarea), :global(select) {
    background: #1c1f35;
    border: 1px solid #2a2f4a;
    border-radius: 6px;
    color: #e8eaf6;
    font-size: 14px;
    padding: 0.45rem 0.75rem;
    outline: none;
    width: 100%;
    transition: border-color 0.15s;
  }
  :global(input[type="radio"]), :global(input[type="checkbox"]) {
    width: auto;
    margin: 0;
    flex-shrink: 0;
    cursor: pointer;
  }
  :global(input:focus), :global(textarea:focus), :global(select:focus) {

    border-color: #6c63ff;
    box-shadow: 0 0 0 2px rgba(108, 99, 255, 0.15);
  }
  :global(input:disabled), :global(textarea:disabled), :global(select:disabled) {
    opacity: 0.5; cursor: not-allowed;
  }
  :global(textarea) { resize: vertical; }
  /* Radios and checkboxes are native controls, not text fields. The rule above
     (width:100% + padding + border + background) turns them into padded boxes
     that shrink to a different width per row inside flex layouts, throwing off
     alignment (e.g. the Studio model modal preset rows). Opt them out so they
     render at their intrinsic size and line up. */
  :global(input[type="radio"]), :global(input[type="checkbox"]) {
    width: auto;
    flex: none;
    padding: 0;
    border: none;
    border-radius: 0;
    background: none;
  }

  /* ── Buttons ────────────────────────────────────────────────────── */
  :global(button) { cursor: pointer; border: none; font-size: 14px; transition: background 0.15s, opacity 0.15s; }
  :global(button:disabled) { opacity: 0.5; cursor: not-allowed; }

  /* `.btn` is used across pages AND their child components, so it has to be
     global. Svelte scopes styles per component, so a `.btn` defined inside
     Studio.svelte does not reach a panel Studio renders — the child's buttons
     fall back to bare `:global(button)` and render as plain text with a hairline
     box. That is how "Refine prompt" and "Generate workflow" — the primary
     action on the Describe step — ended up looking like table cells.
     Scoped definitions still win on specificity, so pages that already style
     .btn themselves are unaffected. */
  :global(.btn) {
    flex: 0 0 auto;
    padding: 9px 16px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: border-color 0.12s ease, background 0.12s ease;
  }
  :global(.btn:hover:not(:disabled)) { border-color: var(--accent); }
  :global(.btn.primary) { background: var(--accent); border-color: var(--accent); color: #fff; }
  :global(.btn.primary:hover:not(:disabled)) { filter: brightness(1.08); }
  :global(.btn:disabled) { opacity: 0.5; cursor: not-allowed; }
  /* Compact variant used in dense panels (library rows, readiness items). */
  :global(.btn-sm) { padding: 4px 10px; font-size: 12px; font-weight: 600; }

  :global(.btn-primary) {
    background: #6c63ff; color: #fff;
    padding: 0.45rem 1.1rem; border-radius: 6px; font-weight: 500;
  }
  :global(.btn-primary:hover:not(:disabled)) { background: #5b52ef; }

  :global(.btn-secondary) {
    background: #1c1f35; color: #e8eaf6;
    border: 1px solid #2a2f4a;
    padding: 0.45rem 1.1rem; border-radius: 6px;
  }
  :global(.btn-secondary:hover:not(:disabled)) { background: #252840; }

  :global(.btn-danger) {
    background: #7f2020; color: #fff;
    padding: 0.45rem 1.1rem; border-radius: 6px;
  }
  :global(.btn-danger:hover:not(:disabled)) { background: #9f2828; }

  /* ── Layout ─────────────────────────────────────────────────────── */
  .layout { display: flex; height: 100vh; height: 100dvh; min-width: 0; overflow: hidden; }

  /* ── Mobile top bar + drawer (≤768px) ───────────────────────────── */
  .topbar { display: none; }
  .backdrop { display: none; }
  .mobile-tabs { display: none; }
  .drawer-close { display: none; }

  /* ── Sidebar ─────────────────────────────────────────────────────── */
  .sidebar {
    width: 210px; flex-shrink: 0;
    background: #0e1020;
    border-right: 1px solid #1a1e36;
    display: flex; flex-direction: column;
    transition: width 0.16s ease;
  }

  /* Collapsed icon rail (desktop): labels hidden, icons centered. */
  .sidebar.collapsed { width: 56px; }
  .sidebar.collapsed .brand-name,
  .sidebar.collapsed .nav-label,
  .sidebar.collapsed .conn-dot,
  .sidebar.collapsed .nav-section { display: none; }
  .sidebar.collapsed .brand { justify-content: center; padding: 1.1rem 0; gap: 0; position: relative; }
  .sidebar.collapsed .nav-item { justify-content: center; padding: 0.6rem 0; gap: 0; }
  .sidebar.collapsed .nav-icon { width: auto; }
  .sidebar.collapsed .sidebar-footer { justify-content: center; padding: 0.65rem 0; }
  /* When collapsed, the toggle sits under the logo as a small centered pill. */
  .sidebar.collapsed .nav-toggle { position: absolute; bottom: -6px; right: 6px; }

  .nav-toggle {
    margin-left: auto; background: none; border: none;
    color: #6b7294; font-size: 0.9rem; line-height: 1;
    padding: 0.2rem 0.35rem; border-radius: 6px; cursor: pointer;
  }
  .nav-toggle:hover { background: #181b30; color: #c8cadf; }

  @media (max-width: 768px) {
    .layout { flex-direction: column; }

    /* Chat owns its navigation bar on phones. Keeping the shell command bar as
       well produced a web-dashboard-style double header; the More tab remains
       the entry point to the full application drawer. */
    .layout.chat-route .topbar { display: none; }

    .topbar {
      display: flex; align-items: center; gap: 0.55rem;
      min-height: calc(56px + env(safe-area-inset-top));
      padding: calc(0.4rem + env(safe-area-inset-top)) max(0.65rem, env(safe-area-inset-right)) 0.4rem max(0.65rem, env(safe-area-inset-left));
      background: #0e1020;
      border-bottom: 1px solid #1a1e36;
      flex-shrink: 0;
    }
    .hamburger {
      background: none; color: #c8cadf;
      width: 44px; height: 44px; display: grid; place-items: center;
      font-size: 1.35rem; line-height: 1; padding: 0; border-radius: 11px;
    }
    .hamburger:hover { background: #181b30; }
    .mobile-context { min-width: 0; display: grid; flex: 1; line-height: 1.2; }
    .mobile-context strong { overflow: hidden; color: #f2f3fb; font-size: .9rem; text-overflow: ellipsis; white-space: nowrap; }
    .mobile-context span { overflow: hidden; color: #737c9e; font-size: .66rem; text-overflow: ellipsis; white-space: nowrap; }
    .mobile-connection { width: 8px; height: 8px; flex: 0 0 8px; margin-right: .25rem; border-radius: 50%; background: #9d5260; box-shadow: 0 0 0 4px rgba(157,82,96,.1); }
    .mobile-connection.live { background: #59d7b0; box-shadow: 0 0 0 4px rgba(89,215,176,.1); }

    /* Sidebar becomes an off-canvas drawer */
    .sidebar {
      position: fixed; top: 0; bottom: 0; left: 0;
      width: min(320px, 88vw);
      padding-top: env(safe-area-inset-top);
      padding-bottom: env(safe-area-inset-bottom);
      transform: translateX(-105%);
      transition: transform 0.22s cubic-bezier(.2,.8,.2,1);
      z-index: 90;
      box-shadow: 4px 0 24px rgba(0, 0, 0, 0.5);
    }
    .sidebar.open { transform: translateX(0); }
    .sidebar.collapsed { width: min(320px, 88vw); }
    .sidebar.collapsed .brand-name, .sidebar.collapsed .nav-label, .sidebar.collapsed .conn-dot,
    .sidebar.collapsed .logout-label, .sidebar.collapsed .nav-section { display: initial; }
    .sidebar.collapsed .brand { justify-content: initial; padding: 1.15rem 1.1rem; gap: .7rem; }
    .sidebar.collapsed .nav-item { padding: .75rem 1rem; gap: .75rem; }
    .sidebar.collapsed .nav-icon { width: 1.2rem; }
    .sidebar.collapsed .sidebar-footer { justify-content: space-between; padding: .65rem 1rem; }
    .nav-toggle { display: none; }
    .drawer-close { display: grid; place-items: center; width: 44px; height: 44px; margin-left: auto; border-radius: 11px; color: #9ca4c4; background: transparent; font-size: 1.5rem; }
    .drawer-close:hover { color: #fff; background: #181b30; }

    .backdrop {
      display: block;
      position: fixed; inset: 0;
      background: rgba(0, 0, 0, 0.55);
      z-index: 80;
      width: 100%; border: none; border-radius: 0;
    }

    .sidebar nav { padding-inline: .65rem; overscroll-behavior: contain; }
    .nav-item { min-height: 48px; padding: 0.75rem 1rem; }
    .sidebar-footer { min-height: 58px; }

    .content { min-width: 0; min-height: 0; overscroll-behavior: contain; -webkit-overflow-scrolling: touch; }
    .mobile-tabs {
      display: grid; grid-template-columns: repeat(auto-fit, minmax(54px, 1fr)); flex: 0 0 auto;
      min-height: calc(58px + env(safe-area-inset-bottom));
      padding: .3rem max(.35rem, env(safe-area-inset-right)) calc(.3rem + env(safe-area-inset-bottom)) max(.35rem, env(safe-area-inset-left));
      border-top: 1px solid rgba(98,108,148,.2); background: rgba(12,15,29,.92);
      box-shadow: 0 -10px 30px rgba(2,5,14,.24); backdrop-filter: blur(18px); -webkit-backdrop-filter: blur(18px);
    }
    .mobile-tabs button { min-width: 0; min-height: 48px; display: grid; place-items: center; align-content: center; gap: .12rem; border-radius: 10px; color: #777f9f; background: transparent; }
    .mobile-tabs button > span { font-size: 1.05rem; line-height: 1; }
    .mobile-tabs small { max-width: 100%; overflow: hidden; font-size: .62rem; font-weight: 650; text-overflow: ellipsis; white-space: nowrap; }
    .mobile-tabs button.active { color: #c4c0ff; background: rgba(108,99,255,.12); }

    :global(input:not([type="radio"]):not([type="checkbox"])), :global(textarea), :global(select) { font-size: 16px !important; }
    :global(.modal-bg), :global(.dialog-backdrop) { padding: max(.75rem, env(safe-area-inset-top)) max(.75rem, env(safe-area-inset-right)) max(.75rem, env(safe-area-inset-bottom)) max(.75rem, env(safe-area-inset-left)); }
    :global(.table-wrap) { max-width: 100%; overflow-x: auto; -webkit-overflow-scrolling: touch; }
    :global([data-tooltip]::after) { display: none; }
  }

  /* App-wide responsive defaults for page content */
  @media (max-width: 768px) {
    :global(.page) { min-width: 0; padding: 1rem !important; }
    :global(.page-header) { flex-wrap: wrap; gap: 0.6rem; row-gap: 0.6rem; }
    :global(.page-header h1) { font-size: 1.15rem; }
  }

  .brand {
    display: flex; align-items: center; gap: 0.7rem;
    padding: 1.15rem 1.1rem;
  }
  /* The same Living Core artwork used by the native app. */
  .brand-logo {
    width: 30px; height: 30px; flex-shrink: 0;
    display: flex; align-items: center; justify-content: center;
  }
  .brand-name { font-weight: 750; font-size: 1.15rem; letter-spacing: -0.04em; color: #f2f3fb; }


  nav { flex: 1; padding: 0.5rem 0.5rem; overflow-y: auto; }
  /* Uppercase section header (CAPABILITIES / INTEGRATIONS / …). */
  .nav-section {
    padding: 0.9rem 0.65rem 0.4rem;
    font-size: 0.66rem; font-weight: 600; letter-spacing: 0.09em;
    text-transform: uppercase; color: #565c82;
  }
  .nav-item {
    display: flex; align-items: center; gap: 0.75rem;
    width: 100%; padding: 0.55rem 0.65rem; margin: 0.05rem 0;
    background: none; color: #8188ad;
    font-size: 0.9rem; font-weight: 500;
    text-align: left; border-radius: 9px;
    transition: background 0.1s, color 0.1s;
  }
  .nav-item:hover  { background: #181b30; color: #d4d7ea; }
  .nav-item.active { background: rgba(108, 99, 255, 0.16); color: #b3adff; }
  .nav-icon { font-size: 1rem; width: 1.2rem; text-align: center; }

  /* Action item (not a page): restart the gateway — pinned at the bottom. */
  .nav-action { margin: 0.35rem 0.5rem 0.6rem; width: auto; color: #d98a8a; border-radius: 9px; }
  .nav-action:hover { background: rgba(127, 32, 32, 0.18); color: #ff9d9d; }
  .restart-dot { color: #e06666; font-size: 0.7rem; }

  /* Blocking overlay shown while the gateway re-execs. */
  .restart-overlay {
    position: fixed; inset: 0; z-index: 1000;
    display: flex; align-items: center; justify-content: center;
    background: rgba(8, 10, 20, 0.82);
    backdrop-filter: blur(3px);
  }
  .restart-card {
    text-align: center; color: #e8eaf6;
    background: #14172a; border: 1px solid #2a2f4a;
    border-radius: 12px; padding: 1.75rem 2.25rem;
    box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
  }
  .restart-card p { margin: 0.6rem 0 0.25rem; font-weight: 500; }
  .restart-card small { color: #8b8fa8; }
  .restart-spinner {
    display: inline-block; font-size: 1.8rem; color: #8b85ff;
    animation: restart-spin 1s linear infinite;
  }
  @keyframes restart-spin { to { transform: rotate(360deg); } }

  .sidebar-footer {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.65rem 1rem;
    border-top: 1px solid #1a1e36;
  }
  .conn-dot { font-size: 0.72rem; font-family: monospace; color: #5a3030; }
  .conn-dot.live { color: #4caf82; }
  .conn-dot.auth-required {
    background: none; color: #f0a060; padding: 0;
    font-size: 0.72rem; font-family: monospace; text-align: left;
  }
  .conn-dot.auth-required:hover { color: #ffc08a; text-decoration: underline; }
  .icon-btn { background: none; color: #6b7294; font-size: 0.85rem; padding: 0.15rem; }
  .icon-btn:hover { color: #e8eaf6; }

  /* ── Main content ────────────────────────────────────────────────── */
  .content { flex: 1; overflow-y: auto; display: flex; flex-direction: column; }
  .page-loading {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    color: #8f96bd;
    font-size: 0.9rem;
    letter-spacing: 0;
  }
  .page-loading.err {
    color: #ff8c8c;
    padding: 2rem;
    text-align: center;
  }

  /* ── Modal ───────────────────────────────────────────────────────── */
  .modal-bg {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.65);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 420px; max-width: 92vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal h2 { font-size: 1rem; font-weight: 600; }
  .modal p  { color: #7b82a8; font-size: 0.85rem; line-height: 1.6; }
  .modal p code { background: #1c1f35; padding: 0.1rem 0.35rem; border-radius: 4px; font-size: 0.8rem; }
  .modal-row { display: flex; gap: 0.75rem; justify-content: flex-end; }

  /* ── Custom Tooltips ────────────────────────────────────────────────────── */
  :global([data-tooltip]) {
    position: relative;
  }
  :global([data-tooltip]::after) {
    content: attr(data-tooltip);
    position: absolute;
    bottom: 125%;
    left: 50%;
    transform: translateX(-50%) scale(0.95);
    background: #0f1123;
    color: #dfe2ff;
    padding: 6px 10px;
    border-radius: 6px;
    border: 1px solid rgba(126, 92, 255, 0.4);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
    font-size: 11px;
    font-family: 'Inter', sans-serif;
    white-space: nowrap;
    opacity: 0;
    pointer-events: none;
    transition: opacity 0.12s ease, transform 0.12s ease;
    z-index: 10000;
  }
  :global([data-tooltip]:hover::after) {
    opacity: 1;
    transform: translateX(-50%) scale(1);
  }
</style>
