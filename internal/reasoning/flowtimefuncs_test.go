package reasoning

import (
	"strings"
	"testing"
	"text/template"
)

// The time helpers must be registered so a generated template using {{ now }} —
// the exact "function \"now\" not defined" blocker seen on NotebookLM drafts —
// parses and renders instead of failing.
func TestFlowTemplateFuncs_TimeHelpers(t *testing.T) {
	cases := map[string]string{
		"now":     `{{ now }}`,
		"today":   `{{ today }}`,
		"nowUnix": `{{ nowUnix }}`,
		"dateFmt": `{{ dateFmt "2006-01-02" }}`,
	}
	for name, tmplStr := range cases {
		tmpl, err := template.New("").Funcs(FlowTemplateFuncs()).Parse(tmplStr)
		if err != nil {
			t.Errorf("%s: parse failed (function not registered?): %v", name, err)
			continue
		}
		var sb strings.Builder
		if err := tmpl.Execute(&sb, map[string]any{}); err != nil {
			t.Errorf("%s: execute failed: %v", name, err)
			continue
		}
		if strings.TrimSpace(sb.String()) == "" {
			t.Errorf("%s: rendered empty", name)
		}
	}
}

func TestTmplToday_Format(t *testing.T) {
	got := tmplToday()
	if len(got) != 10 || got[4] != '-' || got[7] != '-' {
		t.Errorf("today should be YYYY-MM-DD, got %q", got)
	}
}

// Every common spelling must be registered so a generated template never fails
// with "function X not defined" over a naming choice (the live dateFormat bug).
func TestFlowTemplateFuncs_DateFormatAliases(t *testing.T) {
	for _, name := range []string{"dateFmt", "dateFormat", "formatDate", "date"} {
		if _, ok := FlowTemplateFuncs()[name]; !ok {
			t.Errorf("date function alias %q must be registered", name)
		}
	}
}

// dateFormat tolerates Go-reference, token, and strftime layouts — all yield the
// same YYYY-MM-DD shape — and an optional time argument.
func TestTmplDateFormat_LayoutStyles(t *testing.T) {
	for _, layout := range []string{"2006-01-02", "YYYY-MM-DD", "%Y-%m-%d"} {
		got := tmplDateFormat(layout)
		if len(got) != 10 || got[4] != '-' || got[7] != '-' {
			t.Errorf("layout %q should render YYYY-MM-DD, got %q", layout, got)
		}
	}
	// With an explicit RFC3339 time argument it formats THAT time, not now.
	if got := tmplDateFormat("YYYY-MM-DD", "2020-01-02T03:04:05Z"); got != "2020-01-02" {
		t.Errorf("expected the supplied date, got %q", got)
	}
}
