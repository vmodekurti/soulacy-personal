export function connectorList(payload) {
  return Array.isArray(payload?.connectors) ? payload.connectors : []
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
    const values = [item?.name, item?.provider, item?.summary, item?.category]
    for (const capability of item?.capabilities || []) {
      values.push(capability?.label, capability?.description)
    }
    return values.some((value) => String(value || '').toLowerCase().includes(needle))
  })
}

export function effectSummary(capabilities) {
  const writes = (capabilities || []).filter((capability) => capability?.effect === 'write').length
  return writes > 0 ? `${writes} write ${writes === 1 ? 'action' : 'actions'}` : 'Read only'
}
