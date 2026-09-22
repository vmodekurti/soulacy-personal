// Package presentation turns an agent's markdown result into typed blocks so
// every surface — web feed, phone, story — shows the right thing for the
// content: a comparison grid and a bar chart for a fare table, metric tiles
// for the numbers that matter, a timeline for a day, link cards for
// "book here", a checklist for a report. Decided once, here, deterministically
// (no model call, microseconds), so the clients never drift from each other.
// Anything not recognised stays markdown, so an older client loses nothing.
// (soulacy-personal #199)
package presentation

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Presentation is what a client renders for one result.
type Presentation struct {
	Headline string  `json:"headline,omitempty"`
	Summary  string  `json:"summary,omitempty"`
	Blocks   []Block `json:"blocks"`
}

// Block kinds: markdown, metrics, comparison, chart, timeline, links, checklist.
type Block struct {
	Kind    string     `json:"kind"`
	Title   string     `json:"title,omitempty"`
	Text    string     `json:"text,omitempty"`    // markdown
	Items   []Item     `json:"items,omitempty"`   // metrics · timeline · links · checklist
	Columns []string   `json:"columns,omitempty"` // comparison
	Rows    [][]string `json:"rows,omitempty"`
	Numeric []string   `json:"numeric,omitempty"` // columns that parse as numbers
	Best    *Best      `json:"best,omitempty"`
	Chart   *Chart     `json:"chart,omitempty"`
}

type Item struct {
	Label  string `json:"label,omitempty"`
	Value  string `json:"value,omitempty"`
	Hint   string `json:"hint,omitempty"`
	At     string `json:"at,omitempty"`
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
	Done   *bool  `json:"done,omitempty"`
}

// Best names the row a person would probably pick: lowest price/duration,
// highest of anything else.
type Best struct {
	Column string `json:"column"`
	Row    int    `json:"row"`
}

type Chart struct {
	Type   string   `json:"type"` // bar | line
	X      []string `json:"x"`
	Series []Series `json:"series"`
}

type Series struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bulletRe  = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	checkRe   = regexp.MustCompile(`^\[([ xX])\]\s*(.*)$`)
	linkRe    = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	boldNumRe = regexp.MustCompile(`\*\*([^*]*\d[^*]*)\*\*`)
	// A metric value is number-led and short: $839 · 3 of 3 · 12% · 71°/58° · 1.2 GB.
	metricValRe = regexp.MustCompile(`^[~≈]?[-+]?[$€£]?\d[\d,.]*(?:\s*(?:%|°[CF]?|/\d[\d,.]*°?|of\s+\d+|[kKmMbB]|[A-Za-z]{1,4}))?$`)
	timeLeadRe  = regexp.MustCompile(`^((?:[01]?\d|2[0-3]):[0-5]\d\s*(?:[ap]m)?|(?:[1-9]|1[0-2])\s*[ap]m|(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)[a-z]*\.?\s+\d{1,2}(?:\s+[A-Z][a-z]{2})?|(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.?\s+\d{1,2}|\d{4}-\d{2}-\d{2})\b[\s:—–-]*(.*)$`)
	numRe       = regexp.MustCompile(`^[~≈]?\s*[-+]?\$?\s*\d[\d,]*(?:\.\d+)?\s*(?:%|°[CF]?|[kKmMbB])?$`)
	durRe       = regexp.MustCompile(`^(?:(\d+)\s*h)?\s*(?:(\d+)\s*m(?:in)?)?$`)
	redactedRe  = regexp.MustCompile(`^\[REDACTED:[0-9a-f]+\]$`)
	sentenceEnd = regexp.MustCompile(`([.!?])(\s|$)`)
	lowerBetter = regexp.MustCompile(`(?i)price|cost|fare|total|duration|time|stops|delay|latency|wait|distance|risk`)
	dateish     = regexp.MustCompile(`(?i)^(?:\d{4}-\d{2}-\d{2}|(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)|(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)|\d{1,2}[/-]\d{1,2}|Q[1-4])`)
)

