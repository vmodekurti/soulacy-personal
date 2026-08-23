<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { apiKey, authRequired } from '../lib/stores.js'
  import { confirmPlatform } from '../lib/destructive.js'
  import ProviderIcon from '../lib/ProviderIcon.svelte'
  import PublicNav from '../lib/PublicNav.svelte'
  import PageHelp from '../lib/PageHelp.svelte'

  let keyInput = '', authenticated = false, loading = false, error = '', active = 'overview'
  let overview = null, organizations = [], refreshedAt = null, restarting = false
  let logLines = [], logSource = '', logNote = '', logFilter = '', logsLoading = false
  let auditEvents = [], auditCursor = '', auditFilter = '', auditLoading = false
  let provisioning = false, provisionMessage = ''
  let organizationName = '', firstWorkspaceName = '', firstWorkspaceSlug = '', ownerName = '', ownerEmail = ''
  let targetOrganization = '', workspaceName = '', workspaceSlug = '', workspaceOwnerName = '', workspaceOwnerEmail = ''
	let organizationLogo='', firstWorkspaceLogo='', workspaceLogo='', setupLink=''
  let editingWorkspaceID='', editingSlug='', addressSaving=false
	let lifecycleTarget=null, lifecycleReason='', lifecycleConfirmation='', lifecycleSaving=false
  const tabs = [['overview','Overview'],['tenants','Organizations'],['diagnostics','Diagnostics'],['audit','Audit trail'],['security','Security boundary']]

  $: filteredAudit = auditEvents.filter(event => {
    const query = auditFilter.trim().toLowerCase()
    return !query || JSON.stringify(event).toLowerCase().includes(query)
  })

  onMount(async () => { if ($apiKey) await refresh() })
  async function unlock() {
    if (!keyInput.trim()) return
    $apiKey = keyInput.trim(); $authRequired = false
    await refresh(true)
  }
  async function refresh(clearOnFailure = false) {
    loading = true; error = ''
    try {
      const [o, t] = await Promise.all([api.admin.platformOverview(), api.admin.platformOrganizations()])
      overview = o; organizations = t?.organizations || []; authenticated = true; refreshedAt = new Date()
    } catch (e) {
      authenticated = false
      error = e?.status === 401 || e?.status === 403 ? 'That deployment administrator key was rejected.' : (e?.message || 'The control plane could not be loaded.')
      if (clearOnFailure) $apiKey = ''
    } finally { loading = false }
  }
  function signOut() { $apiKey = ''; keyInput = ''; authenticated = false; overview = null; organizations = [] }
  async function provisionOrganization() {
    provisioning = true; error = ''; provisionMessage = ''
    try {
		const created = await api.admin.provisionOrganization({organization_name:organizationName,workspace_name:firstWorkspaceName,workspace_slug:firstWorkspaceSlug,owner_display_name:ownerName,owner_email:ownerEmail,organization_logo:organizationLogo,workspace_logo:firstWorkspaceLogo})
      provisionMessage = `${created?.result?.organization?.name || organizationName} is ready. Its owner can sign in with ${ownerEmail}.`
		setupLink = workspaceSetupLink(created?.result)
		organizationName=''; firstWorkspaceName=''; firstWorkspaceSlug=''; ownerName=''; ownerEmail=''; organizationLogo=''; firstWorkspaceLogo=''; await refresh()
    } catch(e) { error=e?.message||'Organization could not be created.' }
    finally { provisioning=false }
  }
  async function provisionWorkspace() {
    if (!targetOrganization) return
    provisioning = true; error = ''; provisionMessage = ''
    try {
		const created = await api.admin.provisionWorkspace(targetOrganization,{workspace_name:workspaceName,workspace_slug:workspaceSlug,owner_display_name:workspaceOwnerName,owner_email:workspaceOwnerEmail,workspace_logo:workspaceLogo})
      provisionMessage = `${created?.result?.workspace?.name || workspaceName} is ready. Its owner can sign in with ${workspaceOwnerEmail}.`
		setupLink = workspaceSetupLink(created?.result)
		workspaceName=''; workspaceSlug=''; workspaceOwnerName=''; workspaceOwnerEmail=''; workspaceLogo=''; await refresh()
    } catch(e) { error=e?.message||'Workspace could not be created.' }
    finally { provisioning=false }
  }
	function workspaceSetupLink(result){const workspace=result?.workspace;const address=workspace?.slug||workspace?.id;return result?.setup_token&&address?`${location.origin}/w/${encodeURIComponent(address)}/setup?token=${encodeURIComponent(result.setup_token)}`:''}
	function editAddress(workspace){editingWorkspaceID=workspace.id;editingSlug=workspace.slug||'';error=''}
	async function saveAddress(){if(!editingWorkspaceID||!editingSlug.trim()||addressSaving)return;addressSaving=true;error='';try{await api.admin.updateWorkspaceAddress(editingWorkspaceID,editingSlug.trim().toLowerCase());editingWorkspaceID='';editingSlug='';await refresh()}catch(e){error=e?.message||'Workspace address could not be updated.'}finally{addressSaving=false}}
	function readLogo(event, target){const file=event.target.files?.[0];if(!file)return;if(file.size>500000){error='Logo must be smaller than 500 KB.';return}const reader=new FileReader();reader.onload=()=>{if(target==='organization')organizationLogo=String(reader.result||'');else if(target==='first-workspace')firstWorkspaceLogo=String(reader.result||'');else workspaceLogo=String(reader.result||'')};reader.readAsDataURL(file)}
	async function copySetupLink(){if(setupLink)await navigator.clipboard.writeText(setupLink)}
	function beginSuspension(kind, item, organization=null){lifecycleTarget={kind,item,organization};lifecycleReason='';lifecycleConfirmation='';error=''}
	function cancelSuspension(){lifecycleTarget=null;lifecycleReason='';lifecycleConfirmation=''}
	async function applySuspension(){if(!lifecycleTarget||lifecycleSaving)return;lifecycleSaving=true;error='';try{const body={status:'suspended',reason:lifecycleReason.trim(),confirm_name:lifecycleConfirmation.trim()};if(lifecycleTarget.kind==='organization')await api.admin.updateOrganizationStatus(lifecycleTarget.item.id,body);else await api.admin.updateWorkspaceStatus(lifecycleTarget.item.id,body);cancelSuspension();await refresh()}catch(e){error=e?.message||'The tenant could not be suspended.'}finally{lifecycleSaving=false}}
	async function reactivate(kind,item){const label=kind==='organization'?'organization':'workspace';if(!confirmPlatform(`Reactivate ${label} “${item.name}”? Access will resume immediately, except for workspaces with their own hold.`))return;error='';try{if(kind==='organization')await api.admin.updateOrganizationStatus(item.id,{status:'active',reason:'Deployment administrator reactivated tenant'});else await api.admin.updateWorkspaceStatus(item.id,{status:'active',reason:'Deployment administrator reactivated tenant'});await refresh()}catch(e){error=e?.message||`The ${label} could not be reactivated.`}}
  function statusClass(value) {
    const v = String(value || '').toLowerCase()
    return v === 'ok' || v === 'ready' || v === 'active' ? 'good' : v === 'deleting' || v === 'degraded' ? 'warn' : 'bad'
  }
  function exportSnapshot() {
    const blob = new Blob([JSON.stringify({ generated_at:new Date().toISOString(), overview, organizations }, null, 2)], {type:'application/json'})
    const link = document.createElement('a'); link.href = URL.createObjectURL(blob); link.download = `soulacy-platform-${Date.now()}.json`; link.click(); URL.revokeObjectURL(link.href)
  }
  async function restart() {
    if (!confirmPlatform('Restart the entire Soulacy deployment? In-flight work across every workspace will be interrupted.')) return
    restarting = true; error = ''
    try { await api.admin.restart(); setTimeout(() => location.reload(), 2500) }
    catch (e) { error = e?.message || 'Restart was not accepted.'; restarting = false }
  }
  async function refreshLogs() {
    logsLoading = true; error = ''
    try {
      const result = await api.logs.get(500, logFilter.trim())
      logLines = result?.lines || []
      logSource = result?.source || ''
      logNote = result?.note || ''
    } catch (e) {
      error = e?.message || 'Gateway logs could not be loaded.'
    } finally { logsLoading = false }
  }
  async function refreshAudit(append = false) {
    if (auditLoading) return
    auditLoading = true; error = ''
    try {
      const result = await api.admin.platformAudit(100, append ? auditCursor : '')
      auditEvents = append ? [...auditEvents, ...(result?.events || [])] : (result?.events || [])
      auditCursor = result?.next_cursor || ''
    } catch (e) {
      error = e?.message || 'The deployment audit trail could not be loaded.'
    } finally { auditLoading = false }
  }
  function selectTab(id) {
    active = id
    if (id === 'audit' && auditEvents.length === 0) refreshAudit()
  }
  function exportAudit() {
    const blob = new Blob([JSON.stringify(filteredAudit, null, 2)], {type:'application/json'})
    const link = document.createElement('a'); link.href = URL.createObjectURL(blob); link.download = `soulacy-platform-audit-${Date.now()}.json`; link.click(); URL.revokeObjectURL(link.href)
  }
  function formatDate(value) { return value ? new Date(value).toLocaleString() : '—' }
