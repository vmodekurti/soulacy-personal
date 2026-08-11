// buildspecview.js — turn the /studio/build-spec payload into display rows.
//
// ST-01 asks Studio to convert an intent into a structured specification the
// user can VERIFY before anything is generated. The endpoint returns that
// structure; this maps it to the reviewable rows the Describe screen shows, and
// keeps two things honest that a naive rendering gets wrong:
//
//  1. An empty section is not the same as an absent one. "Delivery: —" tells
//     the user Studio found no destination, which is exactly the thing they
//     need to notice before pressing Generate. Silently dropping the row hides
//     the omission until the workflow fails to deliver anything.
//
//  2. Blockers are not warnings. A blocker means Generate would produce
//     something known-incomplete, so it is rendered as an action to resolve,
//     not a note to skim.
//
// Pure and presentation-free so it can be tested without mounting a component.

/** Human label for a strategy token. */
export function strategyLabel(mode) {
  switch (String(mode || '').toLowerCase()) {
    case 'workflow': return 'Workflow (fixed flow)'
    case 'auto': return 'Auto (native tool calling)'
    case 'react': return 'ReAct (advanced)'
    case 'plan_execute': return 'Plan-Execute'
    default: return ''
  }
}

/** Join a list for display, or return '' when there is nothing. */
function list(v) {
  if (!Array.isArray(v)) return typeof v === 'string' ? v.trim() : ''
  return v.filter(Boolean).map((x) => (typeof x === 'string' ? x : x && (x.name || x.id))).filter(Boolean).join(', ')
}

/** Stage lines, marking parallel groups so fan-out is visible before build. */
function stageLines(stages) {
  if (!Array.isArray(stages)) return []
  return stages.filter(Boolean).map((s) => {
    const name = s.name || s.title || ''
    const detail = s.detail ? ` — ${s.detail}` : ''
    // Parallelism is a structural decision the user should see in the plain
    // description, not discover on the canvas.
    return `${name}${detail}${s.parallel ? '  ·  runs in parallel' : ''}`
  }).filter((l) => l.trim())
}

function triggerQuestion(q) {
  const text = `${q && q.id || ''} ${q && q.field || ''} ${q && q.question || ''}`.toLowerCase()
  return /schedule|cron|time of day/.test(text)
}

/**
 * Apply the operator's creation-time trigger choice to the review spec.
 *
 * The server spec is an inference from prose. Once a person selects a trigger,
 * continuing to show the inferred schedule (and its required destination/time
 * questions) makes the review panel contradict the value that will be saved.
 * Return a copy so the raw server response remains available for comparison.
 */
export function specWithTrigger(spec, selection) {
  if (!spec || !selection) return spec

  const type = selection.type || 'auto'
  const deliveryChoice = selection.delivery || 'auto'
  if (type === 'auto' && deliveryChoice === 'auto') return spec
  const channel = String(selection.channel || '').trim()
  const destination = String(selection.destination || '').trim()
  const cron = String(selection.cron || '').trim()
  const labels = {
    manual: 'manual / on demand',
    schedule: 'schedule',
    channel: channel ? `incoming ${channel} message` : 'incoming channel message',
    webhook: 'inbound webhook',
  }
  const keepQuestion = (q) => {
    // The trigger control owns this value and validates it separately.
    if (type !== 'auto' && triggerQuestion(q)) return false
    // Manual, webhook and same-channel conversational runs do not need the
    // outbound destination that a scheduled job requires. A user can still add
    // delivery after generation; it must not remain a stale generation gate.
    if (((type !== 'auto' && type !== 'schedule' && deliveryChoice === 'auto') ||
        deliveryChoice !== 'auto') && isDeliveryQuestion(q)) return false
    // GenerationTrigger owns channel selection, so do not render a second,
    // conflicting input-channel question below it.
    if (type === 'channel' && (q?.field === 'trigger' || q?.id === 'input_channel')) return false
    return true
  }
  const questions = (Array.isArray(spec.questions) ? spec.questions : []).filter(keepQuestion)
  const blockers = (Array.isArray(spec.blockers) ? spec.blockers : []).filter(keepQuestion)
  const next = {
    ...spec,
    trigger: type === 'auto' ? spec.trigger : (labels[type] || type),
    schedule: type === 'auto' ? spec.schedule : (type === 'schedule' ? cron : ''),
    schedule_text: type === 'auto' ? spec.schedule_text : (type === 'schedule' ? cron : ''),
    questions,
    blockers,
    ready: blockers.length === 0 && !questions.some((q) => q && q.blocker),
  }
  if (deliveryChoice === 'reply') {
    next.delivery = ['Reply on the inbound channel']
  } else if (deliveryChoice === 'none') {
    next.delivery = []
  } else if (deliveryChoice !== 'auto') {
    next.delivery = [destination ? `${deliveryChoice} → ${destination}` : deliveryChoice]
  } else if (type !== 'auto' && type !== 'schedule') {
    next.delivery = type === 'channel' && channel ? [`Reply on ${channel}`] : []
  }
  if (deliveryChoice === 'reply' || deliveryChoice === 'none' ||
      (deliveryChoice === 'auto' && type !== 'auto' && type !== 'schedule')) {
    next.security = (Array.isArray(spec.security) ? spec.security : [])
      .filter((item) => !/^sends messages on your behalf/i.test(String(item || '')))
  }
  return next
}

