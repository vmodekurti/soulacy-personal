package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/soulacy/soulacy/pkg/agent"
)

// chartToolGuide nudges agents to reach for generate_chart when an answer is
// fundamentally about numbers. Appended to the system prompt only when the tool
// is actually offered (see agentHasChartTool), so we never advertise a tool the
// agent can't call.
const chartToolGuide = "## Visualizing data\n" +
	"You can render an interactive chart for the user with the `generate_chart` tool " +
	"(bar, line, pie, doughnut, scatter, radar). When your answer centers on numbers — " +
	"comparisons, trends over time, breakdowns, distributions, or rankings — prefer a chart " +
	"over a long table or prose: call `generate_chart` with the data, then give a one-line " +
	"takeaway. The chart is shown to the user automatically, so don't restate every value. " +
	"For a single number or a tiny list, plain text is still better.\n" +
	"IMPORTANT: do NOT draw a data chart any other way. Specifically, never use a Mermaid " +
	"```mermaid block (including xychart / xychart-beta or pie), ASCII art, raw HTML, <canvas>, " +
	"<script>, <style>, SVG, CDN-loaded JavaScript, or a hand-written chart spec/JSON. Mermaid is " +
	"only for diagrams (flowcharts, sequence) — never for plotting data, and its xychart syntax often " +
	"fails to render. The app already applies a consistent visual theme. The ONLY way to show a real " +
	"data chart (a trend, comparison, breakdown, distribution, or ranking) is to call `generate_chart` " +
	"with just the data (chart_type, labels, datasets) — do not specify colors, styles, or chart " +
	"options yourself; the app themes every chart for you."

// agentHasChartTool reports whether generate_chart is offered to this agent,
// mirroring the allowlist logic in allToolSchemas. generate_chart has Gate "",
// so only an explicit `builtins:` allowlist can withhold it.
func agentHasChartTool(def *agent.Definition) bool {
	if def.Builtins == nil {
		return true
	}
	for _, n := range *def.Builtins {
		if n == "*" || n == "all" || n == "generate_chart" {
			return true
		}
	}
	return false
}

// generate_chart is a built-in, always-on tool that lets ANY agent render an
// interactive data chart for the user. The agent calls it with the data it wants
// visualized; the handler builds and validates a Chart.js spec and stashes it in
// a run-scoped collector. After the reasoning loop produces the final answer, the
// engine appends the collected ```chart fences to the reply text, which the GUI
// turns into a live Chart.js canvas.
//
// Why append in the engine instead of returning the spec to the model: models are
// unreliable at echoing a large JSON block verbatim, and if they do echo it we'd
// risk a double render. So the handler returns only a short acknowledgement to the
// model (which keeps reasoning cheap and clean) and the deterministic append below
// guarantees the chart actually reaches the user, deduped against any spec the
// model happened to include itself.

// ── run-scoped chart collector ───────────────────────────────────────────────

type chartSinkKey struct{}

// chartSink gathers chart fences produced during a single Handle run.
type chartSink struct {
	mu     sync.Mutex
	blocks []string
}

func (c *chartSink) add(block string) {
	c.mu.Lock()
	c.blocks = append(c.blocks, block)
	c.mu.Unlock()
}

func (c *chartSink) collected() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.blocks...)
}

// withChartSink attaches a fresh chart collector for the duration of a run.
func withChartSink(ctx context.Context) context.Context {
	return context.WithValue(ctx, chartSinkKey{}, &chartSink{})
}

func chartSinkFrom(ctx context.Context) *chartSink {
	cs, _ := ctx.Value(chartSinkKey{}).(*chartSink)
	return cs
}

// appendCollectedCharts appends each collected chart fence to the reply text,
// skipping any block the model already included verbatim. Safe with a nil sink.
func appendCollectedCharts(finalContent string, cs *chartSink) string {
	if cs == nil {
		return finalContent
	}
	for _, block := range cs.collected() {
		if strings.Contains(finalContent, block) {
			continue
		}
		if strings.TrimSpace(finalContent) == "" {
			finalContent = block
		} else {
			finalContent += "\n\n" + block
		}
	}
	return finalContent
}

// ── the built-in tool ────────────────────────────────────────────────────────

func (e *Engine) buildChartBuiltin() BuiltinTool {
	return BuiltinTool{
		Name: "generate_chart",
		Gate: "",
		Description: "Render an interactive visual chart for the user (bar, line, pie, doughnut, scatter, or radar). " +
			"Call this whenever numbers, comparisons, trends, time series, or distributions would be clearer as a chart " +
			"instead of a table or prose. The chart is shown directly to the user, so you don't need to restate the raw " +
			"numbers afterward — a one-line takeaway is enough. Provide the data as one or more datasets of numbers.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"chart_type": map[string]any{
					"type":        "string",
					"enum":        []string{"bar", "line", "pie", "doughnut", "scatter", "radar"},
					"description": "The kind of chart. Use bar/line for comparisons & trends, pie/doughnut for parts of a whole, scatter for correlations, radar for multi-axis profiles.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "A short chart title shown above the chart.",
				},
				"labels": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Category labels for the x-axis / slices, e.g. [\"Q1\",\"Q2\",\"Q3\",\"Q4\"]. One label per data point.",
				},
				"datasets": map[string]any{
					"type":        "array",
					"description": "One or more series. Each item is an object {\"label\": string, \"data\": [numbers]}. The data array length should match labels.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label": map[string]any{"type": "string", "description": "Series name shown in the legend."},
							"data":  map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "The numeric values for this series."},
						},
						"required": []string{"data"},
					},
				},
			},
			"required": []string{"chart_type", "datasets"},
		},
		Handler: e.generateChart,
	}
}

