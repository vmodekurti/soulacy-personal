<script>
  import {
    TRIGGER_OPTIONS,
    scheduleOutputPatch,
    toggleChannelPatch,
    triggerChannelPatch,
    triggerCronPatch,
    triggerTypePatch,
  } from './triggersettings.js'

  export let workflow = null
  export let channels = []
  export let onChange = () => {}

  const CRON_HINTS = [
    '0 8 * * 1-5  =  weekdays at 8am',
    '*/15 * * * *  =  every 15 minutes',
    '0 0 * * 0  =  Sundays at midnight',
  ]
  const scheduleTemplatePlaceholder = 'Optional. Use {reply} for the agent result.'

  $: trigger = (workflow && workflow.trigger) || { type: 'manual', config: {} }
  $: triggerType = trigger.type === 'cron'
    ? 'schedule'
    : trigger.type === 'internal' ? 'manual' : (trigger.type || 'manual')
  $: triggerCron = trigger.config?.cron || ''
  $: boundChannels = workflow && Array.isArray(workflow.channels) ? workflow.channels : []
  // Older/saved definitions persist channel bindings but not Studio's optional
  // primary-channel hint. Show the first real binding instead of reopening a
  // working channel agent with a misleading blank selector.
  $: triggerChannel = trigger.config?.channel || (triggerType === 'channel' ? (boundChannels[0] || '') : '')
  $: selectedChannels = new Set(boundChannels)
  $: output = (workflow && workflow.output) || {}
  $: outputChannel = output.channel || ''

  function apply(patch) {
    onChange(patch)
  }
</script>

