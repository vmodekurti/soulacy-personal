<script>
  import { createEventDispatcher } from 'svelte'
  import BrandMark from '../lib/BrandMark.svelte'

  export let loginKey = ''
  export let loginError = ''
  export let loginChecking = false

  const dispatch = createEventDispatcher()
  const year = new Date().getFullYear()

  function submit() {
    if (!loginChecking && loginKey.trim()) dispatch('login')
  }
</script>

<svelte:head>
  <title>Soulacy Personal · Your agents, on your terms</title>
  <meta name="description" content="Build, run, and automate with private AI agents in your own Soulacy Personal environment." />
</svelte:head>

<main class="personal-landing">
  <div class="aurora aurora-violet" aria-hidden="true"></div>
  <div class="aurora aurora-green" aria-hidden="true"></div>

  <header class="public-header">
    <a class="brand" href="#personal-home" aria-label="Soulacy Personal home">
      <BrandMark size={38} />
      <span class="brand-copy"><strong>soulacy</strong><small>Personal</small></span>
    </a>
    <nav aria-label="Personal edition navigation">
      <a class="nav-link" href="#why-personal">Why Personal</a>
      <a class="nav-link" href="https://github.com/vmodekurti/soulacy-personal" target="_blank" rel="noreferrer">GitHub <span aria-hidden="true">↗</span></a>
      <a class="login-link" href="#personal-login">Log in</a>
    </nav>
  </header>

  <section class="hero" id="personal-home">
    <div class="hero-copy">
      <p class="eyebrow"><span></span> OPEN-SOURCE · SELF-HOSTED · PERSONAL AI</p>
      <h1>Your agents.<br /><em>Your machine. Your rules.</em></h1>
      <p class="lead">Build capable AI agents, connect the models and tools you choose, and keep the entire workspace under your control.</p>

      <div class="hero-actions">
        <a class="primary-action" href="#personal-login">Open your workspace <span aria-hidden="true">→</span></a>
        <a class="secondary-action" href="#why-personal">Explore Personal</a>
      </div>

      <div class="trust-row" aria-label="Personal edition qualities">
        <span>Self-hosted</span><span>Model flexible</span><span>Open source</span>
      </div>
    </div>

    <div class="login-panel" id="personal-login">
      <div class="panel-topline">
        <BrandMark size={32} />
        <span class="status"><i></i> Personal gateway</span>
      </div>
      <h2>Welcome back</h2>
      <p class="panel-lead">Log in to build, deploy, and work with your agents.</p>

      <form on:submit|preventDefault={submit}>
        <label for="personal-api-key">Soulacy API key</label>
        <div class="key-field">
          <span aria-hidden="true">⌁</span>
          <input
            id="personal-api-key"
            type="password"
            autocomplete="current-password"
            placeholder="sy_…"
            bind:value={loginKey}
            disabled={loginChecking}
          />
        </div>

        {#if loginError}
          <p class="login-error" role="alert">{loginError}</p>
        {/if}

        <button class="login-submit" type="submit" disabled={loginChecking || !loginKey.trim()}>
          {loginChecking ? 'Verifying…' : 'Log in securely'}
          {#if !loginChecking}<span aria-hidden="true">→</span>{/if}
        </button>
      </form>

      <p class="session-note"><span aria-hidden="true">◈</span> This browser stays trusted for up to 30 days. Your API key is exchanged for secure, HTTP-only session cookies.</p>
      <details>
        <summary>Where do I find my key?</summary>
        <p>Look in <code>~/.soulacy/soulspace/config.yaml</code> under <code>server.api_key</code>, or use the <code>SOULACY_API_KEY</code> environment variable.</p>
      </details>
    </div>
  </section>

  <section class="why" id="why-personal" aria-labelledby="why-title">
    <div class="section-heading">
      <p class="eyebrow"><span></span> ONE PRIVATE OPERATING SPACE</p>
      <h2 id="why-title">Everything you need to put personal agents to work.</h2>
      <p>Go from an idea to a working agent without giving up ownership of your models, data, or runtime.</p>
    </div>

    <div class="feature-grid">
      <article>
        <span class="feature-icon" aria-hidden="true">✦</span>
        <h3>Build visually</h3>
        <p>Shape agent behavior, tools, knowledge, schedules, and outputs in one focused studio.</p>
      </article>
      <article>
        <span class="feature-icon" aria-hidden="true">⌘</span>
        <h3>Bring your models</h3>
        <p>Choose the provider and model that fit each agent instead of locking your work to one stack.</p>
      </article>
      <article>
        <span class="feature-icon" aria-hidden="true">◇</span>
        <h3>Keep control</h3>
        <p>Run Soulacy in your environment with your credentials, data boundaries, and operating choices.</p>
      </article>
      <article>
        <span class="feature-icon" aria-hidden="true">↻</span>
        <h3>Automate the routine</h3>
        <p>Schedule recurring work, inspect every run, and keep useful agents improving over time.</p>
      </article>
    </div>
  </section>

  <section class="closing">
    <div>
      <p class="eyebrow"><span></span> YOUR WORKSPACE IS READY</p>
      <h2>Pick up where your agents left off.</h2>
    </div>
    <a class="primary-action" href="#personal-login">Log in to Soulacy <span aria-hidden="true">→</span></a>
  </section>

  <footer>
    <span>Soulacy Personal · AI agents under your control.</span>
    <span><a href="https://github.com/vmodekurti/soulacy-personal" target="_blank" rel="noreferrer">Open source on GitHub</a> · © {year} Soulacy</span>
  </footer>
</main>

<style>
  :global(body) { margin: 0; background: #080a12; color: #f4f3ff; font-family: Inter, ui-sans-serif, system-ui, -apple-system, sans-serif; }
  .personal-landing { position: fixed; inset: 0; z-index: 1000; overflow-x: hidden; overflow-y: auto; scroll-behavior: smooth; background: linear-gradient(145deg, #080a13 0%, #101127 51%, #071916 100%); color: #f4f3ff; }
  .aurora { position: absolute; border-radius: 999px; filter: blur(100px); opacity: .3; pointer-events: none; }
  .aurora-violet { width: 600px; height: 600px; left: -300px; top: -300px; background: #7651ff; }
  .aurora-green { width: 560px; height: 560px; right: -280px; top: 520px; background: #18bd7a; }

  .public-header { position: relative; z-index: 5; display: flex; align-items: center; justify-content: space-between; box-sizing: border-box; width: min(1180px, calc(100% - 48px)); margin: auto; padding: calc(24px + env(safe-area-inset-top)) 0 24px; border-bottom: 1px solid #ffffff12; }
  .brand { display: flex; align-items: center; gap: 11px; color: #fff; text-decoration: none; }
  .brand-copy { display: grid; line-height: 1.05; }
  .brand-copy strong { font-size: 21px; letter-spacing: -.04em; }
  .brand-copy small { margin-top: 4px; color: #777f9b; font-size: 9px; font-weight: 800; letter-spacing: .16em; text-transform: uppercase; }
  nav { display: flex; align-items: center; gap: 7px; }
  nav a { box-sizing: border-box; color: #c8cbe0; text-decoration: none; font-size: 13px; font-weight: 750; white-space: nowrap; }
  .nav-link { padding: 10px 12px; border-radius: 9px; }
  .nav-link:hover { background: #ffffff0c; color: #fff; }
  .login-link { padding: 11px 19px; border: 1px solid #8e7bff77; border-radius: 10px; background: linear-gradient(135deg, #7358eb, #6553d9); color: #fff; box-shadow: 0 10px 28px #392a9e48; }
  .login-link:hover { filter: brightness(1.1); }

  .hero { position: relative; z-index: 1; display: grid; grid-template-columns: minmax(0, 1.08fr) minmax(340px, .72fr); align-items: center; gap: clamp(50px, 7vw, 96px); width: min(1180px, calc(100% - 48px)); min-height: min(760px, calc(100vh - 92px)); margin: auto; padding: 78px 0 96px; scroll-margin-top: 20px; }
  .eyebrow { display: flex; align-items: center; gap: 9px; margin: 0 0 20px; color: #7ce3b4; font-size: 11px; font-weight: 850; letter-spacing: .16em; }
  .eyebrow span { width: 24px; height: 1px; background: #62d9a4; }
  .hero h1 { margin: 0; max-width: 760px; font-size: clamp(48px, 5.8vw, 78px); line-height: 1.01; letter-spacing: -.057em; }
  .hero h1 em { font-style: normal; background: linear-gradient(100deg, #a28eff, #5ea7ff 48%, #48dda0); -webkit-background-clip: text; background-clip: text; color: transparent; }
  .lead { max-width: 650px; margin: 26px 0 31px; color: #a7adc4; font-size: 18px; line-height: 1.7; }
  .hero-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 11px; }
  .primary-action, .secondary-action { display: inline-flex; align-items: center; justify-content: center; min-height: 48px; padding: 0 20px; border-radius: 11px; color: #fff; text-decoration: none; font-size: 14px; font-weight: 800; }
  .primary-action { gap: 18px; background: linear-gradient(135deg, #7358eb, #6553d9); box-shadow: 0 12px 34px #392a9e55; }
  .primary-action:hover { filter: brightness(1.1); transform: translateY(-1px); }
  .secondary-action { border: 1px solid #ffffff1d; background: #ffffff08; color: #c9cce0; }
  .secondary-action:hover { border-color: #ffffff33; background: #ffffff0e; }
  .trust-row { display: flex; flex-wrap: wrap; gap: 22px; margin-top: 27px; color: #838aa2; font-size: 11px; }
  .trust-row span::before { content: '✓'; margin-right: 7px; color: #54d99e; }

  .login-panel { position: relative; scroll-margin-top: 28px; padding: clamp(25px, 3vw, 36px); border: 1px solid #ffffff1c; border-radius: 24px; background: linear-gradient(145deg, #181a30dc, #101722e8); box-shadow: 0 30px 90px #0009, inset 0 1px 0 #ffffff0c; backdrop-filter: blur(24px) saturate(135%); -webkit-backdrop-filter: blur(24px) saturate(135%); }
  .login-panel::before { content: ''; position: absolute; inset: -1px; z-index: -1; border-radius: 24px; background: linear-gradient(145deg, #9e8cff44, transparent 45%, #52da9c22); }
  .panel-topline { display: flex; align-items: center; justify-content: space-between; margin-bottom: 27px; }
  .status { display: flex; align-items: center; gap: 7px; color: #8e96ae; font-size: 10px; font-weight: 750; letter-spacing: .05em; text-transform: uppercase; }
  .status i { width: 7px; height: 7px; border-radius: 50%; background: #52da9c; box-shadow: 0 0 12px #52da9c; }
  .login-panel h2 { margin: 0; color: #fbfaff; font-size: 27px; letter-spacing: -.035em; }
  .panel-lead { margin: 8px 0 25px; color: #9299b2; font-size: 13px; line-height: 1.55; }
  form { display: grid; gap: 10px; }
  label { color: #bec2d4; font-size: 11px; font-weight: 800; letter-spacing: .04em; }
  .key-field { display: flex; align-items: center; gap: 8px; height: 51px; padding: 0 14px; border: 1px solid #ffffff1b; border-radius: 11px; background: #090c17a8; transition: border-color .15s, box-shadow .15s; }
  .key-field:focus-within { border-color: #8e7bff99; box-shadow: 0 0 0 3px #765cff1f; }
  .key-field span { color: #777f9d; font-size: 17px; }
  .key-field input { min-width: 0; height: 100%; padding: 0 !important; border: 0 !important; border-radius: 0 !important; background: transparent !important; color: #f4f3ff !important; font-size: 16px !important; box-shadow: none !important; }
  .key-field input::placeholder { color: #596078; }
  .login-submit { display: flex; align-items: center; justify-content: space-between; width: 100%; min-height: 51px; margin-top: 5px; padding: 0 17px; border: 0; border-radius: 11px; background: linear-gradient(135deg, #7659ef, #5b53d6); color: #fff; font-size: 14px; font-weight: 800; box-shadow: 0 12px 28px #3f32a554; }
  .login-submit:hover:not(:disabled) { filter: brightness(1.09); }
  .login-submit:disabled { opacity: .5; cursor: not-allowed; }
  .login-error { margin: 2px 0; padding: 10px 12px; border: 1px solid #ff8c9840; border-radius: 9px; background: #e94f6114; color: #ffb5be; font-size: 12px; line-height: 1.45; }
  .session-note { display: flex; align-items: flex-start; gap: 8px; margin: 18px 0 0; color: #727a94; font-size: 10px; line-height: 1.5; }
  .session-note span { color: #52c893; }
  details { margin-top: 16px; padding-top: 15px; border-top: 1px solid #ffffff10; color: #7f879f; font-size: 11px; }
  summary { color: #a6acc1; cursor: pointer; font-weight: 700; }
  details p { margin: 9px 0 0; line-height: 1.6; }
  code { padding: 2px 4px; border-radius: 4px; background: #080b15; color: #b8b4d5; font-size: .92em; }

  .why { position: relative; z-index: 1; width: min(1180px, calc(100% - 48px)); margin: auto; padding: 100px 0 116px; border-top: 1px solid #ffffff10; scroll-margin-top: 24px; }
  .section-heading { display: grid; grid-template-columns: minmax(280px, .8fr) minmax(300px, 1.2fr); column-gap: 70px; align-items: end; }
  .section-heading .eyebrow { grid-column: 1 / -1; }
  .section-heading h2 { margin: 0; max-width: 600px; font-size: clamp(34px, 4vw, 52px); line-height: 1.08; letter-spacing: -.045em; }
  .section-heading > p:last-child { margin: 0; color: #9299b2; font-size: 15px; line-height: 1.7; }
  .feature-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-top: 54px; }
  article { min-height: 220px; padding: 24px; border: 1px solid #ffffff12; border-radius: 17px; background: linear-gradient(145deg, #121522c7, #0d1518a8); }
  .feature-icon { display: grid; place-items: center; width: 42px; height: 42px; margin-bottom: 35px; border: 1px solid #8e7bff44; border-radius: 11px; background: #8e7bff12; color: #a898ff; font-size: 20px; }
  article h3 { margin: 0 0 10px; font-size: 16px; }
  article p { margin: 0; color: #858da6; font-size: 12px; line-height: 1.65; }

  .closing { position: relative; z-index: 1; display: flex; align-items: center; justify-content: space-between; gap: 30px; width: min(1180px, calc(100% - 48px)); margin: 0 auto 72px; padding: 42px; border: 1px solid #ffffff16; border-radius: 21px; background: linear-gradient(115deg, #7659ef24, #101522aa 52%, #25bd7d17); }
  .closing .eyebrow { margin-bottom: 10px; }
  .closing h2 { margin: 0; font-size: clamp(27px, 3vw, 40px); letter-spacing: -.04em; }
  footer { position: relative; z-index: 1; display: flex; justify-content: space-between; width: min(1180px, calc(100% - 48px)); margin: auto; padding: 24px 0 calc(24px + env(safe-area-inset-bottom)); border-top: 1px solid #ffffff0e; color: #676f88; font-size: 11px; }
  footer a { color: #949bb4; text-decoration: none; }
  footer a:hover { color: #fff; }

  @media (max-width: 920px) {
    .hero { grid-template-columns: 1fr; gap: 56px; padding: 72px 0 90px; }
    .hero-copy { max-width: 760px; }
    .login-panel { width: min(520px, 100%); box-sizing: border-box; }
    .feature-grid { grid-template-columns: repeat(2, 1fr); }
  }
  @media (max-width: 680px) {
    .public-header { width: min(100% - 32px); padding-top: calc(17px + env(safe-area-inset-top)); padding-bottom: 17px; }
    .nav-link { display: none; }
    .login-link { min-height: 44px; display: grid; place-items: center; padding: 0 17px; }
    .hero { width: min(100% - 32px); min-height: auto; gap: 48px; padding: 58px 0 82px; }
    .eyebrow { font-size: 9px; line-height: 1.5; }
    .hero h1 { font-size: clamp(42px, 13vw, 58px); }
    .lead { margin-top: 22px; font-size: 15px; }
    .hero-actions { display: grid; grid-template-columns: 1fr 1fr; }
    .primary-action, .secondary-action { padding: 0 14px; font-size: 12px; }
    .trust-row { gap: 9px 17px; margin-top: 23px; }
    .login-panel { padding: 25px 20px; border-radius: 20px; }
    .login-panel::before { border-radius: 20px; }
    .why { width: min(100% - 32px); padding: 78px 0 88px; }
    .section-heading { grid-template-columns: 1fr; row-gap: 18px; }
    .section-heading h2 { font-size: 36px; }
    .feature-grid { grid-template-columns: 1fr; margin-top: 38px; }
    article { min-height: 0; }
    .feature-icon { margin-bottom: 24px; }
    .closing { align-items: stretch; flex-direction: column; width: min(100% - 32px); margin-bottom: 56px; padding: 28px 22px; }
    .closing .primary-action { align-self: flex-start; }
    footer { display: grid; gap: 6px; width: min(100% - 32px); }
  }
  @media (max-width: 390px) {
    .brand-copy small { display: none; }
    .hero-actions { grid-template-columns: 1fr; }
    .primary-action, .secondary-action { width: 100%; box-sizing: border-box; }
  }
  @media (prefers-reduced-motion: reduce) {
    .personal-landing { scroll-behavior: auto; }
    .primary-action { transition: none; }
  }
</style>
