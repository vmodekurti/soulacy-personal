<script>
  import { onMount } from 'svelte'
  import ProviderIcon from '../lib/ProviderIcon.svelte'
  import PublicNav from '../lib/PublicNav.svelte'
  export let workspaceID
  let config=null, loading=true, error='', saving=false, complete=false
  const authErrorCode=new URLSearchParams(location.search).get('auth_error')||''
  const authErrors={cancelled:'Sign-in was cancelled. No changes were made.',session_expired:'That sign-in attempt expired. Please start again.',not_authorized:'Your account is not authorized for this workspace. Ask a workspace administrator to invite you.',workspace_unavailable:'This workspace is temporarily unavailable and is not accepting sign-ins.',sign_in_failed:'We couldn’t complete sign-in. Please try again or contact your workspace administrator.'}
  let authError=authErrors[authErrorCode]||''
  const query=new URLSearchParams(location.search)
  let token=query.get('token')||''
  let invitationToken=query.get('invite')||''
  let canonicalWorkspaceID=''
  $: workspaceLoginURL=`/api/v1/auth/oidc/start?client=gui&navigate=true&workspace_id=${encodeURIComponent(canonicalWorkspaceID||workspaceID)}&return_to=${encodeURIComponent('/#dashboard')}`
  let provider='google',issuer='https://accounts.google.com',clientId='',clientSecret='',audience='',workspaceLogo=''
  const providers=[['google','Google Workspace','https://accounts.google.com'],['microsoft','Microsoft Entra ID',''],['okta','Okta',''],['auth0','Auth0',''],['keycloak','Keycloak',''],['custom','Custom OIDC','']]
  onMount(load)
  async function load(){loading=true;try{const r=await fetch(`/api/v1/auth/workspaces/${encodeURIComponent(workspaceID)}/config`);const j=await r.json().catch(()=>({}));if(!r.ok){config=j.workspace||null;throw new Error(j.error||'Workspace was not found.')}config=j.workspace;canonicalWorkspaceID=config.workspace_id;try{const saved=JSON.parse(localStorage.getItem('soulacy.recentWorkspaces')||'[]');const item={id:config.workspace_id,slug:config.workspace_slug||config.workspace_id,name:config.workspace_name,organization:config.organization_name,logo:config.workspace_logo||config.organization_logo||''};localStorage.setItem('soulacy.recentWorkspaces',JSON.stringify([item,...saved.filter((entry)=>entry.id!==item.id)].slice(0,4)))}catch(_){}}catch(e){error=e.message}finally{loading=false}}
  function choose(id,defaultIssuer){provider=id;issuer=defaultIssuer}
  function readLogo(event){const file=event.target.files?.[0];if(!file)return;if(file.size>500000){error='Logo must be smaller than 500 KB.';return}const reader=new FileReader();reader.onload=()=>workspaceLogo=String(reader.result||'');reader.readAsDataURL(file)}
  async function activate(){saving=true;error='';try{const r=await fetch(`/api/v1/auth/workspaces/${encodeURIComponent(workspaceID)}/setup`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({setup_token:token,provider_type:provider,issuer,client_id:clientId,client_secret:clientSecret,audience,scopes:['openid','profile','email'],workspace_logo:workspaceLogo})});const j=await r.json();if(!r.ok)throw new Error(j.error||'Provider could not be activated.');complete=true;await load()}catch(e){error=e.message}finally{saving=false}}
  async function signIn(){
    if(!invitationToken){location.assign(workspaceLoginURL);return}
    error=''
    try{
      const response=await fetch('/api/v1/auth/oidc/start',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({client:'gui',workspace_id:canonicalWorkspaceID||workspaceID,invitation_token:invitationToken,return_to:'/#dashboard'})})
      const body=await response.json()
      if(!response.ok||!body.authorization_url)throw new Error('Workspace sign-in could not be started.')
      location.assign(body.authorization_url)
    }catch(cause){error=cause?.message||'Workspace sign-in could not be started.'}
  }
