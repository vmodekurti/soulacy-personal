// flowstrategy.go — Story E25: the "flow" reasoning strategy.
//
// Registered with the E15 registry so SOUL.yaml's reasoning.strategy: flow
// routes tasks through the agent's declarative graph (Config.Flow). Node
// actions are bridged onto env.Tools — the same policy surface as every
// other strategy. The runtime's workflow path uses RunFlow directly with
// checkpoint hooks; this strategy is the chat-triggered, hook-free route.
package reasoning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

func init() {
	registry.MustRegisterReasoningStrategy("flow", func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return flowStrategy{}, nil
	})
}

type flowStrategy struct{}

// Run executes Config.Flow over env.Tools. Per the Strategy contract it
// always returns a usable ReflectResponse — graph errors become degraded
// output, never panics.
func (flowStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	if env.Config.Flow == nil {
		return nil, ReflectResponse{Output: "flow strategy selected but the agent declares no flow graph (workflow.nodes)"}
	}
	g, err := CompileFlow(*env.Config.Flow)
	if err != nil {
		return nil, ReflectResponse{Output: "flow graph invalid: " + err.Error()}
	}

	var steps []Step
	runNode := func(ctx context.Context, node sdkreasoning.FlowNode, renderedInput string) (json.RawMessage, error) {
		toolName := node.Tool
		if node.Kind == sdkreasoning.FlowNodeAgent {
			toolName = "agent__" + node.Agent
		}
		start := time.Now()
		obs := env.Tools.Execute(ctx, ToolCall{
			Tool:  toolName,
			Input: flowCallInput(renderedInput),
		})
		step := Step{
			ID:       node.ID,
			Thought:  fmt.Sprintf("flow node %q", node.ID),
			Action:   ToolCall{Tool: toolName, Input: flowCallInput(renderedInput)},
			Obs:      obs,
			Duration: time.Since(start),
		}
		steps = append(steps, step)
		if obs.Error != nil {
			return nil, obs.Error
		}
		return json.RawMessage(flowResultJSON(obs.Content)), nil
	}

	vars := map[string]any{"trigger": taskInput}
	out, err := RunFlow(ctx, g, vars, runNode, FlowHooks{})
	if err != nil {
		return steps, ReflectResponse{Output: "flow aborted: " + err.Error()}
	}
	return steps, ReflectResponse{Output: flowOutputString(out)}
}

// flowCallInput shapes a rendered input for ToolCall.Input. JSON objects
// with scalar values map onto the args map; everything else travels under
// the "input" key.
func flowCallInput(rendered string) map[string]string {
	if rendered == "" {
		return map[string]string{}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(rendered), &obj); err == nil {
		out := make(map[string]string, len(obj))
		for k, v := range obj {
			switch t := v.(type) {
			case string:
				out[k] = t
			default:
				b, _ := json.Marshal(v)
				out[k] = string(b)
			}
		}
		return out
	}
	return map[string]string{"input": rendered}
}

// flowResultJSON wraps tool output as JSON: pass-through when it already
// is JSON, quoted string otherwise.
func flowResultJSON(content string) string {
	if json.Valid([]byte(content)) && content != "" {
		return content
	}
	b, _ := json.Marshal(content)
	return string(b)
}

// flowOutputString renders the final node result for the reply.
func flowOutputString(out json.RawMessage) string {
	if out == nil {
		return "(flow completed with no output)"
	}
	var s string
	if err := json.Unmarshal(out, &s); err == nil {
		return s
	}
	return string(out)
}

