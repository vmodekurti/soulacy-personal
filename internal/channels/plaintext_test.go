package channels

import (
	"strings"
	"testing"
)

func TestPlainTextForMessaging_ReplacesChartFenceWithSummary(t *testing.T) {
	in := "The chart above shows the relentless heat.\n\n```chart\n" +
		`{"series":[{"data":[100,101,98],"name":"High (°F)","type":"line"},{"data":[75,76,73],"name":"Low (°F)","type":"line"}],"title":{"text":"Charlotte 7-Day Forecast"},"xAxis":{"data":["Thu","Fri","Sat"],"type":"category"},"yAxis":{"type":"value"}}` +
		"\n```"

	got := PlainTextForMessaging(in)
	if strings.Contains(got, "```chart") || strings.Contains(got, `"series"`) {
		t.Fatalf("raw chart JSON leaked into messaging output:\n%s", got)
	}
	for _, want := range []string{
		"The forecast trend shows the relentless heat.",
		"Chart summary: Charlotte 7-Day Forecast",
		"High (°F): Thu 100 -> Sat 98; peak Fri 101",
		"Low (°F): Thu 75 -> Sat 73; peak Fri 76",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPlainTextForMessaging_DropsMalformedChartFence(t *testing.T) {
	got := PlainTextForMessaging("hello\n\n```chart\nnot json\n```\n\nworld")
	if got != "hello\n\nworld" {
		t.Fatalf("got %q", got)
	}
}
