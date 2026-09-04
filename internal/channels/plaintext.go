package channels

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var chartFenceRe = regexp.MustCompile("(?s)```chart\\s*\\n(.*?)\\n```")

// PlainTextForMessaging adapts rich GUI-oriented Markdown for text-only
// messaging channels. Soulacy chart fences are useful in the web UI, but they
// are unreadable JSON in Telegram/SMS-style surfaces.
func PlainTextForMessaging(text string) string {
	text = strings.ReplaceAll(text, "The chart above shows", "The forecast trend shows")
	text = strings.ReplaceAll(text, "the chart above shows", "the forecast trend shows")
	text = strings.ReplaceAll(text, "The chart is now displayed to the user.", "")
	text = chartFenceRe.ReplaceAllStringFunc(text, func(block string) string {
		m := chartFenceRe.FindStringSubmatch(block)
		if len(m) != 2 {
			return ""
		}
		return chartTextSummary(m[1])
	})
	return strings.TrimSpace(collapseBlankLines(text))
}

func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

type chartSpec struct {
	Title struct {
		Text string `json:"text"`
	} `json:"title"`
	XAxis struct {
		Data []string `json:"data"`
	} `json:"xAxis"`
	Series []chartSeries `json:"series"`
}

type chartSeries struct {
	Name string    `json:"name"`
	Data []float64 `json:"data"`
	Type string    `json:"type"`
}

func chartTextSummary(raw string) string {
	var spec chartSpec
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &spec); err != nil {
		return ""
	}
	title := strings.TrimSpace(spec.Title.Text)
	if title == "" {
		title = "Chart summary"
	}
	lines := []string{"", "Chart summary: " + title}
	for _, s := range spec.Series {
		line, ok := summarizeChartSeries(s, spec.XAxis.Data)
		if ok {
			lines = append(lines, "- "+line)
		}
	}
	if len(lines) == 2 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func summarizeChartSeries(s chartSeries, labels []string) (string, bool) {
	if len(s.Data) == 0 {
		return "", false
	}
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = "Series"
	}
	first := s.Data[0]
	last := s.Data[len(s.Data)-1]
	peak, peakIdx := first, 0
	for i, v := range s.Data {
		if v > peak || math.IsNaN(peak) {
			peak, peakIdx = v, i
		}
	}
	firstLabel := labelAt(labels, 0)
	lastLabel := labelAt(labels, len(s.Data)-1)
	peakLabel := labelAt(labels, peakIdx)
	return fmt.Sprintf("%s: %s%s -> %s%s; peak %s%s",
		name,
		firstLabel,
		formatChartNumber(first),
		lastLabel,
		formatChartNumber(last),
		peakLabel,
		formatChartNumber(peak),
	), true
}

func labelAt(labels []string, idx int) string {
	if idx >= 0 && idx < len(labels) && strings.TrimSpace(labels[idx]) != "" {
		return strings.TrimSpace(labels[idx]) + " "
	}
	return ""
}

func formatChartNumber(v float64) string {
	if math.Abs(v-math.Round(v)) < 0.000001 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
