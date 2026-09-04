// steps.js — the platform walkthrough script.
//
// One stop per sidebar destination, in nav order, bookended by a short intro
// and a close. Each stop answers two questions: what this screen is, and when
// you would actually come here — the second is the part a tooltip on the icon
// could never tell you.
//
// The copy lives in `navTourCopy`, keyed by nav id. Steps are generated from
// `navPages` so the tour can never present screens in a different order than
// the sidebar does, and steps.test.js asserts the two sides stay in parity.

import { navPages, navAnchor } from '../nav.js'

/** Bumped when the script changes enough that finishers should see it again. */
export const WALKTHROUGH_VERSION = 1

/** What each destination is, and when you'd come here. */
export const navTourCopy = {
  dashboard: {
    what: 'The home screen: gateway health, your agents, and the runs that just happened.',
    when: 'Glance here first — if something broke overnight, it shows up here before anywhere else.',
  },
  onboarding: {
    what: 'The setup checklist — connect a model, deploy an agent, wire a delivery channel, turn on updates.',
    when: 'It re-reads live state every time rather than remembering what you clicked, so it stays useful long after day one.',
  },
  studio: {
    what: 'Where agents get built. Describe the job in plain language, Studio drafts a workflow, you test it, then save.',
    when: 'Start here for anything new. Everything else on this tour is either fuel for Studio or a view of what it produced.',
  },
  agents: {
    what: 'Every agent you have saved, whether it is enabled, and the controls to run, edit, export or retire it.',
    when: 'Come here to turn something on or off, or to find the agent you built last week.',
  },
  templates: {
    what: 'Ready-made agents you can deploy in one click and then open in Studio to make your own.',
    when: 'Useful as a starting point when you know roughly what you want but not how to phrase it.',
  },
  chat: {
    what: 'A direct line to any deployed agent, with token counts and the full trace of every turn.',
    when: 'The fastest way to check whether a change actually improved an answer.',
  },
  memory: {
    what: 'What your agents have learned: remembered facts, picked-up procedures, and a queue of things waiting for your approval.',
    when: 'Check the review queue when an agent starts behaving in a way you did not ask for.',
  },
  knowledge: {
    what: 'Your own documents, indexed so agents can search and cite them instead of guessing.',
    when: 'Add a knowledge base when an agent needs facts that are yours, not the model’s.',
  },
  queues: {
    what: 'The in-memory buffers that hand work between workflow steps and between live agents.',
    when: 'Mostly a diagnostic view — worth opening when a multi-step workflow stalls between stages.',
  },
  workboard: {
    what: 'The shared task board: work agents have queued for themselves, or for you.',
    when: 'Open it when an agent reports that it left something for later.',
  },
  channels: {
    what: 'Where output goes — Telegram, Slack, email, webhooks — configured and testable in one place.',
    when: 'Set a channel up here before you ask Studio to deliver anything to it.',
  },
  schedule: {
    what: 'Recurring and triggered runs: the clock side of Soulacy.',
    when: 'Once an agent works when you press the button, come here to stop pressing the button.',
  },
  skills: {
    what: 'Reusable capability packs an agent can load, installed locally or pulled from a skill source.',
    when: 'Add a skill when several agents need the same know-how and you would rather not repeat it in each prompt.',
  },
  mcp: {
    what: 'External tool servers. Anything you connect here appears in the Studio tool palette.',
    when: 'This is how an agent gets to act on the outside world beyond the built-in tools.',
  },
  pluginmgr: {
    what: 'Plugins bundle channels, tools and even whole pages of this UI. Install, enable and configure them here.',
    when: 'Worth a look when the capability you want is not built in.',
  },
  providers: {
    what: 'Your model connections, and which model is the default for agents and for Studio’s own generation.',
    when: 'The first stop on a fresh install, and the place to look when generation quality suddenly changes.',
  },
  secrets: {
    what: 'An encrypted store for API keys and tokens, referenced by name instead of pasted into configs.',
    when: 'Use it any time a config asks for a credential — the value never ends up in a YAML file.',
  },
  activity: {
    what: 'Every run, with its action log, token cost and a plain-language diagnosis when it failed.',
    when: 'This is the debugger. A failed run can be sent straight back into Studio from here.',
  },
  browser: {
    what: 'A step-by-step replay of what a browsing agent saw and clicked.',
    when: 'Open it when a web-browsing agent came back with the wrong answer and you need to see where it went.',
  },
  config: {
    what: 'The gateway configuration, edited safely from the UI instead of by hand.',
    when: 'For settings that have no dedicated screen — and to see exactly what is on disk.',
  },
  mobile: {
    what: 'A phone-sized control surface: approvals waiting on you, and quick run controls.',
    when: 'Pin this on your phone so you can approve an agent’s request without opening a laptop.',
  },
  logs: {
    what: 'Raw gateway logs, live-tailing.',
    when: 'The last resort when a failure has no diagnosis anywhere else.',
  },
}

/** The intro card — shown centred, before the tour touches the sidebar. */
export const introStep = {
  id: 'intro',
  nav: '',
  place: 'center',
  title: 'A quick tour of Soulacy',
  what: 'We’ll walk down the sidebar one screen at a time, so you know what lives where before you need it.',
  when: 'It takes a couple of minutes. Leave whenever you like — we remember where you stopped, and you can pick it up again from “Show me around” at the bottom of the sidebar.',
}

/** The closing card. */
export const outroStep = {
  id: 'outro',
  nav: '',
  place: 'center',
  title: 'That’s the whole platform',
  what: 'The short version: Providers gives you a model, Studio builds the agent, Delivery sends its output, Runs tells you what happened.',
  when: 'Replay this any time from “Show me around” at the bottom of the sidebar.',
}

/** The full ordered script: intro → one stop per nav destination → outro. */
export const walkthroughSteps = [
  introStep,
  ...navPages
    .filter((p) => navTourCopy[p.id])
    .map((p) => ({
      id: navAnchor(p.id),
      nav: p.id,
      anchor: navAnchor(p.id),
      place: 'anchor',
      title: p.label,
      icon: p.icon,
      what: navTourCopy[p.id].what,
      when: navTourCopy[p.id].when,
    })),
  outroStep,
]

export const walkthroughLength = walkthroughSteps.length

/** Clamp an arbitrary (possibly persisted, possibly stale) index into range. */
export function clampIndex(i) {
  const n = Number(i)
  if (!Number.isFinite(n)) return 0
  return Math.min(Math.max(Math.trunc(n), 0), walkthroughSteps.length - 1)
}
