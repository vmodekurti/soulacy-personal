<script>
  // WorkspaceSwitcher — MU-029 criteria 1 and 5 in the global shell.
  //
  // ALWAYS VISIBLE, never a menu item you have to go looking for. The question
  // this answers is "where will this action happen", and it is asked at the
  // moment of acting, not at the moment of navigating. A selector hidden
  // behind a settings page is a selector nobody consults before pressing
  // Delete.
  //
  // In a personal deployment there is one workspace and nothing to choose
  // between, so the whole component renders nothing — product invariant 7 says
  // a single-user installation must not notice that the storage layer became
  // tenant-aware, and a chrome element that only ever names one thing is
  // exactly the sort of noticing it must not do.
  import { onMount } from 'svelte'
  import { api } from './api.js'
  import {
    activeWorkspace, selectableWorkspaces, workspaceAccessState,
    switchWorkspace, resolveDeepLink, accessStateFor, accessStateForList,
    workspaceLabel, normalizeWorkspace, permissions,
    rememberedWorkspaceID, forgetWorkspaceSelection,
  } from './workspace.js'

  let open = false
  let switching = ''
  let loaded = false

  $: label = workspaceLabel($activeWorkspace)
  $: banner = $workspaceAccessState
  // Multi-user only. `deployment_mode` comes from the server rather than being
  // inferred from "more than one workspace in the list": a Team deployment
  // where you currently belong to exactly one workspace is still a Team
  // deployment, and hiding the indicator there would hide the answer to
  // "where am I" precisely where it starts to matter.
  $: multiUser = !!$activeWorkspace && $activeWorkspace.deploymentMode &&
                 $activeWorkspace.deploymentMode !== 'personal'

  onMount(load)

  async function load() {
    try {
      let identity
      try {
        identity = await api.workspace.identity()
      } catch (e) {
        // A remembered selection can outlive a membership or a login. Drop
        // only that tab-local hint and retry once; the server then resolves
        // the token's verified default workspace.
        if (rememberedWorkspaceID() && (e?.status === 403 || e?.status === 404)) {
          forgetWorkspaceSelection()
          activeWorkspace.set(null)
          identity = await api.workspace.identity()
        } else {
          throw e
        }
      }
      activeWorkspace.set(normalizeWorkspace(identity))
      permissions.set(identity?.permissions || {})
    } catch (e) {
      // A failure here is not fatal to the app — the rest of the GUI still
      // works against whatever workspace the server resolves — but it must be
      // visible, because every other surface will now be showing data from a
      // workspace this component cannot name.
      workspaceAccessState.set(accessStateFor(e))
      loaded = true
      return
    }
    try {
      const listed = await api.workspace.list()
      const workspaces = listed?.workspaces || []
      selectableWorkspaces.set(workspaces)
      const listState = accessStateForList(workspaces)
      if (listState.state !== 'ok') workspaceAccessState.set(listState)
      await followDeepLink(workspaces)
    } catch (e) {
      // Listing is unavailable in deployments that cannot enumerate
      // memberships. That is not an error state for the user — they simply
      // cannot switch — so the indicator stays and the picker does not.
      selectableWorkspaces.set([])
    }
    loaded = true
  }

  // Criterion 3. `#ws=<id>` in the fragment is a request, verified against the
  // membership list before it moves anything. The fragment is cleared either
  // way so a refused link does not re-fire on every reload.
  async function followDeepLink(workspaces) {
    const match = /(?:^|[#&])ws=([^&]+)/.exec(window.location.hash || '')
    if (!match) return
    const requested = decodeURIComponent(match[1])
    const outcome = resolveDeepLink(requested, workspaces, $activeWorkspace?.workspaceId)
    clearWorkspaceFragment()
    if (outcome.action === 'switch') {
      await select(outcome.workspaceId)
    } else if (outcome.action === 'redirect') {
      workspaceAccessState.set({ state: outcome.state, detail: outcome.detail, workspaceName: '' })
    }
  }

  function clearWorkspaceFragment() {
    const cleaned = (window.location.hash || '').replace(/(?:^|[#&])ws=[^&]*/, '')
    const next = cleaned === '#' ? '' : cleaned
    window.history.replaceState(null, '', window.location.pathname + window.location.search + next)
  }

  async function select(workspaceId) {
    if (switching) return
    switching = workspaceId
    open = false
    try {
      await switchWorkspace(api.workspace, workspaceId)
      // A full reload is the honest way to re-fetch every page's data. The
      // stores are cleared by switchWorkspace, but mounted components hold
      // their own local copies — a page that fetched agents into a `let` is
      // not something the store registry can reach, and asking every page to
      // subscribe to a workspace change is a rule each new page can forget.
      window.location.reload()
    } catch (_) {
      // switchWorkspace already set the recovery banner.
    }
    switching = ''
  }

  function dismiss() {
    workspaceAccessState.set({ state: 'ok', detail: '', workspaceName: '' })
  }
</script>

{#if multiUser}
  <div class="ws-shell">
    <button
      class="ws-current"
      on:click={() => open = !open}
      disabled={!!switching}
      aria-haspopup="listbox"
      aria-expanded={open}
      title={label ? `Actions happen in ${label}` : 'Workspace'}
    >
      {#if $activeWorkspace.workspaceLogo || $activeWorkspace.organizationLogo}
        <img class="ws-logo" src={$activeWorkspace.workspaceLogo || $activeWorkspace.organizationLogo} alt="" />
      {/if}
      <span class="ws-label">{label || 'No workspace'}</span>
      {#if $selectableWorkspaces.length > 1}<span class="ws-caret">▾</span>{/if}
    </button>

    {#if open && $selectableWorkspaces.length}
      <ul class="ws-menu" role="listbox">
        {#each $selectableWorkspaces as ws (ws.workspace_id)}
          {@const normalized = normalizeWorkspace(ws)}
          <li>
            <button
              role="option"
              aria-selected={ws.workspace_id === $activeWorkspace?.workspaceId}
              class:active={ws.workspace_id === $activeWorkspace?.workspaceId}
              on:click={() => select(ws.workspace_id)}
              disabled={!!switching}
            >
              <span class="ws-menu-main">
                {#if normalized.workspaceLogo || normalized.organizationLogo}
                  <img class="ws-logo" src={normalized.workspaceLogo || normalized.organizationLogo} alt="" />
                {/if}
                <span class="ws-menu-name">{workspaceLabel(normalized) || ws.workspace_id}</span>
              </span>
              {#if ws.role}<span class="ws-menu-role">{ws.role}</span>{/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/if}

{#if banner.state !== 'ok' && loaded}
  <!--
    Criterion 5. One banner, one recovery path, and no resource names: telling
    an ex-member that "ws_acme is suspended" confirms both that ws_acme exists
    and that they were in it.
  -->
  <div class="ws-banner" class:severe={banner.state === 'expired' || banner.state === 'revoked'} role="status">
    <p>{banner.detail}</p>
    {#if banner.state === 'expired'}
      <button on:click={() => window.location.reload()}>Sign in again</button>
    {:else if banner.state === 'revoked' && $selectableWorkspaces.length}
      <button on:click={() => { dismiss(); open = true }}>Choose a workspace</button>
    {:else if banner.state === 'unavailable'}
      <button on:click={() => { dismiss(); load() }}>Retry</button>
    {:else}
      <button on:click={dismiss}>Dismiss</button>
    {/if}
  </div>
{/if}

<style>
  .ws-shell { position: relative; padding: 0 .75rem .6rem; }
  .ws-current {
    display: flex; align-items: center; justify-content: space-between; gap: .4rem;
    width: 100%; padding: .4rem .6rem; border-radius: 6px; cursor: pointer;
    background: rgba(126, 92, 255, .12); border: 1px solid rgba(126, 92, 255, .35);
    color: #e8e9f5; font-size: .82rem; text-align: left;
  }
  .ws-current:disabled { opacity: .6; cursor: progress; }
  .ws-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ws-logo { width: 1.45rem; height: 1.45rem; flex: 0 0 auto; border-radius: 5px; object-fit: contain; background: rgba(255,255,255,.08); }
  .ws-menu-main { min-width: 0; display: flex; align-items: center; gap: .45rem; }
  .ws-menu-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ws-caret { opacity: .7; }
  .ws-menu {
    position: absolute; left: .75rem; right: .75rem; z-index: 40; margin: .25rem 0 0;
    padding: .25rem; list-style: none; border-radius: 6px;
    background: #141824; border: 1px solid #2a3140; box-shadow: 0 8px 24px rgba(0,0,0,.45);
  }
  .ws-menu button {
    display: flex; justify-content: space-between; gap: .5rem; width: 100%;
    padding: .4rem .5rem; border: 0; border-radius: 4px; background: transparent;
    color: #e8e9f5; cursor: pointer; font-size: .8rem; text-align: left;
  }
  .ws-menu button:hover:not(:disabled) { background: rgba(255,255,255,.07); }
  .ws-menu button.active { background: rgba(126, 92, 255, .18); }
  .ws-menu-role { opacity: .6; font-size: .72rem; }
  .ws-banner {
    margin: 0 .75rem .6rem; padding: .55rem .65rem; border-radius: 6px;
    background: rgba(240, 180, 60, .12); border: 1px solid rgba(240, 180, 60, .4);
    color: #f3e6c8; font-size: .78rem;
  }
  .ws-banner.severe {
    background: rgba(230, 90, 90, .13); border-color: rgba(230, 90, 90, .45); color: #f5d9d9;
  }
  .ws-banner p { margin: 0 0 .4rem; }
  .ws-banner button {
    padding: .25rem .55rem; border-radius: 4px; cursor: pointer;
    background: rgba(255,255,255,.09); border: 1px solid rgba(255,255,255,.18); color: inherit;
    font-size: .75rem;
  }
</style>
