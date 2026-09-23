package presentation

import (
	"strings"
	"testing"
)

const fares = `Here are the best round-trip fares for **Chicago (ORD) → London (LHR), Oct 2 → Oct 9, 2026**, 1 adult, economy. All live fares came from Momondo (Skyscanner returned no listings this run):

## Top options

| # | Airline | Outbound (Oct 2) | Return (Oct 9) | Stops | Price |
[REDACTED:73cfe0f9ad8c]
| 1 | British Airways | 4:29pm → 6:49am+1 (8h 20m) | 4:44pm → 11:37pm (1 stop) | 0 out / 1 back | **$770** |
| 2 | American Airlines | 5:55pm → 7:40am+1 (7h 45m) | 1:10pm → 3:55pm (8h 45m) | Nonstop both ways | $839 |
| 3 | British Airways | 8:50pm → 10:50am+1 (8h 00m) | 1:10pm → 3:55pm (8h 45m) | Nonstop both ways | $848 |
| 4 | British Airways | 5:00pm → 6:50am+1 (7h 50m) | 1:10pm → 3:55pm (8h 45m) | Nonstop both ways | $858 |

## My recommendation

**Option 2 — American Airlines, $839 nonstop both ways** is the best value.

Book it:
- [Momondo — ORD→LHR, Oct 2–9](https://www.momondo.com/flight-search/ORD-LHR/2026-10-02/2026-10-09)
- [Skyscanner — same dates](https://www.skyscanner.com/transport/flights/ord/lhr/261002/261009/)
`

func kinds(p Presentation) string {
	var k []string
	for _, b := range p.Blocks {
		k = append(k, b.Kind)
	}
	return strings.Join(k, ",")
}

func find(p Presentation, kind string) *Block {
	for i := range p.Blocks {
		if p.Blocks[i].Kind == kind {
			return &p.Blocks[i]
		}
	}
	return nil
}

func TestFares(t *testing.T) {
	p := Present(fares)
	if !strings.HasPrefix(p.Headline, "Here are the best round-trip fares for Chicago") {
		t.Fatalf("headline = %q", p.Headline)
	}
	if !strings.HasPrefix(p.Summary, "All live fares came from Momondo") {
		t.Fatalf("summary = %q", p.Summary)
	}
	c := find(p, "comparison")
	if c == nil {
		t.Fatalf("no comparison in %s", kinds(p))
	}
	if c.Title != "Top options" || len(c.Rows) != 4 || len(c.Columns) != 6 {
		t.Fatalf("comparison = %+v", *c)
	}
	if c.Rows[0][5] != "$770" {
		t.Fatalf("cell kept inline markdown: %q", c.Rows[0][5])
	}
	if strings.Join(c.Numeric, ",") != "Price" {
		t.Fatalf("numeric columns = %v (the # index column must not count)", c.Numeric)
	}
	if c.Best == nil || c.Best.Column != "Price" || c.Best.Row != 0 {
		t.Fatalf("best = %+v (lowest price is row 0)", c.Best)
	}
	ch := find(p, "chart")
	if ch == nil || ch.Chart.Type != "bar" || len(ch.Chart.X) != 4 || ch.Chart.X[0] != "British Airways" {
		t.Fatalf("chart = %+v", ch)
	}
	if ch.Chart.Series[0].Name != "Price" || ch.Chart.Series[0].Values[1] != 839 {
		t.Fatalf("series = %+v", ch.Chart.Series)
	}
	l := find(p, "links")
	if l == nil || len(l.Items) != 2 || !strings.Contains(l.Items[0].URL, "momondo") {
		t.Fatalf("links = %+v", l)
	}
	// The numbers live in the table; long bold phrases are not metrics.
	if m := find(p, "metrics"); m != nil {
		t.Fatalf("unexpected metrics = %+v", m.Items)
	}
	// The headline is not repeated in the prose, and the heading that titles
	// the grid is not shown twice.
	// Prose neither repeats the headline or the summary nor shows the
	// heading that titles the grid; here nothing is left before the grid.
	if p.Blocks[0].Kind != "comparison" {
		t.Fatalf("blocks = %s (first markdown = %q)", kinds(p), p.Blocks[0].Text)
	}
}