/**
 * specRows builds the ordered review rows.
 * `spec` is the build-spec payload; `recommendation` is { mode, rationale }.
 * Every row is returned even when empty, with `empty: true` so the UI can show
 * "not specified" rather than omitting it.
 */
export function specRows(spec, recommendation) {
  const s = spec || {}
  const rows = []

  // `reason` is what StrategyAdvice serialises; `rationale` is the older
  // generated-workflow field. Both reach this row, so accept either rather than
  // silently dropping the explanation for whichever source is in play.
  const rec = recommendation || {}
  const strat = strategyLabel(rec.mode)
  rows.push({
    key: 'strategy', icon: '◇', label: 'Strategy',
    value: strat, detail: rec.rationale || rec.reason || '',
    empty: !strat,
  })

  const when = [s.trigger, s.schedule_text || s.schedule].filter(Boolean).join(' · ')
  rows.push({ key: 'trigger', icon: '◷', label: 'Trigger', value: when, empty: !when })

  const sources = list(s.inputs) || list(s.integrations)
  rows.push({ key: 'sources', icon: '⊙', label: 'Sources', value: sources, empty: !sources })

  const stages = stageLines(s.stages)
  rows.push({ key: 'stages', icon: '▤', label: 'Work', value: stages.join('\n'), lines: stages, empty: !stages.length })

  const out = list(s.outputs)
  rows.push({ key: 'outputs', icon: '⌸', label: 'Produces', value: out, empty: !out })

  const delivery = list(s.delivery)
  rows.push({ key: 'delivery', icon: '➤', label: 'Delivery', value: delivery, empty: !delivery })

  const caps = list(s.integrations)
  rows.push({ key: 'capabilities', icon: '⚙', label: 'Capabilities', value: caps, empty: !caps })

  const sec = list(s.security)
  // Only shown when there IS something: an empty security row reads as
  // reassurance, and this function has no basis for reassuring anyone.
  if (sec) rows.push({ key: 'security', icon: '⚠', label: 'Security', value: sec, empty: false })

  return rows
}

/**
 * specBlockers returns the questions that must be answered before generating.
 * Falls back to `questions[].blocker` when the endpoint did not pre-filter.
 */
export function specBlockers(spec) {
  const s = spec || {}
  if (Array.isArray(s.blockers) && s.blockers.length) return s.blockers
  return (Array.isArray(s.questions) ? s.questions : []).filter((q) => q && q.blocker)
}

/**
 * unresolvedBlockers are the spec blockers the user has not yet answered.
 *
 * This is THE gate on Generate, and it is exported rather than computed inside
 * the panel because the header also has a Generate button. While the predicate
 * lived only in BuildSpecPanel, that header button generated straight past the
 * required answers — the check was enforced on the button the user could see
 * the reason next to, and skipped on the one they were more likely to press.
 *
 * `spec.ready` is not usable here: it describes the spec as the SERVER saw it,
 * before these answers were typed.
 */
