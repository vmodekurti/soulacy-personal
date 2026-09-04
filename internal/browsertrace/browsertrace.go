// Package browsertrace reconstructs a readable, step-by-step trace of an agent's
// browser/computer automation from the action log. Browser work happens through
// the Playwright (or similar) MCP sidecar, so every navigate/click/type/extract/
// screenshot is already recorded as a tool.call/tool.result event — this package
// distills those into an ordered trace the GUI can replay, plus the last known
// URL and screenshot reference. It is a pure read-only aggregation.
//
// Per-domain approval for browser navigation is enforced separately by the tool
// policy engine (internal/policy): MCP tools are classified as "network" and a
// navigate whose url is off the allow-list (or on the deny-list) is blocked
// before it runs. This package is the observability half of that story.
package browsertrace

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/pkg/message"
)

// Step is one browser action in the trace.
type Step struct {
	Seq           int    `json:"seq"`
	Tool          string `json:"tool"`
	Action        string `json:"action"`                   // normalized verb: navigate|click|type|extract|screenshot|other
	URL           string `json:"url,omitempty"`            // target url when the step carried one
	Target        string `json:"target,omitempty"`         // selector / element / text argument
	Output        string `json:"output,omitempty"`         // short tool observation/result preview
	Screenshot    string `json:"screenshot,omitempty"`     // resource ref/path/URL if this step captured one
	ScreenshotURL string `json:"screenshot_url,omitempty"` // authenticated local download URL when gateway can serve the ref
	IsError       bool   `json:"is_error,omitempty"`
	At            string `json:"at,omitempty"`
}

// Trace is the reconstructed browser session.
type Trace struct {
	AgentID     string `json:"agent_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Steps       []Step `json:"steps"`
	LastURL     string `json:"last_url,omitempty"`
	Navigations int    `json:"navigations"`
	Screenshot  string `json:"last_screenshot,omitempty"` // resource ref/URL if one was captured
}

// isBrowserTool reports whether a tool name belongs to a browser/computer
// automation sidecar. Tolerant of naming across MCP servers.
func isBrowserTool(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "mcp__") && (strings.Contains(n, "browser") || strings.Contains(n, "playwright") || strings.Contains(n, "puppeteer") || strings.Contains(n, "computer")) {
		return true
	}
	for _, kw := range []string{"navigate", "goto", "browser_", "screenshot", "page_"} {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

func actionOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "navigate") || strings.Contains(n, "goto"):
		return "navigate"
	case strings.Contains(n, "screenshot") || strings.Contains(n, "capture"):
		return "screenshot"
	case strings.Contains(n, "click"):
		return "click"
	case strings.Contains(n, "type") || strings.Contains(n, "fill") || strings.Contains(n, "input"):
		return "type"
	case strings.Contains(n, "extract") || strings.Contains(n, "read") || strings.Contains(n, "get_text") || strings.Contains(n, "content"):
		return "extract"
	default:
		return "other"
	}
}

// Build reconstructs the browser trace for one (agentID, sessionID) from events.
// An empty sessionID matches any session in the supplied events.
func Build(agentID, sessionID string, events []message.Event) Trace {
	tr := Trace{AgentID: agentID, SessionID: sessionID, Steps: []Step{}}

	// Track error state by call id so a failing tool.result marks its step.
	errByCall := map[string]bool{}
	contentByCall := map[string]string{}
	for _, ev := range events {
		if ev.Type == "tool.result" {
			if id, content, isErr, ok := resultInfo(ev.Payload); ok {
				if isErr {
					errByCall[id] = true
				}
				if strings.TrimSpace(content) != "" {
					contentByCall[id] = content
				}
			}
		}
	}

	seq := 0
	for _, ev := range events {
		if agentID != "" && ev.AgentID != agentID {
			continue
		}
		if sessionID != "" && ev.SessionID != sessionID {
			continue
		}
		if ev.Type != "tool.call" {
			continue
		}
		name, id, args := callInfo(ev.Payload)
		if name == "" || !isBrowserTool(name) {
			continue
		}
		seq++
		st := Step{Seq: seq, Tool: name, Action: actionOf(name)}
		if !ev.Timestamp.IsZero() {
			st.At = ev.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		st.URL = firstString(args, "url", "uri", "href", "address")
		st.Target = firstString(args, "selector", "element", "text", "ref", "query")
		if id != "" && errByCall[id] {
			st.IsError = true
		}
		if content := contentByCall[id]; content != "" {
			st.Output = previewContent(content, 700)
			if st.Action == "screenshot" {
				st.Screenshot = screenshotRef(content)
			}
		}
		if st.Action == "navigate" && st.URL != "" {
			tr.Navigations++
			tr.LastURL = st.URL
		}
		if st.Action == "screenshot" {
			if ref := firstString(args, "path", "filename", "name", "url"); ref != "" {
				st.Screenshot = ref
				tr.Screenshot = ref
			} else if st.Screenshot != "" {
				tr.Screenshot = st.Screenshot
			}
		}
		tr.Steps = append(tr.Steps, st)
	}

	sort.SliceStable(tr.Steps, func(i, j int) bool { return tr.Steps[i].Seq < tr.Steps[j].Seq })
	return tr
}

func callInfo(payload any) (name, id string, args map[string]any) {
	if tc, ok := payload.(message.ToolCall); ok {
		return tc.Name, tc.ID, tc.Arguments
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return "", "", nil
	}
	name, _ = m["name"].(string)
	id, _ = m["id"].(string)
	args, _ = m["arguments"].(map[string]any)
	return name, id, args
}

func resultInfo(payload any) (callID, content string, isErr bool, ok bool) {
	if tr, o := payload.(message.ToolResult); o {
		return tr.CallID, tr.Content, tr.IsError, true
	}
	m, o := payload.(map[string]any)
	if !o {
		return "", "", false, false
	}
	callID, _ = m["call_id"].(string)
	content, _ = m["content"].(string)
	isErr, _ = m["is_error"].(bool)
	return callID, content, isErr, true
}

func firstString(args map[string]any, keys ...string) string {
	if args == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := args[k].(string); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

func previewContent(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		if ref := firstNestedString(v, "text", "content", "title", "url", "path", "filename", "error", "message"); ref != "" {
			s = ref
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func screenshotRef(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return firstNestedString(v, "screenshot", "image", "image_url", "url", "path", "filename", "file")
	}
	if strings.HasPrefix(s, "data:image/") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return ""
}

func firstNestedString(v any, keys ...string) string {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range keys {
			if s, ok := t[k].(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					return s
				}
			}
		}
		for _, child := range t {
			if s := firstNestedString(child, keys...); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range t {
			if s := firstNestedString(child, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}
