package runtime

// flowports.go enforces the DECLARED side of typed ports (sdk/reasoning
// FlowPort.Type) at the producer. Ports carried type hints since Story S0.3
// but nothing ever checked them, so output-shape drift was only discovered
// when a downstream consumer blew up — one node too late for a useful
// diagnosis. After a node succeeds, the runtime compares each declared output
// port's type hint against the value actually produced; a mismatch is shape
// drift caught where the context is freshest. Enforcement is deliberately
// soft: a mismatch triggers the node's (bounded) adaptive reshape when the
// flow is adaptive, and otherwise only emits a flow.portdrift event — the run
// itself keeps today's forgiving behavior and never fails on a hint.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// flowPortTypeMismatch reports the FIRST declared output port whose type hint
// does not match the node's produced value, as a human-readable description —
// or "" when everything matches (or nothing is checkable: no declared ports,
// no typed hints, or non-JSON output). Port value resolution mirrors the
// walker's wired-port semantics: an explicit Field is a dotted path into the
// output; otherwise a port name addresses the output's same-named field when
// the output is an object that has it, and the whole output when not.
func flowPortTypeMismatch(node sdkr.FlowNode, out json.RawMessage) string {
	if len(out) == 0 || len(node.Outputs) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(out, &v) != nil {
		return ""
	}
	for _, p := range node.Outputs {
		want := normalizeFlowPortType(p.Type)
		if want == "" {
			continue
		}
		got := flowJSONTypeName(flowPortValue(v, p))
		if got != want {
			return fmt.Sprintf("port %q declares type %q but the produced value is %s", p.Name, p.Type, got)
		}
	}
	return ""
}

// flowPortValue resolves which value an output port exposes, mirroring the
// walker's resolvePortInputs semantics (reasoning/flow.go): explicit Field =
// dotted path (an unwalkable segment yields null — that IS the drift), port
// name = same-named field if present, else the whole output.
func flowPortValue(v any, p sdkr.FlowPort) any {
	if strings.TrimSpace(p.Field) != "" {
		cur := v
		for _, seg := range strings.Split(p.Field, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[seg]
		}
		return cur
	}
	if m, ok := v.(map[string]any); ok {
		if val, present := m[p.Name]; present {
			return val
		}
	}
	return v
}

// normalizeFlowPortType maps the type spellings authors and builder models
// actually write onto JSON type names. Unknown spellings and the deliberately
// unchecked hints ("json", "any") return "" — no enforcement — so a creative
// type label can never fail a run.
func normalizeFlowPortType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "string", "str", "text":
		return "string"
	case "number", "int", "integer", "float", "double":
		return "number"
	case "bool", "boolean":
		return "boolean"
	case "object", "map", "dict":
		return "object"
	case "array", "list", "string[]", "number[]", "object[]", "[]string":
		return "array"
	default:
		return ""
	}
}

// flowJSONTypeName names a decoded JSON value's type in JSON vocabulary.
func flowJSONTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// emitFlowPortDrift surfaces a detected output-port type mismatch as a
// flow.portdrift event, so Studio's trace and the action log can show WHERE a
// shape drifted even when the run keeps going.
func (e *Engine) emitFlowPortDrift(msg message.Message, node sdkr.FlowNode, drift string) {
	if e.sink == nil {
		return
	}
	e.sink.Emit(message.Event{
		Type:      "flow.portdrift",
		AgentID:   msg.AgentID,
		SessionID: msg.SessionID,
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"node":  node.ID,
			"drift": drift,
		},
	})
}
