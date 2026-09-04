// pyinfer.go — deterministic inference of when a task warrants a Python step,
// and a plain-English reason why (Epic: Guided Studio Builder, slice items 3 &
// 4 "Python Block Inference"). Pure and unit-tested; the compiler/guided flow
// can call this to decide whether to add a Python node and to surface the
// rationale to the user (e.g. "Python is used here because ranking requires
// deterministic calculations").
package studio

import "strings"

// PythonInference is the result of inspecting a task description.
type PythonInference struct {
	NeedsPython bool   // whether a deterministic Python step is warranted
	Reason      string // plain-English explanation shown to the user
	Template    string // suggested template key (see gui pythonTemplates.js)
	Label       string // suggested domain-specific step label
}

// pyTrigger pairs a set of intent keywords with the template/label/reason to use
// when they match. Ordered by specificity — the first match wins.
type pyTrigger struct {
	keywords []string
	template string
	label    string
	reason   string
}

var pyTriggers = []pyTrigger{
	{
		keywords: []string{"clean", "dedup", "deduplicate", "normalize", "tidy", "sanitize", "sanitise"},
		template: "clean_csv", label: "Clean Spreadsheet",
		reason: "Python is used here because cleaning tabular data needs deterministic, repeatable rules.",
	},
	{
		keywords: []string{"rank", "ranking", "sort", "score", "prioritize", "prioritise"},
		template: "calculate_metrics", label: "Rank Items",
		reason: "Python is used here because ranking requires deterministic, reproducible calculations.",
	},
	{
		keywords: []string{"calculate", "compute", "sum", "average", "metric", "ratio", "aggregate", "total"},
		template: "calculate_metrics", label: "Calculate Metrics",
		reason: "Python is used here because numeric calculations must be exact and reproducible.",
	},
	{
		keywords: []string{"chart", "graph", "plot", "series", "visuali"},
		template: "chart_data", label: "Prepare Chart Data",
		reason: "Python is used here because preparing chart series requires deterministic aggregation.",
	},
	{
		keywords: []string{"validate", "verify records", "check records", "required field"},
		template: "validate_records", label: "Validate Records",
		reason: "Python is used here because validation rules must be applied deterministically to every record.",
	},
	{
		keywords: []string{"transform", "reshape", "convert", "parse", "restructure", "map fields", "extract fields"},
		template: "transform_json", label: "Transform Data",
		reason: "Python is used here because reshaping or parsing structured data is a deterministic transformation.",
	},
}

// InferPython inspects a task/step description and reports whether a Python step
// is warranted, with a suggested template and a plain-English reason. Returns a
// zero-value (NeedsPython=false) when nothing deterministic is implied.
func InferPython(text string) PythonInference {
	t := strings.ToLower(text)
	tokens := pyTokenize(t)
	for _, tr := range pyTriggers {
		for _, kw := range tr.keywords {
			if matchKeyword(t, tokens, kw) {
				return PythonInference{
					NeedsPython: true,
					Reason:      tr.reason,
					Template:    tr.template,
					Label:       tr.label,
				}
			}
		}
	}
	return PythonInference{}
}

// pyTokenize splits lowercased text into word tokens (letters only).
func pyTokenize(t string) []string {
	return strings.FieldsFunc(t, func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	})
}

// matchKeyword matches a phrase keyword as a substring, and a single-word
// keyword as a whole word (with common plural/gerund suffixes for stems of 4+
// letters). This prevents false positives like "sum" inside "summarize".
func matchKeyword(text string, tokens []string, kw string) bool {
	if strings.Contains(kw, " ") {
		return strings.Contains(text, kw)
	}
	for _, tok := range tokens {
		if tok == kw {
			return true
		}
		if len(kw) >= 4 && (tok == kw+"s" || tok == kw+"d" || tok == kw+"ed" || tok == kw+"ing") {
			return true
		}
	}
	return false
}
