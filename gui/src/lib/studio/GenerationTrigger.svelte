<script>
  import { TRIGGER_OPTIONS } from './triggersettings.js'

  export let selection = { type: 'auto', cron: '', channel: '' }
  export let channels = []
  export let onChange = () => {}

  $: channelList = Array.isArray(channels) ? channels : (channels && channels.channels) || []
  $: type = (selection && selection.type) || 'auto'

  function change(patch) {
    onChange({ type: 'auto', cron: '', channel: '', ...selection, ...patch })
  }
</script>

<div class="generation-trigger">
  <label for="generation-trigger-type">Trigger</label>
  <select
    id="generation-trigger-type"
    value={type}
    on:change={(e) => change({ type: e.target.value })}
  >
    <option value="auto">Use the prompt</option>
    {#each TRIGGER_OPTIONS as option}
      <option value={option.value} disabled={option.value === 'channel' && channelList.length === 0}>
        {option.label}{option.value === 'channel' && channelList.length === 0 ? ' (configure a channel first)' : ''}
      </option>
    {/each}
  </select>

  {#if type === 'schedule'}
    <label for="generation-trigger-cron">Cron expression</label>
    <input
      id="generation-trigger-cron"
      type="text"
      value={selection.cron || ''}
      placeholder="0 8 * * 1-5"
      required
      on:input={(e) => change({ cron: e.target.value })}
    />
  {:else if type === 'channel'}
    <label for="generation-trigger-channel">Inbound channel</label>
    <select
      id="generation-trigger-channel"
      value={selection.channel || ''}
      required
      on:change={(e) => change({ channel: e.target.value })}
    >
      <option value="">Choose channel…</option>
      {#each channelList as channel}
        <option value={channel.id}>{channel.name || channel.id}</option>
      {/each}
    </select>
  {/if}

  <p>
    {type === 'auto'
      ? 'Studio will infer the trigger from your prompt.'
      : 'Your explicit choice is authoritative and will be applied after generation.'}
  </p>
</div>

<style>
  .generation-trigger {
    display: grid;
    grid-template-columns: auto minmax(150px, 1fr);
    align-items: center;
    gap: 6px 9px;
    padding: 9px 10px;
    border: 1px solid var(--border, #2a3350);
    border-radius: 8px;
    background: var(--bg-elev-2, #171d2c);
  }
  label { color: var(--text-muted, #8b93ab); font-size: 11px; font-weight: 650; }
  select, input {
    min-width: 0; width: 100%; box-sizing: border-box; padding: 6px 8px;
    color: var(--text, #e6e9ef); background: var(--bg, #0f1420);
    border: 1px solid var(--border, #2a3350); border-radius: 6px; font-size: 12px;
  }
  select:focus, input:focus { outline: none; border-color: var(--accent, #6c63ff); }
  p {
    grid-column: 1 / -1; margin: 0; color: var(--text-muted, #8b93ab);
    font-size: 10.5px; line-height: 1.4;
  }
  @media (max-width: 680px) {
    .generation-trigger { grid-template-columns: 1fr; }
    p { grid-column: auto; }
  }
</style>
