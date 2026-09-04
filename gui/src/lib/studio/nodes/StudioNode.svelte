<script>
  import { Handle, Position } from '@xyflow/svelte'
  // A workflow step. Visual keyed by node.kind:
  //   tool   -> teal card with default handles
  //   agent  -> indigo "peer" card (double border, handoff affordance)
  //   branch -> amber decision node (diamond accent)
  // When the node declares inputs[]/outputs[] we render ONE handle per declared
  // port (handle id = port name) stacked on the left/right; otherwise we fall
  // back to a single default handle per side (legacy behaviour).
  export let data
  $: node = data.node
  $: inputs = (data && data.inputs) || []
  $: outputs = (data && data.outputs) || []
  $: shape = (data && data.shape) || 'card'
  // Post-build execution status for the semantic accent (set by runstate.js).
  $: runState = (data && data.runState) || 'idle'

  // Evenly distribute N handles across the vertical edge of the node.
  function offsetPct(i, n) {
    return ((i + 1) / (n + 1)) * 100
  }
</script>

<div
  class="studio-node shape-{shape}"
  class:entry={data.isEntry}
  class:invalid={data.invalid}
  class:warn={data.warn}
  class:state-ok={runState === 'ok'}
  class:state-repaired={runState === 'repaired'}
  class:state-problem={runState === 'problem'}
  style="--node-accent: {data.color}"
