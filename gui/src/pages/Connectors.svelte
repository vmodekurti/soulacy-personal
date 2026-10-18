<script>
  import { onMount } from 'svelte'
  import TourButton from '../lib/TourButton.svelte'
  import { api } from '../lib/api.js'
  import { advancedCapabilityCount, connectorCategories, connectorExamples, connectorList, connectorPayload, filterConnectors, selectedPlanSites, siteAccessLabel } from '../lib/connectors.js'

  let connectors = []
  let examples = []
  let loading = true
  let error = ''
  let info = ''
  let query = ''
  let category = 'all'
  let composing = false
  let planning = false
  let saving = false
  let intent = ''
  let plan = null
  let editingID = ''
  let customURL = ''

  $: categories = connectorCategories(connectors)
  $: visible = filterConnectors(connectors, query, category)
  $: selectedCount = selectedPlanSites(plan).length

  async function load() {
    loading = true
    error = ''
    try {
      const payload = await api.connectors.list()
      connectors = connectorList(payload)
      examples = connectorExamples(payload)
    } catch (e) {
      error = e?.message || 'Could not load connectors.'
    } finally {
      loading = false
    }
  }

  function startComposer(exampleIntent = '') {
    error = ''
    info = ''
    composing = true
    editingID = ''
    plan = null
    intent = exampleIntent
    customURL = ''
  }

  function closeComposer() {
    composing = false
    plan = null
    editingID = ''
    customURL = ''
  }

  async function planIntent() {
    if (!intent.trim()) return
    planning = true
    error = ''
    try {
      const response = await api.connectors.plan(intent.trim())
      plan = response.plan
    } catch (e) {
      error = e?.message || 'Soulacy could not plan this connector.'
    } finally {
      planning = false
    }
  }

  function toggleSite(index) {
    plan = {
      ...plan,
      sites: plan.sites.map((site, i) => i === index ? { ...site, selected: !site.selected } : site),
    }
  }

  function removeSite(index) {
    plan = { ...plan, sites: plan.sites.filter((_, i) => i !== index) }
  }

  function addCustomSite() {
    error = ''
    try {
      const raw = customURL.trim().includes('://') ? customURL.trim() : `https://${customURL.trim()}`
      const parsed = new URL(raw)
      if (parsed.protocol !== 'https:') throw new Error('Use an HTTPS website.')
      const domain = parsed.hostname.toLowerCase()
      if (!domain || domain === 'localhost' || domain.endsWith('.local')) throw new Error('Use a public website.')
      if ((plan.sites || []).some((site) => site.domain === domain)) throw new Error('That website is already included.')
      const label = domain.replace(/^www\./, '').split('.')[0]
      const name = label ? label[0].toUpperCase() + label.slice(1) : domain
      plan = {
        ...plan,
        sites: [...(plan.sites || []), {
          id: `site_${crypto.randomUUID().replaceAll('-', '')}`,
          name,
          base_url: `https://${domain}`,
          domain,
          public_access: true,
          auth_requirement: 'optional',
          auth_reason: 'Saved items, subscriptions, personalized results, or account pages may require your signed-in session.',
          public_capabilities: [],
          advanced_capabilities: [],
          selected: true,
          reason: 'Added by you',
        }],
      }
      customURL = ''
    } catch (e) {
      error = e?.message || 'Enter a valid public HTTPS website.'
    }
  }

  function editConnector(connector) {
    composing = true
    editingID = connector.id
    intent = connector.intent
    customURL = ''
    plan = {
      name: connector.name,
      intent: connector.intent,
      category: connector.category,
      skill_name: connector.skill_name,
      summary: 'Update the purpose or website set. Soulacy will rebuild the generated skill.',
      guardrails: [
        'Public access never requires credentials.',
        'Existing website sign-ins stay linked when their site remains selected.',
        'Removing a site does not silently delete its saved Website Access session.',
      ],
      sites: (connector.sites || []).map((site) => ({ ...site, selected: true, reason: 'Currently included' })),
    }
  }

  async function saveConnector() {
    if (!plan || selectedCount === 0) {
      error = 'Choose at least one website.'
      return
    }
    saving = true
    error = ''
    info = ''
    try {
      plan = { ...plan, intent: intent.trim() || plan.intent }
      const body = connectorPayload(plan)
      const response = editingID
        ? await api.connectors.update(editingID, body)
        : await api.connectors.create(body)
      info = response.message || 'Connector saved.'
      if (response.warnings?.length) info += ` ${response.warnings.join(' ')}`
      closeComposer()
      await load()
    } catch (e) {
      error = e?.message || 'Could not save the connector.'
    } finally {
      saving = false
    }
  }

  async function configureWebsiteAccess(connector, site) {
    error = ''
    info = ''
    try {
      const response = await api.connectors.websiteAccess(connector.id, site.id)
      if (response.next) {
        location.hash = response.next.replace(/^#/, '')
        return
      }
      location.hash = 'websites'
    } catch (e) {
      error = e?.message || 'Could not prepare Website Access.'
    }
  }

  async function deleteConnector(connector) {
    if (!confirm(`Delete ${connector.name}? The generated skill will be removed. Saved website sign-ins will remain available in Website Access.`)) return
    error = ''
    try {
      const response = await api.connectors.delete(connector.id)
      info = response.message || 'Connector deleted.'
      await load()
    } catch (e) {
      error = e?.message || 'Could not delete the connector.'
    }
  }

  onMount(load)
</script>

<svelte:head><title>Connectors | Soulacy</title></svelte:head>

<main class="page">
  <header class="page-header">
    <div>
      <p class="eyebrow">INTEGRATIONS</p>
      <h1>Connectors</h1>
      <p>Tell Soulacy what you want to do. It builds the website set, access plan, and agent skill.</p>
    </div>
    <div class="header-actions">
      <button class="secondary" on:click={load} disabled={loading}>Refresh</button>
      <TourButton />
    </div>
  </header>

  {#if error}<div class="notice error">{error}</div>{/if}
  {#if info}<div class="notice success">{info}</div>{/if}

  <section class="hero">
    <div>
      <span class="hero-label">Intent to integration</span>
      <h2>Build a connector around your goal</h2>
      <p>Soulacy suggests websites, enables public search immediately, writes the operating skill, and offers a separate encrypted sign-in for each site when account access adds value.</p>
    </div>
    <button class="primary hero-action" on:click={() => startComposer()}>+ Create connector</button>
  </section>

  {#if examples.length}
    <div class="examples" aria-label="Connector examples">
      {#each examples as example}
        <button on:click={() => startComposer(example.intent)}><strong>{example.label}</strong><span>{example.intent}</span></button>
      {/each}
    </div>
  {/if}

  {#if connectors.length}
    <div class="filters">
      <label><span>Search</span><input bind:value={query} type="search" placeholder="Name, purpose, or website" /></label>
      <label><span>Category</span><select bind:value={category}><option value="all">All categories</option>{#each categories as item}<option value={item}>{item}</option>{/each}</select></label>
    </div>
  {/if}

  {#if loading}
    <div class="empty">Loading your connectors...</div>
  {:else if connectors.length === 0}
    <section class="empty first">
      <div class="empty-icon">✦</div>
      <h2>No connectors yet</h2>
      <p>Create one from a sentence. You can choose the sites before anything is saved.</p>
      <button class="primary" on:click={() => startComposer()}>Create your first connector</button>
    </section>
  {:else if visible.length === 0}
    <div class="empty">No connectors match this search.</div>
  {:else}
    <section class="connector-grid">
      {#each visible as connector (connector.id)}
        <article class="connector-card">
          <div class="card-heading">
            <div class="connector-mark">{connector.name?.slice(0, 1) || 'C'}</div>
            <div>
              <div class="title-line"><h2>{connector.name}</h2><span class="ready">Ready</span></div>
              <span class="category">{connector.category}</span>
            </div>
          </div>
          <p class="intent">{connector.intent}</p>
          <div class="connector-facts">
            <span><strong>{connector.sites?.length || 0}</strong> websites</span>
            <span><strong>{advancedCapabilityCount(connector)}</strong> optional account capabilities</span>
            <span><strong>1</strong> generated skill</span>
          </div>
          <div class="sites">
            {#each connector.sites || [] as site}
              <div class="site-row">
                <div class="site-identity"><strong>{site.name}</strong><span>{site.domain}</span></div>
                <div class="access-state"><span class:connected={site.auth_connection_id}>{siteAccessLabel(site)}</span></div>
                <button class="compact" on:click={() => configureWebsiteAccess(connector, site)}>{site.auth_connection_id ? 'Manage sign-in' : 'Add sign-in'}</button>
              </div>
            {/each}
          </div>
          <div class="skill-note"><strong>Agent playbook</strong><code>{connector.skill_name}</code><span>Public pages first. Account access only when the task needs it.</span></div>
          <div class="card-actions">
            <button class="secondary" on:click={() => editConnector(connector)}>Edit websites</button>
            <button class="secondary" on:click={() => location.hash = 'agents'}>Assign skill to agent</button>
            <button class="danger" on:click={() => deleteConnector(connector)}>Delete</button>
          </div>
        </article>
      {/each}
    </section>
  {/if}
</main>

{#if composing}
  <div class="backdrop" role="presentation" on:click|self={closeComposer}>
    <div class="composer" role="dialog" aria-modal="true" aria-labelledby="composer-title" tabindex="-1">
      <header class="composer-header">
        <div><p class="eyebrow">CONNECTOR COMPOSER</p><h2 id="composer-title">{editingID ? 'Edit connector' : 'What should this connector do?'}</h2></div>
        <button class="icon-button" aria-label="Close" on:click={closeComposer}>×</button>
      </header>

      {#if !plan}
        <p class="composer-copy">Describe the outcome in your own words. Include websites if you already know them.</p>
        <textarea bind:value={intent} rows="5" maxlength="1200" placeholder="I want a shopping connector that compares products across Amazon, eBay, Etsy, and any stores I add later."></textarea>
        <div class="composer-actions"><button class="secondary" on:click={closeComposer}>Cancel</button><button class="primary" disabled={planning || intent.trim().length < 5} on:click={planIntent}>{planning ? 'Planning...' : 'Suggest websites'}</button></div>
      {:else}
        <div class="plan-summary">
          <label><span>Connector name</span><input bind:value={plan.name} maxlength="120" /></label>
          <label><span>Purpose</span><textarea bind:value={intent} rows="3" maxlength="1200"></textarea></label>
          <div class="plan-meta"><span>{plan.category}</span><span>{selectedCount} selected</span><span>Skill generated automatically</span></div>
        </div>

        <section class="site-picker">
          <div class="section-heading"><div><h3>Choose websites</h3><p>Public access works without credentials. Sign-in stays optional and separate for each site.</p></div></div>
          <div class="suggestions">
            {#each plan.sites || [] as site, index}
              <article class:selected={site.selected} class="suggestion">
                <button class="site-select" on:click={() => toggleSite(index)} aria-pressed={site.selected}>
                  <span class="check">{site.selected ? '✓' : ''}</span>
                  <span class="suggestion-copy"><strong>{site.name}</strong><small>{site.domain || site.base_url}</small><em>{site.reason}</em></span>
                </button>
                {#if site.reason === 'Added by you' || site.reason === 'Currently included'}<button class="remove" aria-label={`Remove ${site.name}`} on:click={() => removeSite(index)}>×</button>{/if}
                <div class="access-options"><span>Public search ready</span><span>Website sign-in optional</span></div>
              </article>
            {/each}
          </div>
          <div class="custom-site"><input bind:value={customURL} placeholder="Add another website, for example store.com" on:keydown={(e) => e.key === 'Enter' && addCustomSite()} /><button class="secondary" on:click={addCustomSite} disabled={!customURL.trim()}>Add website</button></div>
        </section>

        <section class="security-plan">
          <strong>Security plan</strong>
          {#each plan.guardrails || [] as guardrail}<span>✓ {guardrail}</span>{/each}
        </section>

        <div class="composer-actions"><button class="secondary" on:click={() => plan = null}>Back</button><button class="primary" disabled={saving || selectedCount === 0} on:click={saveConnector}>{saving ? 'Building...' : editingID ? 'Save changes' : 'Build connector'}</button></div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .page{display:flex;flex-direction:column;gap:18px;max-width:1180px}.page-header{display:flex;justify-content:space-between;align-items:flex-start;gap:16px}.page-header h1,.composer-header h2{margin:0}.page-header p:not(.eyebrow){margin:5px 0 0;color:var(--muted,#9298ad)}.eyebrow{margin:0 0 5px;color:#55d8cf;font-size:.69rem;letter-spacing:.11em;font-weight:800}.header-actions,.card-actions,.composer-actions{display:flex;gap:9px;align-items:center;flex-wrap:wrap}.hero{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:20px;align-items:center;padding:24px;border:1px solid rgba(81,216,206,.28);border-radius:15px;background:linear-gradient(135deg,rgba(15,54,67,.96),rgba(19,25,45,.96))}.hero h2{margin:5px 0 8px;font-size:1.35rem}.hero p{margin:0;max-width:760px;color:#bdc8d9;line-height:1.55}.hero-label{color:#65e3d9;font-size:.73rem;font-weight:800;text-transform:uppercase;letter-spacing:.08em}.hero-action{padding:11px 16px}.examples{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:9px}.examples button{display:flex;flex-direction:column;gap:5px;text-align:left;padding:13px;background:var(--panel,#151b29);border:1px solid var(--border,#30394e);border-radius:10px;color:inherit;cursor:pointer}.examples strong{font-size:.8rem}.examples span{font-size:.72rem;color:#929db1;line-height:1.4}.filters{display:grid;grid-template-columns:minmax(240px,1fr) 220px;gap:12px}.filters label,.plan-summary label{display:grid;gap:6px;color:#8994aa;font-size:.76rem}input,select,textarea{box-sizing:border-box;width:100%;border:1px solid var(--border,#30394e);border-radius:8px;background:#101624;color:inherit;padding:10px 12px;font:inherit}textarea{resize:vertical;line-height:1.5}.connector-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.connector-card{display:flex;flex-direction:column;gap:14px;padding:18px;border:1px solid var(--border,#2e374b);border-radius:14px;background:var(--panel,#151b29)}.card-heading{display:flex;gap:12px;align-items:center}.connector-mark{width:42px;height:42px;display:grid;place-items:center;border-radius:11px;background:#153e48;color:#65ded6;font-weight:800}.title-line{display:flex;align-items:center;gap:8px}.title-line h2{margin:0;font-size:1.03rem}.ready{padding:2px 7px;border-radius:999px;background:#14382e;color:#75dfab;font-size:.65rem}.category{color:#8c97ad;font-size:.72rem;text-transform:capitalize}.intent{margin:0;color:#c8cfdd;line-height:1.5}.connector-facts{display:grid;grid-template-columns:repeat(3,1fr);gap:8px}.connector-facts span{display:flex;flex-direction:column;gap:2px;padding:8px;border-radius:8px;background:#101624;color:#8590a6;font-size:.68rem}.connector-facts strong{color:#e7ebf4;font-size:.95rem}.sites{display:flex;flex-direction:column;gap:7px}.site-row{display:grid;grid-template-columns:minmax(0,1fr) auto auto;gap:9px;align-items:center;padding:9px;border:1px solid #2b3448;border-radius:9px}.site-identity{display:flex;flex-direction:column;gap:2px;min-width:0}.site-identity strong{font-size:.79rem}.site-identity span{color:#7e899f;font-size:.68rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.access-state span{color:#64d7ad;font-size:.66rem}.access-state span.connected{color:#76c9f1}.skill-note{display:grid;grid-template-columns:auto 1fr;gap:4px 9px;padding:11px;border-radius:9px;background:#101624}.skill-note strong{font-size:.73rem}.skill-note code{color:#7cdbd4;font-size:.7rem;word-break:break-all}.skill-note span{grid-column:1/-1;color:#8f9aae;font-size:.7rem}.card-actions{margin-top:auto}.primary,.secondary,.danger,.compact,.icon-button{border-radius:7px;padding:8px 11px;font:inherit;font-size:.78rem;cursor:pointer}.primary{border:1px solid #4bd2c8;background:#55d8cf;color:#061718;font-weight:800}.secondary,.compact{border:1px solid #374159;background:#20283a;color:inherit}.danger{margin-left:auto;border:1px solid #673a47;background:#2b1d28;color:#f39aa6}.compact{padding:6px 8px;font-size:.68rem}.notice,.empty{padding:18px;border-radius:10px}.notice.error{color:#ffaaaa;background:#361e28}.notice.success{color:#77dfac;background:#14342c}.empty{text-align:center;border:1px dashed #364057;color:#929caf}.empty.first{padding:36px}.empty h2{margin:7px 0}.empty p{margin:0 0 15px}.empty-icon{font-size:1.6rem;color:#63ddd4}.backdrop{position:fixed;inset:0;z-index:100;background:rgba(3,6,14,.78);display:grid;place-items:center;padding:20px}.composer{width:min(800px,100%);max-height:92vh;overflow:auto;border:1px solid #3a465f;border-radius:15px;background:#141a29;padding:22px;box-shadow:0 24px 90px #0009}.composer-header{display:flex;justify-content:space-between;gap:16px;align-items:flex-start}.icon-button{padding:2px 9px;border:0;background:transparent;color:#aeb7c9;font-size:1.6rem}.composer-copy{color:#aeb7ca;line-height:1.5}.composer>textarea{margin:10px 0 16px}.composer-actions{justify-content:flex-end;margin-top:18px}.plan-summary{display:grid;gap:12px;margin-top:16px}.plan-meta{display:flex;gap:7px;flex-wrap:wrap}.plan-meta span{padding:4px 8px;border-radius:999px;background:#202a3e;color:#9ca8bd;font-size:.69rem;text-transform:capitalize}.site-picker{margin-top:18px}.section-heading h3{margin:0}.section-heading p{margin:4px 0 12px;color:#919db2;font-size:.78rem}.suggestions{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:9px}.suggestion{position:relative;border:1px solid #303a50;border-radius:10px;background:#101624;overflow:hidden}.suggestion.selected{border-color:#3c9f99;background:#11252c}.site-select{display:flex;width:100%;gap:10px;padding:12px 38px 8px 11px;border:0;background:transparent;color:inherit;text-align:left;cursor:pointer}.check{flex:0 0 18px;height:18px;border:1px solid #526078;border-radius:5px;display:grid;place-items:center;color:#54dbd1;font-size:.7rem}.suggestion-copy{display:flex;flex-direction:column;gap:2px;min-width:0}.suggestion-copy strong{font-size:.8rem}.suggestion-copy small{color:#7f8ba0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.suggestion-copy em{color:#58cfc7;font-size:.65rem;font-style:normal}.remove{position:absolute;right:8px;top:8px;border:0;background:transparent;color:#8f9bb0;cursor:pointer;font-size:1rem}.access-options{display:flex;gap:6px;flex-wrap:wrap;padding:0 11px 10px 39px}.access-options span{font-size:.62rem;color:#77dbae}.access-options span+span{color:#98a3b8}.custom-site{display:grid;grid-template-columns:1fr auto;gap:8px;margin-top:10px}.security-plan{display:flex;flex-direction:column;gap:5px;margin-top:16px;padding:12px;border-left:3px solid #4bcfc5;background:#101624}.security-plan strong{font-size:.78rem}.security-plan span{color:#9eabba;font-size:.7rem}@media(max-width:900px){.examples{grid-template-columns:repeat(2,1fr)}.connector-grid{grid-template-columns:1fr}}@media(max-width:650px){.page-header,.hero{grid-template-columns:1fr;display:grid}.filters,.suggestions{grid-template-columns:1fr}.examples{grid-template-columns:1fr}.connector-facts{grid-template-columns:1fr}.site-row{grid-template-columns:1fr}.danger{margin-left:0}.custom-site{grid-template-columns:1fr}}
</style>
