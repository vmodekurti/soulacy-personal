<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { activeWorkspace } from '../lib/workspace.js'
  import PageHelp from '../lib/PageHelp.svelte'
  import TourButton from '../lib/TourButton.svelte'
  import { confirmDestructive } from '../lib/destructive.js'

  const adminTabs = [['policy','Limits & retention'],['usage','Usage report']]
  const ownerTabs = [...adminTabs,['billing','Plan & billing'],['credentials','Automation credentials'],['audit','Audit trail'],['data','Export & deletion']]
  let active = 'policy', loading = true, saving = false, error = '', message = ''
  let policy = emptyPolicy(), effective = {}, retention = {}
  let credentials = [], credentialName = '', credentialScopes = 'agents:read,chat:chat,runs:read', createdSecret = ''
  let auditEvents = [], auditFilter = '', auditCursor = '', auditLoading = false
  let exportsList = [], deletion = null, deletionReason = '', deletionConfirmation = '', deletionWindow = '168h'
  let billing = null, selectedPlan = ''
  let usageReport = null, usageWindow = '24h', usageLoading = false

  $: isOwner = String($activeWorkspace?.role || '').toLowerCase() === 'owner'
  $: isWorkspaceAdmin = ['owner', 'admin'].includes(String($activeWorkspace?.role || '').toLowerCase())
  $: tabs = isOwner ? ownerTabs : adminTabs
  $: workspaceName = $activeWorkspace?.workspaceName || 'this workspace'
  $: filteredAudit = auditEvents.filter((event) => {
    const query = auditFilter.trim().toLowerCase()
    return !query || JSON.stringify(event).toLowerCase().includes(query)
  })

  function emptyPolicy() {
    return { daily_usd:0, monthly_usd:0, daily_tokens:0, per_user_daily_tokens:0, concurrency:0, conversation_history:'', action_events:'', audit_logs:'' }
  }
  function resetNotice() { error = ''; message = '' }
  function failure(e, fallback) {
    if (e?.redirecting) return
    error = e?.message || fallback
  }
  async function loadAll() {
    loading = true; resetNotice()
    if (!isWorkspaceAdmin) { loading = false; return }
    const requests = [api.workspaceAdmin.policy(), api.workspaceAdmin.usageReport(usageWindow)]
    if (isOwner) requests.push(api.workspaceAdmin.credentials(true), api.workspaceAdmin.audit(100), api.workspaceAdmin.exports(), api.workspaceAdmin.deletion(), api.workspaceAdmin.billing())
    const results = await Promise.allSettled(requests)
    if (results[0].status === 'fulfilled') {
      policy = { ...emptyPolicy(), ...(results[0].value?.policy || {}) }
      effective = results[0].value?.effective || {}; retention = results[0].value?.retention || {}
    }
    if (results[1]?.status === 'fulfilled') usageReport = results[1].value
    if (isOwner && results[2]?.status === 'fulfilled') credentials = results[2].value?.keys || []
    if (isOwner && results[3]?.status === 'fulfilled') {
      auditEvents = results[3].value?.events || []; auditCursor = results[3].value?.next_cursor || ''
    }
    if (isOwner && results[4]?.status === 'fulfilled') exportsList = results[4].value?.exports || []
    if (isOwner && results[5]?.status === 'fulfilled') deletion = results[5].value
    if (isOwner && results[6]?.status === 'fulfilled') {
      billing = results[6].value
      selectedPlan = billing?.plan || billing?.default_plan || billing?.plans?.[0] || ''
    }
    const failed = results.find(result => result.status === 'rejected' && !result.reason?.redirecting)
    if (failed) failure(failed.reason, 'Some workspace settings could not be loaded.')
    loading = false
  }
  async function savePolicy() {
    saving = true; resetNotice()
    try {
      const result = await api.workspaceAdmin.savePolicy(policy)
      policy = { ...emptyPolicy(), ...(result?.policy || {}) }; effective = result?.effective || {}; retention = result?.retention || {}
      message = 'Workspace limits and retention were saved.'
    } catch (e) { failure(e, 'Workspace policy could not be saved.') }
    finally { saving = false }
  }
  function monitorOnlyDraft() {
    policy = { ...policy, daily_usd:0, monthly_usd:0, daily_tokens:0, per_user_daily_tokens:0 }
    message = 'Monitoring-only values are ready. Select Save policy to apply them; deployment and provider safeguards remain active.'
  }
  async function loadUsage() {
    usageLoading = true; resetNotice()
    try { usageReport = await api.workspaceAdmin.usageReport(usageWindow) }
    catch (e) { failure(e, 'Workspace usage report could not be loaded.') }
    finally { usageLoading = false }
  }
  async function startCheckout() {
    if (!selectedPlan) return
    saving = true; resetNotice()
    try {
      const session = await api.workspaceAdmin.checkout(selectedPlan)
      if (!session?.url) throw new Error('Checkout did not return a destination.')
      window.location.assign(session.url)
    } catch (e) { failure(e, 'Subscription checkout could not be opened.') }
    finally { saving = false }
  }
  async function openBillingPortal() {
    saving = true; resetNotice()
    try {
      const session = await api.workspaceAdmin.portal()
      if (!session?.url) throw new Error('Billing portal did not return a destination.')
      window.location.assign(session.url)
    } catch (e) { failure(e, 'Billing portal could not be opened.') }
    finally { saving = false }
  }
  async function createCredential() {
    saving = true; resetNotice(); createdSecret = ''
    try {
      const result = await api.workspaceAdmin.createCredential({
        name: credentialName.trim(), kind:'personal_access_token',
        scopes: credentialScopes.split(',').map(value => value.trim()).filter(Boolean),
      })
      createdSecret = result?.key || ''; credentialName = ''
      credentials = (await api.workspaceAdmin.credentials(true))?.keys || []
      message = 'Credential created. Copy it now; Soulacy will not show it again.'
    } catch (e) { failure(e, 'Credential could not be created.') }
    finally { saving = false }
  }
  async function revokeCredential(item) {
    if (!confirmDestructive(`Revoke “${item.name}”? Any automation using it will stop immediately.`)) return
    resetNotice()
    try { await api.workspaceAdmin.revokeCredential(item.id); credentials = (await api.workspaceAdmin.credentials(true))?.keys || []; message = 'Credential revoked.' }
    catch (e) { failure(e, 'Credential could not be revoked.') }
  }
  async function rotateCredential(item) {
    if (!confirmDestructive(`Rotate “${item.name}”? Its current secret will stop working immediately.`)) return
    resetNotice(); createdSecret = ''
    try {
      const result = await api.workspaceAdmin.rotateCredential(item.id)
      createdSecret = result?.key || ''; credentials = (await api.workspaceAdmin.credentials(true))?.keys || []
      message = 'Credential rotated. Copy the replacement now.'
    } catch (e) { failure(e, 'Credential could not be rotated.') }
  }
  async function loadMoreAudit() {
    if (!auditCursor || auditLoading) return
    auditLoading = true; resetNotice()
    try {
      const result = await api.workspaceAdmin.audit(100, auditCursor)
      auditEvents = [...auditEvents, ...(result?.events || [])]; auditCursor = result?.next_cursor || ''
    } catch (e) { failure(e, 'More audit events could not be loaded.') }
    finally { auditLoading = false }
  }
  function exportAudit() {
    const blob = new Blob([JSON.stringify(filteredAudit, null, 2)], { type:'application/json' })
    downloadBlob(blob, `soulacy-audit-${Date.now()}.json`)
  }
  async function requestExport() {
    saving = true; resetNotice()
    try { await api.workspaceAdmin.requestExport(); exportsList = (await api.workspaceAdmin.exports())?.exports || []; message = 'Workspace export started.' }
    catch (e) { failure(e, 'Workspace export could not be started.') }
    finally { saving = false }
  }
  async function downloadWorkspaceExport(item) {
    resetNotice()
    try { const result = await api.workspaceAdmin.downloadExport(item.id); downloadBlob(result.blob, result.filename) }
    catch (e) { failure(e, 'Workspace export could not be downloaded.') }
  }
  function downloadBlob(blob, filename) {
    const link = document.createElement('a'); link.href = URL.createObjectURL(blob); link.download = filename; link.click(); URL.revokeObjectURL(link.href)
  }
  async function requestDeletion() {
    if (deletionConfirmation !== workspaceName) return
    saving = true; resetNotice()
    try {
      await api.workspaceAdmin.requestDeletion({ confirm_workspace_name:deletionConfirmation, recovery_window:deletionWindow, reason:deletionReason })
      deletion = await api.workspaceAdmin.deletion(); message = 'Deletion scheduled. Workspace writes are now paused during the recovery window.'
    } catch (e) { failure(e, 'Workspace deletion could not be scheduled.') }
    finally { saving = false }
  }
  async function cancelDeletion() {
    if (!confirmDestructive(`Cancel deletion of “${workspaceName}” and restore workspace writes?`)) return
    saving = true; resetNotice()
    try { await api.workspaceAdmin.cancelDeletion(); deletion = await api.workspaceAdmin.deletion(); message = 'Workspace deletion was cancelled.' }
    catch (e) { failure(e, 'Workspace deletion could not be cancelled.') }
    finally { saving = false }
  }
  function date(value) { return value ? new Date(value).toLocaleString() : '—' }
  function number(value) { return Number(value || 0).toLocaleString() }
  function usd(value) { return Number(value || 0).toLocaleString(undefined, { style:'currency', currency:'USD', maximumFractionDigits:4 }) }
  function userLabel(value) { return value || 'Unattributed' }

  onMount(loadAll)
