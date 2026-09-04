package studio

import (
	"regexp"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// tmplActionRe captures the body of each {{ … }} action so a reference can be
// judged in context (e.g. whether it's wrapped in an object-coercing helper).
var tmplActionRe = regexp.MustCompile(`\{\{(.*?)\}\}`)

// tmplPathRe captures a FULL dotted flow-var path inside a template expression:
// `.notebook` → "notebook", `.notebook.id` → "notebook.id",
// `.notebook.notebook` → "notebook.notebook". Unlike flowdeps' tmplVarRe (which
// keeps only the leading identifier for dependency checks) this preserves the
// whole chain so we can reason about nested-object vs scalar access.
var tmplPathRe = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)`)

// objectCoercers are template helpers that legitimately consume a whole object
// or array (so a bare reference inside one is intentional, not a bug). Matches
// the helper set registered by the flow renderer (reasoning.FlowTemplateFuncs).
var objectCoercers = []string{"toJson", "json", "pluck"}

// checkTemplateReferences catches a class of bug the syntax + dependency checks
// miss: a template that interpolates a whole STRUCTURED value (an object/array
// produced by an earlier step) where a scalar is expected. Go's text/template
// renders a map as "map[id:… title:…]" — not JSON — so a step that writes
// {{ .notebook }} (or the wrong nested path {{ .notebook.notebook }}) feeds a
// malformed string like "map[id:real-uuid title:…]" to downstream tools
// (add_sources, studio_create, studio_status) instead of the bare id.
//
// It is heuristic but conservative: it only flags references to vars PRODUCED by
// a step (dangling refs are left to checkDataFlow) that are known/likely to be
// structured, and it offers the concrete fix (reference a scalar field, or use
// toJson for the whole object). Warnings only — never blocks a save.
func checkTemplateReferences(draft Draft, add func(sev, kind, node, msg, fix string)) {
	nodes := draft.Flow.Nodes
	if len(nodes) == 0 {
		return
	}

	// producer: output var name -> the node that produces it.
	producer := map[string]sdkr.FlowNode{}
	for _, n := range nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n
		}
	}

	// A produced var is definitely an object if it is accessed with a field
	// (`.X.field`) ANYWHERE in the flow — that's a schema-free proof of shape.
	accessedWithField := map[string]bool{}
	for _, n := range nodes {
		for _, path := range templatePaths(n.Input) {
			if segs := strings.Split(path, "."); len(segs) >= 2 {
				accessedWithField[segs[0]] = true
			}
		}
	}

	for _, n := range nodes {
		if !strings.Contains(n.Input, "{{") {
			continue
		}
		seen := map[string]bool{} // dedupe messages per node
		for _, action := range templateActions(n.Input) {
			coerced := mentionsAny(action, objectCoercers)
			for _, path := range templatePaths(action) {
				segs := strings.Split(path, ".")
				root := segs[0]
				prod, ok := producer[root]
				if !ok || prod.ID == n.ID {
					continue // dangling (checkDataFlow) or self-ref
				}

				// Case B: the path ENDS with a repeated segment like
				// `.notebook.notebook` — the author stopped at the nested object
				// instead of reaching a single field. (A path that continues to a
				// field, e.g. `.notebook.notebook.id`, is fine and must not be
				// flagged, or the one-click fix would keep appending ".id".)
				if endsWithRepeatedSegment(segs) {
					if key := "B:" + path; !seen[key] {
						seen[key] = true
						add("warn", "template", n.ID,
							"This step stops at the nested \""+root+"\" object where it needs a single value (like an ID) — passing a nested object, not a field. It would send text like \"map[id:… title:…]\" and the step would fail.",
							"Point to the exact field — change {{ ."+path+" }} to {{ ."+path+".id }} (or whichever field holds the value you want).")
					}
					continue
				}

				// Case A: a bare whole-object interpolation ({{ .notebook }})
				// of a structured value, with no coercing helper.
				structured := accessedWithField[root] || producerEmitsObject(prod)
				if len(segs) == 1 && structured && !coerced {
					if key := "A:" + root; !seen[key] {
						seen[key] = true
						add("warn", "template", n.ID,
							"This step sends the whole \""+root+"\" object where a single value is expected. The next step would receive \"map[…]\" instead of a usable value and fail.",
							"Use one field, e.g. {{ ."+root+".id }} — or {{ toJson ."+root+" }} to send the whole object as JSON on purpose.")
					}
				}
			}
		}
	}
}

// ApplyTemplateFixes deterministically corrects the clear template-reference
// bugs (whole-object interpolation and repeated nested paths) in a freshly
// generated draft, so generation self-heals regardless of how well the model
// followed the rules. Returns the number of node inputs changed. Safe to call
// repeatedly — once a reference reaches a scalar field it is no longer flagged,
// so it never compounds.
func ApplyTemplateFixes(draft *Draft) int {
	if draft == nil {
		return 0
	}
	changed := applyEntryCaptureFixes(draft)
	changed += applyPythonInputMappingFixes(draft)
	fixes := SuggestTemplateFixes(*draft)
	if len(fixes) == 0 {
		return changed
	}
	for i := range draft.Flow.Nodes {
		in := draft.Flow.Nodes[i].Input
		if !strings.Contains(in, "{{") {
			continue
		}
		orig := in
		for _, f := range fixes {
			if f.Find != "" && strings.Contains(in, f.Find) {
				in = strings.ReplaceAll(in, f.Find, f.Replace)
			}
		}
		if in != orig {
			draft.Flow.Nodes[i].Input = in
			changed++
		}
	}
	return changed
}

var pyInputsGetRe = regexp.MustCompile(`inputs\.get\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]`)

func applyPythonInputMappingFixes(draft *Draft) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}
	produced := map[string]string{}
	for _, n := range draft.Flow.Nodes {
		if out := strings.TrimSpace(n.Output); out != "" {
			produced[out] = strings.TrimSpace(n.ID)
		}
	}
	changed := 0
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if strings.TrimSpace(n.Kind) != sdkr.FlowNodePython || !strings.Contains(n.Code, "inputs.get") {
			continue
		}
		vars := pythonInputGets(n.Code, produced, strings.TrimSpace(n.ID))
		if len(vars) == 0 || inputAlreadyMapsVars(n.Input, vars) {
			continue
		}
		var b strings.Builder
		b.WriteString("{")
		for j, v := range vars {
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"`)
			b.WriteString(v)
			b.WriteString(`": {{ toJson .`)
			b.WriteString(v)
			b.WriteString(` }}`)
		}
		b.WriteString("}")
		n.Input = b.String()
		changed++
	}
	return changed
}

