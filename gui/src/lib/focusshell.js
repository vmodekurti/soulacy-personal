// focusshell.js — the rules of the chat-first shell.
//
// In the chat-first shell the conversation is the home screen and every other
// page is something the conversation summons into a panel beside it. This file
// holds the decisions that make that true — which route is home, how much room
// a summoned page gets, what "/" finds, and what the opening brief says — as
// plain functions, so the shell and the composer cannot disagree about them and
// each rule can be tested without mounting the app.

import { navPages } from './nav.js'

export const shellModes = [
  { key: 'focus',     label: 'Chat first',     hint: 'One conversation; everything else opens beside it' },
  { key: 'workspace', label: 'Full workspace', hint: 'The sidebar and every screen, as before' },
]

/** The route that shows the conversation alone. */
export const HOME_PAGE = 'chat'

export function isHomePage(page) {
  return page === HOME_PAGE
}

// Editors and canvases cannot work in half a window. They take the stage and
// the conversation narrows to a column beside them.
const WIDE_PAGES = new Set(['studio', 'start', 'missions', 'autopilot', 'browser', 'reports'])

// First-run setup comes before there is a model to talk to, so a conversation
// beside it would be a dead composer. It gets the whole window.
const SOLO_PAGES = new Set(['onboarding'])

/**
 * How a page shares the window with the conversation.
 *   ''     — home; no panel
 *   'side' — the conversation leads, the page sits beside it
 *   'wide' — the page leads, the conversation narrows
 *   'solo' — the page alone
 */
export function panelLayout(page) {
  if (!page || isHomePage(page)) return ''
  if (SOLO_PAGES.has(page)) return 'solo'
  if (WIDE_PAGES.has(page)) return 'wide'
  return 'side'
}

// Words people use that are not the label on the screen. The nav calls the
// schedule "Automations" and the run log "Runs"; nobody should have to know.
const ALIASES = {
  feed: ['home', 'inbox', 'results', 'approvals'],
  start: ['builder', 'genie', 'create agent', 'new agent'],
  agents: ['agents', 'deployed'],
  schedule: ['schedule', 'cron', 'calendar', 'recurring'],
  activity: ['activity', 'runs', 'history'],
  memory: ['memory', 'learning'],
  person: ['profile', 'me', 'about'],
  config: ['settings', 'config', 'preferences', 'admin'],
  channels: ['channels', 'delivery', 'telegram', 'email'],
  workboard: ['tasks', 'board', 'kanban'],
  providers: ['models', 'llm', 'providers'],
  secrets: ['keys', 'credentials'],
  websites: ['logins', 'website access'],
  pluginmgr: ['plugins'],
  reports: ['analytics'],
}

// What "/" offers before anything is typed: the places people go most.
const DEFAULT_SUMMON = ['schedule', 'agents', 'knowledge', 'workboard', 'activity', 'config']

/**
 * Pages matching what was typed after "/". Every page is findable here
 * regardless of menu level: hidden by default must never mean unreachable.
 */
export function summonMatches(query, { limit = 6, pages = navPages } = {}) {
  const candidates = pages.filter((p) => !isHomePage(p.id))
  const q = String(query || '').trim().toLowerCase()
  if (!q) {
    return DEFAULT_SUMMON
      .map((id) => candidates.find((p) => p.id === id))
      .filter(Boolean)
      .slice(0, limit)
  }
  const scored = []
  for (const p of candidates) {
    const words = [p.label.toLowerCase(), p.id, ...(ALIASES[p.id] || [])]
    let best = 0
    for (const w of words) {
      if (w === q) best = Math.max(best, 3)
      else if (w.startsWith(q)) best = Math.max(best, 2)
      else if (w.includes(q)) best = Math.max(best, 1)
    }
    if (best) scored.push({ p, best })
  }
  // Stable sort keeps nav order within a rank.
  return scored.sort((a, b) => b.best - a.best).slice(0, limit).map((s) => s.p)
}

export function greetingFor(now = new Date()) {
  const h = now.getHours()
  if (h < 5) return 'Still up'
  if (h < 12) return 'Good morning'
  if (h < 18) return 'Good afternoon'
  return 'Good evening'
}

const sameDay = (a, b) =>
  a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()

const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`

function clock(d) {
  return d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

const humanTool = (tool) => String(tool || '').replace(/[_.-]+/g, ' ').trim()

/**
 * The opening brief: one sentence about the day and, only when something is
 * waiting, the single oldest thing the person has to decide.
 *
 * It used to carry three counters that were on screen even when all were zero.
 * Now a quiet day is a greeting and a sentence, and nothing else. It must never
 * claim more than the data says — an empty day reads as an empty day.
 */
export function buildBrief({ approvals = [], running = 0, runs = [], agents = [], now = new Date() } = {}) {
  const nameOf = (id) => agents.find((a) => a.id === id)?.name || id || 'An agent'
  const finishedToday = runs
    .filter((r) => r.status !== 'running' && r.status !== 'pending')
    .map((r) => ({ ...r, at: new Date(r.updatedAt || r.startedAt || 0) }))
    .filter((r) => !Number.isNaN(r.at.getTime()) && sameDay(r.at, now))
    .sort((a, b) => b.at - a.at)
  const runningCount = Math.max(0, Number(running) || 0)

  // Oldest first: the thing that has waited longest is the one to show.
  const queue = approvals
    .filter((a) => a && a.call_id)
    .slice()
    .sort((a, b) => new Date(a.created_at || 0) - new Date(b.created_at || 0))
  const first = queue[0]
  const waiting = first
    ? {
        callId: first.call_id,
        lead: queue.length === 1 ? 'One thing needs you' : `${queue.length} things need you. First`,
        text: String(first.reason || '').trim() || `Allow ${humanTool(first.tool) || 'an action'}`,
        // Who is asking and what it will use, so a yes is never blind.
        detail: [nameOf(first.agent_id), humanTool(first.tool)].filter(Boolean).join(' · '),
      }
    : null

  const parts = []
  const latest = finishedToday[0]
  if (latest) {
    const failed = latest.ok === false || latest.status === 'failed'
    parts.push(`${nameOf(latest.agentId)} ${failed ? 'failed' : 'finished'} at ${clock(latest.at)}.`)
  }
  if (runningCount) parts.push(`${plural(runningCount, 'run is', 'runs are')} still working.`)
  if (!parts.length) {
    parts.push(waiting ? 'Nothing has run yet today.' : 'Nothing has run yet today, and nothing is waiting on you.')
  }

  return { greeting: greetingFor(now), sentence: parts.join(' '), waiting }
}

/** Fetch what the brief needs. Each source fails soft: a missing endpoint is a zero. */
export async function loadBrief(api, now = new Date()) {
  const [app, act, ledger, ag] = await Promise.all([
    api.approvals.list().catch(() => ({ approvals: [] })),
    api.activity.running().catch(() => ({ count: 0 })),
    api.runs.ledger({ limit: 60, eventLimit: 2000 }).catch(() => ({ runs: [] })),
    api.agents.list().catch(() => ({ agents: [] })),
  ])
  return buildBrief({
    approvals: app?.approvals || [],
    running: act?.count || 0,
    runs: ledger?.runs || [],
    agents: ag?.agents || [],
    now,
  })
}
