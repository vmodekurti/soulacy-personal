<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { searchSkills } from '../lib/skillsearch.js'

  let skills        = []
  let selected      = null
  let loading       = true
  let detailLoading = false
  let error         = ''

  // Filter over INSTALLED skills. Distinct from the registry search further up
  // the page, which looks for skills you do not have yet.
  let filterQ = ''
  $: visibleSkills = searchSkills(skills, filterQ)

  // AgenticSkills provisioner modal
  let asModal    = false
  let asURL      = ''
  let asFetching = false
  let asError    = ''
  let asSuccess  = ''

  // Skill sources modal (Story E26: review a URL → add as registry source)
  let srcModal   = false
  let srcURL     = ''
  let srcAuthToken = ''
  let srcBusy    = false
  let srcError   = ''
  let srcSuccess = ''
  let srcReport  = null   // pkgregistry.ProbeReport
  let sources    = []     // configured registries
  let findQ      = ''
  let findBusy   = false
  let findResults = null  // packages from /registries/search
  let findWarnings = []
  let findChecked = []
  let findSuggestions = []
  let findStatus = ''
  let allowUnverifiedInstall = false
  let installingSlug = ''
  let installResult = ''
  let installError = ''
  let marketplace = null
  let marketplaceError = ''

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.skills.list()
      skills = res.skills || []
      await loadMarketplace()
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function loadMarketplace() {
    marketplaceError = ''
    try {
      marketplace = await api.marketplace.status()
    } catch (e) {
      marketplaceError = e.message
    }
  }

  async function select(sk) {
    if (selected?.name === sk.name) {
      selected = null
      return
    }
    detailLoading = true
    selected = sk
    try {
      const full = await api.skills.get(sk.name)
      selected = full
    } catch (e) {
      error = e.message
    } finally {
      detailLoading = false
    }
  }

  function openASModal() {
    asURL     = ''
    asError   = ''
    asSuccess = ''
    asModal   = true
  }
  function closeASModal() { asModal = false }

  async function installFromAS() {
    if (!asURL.trim()) return
    asFetching = true
    asError    = ''
    asSuccess  = ''
    try {
      const res = await api.skills.provisionAgenticSkills({ url: asURL.trim() })
      if (res.ok) {
        asSuccess = res.message || `Skill installed.`
        setTimeout(() => { closeASModal(); load() }, 1800)
      } else {
        asError = res.error || 'Install failed.'
      }
    } catch (e) {
      asError = e.message
    } finally {
      asFetching = false
    }
  }

  async function openSrcModal() {
    srcURL = ''; srcAuthToken = ''; srcError = ''; srcSuccess = ''; srcReport = null
    installResult = ''; installError = ''; installingSlug = ''
    srcModal = true
    try {
      const res = await api.registries.list()
      sources = res.registries || []
    } catch { sources = [] }
  }
  function closeSrcModal() { srcModal = false }

  async function probeSource() {
    if (!srcURL.trim()) return
    srcBusy = true; srcError = ''; srcSuccess = ''; srcReport = null
    try {
      srcReport = await api.registries.probe(srcURL.trim())
    } catch (e) {
      srcError = e.message
    } finally {
      srcBusy = false
    }
  }

  async function addSource() {
    if (!srcReport?.suggested) return
    srcBusy = true; srcError = ''
    try {
      const s = srcReport.suggested
      const auth = registryAuthHeaders(srcAuthToken)
      const res = await api.registries.add({
        id: s.ID || s.id, type: s.Type || s.type,
        base_url: s.BaseURL || s.base_url || '', priority: s.Priority || s.priority || 0,
        ...(auth ? { auth_headers: auth } : {}),
      })
      srcSuccess = res.message || 'Source saved.'
      const list = await api.registries.list()
      sources = list.registries || []
      srcReport = null
      srcURL = ''
    } catch (e) {
      srcError = e.message
    } finally {
      srcBusy = false
    }
  }

  function registryAuthHeaders(token) {
    token = String(token || '').trim()
    if (!token) return null
    return { Authorization: /^Bearer\s+/i.test(token) ? token : `Bearer ${token}` }
  }

  async function findSkills() {
    if (!findQ.trim()) return
    findBusy = true; srcError = ''; installError = ''; installResult = ''
    findResults = null; findWarnings = []; findChecked = []; findSuggestions = []; findStatus = ''
    try {
      const res = await api.registries.search(findQ.trim())
      findResults = res.packages || []
      findWarnings = res.warnings || []
      findChecked = res.checked || []
      findSuggestions = res.suggestions || []
      findStatus = res.status || ''
    } catch (e) {
      srcError = e.message
    } finally {
      findBusy = false
    }
  }

  async function installSkill(pkg) {
    if (!pkg?.slug || pkg.provider === 'local' || pkg.manifest?.installed) return
    installingSlug = pkg.slug
    installError = ''
    installResult = ''
    try {
      const res = await api.skills.install({
        slug: pkg.slug,
        allow_unverified: allowUnverifiedInstall,
      })
      installResult = res.message || `Installed ${pkg.slug}.`
      await load()
    } catch (e) {
      if (e.body?.code === 'unverified_package') {
        installError = [e.body.error, e.body.remedy].filter(Boolean).join(' ')
      } else if (e.body?.code === 'danger_verdict') {
        installError = `${e.message}: ${securitySummary(e.body.security_report)}`
      } else {
        installError = e.message
      }
    } finally {
      installingSlug = ''
    }
  }

  async function installDirectQuery() {
    const slug = findQ.trim()
    if (!slug) return
    await installSkill({ slug, provider: 'direct' })
  }

  function securitySummary(report) {
    const findings = report?.findings || []
    if (!findings.length) return 'No detailed findings were returned.'
    return findings.slice(0, 3).map((f) => f.message || f.title || f.kind || 'security finding').join('; ')
  }

  const kindLabels = {
    skillssh: '📚 Skill directory (skills.sh-compatible)',
    http:     '📦 Soulacy package registry',
    git:      '🐙 Git host',
    unknown:  '❓ Not a recognisable registry',
  }

  $: authWarning = (findWarnings || []).find((w) => /authentication_required|requires authentication|VERCEL_OIDC_TOKEN|401|unauthorized/i.test(String(w)))
  $: noResultMessage = authWarning
    ? 'The search source answered, but it requires authentication before it can return skills.'
    : (findWarnings.length > 0 ? 'No installable results were returned by the available sources.' : 'No skills matched.')

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <h1>Skills</h1>
    <div class="header-actions">
      <button class="btn-as" on:click={openSrcModal}>➕ Skill sources</button>
      <button class="btn-as" on:click={openASModal}>⚡ From AgenticSkills</button>
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
    </div>
        <TourButton />
    </div>

  <!-- Skill sources modal (Story E26) -->
  {#if srcModal}
    <div
      class="modal-backdrop"
      role="button"
      tabindex="0"
      aria-label="Close skill sources modal"
      on:click|self={closeSrcModal}
      on:keydown={(e) => e.key === 'Escape' && closeSrcModal()}
    >
      <div class="modal">
        <div class="modal-header">
          <h2>Skill sources</h2>
          <button class="modal-close" on:click={closeSrcModal}>✕</button>
        </div>

        {#if sources.length > 0}
          <div class="src-list">
            {#each sources as s (s.id)}
              <div class="src-row">
                <span class="src-id">{s.id}</span>
                <span class="src-type">{s.type}</span>
                {#if s.base_url}<span class="src-url">{s.base_url}</span>{/if}
                {#if s.has_auth}<span class="src-auth" title="auth headers configured">🔑</span>{/if}
                {#if s.signing_key}<span class="src-auth" title="signature verification configured">✓ signed</span>{/if}
              </div>
            {/each}
          </div>
        {:else}
          <p class="as-hint">No sources configured yet.</p>
        {/if}

        <div class="src-probe-row">
          <input
            class="as-input"
            type="text"
            aria-label="Search skills"
            placeholder="Search skills across your sources (skills.sh built in)…"
            bind:value={findQ}
            disabled={findBusy}
            on:keydown={(e) => e.key === 'Enter' && findSkills()}
          />
          <button class="btn-as" on:click={findSkills} disabled={findBusy || !findQ.trim()}>
            {findBusy ? 'Searching…' : '🔎 Find skills'}
          </button>
        </div>
        {#if findResults}
          <div class="src-report">
            {#if findChecked.length > 0}
              <p class:src-degraded={findStatus === 'degraded'}>
                Checked {findChecked.join(', ')}{findStatus === 'degraded' ? ' — at least one source needs attention.' : '.'}
              </p>
            {/if}
            {#if findWarnings.length > 0}
              <div class="src-warnings">
                {#each findWarnings as warning}
                  <p>{warning}</p>
                {/each}
              </div>
            {/if}
            {#if findSuggestions.length > 0}
              <div class="src-suggestions">
                {#each findSuggestions as suggestion}
                  <p>{suggestion}</p>
                {/each}
              </div>
            {/if}
            {#if findResults.length === 0}
              <p>{noResultMessage}</p>
              {#if authWarning}
                <div class="src-auth-help">
                  The public skills.sh API requires a Vercel OIDC bearer token. Add an auth header for the source, or set <code>VERCEL_OIDC_TOKEN</code> before starting Soulacy. You can also paste an exact GitHub source or full registry slug and try direct install below.
                </div>
              {/if}
              <label class="unverified-toggle">
                <input type="checkbox" bind:checked={allowUnverifiedInstall} />
                <span>Allow unverified installs from sources I trust</span>
              </label>
              {#if installError}<div class="as-err">{installError}</div>{/if}
              {#if installResult}<div class="as-ok">✓ {installResult}</div>{/if}
              <div class="direct-install">
                <code>{findQ.trim()}</code>
                <button class="btn-install" on:click={installDirectQuery} disabled={installingSlug === findQ.trim()}>
                  {installingSlug === findQ.trim() ? 'Installing…' : 'Try direct install'}
                </button>
              </div>
            {:else}
              <label class="unverified-toggle">
                <input type="checkbox" bind:checked={allowUnverifiedInstall} />
                <span>Allow unverified installs from sources I trust</span>
              </label>
              {#if installError}<div class="as-err">{installError}</div>{/if}
              {#if installResult}<div class="as-ok">✓ {installResult}</div>{/if}
              {#each findResults.slice(0, 10) as pkg}
                <div class="find-row">
                  <code>{pkg.slug}</code>
                  {#if pkg.description}<span class="find-desc">{pkg.description}</span>{/if}
                  {#if pkg.provider === 'local' || pkg.manifest?.installed}
                    <span class="find-install installed">already installed</span>
                  {:else}
                    <button class="btn-install" on:click={() => installSkill(pkg)} disabled={installingSlug === pkg.slug}>
                      {installingSlug === pkg.slug ? 'Installing…' : 'Install'}
                    </button>
                  {/if}
                </div>
              {/each}
            {/if}
          </div>
        {/if}

        <p class="as-hint">
          Paste any URL — a skill directory like <strong>skills.sh</strong>, a Soulacy
          registry, or a GitHub host — and Review will identify it and suggest the
          config entry. Installs always run the local safety pipeline.
        </p>

        <div class="src-probe-row">
          <input
            class="as-input"
            type="url"
            aria-label="Source URL"
            placeholder="https://www.skills.sh/"
            bind:value={srcURL}
            disabled={srcBusy}
            on:keydown={(e) => e.key === 'Enter' && probeSource()}
          />
          <button class="btn-as" on:click={probeSource} disabled={srcBusy || !srcURL.trim()}>
            {srcBusy ? 'Reviewing…' : '🔍 Review'}
          </button>
        </div>
        <label class="src-auth-input">
          <span>Auth token <em>optional</em></span>
          <input
            class="as-input"
            type="password"
            aria-label="Registry auth token"
            placeholder="Paste Vercel OIDC token or Bearer token for protected sources"
            bind:value={srcAuthToken}
            disabled={srcBusy}
          />
        </label>

        {#if srcError}<div class="as-err">{srcError}</div>{/if}
        {#if srcSuccess}<div class="as-ok">✓ {srcSuccess}</div>{/if}

        {#if srcReport}
          <div class="src-report">
            <div class="src-kind">{kindLabels[srcReport.kind] || srcReport.kind}</div>
            <p>{srcReport.detail}</p>
            {#if srcReport.has_audits}
              <p class="src-audits">🛡 Publishes third-party security audits (shown at install consent).</p>
            {/if}
            {#if (srcReport.samples || []).length > 0}
              <div class="src-samples">
                {#each srcReport.samples.slice(0, 6) as smp}
                  <code>{smp}</code>
                {/each}
              </div>
            {/if}
            {#if srcReport.suggested}
              <div class="modal-footer">
                <button class="btn-as" on:click={addSource} disabled={srcBusy}>
                  ➕ Add "{srcReport.suggested.ID || srcReport.suggested.id}" as a source
                </button>
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- AgenticSkills provisioner modal -->
  {#if asModal}
    <div
      class="modal-backdrop"
      role="button"
      tabindex="0"
      aria-label="Close AgenticSkills modal"
      on:click|self={closeASModal}
      on:keydown={(e) => e.key === 'Escape' && closeASModal()}
    >
      <div class="modal">
        <div class="modal-header">
          <h2>Install from AgenticSkills</h2>
          <button class="modal-close" on:click={closeASModal}>✕</button>
        </div>

        <p class="as-hint">
          Paste an <strong>agenticskills.io</strong> skill URL and click Install.
          The SKILL.md will be downloaded and hot-loaded — no restart needed.
        </p>

        <input
          class="as-input"
          type="url"
          aria-label="AgenticSkills skill URL"
          placeholder="https://agenticskills.io/skills/frontend-design"
          bind:value={asURL}
          disabled={asFetching}
          on:keydown={(e) => e.key === 'Enter' && installFromAS()}
        />

        {#if asError}
          <div class="as-err">{asError}</div>
        {/if}
        {#if asSuccess}
          <div class="as-ok">✓ {asSuccess}</div>
        {/if}

        <div class="modal-footer">
          <button class="btn-secondary" on:click={closeASModal} disabled={asFetching}>Cancel</button>
          <button class="btn-as" on:click={installFromAS} disabled={asFetching || !asURL.trim()}>
            {asFetching ? 'Installing…' : '⚡ Install'}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if error}
    <div class="banner err">{error}</div>
  {/if}
  {#if marketplaceError}
    <div class="banner err">Marketplace readiness unavailable: {marketplaceError}</div>
  {:else if marketplace}
    <section class="marketplace-card {marketplace.status}">
      <div class="marketplace-score">
        <strong>{marketplace.score}</strong>
        <span>ecosystem score</span>
      </div>
      <div class="marketplace-main">
        <div class="marketplace-title">
          <h2>Skill ecosystem readiness</h2>
          <p>
            {marketplace.installed_skills} installed · {marketplace.registries} source{marketplace.registries === 1 ? '' : 's'} · {marketplace.searchable_sources} searchable · {marketplace.signed_sources} signed
          </p>
        </div>
        <div class="marketplace-checks">
          {#each marketplace.checks || [] as check}
            <span class={check.status} title={check.detail}>{check.label}</span>
          {/each}
        </div>
        {#if marketplace.next_actions?.length}
          <div class="marketplace-next">
            <strong>Next:</strong>
            <span>{marketplace.next_actions[0]}</span>
          </div>
        {/if}
      </div>
    </section>
  {/if}

  <div class="layout">
    <!-- Skill list -->
    <section class="list-panel">
      {#if loading}
        <div class="empty">Loading skills…</div>
      {:else if skills.length === 0}
        <div class="empty-state">
          <div class="empty-icon">🧩</div>
          <p>No skills loaded.</p>
          <p class="hint">Install skills into <code>~/.soulacy/skills/</code> or use <code>sy skill install &lt;path&gt;</code>.</p>
        </div>
      {:else}
        <div class="skill-filter">
          <input
            class="skill-filter-input"
            type="search"
            placeholder="Search installed skills…"
            aria-label="Search installed skills"
            bind:value={filterQ}
          />
          {#if filterQ.trim()}
            <span class="skill-filter-count">
              {visibleSkills.length} of {skills.length}
            </span>
          {/if}
        </div>

        {#if visibleSkills.length === 0}
          <div class="empty-state">
            <div class="empty-icon">🔍</div>
            <p>No installed skill matches “{filterQ.trim()}”.</p>
            <p class="hint">Searches skill names and descriptions. To find skills you don’t have yet, use the registry search above.</p>
          </div>
        {/if}

        {#each visibleSkills as sk}
          <button class="skill-row" class:active={selected?.name === sk.name} on:click={() => select(sk)}>
            <div class="skill-row-top">
              <span class="skill-name">{sk.name}</span>
              {#if sk.license}
                <span class="skill-license">{sk.license}</span>
              {/if}
            </div>
            <p class="skill-desc">{sk.description}</p>
            {#if sk.resources?.length}
              <span class="skill-res">{sk.resources.length} resource{sk.resources.length !== 1 ? 's' : ''}</span>
            {/if}
          </button>
        {/each}
      {/if}
    </section>

    <!-- Skill detail -->
    <section class="detail-panel">
      {#if !selected}
        <div class="detail-empty">
          <div class="detail-empty-icon">🧩</div>
          <p>Select a skill to view its instructions and resources.</p>
        </div>
      {:else if detailLoading}
        <div class="detail-empty"><p>Loading…</p></div>
      {:else}
        <div class="detail-header">
          <div>
            <h2 class="detail-name">{selected.name}</h2>
            {#if selected.compatibility}
              <p class="detail-compat">{selected.compatibility}</p>
            {/if}
          </div>
          <div class="detail-meta">
            {#if selected.license}
              <span class="meta-chip">{selected.license}</span>
            {/if}
            {#if selected.metadata?.version}
              <span class="meta-chip">v{selected.metadata.version}</span>
            {/if}
            {#if selected.metadata?.author}
              <span class="meta-chip">by {selected.metadata.author}</span>
            {/if}
          </div>
        </div>

        <p class="detail-description">{selected.description}</p>

        {#if selected.dir}
          <div class="detail-path">
            <span class="path-label">Location</span>
            <code class="path-val">{selected.dir}</code>
          </div>
        {/if}

        {#if selected.resources?.length}
          <div class="resources-section">
            <h3>Resources</h3>
            <ul class="resource-list">
              {#each selected.resources as r}
                <li><code>{r}</code></li>
              {/each}
            </ul>
          </div>
        {/if}

        {#if selected.body}
          <div class="body-section">
            <h3>Instructions</h3>
            <pre class="skill-body">{selected.body}</pre>
          </div>
        {/if}
      {/if}
    </section>
  </div>

  <div class="info-card">
    <h3>Installing skills</h3>
    <p>Copy a skill directory containing a <code>SKILL.md</code> into <code>~/.soulacy/skills/</code> or run:</p>
    <pre class="code-block">sy skill install ./my-skill-dir</pre>
    <p>Skills are automatically injected into the system prompt of every agent as an available_skills catalog. Agents call <code>read_skill</code> to load full instructions when needed.</p>
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.5rem; height: 100%; min-height: 0; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; align-items: center; }

  /* AgenticSkills button */
  .btn-as {
    padding: .45rem .85rem; border-radius: 7px; font-size: .8rem; font-weight: 600; cursor: pointer;
    background: linear-gradient(135deg, #f59e0b, #d97706); color: #fff; border: none;
    transition: opacity .15s;
  }
  .btn-as:hover    { opacity: .85; }
  .btn-as:disabled { opacity: .5; cursor: not-allowed; }

  /* Modal */
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,.65); z-index: 200;
    display: flex; align-items: center; justify-content: center;
  }
  .modal {
    background: #141626; border: 1px solid #2a2f4a; border-radius: 12px;
    padding: 1.5rem; width: 520px; max-width: 95vw; max-height: 88vh; overflow-y: auto;
    display: flex; flex-direction: column; gap: 1rem;
  }
  .modal-header { display: flex; align-items: center; justify-content: space-between; }
  .modal-header h2 { font-size: 1rem; font-weight: 700; }
  .modal-close { background: none; border: none; color: #6b7294; font-size: 1rem; cursor: pointer; padding: .2rem; }
  .modal-footer { display: flex; justify-content: flex-end; gap: .5rem; margin-top: .25rem; }
  .as-hint { font-size: .82rem; color: #7b82a8; line-height: 1.5; }
  .as-hint strong { color: #f59e0b; }
  .as-input {
    width: 100%; padding: .6rem .8rem; border-radius: 7px; font-size: .85rem;
    background: #0e1020; border: 1px solid #2a2f4a; color: #c8cadf;
    outline: none; box-sizing: border-box;
  }
  .as-input:focus { border-color: #f59e0b; }
  .as-err { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; padding: .6rem .8rem; border-radius: 7px; font-size: .82rem; }
  .as-ok  { background: rgba(80,200,120,.1); border: 1px solid rgba(80,200,120,.3); color: #50c878; padding: .6rem .8rem; border-radius: 7px; font-size: .82rem; }
  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err    { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .marketplace-card {
    display: grid; grid-template-columns: 92px 1fr; gap: .85rem;
    background: #141626; border: 1px solid #2a2f4a; border-radius: 10px;
    padding: .85rem 1rem; flex-shrink: 0;
  }
  .marketplace-card.ok { border-color: rgba(76,175,130,.42); }
  .marketplace-card.warn { border-color: rgba(240,160,96,.42); }
  .marketplace-card.fail { border-color: rgba(240,96,96,.42); }
  .marketplace-score {
    min-height: 72px; display: flex; flex-direction: column; align-items: center; justify-content: center;
    background: #0e1020; border: 1px solid #1f2440; border-radius: 8px;
  }
  .marketplace-score strong { font-size: 1.45rem; color: #f0f2ff; line-height: 1; }
  .marketplace-score span { font-size: .66rem; color: #7b82a8; text-transform: uppercase; letter-spacing: .05em; margin-top: .25rem; }
  .marketplace-main { min-width: 0; display: flex; flex-direction: column; gap: .55rem; }
  .marketplace-title { display: flex; justify-content: space-between; align-items: baseline; gap: 1rem; flex-wrap: wrap; }
  .marketplace-title h2 { margin: 0; font-size: .95rem; }
  .marketplace-title p { margin: 0; color: #8b91b3; font-size: .78rem; }
  .marketplace-checks { display: flex; flex-wrap: wrap; gap: .35rem; }
  .marketplace-checks span {
    border: 1px solid #2a2f4a; border-radius: 999px; padding: .16rem .5rem;
    font-size: .7rem; color: #b7bcda; background: #0e1020;
  }
  .marketplace-checks span.ok { border-color: rgba(76,175,130,.35); color: #8bd6b0; }
  .marketplace-checks span.warn { border-color: rgba(240,160,96,.4); color: #f0c08a; }
  .marketplace-checks span.fail { border-color: rgba(240,96,96,.4); color: #f09090; }
  .marketplace-next {
    display: flex; gap: .4rem; color: #9fa5ca; font-size: .78rem; line-height: 1.45;
  }
  .marketplace-next strong { color: #cdd1ef; }

  .layout { display: flex; gap: 1rem; flex: 1; min-height: 0; overflow: hidden; }

  /* List panel */
  .list-panel {
    width: 260px; flex-shrink: 0;
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 10px;
    overflow-y: auto; display: flex; flex-direction: column;
  }
  .empty { padding: 2rem 1rem; text-align: center; color: #6b7294; font-size: .85rem; }
  .empty-state {
    padding: 3rem 1.5rem; text-align: center; display: flex; flex-direction: column;
    align-items: center; gap: .5rem; color: #6b7294;
  }
  .empty-icon { font-size: 2rem; }
  .hint { font-size: .78rem; }
  .hint code { background: #1c1f35; padding: .1rem .3rem; border-radius: 4px; color: #8b85ff; }

  /* Filter over installed skills. Sticky so it stays reachable while the list
     scrolls — the list is the thing you are searching. */
  .skill-filter {
    position: sticky; top: 0; z-index: 2;
    display: flex; align-items: center; gap: .5rem;
    padding: .6rem .7rem;
    background: #0e1020; border-bottom: 1px solid #1a1e36;
  }
  .skill-filter-input {
    flex: 1; min-width: 0;
    padding: .45rem .6rem; border-radius: 6px; font-size: .82rem;
    background: #141626; border: 1px solid #2a2f4a; color: #c8cadf;
    outline: none; box-sizing: border-box;
  }
  .skill-filter-input:focus { border-color: #6c63ff; }
  .skill-filter-input::placeholder { color: #6b7294; }
  .skill-filter-count {
    font-size: .72rem; color: #6b7294; white-space: nowrap;
  }

  .skill-row {
    width: 100%; text-align: left; background: none; padding: .85rem 1rem;
    border-bottom: 1px solid #1a1e36; border-radius: 0;
    color: #c8cadf; cursor: pointer; transition: background .1s;
    display: flex; flex-direction: column; gap: .3rem;
  }
  .skill-row:hover  { background: #141626; }
  .skill-row.active { background: rgba(108,99,255,.12); }
  .skill-row-top    { display: flex; align-items: center; justify-content: space-between; }
  .skill-name    { font-weight: 600; font-size: .87rem; font-family: monospace; color: #8b85ff; }
  .skill-license { font-size: .68rem; color: #555a7a; }
  .skill-desc    { font-size: .78rem; color: #7b82a8; line-height: 1.4; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  .skill-res     { font-size: .7rem; color: #6c63ff; }

  /* Detail panel */
  .detail-panel {
    flex: 1; min-width: 0;
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    overflow-y: auto; padding: 1.25rem; display: flex; flex-direction: column; gap: 1rem;
  }
  .detail-empty {
    flex: 1; display: flex; flex-direction: column; align-items: center;
    justify-content: center; gap: .75rem; color: #6b7294; text-align: center;
  }
  .detail-empty-icon { font-size: 2.5rem; }

  .detail-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
  .detail-name   { font-size: 1.1rem; font-weight: 700; font-family: monospace; color: #8b85ff; }
  .detail-compat { font-size: .78rem; color: #555a7a; margin-top: .2rem; }
  .detail-meta   { display: flex; flex-wrap: wrap; gap: .35rem; }
  .meta-chip {
    font-size: .7rem; padding: .15rem .5rem; border-radius: 999px;
    background: #1a1e36; border: 1px solid #2a2f4a; color: #b0b5d8;
  }
  .detail-description { font-size: .875rem; color: #c8cadf; line-height: 1.6; }

  .detail-path { display: flex; align-items: center; gap: .75rem; }
  .path-label  { font-size: .72rem; color: #555a7a; text-transform: uppercase; letter-spacing: .06em; font-weight: 600; flex-shrink: 0; }
  .path-val    { font-size: .78rem; color: #7b82a8; word-break: break-all; }

  .resources-section h3,
  .body-section h3 {
    font-size: .8rem; text-transform: uppercase; letter-spacing: .06em;
    color: #555a7a; font-weight: 600; margin-bottom: .5rem;
  }
  .resource-list { list-style: none; display: flex; flex-direction: column; gap: .3rem; }
  .resource-list li code {
    font-size: .8rem; color: #f0a060; background: #1a1e36;
    padding: .2rem .5rem; border-radius: 4px;
  }
  .skill-body {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 8px;
    padding: 1rem 1.1rem; font-family: monospace; font-size: .78rem;
    color: #b0b5d8; line-height: 1.65; white-space: pre-wrap; word-break: break-word;
    max-height: 480px; overflow-y: auto;
  }

  .info-card {
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .5rem; flex-shrink: 0;
  }
  .info-card h3 { font-size: .875rem; font-weight: 600; }
  .info-card p  { font-size: .82rem; color: #7b82a8; line-height: 1.6; }
  .info-card code { background: #1c1f35; padding: .1rem .35rem; border-radius: 4px; font-size: .78rem; color: #8b85ff; }
  .code-block {
    background: #0e1020; border: 1px solid #1a1e36; border-radius: 6px;
    padding: .75rem 1rem; font-family: monospace; font-size: .8rem;
    color: #b0b5d8; white-space: pre;
  }

  /* Story 15: the fixed left column stacks on narrow screens. */
  @media (max-width: 768px) {
    .layout { flex-direction: column; overflow: visible; }
    .layout > :first-child { width: 100%; max-height: 240px; }
  }

  /* Story E26: skill sources modal */
  .src-list { display: flex; flex-direction: column; gap: .3rem; margin-bottom: .6rem; }
  .src-row {
    display: flex; align-items: center; gap: .6rem;
    background: #0e1020; border: 1px solid #2a2f4a; border-radius: 6px;
    padding: .35rem .6rem; font-size: .8rem;
  }
  .src-id { font-family: monospace; color: #8b85ff; }
  .src-type {
    background: rgba(108,99,255,.15); border-radius: 999px;
    padding: .05rem .5rem; font-size: .7rem; color: #ada8ff;
  }
  .src-url { color: #6b7294; font-size: .75rem; overflow: hidden; text-overflow: ellipsis; }
  .src-probe-row { display: flex; gap: .5rem; align-items: stretch; }
  .src-probe-row .as-input { flex: 1; }
  .src-auth-input {
    display: flex; flex-direction: column; gap: .35rem; margin-top: .55rem;
    font-size: .72rem; color: #8b91b3; text-transform: uppercase; letter-spacing: .06em;
  }
  .src-auth-input em {
    color: #6b7294; font-style: normal; font-weight: 500; text-transform: none; letter-spacing: 0;
  }
  .src-report {
    margin-top: .7rem; background: #0e1020; border: 1px solid #2a2f4a;
    border-radius: 8px; padding: .7rem .8rem; font-size: .83rem; color: #c8cadf;
  }
  .src-kind { font-weight: 600; margin-bottom: .3rem; }
  .src-audits { color: #4caf82; font-size: .78rem; }
  .src-samples { display: flex; flex-wrap: wrap; gap: .35rem; margin-top: .4rem; }
  .src-degraded { color: #f0a060; font-weight: 650; }
  .src-warnings {
    margin-bottom: .6rem; padding: .55rem .65rem; border-radius: 7px;
    border: 1px solid rgba(240,160,96,.35); background: rgba(240,160,96,.1);
    color: #f0a060; font-size: .78rem; line-height: 1.45;
  }
  .src-warnings p { margin: 0 0 .35rem; }
  .src-warnings p:last-child { margin-bottom: 0; }
  .src-suggestions {
    margin-bottom: .6rem; padding: .55rem .65rem; border-radius: 7px;
    border: 1px solid rgba(108,99,255,.35); background: rgba(108,99,255,.1);
    color: #ada8ff; font-size: .78rem; line-height: 1.45;
  }
  .src-suggestions p { margin: 0 0 .35rem; }
  .src-suggestions p:last-child { margin-bottom: 0; }
  .src-auth-help {
    margin-top: .45rem; padding: .55rem .65rem; border-radius: 7px;
    border: 1px solid rgba(76,175,130,.35); background: rgba(76,175,130,.08);
    color: #8bd6b0; font-size: .78rem; line-height: 1.45;
  }
  .src-auth-help code {
    background: rgba(76,175,130,.14); color: #c6f3d9;
    padding: .05rem .25rem; border-radius: 4px;
  }
  .unverified-toggle {
    display: flex; align-items: center; gap: .45rem; margin: .2rem 0 .65rem;
    color: #9aa0c0; font-size: .78rem; line-height: 1.35;
  }
  .unverified-toggle input { accent-color: #6c63ff; }
  .find-row {
    display: flex; flex-wrap: wrap; align-items: center; gap: .5rem;
    padding: .3rem 0; border-bottom: 1px solid #1c2038; font-size: .8rem;
  }
  .find-row > code { color: #8b85ff; }
  .direct-install {
    display: flex; align-items: center; gap: .55rem; flex-wrap: wrap;
    margin-top: .65rem; padding-top: .65rem; border-top: 1px solid #1c2038;
  }
  .direct-install code {
    color: #8b85ff; background: rgba(108,99,255,.1);
    border: 1px solid rgba(108,99,255,.2); border-radius: 5px;
    padding: .18rem .45rem; word-break: break-all;
  }
  .find-desc { color: #9aa0c0; flex: 1; min-width: 12rem; }
  .find-install { color: #6b7294; font-size: .72rem; }
  .find-install.installed { color: #4caf82; font-weight: 650; }
  .btn-install {
    border: 1px solid rgba(108,99,255,.45); background: rgba(108,99,255,.14);
    color: #c8c5ff; border-radius: 6px; padding: .28rem .6rem;
    font-size: .74rem; font-weight: 700; cursor: pointer;
  }
  .btn-install:hover { background: rgba(108,99,255,.22); }
  .btn-install:disabled { opacity: .55; cursor: not-allowed; }
  .src-samples code {
    background: rgba(108,99,255,.12); border-radius: 4px;
    padding: .1rem .4rem; font-size: .72rem; color: #ada8ff;
  }
</style>
