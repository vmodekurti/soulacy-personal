// These API sections replace their members rather than merging individual keys.
// Include their saved values when editing one member so hidden controls survive.
const replacementSections = new Set(['executor', 'costs', 'ops', 'deployment', 'llm.studio', 'llm.reasoner'])
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value)

export function changedSettings(before, after, saved = {}, path = '') {
  const patch = {}
  for (const [key, value] of Object.entries(after)) {
    if (JSON.stringify(value) === JSON.stringify(before?.[key])) continue
    const nextPath = path ? `${path}.${key}` : key
    if (path.startsWith('plugins_config.')) {
      // Plugin settings are shallow-merged by key, including object values.
      patch[key] = value
    } else if (replacementSections.has(nextPath)) {
      patch[key] = { ...saved?.[key], ...value }
    } else if (object(value)) {
      const nested = changedSettings(before?.[key], value, saved?.[key], nextPath)
      if (Object.keys(nested).length) patch[key] = nested
    } else {
      patch[key] = value
    }
  }
  return patch
}