</script>

<svelte:head><title>Platform administration · Soulacy</title></svelte:head>

{#if !authenticated}
  <div class="login-shell"><PublicNav current="admin" helpPage="admin-login" /><main class="login-main"><form class="login-card" on:submit|preventDefault={unlock}>
    <div class="brand">⬡ <span>Soulacy</span></div><p class="eyebrow">DEPLOYMENT CONTROL PLANE</p>
    <h1>Platform administration</h1><p class="lead">Operate the service without entering customer workspaces.</p>
    <label>Deployment administrator key<input type="password" autocomplete="current-password" bind:value={keyInput} placeholder="server.api_key" /></label>
    <button class="primary" disabled={loading || !keyInput.trim()}>{loading ? 'Verifying…' : 'Open control plane'}</button>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <p class="hint">This host credential can access deployment routes only. It cannot list agents, runs, secrets, conversations, or other workspace content.</p>
    <div class="login-links"><a href="/">← Back to home</a><a href="/admin/setup">First-time deployment setup</a></div>
  </form></main></div>
{:else}
  <div class="shell"><aside>
    <div class="brand">⬡ <span>Soulacy</span></div>
    <div class="scope"><strong>Platform</strong><span>{overview?.mode || 'team'} deployment</span></div>
    <nav>{#each tabs as tab}<button class:active={active===tab[0]} on:click={() => selectTab(tab[0])}>{tab[1]}</button>{/each}</nav>
    <div class="aside-foot"><a href="https://soulacy.io" target="_blank" rel="noreferrer">Soulacy.io ↗</a><button on:click={signOut}>Sign out</button></div>
  </aside><main class="content">
    <header><div><p class="eyebrow">DEPLOYMENT ADMINISTRATOR</p><h1>{tabs.find(t=>t[0]===active)?.[1]}</h1></div>
      <div class="actions"><span>{refreshedAt ? `Updated ${refreshedAt.toLocaleTimeString()}` : ''}</span><PageHelp page={`platform-${active}`} compact /><button on:click={() => refresh()} disabled={loading}>{loading?'Refreshing…':'↻ Refresh'}</button></div></header>
    {#if error}<p class="error" role="alert">{error}</p>{/if}

    {#if active === 'overview'}
      <section class="stats">
        <article><span>Organizations</span><strong>{overview?.summary?.organizations ?? '—'}</strong><small>{overview?.summary?.suspended_organizations || 0} suspended</small></article>
        <article><span>Workspaces</span><strong>{overview?.summary?.workspaces ?? '—'}</strong><small>{overview?.summary?.active_workspaces || 0} active · {overview?.summary?.suspended_workspaces || 0} suspended</small></article>
        <article><span>Users</span><strong>{overview?.summary?.users ?? '—'}</strong></article>
        <article><span>Active memberships</span><strong>{overview?.summary?.active_memberships ?? '—'}</strong></article>
      </section>
      <section class="panel"><div class="panel-title"><div><h2>Platform status</h2><p>Service-level signals only; no tenant payloads are queried.</p></div><span class="badge {statusClass(overview?.replica?.status)}">{overview?.replica?.status}</span></div>
        <div class="rows"><div><span>Gateway version</span><strong>{overview?.version||'unknown'}</strong></div><div><span>Deployment mode</span><strong>{overview?.mode}</strong></div><div><span>Authentication</span><strong>{overview?.authentication?.mode} · {overview?.authentication?.status}</strong></div><div><span>Pending invitations</span><strong>{overview?.summary?.pending_invitations||0}</strong></div></div>
      </section>
    {:else if active === 'tenants'}
      <section class="provision-grid">
        <form class="panel" on:submit|preventDefault={provisionOrganization}><h2>Create organization</h2><p>Create its first workspace and designate the customer owner. You will not become a member.</p>
			<label>Organization name<input bind:value={organizationName} required placeholder="Acme" /></label><label>Organization logo<input type="file" accept="image/png,image/jpeg,image/webp" on:change={(e)=>readLogo(e,'organization')} /></label><label>First workspace<input bind:value={firstWorkspaceName} required placeholder="Production" /></label><label>Workspace address <small>Optional · lowercase letters, numbers, and hyphens</small><input bind:value={firstWorkspaceSlug} pattern="[a-z0-9][a-z0-9-]{1,46}[a-z0-9]" placeholder="acme-production" /></label><label>Workspace logo<input type="file" accept="image/png,image/jpeg,image/webp" on:change={(e)=>readLogo(e,'first-workspace')} /></label><div class="form-two"><label>Workspace administrator<input bind:value={ownerName} required /></label><label>Administrator email<input type="email" bind:value={ownerEmail} required /></label></div><button class="primary" disabled={provisioning}>{provisioning?'Creating…':'Create organization'}</button></form>
        <form class="panel" on:submit|preventDefault={provisionWorkspace}><h2>Create workspace</h2><p>Add a workspace to an existing organization and assign its initial owner.</p>
			<label>Organization<select bind:value={targetOrganization} required><option value="">Select organization</option>{#each organizations as org}<option value={org.id} disabled={org.status==='suspended'}>{org.name}{org.status==='suspended'?' · suspended':''}</option>{/each}</select></label><label>Workspace name<input bind:value={workspaceName} required placeholder="Operations" /></label><label>Workspace address <small>Optional · for example acme-operations</small><input bind:value={workspaceSlug} pattern="[a-z0-9][a-z0-9-]{1,46}[a-z0-9]" placeholder="acme-operations" /></label><label>Workspace logo<input type="file" accept="image/png,image/jpeg,image/webp" on:change={(e)=>readLogo(e,'workspace')} /></label><div class="form-two"><label>Workspace administrator<input bind:value={workspaceOwnerName} required /></label><label>Administrator email<input type="email" bind:value={workspaceOwnerEmail} required /></label></div><button class="primary" disabled={provisioning||!targetOrganization}>{provisioning?'Creating…':'Create workspace'}</button></form>
      </section>
      {#if provisionMessage}<p class="success">{provisionMessage}</p>{/if}
		{#if setupLink}<section class="panel setup-link"><h2>One-time workspace setup link</h2><p>Send this securely to the workspace administrator. It expires in seven days and can be used once.</p><code>{setupLink}</code><button class="secondary" on:click={copySetupLink}>Copy setup link</button></section>{/if}
      <section class="panel"><div class="panel-title"><div><h2>Organizations and workspaces</h2><p>Lifecycle metadata for support and capacity planning.</p></div><span>{organizations.length} organizations</span></div>
		{#if lifecycleTarget}<form class="lifecycle-confirm" on:submit|preventDefault={applySuspension}><h3>Suspend {lifecycleTarget.kind}</h3><p>This immediately blocks new sign-ins and pauses writes. Existing sessions become read-only. {#if lifecycleTarget.kind==='organization'}All {lifecycleTarget.item.workspaces?.length||0} workspaces in this organization are affected, while their individual states are preserved.{/if}</p><label>Reason<textarea bind:value={lifecycleReason} maxlength="500" required placeholder="Incident, billing hold, security review…"></textarea></label><label>Type <strong>{lifecycleTarget.item.name}</strong> to confirm<input bind:value={lifecycleConfirmation} required /></label><div class="lifecycle-actions"><button class="danger" disabled={lifecycleSaving||!lifecycleReason.trim()||lifecycleConfirmation.trim()!==lifecycleTarget.item.name}>{lifecycleSaving?'Suspending…':'Suspend access'}</button><button type="button" class="secondary" on:click={cancelSuspension}>Cancel</button></div></form>{/if}
        {#if organizations.length===0}<p class="empty">No organizations have been provisioned.</p>{/if}
        {#each organizations as org}<div class="org"><div class="org-head"><div class="org-identity">{#if org.logo_data_url}<img class="tenant-logo" src={org.logo_data_url} alt="{org.name} logo" />{/if}<div><h3>{org.name}</h3><code>{org.id}</code></div></div><div class="tenant-actions"><span class="badge {statusClass(org.status)}">{org.status}</span><span>{org.workspaces?.length||0} workspaces</span>{#if org.status==='suspended'}<button class="reactivate" on:click={()=>reactivate('organization',org)}>Reactivate</button>{:else}<button class="suspend" on:click={()=>beginSuspension('organization',org)}>Suspend</button>{/if}</div></div>
			{#each org.workspaces||[] as ws}<div class="workspace">{#if ws.logo_data_url}<img class="tenant-logo" src={ws.logo_data_url} alt="{ws.name} logo" />{/if}<div><strong>{ws.name}</strong>{#if editingWorkspaceID===ws.id}<form class="address-editor" on:submit|preventDefault={saveAddress}><span>{location.host}/w/</span><input bind:value={editingSlug} pattern="[a-z0-9][a-z0-9-]{1,46}[a-z0-9]" required /><button disabled={addressSaving}>{addressSaving?'Saving…':'Save'}</button><button type="button" on:click={()=>editingWorkspaceID=''}>Cancel</button></form>{:else}<span class="address-row"><a class="workspace-address" href={`/w/${encodeURIComponent(ws.slug||ws.id)}`}>/w/{ws.slug||ws.id}</a><button class="edit-address" on:click={()=>editAddress(ws)}>Edit address</button></span>{/if}</div><span>{ws.active_members} members</span><span>{ws.pending_invitations} invites</span><span class="provider-state">{#if ws.provider_type}<ProviderIcon provider={ws.provider_type} size={22}/>{ws.provider_type}{:else}Identity setup pending{/if}</span><span class="badge {statusClass(ws.status)}">{ws.status}</span>{#if ws.status==='suspended'}<button class="reactivate" on:click={()=>reactivate('workspace',ws)}>Reactivate</button>{:else if ws.status==='active'}<button class="suspend" on:click={()=>beginSuspension('workspace',ws,org)}>Suspend</button>{/if}</div>{/each}
        </div>{/each}
      </section>
    {:else if active === 'diagnostics'}
      <section class="split"><article class="panel"><div class="panel-title"><div><h2>Replica readiness</h2><p>Required dependencies must be healthy before traffic is accepted.</p></div><span class="badge {statusClass(overview?.replica?.status)}">{overview?.replica?.status}</span></div><div class="rows"><div><span>Accepting traffic</span><strong>{overview?.replica?.ready?'Yes':'No'}</strong></div><div><span>Draining</span><strong>{overview?.replica?.draining?'Yes':'No'}</strong></div></div></article>
        <article class="panel"><h2>Operator tools</h2><p>Export a redacted service snapshot for troubleshooting, or restart the deployment.</p><button class="secondary" on:click={exportSnapshot}>Download diagnostic snapshot</button><button class="danger" disabled={restarting} on:click={restart}>{restarting?'Restarting…':'Restart gateway'}</button></article></section>
      <section class="panel"><h2>Dependencies</h2>{#if !(overview?.dependencies?.length)}<p class="empty">No shared dependency probes are registered.</p>{/if}<div class="dependencies">{#each overview?.dependencies||[] as dep}<div><span class="dot {statusClass(dep.status)}"></span><div><strong>{dep.name}</strong><small>{dep.required?'Required':'Optional'}{dep.detail?` · ${dep.detail}`:''}</small></div><span>{dep.status}</span></div>{/each}</div></section>
      <section class="panel platform-logs"><div class="panel-title"><div><h2>Gateway logs</h2><p>Deployment-level output. This surface is available only with the deployment administrator key.</p></div><span>{logLines.length} lines</span></div>
        <div class="log-tools"><label>Filter<input bind:value={logFilter} placeholder="error, request id, component…" on:keydown={(e)=>e.key==='Enter'&&refreshLogs()} /></label><button class="secondary" on:click={refreshLogs} disabled={logsLoading}>{logsLoading?'Loading…':'Load latest logs'}</button></div>
        {#if logSource}<small class="log-source">Source: {logSource}</small>{/if}
        {#if logNote}<p class="empty">{logNote}</p>{/if}
        {#if logLines.length}<pre>{logLines.join('\n')}</pre>{:else if !logsLoading && !logNote}<p class="empty">Load logs when deployment troubleshooting requires them.</p>{/if}
      </section>
    {:else if active === 'audit'}
      <section class="panel platform-audit"><div class="panel-title"><div><h2>Deployment audit trail</h2><p>Search deployment-key activity and tenant lifecycle operations without exposing workspace payloads.</p></div><button class="secondary audit-export" on:click={exportAudit} disabled={!filteredAudit.length}>Export visible</button></div>
        <div class="log-tools"><label>Search<input bind:value={auditFilter} placeholder="actor, action, tenant, request ID…" /></label><button class="secondary" on:click={() => refreshAudit()} disabled={auditLoading}>{auditLoading?'Loading…':'Reload audit'}</button></div>
        {#if filteredAudit.length}<div class="audit-list">{#each filteredAudit as event}<article><div class="audit-head"><strong>{event.action}</strong><span class="badge {statusClass(event.status)}">{event.status}</span></div><p>{event.actor || 'deployment key'} · {event.resource}{event.target ? ` · ${event.target}` : ''}</p><div class="audit-meta"><span>{formatDate(event.timestamp)}</span>{#if event.organization_id}<code>org: {event.organization_id}</code>{/if}{#if event.workspace_id}<code>workspace: {event.workspace_id}</code>{/if}{#if event.request_id}<code>request: {event.request_id}</code>{/if}</div></article>{/each}</div>{:else if !auditLoading}<p class="empty">No matching audit events.</p>{/if}
        {#if auditCursor}<button class="secondary load-more" on:click={() => refreshAudit(true)} disabled={auditLoading}>{auditLoading?'Loading…':'Load older events'}</button>{/if}
      </section>
    {:else}
      <section class="panel boundary"><h2>Control-plane boundary</h2><p>The deployment administrator is intentionally not a workspace super-user.</p><div class="boundary-grid"><div class="allowed"><h3>Can manage</h3><ul><li>Gateway health and readiness</li><li>Organization and workspace lifecycle metadata</li><li>Deployment configuration and restarts</li><li>Capacity and incident diagnostics</li></ul></div><div class="denied"><h3>Cannot access by default</h3><ul><li>Workspace agents and conversations</li><li>Runs, files, memories, and knowledge</li><li>Workspace secrets and provider credentials</li><li>Member actions inside a tenant</li></ul></div></div><p class="callout">Customer support access should be a separate, explicit, time-limited grant with tenant consent and a durable audit record.</p></section>
    {/if}
  </main></div>
{/if}

<style>
	.provision-grid{display:grid;grid-template-columns:1fr 1fr;gap:18px}.provision-grid label{display:grid;gap:6px;margin-top:12px;color:#aeb6cc;font-size:12px}.provision-grid input,.provision-grid select{box-sizing:border-box;width:100%;padding:10px;border:1px solid #ffffff18;border-radius:8px;background:#090c16;color:#fff}.form-two{display:grid;grid-template-columns:1fr 1fr;gap:10px}.success{padding:12px 14px;border-radius:9px;background:#22bd7b18;color:#8ce4b9}.setup-link code{overflow-wrap:anywhere;padding:12px;background:#090c16;border-radius:8px}.tenant-logo{width:36px;height:36px;object-fit:contain;border-radius:8px;background:#fff;padding:3px}.org-identity{display:flex!important;align-items:center;gap:10px;min-width:0!important}.provider-state{display:flex;align-items:center;gap:6px}.workspace-address{color:#8fddb9;font-size:11px;text-decoration:none}.workspace-address:hover{text-decoration:underline}@media(max-width:900px){.provision-grid{grid-template-columns:1fr}}@media(max-width:560px){.form-two{grid-template-columns:1fr}}
	.address-row{display:flex;align-items:center;gap:8px}.edit-address{padding:2px 6px;border:1px solid #ffffff18;border-radius:5px;background:transparent;color:#9c93d8;font-size:10px}.address-editor{display:flex;align-items:center;gap:5px;margin-top:4px}.address-editor span{color:#737d99;font-size:10px}.address-editor input{min-width:110px;padding:5px;border:1px solid #ffffff20;border-radius:5px;background:#090c16;color:#fff}.address-editor button{padding:5px 7px;border:1px solid #ffffff18;border-radius:5px;background:#20263a;color:#fff;font-size:10px}
	.tenant-actions{display:flex;align-items:center;gap:10px}.suspend,.reactivate{padding:6px 9px;border-radius:7px;font-size:11px;font-weight:700}.suspend{border:1px solid #ef5b6855;background:#ef5b6815;color:#ff9da7}.reactivate{border:1px solid #22bd7b55;background:#22bd7b12;color:#7ce0ae}.lifecycle-confirm{margin:16px 0;padding:18px;border:1px solid #ef5b6855;border-radius:12px;background:#ef5b680b}.lifecycle-confirm h3{margin:0}.lifecycle-confirm label{display:grid;gap:6px;margin-top:12px;color:#adb5cb;font-size:12px}.lifecycle-confirm input,.lifecycle-confirm textarea{box-sizing:border-box;width:100%;padding:10px;border:1px solid #ffffff20;border-radius:8px;background:#090c16;color:#fff}.lifecycle-confirm textarea{min-height:78px;resize:vertical}.lifecycle-actions{display:flex;gap:10px}.lifecycle-actions .danger,.lifecycle-actions .secondary{width:auto}.workspace>.badge{white-space:nowrap}
	.log-tools{display:grid;grid-template-columns:1fr auto;align-items:end;gap:12px;margin:16px 0}.log-tools label{display:grid;gap:6px;color:#8992ae;font-size:12px}.log-tools input{box-sizing:border-box;width:100%;padding:10px;border:1px solid #ffffff18;border-radius:8px;background:#090c16;color:#fff}.log-tools .secondary{margin:0;width:auto}.platform-logs pre{max-height:430px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;padding:14px;border:1px solid #ffffff0d;border-radius:9px;background:#070a12;color:#c8cede;font:11px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace}.log-source{color:#737d99}
	.audit-export{width:auto;margin:0}.audit-list{display:grid;gap:9px}.audit-list article{padding:14px;border:1px solid #ffffff0d;border-radius:10px;background:#090c1688}.audit-head{display:flex;justify-content:space-between;gap:14px}.audit-list p{margin:6px 0;color:#adb5ca}.audit-meta{display:flex;flex-wrap:wrap;gap:10px;color:#727c98;font-size:11px}.audit-meta code{color:#727c98}.load-more{width:auto;margin:16px auto 0}
  :global(body){margin:0;background:#090b14;color:#eef0ff;font-family:Inter,ui-sans-serif,system-ui,sans-serif}.login-shell{min-height:100vh;display:flex;flex-direction:column;background:radial-gradient(circle at 10% 10%,#34237366,transparent 35%),radial-gradient(circle at 90% 90%,#07654a55,transparent 35%)}.login-main{flex:1;display:grid;place-items:center;padding:48px 24px 72px}.login-card{width:min(520px,100%);box-sizing:border-box;padding:36px;border:1px solid #ffffff18;border-radius:22px;background:#111522f2}.brand{display:flex;gap:10px;align-items:center;color:#9b86ff;font-size:27px}.brand span{font-size:18px;font-weight:750;color:#fff}.eyebrow{margin:24px 0 7px;color:#70ddb0;font-size:11px;font-weight:800;letter-spacing:.15em}h1{margin:0;font-size:34px;letter-spacing:-.035em}.lead,.panel p{color:#969eb8;line-height:1.55}.login-card label{display:grid;gap:8px;margin-top:24px;color:#cbd0e2;font-size:13px;font-weight:650}.login-card input{box-sizing:border-box;width:100%;padding:12px;border:1px solid #ffffff20;border-radius:9px;background:#090c16;color:#fff}.primary,.secondary,.danger,.actions button{border-radius:9px;padding:10px 14px;font-weight:700;cursor:pointer}.primary{width:100%;margin-top:14px;border:0;background:linear-gradient(135deg,#795cff,#24bd7c);color:#fff}.hint{color:#7f879f;font-size:12px;line-height:1.5}.login-card a{color:#9d91e8;text-decoration:none}.login-links{display:flex;justify-content:space-between;gap:16px;margin-top:20px;font-size:12px}.error{padding:11px 13px;border-radius:9px;background:#ef5b6818;color:#ff9da7}.shell{min-height:100vh;display:grid;grid-template-columns:240px 1fr}aside{position:sticky;top:0;height:100vh;box-sizing:border-box;padding:24px 16px;display:flex;flex-direction:column;border-right:1px solid #ffffff12;background:#0d101c}.scope{display:grid;gap:2px;margin:26px 4px 14px;padding:12px;border:1px solid #795cff40;border-radius:10px;background:#795cff12}.scope span{color:#8189a5;font-size:12px}nav{display:grid;gap:4px}nav button,.aside-foot button{border:0;background:transparent;color:#959db8;padding:10px 12px;border-radius:8px;text-align:left;font-weight:650}nav button:hover,nav button.active{background:#795cff20;color:#d8d2ff}.aside-foot{display:grid;gap:6px;margin-top:auto;padding:12px}.aside-foot a{padding:8px 12px;color:#70ddb0;text-decoration:none;font-size:12px}.aside-foot button{color:#ff939d}.content{padding:34px clamp(22px,4vw,64px);max-width:1400px;width:100%;box-sizing:border-box}.content header{display:flex;justify-content:space-between;align-items:flex-end;margin-bottom:28px}.content header .eyebrow{margin:0 0 6px}.actions{display:flex;align-items:center;gap:12px}.actions span{color:#747d99;font-size:12px}.actions button,.secondary{border:1px solid #ffffff18;background:#20263a;color:#eef0ff}.stats{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:14px;margin-bottom:18px}.stats article,.panel{border:1px solid #ffffff13;border-radius:14px;background:#121625;padding:20px}.stats article{display:grid;gap:7px}.stats span,.rows span{color:#8992ae;font-size:12px}.stats strong{font-size:30px}.stats small{color:#60d6a0}.panel{margin-bottom:18px}.panel h2{margin:0 0 5px;font-size:17px}.panel-title,.org-head,.workspace{display:flex;justify-content:space-between;align-items:center;gap:14px}.rows{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1px;margin-top:18px;background:#ffffff0c}.rows div{display:flex;justify-content:space-between;padding:14px;background:#121625}.badge{display:inline-flex;align-items:center;border-radius:999px;padding:4px 9px;font-size:11px;font-weight:750}.good{background:#22bd7b18;color:#7ce0ae}.warn{background:#f5ad4218;color:#ffc56f}.bad{background:#ef5b6818;color:#ff9da7}.org{margin-top:16px;border:1px solid #ffffff12;border-radius:12px;overflow:hidden}.org-head{padding:15px;background:#0b0e19}.org-head h3{margin:0 0 3px}.org-head span,.workspace>span{color:#8e96b0;font-size:12px}code{display:block;color:#737d99;font-size:11px}.workspace{padding:13px 15px;border-top:1px solid #ffffff0d}.workspace>div{min-width:240px;display:grid;gap:3px}.split{display:grid;grid-template-columns:1fr 1fr;gap:18px}.secondary,.danger{display:block;width:100%;margin-top:12px}.danger{border:1px solid #ef5b6855;background:#ef5b6815;color:#ff9da7}.dependencies{margin-top:14px}.dependencies>div{display:grid;grid-template-columns:14px 1fr auto;align-items:center;gap:10px;padding:12px 0;border-top:1px solid #ffffff0d}.dependencies small{display:block;color:#7f88a5}.dot{width:8px;height:8px;border-radius:50%}.boundary-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin:20px 0}.boundary-grid>div{padding:18px;border-radius:11px}.allowed{background:#22bd7b10}.denied{background:#ef5b6810}.boundary-grid h3{margin:0}.boundary-grid li{margin:9px 0;color:#adb4c9}.callout{padding:14px;border-left:3px solid #927cff;background:#795cff10}.empty{padding:20px 0}.panel-title>span{color:#858eaa;font-size:12px}@media(max-width:900px){.shell{grid-template-columns:1fr}aside{position:static;height:auto}.aside-foot{display:flex}.stats{grid-template-columns:repeat(2,1fr)}.split{grid-template-columns:1fr}.content header{align-items:flex-start;gap:18px}.workspace{align-items:flex-start;flex-wrap:wrap}.workspace>div{width:100%}}@media(max-width:560px){.login-links{display:grid}.stats,.boundary-grid,.rows{grid-template-columns:1fr}.content{padding:24px 16px}.content header{display:grid}.actions{justify-content:space-between}.stats strong{font-size:24px}}
</style>
