// pythonTemplates.js — named, ready-to-edit Python step templates for the Guided
// Studio Builder (Epic: Guided Studio Builder, slice item 5 "Manual Python
// Control"). Each template is a domain-specific starting point ("Clean CSV",
// "Transform JSON", …) rather than a raw empty python_eval node.
//
// The runtime contract every template follows (Phase 1): upstream node outputs
// arrive as `inputs` (a dict); whatever `run(inputs)` returns becomes this
// node's output. Kept framework-free so it's unit-testable without Svelte.

// The blank starting point used when no template is chosen (mirrors the inline
// PYTHON_STARTER in Studio.svelte so both paths stay in sync).
export const BLANK_PYTHON = `# Custom Python step.
# Upstream node outputs arrive as 'inputs' (a dict).
# Return (or print) a value — it becomes this node's output.
def run(inputs):
    # TODO: your logic here
    return inputs
`

// Each template: a stable key, a plain-English label + description, a "why"
// sentence (surfaced when Studio explains why a Python step fits), and the code.
export const PYTHON_TEMPLATES = [
  {
    key: 'clean_csv',
    label: 'Clean CSV',
    description: 'Normalize rows, drop blanks, and tidy a spreadsheet.',
    why: 'Python is used here because cleaning tabular data needs deterministic, repeatable rules.',
    code: `# Clean CSV — normalize rows and drop empty/duplicate records.
# inputs: { "rows": [ {col: value, ...}, ... ] }
def run(inputs):
    rows = inputs.get("rows") or []
    cleaned = []
    seen = set()
    for row in rows:
        # skip fully-empty rows
        if not any(str(v).strip() for v in row.values()):
            continue
        # trim whitespace on every string cell
        norm = {k: (v.strip() if isinstance(v, str) else v) for k, v in row.items()}
        key = tuple(sorted(norm.items()))
        if key in seen:            # drop exact duplicates
            continue
        seen.add(key)
        cleaned.append(norm)
    return {"rows": cleaned, "removed": len(rows) - len(cleaned)}
`,
  },
  {
    key: 'transform_json',
    label: 'Transform JSON',
    description: 'Reshape or filter a JSON payload into the shape you need.',
    why: 'Python is used here because reshaping structured data is a deterministic transformation.',
    code: `# Transform JSON — reshape an incoming payload.
# inputs: { "data": <any JSON value> }
def run(inputs):
    data = inputs.get("data")
    items = data if isinstance(data, list) else [data]
    result = []
    for item in items:
        if not isinstance(item, dict):
            continue
        # TODO: pick / rename / compute the fields you need
        result.append({
            "id": item.get("id"),
            "name": item.get("name"),
        })
    return {"items": result, "count": len(result)}
`,
  },
  {
    key: 'calculate_metrics',
    label: 'Calculate Metrics',
    description: 'Compute totals, averages, ratios, or rankings from data.',
    why: 'Python is used here because numeric calculations must be exact and reproducible.',
    code: `# Calculate Metrics — summarize a list of numbers or records.
# inputs: { "values": [number, ...] }
def run(inputs):
    values = [float(v) for v in (inputs.get("values") or []) if v is not None]
    if not values:
        return {"count": 0, "sum": 0, "avg": None, "min": None, "max": None}
    total = sum(values)
    return {
        "count": len(values),
        "sum": total,
        "avg": total / len(values),
        "min": min(values),
        "max": max(values),
    }
`,
  },
  {
    key: 'validate_records',
    label: 'Validate Records',
    description: 'Check records against rules and separate valid from invalid.',
    why: 'Python is used here because validation rules must be applied deterministically to every record.',
    code: `# Validate Records — split records into valid / invalid with reasons.
# inputs: { "records": [ {..}, .. ], "required": ["field1", "field2"] }
def run(inputs):
    records = inputs.get("records") or []
    required = inputs.get("required") or []
    valid, invalid = [], []
    for rec in records:
        missing = [f for f in required if not str(rec.get(f, "")).strip()]
        if missing:
            invalid.append({"record": rec, "missing": missing})
        else:
            valid.append(rec)
    return {"valid": valid, "invalid": invalid, "ok": len(invalid) == 0}
`,
  },
  {
    key: 'chart_data',
    label: 'Generate Chart Data',
    description: 'Turn raw data into series/labels ready for a chart.',
    why: 'Python is used here because preparing chart series requires deterministic aggregation.',
    code: `# Generate Chart Data — build labels + series for a chart.
# inputs: { "rows": [ {"label": str, "value": number}, ... ] }
def run(inputs):
    rows = inputs.get("rows") or []
    labels, series = [], []
    for row in rows:
        labels.append(row.get("label"))
        series.append(row.get("value"))
    return {"labels": labels, "series": series}
`,
  },
]

// templateByKey returns a template object for a key, or null.
export function templateByKey(key) {
  return PYTHON_TEMPLATES.find((t) => t.key === key) || null
}

// pythonCodeFor returns the code to seed a new python node, given an optional
// template key. Falls back to the blank starter when the key is unknown/absent.
export function pythonCodeFor(key) {
  return templateByKey(key)?.code || BLANK_PYTHON
}

// pythonLabelFor returns a plain-English node description for a template key
// (used as the node's `description`), or a generic label when there's no match.
export function pythonLabelFor(key) {
  return templateByKey(key)?.label || 'Custom Python'
}
