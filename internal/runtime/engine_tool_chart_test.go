package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestAgentHasChartTool(t *testing.T) {
	list := func(names ...string) *[]string { return &names }
	cases := []struct {
		name string
		def  *agent.Definition
		want bool
	}{
		{"nil builtins (default) → offered", &agent.Definition{}, true},
		{"wildcard *", &agent.Definition{Builtins: list("*")}, true},
		{"wildcard all", &agent.Definition{Builtins: list("all")}, true},
		{"explicit allow", &agent.Definition{Builtins: list("web_search", "generate_chart")}, true},
		{"opted out (empty)", &agent.Definition{Builtins: list()}, false},
		{"allowlist without it", &agent.Definition{Builtins: list("web_search")}, false},
	}
	for _, c := range cases {
		if got := agentHasChartTool(c.def); got != c.want {
			t.Errorf("%s: agentHasChartTool = %v, want %v", c.name, got, c.want)
		}
	}
}

// decode the JSON inside a ```chart fence (or a bare spec string) for assertions.
func decodeSpec(t *testing.T, spec string) map[string]any {
	t.Helper()
	spec = strings.TrimSpace(spec)
	spec = strings.TrimPrefix(spec, "```chart")
	spec = strings.TrimSuffix(spec, "```")
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(spec)), &m); err != nil {
		t.Fatalf("spec is not valid JSON: %v\n%s", err, spec)
	}
	return m
}

func TestBuildChartSpec_BarWithLabelsAndDatasets(t *testing.T) {
	spec, summary, err := buildChartSpec(map[string]any{
		"chart_type": "bar",
		"title":      "Quarterly Revenue",
		"labels":     []any{"Q1", "Q2", "Q3", "Q4"},
		"datasets": []any{
			map[string]any{"label": "Revenue", "data": []any{120.0, 150.0, 140.0, 190.0}},
		},
	})
	if err != nil {
		t.Fatalf("buildChartSpec: %v", err)
	}
	m := decodeSpec(t, spec)
	// ECharts option: xAxis.data holds labels, series[].type/data hold the bars.
	if got := m["xAxis"].(map[string]any)["data"].([]any); len(got) != 4 {
		t.Errorf("xAxis.data len = %d, want 4", len(got))
	}
	if m["yAxis"].(map[string]any)["type"] != "value" {
		t.Errorf("yAxis.type = %v, want value", m["yAxis"])
	}
	series := m["series"].([]any)
	if len(series) != 1 {
		t.Fatalf("series len = %d, want 1", len(series))
	}
	s0 := series[0].(map[string]any)
	if s0["type"] != "bar" {
		t.Errorf("series type = %v, want bar", s0["type"])
	}
	if s0["name"] != "Revenue" {
		t.Errorf("series name = %v, want Revenue", s0["name"])
	}
	if len(s0["data"].([]any)) != 4 {
		t.Errorf("series data len = %d, want 4", len(s0["data"].([]any)))
	}
	if m["title"].(map[string]any)["text"] != "Quarterly Revenue" {
		t.Errorf("title not set correctly: %v", m["title"])
	}
	if !strings.Contains(summary, "bar") || !strings.Contains(summary, "1 series") {
		t.Errorf("summary unexpected: %q", summary)
	}
}

func TestBuildChartSpec_CoercesStringsAndInts(t *testing.T) {
	// A small model may emit numbers as strings or ints — we should coerce.
	spec, _, err := buildChartSpec(map[string]any{
		"chart_type": "line",
		"datasets": []any{
			map[string]any{"data": []any{"10", 20, 30.5}},
		},
	})
	if err != nil {
		t.Fatalf("buildChartSpec: %v", err)
	}
	m := decodeSpec(t, spec)
	s0 := m["series"].([]any)[0].(map[string]any)
	if s0["type"] != "line" {
		t.Errorf("series type = %v, want line", s0["type"])
	}
	nums := s0["data"].([]any)
	if len(nums) != 3 || nums[0].(float64) != 10 || nums[2].(float64) != 30.5 {
		t.Errorf("coercion wrong: %v", nums)
	}
}

