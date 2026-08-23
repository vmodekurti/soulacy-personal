<script>
  import PageHelp from './PageHelp.svelte'
  export let current = ''
  export let workspaceHref = '/workspace-login'
  export let workspaceEnabled = true
  export let helpPage = ''
</script>

<header class="public-header">
  <a class="brand" href="/" aria-label="Soulacy home">
    <span class="brand-mark" aria-hidden="true">⬡</span>
    <span>Soulacy</span>
  </a>
  <nav aria-label="Access navigation">
    <a class:active={current === 'home'} href="/" aria-current={current === 'home' ? 'page' : undefined}>Home</a>
    {#if workspaceEnabled}
      <a class:active={current === 'workspace'} href={workspaceHref} aria-current={current === 'workspace' ? 'page' : undefined}>Workspace login</a>
    {:else}
      <span class="unavailable" title="Workspace identity is not configured">Workspace login</span>
    {/if}
    <a class="admin" class:active={current === 'admin'} href="/admin" aria-current={current === 'admin' ? 'page' : undefined}>Deployment admin login</a>
    <a class="marketing" href="https://soulacy.io" target="_blank" rel="noreferrer">Soulacy.io ↗</a>
    {#if helpPage}<PageHelp page={helpPage} />{/if}
  </nav>
</header>

<style>
  .public-header{position:relative;z-index:10;display:flex;justify-content:space-between;align-items:center;box-sizing:border-box;width:min(1180px,calc(100% - 48px));margin:auto;padding:28px 0;border-bottom:1px solid #ffffff12}
  .brand{display:flex;align-items:center;gap:10px;color:#fff;text-decoration:none;font-size:18px;font-weight:800;letter-spacing:-.02em}
  .brand-mark{display:grid;place-items:center;width:34px;height:34px;border:1px solid #8e7bff66;border-radius:10px;background:#8e7bff15;color:#9d8cff;font-size:22px}
  nav{display:flex;align-items:center;gap:6px}
  nav a,nav span{box-sizing:border-box;padding:10px 13px;border-radius:9px;color:#c8cbe0;text-decoration:none;font-size:13px;font-weight:700;white-space:nowrap}
  nav a:hover,nav a.active{background:#ffffff0c;color:#fff}
  nav a.admin{border:1px solid #ffffff20;background:#ffffff09}nav a.marketing{color:#7ce3b4}
  nav a.admin:hover,nav a.admin.active{border-color:#8e7bff66;background:#8e7bff18}
  .unavailable{opacity:.45;cursor:not-allowed}
  @media(max-width:680px){.public-header{width:min(100% - 32px);align-items:flex-start;padding:20px 0}.public-header nav{justify-content:flex-end;flex-wrap:wrap;gap:2px}.public-header nav a,.public-header nav span{padding:8px;font-size:11px}.public-header nav a.admin{border:0;background:transparent}}
</style>