</script>
<svelte:head><title>{config?.workspace_name||'Workspace'} · Soulacy</title></svelte:head>
<div class="access-shell"><PublicNav current="workspace" helpPage="workspace-login" /><main><section class="card">
  {#if loading}<p>Loading workspace…</p>
  {:else if config}
    <div class="brands">{#if config.organization_logo}<img src={config.organization_logo} alt="{config.organization_name} logo" />{/if}{#if config.workspace_logo}<img src={config.workspace_logo} alt="{config.workspace_name} logo" />{/if}</div>
    <p class="eyebrow">{config.organization_name}</p><h1>{config.workspace_name}</h1>
    {#if config.identity_status === 'active'}
      <p class="lead">{invitationToken?'Sign in with the invited email address to accept your workspace invitation.':'Sign in using this workspace’s identity provider.'}</p><button class="primary signin" on:click={signIn}><ProviderIcon provider={config.provider_type}/><span>Continue with {providers.find(p=>p[0]===config.provider_type)?.[1]||'your organization'}</span></button>
    {:else if token}
      <p class="lead">Choose the identity provider for this workspace. After activation, the provider and issuer are locked.</p>
      <div class="providers">{#each providers as item}<button class:chosen={provider===item[0]} on:click={()=>choose(item[0],item[2])}><ProviderIcon provider={item[0]}/><span>{item[1]}</span></button>{/each}</div>
      <div class="grid"><label>Issuer URL<input type="url" bind:value={issuer} required /></label><label>Client ID<input bind:value={clientId} required /></label><label>Client secret <small>(if required)</small><input type="password" bind:value={clientSecret}/></label><label>Audience <small>(defaults to client ID)</small><input bind:value={audience}/></label></div>
      <label class="upload">Workspace logo <small>PNG, JPEG, or WebP · 500 KB maximum</small><input type="file" accept="image/png,image/jpeg,image/webp" on:change={readLogo}/></label>
      {#if workspaceLogo}<img class="preview" src={workspaceLogo} alt="Workspace logo preview" />{/if}
      <button class="primary" disabled={saving||!issuer.trim()||!clientId.trim()} on:click={activate}>{saving?'Validating provider…':'Validate and activate workspace'}</button>
    {:else}<p class="lead">Identity setup is pending. Ask the workspace administrator to use the one-time setup link.</p>{/if}
    {#if complete}<p class="success">Provider activated. This assignment is now locked.</p>{/if}
    {#if authError}<div class="auth-error" role="alert"><strong>Sign-in wasn’t completed</strong><span>{authError}</span></div>{/if}
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <a href="/">← Back to home</a>
</section></main></div>
<style>
  :global(body){margin:0;background:#090b14;color:#f2f3ff;font-family:Inter,system-ui,sans-serif}.access-shell{min-height:100vh;display:flex;flex-direction:column;background:radial-gradient(circle at 12% 8%,#34237366,transparent 36%),radial-gradient(circle at 88% 92%,#07654a55,transparent 36%)}main{flex:1;display:grid;place-items:center;padding:48px 24px 72px}.card{width:min(720px,100%);box-sizing:border-box;padding:38px;border:1px solid #ffffff18;border-radius:22px;background:#111522f2}.brands{display:flex;gap:10px}.brands img,.preview{width:64px;height:64px;object-fit:contain;border-radius:14px;background:#fff;padding:5px;box-sizing:border-box}.eyebrow{color:#70ddb0;text-transform:uppercase;letter-spacing:.14em;font-size:11px;font-weight:800;margin:22px 0 6px}h1{margin:0;font-size:36px}.lead{color:#a1a8bf;line-height:1.55}.providers{display:grid;grid-template-columns:repeat(3,1fr);gap:9px;margin:22px 0}.providers button,.signin{display:flex;align-items:center;gap:10px;border:1px solid #ffffff18;border-radius:10px;background:#191e30;color:#eef0ff;padding:11px;text-align:left;font-weight:700}.providers button.chosen{border-color:#8b75ff;background:#795cff20}.grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}label{display:grid;gap:6px;color:#c4cadc;font-size:12px;font-weight:650}input{box-sizing:border-box;width:100%;padding:11px;border:1px solid #ffffff20;border-radius:9px;background:#090c16;color:#fff}.upload{margin-top:14px}.primary{width:100%;margin:18px 0;border:0;border-radius:10px;padding:13px;background:linear-gradient(135deg,#795cff,#24bd7c);color:#fff;font-weight:800}.signin{justify-content:center}.error,.success,.auth-error{padding:12px;border-radius:9px}.error{background:#ef5b6818;color:#ff9da7}.success{background:#22bd7b18;color:#8ce4b9}.auth-error{display:grid;gap:4px;margin:14px 0;background:#ef5b6818;color:#ffb0b8}.auth-error span{font-size:12px;color:#d9a0a7}a{color:#9d91e8;text-decoration:none}@media(max-width:620px){.providers,.grid{grid-template-columns:1fr}.card{padding:24px}}
</style>