<section class="trigger-settings" aria-label="Trigger and delivery settings">
  <h3>Trigger &amp; delivery</h3>
  <p class="hint">Changing how this agent starts also updates the settings that are valid for that trigger.</p>

  <label for="studio-trigger-type">Run when</label>
  <select
    id="studio-trigger-type"
    value={triggerType}
    on:change={(e) => apply(triggerTypePatch(workflow, e.target.value))}
  >
    {#each TRIGGER_OPTIONS as option}
      <option
        value={option.value}
        selected={option.value === triggerType}
        disabled={option.value === 'channel' && channels.length === 0}
      >{option.label}{option.value === 'channel' && channels.length === 0 ? ' (configure a channel first)' : ''}</option>
    {/each}
  </select>

  {#if triggerType === 'schedule'}
    <label for="studio-trigger-cron">Cron expression</label>
    <input
      id="studio-trigger-cron"
      type="text"
      placeholder="0 8 * * 1-5"
      value={triggerCron}
      on:input={(e) => apply(triggerCronPatch(workflow, e.target.value))}
    />
    <ul class="cron-hints">
      {#each CRON_HINTS as cronHint}<li>{cronHint}</li>{/each}
    </ul>
    <label class="check unattended">
      <input
        type="checkbox"
        checked={!!workflow?.unattended}
        on:change={(e) => apply({ unattended: e.target.checked })}
      />
      <span><strong>Allow unattended execution</strong><small>Scheduled system/network actions can run without an approval prompt.</small></span>
    </label>
  {:else if triggerType === 'channel'}
    <label for="studio-trigger-channel">Inbound channel</label>
    <select
      id="studio-trigger-channel"
      value={triggerChannel}
      on:change={(e) => apply(triggerChannelPatch(workflow, e.target.value))}
    >
      <option value="" selected={!triggerChannel}>Choose channel…</option>
      {#each channels as channel}
        <option value={channel.id} selected={channel.id === triggerChannel}>{channel.name || channel.id}</option>
      {/each}
    </select>
    <p class="hint">The chosen channel is automatically added to this agent’s channel bindings.</p>
  {:else if triggerType === 'webhook'}
    <p class="mode-note">Runs when its inbound webhook endpoint receives a request.</p>
  {:else}
    <p class="mode-note">Runs only when started by hand or programmatically by another agent.</p>
  {/if}

  <div class="divider"></div>
  <h4>{triggerType === 'channel' ? 'Channel bindings' : 'Output channels'}</h4>
  <p class="hint">
    {triggerType === 'schedule'
      ? 'Choose where scheduled results may be delivered.'
      : triggerType === 'channel'
        ? 'The inbound channel must stay selected; add others if results may also be delivered there.'
        : 'Optional destinations for results produced by this agent.'}
  </p>
  {#if channels.length === 0}
    <p class="empty">No configured channels are available.</p>
  {:else}
    <div class="channel-list">
      {#each channels as channel}
        <label class="check">
          <input
            type="checkbox"
            checked={selectedChannels.has(channel.id)}
            on:change={(e) => apply(toggleChannelPatch(workflow, channel.id, e.target.checked))}
          />
          <span>{channel.name || channel.id}</span>
        </label>
      {/each}
    </div>
  {/if}

  {#if triggerType === 'schedule' && selectedChannels.size > 0}
    <div class="delivery">
      <label for="studio-output-channel">Scheduled delivery channel</label>
      <select
        id="studio-output-channel"
        value={outputChannel}
        on:change={(e) => apply(scheduleOutputPatch(workflow, { channel: e.target.value }))}
      >
        <option value="" selected={!outputChannel}>Choose delivery channel…</option>
        {#each channels.filter((channel) => selectedChannels.has(channel.id)) as channel}
          <option value={channel.id} selected={channel.id === outputChannel}>{channel.name || channel.id}</option>
        {/each}
      </select>

      <label for="studio-output-to">Destination ID</label>
      <input
        id="studio-output-to"
        type="text"
        placeholder="Chat/channel ID or @channelusername"
        value={output.to || ''}
        on:input={(e) => apply(scheduleOutputPatch(workflow, { to: e.target.value }))}
      />

      <label for="studio-output-bot">Bot label</label>
      <input
        id="studio-output-bot"
        type="text"
        placeholder="Optional display name"
        value={output.bot_name || ''}
        on:input={(e) => apply(scheduleOutputPatch(workflow, { bot_name: e.target.value }))}
      />

      <label for="studio-output-template">Message template</label>
      <textarea
        id="studio-output-template"
        rows="3"
        placeholder={scheduleTemplatePlaceholder}
        value={output.template || ''}
        on:input={(e) => apply(scheduleOutputPatch(workflow, { template: e.target.value }))}
      ></textarea>
    </div>
  {:else if triggerType === 'schedule'}
    <p class="mode-note">Choose an output channel if the scheduled result should be posted automatically.</p>
  {/if}
</section>

<style>
  .trigger-settings {
    display: flex;
    flex-direction: column;
    gap: 7px;
    padding: 13px;
    border: 1px solid var(--border, #2a3350);
    border-radius: 10px;
    background: var(--bg-elev-2, #171d2c);
  }
  h3, h4 { margin: 0; color: var(--text, #e6e9ef); }
  h3 { font-size: 14px; }
  h4 { font-size: 12px; }
  label { color: var(--text-muted, #8b93ab); font-size: 11px; font-weight: 600; }
  select, input[type='text'], textarea {
    width: 100%; box-sizing: border-box; padding: 7px 9px;
    color: var(--text, #e6e9ef); background: var(--bg, #0f1420);
    border: 1px solid var(--border, #2a3350); border-radius: 7px;
    font-size: 12px;
  }
  select:focus, input:focus, textarea:focus { outline: none; border-color: var(--accent, #6c63ff); }
  textarea { resize: vertical; }
  .hint, .mode-note, .empty { margin: 0; color: var(--text-muted, #8b93ab); font-size: 11px; line-height: 1.45; }
  .mode-note { padding: 7px 9px; border-radius: 7px; background: var(--bg, #0f1420); }
  .empty { color: var(--warn, #f5c542); }
  .cron-hints { margin: 0; padding-left: 17px; color: var(--text-muted, #8b93ab); font-size: 10.5px; }
  .divider { height: 1px; margin: 5px 0; background: var(--border, #2a3350); }
  .channel-list { display: flex; flex-wrap: wrap; gap: 7px 12px; }
  .check { display: flex; align-items: flex-start; gap: 7px; color: var(--text, #e6e9ef); font-weight: 500; }
  .check input { margin-top: 2px; }
  .check small { display: block; margin-top: 2px; color: var(--text-muted, #8b93ab); font-weight: 400; line-height: 1.35; }
  .unattended { padding: 8px; border: 1px solid var(--border, #2a3350); border-radius: 7px; }
  .delivery { display: flex; flex-direction: column; gap: 7px; margin-top: 3px; padding: 9px; border: 1px solid var(--border, #2a3350); border-radius: 8px; }
</style>
