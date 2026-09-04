package studio

import "testing"

func TestInferPython(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		want       bool
		wantTmpl   string
		reasonHint string
	}{
		{"rank stocks", "Rank the top stocks by momentum", true, "calculate_metrics", "ranking"},
		{"clean data", "Clean and dedupe the spreadsheet", true, "clean_csv", "cleaning"},
		{"chart", "Prepare a chart of monthly revenue", true, "chart_data", "chart"},
		{"validate", "Validate records have required field email", true, "validate_records", "validation"},
		{"transform", "Parse the JSON and reshape it", true, "transform_json", "reshaping"},
		{"metrics", "Compute the average order value", true, "calculate_metrics", "calculations"},
		{"no python", "Summarize the news and send it to Telegram", false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := InferPython(c.text)
			if got.NeedsPython != c.want {
				t.Fatalf("NeedsPython = %v, want %v", got.NeedsPython, c.want)
			}
			if !c.want {
				return
			}
			if got.Template != c.wantTmpl {
				t.Errorf("Template = %q, want %q", got.Template, c.wantTmpl)
			}
			if got.Reason == "" || got.Label == "" {
				t.Errorf("expected a reason and label, got reason=%q label=%q", got.Reason, got.Label)
			}
		})
	}
}
