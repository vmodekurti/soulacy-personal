// nav.js — the single source of truth for the left-hand navigation.
//
// App.svelte renders the sidebar from `navPages`, and the platform walkthrough
// tours the same list. Keeping one array means a new screen is added in exactly
// one place; `walkthrough/steps.test.js` fails the build when a nav entry has no
// tour step (or a tour step points at a nav id that no longer exists), which is
// the drift that quietly rots every hand-maintained product tour.

/**
 * How much of the product a person wants to see.
 *
 * The sidebar had 26 destinations and a first-time user met all of them at
 * once, which reads as "this is going to be a lot of work" before anything has
 * been done. Level is per-page rather than a separate list so a screen cannot
 * be added without deciding who it is for, and so the walkthrough and the
 * smoke tests keep operating on one complete array.
 *
 * Deep links always work: navigating to a page above the current level shows
 * it, it simply is not listed until the level is raised.
 */
export const navLevels = [
  { key: 'simple',   label: 'Simple',   hint: 'Just the essentials' },
  { key: 'standard', label: 'Standard', hint: 'Everyday tools' },
  { key: 'advanced', label: 'Advanced', hint: 'Everything, including developer surfaces' },
]

/** Pages visible at a given level. Higher levels include everything below. */
export function pagesForLevel(level) {
  const rank = { simple: 0, standard: 1, advanced: 2 }
  const want = rank[level] ?? 0
  return navPages.filter((p) => (rank[p.level] ?? 2) <= want)
}

/** Ordered nav sections with their (optional) uppercase headers. */
export const navGroups = [
  { key: 'main',         label: ''             },
  { key: 'capabilities', label: 'Capabilities' },
  { key: 'integrations', label: 'Integrations' },
  { key: 'system',       label: 'System'       },
]

/** Every built-in destination in the sidebar, in render order. */
export const navPages = [
  { id: 'start',     icon: '✦', label: 'Genie',       group: 'main', level: 'simple' },
  { id: 'dashboard', icon: '◈', label: 'Dashboard',   group: 'main', level: 'simple' },
  { id: 'onboarding', icon: '✓', label: 'First Run',   group: 'main', level: 'standard' },
  { id: 'studio',    icon: '🎬', label: 'Studio',      group: 'main', level: 'standard' },
  { id: 'agents',    icon: '⊕', label: 'Deployed',    group: 'main', level: 'simple' },
  { id: 'templates', icon: '📋', label: 'Templates',   group: 'main', level: 'simple' },
  { id: 'chat',      icon: '◎', label: 'Chat',        group: 'main', level: 'simple' },
  { id: 'autopilot', icon: '◇', label: 'Autopilot',   group: 'main', level: 'advanced' },
  { id: 'person',    icon: '👤', label: 'About You',   group: 'capabilities', level: 'simple' },
  { id: 'memory',    icon: '🧠', label: 'Learning',    group: 'capabilities', level: 'standard' },
  { id: 'knowledge', icon: '📚', label: 'Knowledge',   group: 'capabilities', level: 'standard' },
  { id: 'queues',    icon: '☷', label: 'Queues',      group: 'capabilities', level: 'advanced' },
  { id: 'workboard', icon: '▦', label: 'Workboard',   group: 'capabilities', level: 'advanced' },
  { id: 'channels',  icon: '📡', label: 'Delivery',    group: 'integrations', level: 'standard' },
  { id: 'schedule',  icon: '⏱', label: 'Automations', group: 'integrations', level: 'standard' },
  { id: 'skills',    icon: '🧩', label: 'Skills',      group: 'integrations', level: 'standard' },
  { id: 'mcp',       icon: '🔌', label: 'MCP',         group: 'integrations', level: 'advanced' },
  { id: 'pluginmgr', icon: '🧱', label: 'Plugins',     group: 'integrations', level: 'advanced' },
  { id: 'providers', icon: '⚙', label: 'Providers',   group: 'integrations', level: 'standard' },
  { id: 'secrets',   icon: '🔑', label: 'Secrets',     group: 'integrations', level: 'advanced' },
  { id: 'activity',  icon: '📈', label: 'Runs',        group: 'system', level: 'standard' },
  { id: 'reports',   icon: '▤', label: 'Reports',     group: 'system', level: 'advanced' },
  { id: 'browser',   icon: '🕸', label: 'Browser',     group: 'system', level: 'advanced' },
  { id: 'config',    icon: '≡', label: 'Config',      group: 'system', level: 'advanced' },
  { id: 'mobile',    icon: '▣', label: 'Mobile',      group: 'system', level: 'advanced' },
  { id: 'logs',      icon: '📋', label: 'Logs',        group: 'system', level: 'advanced' },
]

/** Nav ids in render order. */
export const navIds = navPages.map((p) => p.id)

/**
 * The `data-tour` value stamped on a nav button. The walkthrough overlay looks
 * elements up with exactly this string, so both sides derive it from one
 * function rather than retyping the selector.
 */
export function navAnchor(id) {
  return `nav:${id}`
}
