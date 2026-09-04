<script>
  // Sandboxed host page for one plugin UI (Story E8). Fetches the plugin's
  // SCOPED token (bound to the plugin:<id> principal and its manifest
  // capabilities — never the user's API key) and embeds the static mount in
  // a sandboxed iframe. The token travels in the URL fragment, readable by
  // the plugin via location.hash but never sent to the server.
  import { api } from '../lib/api.js'
  import { iframeSrc, IFRAME_SANDBOX } from '../lib/pluginui.js'

  export let pluginId = ''
  export let label = ''
  export let url = ''

  let src = ''
  let error = ''
  let loading = true

  $: pluginId, load()

  async function load() {
    if (!pluginId || !url) return
    loading = true
    error = ''
    src = ''
    try {
      const res = await api.plugins.token(pluginId)
      src = iframeSrc(url, res?.token || '')
    } catch (e) {
      error = e.message || 'failed to fetch plugin token'
    } finally {
      loading = false
    }
  }

  // ARCH-6: the Studio UI no longer runs in this iframe, so the host-mediated
  // postMessage RPC bridge that used to relay Studio's compile/test/save/etc.
  // calls has been removed. Studio is now a first-class dashboard page
  // (pages/Studio.svelte) that calls /api/v1/studio/* directly with the user's
  // session. PluginFrame remains the generic host for OTHER plugin UIs: it
  // fetches a scoped token and mounts the plugin in a sandboxed iframe, with no
  // privileged message bridge.
</script>

<div class="page plugin-page">
  <div class="page-header">
    <h1>{label || pluginId}</h1>
    <span class="principal" title="This UI runs as a scoped plugin principal, not as you">
      plugin:{pluginId}
    </span>
  </div>

  {#if loading}
    <div class="state">Loading plugin UI…</div>
  {:else if error}
    <div class="state error">⚠ {error}</div>
  {:else}
    <iframe
      title={label || pluginId}
      {src}
      sandbox={IFRAME_SANDBOX}
      referrerpolicy="no-referrer"
    ></iframe>
  {/if}
</div>

<style>
  .plugin-page { display: flex; flex-direction: column; height: 100%; }
  .page-header { display: flex; align-items: baseline; gap: 0.75rem; }
  .principal {
    font-family: ui-monospace, monospace;
    font-size: 0.75rem;
    color: #8a91b4;
    border: 1px solid #2a2f4a;
    border-radius: 4px;
    padding: 0.1rem 0.4rem;
  }
  iframe {
    flex: 1;
    width: 100%;
    min-height: 70vh;
    border: 1px solid #2a2f4a;
    border-radius: 8px;
    background: #fff;
  }
  .state { padding: 2rem; color: #8a91b4; }
  .state.error { color: #ff7676; }
</style>
