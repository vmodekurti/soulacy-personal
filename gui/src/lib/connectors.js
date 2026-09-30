export function connectorList(payload) {
  return Array.isArray(payload?.connectors) ? payload.connectors : []
}

export function connectorExamples(payload) {
  return Array.isArray(payload?.examples) ? payload.examples : []
}

export function connectorCategories(items) {
  return [...new Set((items || []).map((item) => item?.category).filter(Boolean))].sort()
}

export function filterConnectors(items, query = '', category = 'all') {
  const needle = String(query || '').trim().toLowerCase()
  const wantedCategory = String(category || 'all').toLowerCase()
  return (items || []).filter((item) => {
    if (wantedCategory !== 'all' && String(item?.category || '').toLowerCase() !== wantedCategory) return false
    if (!needle) return true
    const values = [item?.name, item?.intent, item?.category]
    for (const site of item?.sites || []) values.push(site?.name, site?.domain)
    return values.some((value) => String(value || '').toLowerCase().includes(needle))
  })
}

export function selectedPlanSites(plan) {
  return (plan?.sites || []).filter((site) => site?.selected)
}

export function connectorPayload(plan) {
  return {
    name: String(plan?.name || '').trim(),
    intent: String(plan?.intent || '').trim(),
    category: String(plan?.category || 'custom').trim(),
    sites: selectedPlanSites(plan).map(({ selected, reason, ...site }) => site),
  }
}

export function siteAccessLabel(site) {
  return site?.auth_connection_id ? 'Website sign-in connected' : 'Public access ready'
}

export function advancedCapabilityCount(connector) {
  return (connector?.sites || []).reduce((total, site) => total + (site?.advanced_capabilities?.length || 0), 0)
}
