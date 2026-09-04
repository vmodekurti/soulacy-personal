<script>
  import { Handle, Position } from '@xyflow/svelte'
  // The workflow trigger, framed distinctly as a START source.
  export let data
</script>

<div class="trigger-node">
  <span class="trigger-chip">trigger</span>
  <div class="trigger-type">{data.label}</div>
  <Handle type="source" position={Position.Right} />
</div>

<style>
  .trigger-node {
    min-width: 120px;
    background: linear-gradient(160deg, #213, #142);
    border: 1px dashed var(--ok, #36d399);
    border-radius: 999px 14px 14px 999px;
    padding: 10px 16px;
    text-align: center;
    color: var(--text, #e6e9f2);
  }
  .trigger-chip {
    font-size: 9px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--ok, #36d399);
  }
  .trigger-type {
    font-size: 13px;
    font-weight: 600;
    margin-top: 2px;
  }
</style>
