package runtime

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/soulacy/soulacy/pkg/message"
)

// A run that reads many pages overflows the model's window, and compaction
// then cuts each older page snapshot to a few hundred characters. The model
// forgets what the page said and opens it again; one shopping question opened
// the same three pages about twenty times each (#403). Opening a managed
// browser is the expensive part, so after a page has really been opened
// pageOpensPerRun times in a run, later requests for it are answered from the
// text of the last visit instead.
const (
	pageOpensPerRun   = 2
	pageTextKeepRunes = 8000
	// Past this many answers from memory the text is no longer repeated: the
	// model has had it several times and resending it only refills the window.
	pageRecallsWithText = 3
)

type pageVisit struct {
	opens, recalls int
	title, text    string
}

// pageKey treats trivially different spellings of one address as one page.
func pageKey(raw string) string {
	u := strings.TrimSpace(raw)
	if i := strings.Index(u, "#"); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(strings.TrimRight(u, "/"))
}

func pageOpenURL(call message.ToolCall) (string, bool) {
	if normalizeToolCallName(call.Name) != "start_website_action" {
		return "", false
	}
	raw, _ := call.Arguments["url"].(string)
	key := pageKey(raw)
	return key, key != ""
}

// recallPage returns the answer to give instead of opening the page again, or
// false when the page should really be opened.
func (g *progressGuard) recallPage(call message.ToolCall) (string, bool) {
	key, ok := pageOpenURL(call)
	if g == nil || !ok {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v := g.pages[key]
	if v == nil || v.opens < pageOpensPerRun {
		return "", false
	}
	v.recalls++
	out := map[string]any{
		"status": "already_read",
		"url":    call.Arguments["url"],
		"message": "This page was already opened " + strconv.Itoa(v.opens) + " times in this run, so it was not opened again. " +
			"No browser session is open: do not call close_website_action or inspect_website_action for it. " +
			"Use the page text below, and write the answer from what you have gathered instead of reopening pages.",
	}
	if v.recalls > pageRecallsWithText {
		out["message"] = "This page was already read and its text was already returned to you several times in this run. " +
			"It will not be opened or repeated again. No browser session is open. Write the answer now from what you have gathered."
	} else {
		out["title"], out["page_text"] = v.title, v.text
	}
	b, _ := json.Marshal(out)
	return string(b), true
}

// rememberPage records a real page open and keeps its readable text.
func (g *progressGuard) rememberPage(call message.ToolCall, result string, failed bool) {
	key, ok := pageOpenURL(call)
	if g == nil || !ok || failed {
		return
	}
	var parsed struct {
		Observation struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"observation"`
	}
	_ = json.Unmarshal([]byte(result), &parsed)
	text := []rune(parsed.Observation.Text)
	if len(text) > pageTextKeepRunes {
		text = append(text[:pageTextKeepRunes], []rune(" …[rest of page not kept]")...)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pages == nil {
		g.pages = map[string]*pageVisit{}
	}
	v := g.pages[key]
	if v == nil {
		v = &pageVisit{}
		g.pages[key] = v
	}
	v.opens++
	if len(text) > 0 || v.text == "" {
		v.title, v.text = parsed.Observation.Title, string(text)
	}
}