func pythonInputGets(code string, produced map[string]string, nodeID string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range pyInputsGetRe.FindAllStringSubmatch(code, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		if producerID, ok := produced[name]; !ok || producerID == nodeID {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func inputAlreadyMapsVars(input string, vars []string) bool {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	for _, v := range vars {
		if !strings.Contains(trimmed, `"`+v+`"`) {
			return false
		}
	}
	return true
}

func applyEntryCaptureFixes(draft *Draft) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}
	entry := strings.TrimSpace(draft.Flow.Entry)
	if entry == "" {
		entry = strings.TrimSpace(draft.Flow.Nodes[0].ID)
	}
	if entry == "" {
		return 0
	}
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if strings.TrimSpace(n.ID) != entry || strings.TrimSpace(n.Kind) != sdkr.FlowNodePython {
			continue
		}
		code := strings.TrimSpace(n.Code)
		if code == "" || strings.Contains(code, "trigger") || !strings.Contains(code, "inputs.get") {
			return 0
		}
		lower := strings.ToLower(n.ID + " " + n.Description + " " + n.Intent + " " + n.Output)
		if !strings.Contains(lower, "message") && !strings.Contains(lower, "input") && !strings.Contains(lower, "request") {
			return 0
		}
		n.Code = `def run(inputs):
    trigger = inputs.get('trigger') or {}
    trigger_text = ''
    if isinstance(trigger, dict):
        trigger_text = trigger.get('text') or trigger.get('message') or trigger.get('input') or ''
    elif trigger:
        trigger_text = str(trigger)
    return (inputs.get('message') or inputs.get('text') or inputs.get('input') or trigger_text or '').strip()`
		n.Input = `{"message":"{{ .trigger.text }}","text":"{{ .trigger.text }}","input":"{{ .trigger.text }}"}`
		ensurePort := func(ports *[]sdkr.FlowPort, name string) {
			for _, p := range *ports {
				if p.Name == name {
					return
				}
			}
			*ports = append(*ports, sdkr.FlowPort{Name: name, Type: "string"})
		}
		ensurePort(&n.Inputs, "message")
		ensurePort(&n.Inputs, "text")
		ensurePort(&n.Inputs, "input")
		return 1
	}
	return 0
}

