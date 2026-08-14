export const TRIGGER_OPTIONS = [
  { value: 'chat', label: 'Soulacy GUI Chat' },
  { value: 'manual', label: 'Manual / programmatic' },
  { value: 'schedule', label: 'Cron schedule' },
  { value: 'channel', label: 'Channel message' },
  { value: 'webhook', label: 'Webhook' },
]

function triggerOf(draft) {
  return (draft && draft.trigger) || { type: 'manual', config: {} }
}

function channelsOf(draft) {
  return draft && Array.isArray(draft.channels) ? draft.channels.filter(Boolean) : []
}

function outputOf(draft) {
  return (draft && draft.output) || {}
}

export function deliveryModeOf(draft) {
  const explicit = String(draft?.delivery_mode || '').toLowerCase()
  if (['reply', 'none', 'outbound'].includes(explicit)) return explicit
  const triggerType = String(draft?.trigger?.type || '').toLowerCase()
  if (triggerType === 'schedule' || triggerType === 'cron') {
    return (outputOf(draft).channel || channelsOf(draft).length) ? 'outbound' : 'none'
  }
  if (triggerType === 'chat' || triggerType === 'channel') return 'reply'
  const intent = `${draft?.intent || ''} ${draft?.raw_intent || ''}`.toLowerCase()
  if (['same channel', 'same conversation', 'reply directly', 'return to the caller'].some((text) => intent.includes(text))) {
    return 'reply'
  }
  return 'outbound'
}

// Changing trigger type is a schema transition, not just a string edit. Values
// belonging to the old trigger must not leak into the saved SOUL.yaml.
export function triggerTypePatch(draft, requestedType) {
  const type = TRIGGER_OPTIONS.some((o) => o.value === requestedType)
    ? requestedType
    : 'manual'
  const previous = triggerOf(draft)
  const previousType = previous.type === 'cron' ? 'schedule' : previous.type
  const config = {}
  if (type === 'schedule' && previousType === 'schedule') {
    config.cron = (previous.config && previous.config.cron) || ''
  }
  if (type === 'channel' && previousType === 'channel') {
    config.channel = (previous.config && previous.config.channel) || ''
  }

  const patch = { trigger: { type, config } }
  if (type === 'chat') {
    patch.delivery_mode = 'reply'
    patch.channels = []
  }
  if (type !== 'schedule' || previousType !== 'schedule') {
    // These fields only affect unattended scheduled runs. Keeping them after a
    // switch to manual/channel/webhook makes the Settings panel lie about what
    // will happen and can resurrect an old destination on a later edit.
    patch.output = null
    patch.unattended = false
  }
  return patch
}

export function triggerCronPatch(draft, cron) {
  return {
    trigger: {
      type: 'schedule',
      config: { ...(triggerOf(draft).config || {}), cron: String(cron || '') },
    },
  }
}

export function triggerChannelPatch(draft, channel) {
  const id = String(channel || '').trim()
  const channels = channelsOf(draft)
  if (id && !channels.includes(id)) channels.push(id)
  return {
    trigger: { type: 'channel', config: id ? { channel: id } : {} },
    channels,
  }
}

export function toggleChannelPatch(draft, channel, selected) {
  const id = String(channel || '').trim()
  let channels = channelsOf(draft).filter((c) => c !== id)
  if (selected && id) channels = [...channels, id]

  const patch = { channels }
  const trigger = triggerOf(draft)
  if (!selected && trigger.type === 'channel' && trigger.config?.channel === id) {
    patch.trigger = { type: 'channel', config: {} }
  }

  const output = outputOf(draft)
  if (!selected && output.channel === id) {
    patch.output = { ...output, channel: '', bot_name: '' }
  } else if (selected && trigger.type === 'schedule' && !output.channel) {
    patch.output = { ...output, channel: id }
  }
  return patch
}

export function scheduleOutputPatch(draft, patch) {
  return { output: { ...outputOf(draft), ...patch } }
}

export function deliveryModePatch(draft, requestedMode) {
  const mode = ['reply', 'none', 'outbound'].includes(requestedMode) ? requestedMode : ''
  const patch = { delivery_mode: mode }
  if (mode === 'reply' || mode === 'none') patch.output = null
  const triggerType = String(draft?.trigger?.type || '').toLowerCase()
  if (mode === 'none' || (mode === 'reply' && ['chat', 'manual', 'webhook'].includes(triggerType))) patch.channels = []
  return patch
}

export function contextualDeliveryLabel(triggerType) {
  switch (String(triggerType || '').toLowerCase()) {
    case 'chat': return 'Same Soulacy GUI Chat conversation'
    case 'channel': return 'Same inbound channel conversation'
    case 'webhook': return 'HTTP response to the webhook caller'
    case 'manual': return 'Return to the manual/API caller'
    default: return 'Return through the invocation route'
  }
}

