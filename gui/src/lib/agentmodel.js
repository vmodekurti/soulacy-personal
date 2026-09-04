export function modelAvailability({ provider, model, models = [], loading = false, error = '' } = {}) {
  const providerID = String(provider || '').trim()
  const modelID = String(model || '').trim()
  const list = Array.isArray(models) ? models.filter(Boolean).map(String) : []
  const err = String(error || '').trim()

  if (!providerID) {
    return {
      kind: 'warn',
      label: 'Provider missing',
      detail: 'Choose a registered provider before saving or testing this agent.',
    }
  }
  if (loading) {
    return {
      kind: 'info',
      label: 'Checking models',
      detail: `Soulacy is asking ${providerID} for the current model list.`,
    }
  }
  if (err) {
    return {
      kind: 'warn',
      label: 'Model list unavailable',
      detail: `Soulacy could not list models for ${providerID}: ${err}`,
    }
  }
  if (!modelID || modelID === '__custom__') {
    return {
      kind: 'warn',
      label: 'Model missing',
      detail: 'Choose a listed model or type a custom model name that this provider accepts.',
    }
  }
  if (list.length === 0) {
    return {
      kind: 'info',
      label: 'Custom model',
      detail: `No model list is available for ${providerID}. Soulacy will use "${modelID}" as typed.`,
    }
  }
  if (list.includes(modelID)) {
    return {
      kind: 'ok',
      label: 'Model available',
      detail: `${modelID} is listed by ${providerID}.`,
    }
  }
  return {
    kind: 'warn',
    label: 'Unlisted model',
    detail: `${modelID} is not in ${providerID}'s model list. It may still work as a custom alias, but test it before relying on this agent.`,
  }
}