// Present decides how a result should look.
func Present(markdown string) Presentation {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var p Presentation
	p.Headline, p.Summary = headlineAndSummary(lines)

	var md []string
	// flush emits the buffered prose as a markdown block. Before a typed
	// block that took the last heading as its title, that heading line is
	// dropped so it is not shown twice; the headline line is dropped from
	// the first block for the same reason.
	headlineDropped := false
	flush := func(titled bool) {
		if titled {
			for len(md) > 0 && strings.TrimSpace(md[len(md)-1]) == "" {
				md = md[:len(md)-1]
			}
			if len(md) > 0 && headingRe.MatchString(strings.TrimSpace(md[len(md)-1])) {
				md = md[:len(md)-1]
			}
		}
		if !headlineDropped && p.Headline != "" {
			md, headlineDropped = dropHeadline(md, p.Headline)
		}
		text := strings.TrimSpace(strings.Join(md, "\n"))
		md = md[:0]
		if text != "" {
			p.Blocks = append(p.Blocks, Block{Kind: "markdown", Text: text})
		}
	}
	title := "" // the most recent heading, for the next typed block

	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		if m := headingRe.FindStringSubmatch(line); m != nil {
			title = strings.TrimSpace(m[2])
			md = append(md, lines[i])
			i++
			continue
		}
		if isTableRow(line) && i+1 < len(lines) && isTableSeparator(strings.TrimSpace(lines[i+1])) {
			cols := cells(line)
			var rows [][]string
			j := i + 2
			for j < len(lines) && isTableRow(strings.TrimSpace(lines[j])) {
				if r := cells(strings.TrimSpace(lines[j])); len(r) > 0 {
					rows = append(rows, pad(r, len(cols)))
				}
				j++
			}
			if len(rows) >= 2 {
				flush(title != "")
				p.Blocks = append(p.Blocks, comparison(title, cols, rows)...)
				title = ""
				i = j
				continue
			}
		}
		if bulletRe.MatchString(line) {
			var items []string
			j := i
			for j < len(lines) {
				t := strings.TrimSpace(lines[j])
				m := bulletRe.FindStringSubmatch(t)
				if m == nil {
					break
				}
				items = append(items, strings.TrimSpace(m[1]))
				j++
			}
			if b, ok := typedList(title, items); ok {
				flush(title != "")
				p.Blocks = append(p.Blocks, b)
				title = ""
				i = j
				continue
			}
			md = append(md, lines[i:j]...)
			i = j
			continue
		}
		md = append(md, lines[i])
		i++
	}
	flush(false)

	// The summary is shown on its own; do not open the prose with it too.
	if p.Summary != "" && len(p.Blocks) > 0 && p.Blocks[0].Kind == "markdown" {
		key := strings.TrimSuffix(p.Summary, "…")
		if t := strings.TrimSpace(p.Blocks[0].Text); strings.HasPrefix(t, key) {
			rest := strings.TrimSpace(strings.TrimPrefix(t, key))
			if rest == "" {
				p.Blocks = p.Blocks[1:]
			} else {
				p.Blocks[0].Text = rest
			}
		}
	}

	if m := metrics(markdown); len(m.Items) > 0 {
		// Metrics lead: they are the point of the result.
		p.Blocks = append([]Block{m}, p.Blocks...)
	}
	if len(p.Blocks) == 0 {
		p.Blocks = []Block{{Kind: "markdown", Text: strings.TrimSpace(markdown)}}
	}
	return p
}

// dropHeadline removes the line the headline was taken from (a heading, or
// a first sentence — in which case only that sentence is removed).
func dropHeadline(md []string, headline string) ([]string, bool) {
	key := strings.TrimSuffix(headline, "…")
	if len(key) > 24 {
		key = key[:24]
	}
	for i, raw := range md {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "|") || redactedRe.MatchString(l) {
			continue
		}
		if m := headingRe.FindStringSubmatch(l); m != nil {
			if strings.HasPrefix(strings.TrimSpace(m[2]), key) {
				return append(md[:i:i], md[i+1:]...), true
			}
			return md, true
		}
		plain := stripInline(l)
		if !strings.HasPrefix(plain, key) {
			return md, true
		}
		if loc := sentenceEnd.FindStringIndex(plain); loc != nil && loc[0] > 24 && len(plain) > loc[1]+1 {
			out := append([]string{}, md[:i]...)
			out = append(out, strings.TrimSpace(plain[loc[1]:]))
			return append(out, md[i+1:]...), true
		}
		return append(md[:i:i], md[i+1:]...), true
	}
	return md, false
}

