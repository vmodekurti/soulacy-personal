<script>
  export let info = null
  export let onUpgrade = () => {}
  export let onCheck = null

  let showingInstructions = false
  let copied = false
  let checking = false
  let checkMessage = ''

  $: canUpgradeInline = info?.upgrade_strategy === 'inline' ||
    (!info?.upgrade_strategy && info?.mode !== 'notify')
  $: instructions = Array.isArray(info?.upgrade_instructions)
    ? info.upgrade_instructions
    : ['Open the deployment platform, update Soulacy to the latest release, and redeploy the service.']

  function close() {
    showingInstructions = false
    copied = false
    checkMessage = ''
  }

  function handleKeydown(event) {
    if (event.key === 'Escape' && showingInstructions) close()
  }

  async function copyInstructions() {
    const text = [
      `Upgrade Soulacy on ${info?.deployment_platform || 'this deployment'}`,
      info?.target_image ? `Target image: ${info.target_image}` : '',
      ...instructions.map((step, index) => `${index + 1}. ${step}`),
      info?.upgrade_help_url || '',
    ].filter(Boolean).join('\n')
    try {
      await navigator.clipboard.writeText(text)
      copied = true
    } catch {
      copied = false
    }
  }

  async function checkAgain() {
    if (checking || typeof onCheck !== 'function') return
    checking = true
    checkMessage = ''
    try {
      const result = await onCheck()
      if (result?.update_available) {
        checkMessage = `Still running ${result.current_version || 'the previous release'}. Finish the redeploy, then check again.`
      } else if (result) {
        checkMessage = result.current_version
          ? `Soulacy ${result.current_version} is up to date.`
          : 'Soulacy is up to date.'
      } else {
        checkMessage = 'The gateway did not return version status yet. Wait for the redeploy to finish, then check again.'
      }
    } catch {
      checkMessage = 'The gateway is still restarting or unavailable. Wait a moment, then check again.'
    } finally {
      checking = false
    }
  }
</script>

<svelte:window on:keydown={handleKeydown} />

{#if canUpgradeInline}
  <button class="btn-primary btn-sm" on:click={onUpgrade}>Upgrade Now</button>
{:else}
  <button class="btn-primary btn-sm" on:click={() => showingInstructions = true}>How to upgrade</button>
{/if}

{#if showingInstructions}
  <div class="upgrade-modal-bg">
    <div class="upgrade-modal" role="dialog" aria-modal="true" aria-labelledby="upgrade-modal-title">
      <div class="upgrade-modal-heading">
        <div>
          <div class="eyebrow">{info?.deployment_platform || 'Managed deployment'}</div>
          <h2 id="upgrade-modal-title">Redeploy Soulacy</h2>
        </div>
        <button class="close-button" aria-label="Close upgrade instructions" on:click={close}>×</button>
      </div>

      <p>{info?.upgrade_reason || info?.mode_reason || 'This deployment must be upgraded through its deployment platform.'}</p>

      {#if info?.target_image}
        <div class="target-image">
          <span>Target image</span>
          <code>{info.target_image}</code>
        </div>
      {/if}

      {#if info?.current_version && info?.latest_version}
        <div class="version-move" aria-label="Upgrade version">
          <span>{info.current_version}</span>
          <span aria-hidden="true">→</span>
          <strong>{info.latest_version}</strong>
        </div>
      {/if}

      <ol>
        {#each instructions as step}
          <li>{step}</li>
        {/each}
      </ol>

      <div class="upgrade-modal-actions">
        {#if typeof navigator !== 'undefined' && navigator.clipboard}
          <button class="btn-secondary" on:click={copyInstructions}>{copied ? 'Copied' : 'Copy instructions'}</button>
        {/if}
        {#if info?.upgrade_help_url}
          <a class="btn-secondary" href={info.upgrade_help_url} target="_blank" rel="noreferrer">Open upgrade guide</a>
        {/if}
        {#if typeof onCheck === 'function'}
          <button class="btn-primary" disabled={checking} on:click={checkAgain}>
            {checking ? 'Checking…' : 'I’ve redeployed — check again'}
          </button>
        {:else}
          <button class="btn-primary" on:click={close}>Done</button>
        {/if}
      </div>
      {#if checkMessage}<p class="check-message" role="status">{checkMessage}</p>{/if}
    </div>
  </div>
{/if}

<style>
  .upgrade-modal-bg {
    position: fixed;
    inset: 0;
    z-index: 140;
    display: grid;
    place-items: center;
    padding: 1rem;
    background: rgba(3, 5, 12, 0.76);
    backdrop-filter: blur(4px);
  }

  .upgrade-modal {
    width: min(560px, 94vw);
    max-height: 88vh;
    overflow-y: auto;
    padding: 1.5rem;
    border: 1px solid var(--sl-line, #2a2f4a);
    border-radius: 14px;
    background: var(--sl-surface, #14172a);
    box-shadow: 0 24px 80px rgba(0, 0, 0, 0.45);
  }

  .upgrade-modal-heading {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }

  .eyebrow {
    margin-bottom: 0.3rem;
    color: var(--sl-accent, #8d95ff);
    font-size: 0.72rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  h2 { margin: 0; font-size: 1.2rem; }
  p { color: var(--sl-text-dim, #a4a9c6); line-height: 1.55; }

  .close-button {
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--sl-text-dim, #a4a9c6);
    font-size: 1.6rem;
    line-height: 1;
  }

  .target-image {
    display: grid;
    gap: 0.4rem;
    margin: 1rem 0;
    padding: 0.8rem 0.9rem;
    border: 1px solid var(--sl-line, #2a2f4a);
    border-radius: 9px;
    background: rgba(7, 9, 20, 0.55);
  }

  .target-image span { color: var(--sl-text-faint, #7b82a8); font-size: 0.72rem; text-transform: uppercase; }
  .target-image code { overflow-wrap: anywhere; color: var(--sl-text, #e8eaf6); }

  .version-move {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    width: fit-content;
    margin: 0.9rem 0;
    padding: 0.45rem 0.7rem;
    border-radius: 999px;
    background: var(--sl-accent-soft, color-mix(in srgb, var(--sl-accent-hover) 12%, transparent));
    color: var(--sl-text-dim, #a4a9c6);
    font-size: 0.82rem;
  }

  .version-move strong { color: var(--sl-text, #e8eaf6); }

  ol {
    display: grid;
    gap: 0.75rem;
    margin: 1.1rem 0 1.35rem;
    padding-left: 1.3rem;
    color: var(--sl-text, #e8eaf6);
    line-height: 1.5;
  }

  li::marker { color: var(--sl-accent, #8d95ff); font-weight: 700; }

  .upgrade-modal-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 0.65rem;
  }

  .upgrade-modal-actions a { text-decoration: none; }
  .check-message { margin: 0.85rem 0 0; font-size: 0.82rem; }

  @media (max-width: 560px) {
    .upgrade-modal { padding: 1.15rem; }
    .upgrade-modal-actions > * { flex: 1 1 100%; text-align: center; }
  }
</style>
