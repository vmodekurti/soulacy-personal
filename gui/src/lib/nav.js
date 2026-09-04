// nav.js — the single source of truth for the left-hand navigation.
//
// App.svelte renders the sidebar from `navPages`, and the platform walkthrough
// tours the same list. Keeping one array means a new screen is added in exactly
// one place; `walkthrough/steps.test.js` fails the build when a nav entry has no
// tour step (or a tour step points at a nav id that no longer exists), which is
// the drift that quietly rots every hand-maintained product tour.

/** Ordered nav sections with their (optional) uppercase headers. */
export const navGroups = [
  { key: 'main',         label: ''             },
  { key: 'capabilities', label: 'Capabilities' },
  { key: 'integrations', label: 'Integrations' },
  { key: 'system',       label: 'System'       },
]

/** Every built-in destination in the sidebar, in render order. */
export const navPages = [
  { id: 'dashboard', icon: '◈', label: 'Dashboard',   group: 'main'         },
  { id: 'onboarding', icon: '✓', label: 'First Run',   group: 'main'         },
  { id: 'studio',    icon: '🎬', label: 'Studio',      group: 'main'         },
  { id: 'agents',    icon: '⊕', label: 'Deployed',    group: 'main'         },
  { id: 'templates', icon: '📋', label: 'Templates',   group: 'main'         },
  { id: 'chat',      icon: '◎', label: 'Chat',        group: 'main'         },
  { id: 'memory',    icon: '🧠', label: 'Learning',    group: 'capabilities' },
  { id: 'knowledge', icon: '📚', label: 'Knowledge',   group: 'capabilities' },
  { id: 'queues',    icon: '☷', label: 'Queues',      group: 'capabilities' },
  { id: 'workboard', icon: '▦', label: 'Workboard',   group: 'capabilities' },
  { id: 'channels',  icon: '📡', label: 'Delivery',    group: 'integrations' },
  { id: 'schedule',  icon: '⏱', label: 'Automations', group: 'integrations' },
  { id: 'skills',    icon: '🧩', label: 'Skills',      group: 'integrations' },
  { id: 'mcp',       icon: '🔌', label: 'MCP',         group: 'integrations' },
  { id: 'pluginmgr', icon: '🧱', label: 'Plugins',     group: 'integrations' },
  { id: 'providers', icon: '⚙', label: 'Providers',   group: 'integrations' },
  { id: 'secrets',   icon: '🔑', label: 'Secrets',     group: 'integrations' },
  { id: 'activity',  icon: '📈', label: 'Runs',        group: 'system'       },
  { id: 'browser',   icon: '🕸', label: 'Browser',     group: 'system'       },
  { id: 'config',    icon: '≡', label: 'Config',      group: 'system'       },
  { id: 'mobile',    icon: '▣', label: 'Mobile',      group: 'system'       },
  { id: 'logs',      icon: '📋', label: 'Logs',        group: 'system'       },
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
