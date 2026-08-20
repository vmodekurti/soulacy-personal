<script>export let provider = 'custom'; export let size = 28</script>
<span class="provider-icon {provider}" style="--size:{size}px" aria-hidden="true">
  {#if provider === 'google'}<b>G</b>
  {:else if provider === 'microsoft'}<i></i><i></i><i></i><i></i>
  {:else if provider === 'okta'}<b>O</b>
  {:else if provider === 'auth0'}<b>◆</b>
  {:else if provider === 'keycloak'}<b>⌘</b>
  {:else}<b>◉</b>{/if}
</span>
<style>
  .provider-icon{width:var(--size);height:var(--size);display:inline-grid;place-items:center;flex:0 0 auto;border-radius:8px;background:#fff;color:#202124;font:800 calc(var(--size)*.55)/1 system-ui;box-sizing:border-box}.google b{background:conic-gradient(from -40deg,#4285f4,#34a853,#fbbc05,#ea4335,#4285f4);background-clip:text;color:transparent}.microsoft{grid-template-columns:1fr 1fr;grid-template-rows:1fr 1fr;gap:2px;padding:4px}.microsoft i:nth-child(1){background:#f25022}.microsoft i:nth-child(2){background:#7fba00}.microsoft i:nth-child(3){background:#00a4ef}.microsoft i:nth-child(4){background:#ffb900}.okta{color:#1662dd}.auth0{color:#eb5424}.keycloak{color:#4d8fcc}.custom{color:#785cff}
</style>