>
  <!-- ── Input handles (left) ── -->
  {#if inputs.length}
    {#each inputs as p, i}
      <Handle
        type="target"
        position={Position.Left}
        id={p.name}
        style={`top:${offsetPct(i, inputs.length)}%`}
      />
      <span class="port-label in" style={`top:${offsetPct(i, inputs.length)}%`}>{p.label}{#if p.type}<em>:{p.type}</em>{/if}</span>
    {/each}
  {:else}
    <Handle type="target" position={Position.Left} />
  {/if}

  <div class="node-head">
    <span class="kind-chip">{data.kindLabel}</span>
    {#if shape === 'decision'}<span class="decision-glyph" aria-hidden="true">◆</span>{/if}
    {#if shape === 'peer'}<span class="peer-glyph" title="peer-agent handoff" aria-hidden="true">⇄</span>{/if}
    {#if data.isEntry}<span class="entry-chip">entry</span>{/if}
    {#if node.for_each}
      <span
        class="map-chip"
        title={node.max_parallel > 1
          ? `Runs up to ${node.max_parallel} items concurrently and preserves input order`
          : 'Runs once for every item and preserves input order'}
      >
        {node.max_parallel > 1 ? `parallel ×${node.max_parallel}` : 'for each'}
      </span>
    {/if}
    {#if runState !== 'idle'}
      <span class="state-dot {runState}" title={runState === 'ok' ? 'verified by the last build' : runState === 'repaired' ? 'repaired during the last build' : 'unresolved problem'} aria-hidden="true"></span>
    {:else if data.readiness && data.readiness !== 'ready'}
      <span class="ready-dot {data.readiness}"
            title={data.readiness === 'risky' ? 'Risky — external send or generated code' : 'Needs attention — missing a required field or not connected'}
            aria-hidden="true"></span>
    {/if}
  </div>
  <div class="node-title" title={data.label}>{data.label}</div>
  {#if data.description}<div class="node-desc" title={data.description}>{data.description}</div>{/if}
  <div class="node-meta">
    <span class="node-id">{node.id}</span>
    {#if node.output}<span class="node-out">→ {node.output}</span>{/if}
  </div>

  <!-- ── Output handles (right) ── -->
  {#if outputs.length}
    {#each outputs as p, i}
      <Handle
        type="source"
        position={Position.Right}
        id={p.name}
        style={`top:${offsetPct(i, outputs.length)}%`}
      />
      <span class="port-label out" style={`top:${offsetPct(i, outputs.length)}%`}>{p.label}{#if p.type}<em>:{p.type}</em>{/if}</span>
    {/each}
  {:else}
    <Handle type="source" position={Position.Right} />
  {/if}
</div>

<style>
  .studio-node {
    position: relative;
    min-width: 170px;
    max-width: 220px;
    background: var(--bg-elev-2, #1b2235);
    border: 1px solid var(--node-accent);
    border-left: 4px solid var(--node-accent);
    border-radius: 10px;
    padding: 10px 12px;
    color: var(--text, #e6e9f2);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.35);
  }

  /* Agent = peer handoff: doubled accent border + faint top-stripe so it reads
     as a hand-off to another agent rather than a tool call. */
  .studio-node.shape-peer {
    border-style: double;
    border-width: 1px 1px 1px 4px;
    background:
      linear-gradient(180deg, color-mix(in srgb, var(--node-accent) 16%, transparent), transparent 38%),
      var(--bg-elev-2, #1b2235);
  }

  /* Branch = decision: clipped corners give a diamond-ish silhouette + the ◆
     glyph in the head. */
  .studio-node.shape-decision {
    border-radius: 12px;
    clip-path: polygon(8% 0, 92% 0, 100% 50%, 92% 100%, 8% 100%, 0 50%);
    padding-left: 18px;
    padding-right: 18px;
    background:
      linear-gradient(160deg, color-mix(in srgb, var(--node-accent) 14%, transparent), transparent 50%),
      var(--bg-elev-2, #1b2235);
  }

  .studio-node.entry {
    box-shadow: 0 0 0 2px var(--node-accent), 0 4px 16px rgba(0, 0, 0, 0.4);
  }
  /* Validation rings (also driven by the node wrapper class for safety). */
  .studio-node.invalid {
    box-shadow: 0 0 0 2px var(--error, #ff6b81), 0 4px 16px rgba(0, 0, 0, 0.4);
  }
  .studio-node.warn {
    box-shadow: 0 0 0 2px var(--warn, #f5a742), 0 4px 16px rgba(0, 0, 0, 0.4);
  }

  /* Post-build execution accents — restrained, semantic. Validation rings
     (invalid/warn) take precedence by being later in the cascade if both apply. */
  .studio-node.state-ok {
    box-shadow: 0 0 0 1.5px var(--ok, #38c172), 0 4px 16px rgba(0, 0, 0, 0.4);
  }
  .studio-node.state-repaired {
    box-shadow: 0 0 0 1.5px var(--warn, #f5a742), 0 4px 16px rgba(0, 0, 0, 0.4);
  }
  .studio-node.state-problem {
    box-shadow: 0 0 0 1.5px var(--error, #ff6b81), 0 4px 16px rgba(0, 0, 0, 0.4);
  }
  .state-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    margin-left: auto;
    flex: none;
  }
  .state-dot.ok { background: var(--ok, #38c172); box-shadow: 0 0 6px var(--ok, #38c172); }
  .state-dot.repaired { background: var(--warn, #f5a742); box-shadow: 0 0 6px var(--warn, #f5a742); }
  .state-dot.problem { background: var(--error, #ff6b81); box-shadow: 0 0 6px var(--error, #ff6b81); }
  /* Author-time readiness dot (Story 8): amber = needs attention, red = risky. */
  .ready-dot {
    width: 7px; height: 7px; border-radius: 50%; margin-left: auto; flex: none;
  }
  .ready-dot.needs-attention { background: var(--warn, #f5a524); box-shadow: 0 0 6px var(--warn, #f5a524); }
  .ready-dot.risky { background: var(--error, #ff6b81); box-shadow: 0 0 6px var(--error, #ff6b81); }

  .node-head {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 6px;
  }
  .kind-chip {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    background: var(--node-accent);
    color: #0c0f1a;
    padding: 1px 7px;
    border-radius: 999px;
    font-weight: 700;
  }
  .decision-glyph { color: var(--node-accent); font-size: 12px; }
  .peer-glyph { color: var(--node-accent); font-size: 13px; }
  .entry-chip {
    font-size: 10px;
    color: var(--node-accent);
    border: 1px solid var(--node-accent);
    border-radius: 999px;
    padding: 0 6px;
  }
  .map-chip {
    font-size: 9px;
    color: #b9f7ee;
    border: 1px solid color-mix(in srgb, #36d9c4 72%, transparent);
    background: color-mix(in srgb, #36d9c4 14%, transparent);
    border-radius: 3px;
    padding: 1px 5px;
    white-space: nowrap;
  }
  .node-title {
    font-size: 13px;
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .node-desc {
    font-size: 10.5px;
    line-height: 1.35;
    color: var(--text-muted);
    margin-top: 3px;
    max-width: 200px;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .node-meta {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    margin-top: 4px;
    font-size: 10px;
    color: var(--text-muted, #8b93ab);
  }
  .node-out {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* Per-port labels sit next to their handle. */
  .port-label {
    position: absolute;
    transform: translateY(-50%);
    font-size: 9px;
    line-height: 1;
    color: var(--text-muted, #8b93ab);
    white-space: nowrap;
    pointer-events: none;
  }
  .port-label em { font-style: normal; opacity: 0.7; }
  .port-label.in { left: 10px; }
  .port-label.out { right: 10px; text-align: right; }
</style>
