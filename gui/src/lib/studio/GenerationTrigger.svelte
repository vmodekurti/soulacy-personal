<script>
  import { TRIGGER_OPTIONS, contextualDeliveryLabel, outboundDeliveryLabel, routeSummary } from './triggersettings.js'

  export let selection = { type: 'auto', cron: '', channel: '', delivery: 'auto', destination: '' }
  export let channels = []
  export let onChange = () => {}

  $: channelList = Array.isArray(channels) ? channels : (channels && channels.channels) || []
  $: type = (selection && selection.type) || 'auto'
  $: delivery = (selection && selection.delivery) || 'auto'

  function change(patch) {
    onChange({ type: 'auto', cron: '', channel: '', delivery: 'auto', destination: '', ...selection, ...patch })
  }

  function changeType(nextType) {
    const patch = { type: nextType }
    // Same-channel reply is the coherent default for a conversational trigger.
    // Scheduled runs have no invocation route to reply through. Manual and
    // webhook calls do, so keep an explicit contextual-reply choice for them.
    if (!['auto', 'schedule'].includes(nextType) && (selection.delivery || 'auto') === 'auto') patch.delivery = 'reply'
    if (nextType === 'schedule' && selection.delivery === 'reply') patch.delivery = 'auto'
    if (nextType !== 'schedule' && selection.delivery === 'none') patch.delivery = 'reply'
    change(patch)
  }
</script>

<div class="generation-trigger">
  <label for="generation-trigger-type">Trigger</label>
  <select
    id="generation-trigger-type"
    value={type}
    on:change={(e) => changeType(e.target.value)}
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

  <label for="generation-delivery">Output</label>
  <select
    id="generation-delivery"
    value={selection.delivery || 'auto'}
    on:change={(e) => change({ delivery: e.target.value, destination: '' })}
  >
    <option value="auto">Use the prompt</option>
    {#if !['auto', 'schedule'].includes(type)}
      <option value="reply">{contextualDeliveryLabel(type)}</option>
    {/if}
    {#if ['auto', 'schedule'].includes(type)}
      <option value="none">{type === 'schedule' ? 'Runs / Activity only (no message sent)' : 'Normal response only (no external push)'}</option>
    {/if}
    {#each channelList as channel}
      <option value={channel.id}>{outboundDeliveryLabel(type, `fixed ${channel.name || channel.id} destination`)}</option>
    {/each}
  </select>

  {#if !['auto', 'reply', 'none'].includes(selection.delivery || 'auto')}
    <label for="generation-destination">Destination</label>
    <input
      id="generation-destination"
      type="text"
      value={selection.destination || ''}
      placeholder="Chat, channel, address, or destination ID"
      required
      on:input={(e) => change({ destination: e.target.value })}
    />
  {/if}

  <p class="route-summary"><strong>Route:</strong> {routeSummary(type, delivery, delivery)}</p>
  <p>{type === 'auto' && delivery === 'auto' ? 'Studio will infer both sides from your prompt.' : 'Only output choices compatible with this input are shown.'}</p>
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
  .route-summary { color: var(--text, #e6e9ef); }
  @media (max-width: 680px) {
    .generation-trigger { grid-template-columns: 1fr; }
    p { grid-column: auto; }
  }
</style>
