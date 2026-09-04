// introspect.go — deep tool introspection (Architect pillar #2).
//
// Preflight already checks that an MCP tool's REQUIRED arguments are present.
// This layer goes one step further and validates a tool call against the tool's
// FULL signature, derived from the catalog's compact param hint
// ("title*:string, summary:string"): it flags arguments the tool does not accept
// (the single most common cause of a silent MCP failure — the model invents
// "name" when the tool wants "title") and gross type mismatches on literal
// values. Templated values ({{ .x }}) are treated as wired and not type-checked.
//
// Everything here is pure and deterministic so the build loop can run it between
// generation and execution, and so the results can be fed straight into the LLM
// repair prompt as concrete problems.
package studio

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ToolParam is one argument of a tool, parsed from a catalog param hint.
type ToolParam struct {
	Name     string
	Type     string // schema type: string|number|integer|boolean|object|array (may be "")
	Required bool
}

// parseToolParams parses a compact param hint ("title*:string, summary:string,
// tags:array") into structured params. Required args are marked with a trailing
// "*" on the name. An empty hint yields nil.
func parseToolParams(hint string) []ToolParam {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return nil
	}
	var out []ToolParam
	for _, part := range strings.Split(hint, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		name, typ := p, ""
		if i := strings.IndexByte(p, ':'); i >= 0 {
			name = strings.TrimSpace(p[:i])
			typ = strings.TrimSpace(p[i+1:])
		}
		req := strings.HasSuffix(name, "*")
		name = strings.TrimSpace(strings.TrimSuffix(name, "*"))
		if name == "" {
			continue
		}
		out = append(out, ToolParam{Name: name, Type: typ, Required: req})
	}
	return out
}

// toolSignatures indexes every catalog MCP tool by its full callable name to its
// parsed parameter list. Tools with no param hint map to a nil slice (signature
// known but argument-less / unspecified).
func toolSignatures(cat Catalog) map[string][]ToolParam {
	sigs := builtinToolParams()
	for _, srv := range cat.MCP {
		for _, t := range srv.Tools {
			full := strings.TrimSpace(t.Name)
			if full == "" {
				continue
			}
			sigs[full] = parseToolParams(t.Params)
		}
	}
	return sigs
}

// tmplExprRe matches a Go template expression so we can blank it out before
// JSON-parsing a node input that mixes literal JSON with {{ .var }} holes.
var tmplExprRe = regexp.MustCompile(`\{\{[^}]*\}\}`)
var quotedTmplExprRe = regexp.MustCompile(`"\s*\{\{[^}]*\}\}\s*"`)

// parseInputObject best-effort parses a tool node's Input (a JSON-object
// template) into its top-level keys and their literal value types. Template
// expressions are replaced with a sentinel string first so the result is valid
// JSON. It returns the decoded map, the set of keys whose value was (wholly or
// partly) a template expression (and so must not be type-checked), and whether
// parsing succeeded at all.
func parseInputObject(input string) (vals map[string]any, templated map[string]bool, ok bool) {
	s := strings.TrimSpace(input)
	if s == "" || !strings.HasPrefix(s, "{") {
		return nil, nil, false
	}
	// Record which raw key/value pairs contain a template, then blank the
	// expressions to a sentinel so json.Unmarshal succeeds. Handle both
	// quoted templates ("{{ .x }}") and JSON-safe unquoted templates
	// ({{ toJson .x }}).
	templated = map[string]bool{}
	blanked := quotedTmplExprRe.ReplaceAllString(s, `"__TMPL__"`)
	blanked = tmplExprRe.ReplaceAllString(blanked, `"__TMPL__"`)
	var m map[string]any
	if err := json.Unmarshal([]byte(blanked), &m); err != nil {
		return nil, nil, false
	}
	for k, v := range m {
		if sv, isStr := v.(string); isStr && strings.Contains(sv, "__TMPL__") {
			templated[k] = true
		}
	}
	return m, templated, true
}

// jsonTypeOf returns the schema type name for a decoded JSON value.
func jsonTypeOf(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		// JSON numbers decode to float64; report "integer" when whole so an
		// integer-typed param matches, but treat number/integer as compatible.
		if t == float64(int64(t)) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return ""
	}
}

// typeCompatible reports whether a literal value's JSON type satisfies the
// param's declared schema type. number/integer are interchangeable; an empty
// declared type matches anything. Conservative: anything we can't judge passes.
func typeCompatible(declared, got string) bool {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared == "" || got == "" || got == "null" {
		return true
	}
	if declared == got {
		return true
	}
	num := map[string]bool{"number": true, "integer": true}
	if num[declared] && num[got] {
		return true
	}
	return false
}

// checkToolArgs validates each tool node's arguments against the real tool
// signature from the catalog, emitting issues for:
//   - unknown arguments the tool does not declare (warn — the hint may be
//     partial, but this is the #1 cause of silent MCP failures);
//   - literal values whose type contradicts the declared param type (warn).
//
// It only runs for tools with a KNOWN signature (present in the catalog) and a
// parseable JSON-object input, so it never fires on builtins or free-form inputs.
// Required-argument presence is already covered by Preflight; this complements it.
func checkToolArgs(draft Draft, cat Catalog, add func(sev, kind, node, msg, fix string)) {
	sigs := toolSignatures(cat)
	if len(sigs) == 0 {
		return
	}
	for _, n := range draft.Flow.Nodes {
		tool := strings.TrimSpace(n.Tool)
		if tool == "" {
			continue
		}
		params, known := sigs[tool]
		if !known || len(params) == 0 {
			continue // unknown tool or no declared params → nothing to validate
		}
		vals, templated, parsed := parseInputObject(n.Input)
		if !parsed {
			continue
		}
		accepted := map[string]ToolParam{}
		for _, p := range params {
			accepted[p.Name] = p
		}
		// Stable order for deterministic output.
		for _, key := range sortedAnyKeys(vals) {
			p, isAccepted := accepted[key]
			if !isAccepted {
				add("warn", "dependency", n.ID,
					"Argument \""+key+"\" is not accepted by tool \""+tool+"\" (expects: "+paramNameList(params)+").",
					"Use one of the tool's real argument names, or remove this argument.")
				continue
			}
			if templated[key] {
				continue // value comes from an upstream output at run time
			}
			got := jsonTypeOf(vals[key])
			if !typeCompatible(p.Type, got) {
				add("warn", "dependency", n.ID,
					"Argument \""+key+"\" for \""+tool+"\" should be "+p.Type+" but a "+got+" was given.",
					"Provide a "+p.Type+" value (or wire it from an upstream step that produces one).")
			}
		}
	}
}

// paramNameList renders a tool's accepted argument names for an error message,
// e.g. `title* (required), summary, tags`.
func paramNameList(params []ToolParam) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		s := p.Name
		if p.Required {
			s += " (required)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// sortedAnyKeys returns a map's keys in deterministic order.
func sortedAnyKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// simple insertion sort to avoid an extra import; maps are small
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
