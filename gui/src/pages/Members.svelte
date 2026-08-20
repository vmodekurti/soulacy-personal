<script>
  import { onMount } from 'svelte'
  import { confirmDestructive } from '../lib/destructive.js'
  import TourButton from '../lib/TourButton.svelte'
  import { api } from '../lib/api.js'

  const roles = ['viewer', 'operator', 'developer', 'admin', 'owner']
  const roleRank = { viewer: 1, operator: 2, developer: 3, admin: 4, owner: 5 }
  let members = []
  let invitations = []
  let audit = []
  let email = ''
  let role = 'viewer'
  let expiresIn = '168h'
  let loading = true
  let saving = false
  let error = ''
  let notice = ''
  let inviteToken = ''
  let invitationURL = ''
  let acceptanceToken = ''
  let accepting = false
  let currentRole = ''
  $: assignableRoles = roles.filter((candidate) => roleRank[candidate] <= (roleRank[currentRole] || 0))
  const manageableRoles = (member) => roles.filter((candidate) => candidate === member.role || roleRank[candidate] <= (roleRank[currentRole] || 0))

  async function load() {
    loading = true
    error = ''
    try {
      const [memberData, inviteData] = await Promise.all([
        api.workspaceMembers.list(), api.workspaceMembers.invitations(),
      ])
      members = Array.isArray(memberData?.members) ? memberData.members : []
      currentRole = memberData?.current_role || ''
      invitations = Array.isArray(inviteData?.invitations) ? inviteData.invitations : []
      try {
        const auditData = await api.workspaceMembers.audit()
        audit = Array.isArray(auditData?.events) ? auditData.events : []
      } catch (_) { audit = [] } // admins can manage members; audit is owner-only
    } catch (e) {
      error = e?.status === 403
        ? 'Workspace owner or admin access is required to manage members.'
        : (e.message || 'Members could not be loaded.')
    } finally { loading = false }
  }

  async function invite() {
    if (!email.trim() || saving) return
    saving = true; error = ''; notice = ''; inviteToken = ''
    try {
      const created = await api.workspaceMembers.invite(email.trim(), role, expiresIn)
      inviteToken = created.token || ''
      invitationURL = inviteToken ? `${location.origin}${location.pathname}#members?invite=${encodeURIComponent(inviteToken)}` : ''
      notice = `Invitation created for ${created.email}. The token is shown once.`
      email = ''
      await load()
      notice = `Invitation created for ${created.email}. The token is shown once.`
      inviteToken = created.token || ''
      invitationURL = inviteToken ? `${location.origin}${location.pathname}#members?invite=${encodeURIComponent(inviteToken)}` : ''
    } catch (e) { error = e.message } finally { saving = false }
  }

  async function setRole(member, nextRole) {
    if (nextRole === member.role) return
    error = ''; notice = ''
    try {
      await api.workspaceMembers.setRole(member.id, nextRole)
      notice = `${member.display_name || member.email || member.user_id} is now ${nextRole}.`
      await load()
    } catch (e) { const message = e.message; await load(); error = message }
  }

  async function setStatus(member, status) {
    error = ''; notice = ''
    try {
      await api.workspaceMembers.setStatus(member.id, status)
      notice = `${member.display_name || member.email || member.user_id} is now ${status}.`
      await load()
    } catch (e) { const message = e.message; await load(); error = message }
  }

  async function removeMember(member) {
    if (!confirmDestructive(`Remove ${member.display_name || member.email || 'this member'} from the workspace?`)) return
    error = ''; notice = ''
    try {
      await api.workspaceMembers.remove(member.id)
      notice = 'Member removed. Their next workspace request will be denied.'
      await load()
    } catch (e) { const message = e.message; await load(); error = message }
  }

  async function copyToken() {
    try { await navigator.clipboard.writeText(invitationURL || inviteToken); notice = 'Invitation link copied.' }
    catch (_) { error = 'Copy failed. Select and copy the token manually.' }
  }

  async function acceptInvitation() {
    if (!acceptanceToken.trim() || accepting) return
    accepting = true; error = ''; notice = ''
    try {
      await api.workspaceMembers.accept(acceptanceToken.trim())
      notice = 'Invitation accepted. Your workspace access is active.'
      acceptanceToken = ''
      history.replaceState(null, '', `${location.pathname}#members`)
      await load()
    } catch (e) { error = e.message || 'The invitation is invalid or expired.' }
    finally { accepting = false }
  }

  onMount(() => {
    const query = location.hash.includes('?') ? location.hash.split('?')[1] : ''
    acceptanceToken = new URLSearchParams(query).get('invite') || ''
    if (acceptanceToken) loading = false
    else load()
  })
</script>

