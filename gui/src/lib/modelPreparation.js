const supports = new Set(['supported', 'unsupported', 'unknown'])
const text = (v, max) => typeof v === 'string' && v.length <= max
const limit = v => Number.isInteger(v) && v >= 0 && v <= 16 * 1024 * 1024
export function validatePreparation(value, agentID) {
  const p = value?.profile
  if (value?.agent_id !== agentID || value?.goal_preserved !== true || !text(value.strategy, 128) ||
      !p || !text(p.provider, 256) || !text(p.model, 256) ||
      !['provider_metadata', 'adapter', 'unknown'].includes(p.source) ||
      !['provider_metadata', 'configured', 'provider_and_configured', 'unknown'].includes(p.context_source) ||
      !['chat', 'native_tools', 'json_mode', 'reasoning', 'vision'].every(k => supports.has(p[k])) ||
      !['context_tokens', 'input_tokens', 'output_tokens'].every(k => limit(p[k])) || !limit(value.max_output_tokens) ||
      ![value.approach, value.warnings].every(list => Array.isArray(list) && list.length <= 12 && list.every(v => text(v, 1000))) ||
      (value.blocked_reason != null && !text(value.blocked_reason, 1000))) throw new Error('Invalid model preparation. Refresh before relying on it.')
  return value
}
export function supportLabel(value) { return value === 'supported' ? 'Supported' : value === 'unsupported' ? 'Not supported' : 'Not reported' }
export function strategyLabel(value) { return ({ native_tools: 'Native tool loop', react: 'Guarded step-by-step', plan_execute: 'Plan and execute', workflow: 'Authored workflow', router: 'Message routing' })[value] || value }
