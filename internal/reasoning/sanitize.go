package reasoning

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// This guard prevents the reasoning loop's internal control JSON (the
// thought/action/is_done step object, or a raw Reflect body) from ever
// surfacing as the user-facing reply. It happens when a model — often a smaller
// or local one — doesn't emit a clean final answer and the backend falls back to
// returning its raw output. SanitizeFinalOutput turns such output into a real
// answer (extracted from the control JSON when possible) or a graceful message,
// keeping the raw trace in the Thinking panel where it belongs.

// controlJSONShape captures the fields the loop uses internally. If a purported
// final answer decodes into this shape, it's leaked control JSON, not an answer.
type controlJSONShape struct {
	Thought     string          `json:"thought"`
	IsDone      *bool           `json:"is_done"`
	Action      json.RawMessage `json:"action"`
	FinalAnswer string          `json:"final_answer"`
	Output      string          `json:"output"`
	Reply       string          `json:"reply"`
	Answer      string          `json:"answer"`
	Message     string          `json:"message"`
	Text        string          `json:"text"`
}

// SanitizeFinalOutput returns a clean, user-facing answer for a reasoning run.
// If output is a real answer it is returned unchanged. If it is leaked control
// JSON, the embedded final_answer/output is extracted; failing that, a graceful
// message is returned. Empty output also yields the graceful message.
func SanitizeFinalOutput(output string, steps []Step) string {
	return sanitizeFinalOutput(output, steps, true)
}

// SanitizeControlOutput removes leaked ReAct/control JSON while preserving
// ordinary JSON answer payloads. Use it for agents with an explicit
// output_schema, where {"answer":"..."} or {"output":"..."} may be the
// intentional user-facing contract.
func SanitizeControlOutput(output string, steps []Step) string {
	return sanitizeFinalOutput(output, steps, false)
}

func sanitizeFinalOutput(output string, steps []Step, unwrapAnswerEnvelope bool) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return gracefulFallback(steps)
	}
	if isPlaceholderFinalAnswer(trimmed) {
		return gracefulFallback(steps)
	}

	body := stripJSONFence(trimmed)
	if looksLikePendingAsyncPayload(body) {
		return asyncIncompleteFallback(steps)
	}
	// Fast path: the whole reply is the envelope.
	if ans, ok := unwrapEnvelope(body, steps, unwrapAnswerEnvelope); ok {
		return usableOrFallback(ans, steps)
	}
	// The model often appends prose AFTER the JSON object (or wraps the envelope
	// in surrounding text), so the whole-string check above fails and the raw
	// `{"thought":…,"final_answer":…}` leaked to the user. Extract the FIRST
	// balanced {…} object and try to unwrap just that.
	if obj := firstJSONObject(body); obj != "" && obj != body {
		if ans, ok := unwrapEnvelope(obj, steps, unwrapAnswerEnvelope); ok {
			return usableOrFallback(ans, steps)
		}
	}
	// Last-resort boundary guard: some providers return an object with leading
	// provider metadata before the actual ReAct control object, or malformed
	// control JSON that still contains a valid JSON string final_answer. Scan
	// every balanced object and then try a tolerant field extraction so
	// `{"thought":...,"is_done":true,"final_answer":"..."}` never reaches chat.
	for _, obj := range jsonObjects(body) {
		if ans, ok := unwrapEnvelope(obj, steps, unwrapAnswerEnvelope); ok {
			return usableOrFallback(ans, steps)
		}
	}
	if ans, ok := tolerantControlAnswer(body); ok {
		return usableOrFallback(ans, steps)
	}
	if looksLikeSanitizerControlPayload(body) {
		return gracefulFallback(steps)
	}
	return output
}

func usableOrFallback(answer string, steps []Step) string {
	if isPlaceholderFinalAnswer(answer) {
		return gracefulFallback(steps)
	}
	return strings.TrimSpace(answer)
}

func asyncIncompleteFallback(steps []Step) string {
	completed := 0
	for _, step := range steps {
		if step.Obs.Source == "controller" || strings.TrimSpace(step.Action.Tool) == "" || isToolFailure(step.Obs) {
			continue
		}
		completed++
	}
	if completed > 0 {
		return fmt.Sprintf("The workflow started an async artifact job, but the final artifact was still processing when the run ended. I did not publish the raw status payload as the final answer. Open the run trace to inspect the job status, or rerun after it finishes. Completed tool steps: %d.", completed)
	}
	return "The workflow started an async artifact job, but the final artifact was still processing when the run ended. I did not publish the raw status payload as the final answer. Open the run trace to inspect the job status, or rerun after it finishes."
}

