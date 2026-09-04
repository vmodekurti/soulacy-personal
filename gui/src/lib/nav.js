// nav.js — the single source of truth for the left-hand navigation.
//
// App.svelte renders the sidebar from `navPages`, and the platform walkthrough
// tours the same list. Keeping one array means a new screen is added in exactly
// one place; `walkthrough/steps.test.js` fails the build when a nav entry has no
// tour step (or a tour step points at a nav id that no longer exists), which is
// the drift that quietly rots every hand-maintained product tour.

import { editionCapabilities } from './edition.js'
import { editionHas } from '../distribution/edition.js'

/** Ordered nav sections with their (optional) uppercase headers. */
export const navGroups = [
  { key: 'main',         label: ''             },
  { key: 'capabilities', label: 'Capabilities' },
  { key: 'integrations', label: 'Integrations' },
  { key: 'system',       label: 'System'       },
]

// MU-030 criterion 2. `requires` names the permission a destination is useless
// without — read access to the thing the page is about. A viewer offered a
// Secrets tab that answers 403 on open has been told they can do something
// they cannot, and learns to distrust the rest of the navigation.
//
// A page with no `requires` is reachable by anyone who is signed in. That is
// the honest default: most screens are read-mostly, the gate that matters is
// on the route, and hiding a page nobody is forbidden from is worse than
// showing it.
//
// This is CHROME, not a boundary. Every route authorizes itself; a user who
// types the fragment reaches the page and the page's calls still fail.

/** Every built-in destination in the sidebar, in render order. */
export const navPages = [
  { id: 'dashboard', icon: '◈', label: 'Dashboard',   group: 'main'         },
  { id: 'onboarding', icon: '✓', label: 'First Run',   group: 'main'         },
  { id: 'studio',    icon: '🎬', label: 'Studio',      group: 'main', requires: ['studio', 'write'] },
  { id: 'agents',    icon: '⊕', label: 'Deployed',    group: 'main', requires: ['agents', 'read'] },
  { id: 'templates', icon: '📋', label: 'Templates',   group: 'main'         },
  { id: 'chat',      icon: '◎', label: 'Chat',        group: 'main', requires: ['chat', 'read'] },
  { id: 'memory',    icon: '🧠', label: 'Learning',    group: 'capabilities', requires: ['memory', 'read'] },
  { id: 'knowledge', icon: '📚', label: 'Knowledge',   group: 'capabilities', requires: ['knowledge', 'read'] },
  { id: 'queues',    icon: '☷', label: 'Queues',      group: 'capabilities', demoUnavailable: true },
  { id: 'workboard', icon: '▦', label: 'Workboard',   group: 'capabilities', demoUnavailable: true },
  { id: 'channels',  icon: '📡', label: 'Delivery',    group: 'integrations', requires: ['channels', 'read'] },
  { id: 'schedule',  icon: '⏱', label: 'Automations', group: 'integrations', requires: ['schedule', 'read'] },
  { id: 'skills',    icon: '🧩', label: 'Skills',      group: 'integrations', requires: ['skills', 'read'] },
  { id: 'mcp',       icon: '🔌', label: 'MCP',         group: 'integrations', requires: ['mcp', 'read'] },
  { id: 'connected-apps', icon: '🔗', label: 'Connected Apps', group: 'integrations', requires: ['mcp', 'read'] },
  { id: 'pluginmgr', icon: '🧱', label: 'Plugins',     group: 'integrations', requires: ['plugins', 'read'] },
  { id: 'providers', icon: '⚙', label: 'Providers',   group: 'integrations', requires: ['providers', 'read'] },
  { id: 'secrets',   icon: '🔑', label: 'Secrets',     group: 'integrations', requires: ['secrets', 'list'] },
  { id: 'activity',  icon: '📈', label: 'Runs',        group: 'system', demoUnavailable: true },
  { id: 'browser',   icon: '🕸', label: 'Browser',     group: 'system', demoUnavailable: true },
  { id: 'config',    icon: '≡', label: 'Config',      group: 'system',       requires: ['config', 'read'] },
  { id: 'mobile',    icon: '▣', label: 'Mobile',      group: 'system', demoUnavailable: true },
  { id: 'logs',      icon: '📋', label: 'Logs',        group: 'system', excludedByEditionCapability: editionCapabilities.multiUser },
  { id: 'members',   icon: '👥', label: 'Members',     group: 'system', requires: ['rbac', 'read'], editionCapability: editionCapabilities.multiUser },
  { id: 'workspace-admin', icon: '🛡', label: 'Workspace settings', group: 'system', workspaceAdminOnly: true, editionCapability: editionCapabilities.workspaceAdmin },
]

/** Nav ids in render order. */
export const navIds = navPages.map((p) => p.id)

/**
 * visibleNavPages filters the sidebar to what this caller can use.
 *
 * `allow` is injected rather than imported so this stays a pure function the
 * walkthrough's own tests can drive without standing up a permission store.
 */
export function visibleNavPages(allow, pages = navPages, options = {}) {
  const mode = options.deploymentMode
  const multiUser = editionHas(mode, editionCapabilities.multiUser)
  const role = String(options.role || '').toLowerCase()
  return pages
    .filter((p) => {
      if (role === 'demo_developer' && p.demoUnavailable) return false
      if (p.editionCapability && !editionHas(mode, p.editionCapability)) return false
      if (p.excludedByEditionCapability && editionHas(mode, p.excludedByEditionCapability)) return false
      if (p.ownerOnly && role !== 'owner') return false
      if (p.workspaceAdminOnly && !['owner', 'admin'].includes(role)) return false
      return typeof allow !== 'function' || !p.requires || allow(p.requires[0], p.requires[1])
    })
    .map((p) => multiUser && p.id === 'providers'
      ? { ...p, label: 'Providers & models' }
      : p)
}

/**
 * The `data-tour` value stamped on a nav button. The walkthrough overlay looks
 * elements up with exactly this string, so both sides derive it from one
 * function rather than retyping the selector.
 */
export function navAnchor(id) {
  return `nav:${id}`
}