export function unresolvedBlockers(spec, answers) {
  const a = answers || {}
  return specBlockers(spec).filter((b) => !String(a[b && b.id] || '').trim())
}

/** Non-blocking clarifying questions — useful, but they do not gate Generate. */
export function specQuestions(spec) {
  const s = spec || {}
  const blockers = new Set(specBlockers(s).map((b) => b && b.id))
  return (Array.isArray(s.questions) ? s.questions : []).filter((q) => q && !q.blocker && !blockers.has(q.id))
}

/** True when Studio has enough to build something coherent. */
export function specReady(spec) {
  if (!spec) return false
  if (typeof spec.ready === 'boolean') return spec.ready
  return specBlockers(spec).length === 0
}

// ── Delivery destination ───────────────────────────────────────────────────
//
// "Where exactly should the result be delivered (which chat, channel, or
// address)?" is unanswerable as written. It asks the user to supply an
// identifier in a format only the adapter knows: a Telegram chat is a negative
// integer or an @name, a Slack channel is a C-prefixed ID or #name, a Discord
// channel is an 18-digit snowflake, WhatsApp wants E.164. Someone who has never
// seen those will type "my group chat" and be told it is invalid.
//
// Once the spec names the channel we know which of those to ask for, so the
// question becomes specific and says where to FIND the value.

const DELIVERY_PROMPTS = {
  telegram: {
    label: 'Telegram chat',
    question: 'Which Telegram chat should receive this?',
    placeholder: '-1001234567890  or  @mychannel',
    help: 'A numeric chat ID, or a public channel’s @username. To find a group’s ID, forward one of its messages to @getidsbot; for your own, message @userinfobot. Group IDs are negative.',
  },
  slack: {
    label: 'Slack channel',
    question: 'Which Slack channel should receive this?',
    placeholder: '#general  or  C0123456789',
    help: 'The channel name including #, or its ID (starts with C) from the channel’s details panel or the end of its URL. The bot must be invited to private channels.',
  },
  discord: {
    label: 'Discord channel',
    question: 'Which Discord channel should receive this?',
    placeholder: '123456789012345678',
    help: 'The numeric channel ID. Enable Developer Mode (User Settings → Advanced), then right-click the channel → Copy Channel ID.',
  },
  whatsapp: {
    label: 'WhatsApp number',
    question: 'Which WhatsApp number should receive this?',
    placeholder: '+14155551234',
    help: 'The recipient’s number in international E.164 format, including the country code and no spaces.',
  },
  email: {
    label: 'Email address',
    question: 'Which email address should receive this?',
    placeholder: 'you@example.com',
    help: 'Separate multiple recipients with commas.',
  },
  http: {
    label: 'Webhook URL',
    question: 'Which URL should receive this?',
    placeholder: 'https://example.com/hooks/soulacy',
    help: 'Soulacy POSTs a JSON body to this URL. It must be reachable from wherever the gateway runs.',
  },
  sms: {
    label: 'Phone number',
    question: 'Which phone number should receive this?',
    placeholder: '+14155551234',
    help: 'International E.164 format, including the country code.',
  },
  console: {
    label: 'Console',
    question: '',
    placeholder: '',
    help: 'Console output needs no destination.',
  },
}

/** Normalise whatever the spec called the channel to a known adapter key. */
export function detectChannel(delivery) {
  const text = (Array.isArray(delivery) ? delivery.join(' ') : String(delivery || '')).toLowerCase()
  if (!text.trim()) return ''
  // Ordered so a more specific match wins: whatsapp_web before a bare "web",
  // and "webhook" must not be read as "web".
  if (text.includes('telegram')) return 'telegram'
  if (text.includes('slack')) return 'slack'
  if (text.includes('discord')) return 'discord'
  if (text.includes('whatsapp')) return 'whatsapp'
  if (text.includes('sms') || text.includes('twilio')) return 'sms'
  if (text.includes('mail')) return 'email'
  if (text.includes('webhook') || text.includes('http')) return 'http'
  if (text.includes('console') || text.includes('stdout')) return 'console'
  return ''
}

