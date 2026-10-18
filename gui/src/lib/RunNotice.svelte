<script>
  import { responseNotice } from './runStatus.js'
  export let metadata = null
  $: notice = responseNotice(metadata)
</script>
{#if notice}
  <div class="run-notice" class:warning={notice.tone === 'warning'} role="status">
    <strong>{notice.title}</strong><p>{notice.detail}</p>
  </div>
{/if}
<style>
  .run-notice { margin: .5rem 0; padding: .7rem; border: 1px solid var(--sl-line); border-radius: 8px; font-size: .85rem; line-height: 1.5; }
  .warning { border-color: #c7984b; background: rgba(199,152,75,.1); }
  p { margin: .25rem 0 0; }
</style>