func headlineAndSummary(lines []string) (string, string) {
	var first string
	var rest []string
	for i, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "|") || strings.HasPrefix(l, "```") || redactedRe.MatchString(l) {
			continue
		}
		if m := headingRe.FindStringSubmatch(l); m != nil {
			first = clip(strings.TrimSpace(m[2]), 110)
			rest = lines[i+1:]
			break
		}
		plain := stripInline(l)
		if loc := sentenceEnd.FindStringIndex(plain); loc != nil && loc[0] > 24 {
			first = clip(plain[:loc[0]+1], 110)
			rest = append([]string{strings.TrimSpace(plain[loc[1]:])}, lines[i+1:]...)
		} else {
			first = clip(plain, 110)
			rest = lines[i+1:]
		}
		break
	}
	// Summary: the next one or two sentences of prose, not a table or list.
	var buf []string
	for _, raw := range rest {
		l := strings.TrimSpace(raw)
		if l == "" {
			if len(buf) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(l, "|") || strings.HasPrefix(l, "#") || bulletRe.MatchString(l) || strings.HasPrefix(l, "```") {
			break
		}
		buf = append(buf, stripInline(l))
	}
	summary := strings.Join(buf, " ")
	if locs := sentenceEnd.FindAllStringIndex(summary, 2); len(locs) == 2 {
		summary = summary[:locs[1][0]+1]
	}
	return first, clip(strings.TrimSpace(summary), 220)
}

// comparison turns a table into a grid, plus a chart when a numeric column
// has enough rows to be worth drawing.
func comparison(title string, cols []string, rows [][]string) []Block {
	b := Block{Kind: "comparison", Title: title, Columns: cols, Rows: rows}
	var firstNumeric = -1
	for c := range cols {
		ok := 0
		for _, r := range rows {
			if _, is := number(r[c]); is {
				ok++
			}
		}
		if len(rows) > 0 && ok*100/len(rows) >= 80 && !isIndexColumn(cols[c], rows, c) {
			b.Numeric = append(b.Numeric, cols[c])
			if firstNumeric < 0 {
				firstNumeric = c
			}
		}
	}
	out := []Block{b}
	if firstNumeric < 0 {
		return out
	}
	// Best row on the first numeric column.
	best, bestIdx := 0.0, -1
	lower := lowerBetter.MatchString(cols[firstNumeric])
	for i, r := range rows {
		v, ok := number(r[firstNumeric])
		if !ok {
			continue
		}
		if bestIdx < 0 || (lower && v < best) || (!lower && v > best) {
			best, bestIdx = v, i
		}
	}
	if bestIdx >= 0 {
		out[0].Best = &Best{Column: cols[firstNumeric], Row: bestIdx}
	}
	if len(rows) < 3 {
		return out
	}
	// X axis: the first non-numeric, non-index column; else row numbers.
	xCol := -1
	for c := range cols {
		if c != firstNumeric && !contains(b.Numeric, cols[c]) && !isIndexColumn(cols[c], rows, c) {
			xCol = c
			break
		}
	}
	ch := &Chart{Type: "bar"}
	for i, r := range rows {
		if xCol >= 0 {
			ch.X = append(ch.X, clip(stripInline(r[xCol]), 24))
		} else {
			ch.X = append(ch.X, strconv.Itoa(i+1))
		}
	}
	if xCol >= 0 && dateish.MatchString(strings.TrimSpace(rows[0][xCol])) {
		ch.Type = "line"
	}
	for _, name := range b.Numeric {
		if len(ch.Series) == 2 {
			break
		}
		c := indexOf(cols, name)
		s := Series{Name: name}
		for _, r := range rows {
			v, _ := number(r[c])
			s.Values = append(s.Values, v)
		}
		ch.Series = append(ch.Series, s)
	}
	out = append(out, Block{Kind: "chart", Title: title, Chart: ch})
	return out
}

// typedList recognises checklists, timelines and link lists.
func typedList(title string, items []string) (Block, bool) {
	if len(items) < 2 {
		return Block{}, false
	}
	checks, times, links := 0, 0, 0
	for _, it := range items {
		if checkRe.MatchString(it) {
			checks++
		}
		if timeLeadRe.MatchString(stripInline(it)) {
			times++
		}
		if m := linkRe.FindStringSubmatch(it); m != nil && strings.TrimSpace(strings.Replace(it, m[0], "", 1)) == "" {
			links++
		}
	}
	switch {
	case checks == len(items):
		b := Block{Kind: "checklist", Title: title}
		for _, it := range items {
			m := checkRe.FindStringSubmatch(it)
			done := m[1] != " "
			b.Items = append(b.Items, Item{Label: stripInline(m[2]), Done: &done})
		}
		return b, true
	case times == len(items):
		b := Block{Kind: "timeline", Title: title}
		for _, it := range items {
			m := timeLeadRe.FindStringSubmatch(stripInline(it))
			b.Items = append(b.Items, Item{At: strings.TrimSpace(m[1]), Title: strings.TrimSpace(m[2])})
		}
		return b, true
	case links == len(items):
		b := Block{Kind: "links", Title: title}
		for _, it := range items {
			m := linkRe.FindStringSubmatch(it)
			b.Items = append(b.Items, Item{Title: m[1], URL: m[2]})
		}
		return b, true
	}
	return Block{}, false
}