/**
 * deliveryPrompt returns the channel-specific ask, or a generic one when the
 * channel is unknown. `needsDestination` is false for channels that genuinely
 * have no address, so the UI does not demand one that cannot exist.
 */
export function deliveryPrompt(delivery) {
  const channel = detectChannel(delivery)
  const p = DELIVERY_PROMPTS[channel]
  if (!p) {
    return {
      channel: '',
      label: 'Destination',
      question: 'Where exactly should the result be delivered?',
      placeholder: 'chat ID, channel name, address or URL',
      help: 'Studio could not tell which channel this uses, so give the destination in whatever form that channel expects.',
      needsDestination: true,
    }
  }
  return { channel, ...p, needsDestination: channel !== 'console' }
}

/**
 * knownDestinations extracts the destinations Soulacy is ALREADY configured for,
 * from the `GET /channels` payload, so the user picks one instead of hunting for
 * an ID.
 *
 * Sources, in order of confidence:
 *   • settings.default_output_to — the configured default destination
 *   • settings.allowed_chat_ids  — the allow-list (comma separated)
 *   • bots[].default_output_to / bots[].allowed_chat_ids — per-bot mappings
 *
 * Returns [{ value, label }] deduped and in that order, so the most likely
 * answer is first. An empty result is meaningful: it means nothing is
 * configured, and the caller should fall back to asking for a raw ID rather
 * than showing an empty dropdown that looks broken.
 */
export function knownDestinations(channels, channelId) {
  if (!channelId) return []
  const list = Array.isArray(channels) ? channels : (channels && channels.channels) || []
  const ch = list.find((c) => c && String(c.id).toLowerCase() === String(channelId).toLowerCase())
  if (!ch) return []

  const out = []
  const seen = new Set()
  const add = (raw, label) => {
    const value = String(raw == null ? '' : raw).trim()
    if (!value || seen.has(value)) return
    seen.add(value)
    out.push({ value, label: label ? `${value} — ${label}` : value })
  }
  const addCSV = (raw, label) => {
    String(raw == null ? '' : raw)
      .split(',')
      .forEach((part) => add(part, label))
  }

  const s = ch.settings || {}
  add(s.default_output_to, 'default destination')
  addCSV(s.allowed_chat_ids, 'allowed')

  for (const bot of Array.isArray(ch.bots) ? ch.bots : []) {
    if (!bot) continue
    const who = bot.bot_name || bot.name || ''
    add(bot.default_output_to, who ? `${who} default` : 'bot default')
    addCSV(bot.allowed_chat_ids, who || 'bot')
  }
  return out
}

/** True when this question is asking for the delivery destination. */
export function isDeliveryQuestion(q) {
  if (!q) return false
  const field = String(q.field || '').toLowerCase()
  if (field.includes('deliver') || field.includes('channel') || field.includes('destination')) return true
  const text = String(q.question || q.message || '').toLowerCase()
  return text.includes('deliver') && (text.includes('where') || text.includes('which'))
}

/**
 * changeSummary describes what a refine actually changed — ST-01's "visible
 * change summary". Returns null when nothing was compared, which is different
 * from "compared and found identical": the first means we cannot say, and
 * claiming "no changes" in that case would be a lie.
 */
export function changeSummary(spec) {
  const s = spec || {}
  if (!s.compared) return null
  const diff = Array.isArray(s.diff) ? s.diff.filter(Boolean) : []
  return {
    material: !!s.materially_different,
    changes: diff.map((d) => ({
      field: d.field || '',
      before: d.before || '',
      after: d.after || '',
      kind: d.kind || (d.before && d.after ? 'changed' : d.after ? 'added' : 'removed'),
    })),
    // A refine that produced no material difference is worth saying out loud —
    // it means pressing Refine again is unlikely to help.
    summary: !s.materially_different
      ? 'Refining did not materially change the specification.'
      : `${diff.length} change${diff.length === 1 ? '' : 's'} to the specification.`,
  }
}