func TestBuildChartSpec_FallbackBareDataArray(t *testing.T) {
	spec, _, err := buildChartSpec(map[string]any{
		"chart_type": "pie",
		"labels":     []any{"A", "B", "C"},
		"data":       []any{1.0, 2.0, 3.0},
	})
	if err != nil {
		t.Fatalf("expected fallback to single series, got error: %v", err)
	}
	m := decodeSpec(t, spec)
	series := m["series"].([]any)
	if len(series) != 1 || series[0].(map[string]any)["type"] != "pie" {
		t.Fatalf("expected one pie series, got %v", series)
	}
	// Pie data is paired {name,value}; 3 values → 3 slices.
	if len(series[0].(map[string]any)["data"].([]any)) != 3 {
		t.Error("expected 3 pie slices from the bare data array")
	}
}

// Some models cram the whole payload into a stringified `data` field instead of
// passing labels/datasets separately — we must unwrap and still build a chart.
func TestBuildChartSpec_UnwrapsStringifiedData(t *testing.T) {
	spec, _, err := buildChartSpec(map[string]any{
		"chart_type": "line",
		"data":       `{"labels":["Jul 2021","Aug 2021"],"datasets":[{"label":"AAPL","data":[142.18,148.0]}]}`,
	})
	if err != nil {
		t.Fatalf("expected stringified data wrapper to be accepted, got: %v", err)
	}
	m := decodeSpec(t, spec)
	if got := m["xAxis"].(map[string]any)["data"].([]any); len(got) != 2 {
		t.Errorf("labels not lifted from wrapper: %v", got)
	}
	s0 := m["series"].([]any)[0].(map[string]any)
	if s0["name"] != "AAPL" || len(s0["data"].([]any)) != 2 {
		t.Errorf("series not lifted from wrapper: %v", s0)
	}
}

// datasets passed as a JSON string should also decode.
func TestBuildChartSpec_UnwrapsStringifiedDatasets(t *testing.T) {
	spec, _, err := buildChartSpec(map[string]any{
		"chart_type": "bar",
		"labels":     []any{"A", "B"},
		"datasets":   `[{"label":"x","data":[1,2]}]`,
	})
	if err != nil {
		t.Fatalf("expected stringified datasets to be accepted, got: %v", err)
	}
	m := decodeSpec(t, spec)
	if len(m["series"].([]any)) != 1 {
		t.Error("expected one series from stringified datasets")
	}
}

// qwen3-coder-next invents a `chart_code` arg and stuffs Chart.js-shaped JSON in
// it: {"chart_code":"{\"type\":\"line\",\"data\":{\"labels\":[...],\"datasets\":[...]}}"}.
func TestBuildChartSpec_ChartCodeAliasChartJSShape(t *testing.T) {
	spec, _, err := buildChartSpec(map[string]any{
		"chart_code": `{"type":"line","data":{"labels":["Jul 21","Jan 22"],"datasets":[{"label":"AAPL","data":[142.18,170.87]}]}}`,
	})
	if err != nil {
		t.Fatalf("expected chart_code+Chart.js shape to be accepted, got: %v", err)
	}
	m := decodeSpec(t, spec)
	s := m["series"].([]any)
	if len(s) != 1 || s[0].(map[string]any)["type"] != "line" {
		t.Fatalf("expected one line series, got %v", s)
	}
	if len(s[0].(map[string]any)["data"].([]any)) != 2 {
		t.Errorf("series data not lifted: %v", s[0])
	}
	if len(m["xAxis"].(map[string]any)["data"].([]any)) != 2 {
		t.Errorf("labels not lifted from nested data: %v", m["xAxis"])
	}
}

