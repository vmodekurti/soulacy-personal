<!--
  AskGenie.svelte — the always-available quick question.

  The iPhone app keeps an "Ask Genie" pill above every tab; this is the same
  affordance for the browser. It is deliberately not a chat: one question in,
  handed to the Chat page, which picks the best agent (see lib/genie.js) and
  shows the answer as a normal conversation.
-->
<script>
  import GenieMark from './GenieMark.svelte'
  import { createEventDispatcher, tick } from 'svelte'
  import { genieAsk } from './stores.js'
  import { genieRequest, GENIE_SUGGESTIONS } from './genie.js'

  const dispatch = createEventDispatcher()
  let open = false
  let text = ''
  let inputEl

  async function toggle() {
    open = !open
    if (open) { await tick(); inputEl?.focus() }
  }

  function close() { open = false }

  function ask(question = text) {
    const req = genieRequest(question)
    if (!req) return
    genieAsk.set(req)
    text = ''
    open = false
    // The front door, not Chat: one question, one place that answers it.
    dispatch('navigate', 'start')
  }

  function onKey(e) {
    if (e.key === 'Escape') { e.preventDefault(); close(); return }
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ask() }
  }
</script>

<div class="ask-genie" class:open>
  {#if open}
    <button class="ask-genie-backdrop" aria-label="Close Ask Genie" on:click={close}></button>
    <section class="ask-genie-card" role="dialog" aria-label="Ask Genie">
      <header>
        <span class="mark" aria-hidden="true"><GenieMark size={18} gradient /></span>
        <div>
          <strong>Genie</strong>
          <span>Ask once. I’ll find the right agent.</span>
        </div>
        <button class="close" on:click={close} aria-label="Close">✕</button>
      </header>
      <div class="chips">
        {#each GENIE_SUGGESTIONS as s}
          <button class="chip" on:click={() => ask(s)}>{s}</button>
        {/each}
      </div>
      <div class="row">
        <textarea bind:this={inputEl} bind:value={text} rows="1" placeholder="Ask a quick question"
                  aria-label="Your question" on:keydown={onKey}></textarea>
        <button class="send" on:click={() => ask()} disabled={!text.trim()} aria-label="Ask Genie">➤</button>
      </div>
    </section>
  {/if}
  <button class="ask-genie-pill" on:click={toggle} aria-expanded={open} aria-haspopup="dialog" title="Ask Genie">
    <GenieMark size={18} /><span class="label">Ask Genie</span>
  </button>
</div>

<style>
  .ask-genie { position: fixed; right: 20px; bottom: 20px; z-index: 55; display: flex; flex-direction: column; align-items: flex-end; gap: .6rem; }

  .ask-genie-pill {
    display: inline-flex; align-items: center; gap: .45rem;
    padding: .7rem 1.15rem; border: none; border-radius: 999px; cursor: pointer;
    color: #fff; font-weight: 700; font-size: .95rem; letter-spacing: -.01em;
    background: linear-gradient(135deg, #7c6cff 0%, #4fd1ff 100%);
    box-shadow: 0 10px 30px color-mix(in srgb, var(--sl-accent) 35%, transparent), 0 2px 8px rgba(0,0,0,.35);
    transition: transform .15s ease, box-shadow .15s ease;
  }
  .ask-genie-pill:hover { transform: translateY(-1px); box-shadow: 0 14px 34px color-mix(in srgb, var(--sl-accent) 45%, transparent), 0 2px 8px rgba(0,0,0,.35); }
  .ask-genie-pill:active { transform: translateY(0); }
  .ask-genie-pill > span:first-child { font-size: 1.05rem; line-height: 1; }

  .ask-genie-backdrop { position: fixed; inset: 0; z-index: -1; border: none; background: rgba(2,5,14,.35); cursor: default; }

  .ask-genie-card {
    width: min(420px, calc(100vw - 32px)); padding: 1rem; border-radius: 18px;
    background: #121a30; border: 1px solid #30385b; color: #e9ebfa;
    box-shadow: 0 24px 60px rgba(0,0,0,.5); display: flex; flex-direction: column; gap: .8rem;
    animation: ask-genie-in .16s ease-out;
  }
  @keyframes ask-genie-in { from { opacity: 0; transform: translateY(8px) scale(.98); } to { opacity: 1; transform: none; } }
  @media (prefers-reduced-motion: reduce) { .ask-genie-card { animation: none; } }

  header { display: flex; align-items: center; gap: .7rem; }
  header .mark {
    width: 40px; height: 40px; display: grid; place-items: center; border-radius: 50%;
    background: linear-gradient(135deg, #7c6cff, #4fd1ff); color: #fff; font-size: 1.1rem;
    box-shadow: 0 8px 20px color-mix(in srgb, var(--sl-accent) 35%, transparent);
  }
  header div { display: flex; flex-direction: column; gap: .1rem; min-width: 0; }
  header strong { font-size: 1.05rem; }
  header div > span { color: #9aa1c4; font-size: .82rem; }
  header .close { margin-left: auto; background: none; border: none; color: #8c93b8; cursor: pointer; font-size: .95rem; }
  header .close:hover { color: #fff; }

  .chips { display: flex; gap: .4rem; overflow-x: auto; scrollbar-width: none; padding-bottom: 2px; }
  .chips::-webkit-scrollbar { display: none; }
  .chip {
    flex: 0 0 auto; padding: .35rem .7rem; border-radius: 999px; cursor: pointer;
    border: 1px solid rgba(124,108,255,.45); background: rgba(124,108,255,.12); color: #cfcbff;
    font-size: .78rem; font-weight: 650; white-space: nowrap;
  }
  .chip:hover { background: rgba(124,108,255,.24); }

  .row { display: flex; align-items: flex-end; gap: .5rem; }
  textarea {
    flex: 1; resize: none; min-height: 44px; max-height: 120px; padding: .65rem .85rem;
    border-radius: 14px; border: 1px solid #30385b; background: #0e1424; color: #eef;
    font: inherit; line-height: 1.35;
  }
  textarea:focus { outline: none; border-color: #7c6cff; box-shadow: 0 0 0 3px rgba(124,108,255,.2); }
  .send {
    width: 44px; height: 44px; border-radius: 50%; border: none; cursor: pointer; color: #fff;
    background: linear-gradient(135deg, #7c6cff, #4fd1ff); font-size: 1rem;
  }
  .send:disabled { opacity: .45; cursor: not-allowed; }

  @media (max-width: 768px) {
    /* Clear the bottom tab bar (and Chat's composer on top of it). */
    .ask-genie { right: 14px; bottom: calc(74px + env(safe-area-inset-bottom)); }
    .ask-genie-pill { padding: .65rem 1rem; }
  }
</style>