// metrics pulls the bold numbers out of the prose: "**$839** nonstop both
// ways", "**3 of 3** verified". At most four; none if the result is a table
// already (its numbers live there).
func metrics(markdown string) Block {
	b := Block{Kind: "metrics"}
	seen := map[string]bool{}
	for _, raw := range strings.Split(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "|") || strings.HasPrefix(line, "#") {
			continue
		}
		for _, m := range boldNumRe.FindAllStringSubmatchIndex(line, -1) {
			value := strings.TrimSpace(line[m[2]:m[3]])
			if len(value) > 20 || seen[value] || !metricValRe.MatchString(value) {
				continue
			}
			// Label: the words just before, or just after, the number.
			before := strings.TrimSpace(stripInline(line[:m[0]]))
			after := strings.TrimSpace(stripInline(line[m[1]:]))
			label := lastWords(before, 4)
			if label == "" || strings.HasSuffix(before, ":") {
				label = firstWords(after, 4)
			}
			label = strings.Trim(label, " ,:;—–-()")
			if label == "" {
				continue
			}
			seen[value] = true
			b.Items = append(b.Items, Item{Label: clip(label, 32), Value: value, Hint: clip(firstWords(after, 6), 40)})
			if len(b.Items) == 4 {
				return b
			}
		}
	}
	return b
}

// ── helpers ─────────────────────────────────────────────────────────────

func isTableRow(l string) bool { return strings.HasPrefix(l, "|") && strings.Count(l, "|") >= 2 }

// A separator row, or the redaction marker the gateway used to put in its
// place (#197): either way the row above is the header.
func isTableSeparator(l string) bool {
	if redactedRe.MatchString(l) {
		return true
	}
	if !strings.HasPrefix(l, "|") {
		return false
	}
	t := strings.Trim(l, "| ")
	return t != "" && strings.Trim(t, "-:| ") == ""
}

func cells(l string) []string {
	parts := strings.Split(strings.Trim(l, "|"), "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func pad(r []string, n int) []string {
	for len(r) < n {
		r = append(r, "")
	}
	return r[:n]
}

func isIndexColumn(name string, rows [][]string, c int) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n != "#" && n != "no" && n != "no." && n != "rank" && n != "option" {
		return false
	}
	for i, r := range rows {
		if strings.TrimSpace(r[c]) != strconv.Itoa(i+1) {
			return false
		}
	}
	return true
}

// number parses money, percentages, degrees, plain numbers and h/m durations
// (as minutes).
func number(s string) (float64, bool) {
	s = strings.TrimSpace(stripInline(s))
	if s == "" {
		return 0, false
	}
	if m := durRe.FindStringSubmatch(s); m != nil && (m[1] != "" || m[2] != "") {
		h, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		return float64(h*60 + mm), true
	}
	if !numRe.MatchString(s) {
		return 0, false
	}
	t := strings.NewReplacer("$", "", ",", "", "%", "", "°", "", "~", "", "≈", "", " ", "", "C", "", "F", "").Replace(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(t, "k"), strings.HasSuffix(t, "K"):
		mult, t = 1e3, t[:len(t)-1]
	case strings.HasSuffix(t, "m"), strings.HasSuffix(t, "M"):
		mult, t = 1e6, t[:len(t)-1]
	case strings.HasSuffix(t, "b"), strings.HasSuffix(t, "B"):
		mult, t = 1e9, t[:len(t)-1]
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return v * mult, true
}

func stripInline(s string) string {
	return strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

func lastWords(s string, n int) string {
	w := strings.Fields(s)
	if len(w) > n {
		w = w[len(w)-n:]
	}
	return strings.Join(w, " ")
}

func firstWords(s string, n int) string {
	w := strings.Fields(s)
	if len(w) > n {
		w = w[:n]
	}
	return strings.Join(w, " ")
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func indexOf(xs []string, x string) int {
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return -1
}

var _ = time.Now // reserved for date parsing in a later cut