func TestNoFalseMetrics(t *testing.T) {
	p := Present("Option 1 has a **1-stop return** and a **2h layover**; total **$770** for **2 adults**.")
	m := find(p, "metrics")
	if m == nil {
		t.Fatal("expected the $770 metric")
	}
	for _, it := range m.Items {
		if it.Value != "$770" && it.Value != "2h layover" {
			t.Errorf("false metric %q", it.Value)
		}
	}
}

func TestReport(t *testing.T) {
	p := Present("# Backups verified\n\nAll **3 of 3** targets completed.\n\n- [x] soulspace → S3 · 1.2 GB\n- [x] postgres dump\n- [ ] qdrant snapshot\n")
	if p.Headline != "Backups verified" {
		t.Fatalf("headline = %q", p.Headline)
	}
	c := find(p, "checklist")
	if c == nil || len(c.Items) != 3 || *c.Items[2].Done {
		t.Fatalf("checklist = %+v", c)
	}
	m := find(p, "metrics")
	if m == nil || m.Items[0].Value != "3 of 3" {
		t.Fatalf("metrics = %+v", m)
	}
}

func TestBrief(t *testing.T) {
	p := Present("☀️ Tue 23 Sep · Chicago · 71°/58°, dry until evening\n\n## Today\n- 09:30 Standup (30m)\n- 12:00 Lunch w/ Priya — Lula Cafe\n- 15:00 Dentist — leave by 14:30\n")
	tl := find(p, "timeline")
	if tl == nil || len(tl.Items) != 3 || tl.Items[2].At != "15:00" || !strings.HasPrefix(tl.Items[2].Title, "Dentist") {
		t.Fatalf("timeline = %+v", tl)
	}
	if tl.Title != "Today" {
		t.Fatalf("timeline title = %q", tl.Title)
	}
}

func TestProseStaysMarkdown(t *testing.T) {
	p := Present("I have to be straight with you: live fare extraction is currently blocked on both sites. Rather than fabricate numbers, here is what I can verifiably offer.")
	if kinds(p) != "markdown" {
		t.Fatalf("kinds = %s", kinds(p))
	}
	if !strings.HasPrefix(p.Headline, "I have to be straight with you") {
		t.Fatalf("headline = %q", p.Headline)
	}
}

func TestNumbers(t *testing.T) {
	for in, want := range map[string]float64{"$770": 770, "1,234.5": 1234.5, "12%": 12, "71°": 71, "8h 20m": 500, "45m": 45, "~2.5k": 2500} {
		if v, ok := number(in); !ok || v != want {
			t.Errorf("number(%q) = %v,%v want %v", in, v, ok, want)
		}
	}
	for _, in := range []string{"Nonstop both ways", "4:29pm → 6:49am+1", "BA"} {
		if _, ok := number(in); ok {
			t.Errorf("number(%q) should not parse", in)
		}
	}
}

// A bold number inside a sentence is not a metric; a labelled one is (#212).
func TestMetricsNeedALabel(t *testing.T) {
	p := Present("Here's the market picture:\n\nThe Fed sees rates returning to target until **2029**. The 10-year Treasury: climbed back to **5.00%**. Bitcoin: topped **$80,000** this week.")
	var m *Block
	for i := range p.Blocks {
		if p.Blocks[i].Kind == "metrics" {
			m = &p.Blocks[i]
		}
	}
	if m == nil {
		t.Fatalf("expected a metrics block, got %+v", p.Blocks)
	}
	var labels []string
	for _, it := range m.Items {
		labels = append(labels, it.Label+"="+it.Value)
	}
	got := strings.Join(labels, "|")
	if strings.Contains(got, "2029") {
		t.Fatalf("a year in a sentence became a metric: %s", got)
	}
	if !strings.Contains(got, "Treasury=5.00%") || !strings.Contains(got, "Bitcoin=$80,000") {
		t.Fatalf("labelled values missing: %s", got)
	}
}
