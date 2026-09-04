// configfields.js — describes the editable fields of a Studio block as
// structured "configuration card" fields, instead of raw YAML/JSON (Guided
// Studio Builder, Story 9). Also handles writing an edited value back onto the
// node (fields live either directly on the node or under node.params). Pure &
// unit-tested.

// slugify turns a label into a safe output-variable suggestion.
function slugify(s) {
  return String(s || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 40)
}

// configFields returns an array of field descriptors for a node:
//   { key, label, value, required, missing, suggestion, where, type, options }
// where: 'field' (written on the node) | 'param' (written under node.params).
export function configFields(node) {
  if (!node) return []
  const kind = node.kind || ''
  const p = node.params || {}
  const outSuggestion = slugify(node.description || node.tool || node.agent || kind) || 'result'
  const outputField = {
    key: 'output', label: 'Output name', value: node.output || '', required: false,
    missing: false, suggestion: outSuggestion, where: 'field', type: 'text',
  }

  switch (kind) {
    case 'tool':
      return [
        { key: 'description', label: 'Step name', value: node.description || '', required: false, missing: false, where: 'field', type: 'text' },
        { key: 'tool', label: 'Tool', value: node.tool || '', required: true, missing: !node.tool, where: 'field', type: 'text' },
        { key: 'input', label: 'Input (template)', value: node.input || '', required: false, missing: false, suggestion: '{{ .input }}', where: 'field', type: 'text' },
        outputField,
      ]
    case 'agent':
      return [
        { key: 'description', label: 'Step name', value: node.description || '', required: false, missing: false, where: 'field', type: 'text' },
        { key: 'agent', label: 'Agent', value: node.agent || '', required: true, missing: !node.agent, where: 'field', type: 'text' },
        { key: 'input', label: 'Message (template)', value: node.input || '', required: false, missing: false, where: 'field', type: 'text' },
        outputField,
      ]
    case 'python':
      return [
        { key: 'description', label: 'Step name', value: node.description || '', required: false, missing: false, where: 'field', type: 'text' },
        { key: 'code', label: 'Python code', value: node.code || '', required: true, missing: !(node.code || '').trim(), where: 'field', type: 'code' },
        { ...outputField, suggestion: outSuggestion },
      ]
    case 'llm':
      return [
        { key: 'description', label: 'Step name', value: node.description || '', required: false, missing: false, where: 'field', type: 'text' },
        { key: 'system', label: 'Instruction', value: p.system || '', required: true, missing: !(p.system || '').trim(), where: 'param', type: 'text' },
        { key: 'input', label: 'Input (template)', value: node.input || '', required: false, missing: false, suggestion: '{{ .trigger.text }}', where: 'field', type: 'text' },
        { key: 'response_format', label: 'Response format', value: p.response_format || 'json', required: false, missing: false, where: 'param', type: 'select', options: ['json', 'text'] },
        outputField,
      ]
    case 'exit':
      return [
        { key: 'route', label: 'Deliver via', value: p.route || '', required: true, missing: !p.route, where: 'param', type: 'select', options: ['console', 'channel', 'http'] },
      ]
    case 'trigger':
      return [
        { key: 'kind', label: 'Starts on', value: p.kind || '', required: true, missing: !p.kind, where: 'param', type: 'select', options: ['cron', 'http', 'channel', 'manual'] },
      ]
    default:
      return [
        { key: 'description', label: 'Step name', value: node.description || '', required: false, missing: false, where: 'field', type: 'text' },
      ]
  }
}

// applyField returns a NEW node with `field` set to `value` (writing to the node
// or its params depending on field.where). Never mutates the input.
export function applyField(node, field, value) {
  if (!node || !field) return node
  if (field.where === 'param') {
    return { ...node, params: { ...(node.params || {}), [field.key]: value } }
  }
  return { ...node, [field.key]: value }
}

// missingFields returns the required fields that are still empty (Story 9
// "missing required fields highlighted").
export function missingFields(node) {
  return configFields(node).filter((f) => f.required && f.missing)
}