<div class="page">
  <header class="page-header">
    <div><h1>Workspace members</h1><p>Invite teammates and change access without restarting the gateway.</p></div>
    <div class="header-actions"><button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button><TourButton /></div>
  </header>

  {#if error}<div class="msg err">{error}</div>{/if}
  {#if notice}<div class="msg ok">✓ {notice}</div>{/if}
  {#if inviteToken}
    <div class="token-card"><strong>One-time invitation link</strong><code>{invitationURL || inviteToken}</code><button class="btn-primary" on:click={copyToken}>Copy link</button><small>Send it through a secure channel. Soulacy stores only the token hash.</small></div>
  {/if}

  <section class="card accept-card">
    <div><h2>Accept an invitation</h2><p class="hint">Sign in with the verified email address that received the invitation.</p></div>
    <input aria-label="Invitation token" bind:value={acceptanceToken} placeholder="Paste invitation token" />
    <button class="btn-primary" on:click={acceptInvitation} disabled={accepting || !acceptanceToken.trim()}>{accepting ? 'Accepting…' : 'Accept invitation'}</button>
  </section>

  {#if currentRole === 'owner' || currentRole === 'admin'}
  <section class="card">
    <h2>Invite a teammate</h2>
    <div class="invite-row">
      <label>Email<input type="email" bind:value={email} placeholder="teammate@example.com" /></label>
      <label>Role<select bind:value={role}>{#each assignableRoles as item}<option value={item}>{item}</option>{/each}</select></label>
      <label>Expires<select bind:value={expiresIn}><option value="24h">1 day</option><option value="168h">7 days</option><option value="336h">14 days</option><option value="720h">30 days</option></select></label>
      <button class="btn-primary" on:click={invite} disabled={saving || !email.trim()}>{saving ? 'Creating…' : 'Create invitation'}</button>
    </div>
    <p class="hint">Choose the least privilege needed. You cannot grant a role above your own.</p>
  </section>

  <section class="card">
    <h2>Members</h2>
    {#if loading}<p class="hint">Loading members…</p>
    {:else if members.length === 0}<p class="hint">No members are visible in this workspace.</p>
    {:else}
      <div class="table-wrap"><table><thead><tr><th>Member</th><th>Role</th><th>Status</th><th>Actions</th></tr></thead><tbody>
      {#each members as member (member.id)}
        <tr>
          <td><strong>{member.display_name || member.email || member.user_id}</strong>{#if member.email}<small>{member.email}</small>{/if}</td>
          <td><select value={member.role} disabled={roleRank[member.role] > roleRank[currentRole]} on:change={(e) => setRole(member, e.currentTarget.value)}>{#each manageableRoles(member) as item}<option value={item}>{item}</option>{/each}</select></td>
          <td><span class:active={member.status === 'active'} class="status">{member.status}</span></td>
          <td class="actions">{#if member.status === 'active'}<button class="btn-secondary" on:click={() => setStatus(member, 'suspended')}>Suspend</button>{:else}<button class="btn-secondary" on:click={() => setStatus(member, 'active')}>Restore</button>{/if}<button class="btn-danger" on:click={() => removeMember(member)}>Remove</button></td>
        </tr>
      {/each}
      </tbody></table></div>
    {/if}
  </section>

  <section class="grid">
    <div class="card"><h2>Invitations</h2>{#if invitations.length === 0}<p class="hint">No invitations yet.</p>{:else}<ul>{#each invitations as invitation (invitation.id)}<li><span>{invitation.email}</span><span>{invitation.role}</span><span class="status" class:active={invitation.status === 'pending'}>{invitation.status}</span><small>{new Date(invitation.expires_at).toLocaleString()}</small></li>{/each}</ul>{/if}</div>
    <div class="card"><h2>Membership audit</h2>{#if audit.length === 0}<p class="hint">Audit history is visible to workspace owners.</p>{:else}<ul>{#each audit as event (event.id)}<li><strong>{event.action}</strong><span>{event.actor_subject}</span><small>{new Date(event.created_at).toLocaleString()}</small></li>{/each}</ul>{/if}</div>
  </section>
  {/if}
</div>

<style>
  .page{display:flex;flex-direction:column;gap:16px;max-width:1500px;margin:0 auto}.page-header,.header-actions,.invite-row,.actions,.accept-card{display:flex;align-items:center;gap:10px}.page-header{justify-content:space-between}.page-header h1{margin:0}.page-header p,.hint,small{color:var(--muted,#9298b0);font-size:.82rem}.card,.token-card{background:var(--panel,#151a2c);border:1px solid var(--border,#2a3048);border-radius:10px;padding:16px}.card h2{margin:0 0 12px;font-size:1rem}.accept-card>div{flex:1}.accept-card h2,.accept-card p{margin:0}.accept-card input{min-width:280px}.msg{padding:10px 12px;border-radius:8px}.msg.err{background:#391c24;color:#ff9fae}.msg.ok{background:#173126;color:#7fe0a5}.token-card{display:grid;grid-template-columns:auto 1fr auto;align-items:center;gap:12px;border-color:#6d5df0}.token-card small{grid-column:2/4}.token-card code{overflow-wrap:anywhere;background:#0c1020;padding:9px;border-radius:6px}.invite-row{align-items:end;flex-wrap:wrap}.invite-row label{display:grid;gap:5px;flex:1;min-width:180px}input,select{background:#0c1020;color:inherit;border:1px solid var(--border,#2a3048);border-radius:6px;padding:8px 10px}.table-wrap{overflow-x:auto}table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:10px;border-bottom:1px solid var(--border,#2a3048)}td:first-child{display:grid;gap:3px}.status{border-radius:999px;padding:3px 8px;background:#39262b;color:#ffb1bd}.status.active{background:#173126;color:#7fe0a5}.btn-danger{background:#3a1a22;color:#ff9fae;border:1px solid #69303d;border-radius:6px;padding:6px 10px}.grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}ul{list-style:none;margin:0;padding:0}li{display:grid;grid-template-columns:1fr auto auto;gap:10px;align-items:center;padding:9px 0;border-bottom:1px solid var(--border,#2a3048)}li small{grid-column:1/-1}@media(max-width:800px){.page-header{align-items:flex-start}.grid{grid-template-columns:1fr}.token-card{grid-template-columns:1fr}.token-card small{grid-column:auto}.actions,.accept-card{align-items:stretch;flex-direction:column}.accept-card input{min-width:0;width:100%}}
</style>
