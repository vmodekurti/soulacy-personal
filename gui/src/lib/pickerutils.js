// pickerutils — shared filtering for lookup pickers (FilePicker, ChipPicker).
// An option is {value, label?, description?, group?}.

/** Case-insensitive match across label, value, and description. */
export function filterPickerOptions(options, query) {
  const q = (query || '').trim().toLowerCase()
  if (!Array.isArray(options)) return []
  if (q === '') return options
  return options.filter(o =>
    String(o.label || o.value || '').toLowerCase().includes(q) ||
    String(o.value || '').toLowerCase().includes(q) ||
    String(o.description || '').toLowerCase().includes(q)
  )
}

/** Options minus already-selected values (multi-select pickers). */
export function excludeSelected(options, selected) {
  const sel = new Set(selected || [])
  return (options || []).filter(o => !sel.has(o.value))
}
