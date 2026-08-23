<script>
  import PublicNav from '../lib/PublicNav.svelte'
  const workspaceLoginURL = '/workspace-login'
  const errorCode = new URLSearchParams(location.search).get('auth_error') || ''
  const errorMessages = {
    cancelled: 'Sign-in was cancelled. No changes were made.',
    session_expired: 'That sign-in attempt expired. Start again when you’re ready.',
    not_authorized: 'Your account is not authorized for a Soulacy workspace. Ask a workspace administrator for access.',
    sign_in_failed: 'We couldn’t complete sign-in. Please try again or contact your workspace administrator.',
  }
  const authError = errorMessages[errorCode] || ''
</script>

<svelte:head>
  <title>Soulacy · Secure AI operations</title>
  <meta name="description" content="Build, deploy, and operate AI agents across securely isolated workspaces." />
</svelte:head>

<main class="login-screen">
  <div class="aurora aurora-left" aria-hidden="true"></div>
  <div class="aurora aurora-right" aria-hidden="true"></div>

  <PublicNav current="home" workspaceHref={workspaceLoginURL} helpPage="access" />

  <section class="hero">
    <div class="hero-copy">
      <p class="eyebrow"><span></span> SECURE MULTI-WORKSPACE AI OPERATIONS</p>
      <h1>Build ambitious agents.<br /><em>Operate them with confidence.</em></h1>
      <p class="lead">Soulacy gives every organization a private, governed workspace for creating, deploying, and improving AI agents—without compromising the platform boundary.</p>

      {#if authError}
        <div class="auth-message" role="alert"><span aria-hidden="true">!</span><div><strong>Workspace sign-in wasn’t completed</strong><p>{authError}</p></div></div>
      {/if}

      <div class="access-grid" aria-label="Choose how to sign in">
        <a class="access-card primary workspace-login-link" href={workspaceLoginURL}>
          <span class="card-icon" aria-hidden="true">→</span>
          <span><strong>Workspace login</strong><small>Find your workspace and continue with its identity provider</small></span>
        </a>
        <a class="access-card deployment-admin-link" href="/admin">
          <span class="card-icon admin-icon" aria-hidden="true">⌘</span>
          <span><strong>Deployment admin login</strong><small>Manage the platform with the deployment API key</small></span>
        </a>
      </div>

      <div class="trust-row" aria-label="Platform qualities">
        <span>Tenant isolated</span><span>OIDC secured</span><span>Operationally observable</span>
      </div>
    </div>

    <div class="product-visual" aria-hidden="true">
      <div class="orbit orbit-one"></div><div class="orbit orbit-two"></div>
      <div class="core"><span class="core-mark">⬡</span><strong>Soulacy</strong><small>Agent operations</small></div>
      <div class="workspace-card card-one"><span class="status"></span><div><strong>Research</strong><small>12 agents · healthy</small></div></div>
      <div class="workspace-card card-two"><span class="status"></span><div><strong>Customer care</strong><small>8 agents · healthy</small></div></div>
      <div class="workspace-card card-three"><span class="status"></span><div><strong>Operations</strong><small>5 agents · healthy</small></div></div>
    </div>
  </section>

  <footer><span>Identity and platform administration stay deliberately separate.</span><span><a href="https://soulacy.io" target="_blank" rel="noreferrer">Soulacy.io</a> · © {new Date().getFullYear()} Soulacy</span></footer>
</main>

<style>
  :global(body){margin:0;background:#080a12;color:#f4f3ff;font-family:Inter,ui-sans-serif,system-ui,-apple-system,sans-serif}
  .login-screen{position:relative;min-height:100vh;overflow:hidden;background:linear-gradient(145deg,#090b14 0%,#101127 55%,#071a18 100%)}
  .aurora{position:absolute;border-radius:999px;filter:blur(90px);opacity:.32;pointer-events:none}.aurora-left{width:520px;height:520px;left:-220px;top:-240px;background:#7651ff}.aurora-right{width:520px;height:520px;right:-220px;bottom:-260px;background:#18bd7a}
  .hero{position:relative;z-index:1;display:grid;grid-template-columns:minmax(0,1.12fr) minmax(380px,.88fr);align-items:center;gap:72px;width:min(1180px,calc(100% - 48px));min-height:calc(100vh - 180px);margin:auto;padding:64px 0}.eyebrow{display:flex;align-items:center;gap:9px;margin:0 0 20px;color:#7ce3b4;font-size:11px;font-weight:850;letter-spacing:.16em}.eyebrow span{width:24px;height:1px;background:#62d9a4}.hero h1{margin:0;max-width:760px;font-size:clamp(46px,5.6vw,76px);line-height:1.02;letter-spacing:-.055em}.hero h1 em{font-style:normal;background:linear-gradient(100deg,#a28eff,#5ea7ff 48%,#48dda0);-webkit-background-clip:text;background-clip:text;color:transparent}.lead{max-width:650px;margin:24px 0 30px;color:#a7adc4;font-size:17px;line-height:1.7}
  .access-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px;max-width:720px}.access-card{display:flex;align-items:center;gap:13px;min-height:78px;padding:15px;border:1px solid #ffffff18;border-radius:14px;background:#111523cc;color:#f4f3ff;text-decoration:none;transition:transform .18s,border-color .18s,background .18s}.access-card:hover{transform:translateY(-2px);border-color:#9682ff77;background:#171b2d}.access-card.primary{border-color:#826cff66;background:linear-gradient(135deg,#7659ef33,#25bd7d1d)}.card-icon{display:grid;place-items:center;flex:0 0 42px;height:42px;border-radius:11px;background:linear-gradient(135deg,#775cff,#2fc58a);font-size:20px;font-weight:850}.admin-icon{background:#23283b;color:#aaa3d9}.access-card>span:last-child{display:grid;gap:4px}.access-card strong{font-size:14px}.access-card small{color:#9299b2;font-size:11px;line-height:1.35}.trust-row{display:flex;gap:24px;margin-top:25px;color:#7f879f;font-size:11px}.trust-row span::before{content:'✓';margin-right:7px;color:#54d99e}
  .auth-message{display:flex;align-items:flex-start;gap:12px;max-width:690px;margin:-5px 0 22px;padding:14px 16px;border:1px solid #ff8c9840;border-radius:13px;background:#e94f6114;color:#ffd7dc}.auth-message>span{display:grid;place-items:center;flex:0 0 26px;height:26px;border-radius:50%;background:#ff667633;font-weight:900}.auth-message strong{font-size:13px}.auth-message p{margin:4px 0 0;color:#d8aeb5;font-size:12px;line-height:1.45}
  .product-visual{position:relative;aspect-ratio:1;max-width:510px;margin:auto}.orbit{position:absolute;inset:14%;border:1px solid #8f7cff26;border-radius:50%;animation:spin 24s linear infinite}.orbit-two{inset:28%;border-color:#4bd89b30;animation-direction:reverse;animation-duration:18s}.core{position:absolute;inset:35%;display:grid;place-items:center;align-content:center;gap:3px;border:1px solid #ffffff20;border-radius:28px;background:linear-gradient(145deg,#1b1d36,#101a20);box-shadow:0 30px 80px #0008,0 0 80px #755cff20}.core-mark{color:#9e8cff;font-size:38px}.core strong{font-size:20px}.core small{color:#7f879f}.workspace-card{position:absolute;display:flex;align-items:center;gap:10px;min-width:150px;padding:13px;border:1px solid #ffffff18;border-radius:12px;background:#111522e8;box-shadow:0 16px 40px #0006}.workspace-card div{display:grid;gap:3px}.workspace-card strong{font-size:12px}.workspace-card small{color:#8189a2;font-size:9px}.status{width:8px;height:8px;border-radius:50%;background:#52da9c;box-shadow:0 0 12px #52da9c}.card-one{top:8%;left:2%}.card-two{right:-2%;top:39%}.card-three{bottom:8%;left:5%}@keyframes spin{to{transform:rotate(360deg)}}
  footer{position:relative;z-index:2;display:flex;justify-content:space-between;width:min(1180px,calc(100% - 48px));margin:auto;padding:22px 0;color:#676f88;font-size:11px;border-top:1px solid #ffffff0e}footer a{color:#8e96b1;text-decoration:none}
  @media(max-width:900px){.hero{grid-template-columns:1fr;padding:72px 0}.product-visual{display:none}.hero-copy{max-width:760px}}
  @media(max-width:620px){.hero{width:min(100% - 32px);min-height:auto;padding:64px 0 84px}.hero h1{font-size:42px}.lead{font-size:15px}.access-grid{grid-template-columns:1fr}.trust-row{display:grid;gap:8px}footer{width:min(100% - 32px);display:grid;gap:6px}}
  @media(prefers-reduced-motion:reduce){.orbit{animation:none}.access-card{transition:none}}
</style>