export function outboundDeliveryLabel(triggerType, channel = 'a fixed output channel') {
  const type = String(triggerType || '').toLowerCase()
  const destination = String(channel || 'a fixed output channel')
  if (type === 'schedule' || type === 'auto') return `Send to ${destination}`
  return `${contextualDeliveryLabel(type)} + also send to ${destination}`
}

export function routeSummary(triggerType, delivery, channel = '') {
  const input = {
    auto: 'Input inferred from prompt',
    chat: 'Soulacy GUI Chat message',
    manual: 'Manual or programmatic invocation',
    schedule: 'Cron schedule',
    channel: 'Connected channel message',
    webhook: 'Inbound webhook request',
  }[String(triggerType || '').toLowerCase()] || 'Selected input'
  let output = 'Output inferred from prompt'
  if (delivery === 'reply') output = contextualDeliveryLabel(triggerType)
  else if (delivery === 'none') output = String(triggerType).toLowerCase() === 'schedule' ? 'Runs / Activity only' : 'Normal response only; no external push'
  else if (delivery && delivery !== 'auto' && delivery !== 'outbound') output = outboundDeliveryLabel(triggerType, `fixed ${channel || delivery} destination`)
  else if (delivery === 'outbound') output = outboundDeliveryLabel(triggerType, 'the selected fixed output channel')
  return `${input} → ${output}`
}

// The Describe step offers an explicit operator override before the compiler
// runs. Apply it after compilation: the builder is still free to infer a
// trigger when `type` is "auto", but it cannot replace a choice the user made.
export function applyGenerationTrigger(draft, selection) {
  if (!draft || !selection) return draft

  const type = selection.type
  let next = type === 'auto' ? draft : { ...draft, ...triggerTypePatch(draft, type) }
  if (type === 'schedule') {
    next = { ...next, ...triggerCronPatch(next, selection.cron || '') }
  } else if (type === 'channel') {
    next = { ...next, ...triggerChannelPatch(next, selection.channel || '') }
  }
  const delivery = selection.delivery || 'auto'
  if (delivery === 'reply' || delivery === 'none') {
    // Keep contextual delivery as structured Studio metadata. Without this the
    // readiness validator only sees the lack of a fixed outbound channel and
    // incorrectly blocks an interactive same-channel reply.
    next = { ...next, ...deliveryModePatch(next, delivery) }
  } else if (delivery !== 'auto') {
    const channels = channelsOf(next)
    if (!channels.includes(delivery)) channels.push(delivery)
    next = {
      ...next,
      delivery_mode: 'outbound',
      channels,
      output: { ...outputOf(next), channel: delivery, to: String(selection.destination || '').trim() },
    }
  }
  return next
}

// Give the builder/refiner the same authoritative choice that is applied to
// the resulting draft. This prevents a model from writing a scheduled-agent
// system prompt and then having only the YAML trigger patched to conversational.
export function intentWithGenerationTrigger(intent, selection) {
  const text = String(intent || '').trim()
  if (!text || !selection) return text

  const channel = String(selection.channel || '').trim()
  const cron = String(selection.cron || '').trim()
  const triggerInstruction = {
    chat: 'Run only when a user sends a message in Soulacy GUI Chat. Reply in that same GUI Chat conversation; do not add a schedule, external channel binding, or proactive delivery.',
    manual: 'Run only when invoked manually or from an interactive chat. Do not add a schedule or proactive delivery.',
    schedule: `Run on this cron schedule: ${cron}. Treat this schedule as authoritative.`,
    channel: `Run when a message arrives${channel ? ` on ${channel}` : ''}. Reply conversationally on that inbound channel. Do not convert this into a scheduled job or require a separate outbound destination.`,
    webhook: 'Run only when an inbound webhook request arrives. Do not add a schedule or proactive delivery.',
  }[selection.type]
  const delivery = selection.delivery || 'auto'
  const destination = String(selection.destination || '').trim()
  const deliveryInstruction = delivery === 'reply'
    ? selection.type === 'chat'
      ? 'Reply in the same Soulacy GUI Chat conversation; do not invent a separate destination.'
      : 'Reply on the same inbound channel; do not invent a separate destination.'
    : delivery === 'none'
      ? selection.type === 'schedule'
        ? 'Keep completed results in Runs / Activity only; do not send a channel message.'
        : 'Return the result normally to the caller only; do not send an external channel message.'
      : delivery !== 'auto'
        ? `Deliver outbound results through ${delivery}${destination ? ` to ${destination}` : ''}; do not substitute another channel.`
        : ''
  const instructions = [triggerInstruction, deliveryInstruction].filter(Boolean)
  if (!instructions.length) return text
  return `${text}\n\nStudio run settings (authoritative): ${instructions.join(' ')}`
}