// flowTemplateFuncs are available inside node Input / edge If templates. toJson
// (alias json) is the important one: flow vars hold PARSED JSON (Go maps and
// slices), and Go's default {{ .x }} prints those in Go syntax (map[...]), which
// is NOT valid JSON. {{ toJson .x }} emits real JSON so a python node's stdin
// (or any JSON-consuming tool) receives well-formed input.
var flowTemplateFuncs = template.FuncMap{
	"toJson": toJSONString,
	"json":   toJSONString,
	// fromJson is the inverse models constantly reach for (the exact
	// "function \"fromJson\" not defined" blocker seen on generated flows). Flow
	// vars are already PARSED JSON, so this parses a JSON *string* into a value
	// (or passes a structured value through) and re-emits valid JSON — so a
	// template using either toJson or fromJson always renders well-formed JSON.
	"fromJson":  tmplFromJSON,
	"fromjson":  tmplFromJSON,
	"parseJson": tmplFromJSON,
	"parseJSON": tmplFromJSON,
	"unmarshal": tmplFromJSON,
	// Curated helpers models commonly reach for (Sprig-style). Having them
	// registered means a reasonable generated template (e.g. {{ pluck "url"
	// .articles }}) works instead of failing to parse with "function X not
	// defined". They operate on the PARSED JSON flow vars (maps/slices/any).
	"default": tmplDefault,
	"join":    tmplJoin,
	"first":   tmplFirst,
	"last":    tmplLast,
	"pluck":   tmplPluck,
	"dict":    tmplDict,
	// Time helpers models reach for constantly (the #1 "function not defined"
	// blocker we saw was {{ now }}). All read the wall clock at render time and
	// emit strings safe to drop into a JSON tool input.
	"now":     tmplNow,     // RFC3339 timestamp, e.g. 2026-06-23T07:00:00Z
	"today":   tmplToday,   // date only, e.g. 2026-06-23
	"nowUnix": tmplNowUnix, // unix seconds (int)
	// Date formatting — registered under EVERY common name a model reaches for
	// (dateFmt/dateFormat/formatDate/date) so a reasonable template never fails
	// with "function X not defined" over a spelling choice. All accept a layout in
	// Go reference, YYYY-MM-DD, or %Y-%m-%d style, plus an optional time arg.
	"dateFmt":    tmplDateFormat,
	"dateFormat": tmplDateFormat,
	"formatDate": tmplDateFormat,
	"date":       tmplDateFormat,
}

// tmplNow returns the current UTC time as an RFC3339 string. Registered as
// {{ now }} — the single most common helper models emit and the exact blocker
// ("function \"now\" not defined") seen on generated NotebookLM workflows.
func tmplNow() string { return time.Now().UTC().Format(time.RFC3339) }

// tmplToday returns the current UTC date as YYYY-MM-DD ({{ today }}).
func tmplToday() string { return time.Now().UTC().Format("2006-01-02") }

// tmplNowUnix returns the current unix time in seconds ({{ nowUnix }}).
func tmplNowUnix() int64 { return time.Now().Unix() }

// tmplDateFormat formats a time with a layout, defaulting to now (UTC). The
// layout may be Go reference ("2006-01-02"), token style ("YYYY-MM-DD"), or
// strftime ("%Y-%m-%d") — all normalized. An optional second arg supplies the
// time to format (a time.Time, an RFC3339/date string, or a unix number);
// absent or unparseable, it formats the current time. Never errors — a bad
// layout just formats with what it has — so a generated template always renders.
func tmplDateFormat(layout string, t ...any) string {
	lay := normalizeDateLayout(layout)
	base := time.Now().UTC()
	if len(t) > 0 {
		if parsed, ok := coerceTime(t[0]); ok {
			base = parsed
		}
	}
	return base.Format(lay)
}

// dateLayoutReplacer translates the common token/strftime date layouts models
// emit into Go's reference layout. Longer tokens are listed first so they win.
var dateLayoutReplacer = strings.NewReplacer(
	"YYYY", "2006", "YY", "06",
	"MMMM", "January", "MMM", "Jan", "MM", "01",
	"DDDD", "Monday", "DDD", "Mon", "DD", "02",
	"HH", "15", "hh", "03",
	"mm", "04", "ss", "05", "A", "PM",
	"%Y", "2006", "%m", "01", "%d", "02",
	"%H", "15", "%M", "04", "%S", "05",
)

// normalizeDateLayout converts a date layout to Go's reference form. An empty
// layout becomes RFC3339; a Go-reference layout passes through unchanged.
func normalizeDateLayout(layout string) string {
	if strings.TrimSpace(layout) == "" {
		return time.RFC3339
	}
	return dateLayoutReplacer.Replace(layout)
}

// coerceTime turns a parsed-JSON value into a time.Time when it can: a time.Time,
// an RFC3339 / common date string, or a unix-seconds number. ok=false otherwise.
func coerceTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case string:
		for _, f := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05", time.RFC1123} {
			if p, err := time.Parse(f, strings.TrimSpace(x)); err == nil {
				return p.UTC(), true
			}
		}
	case float64:
		return time.Unix(int64(x), 0).UTC(), true
	case int64:
		return time.Unix(x, 0).UTC(), true
	case int:
		return time.Unix(int64(x), 0).UTC(), true
	}
	return time.Time{}, false
}

