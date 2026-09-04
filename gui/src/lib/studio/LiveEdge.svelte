<script>
  /*
   * LiveEdge — a flow edge with physical weight and a heartbeat.
   *
   * Redesign "feel": edges aren't static lines. When a workflow is running, data
   * is visible as elegant glowing particles flowing along the bezier curve, so the
   * user gets a visceral, immediate read of the system's heartbeat. At rest the
   * edge is a soft, weighted curve; conditional edges read differently from plain
   * ones; the flow's output edge glows in the accent.
   *
   * Dependency-light: the bezier path comes from xyflow's geometry helper; the
   * particles are pure SVG <animateMotion> riding an <mpath> of that path, so the
   * animation is GPU-smooth and needs no per-frame JS.
   */
  import { getBezierPath, BaseEdge } from '@xyflow/svelte'

  export let id
  export let sourceX
  export let sourceY
  export let targetX
  export let targetY
  export let sourcePosition
  export let targetPosition
  export let markerEnd = undefined
  export let data = {}

  $: [path] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  $: active = !!(data && data.active) // a run is in flight → particles flow
  $: cond = !!(data && data.cond) // conditional (branch predicate) edge
  $: pathId = 'le-path-' + id
</script>

<!-- The visible edge curve (xyflow handles the marker + base class). -->
<BaseEdge {id} {path} {markerEnd} class={'live-edge' + (active ? ' is-active' : '') + (cond ? ' is-cond' : '')} />

<!-- A hidden twin used purely as the motion path for the particles. -->
<path id={pathId} d={path} fill="none" stroke="none" />

{#if active}
  {#each [0, 1, 2] as i}
    <circle class="live-particle" r={i === 1 ? 2.5 : 2}>
      <animateMotion dur="1.5s" begin={`${i * 0.5}s`} repeatCount="indefinite" rotate="auto">
        <mpath href={'#' + pathId} />
      </animateMotion>
    </circle>
  {/each}
{/if}

<style>
  /* Weighted, clearly-visible resting curve; the accent on hover/active lifts it.
     Uses the muted text tone (not the faint border) so edges read with physical
     weight on the dark canvas. */
  :global(.svelte-flow .live-edge) {
    stroke: var(--text-muted, #8b93ab);
    stroke-width: 2;
    opacity: 0.85;
    transition: stroke 160ms ease, stroke-width 160ms ease, opacity 160ms ease;
  }
  :global(.svelte-flow .live-edge:hover) {
    stroke: var(--accent, #4f7cff);
    opacity: 1;
  }
  :global(.svelte-flow .live-edge.is-cond) {
    stroke-dasharray: 5 4;
  }
  :global(.svelte-flow .live-edge.is-active) {
    stroke: var(--accent, #4f7cff);
    stroke-width: 2.2;
    filter: drop-shadow(0 0 4px color-mix(in srgb, var(--accent, #4f7cff) 60%, transparent));
  }
  .live-particle {
    fill: var(--accent, #4f7cff);
    filter: drop-shadow(0 0 5px color-mix(in srgb, var(--accent, #4f7cff) 80%, transparent));
  }
</style>