</script>

<div class="page">
  <header><div><p class="eyebrow">WORKSPACE ADMINISTRATION</p><h1>Workspace settings</h1><p>Govern {workspaceName} without changing the Soulacy deployment.</p></div><div class="header-actions"><PageHelp page="workspace-admin" compact /><TourButton page="workspace-admin" /><button on:click={loadAll} disabled={loading}>↻ Refresh</button></div></header>
  {#if !isWorkspaceAdmin}<div class="notice denied"><strong>Administrator access required</strong><span>Only workspace owners and administrators can change workspace-wide limits.</span></div>
  {:else}
    <nav aria-label="Workspace settings sections">{#each tabs as tab}<button class:active={active===tab[0]} on:click={()=>active=tab[0]}>{tab[1]}</button>{/each}</nav>
    {#if error}<p class="notice error" role="alert">{error}</p>{/if}{#if message}<p class="notice success" role="status">{message}</p>{/if}
    {#if loading}<p class="loading">Loading workspace controls…</p>
    {:else if active === 'policy'}
      <form class="panel" on:submit|preventDefault={savePolicy}>
        <div class="panel-title"><div><h2>Budgets and capacity</h2><p>Start with monitoring, observe normal usage in the Usage report, then add a hard limit only when you have a defensible threshold. Workspace values can tighten but never raise a deployment ceiling.</p></div><div class="policy-actions"><button type="button" on:click={monitorOnlyDraft}>Use monitoring only</button><button class="primary" disabled={saving}>{saving?'Saving…':'Save policy'}</button></div></div>
        <p class="education"><strong>Important:</strong> A positive budget is a hard stop—model calls are rejected as soon as it is reached. Zero removes the workspace-level hard stop and continues measuring every call. Deployment and provider safeguards still apply.</p>
        <div class="grid limits"><label>Daily spend (USD)<input type="number" min="0" step="0.01" bind:value={policy.daily_usd}/><small>{policy.daily_usd>0?'Hard stop enabled':'Monitoring only'} · Effective: {effective.daily_usd ? usd(effective.daily_usd) : 'no hard stop'}</small></label><label>Monthly spend (USD)<input type="number" min="0" step="0.01" bind:value={policy.monthly_usd}/><small>{policy.monthly_usd>0?'Hard stop enabled':'Monitoring only'} · Effective: {effective.monthly_usd ? usd(effective.monthly_usd) : 'no hard stop'}</small></label><label>Workspace daily tokens<input type="number" min="0" step="1000" bind:value={policy.daily_tokens}/><small>{policy.daily_tokens>0?'Hard stop enabled':'Monitoring only'} · Effective: {effective.daily_tokens ? number(effective.daily_tokens) : 'no hard stop'}</small></label><label>Per-user tokens / rolling 24h<input type="number" min="0" step="1000" bind:value={policy.per_user_daily_tokens}/><small>{policy.per_user_daily_tokens>0?'Hard stop enabled':'Monitoring only'} · Effective: {effective.per_user_daily_tokens ? number(effective.per_user_daily_tokens) : 'no hard stop'}</small></label><label>Concurrent runs<input type="number" min="0" step="1" bind:value={policy.concurrency}/><small>Effective: {effective.concurrency || 'unlimited'}</small></label></div>
        <h2 class="subhead">Retention</h2><div class="grid three"><label>Conversation history<input bind:value={policy.conversation_history} placeholder="720h"/><small>Effective: {retention.conversation_history || 'deployment default'}</small></label><label>Action events<input bind:value={policy.action_events} placeholder="720h"/><small>Effective: {retention.action_events || 'deployment default'}</small></label><label>Audit logs<input bind:value={policy.audit_logs} placeholder="2160h"/><small>Effective: {retention.audit_logs || 'deployment default'}</small></label></div>
      </form>
    {:else if active === 'usage'}
      <section class="panel">
        <div class="panel-title"><div><h2>Workspace usage</h2><p>Prompt-free operational reporting for this workspace. Use observed demand—not guesswork—to decide whether a hard limit is useful.</p></div><div class="report-actions"><select bind:value={usageWindow} on:change={loadUsage} aria-label="Usage report period"><option value="24h">Last 24 hours</option><option value="7d">Last 7 days</option><option value="30d">Last 30 days</option></select><button on:click={loadUsage} disabled={usageLoading}>{usageLoading?'Loading…':'Refresh'}</button></div></div>
        {#if usageReport}
          <div class="report-status" class:blocked={usageReport.health==='blocked'} class:attention={usageReport.health==='attention'}><strong>{usageReport.monitoring_mode==='monitor_only'?'Monitoring only':'Hard limits active'}</strong><span>{usageReport.health==='healthy'?'Usage accounting is healthy.':usageReport.health==='blocked'?'Calls were blocked in this period.':'Usage needs attention.'}</span></div>
          <div class="summary-grid"><article><small>Total tokens</small><strong>{number(usageReport.totals?.total_tokens)}</strong></article><article><small>Model calls</small><strong>{number(usageReport.totals?.calls)}</strong></article><article><small>Estimated spend</small><strong>{usd(usageReport.totals?.estimated_cost_usd)}</strong></article><article><small>Failed / blocked</small><strong>{number(usageReport.totals?.failed_calls)} / {number(usageReport.totals?.rejected_calls)}</strong></article><article><small>Accounting coverage</small><strong>{Math.round(Number(usageReport.totals?.accounting_coverage || 0)*100)}%</strong></article><article><small>Busiest user</small><strong>{number(usageReport.totals?.highest_user_tokens)} tokens</strong></article></div>
          <div class="recommendations">{#each usageReport.recommendations || [] as recommendation}<p>{recommendation}</p>{/each}{#if usageReport.totals?.suggested_user_tokens>0}<p>If you later enable a per-user ceiling, a cautious starting point based on this period is <strong>{number(usageReport.totals.suggested_user_tokens)} tokens</strong> (twice the observed high-water mark, rounded up). Revisit it as usage changes.</p>{/if}</div>
          <div class="report-columns"><div><h3>Top users</h3><div class="usage-table">{#each usageReport.top_users || [] as row}<div><span title={row.subject}>{userLabel(row.subject)}</span><strong>{number(row.total_tokens)}</strong><small>{usd(row.cost_usd)}</small></div>{/each}{#if !(usageReport.top_users||[]).length}<p>No usage recorded.</p>{/if}</div></div><div><h3>Top agents</h3><div class="usage-table">{#each usageReport.top_agents || [] as row}<div><span title={row.agent_id}>{row.agent_id || 'Unattributed'}</span><strong>{number(row.total_tokens)}</strong><small>{usd(row.cost_usd)}</small></div>{/each}{#if !(usageReport.top_agents||[]).length}<p>No usage recorded.</p>{/if}</div></div><div><h3>Top models</h3><div class="usage-table">{#each usageReport.top_models || [] as row}<div><span title={`${row.provider}/${row.model}`}>{row.provider}/{row.model}</span><strong>{number(row.total_tokens)}</strong><small>{usd(row.cost_usd)}</small></div>{/each}{#if !(usageReport.top_models||[]).length}<p>No usage recorded.</p>{/if}</div></div></div>
          <p class="generated">Generated {date(usageReport.generated_at)} · Costs are estimates; unknown-priced calls are counted in tokens but may show $0.</p>
        {:else}<p>No usage report is available.</p>{/if}
      </section>
    {:else if active === 'billing'}
      <section class="panel">
        <div class="panel-title"><div><h2>Subscription</h2><p>Billing is scoped to this workspace. Only workspace owners can change its plan or payment method.</p></div><span class:active-status={billing?.status==='active'} class="billing-status">{billing?.status || 'unconfigured'}</span></div>
        {#if !billing?.configured}<p>Self-service billing is not configured for this deployment. Contact the platform administrator.</p>
        {:else}
          <div class="billing-summary"><div><small>Current plan</small><strong>{billing?.plan || 'No subscription'}</strong></div><div><small>Enforcement</small><strong>{billing?.enforcement || 'migration'}</strong></div></div>
          <div class="billing-actions">
            {#if billing?.status!=='active'}<label>Plan<select bind:value={selectedPlan}>{#each billing?.plans || [] as plan}<option value={plan}>{plan}</option>{/each}</select></label>
            <button class="primary" on:click={startCheckout} disabled={saving||!selectedPlan}>{saving?'Opening…':'Start subscription'}</button>{/if}
            {#if billing?.customer_portal_available}<button class:primary={billing?.status==='active'} on:click={openBillingPortal} disabled={saving}>Manage subscription, payment & invoices</button>{/if}
          </div>
          {#if billing?.subscription_required && billing?.status==='none'}<p class="notice denied">A subscription is required before this workspace can create or run agents. Read-only access remains available.</p>{/if}
        {/if}
      </section>
    {:else if active === 'credentials'}
      <section class="panel"><h2>Create automation credential</h2><p>Creates a workspace-bound personal token for the current owner. Use the least scopes necessary; plaintext appears once.</p><form class="credential-form" on:submit|preventDefault={createCredential}><label>Name<input bind:value={credentialName} required placeholder="CI deployment"/></label><label>Scopes, comma separated<input bind:value={credentialScopes} required/></label><button class="primary" disabled={saving}>{saving?'Creating…':'Create credential'}</button></form>
        {#if createdSecret}<div class="secret"><strong>Copy this credential now</strong><code>{createdSecret}</code><button on:click={()=>navigator.clipboard.writeText(createdSecret)}>Copy</button></div>{/if}
      </section>
      <section class="panel"><h2>Workspace credentials</h2>{#if credentials.length===0}<p>No credentials have been issued.</p>{:else}<div class="table">{#each credentials as item}<div class="row"><div><strong>{item.name}</strong><small>{item.prefix}… · {item.kind?.replaceAll('_',' ')}</small></div><span class:active-status={item.status==='active'}>{item.status}</span><span>{item.role}</span><span>{date(item.expires_at)}</span><div class="row-actions">{#if item.status==='active'}<button on:click={()=>rotateCredential(item)}>Rotate</button><button class="danger" on:click={()=>revokeCredential(item)}>Revoke</button>{/if}</div></div>{/each}</div>{/if}</section>
    {:else if active === 'audit'}
      <section class="panel"><div class="panel-title"><div><h2>Workspace audit trail</h2><p>Configuration and access changes scoped to this workspace.</p></div><button on:click={exportAudit} disabled={!filteredAudit.length}>Export visible</button></div><label class="search">Search<input bind:value={auditFilter} placeholder="actor, action, resource, request ID…"/></label>
        <div class="audit">{#each filteredAudit as event}<article><div><strong>{event.action}</strong><span class:event-failed={event.status!=='ok'}>{event.status}</span></div><p>{event.actor || 'unknown actor'} · {event.resource}{event.target?` · ${event.target}`:''}</p><small>{date(event.timestamp)} · {event.request_id || 'no request id'}</small></article>{/each}</div>{#if filteredAudit.length===0}<p>No matching audit events.</p>{/if}{#if auditCursor}<button class="load-more" on:click={loadMoreAudit} disabled={auditLoading}>{auditLoading?'Loading…':'Load older events'}</button>{/if}
      </section>
    {:else}
      <section class="panel"><div class="panel-title"><div><h2>Workspace exports</h2><p>Exports are checksummed, expire automatically, and contain only this workspace.</p></div><button class="primary" on:click={requestExport} disabled={saving}>Create export</button></div>{#if exportsList.length===0}<p>No exports yet.</p>{:else}<div class="table">{#each exportsList as item}<div class="row export-row"><div><strong>{item.id}</strong><small>Created {date(item.created_at)}</small></div><span>{item.status}</span><span>Expires {date(item.expires_at)}</span><div>{#if item.status==='ready'}<button on:click={()=>downloadWorkspaceExport(item)}>Download</button>{/if}</div></div>{/each}</div>{/if}</section>
      <section class="panel danger-zone"><h2>Recoverable deletion</h2>{#if deletion?.workspace?.status === 'deleting'}<p>This workspace is scheduled for deletion. Writes are paused until the purge, but an owner can cancel before <strong>{date(deletion.workspace.recover_until)}</strong>.</p><button class="primary" on:click={cancelDeletion} disabled={saving}>Cancel deletion and restore access</button>{:else}<p>Deletion pauses writes immediately and schedules a purge after the recovery window. Type the exact workspace name to continue.</p><div class="grid two"><label>Reason<textarea bind:value={deletionReason} required maxlength="500"></textarea></label><label>Recovery window<select bind:value={deletionWindow}><option value="24h">24 hours</option><option value="168h">7 days</option><option value="720h">30 days</option></select></label></div><label>Type <strong>{workspaceName}</strong><input bind:value={deletionConfirmation}/></label><button class="danger" on:click={requestDeletion} disabled={saving||deletionConfirmation!==workspaceName}>{saving?'Scheduling…':'Schedule workspace deletion'}</button>{/if}</section>
    {/if}
  {/if}
</div>

<style>
  .page{max-width:1220px;margin:0 auto;padding:30px;color:#eef0ff}.page>header{display:flex;justify-content:space-between;gap:24px;align-items:flex-start;margin-bottom:22px}.eyebrow{margin:0 0 7px;color:#67d8a4;font-size:11px;font-weight:800;letter-spacing:.15em}h1{margin:0;font-size:30px}.page header p{color:#929ab4}.header-actions{display:flex;gap:10px}.header-actions>button,.panel button,nav button{border:1px solid #ffffff18;border-radius:8px;background:#20263a;color:#eef0ff;padding:9px 12px;font-weight:700;cursor:pointer}.page>nav{display:flex;gap:5px;margin-bottom:18px;border-bottom:1px solid #ffffff12}.page>nav button{border:0;border-radius:8px 8px 0 0;background:transparent;color:#8f98b3}.page>nav button.active{background:#795cff20;color:#d8d2ff}.panel{margin-bottom:18px;padding:21px;border:1px solid #ffffff14;border-radius:14px;background:#121625}.panel h2{margin:0 0 6px;font-size:18px}.panel p{color:#949db8;line-height:1.55}.panel-title{display:flex;justify-content:space-between;gap:20px;align-items:flex-start}.panel-title .primary{width:auto;margin:0}.grid{display:grid;gap:14px;margin-top:18px}.grid.three{grid-template-columns:repeat(3,1fr)}.grid.two{grid-template-columns:2fr 1fr}label{display:grid;gap:7px;color:#cbd0e2;font-size:12px;font-weight:700}input,textarea,select{box-sizing:border-box;width:100%;padding:10px;border:1px solid #ffffff20;border-radius:8px;background:#090c16;color:#fff}textarea{min-height:82px;resize:vertical}label small,.row small{color:#7f88a3;font-weight:400}.subhead{margin-top:25px!important}.primary{border:0!important;background:linear-gradient(135deg,#795cff,#24bd7c)!important;color:#fff}.credential-form{display:grid;grid-template-columns:1fr 2fr auto;align-items:end;gap:12px}.credential-form .primary{margin:0}.secret{display:grid;grid-template-columns:auto 1fr auto;align-items:center;gap:12px;margin-top:16px;padding:14px;border:1px solid #63d7a955;border-radius:10px;background:#63d7a910}.secret code{overflow:auto;color:#a7f0ce}.table{margin-top:15px;border:1px solid #ffffff10;border-radius:10px;overflow:hidden}.row{display:grid;grid-template-columns:minmax(220px,2fr) 100px 90px 180px minmax(160px,auto);align-items:center;gap:12px;padding:12px;border-top:1px solid #ffffff0d}.row:first-child{border-top:0}.row>div:first-child{display:grid;gap:4px}.row>span{color:#939cb6;font-size:12px}.row-actions{display:flex;justify-content:flex-end;gap:7px}.row-actions button{padding:7px 9px}.row .danger,.danger-zone>.danger{border-color:#ef5b6855;background:#ef5b6815;color:#ff9da7}.active-status{color:#6de0aa!important}.search{margin:17px 0}.audit{display:grid;gap:8px}.audit article{padding:13px;border:1px solid #ffffff0d;border-radius:9px;background:#090c1688}.audit article>div{display:flex;justify-content:space-between}.audit p{margin:5px 0;font-size:12px}.audit small{color:#747e9b}.event-failed{color:#ff909b}.load-more{margin-top:14px}.export-row{grid-template-columns:2fr 100px 220px auto}.notice{display:grid;gap:4px;padding:12px 14px;border-radius:9px}.notice.error,.notice.denied{background:#ef5b6815;color:#ff9da7}.notice.success{background:#22bd7b15;color:#7ce0ae}.loading{padding:40px;text-align:center;color:#8992ad}.danger-zone{border-color:#ef5b6833}.danger-zone label{margin-top:15px}.danger-zone>.danger{margin-top:16px}.danger-zone>.primary{width:auto}.denied{max-width:620px}.denied span{color:#d4a8ad}@media(max-width:900px){.grid.three,.grid.two{grid-template-columns:1fr 1fr}.credential-form{grid-template-columns:1fr}.row,.export-row{grid-template-columns:1fr 1fr}.row>div:first-child{grid-column:1/-1}.page>header{display:grid}}@media(max-width:560px){.page{padding:20px 14px}.grid.three,.grid.two,.row,.export-row{grid-template-columns:1fr}.page>nav{overflow:auto}.panel-title,.secret{grid-template-columns:1fr;display:grid}}
  .billing-status{padding:6px 10px;border-radius:999px;background:#ef5b6815;color:#ff9da7;text-transform:capitalize}.billing-status.active-status{background:#22bd7b15}.billing-summary{display:grid;grid-template-columns:repeat(2,minmax(140px,1fr));gap:12px;margin:18px 0}.billing-summary>div{display:grid;gap:5px;padding:14px;border:1px solid #ffffff10;border-radius:10px;background:#090c1688}.billing-summary small{color:#7f88a3}.billing-actions{display:flex;align-items:end;gap:10px;flex-wrap:wrap}.billing-actions label{min-width:220px}.billing-actions button{margin-bottom:0}
  .grid.limits{grid-template-columns:repeat(5,minmax(0,1fr))}@media(max-width:1050px){.grid.limits{grid-template-columns:repeat(3,1fr)}}@media(max-width:560px){.grid.limits{grid-template-columns:1fr}}
  .policy-actions,.report-actions{display:flex;gap:9px;align-items:center;flex-wrap:wrap}.report-actions select{min-width:150px}.education{padding:12px 14px;border-left:3px solid #8e76ff;border-radius:7px;background:#795cff12;color:#c7cce0!important}.report-status{display:flex;justify-content:space-between;gap:12px;margin-top:18px;padding:12px 14px;border:1px solid #52d39b44;border-radius:10px;background:#22bd7b12;color:#8ce2b8}.report-status.attention{border-color:#e4b64b55;background:#e4b64b12;color:#f0ce7d}.report-status.blocked{border-color:#ef5b6855;background:#ef5b6812;color:#ff9da7}.summary-grid{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));gap:10px;margin-top:14px}.summary-grid article{display:grid;gap:7px;padding:13px;border:1px solid #ffffff10;border-radius:10px;background:#090c1688}.summary-grid small,.usage-table small,.generated{color:#7f88a3}.summary-grid strong{font-size:17px}.recommendations{margin-top:14px;padding:3px 14px;border:1px solid #ffffff10;border-radius:10px;background:#0b0f1c}.recommendations p{margin:10px 0}.report-columns{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;margin-top:20px}.report-columns h3{font-size:14px}.usage-table{border:1px solid #ffffff10;border-radius:10px;overflow:hidden}.usage-table>div{display:grid;grid-template-columns:minmax(0,1fr) auto auto;gap:10px;padding:10px;border-top:1px solid #ffffff0d;align-items:center}.usage-table>div:first-child{border-top:0}.usage-table span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.usage-table p{padding:0 10px}.generated{font-size:11px!important}@media(max-width:900px){.summary-grid{grid-template-columns:repeat(3,1fr)}.report-columns{grid-template-columns:1fr}}@media(max-width:560px){.summary-grid{grid-template-columns:repeat(2,1fr)}.policy-actions,.report-actions{align-items:stretch}.policy-actions button,.report-actions select,.report-actions button{width:100%}}
</style>
