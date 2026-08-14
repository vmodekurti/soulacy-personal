// Keep the SOUL.yaml editor compact when generated or on-disk YAML ends with
// several empty records. A single terminal newline is conventional and keeps
// POSIX-friendly saves; additional blank lines carry no document structure and
// make the editor look taller than its content.
export function normalizeYamlEditorText(value) {
  const text = String(value || '').replace(/\r\n/g, '\n')
  if (!text) return ''
  return `${text.replace(/[\t ]+$/g, '').replace(/(?:\n[\t ]*)+$/, '')}\n`
}