func (e *Engine) generateChart(ctx context.Context, args map[string]any) (string, error) {
	spec, summary, err := buildChartSpec(args)
	if err != nil {
		return "", err
	}
	block := "```chart\n" + spec + "\n```"
	if cs := chartSinkFrom(ctx); cs != nil {
		cs.add(block)
		// Tell the model it succeeded without handing back the JSON to echo.
		return summary + " The chart is now displayed to the user.", nil
	}
	// No run-scoped sink (tool invoked outside a chat Handle, e.g. a test or a
	// non-GUI surface) — return the fence so it can still surface via the text.
	return summary + "\n\n" + block, nil
}

// buildChartSpec converts the friendly tool arguments into a compact, valid
// Apache ECharts `option` JSON string. We emit only structure (axes + series +
// optional title); the GUI restyles it (gradients, smooth lines, rounded bars,
// tooltip, palette) so the agent never has to know ECharts.
func buildChartSpec(args map[string]any) (specJSON, summary string, err error) {
	normalizeChartArgs(args)
	ct := strings.ToLower(strings.TrimSpace(argString(args, "chart_type")))
	if ct == "" {
		ct = "bar"
	}
	allowed := map[string]bool{"bar": true, "line": true, "pie": true, "doughnut": true, "scatter": true, "radar": true}
	if !allowed[ct] {
		return "", "", fmt.Errorf("unsupported chart_type %q (use bar, line, pie, doughnut, scatter, or radar)", ct)
	}

	title := strings.TrimSpace(argString(args, "title"))
	labels := argStringSlice(args, "labels")
	datasets := parseDatasets(args)
	if len(datasets) == 0 {
		return "", "", fmt.Errorf("no chart data: provide \"datasets\" as [{\"label\":..., \"data\":[numbers]}] (or a \"data\" array of numbers)")
	}

	option := map[string]any{}
	if title != "" {
		option["title"] = map[string]any{"text": title}
	}

	switch ct {
	case "pie", "doughnut":
		// Pie uses one series; pair each value with its label.
		nums := numsOf(datasets[0])
		data := make([]map[string]any, 0, len(nums))
		for i, v := range nums {
			name := strconv.Itoa(i + 1)
			if i < len(labels) {
				name = labels[i]
			}
			data = append(data, map[string]any{"name": name, "value": v})
		}
		series := map[string]any{"type": "pie", "data": data}
		if ct == "doughnut" {
			series["radius"] = []string{"45%", "72%"}
		}
		option["series"] = []any{series}

	case "radar":
		indicator := make([]map[string]any, 0, len(labels))
		for _, l := range labels {
			indicator = append(indicator, map[string]any{"name": l})
		}
		option["radar"] = map[string]any{"indicator": indicator}
		data := make([]map[string]any, 0, len(datasets))
		for _, d := range datasets {
			item := map[string]any{"value": numsOf(d)}
			if lbl := dsLabel(d); lbl != "" {
				item["name"] = lbl
			}
			data = append(data, item)
		}
		option["series"] = []any{map[string]any{"type": "radar", "data": data}}

	default: // bar, line, scatter — all cartesian
		xAxis := map[string]any{"type": "category"}
		if len(labels) > 0 {
			xAxis["data"] = labels
		}
		option["xAxis"] = xAxis
		option["yAxis"] = map[string]any{"type": "value"}
		series := make([]any, 0, len(datasets))
		for _, d := range datasets {
			s := map[string]any{"type": ct, "data": numsOf(d)}
			if lbl := dsLabel(d); lbl != "" {
				s["name"] = lbl
			}
			series = append(series, s)
		}
		option["series"] = series
	}

	b, err := json.Marshal(option)
	if err != nil {
		return "", "", fmt.Errorf("encode chart spec: %w", err)
	}

	summary = fmt.Sprintf("Rendered a %s chart", ct)
	if title != "" {
		summary += fmt.Sprintf(" titled %q", title)
	}
	if len(datasets) == 1 {
		summary += " with 1 series."
	} else {
		summary += fmt.Sprintf(" with %d series.", len(datasets))
	}
	return string(b), summary, nil
}

