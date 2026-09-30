<script>
  import { onMount } from 'svelte'
  import TourButton from '../lib/TourButton.svelte'
  import { api } from '../lib/api.js'
  import { authenticationSummary, connectorCategories, connectorList, connectorSkillStatus, effectSummary, filterConnectors, installedSkillNames } from '../lib/connectors.js'

  let connectors = []
  let loading = true
  let error = ''
  let query = ''
  let category = 'all'
  let expanded = ''
  let installedSkills = new Set()

  $: categories = connectorCategories(connectors)
  $: visible = filterConnectors(connectors, query, category)

  async function load() {
    loading = true
    error = ''
    try {
      const [connectorPayload, skillPayload] = await Promise.all([
        api.connectors.list(),
        api.skills.list(),
      ])
      connectors = connectorList(connectorPayload)
      installedSkills = installedSkillNames(skillPayload)
    } catch (e) {
      error = e?.message || 'Could not load the connector catalog.'
    } finally {
      loading = false
    }
  }

  function go(page) {
    location.hash = page
  }

  onMount(load)
</script>

<div class="page">
  <div class="page-header">
    <div>
      <h1>Connectors</h1>
      <p>Find the safest supported path to shopping, events, and other services.</p>
    </div>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
      <TourButton />
    </div>
  </div>

  <section class="intro-card">
    <div>
      <span class="eyebrow">One catalog, existing guardrails</span>
      <h2>Connect services without learning the plumbing</h2>
      <p>A connector recipe tells an agent how to use a public service with the web tools it already has. Provider accounts and API keys are optional extensions for capabilities that actually need them. MCP and plugins remain available for structured or account-specific access.</p>
    </div>
    <div class="boundary">
      <strong>Shopping boundary</strong>
      <span>Agents can search and compare. Purchase and ticket checkout open on the provider site for your review.</span>
    </div>
  </section>

  <div class="filters" aria-label="Filter connectors">
    <label class="search-field">
      <span>Search</span>
      <input bind:value={query} type="search" placeholder="Products, tickets, barcodes..." aria-label="Search connectors" />
    </label>
    <label>
      <span>Category</span>
      <select bind:value={category} aria-label="Connector category">
        <option value="all">All categories</option>
        {#each categories as item}<option value={item}>{item[0].toUpperCase() + item.slice(1)}</option>{/each}
      </select>
    </label>
  </div>

  {#if error}<div class="message error">{error}</div>{/if}

  {#if loading}
    <div class="empty">Loading connector recipes...</div>
  {:else if visible.length === 0}
    <div class="empty">No connectors match this search.</div>
  {:else}
    <div class="catalog" aria-live="polite">
      {#each visible as connector (connector.id)}
        <article class="connector-card">
          <div class="card-top">
            <div class="provider-mark" aria-hidden="true">{connector.name?.slice(0, 1) || 'C'}</div>
            <div class="identity">
              <div class="title-line">
                <h2>{connector.name}</h2>
                <span class="recipe-badge">Recipe ready</span>
              </div>
              <span class="provider">{connector.provider} · {connector.category}</span>
            </div>
          </div>

          <p class="summary">{connector.summary}</p>

          <div class="facts">
            <span><strong>Provider login</strong>{authenticationSummary(connector)}</span>
            <span><strong>Risk</strong>{effectSummary(connector.capabilities)}</span>
            <span><strong>Runs on</strong>{(connector.deployment_targets || []).join(', ')}</span>
          </div>

          <div class="capabilities" aria-label="Capabilities">
            {#each connector.capabilities || [] as capability}
              <span class:write={capability.effect === 'write'} title={capability.description}>{capability.label}</span>
            {/each}
          </div>

          <div class="card-actions">
            <button class="btn-primary" on:click={() => expanded = expanded === connector.id ? '' : connector.id} aria-expanded={expanded === connector.id}>
              {expanded === connector.id ? 'Hide setup' : 'View setup'}
            </button>
            <a class="btn-link" href={connector.docs_url} target="_blank" rel="noopener">Provider website ↗</a>
          </div>

          {#if expanded === connector.id}
            <div class="setup">
              <div class="setup-column">
                <h3>Setup</h3>
                <ol>{#each connector.setup_steps || [] as step}<li>{step}</li>{/each}</ol>
              </div>
              <div class="setup-column">
                <h3>Access</h3>
                {#if connector.credentials?.length}
                  <ul class="credential-list">
                    {#each connector.credentials as credential}
                      <li><code>{credential.name}</code><span>{credential.label}</span></li>
                    {/each}
                  </ul>
                  <button class="btn-secondary" on:click={() => go('secrets')}>Open Secrets</button>
                {:else}
                  <p>No provider account or API key is required for public search and retrieval.</p>
                {/if}
              </div>
              <div class="setup-column full">
                <h3>Recommended skills</h3>
                <div class="skill-list">
                  {#each connectorSkillStatus(connector, installedSkills) as skill}
                    <span class:installed={skill.installed}>
                      <strong>{skill.name}</strong>
                      {skill.installed ? 'Installed' : 'Not installed'}
                    </span>
                  {/each}
                </div>
                <div class="setup-actions skill-actions">
                  <button class="btn-secondary" on:click={() => go('skills')}>Open Skills</button>
                  <button class="btn-secondary" on:click={() => go('agents')}>Assign to an agent</button>
                </div>

                <h3>How agents use it</h3>
                <p>Grant URL retrieval to the agent, then assign the recommended skill. Web search can improve discovery but is optional. Add a reviewed MCP server or plugin only when you need structured or account-specific capabilities.</p>
                <div class="setup-actions">
                  <button class="btn-secondary" on:click={() => go('mcp')}>Open MCP</button>
                  <button class="btn-secondary" on:click={() => go('pluginmgr')}>Open Plugins</button>
                  {#if connector.signup_url}<a class="btn-link" href={connector.signup_url} target="_blank" rel="noopener">Provider setup ↗</a>{/if}
                </div>
                <div class="checkout"><strong>Checkout:</strong> {connector.checkout}</div>
                {#if connector.limitations?.length}
                  <ul class="limitations">{#each connector.limitations as item}<li>{item}</li>{/each}</ul>
                {/if}
              </div>
            </div>
          {/if}
        </article>
      {/each}
    </div>
  {/if}
</div>

<style>
  .page { display: flex; flex-direction: column; gap: 18px; max-width: 1180px; }
  .page-header { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; }
  .page-header h1 { margin: 0; }
  .page-header p { margin: 5px 0 0; color: var(--muted, #9298ad); }
  .header-actions, .card-actions, .setup-actions { display: flex; gap: 9px; align-items: center; flex-wrap: wrap; }
  .intro-card { display: grid; grid-template-columns: minmax(0, 1.7fr) minmax(240px, .8fr); gap: 20px; padding: 22px; border: 1px solid rgba(68, 214, 205, .25); border-radius: 14px; background: linear-gradient(135deg, rgba(19, 50, 65, .92), rgba(18, 25, 44, .92)); }
  .intro-card h2 { margin: 5px 0 8px; font-size: 1.25rem; }
  .intro-card p { margin: 0; color: #b7c3d5; line-height: 1.55; }
  .eyebrow { color: #57ddd4; font-size: .72rem; text-transform: uppercase; letter-spacing: .1em; font-weight: 700; }
  .boundary { padding: 14px; border-radius: 10px; background: rgba(3, 9, 18, .42); display: flex; flex-direction: column; gap: 7px; }
  .boundary strong { color: #f5d98f; }
  .boundary span { color: #b9c2d2; font-size: .86rem; line-height: 1.45; }
  .filters { display: grid; grid-template-columns: minmax(260px, 1fr) 220px; gap: 12px; }
  .filters label { display: grid; gap: 6px; color: var(--muted, #9298ad); font-size: .78rem; }
  input, select { width: 100%; box-sizing: border-box; color: inherit; background: var(--panel, #151a27); border: 1px solid var(--border, #30394e); border-radius: 8px; padding: 10px 12px; }
  .catalog { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
  .connector-card { border: 1px solid var(--border, #2c3549); background: var(--panel, #141a27); border-radius: 13px; padding: 17px; display: flex; flex-direction: column; gap: 13px; }
  .card-top { display: flex; gap: 12px; align-items: center; }
  .provider-mark { width: 40px; height: 40px; border-radius: 11px; background: #163e4b; color: #62e1d8; display: grid; place-items: center; font-weight: 800; }
  .identity { min-width: 0; flex: 1; }
  .title-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .title-line h2 { margin: 0; font-size: 1.02rem; }
  .provider { color: var(--muted, #9298ad); font-size: .78rem; text-transform: capitalize; }
  .recipe-badge { color: #79dfaa; background: rgba(57, 176, 115, .13); border: 1px solid rgba(57, 176, 115, .26); border-radius: 999px; padding: 2px 7px; font-size: .66rem; }
  .summary { color: #c7cede; line-height: 1.48; margin: 0; min-height: 43px; }
  .facts { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
  .facts span { display: flex; flex-direction: column; gap: 3px; color: #b5bed0; font-size: .75rem; }
  .facts strong { color: #737f98; font-size: .64rem; text-transform: uppercase; letter-spacing: .06em; }
  .capabilities { display: flex; flex-wrap: wrap; gap: 6px; }
  .capabilities span { border-radius: 999px; padding: 4px 8px; font-size: .72rem; color: #91d7f2; background: rgba(73, 158, 195, .11); border: 1px solid rgba(73, 158, 195, .22); }
  .capabilities span.write { color: #f2bd91; background: rgba(195, 119, 73, .11); border-color: rgba(195, 119, 73, .25); }
  button, .btn-link { font: inherit; }
  .btn-primary, .btn-secondary, .btn-link { border-radius: 7px; padding: 7px 11px; cursor: pointer; text-decoration: none; font-size: .8rem; }
  .btn-primary { border: 1px solid #40c6bc; color: #041416; background: #52d8cf; font-weight: 700; }
  .btn-secondary { border: 1px solid var(--border, #34405a); color: inherit; background: rgba(35, 44, 64, .78); }
  .btn-link { color: #64dcd4; }
  .setup { border-top: 1px solid var(--border, #2c3549); padding-top: 14px; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
  .setup-column h3 { margin: 0 0 8px; font-size: .8rem; text-transform: uppercase; color: #8a96ae; letter-spacing: .05em; }
  .setup-column p, .setup-column li { color: #b9c1d2; font-size: .8rem; line-height: 1.5; }
  .setup-column ol, .setup-column ul { margin: 0; padding-left: 19px; }
  .setup-column.full { grid-column: 1 / -1; }
  .credential-list { list-style: none; padding: 0 !important; margin-bottom: 10px !important; }
  .credential-list li { display: flex; flex-direction: column; margin-bottom: 7px; }
  .skill-list { display: flex; flex-wrap: wrap; gap: 7px; margin-bottom: 8px; }
  .skill-list span { display: flex; flex-direction: column; gap: 2px; border: 1px solid #604451; border-radius: 8px; padding: 7px 9px; color: #d9a9b4; font-size: .7rem; }
  .skill-list span.installed { border-color: #31654f; color: #91dfb5; background: rgba(49, 101, 79, .12); }
  .skill-list strong { color: inherit; font-size: .76rem; }
  .skill-actions { margin-bottom: 16px; }
  code { color: #e8c982; font-size: .75rem; }
  .checkout { margin-top: 12px; color: #b9c1d2; font-size: .8rem; }
  .limitations { margin-top: 8px !important; }
  .empty, .message { padding: 24px; border: 1px dashed var(--border, #30394e); border-radius: 10px; color: var(--muted, #9298ad); text-align: center; }
  .message.error { color: #ffaaaa; border-style: solid; background: rgba(110, 30, 30, .18); }
  @media (max-width: 850px) {
    .catalog { grid-template-columns: 1fr; }
    .intro-card { grid-template-columns: 1fr; }
  }
  @media (max-width: 620px) {
    .page-header { flex-direction: column; }
    .filters { grid-template-columns: 1fr; }
    .facts { grid-template-columns: 1fr; }
    .setup { grid-template-columns: 1fr; }
    .setup-column.full { grid-column: auto; }
  }
</style>
