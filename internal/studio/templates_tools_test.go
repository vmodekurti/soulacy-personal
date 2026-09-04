package studio

import (
	"strings"
	"testing"
)

// engineHTTPBuiltins mirrors the network builtins the engine actually ships
// (internal/runtime/engine_tools_http.go). It is restated here rather than
// imported because internal/runtime imports internal/studio/consent, so
// depending on runtime from this package would invert the dependency.
//
// The starter templates once named "http_get" and "http_post", which have never
// existed: every user who picked one landed on a draft the contract check
// flagged as "not in the available tools list", and that would have failed at
// run time with "tool not found".
var engineHTTPBuiltins = map[string]bool{
	"fetch_url":     true,
	"http_request":  true,
	"download_file": true,
	"web_search":    true,
}

// TestTemplateToolsExist asserts every tool node in a starter template names a
// real engine builtin. The starters only reach for network tools today; one
// that reaches for a different builtin should extend the map above alongside
// the engine.
func TestTemplateToolsExist(t *testing.T) {
	for _, tpl := range Templates() {
		for _, n := range tpl.Workflow.Flow.Nodes {
			if n.Tool == "" {
				continue
			}
			if !engineHTTPBuiltins[n.Tool] {
				t.Errorf("template %q node %q uses tool %q, which is not an engine builtin", tpl.ID, n.ID, n.Tool)
			}
		}
	}
}

// TestTemplateHTTPRequestCarriesMethod guards the one argument http_request
// requires that fetch_url does not: without a method the call is rejected
// before it leaves the engine.
func TestTemplateHTTPRequestCarriesMethod(t *testing.T) {
	for _, tpl := range Templates() {
		for _, n := range tpl.Workflow.Flow.Nodes {
			if n.Tool != "http_request" {
				continue
			}
			if !strings.Contains(n.Input, `"method"`) {
				t.Errorf("template %q node %q calls http_request without a method: %s", tpl.ID, n.ID, n.Input)
			}
			if !strings.Contains(n.Input, `"url"`) {
				t.Errorf("template %q node %q calls http_request without a url: %s", tpl.ID, n.ID, n.Input)
			}
		}
	}
}
