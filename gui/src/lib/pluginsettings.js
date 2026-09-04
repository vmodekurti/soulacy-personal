// Plugin settings editing helpers (Story 18).
//
// plugins_config is schemaless, so the editor renders each plugin's section
// as key/value rows: scalars edit as plain text, nested objects edit as
// inline JSON. Conversion back is type-aware and conservative — unchanged
// rows reuse the ORIGINAL value (no string→number coercion surprises), and
// redacted "***" placeholders pass through untouched (the server skips them
// so real secrets on disk are never overwritten).

// displayValue renders one settings value for a row input.
export function displayValue(v) {
  if (v === null || v === undefined) return '';
  if (typeof v === 'string') return v;
  return JSON.stringify(v);
}

// rowsFromSettings converts one plugin's settings map into editor rows.
export function rowsFromSettings(settings) {
  return Object.entries(settings || {}).map(([key, value]) => ({
    key,
    value: displayValue(value),
  }));
}

// parseValue converts an edited row value back to a typed setting:
// valid JSON (numbers, booleans, objects, arrays, quoted strings) parses;
// anything else stays a plain string.
export function parseValue(s) {
  const t = (s ?? '').trim();
  if (t === '') return '';
  try {
    return JSON.parse(t);
  } catch {
    return s;
  }
}

// settingsPatchFromRows builds the PATCH body for one plugin section.
// - unchanged rows reuse the original value (type-safe round-trip);
// - edited rows parse type-aware;
// - keys present originally but missing from rows become null (delete);
// - empty keys are ignored.
export function settingsPatchFromRows(original, rows) {
  const patch = {};
  const seen = new Set();
  for (const row of rows) {
    const key = (row.key || '').trim();
    if (!key) continue;
    seen.add(key);
    const orig = original?.[key];
    if (orig !== undefined && displayValue(orig) === row.value) {
      patch[key] = orig; // unchanged — keep the exact original type
    } else {
      patch[key] = parseValue(row.value);
    }
  }
  for (const key of Object.keys(original || {})) {
    if (!seen.has(key)) patch[key] = null; // removed row → delete on disk
  }
  return patch;
}
