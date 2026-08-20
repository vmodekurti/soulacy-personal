// labels_guard_test.go — every metric label is bounded, and somebody said why.
//
// THE EXPOSITION IS ONE GLOBAL BLOB. That is the fact the whole file turns on.
// A Prometheus scrape cannot be scoped to the caller's workspace: the handler
// serialises the entire registry, so a label whose values are chosen by
// tenants is readable by every principal permitted to scrape, whatever the
// route's authorization says. The role gate on /api/v1/metrics restricts WHO
// may scrape; it cannot restrict WHAT a scrape contains.
//
// This already went wrong once, and the shape is worth keeping. The run
// latency histograms were written with a paragraph explaining why they carry
// no workspace label. The agent-run counters beside them carried an `agent`
// label — same registry, same blob, same problem, worse, because agent IDs are
// prose somebody typed: `acme-invoice-reconciliation` names a customer.
//
// So the rule is not "no workspace label". It is that every label has to be
// bounded by something other than customer count, and the bound has to be
// stated. A guard that only banned a list of bad names would pass the day
// somebody adds `principal` or `tenant_slug`.
package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// boundedLabels is every label this package may attach to a metric, with what
// bounds its cardinality. "The operator configures them" is a bound. "Users
// create them" is not.
var boundedLabels = map[string]string{
	"method":  "HTTP verbs; fewer than ten",
	"route":   "Fiber route PATTERNS, not substituted paths; bounded by the router",
	"status":  "HTTP status codes",
	"outcome": "a small closed set per metric (success|error|denied|…)",
	"result":  "a small closed set per metric",

	"provider": "LLM providers the operator configured",
	"model":    "models the operator configured; bounded by the provider list",
	"channel":  "channel adapters the operator configured, plus \"unknown\"",

	// The one genuinely awkward entry, kept with its awkwardness written down.
	// Tool names include mcp__<server>__<tool>, so the set grows with the
	// servers an operator installs rather than with customers — bounded by
	// configuration, not by tenancy — and no part of the name is chosen by an
	// end user. It is the loosest bound here and the one to revisit first if
	// per-workspace MCP installs become common.
	"tool": "builtins plus configured MCP/plugin tool names; bounded by installed extensions, not by tenants",
}

func TestNoMetricLabelIsBoundedByCustomerCount(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, label := range declaredLabels(file) {
			seen[label] = true
			if _, bounded := boundedLabels[label]; bounded {
				continue
			}
			t.Errorf("metric label %q in %s is not in boundedLabels — add it with what bounds its "+
				"cardinality, or remove it. The Prometheus exposition is one global document that "+
				"cannot be scoped per caller, so a label whose values tenants choose is readable by "+
				"everyone allowed to scrape", label, name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no metric labels found; the guard matches nothing and would pass for any code")
	}
	// The other direction: an entry left behind after its label is removed is
	// a standing permission for somebody to reintroduce it, with a reason
	// already written that nobody has to re-examine.
	var stale []string
	for label := range boundedLabels {
		if !seen[label] {
			stale = append(stale, label)
		}
	}
	sort.Strings(stale)
	for _, label := range stale {
		t.Errorf("boundedLabels still lists %q but no metric declares it — remove the entry rather "+
			"than leaving a pre-approved slot", label)
	}
}

// declaredLabels collects the string literals in every []string{...} passed to
// a prometheus.New*Vec constructor. Textual over type-checked deliberately:
// the question is which literals appear in a declaration in this one package,
// and loading type information would be more machinery than that needs.
func declaredLabels(file *ast.File) []string {
	var labels []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "prometheus" || !strings.HasSuffix(sel.Sel.Name, "Vec") {
			return true
		}
		for _, arg := range call.Args {
			composite, ok := arg.(*ast.CompositeLit)
			if !ok {
				continue
			}
			array, ok := composite.Type.(*ast.ArrayType)
			if !ok {
				continue
			}
			if ident, ok := array.Elt.(*ast.Ident); !ok || ident.Name != "string" {
				continue
			}
			for _, element := range composite.Elts {
				lit, ok := element.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if unquoted, err := strconv.Unquote(lit.Value); err == nil {
					labels = append(labels, unquoted)
				}
			}
		}
		return true
	})
	return labels
}
