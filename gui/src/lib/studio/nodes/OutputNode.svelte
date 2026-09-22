<script>
  import { Handle, Position } from '@xyflow/svelte'
  // The output channels, framed distinctly as a terminal SINK.
  export let data
  $: channels = data.channels || []
</script>

<div class="output-node">
  <Handle type="target" position={Position.Left} />
  <span class="output-chip">output</span>
  <div class="channels">
    {#each channels as ch}
      <span class="channel">{typeof ch === 'string' ? ch : (ch.type || ch.name || 'channel')}</span>
    {/each}
    {#if !channels.length}<span class="channel muted">no channels</span>{/if}
  </div>
</div>

<style>
  .output-node {
    min-width: 120px;
    background: linear-gradient(160deg, #2a2350, #1b2235);
    border: 1px dashed var(--accent, var(--sl-accent));
    border-radius: 14px 999px 999px 14px;
    padding: 10px 16px;
    text-align: center;
    color: var(--text, #e6e9f2);
  }
  .output-chip {
    font-size: 9px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--accent, var(--sl-accent));
  }
  .channels {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    justify-content: center;
    margin-top: 4px;
  }
  .channel {
    font-size: 11px;
    background: var(--accent-dim, color-mix(in srgb, var(--sl-accent) 18%, transparent));
    border: 1px solid var(--accent, var(--sl-accent));
    border-radius: 999px;
    padding: 1px 8px;
  }
  .channel.muted {
    color: var(--text-muted, #8b93ab);
    border-color: var(--border, #262e44);
    background: transparent;
  }
</style>