// unwrapEnvelope pulls the real answer out of a leaked control-JSON step or a
// human-answer envelope. The bool reports whether body was recognised as either
// (so the caller knows not to surface it verbatim); when it is control JSON with
// no usable answer, a graceful fallback is returned.
func unwrapEnvelope(body string, steps []Step, unwrapAnswerEnvelope bool) (string, bool) {
	if shape, ok := decodeControlJSON(body); ok {
		if ans := firstNonEmpty(shape.FinalAnswer, shape.Output, shape.Reply, shape.Answer, shape.Message, shape.Text); strings.TrimSpace(ans) != "" {
			return strings.TrimSpace(ans), true
		}
		return gracefulFallback(steps), true
	}
	if unwrapAnswerEnvelope {
		if ans, ok := decodeAnswerEnvelope(body); ok {
			return strings.TrimSpace(ans), true
		}
	}
	return "", false
}

// firstJSONObject returns the first top-level, brace-balanced {…} object in s
// (respecting string literals and escapes), or "" if there is none. It lets the
// sanitizer recover a leaked envelope even when the model wrapped it in prose.
func firstJSONObject(s string) string {
	objs := jsonObjects(s)
	if len(objs) == 0 {
		return ""
	}
	return objs[0]
}

// jsonObjects returns each top-level, brace-balanced {…} object in s
// (respecting string literals and escapes). It is intentionally small and
// dependency-free because it runs on every final answer boundary.
func jsonObjects(s string) []string {
	var out []string
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				out = append(out, s[start:i+1])
			}
		}
	}
	return out
}

// decodeControlJSON reports whether s is a JSON object carrying the loop's
// control fields (thought/action/is_done) — i.e. leaked internal state rather
// than an answer. A bare {"output": "..."} or plain prose is NOT control JSON.
func decodeControlJSON(s string) (controlJSONShape, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return controlJSONShape{}, false
	}
	var shape controlJSONShape
	if err := json.Unmarshal([]byte(s), &shape); err != nil {
		if len(jsonObjects(s)) > 1 {
			return controlJSONShape{}, false
		}
		// Malformed JSON that still smells like control output (common when a
		// model appends prose) — sniff for the tell-tale keys so we don't render
		// it verbatim.
		if looksLikeSanitizerControlPayload(s) {
			return controlJSONShape{}, true
		}
		return controlJSONShape{}, false
	}
	isControl := shape.Thought != "" || shape.IsDone != nil || len(shape.Action) > 0
	return shape, isControl
}

// decodeAnswerEnvelope unwraps a model's final-answer envelope, e.g.
// {"output":"## Markdown..."} or {"reply":"Done"}. These are not tool/control
// payloads; they are human answers accidentally wrapped as JSON by a provider
// or prompt. Arbitrary JSON data remains untouched.
func decodeAnswerEnvelope(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return "", false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "", false
	}
	answerKeys := []string{"final_answer", "output", "reply", "answer", "message", "text"}
	foundKey := ""
	for _, key := range answerKeys {
		if _, ok := obj[key]; ok {
			foundKey = key
			break
		}
	}
	if foundKey == "" {
		return "", false
	}
	allowed := map[string]bool{
		foundKey:        true,
		"updated_rules": true,
		"confidence":    true,
		"confident":     true,
		"metadata":      true,
	}
	for key := range obj {
		if !allowed[key] {
			return "", false
		}
	}
	var ans string
	if err := json.Unmarshal(obj[foundKey], &ans); err != nil {
		return "", false
	}
	return ans, strings.TrimSpace(ans) != ""
}

func looksLikeSanitizerControlPayload(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, `"thought"`) &&
		(strings.Contains(low, `"action"`) || strings.Contains(low, `"is_done"`) || strings.Contains(low, `"final_answer"`))
}

func looksLikePendingAsyncPayload(s string) bool {
	s = strings.TrimSpace(s)
	if !(strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")) {
		return false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return false
	}
	return containsPendingAsyncSignal(v)
}

func containsPendingAsyncSignal(v any) bool {
	pendingStatus := map[string]bool{
		"in_progress": true, "inprogress": true, "in-progress": true,
		"pending": true, "running": true, "processing": true,
		"generating": true, "queued": true, "working": true,
		"started": true, "not_ready": true, "notready": true, "waiting": true,
	}
	pendingCounters := map[string]bool{
		"in_progress": true, "inprogress": true, "pending": true,
		"processing": true, "running": true, "queued": true,
	}

	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			key := strings.ToLower(strings.TrimSpace(k))
			if key == "status" || key == "state" || key == "phase" {
				if s, ok := val.(string); ok && pendingStatus[strings.ToLower(strings.TrimSpace(s))] {
					return true
				}
			}
			if pendingCounters[key] {
				if n, ok := jsonNumberToFloat(val); ok && n > 0 {
					return true
				}
			}
			if containsPendingAsyncSignal(val) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if containsPendingAsyncSignal(item) {
				return true
			}
		}
	}
	return false
}

func jsonNumberToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// tolerantControlAnswer extracts a JSON-string final_answer/output/etc from a
// malformed or prose-wrapped control payload. It does not parse arbitrary JSON;
// it only decodes the string value for known answer fields after control keys
// are present, so legitimate JSON answers remain untouched.
func tolerantControlAnswer(s string) (string, bool) {
	if !looksLikeSanitizerControlPayload(s) {
		return "", false
	}
	for _, key := range []string{"final_answer", "output", "reply", "answer", "message", "text"} {
		if ans, ok := extractJSONStringField(s, key); ok && strings.TrimSpace(ans) != "" {
			return strings.TrimSpace(ans), true
		}
	}
	return "", false
}

func extractJSONStringField(s, key string) (string, bool) {
	needle := `"` + key + `"`
	idx := strings.Index(s, needle)
	if idx < 0 {
		return "", false
	}
	i := idx + len(needle)
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
		i++
	}
	if i >= len(s) || s[i] != ':' {
		return "", false
	}
	i++
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
		i++
	}
	if i >= len(s) || s[i] != '"' {
		return "", false
	}
	start := i
	i++
	esc := false
	for ; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			raw := s[start : i+1]
			var decoded string
			if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
				return decoded, true
			}
			return "", false
		}
	}
	return "", false
}

// gracefulFallback is what the user sees when the loop finished without
// producing an answer. It shows the most recent readable observation, LABELLED
// as an unfinished fragment — never dressed up as the reply.
//
// The label is the whole point. Asked to advise on replacing a three-stock
// portfolio, an agent spent all 26 of its reasoning steps gathering data — the
// three quotes, a correlation matrix, screens, fundamentals for nine candidate
// replacements — hit its step ceiling before writing anything, and this
// function handed back the last observation verbatim:
//
//	ticker: CVX; status: success; current_price: 194.91; outlook: strongly bullish
//
// One line, about a ticker the user never mentioned, presented as the answer to
// their question. Nothing distinguished it from a real reply: not the wording,
// not the shape, not the run status. Showing the fragment is defensible — it is
// evidence, and binning it helps nobody. Showing it SILENTLY is not, because the
// reader cannot tell an answer from a leftover.
func gracefulFallback(steps []Step) string {
	if obs := lastObservationDisplay(steps, false); obs != "" {
		return unfinishedAnswer(obs, steps)
	}
	if obs := lastObservationDisplay(steps, true); obs != "" {
		return unfinishedAnswer(obs, steps)
	}
	for i := len(steps) - 1; i >= 0; i-- {
		content := strings.TrimSpace(steps[i].Obs.Content)
		if content == "" || isInstructionalObservation(steps[i]) {
			continue
		}
		if looksReadable(content) {
			return unfinishedAnswer(content, steps)
		}
	}
	return "I worked through several reasoning steps but couldn't produce a clean final answer. Open the reasoning trace above to see what happened, or ask me to continue."
}

// UnfinishedAnswerPrefix marks a reply that is a fragment of the agent's working
// rather than an answer. Exported so a caller can detect one without matching
// prose — delivery, tests and the run ledger all need to tell the two apart.
const UnfinishedAnswerPrefix = "I ran out of reasoning steps"

// unfinishedAnswer frames a leftover observation as what it is.
//
// It names the step budget because that is the one thing the reader can act on,
// and it is almost always the reason: a loop that ends holding data but no
// answer has usually spent its steps fetching rather than concluding.
func unfinishedAnswer(fragment string, steps []Step) string {
	return UnfinishedAnswerPrefix + " (" + strconv.Itoa(len(steps)) +
		" used) before I could write the answer, so this is unfinished. " +
		"Below is the last thing I retrieved — a fragment of my working, not a reply to your question. " +
		"Ask me to continue, or raise this agent's step budget.\n\n---\n\n" + strings.TrimSpace(fragment)
}

func isPlaceholderFinalAnswer(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "answer", "final_answer", "output", "reply", "message", "text",
		"user-facing answer", "completed answer", "write the answer":
		return true
	default:
		return false
	}
}

// looksReadable rejects observations that are themselves control JSON, HTML, or
// otherwise unsuitable to show as a final answer.
func looksReadable(s string) bool {
	t := strings.TrimSpace(s)
	if _, isControl := decodeControlJSON(s); isControl {
		return false
	}
	if len(s) > 4000 {
		return false
	}
	if strings.HasPrefix(t, "<") && strings.Contains(s, "</") {
		return false // looks like HTML
	}
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return false // raw structured tool output (e.g. a web_search JSON blob)
	}
	if looksLikeSensitiveFile(t) {
		return false // never surface a credential/cookie file as the reply
	}
	return true
}

// stripJSONFence removes a surrounding ```json … ``` (or bare ``` … ```) fence.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		// drop an optional language tag on the first line (e.g. "json")
		if lang := strings.TrimSpace(s[:i]); lang == "" || !strings.ContainsAny(lang, " {}\"") {
			s = s[i+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