// FlowTemplateFuncs exposes the template functions available inside node Input /
// edge If templates, so callers that VALIDATE templates (e.g. the Studio
// pre-save check) parse with the exact same function set the renderer uses —
// keeping "valid at save" and "renders at run" in lockstep.
func FlowTemplateFuncs() template.FuncMap { return flowTemplateFuncs }

func toJSONString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// tmplFromJSON supports {{ fromJson .x }}: if x is a JSON string it is parsed and
// re-emitted as valid JSON; an already-structured value (map/slice) is emitted
// as JSON directly. Never errors on well-formed input — the goal is that a
// template using fromJson renders valid JSON exactly like toJson does.
func tmplFromJSON(v any) (string, error) {
	if s, ok := v.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			return toJSONString(parsed)
		}
	}
	return toJSONString(v)
}

// tmplDefault returns val when it's "present" (non-nil, non-empty), else def.
// Usable as {{ default "x" .y }} or {{ .y | default "x" }}.
func tmplDefault(def, val any) any {
	switch v := val.(type) {
	case nil:
		return def
	case string:
		if strings.TrimSpace(v) == "" {
			return def
		}
	case []any:
		if len(v) == 0 {
			return def
		}
	case map[string]any:
		if len(v) == 0 {
			return def
		}
	}
	return val
}

// asSlice coerces a parsed-JSON value to []any (nil when not a list).
func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

// tmplJoin joins a list's elements with sep. Accepts either argument order —
// {{ join ", " .urls }} (Sprig) or {{ join .urls ", " }} (common slip).
func tmplJoin(a, b any) string {
	sep, parts := joinArgs(a, b)
	strs := make([]string, 0, len(parts))
	for _, p := range parts {
		strs = append(strs, fmt.Sprint(p))
	}
	return strings.Join(strs, sep)
}

// joinArgs resolves (sep, list) from two args given in either order.
func joinArgs(a, b any) (string, []any) {
	if s, ok := a.(string); ok {
		return s, asSlice(b)
	}
	if s, ok := b.(string); ok {
		return s, asSlice(a)
	}
	return "", asSlice(a)
}

func tmplFirst(v any) any {
	if s := asSlice(v); len(s) > 0 {
		return s[0]
	}
	return nil
}

func tmplLast(v any) any {
	if s := asSlice(v); len(s) > 0 {
		return s[len(s)-1]
	}
	return nil
}

// tmplPluck extracts field `key` from each map in a list. Accepts either
// argument order — {{ pluck "url" .articles }} (Sprig) or
// {{ pluck .articles "url" }} (common slip) — since both are valid syntax and
// the order is a frequent model mistake.
func tmplPluck(a, b any) []any {
	var key string
	var list []any
	if s, ok := a.(string); ok {
		key, list = s, asSlice(b)
	} else if s, ok := b.(string); ok {
		key, list = s, asSlice(a)
	} else {
		return nil
	}
	var out []any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if val, present := m[key]; present {
				out = append(out, val)
			}
		}
	}
	return out
}

// tmplDict creates a dictionary from a list of alternating keys and values.
func tmplDict(values ...any) map[string]any {
	if len(values)%2 != 0 {
		values = append(values, "")
	}
	dict := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			key = fmt.Sprintf("%v", values[i])
		}
		dict[key] = values[i+1]
	}
	return dict
}

// renderTemplate executes a Go text/template over the flow vars (same
// semantics as the workflow executor's templates).
func renderTemplate(tmplStr string, vars map[string]any) (string, error) {
	tmpl, err := template.New("").Funcs(flowTemplateFuncs).Option("missingkey=zero").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// renderNodeInputTemplate is strict because a missing value in tool arguments
// must not silently turn into JSON null. Branch predicates intentionally keep
// renderTemplate's forgiving missingkey=zero behavior: an absent optional value
// there simply means the branch is not taken.
func renderNodeInputTemplate(tmplStr string, vars map[string]any) (string, error) {
	tmpl, err := template.New("").Funcs(flowTemplateFuncs).Option("missingkey=error").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}