// templateActions returns the inner text of each {{ … }} action in s.
func templateActions(s string) []string {
	if !strings.Contains(s, "{{") {
		return nil
	}
	var out []string
	for _, m := range tmplActionRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// templatePaths returns the distinct dotted flow-var paths referenced in s
// (e.g. "notebook", "notebook.id"). Returns nil for non-template strings.
func templatePaths(s string) []string {
	if !strings.Contains(s, "{{") && !strings.Contains(s, ".") {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range tmplPathRe.FindAllStringSubmatch(s, -1) {
		p := m[1]
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// producerEmitsObject reports whether a node's output is DEFINITELY likely a
// structured object/array (so interpolating it bare would render as "map[…]").
// Conservative to avoid false positives: only MCP tools are assumed to return
// structured JSON. Agent nodes return text, and a python node can return either
// a scalar (e.g. a date string from a get_date step) or an object — so we do NOT
// assume python is structured here; a python output is only flagged when it is
// independently proven to be an object by a field access elsewhere
// (accessedWithField in checkTemplateReferences).
func producerEmitsObject(n sdkr.FlowNode) bool {
	if strings.TrimSpace(n.Kind) == sdkr.FlowNodeAgent {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(n.Tool), "mcp__")
}

// TemplateFix is a machine-applicable suggestion for a flagged template
// reference: replace the exact Find string with Replace in the SOUL.yaml. The
// GUI offers this as a one-click "Fix". The replacement appends the most common
// scalar accessor (".id"); it's a best-effort default the user can adjust, since
// the real field name depends on the tool's actual output shape.
type TemplateFix struct {
	NodeID  string `json:"nodeId,omitempty"`
	Find    string `json:"find"`
	Replace string `json:"replace"`
	Reason  string `json:"reason,omitempty"`
}

// SuggestTemplateFixes mirrors checkTemplateReferences but returns concrete
// find/replace edits for each auto-fixable reference (whole-object or repeated
// nested path). Deduped by the exact template action so applying the set fixes
// every occurrence (e.g. the same {{ .notebook.notebook }} used by three steps).
func SuggestTemplateFixes(draft Draft) []TemplateFix {
	nodes := draft.Flow.Nodes
	if len(nodes) == 0 {
		return nil
	}
	producer := map[string]sdkr.FlowNode{}
	for _, n := range nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n
		}
	}
	accessedWithField := map[string]bool{}
	for _, n := range nodes {
		for _, path := range templatePaths(n.Input) {
			if segs := strings.Split(path, "."); len(segs) >= 2 {
				accessedWithField[segs[0]] = true
			}
		}
	}

	var fixes []TemplateFix
	seen := map[string]bool{}
	for _, n := range nodes {
		if !strings.Contains(n.Input, "{{") {
			continue
		}
		for _, full := range tmplActionRe.FindAllString(n.Input, -1) { // full "{{ … }}"
			if mentionsAny(full, objectCoercers) {
				continue
			}
			for _, path := range templatePaths(full) {
				segs := strings.Split(path, ".")
				root := segs[0]
				prod, ok := producer[root]
				if !ok || prod.ID == n.ID {
					continue
				}
				apply := false
				if endsWithRepeatedSegment(segs) {
					apply = true // .notebook.notebook -> .notebook.notebook.id
				} else if len(segs) == 1 && (accessedWithField[root] || producerEmitsObject(prod)) {
					apply = true // .notebook -> .notebook.id
				}
				if !apply {
					continue
				}
				replaceWith := strings.Replace(full, "."+path, "."+path+".id", 1)
				if replaceWith == full {
					continue
				}
				key := full
				if seen[key] {
					continue
				}
				seen[key] = true
				fixes = append(fixes, TemplateFix{
					NodeID:  n.ID,
					Find:    full,
					Replace: replaceWith,
					Reason:  "reference the scalar field instead of the whole object",
				})
			}
		}
	}
	return fixes
}

// endsWithRepeatedSegment reports whether a dotted path ENDS with a repeated
// segment (e.g. notebook.notebook) — the author stopped at the nested object
// rather than reaching a scalar field. A path that continues past the repeat
// (notebook.notebook.id) is intentionally NOT flagged: it already reaches a
// field, so it's a valid target — and not re-flagging it is what stops the
// one-click fix from compounding (.id.id.id).
func endsWithRepeatedSegment(segs []string) bool {
	n := len(segs)
	return n >= 2 && segs[n-1] != "" && segs[n-1] == segs[n-2]
}

// mentionsAny reports whether s contains any of the given substrings.
func mentionsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