// {"chart_code":"[1,2,3]","chart_type":"bar","labels":[...]} — bare array in the alias.
func TestBuildChartSpec_ChartCodeBareArray(t *testing.T) {
	spec, _, err := buildChartSpec(map[string]any{
		"chart_code": "[142.18, 170.87, 159.31]",
		"chart_type": "bar",
		"labels":     []any{"a", "b", "c"},
	})
	if err != nil {
		t.Fatalf("expected chart_code bare array to be accepted, got: %v", err)
	}
	m := decodeSpec(t, spec)
	s0 := m["series"].([]any)[0].(map[string]any)
	if s0["type"] != "bar" || len(s0["data"].([]any)) != 3 {
		t.Errorf("bare array not used as series data: %v", s0)
	}
}

func TestBuildChartSpec_Errors(t *testing.T) {
	if _, _, err := buildChartSpec(map[string]any{"chart_type": "sankey", "datasets": []any{map[string]any{"data": []any{1.0}}}}); err == nil {
		t.Error("expected error for unsupported chart_type")
	}
	if _, _, err := buildChartSpec(map[string]any{"chart_type": "bar"}); err == nil {
		t.Error("expected error when no datasets/data provided")
	}
	if _, _, err := buildChartSpec(map[string]any{"chart_type": "bar", "datasets": []any{map[string]any{"data": []any{}}}}); err == nil {
		t.Error("expected error when dataset has no numeric data")
	}
}

func TestGenerateChart_StashesBlockAndAcksModel(t *testing.T) {
	e := &Engine{}
	ctx := withChartSink(context.Background())
	out, err := e.generateChart(ctx, map[string]any{
		"chart_type": "bar",
		"datasets":   []any{map[string]any{"data": []any{1.0, 2.0}}},
	})
	if err != nil {
		t.Fatalf("generateChart: %v", err)
	}
	// The model-facing result must NOT contain the raw fence (avoid echo/double-render).
	if strings.Contains(out, "```chart") {
		t.Errorf("model ack should not contain the chart fence, got: %q", out)
	}
	cs := chartSinkFrom(ctx)
	blocks := cs.collected()
	if len(blocks) != 1 || !strings.HasPrefix(blocks[0], "```chart") {
		t.Fatalf("expected one stashed chart fence, got %v", blocks)
	}
}

func TestGenerateChart_NoSinkReturnsFence(t *testing.T) {
	e := &Engine{}
	out, err := e.generateChart(context.Background(), map[string]any{
		"chart_type": "line",
		"datasets":   []any{map[string]any{"data": []any{3.0, 4.0}}},
	})
	if err != nil {
		t.Fatalf("generateChart: %v", err)
	}
	if !strings.Contains(out, "```chart") {
		t.Errorf("without a sink the fence should be returned inline, got: %q", out)
	}
}

func TestAppendCollectedCharts(t *testing.T) {
	// nil sink → unchanged
	if got := appendCollectedCharts("hello", nil); got != "hello" {
		t.Errorf("nil sink changed content: %q", got)
	}

	cs := &chartSink{}
	cs.add("```chart\n{\"type\":\"bar\"}\n```")
	got := appendCollectedCharts("Here is the trend:", cs)
	if !strings.Contains(got, "Here is the trend:") || !strings.Contains(got, "```chart") {
		t.Errorf("append did not combine text + chart: %q", got)
	}

	// dedupe: if the text already contains the block, don't add it twice.
	block := "```chart\n{\"type\":\"pie\"}\n```"
	cs2 := &chartSink{}
	cs2.add(block)
	pre := "Answer with chart:\n\n" + block
	got2 := appendCollectedCharts(pre, cs2)
	if strings.Count(got2, "```chart") != 1 {
		t.Errorf("expected dedupe to keep a single fence, got %d", strings.Count(got2, "```chart"))
	}

	// empty content → chart becomes the whole reply (no leading blank lines)
	cs3 := &chartSink{}
	cs3.add(block)
	got3 := appendCollectedCharts("   ", cs3)
	if got3 != block {
		t.Errorf("empty content should become the block alone, got: %q", got3)
	}
}