// normalizeChartArgs is liberal in what it accepts. Some models (notably coder
// models that learned a different chart tool) cram the whole payload into a
// single stringified `data` field, e.g.
//
//	{"chart_type":"line","data":"{\"labels\":[...],\"datasets\":[...]}"}
//
// or pass `labels`/`datasets` as JSON strings rather than arrays. Unwrap those
// in place so buildChartSpec sees the documented shape instead of erroring.
func normalizeChartArgs(args map[string]any) {
	// (1) Some models invent a single payload key (chart_code, spec, …) and cram
	// everything into it. Treat the first one we find as `data`.
	for _, alias := range []string{"chart_code", "chart_data", "chartData", "spec", "config", "code", "chart"} {
		if _, hasData := args["data"]; hasData {
			break
		}
		if v, ok := args[alias]; ok {
			args["data"] = v
			delete(args, alias)
			break
		}
	}

	// (2) `type` is a frequent alias for `chart_type`.
	if strings.TrimSpace(argString(args, "chart_type")) == "" {
		if t := strings.TrimSpace(argString(args, "type")); t != "" {
			args["chart_type"] = t
		}
	}

	// (3) `data` given as a JSON string → decode to an object or an array.
	if s, ok := args["data"].(string); ok {
		s = strings.TrimSpace(s)
		var obj map[string]any
		var arr []any
		if json.Unmarshal([]byte(s), &obj) == nil {
			args["data"] = obj
		} else if json.Unmarshal([]byte(s), &arr) == nil {
			args["data"] = arr
		}
	}

	// (4) `data` as a wrapper OBJECT → lift chart_type/title/labels/datasets out.
	// Handles the Chart.js shape {type, data:{labels, datasets}}, the flat
	// {labels, datasets}, and {data:[numbers], labels}.
	if obj, ok := args["data"].(map[string]any); ok {
		if strings.TrimSpace(argString(args, "chart_type")) == "" {
			if t := strings.TrimSpace(argString(obj, "type")); t != "" {
				args["chart_type"] = t
			}
		}
		if _, has := args["title"]; !has {
			if tv, ok := obj["title"]; ok {
				args["title"] = tv
			}
		}
		// The series container is obj itself, or a nested obj["data"] object.
		container := obj
		if nested, ok := obj["data"].(map[string]any); ok {
			container = nested
		}
		if _, has := args["labels"]; !has {
			if v, ok := container["labels"]; ok {
				args["labels"] = v
			}
		}
		if ds, ok := container["datasets"]; ok {
			args["datasets"] = ds
			delete(args, "data")
		} else if nums, ok := container["data"].([]any); ok {
			args["data"] = nums // bare numeric series
		} else {
			delete(args, "data") // wrapper held nothing usable
		}
	}

	// (5) datasets / labels given as JSON strings → decode to arrays.
	for _, key := range []string{"datasets", "labels"} {
		if s, ok := args[key].(string); ok {
			var arr []any
			if json.Unmarshal([]byte(strings.TrimSpace(s)), &arr) == nil {
				args[key] = arr
			}
		}
	}
}

// numsOf returns the numeric data slice a dataset map carries (set by parseDatasets).
func numsOf(d map[string]any) []float64 {
	if n, ok := d["data"].([]float64); ok {
		return n
	}
	return nil
}

// dsLabel returns the dataset's series label, or "" when unset.
func dsLabel(d map[string]any) string {
	if s, ok := d["label"].(string); ok {
		return s
	}
	return ""
}

// parseDatasets accepts the documented shape (datasets: [{label, data}]) and a
// couple of forgiving fallbacks (a bare top-level "data"/"values" number array),
// so a small model that doesn't follow the schema exactly still produces a chart.
func parseDatasets(args map[string]any) []map[string]any {
	var out []map[string]any

	if raw, ok := args["datasets"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			nums := toNumberSlice(m["data"])
			if len(nums) == 0 {
				continue
			}
			ds := map[string]any{"data": nums}
			if lbl := strings.TrimSpace(argString(m, "label")); lbl != "" {
				ds["label"] = lbl
			}
			out = append(out, ds)
		}
	}
	if len(out) > 0 {
		return out
	}

	// Fallback: a single series given as a bare number array.
	for _, key := range []string{"data", "values"} {
		if nums := toNumberSlice(args[key]); len(nums) > 0 {
			ds := map[string]any{"data": nums}
			if lbl := strings.TrimSpace(argString(args, "label")); lbl != "" {
				ds["label"] = lbl
			}
			return []map[string]any{ds}
		}
	}
	return nil
}

// toNumberSlice coerces a JSON-decoded value into a []float64, tolerating ints,
// numeric strings, and json.Number. Non-numeric entries are skipped.
func toNumberSlice(v any) []float64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		switch n := e.(type) {
		case float64:
			out = append(out, n)
		case int:
			out = append(out, float64(n))
		case int64:
			out = append(out, float64(n))
		case json.Number:
			if f, err := n.Float64(); err == nil {
				out = append(out, f)
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
				out = append(out, f)
			}
		}
	}
	return out
}
