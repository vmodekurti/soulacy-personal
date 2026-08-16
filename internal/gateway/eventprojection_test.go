// eventprojection_test.go — MU-026 criterion 2: internal metadata stays
// internal, and the classification cannot be skipped by adding a field.
package gateway

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
)

// THE guard. No field on message.Event today is something a permitted
// recipient must not see, so this is a boundary rather than a fix — and a
// boundary is only worth having if it cannot be walked around. The next field
// added for the authorizer's benefit (a principal, a credential ID, a policy
// snapshot) fails the build until somebody classifies it.
func TestEveryEventFieldIsClassified(t *testing.T) {
	eventType := reflect.TypeOf(message.Event{})
	for i := 0; i < eventType.NumField(); i++ {
		field := eventType.Field(i)
		if !field.IsExported() {
			continue
		}
		if _, classified := publicFields[field.Name]; !classified {
			t.Errorf("message.Event.%s is not classified in publicFields — decide whether it crosses the wire "+
				"(add it to publicEvent and project) or stays internal (map it to false)", field.Name)
		}
	}
	// And the classification cannot drift the other way: a name listed here
	// that no longer exists means the map is describing a field that is gone,
	// which is how a stale `false` starts silently protecting nothing.
	for name := range publicFields {
		if _, ok := eventType.FieldByName(name); !ok {
			t.Errorf("publicFields lists %q, which is no longer a field of message.Event", name)
		}
	}
}

// A field classified public must actually be projected, or the classification
// is documentation rather than behaviour.
func TestEveryPublicFieldSurvivesProjection(t *testing.T) {
	event := message.Event{
		Type: "tool.result", WorkspaceID: "ws_a", AgentID: "bot", SessionID: "s1",
		Payload: map[string]any{"ok": true}, Timestamp: time.Now().UTC(),
	}
	raw, err := json.Marshal(project(event))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for name, public := range publicFields {
		field, _ := reflect.TypeOf(message.Event{}).FieldByName(name)
		tag := field.Tag.Get("json")
		key := tag
		for i, r := range tag {
			if r == ',' {
				key = tag[:i]
				break
			}
		}
		if key == "" || key == "-" {
			continue
		}
		_, present := decoded[key]
		if public && !present && key != "parts" {
			t.Errorf("%s is classified public but did not survive projection", name)
		}
		if !public && present {
			t.Errorf("%s is classified internal but reached the wire as %q", name, key)
		}
	}
}

// The projection is what Emit serializes. A test that only exercised project()
// would pass while the hub marshalled the raw event beside it.
func TestTheHubBroadcastsTheProjection(t *testing.T) {
	h := NewEventHub(zap.NewNop(), nil)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	client := &wsClient{
		send:      make(chan []byte, 4),
		principal: subscriber("ws_a", "operator:alice", "operator", false),
	}
	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	h.Emit(message.Event{Type: "tool.result", WorkspaceID: "ws_a", AgentID: "bot", SessionID: "s1"})

	select {
	case raw := <-client.send:
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		for key := range decoded {
			if !isProjectedKey(key) {
				t.Errorf("the wire form carries %q, which the projection does not declare", key)
			}
		}
		if decoded["type"] != "tool.result" {
			t.Fatalf("the projection lost the event type: %v", decoded)
		}
	default:
		t.Fatal("no event was broadcast")
	}
}

// isProjectedKey reports whether a JSON key is one publicEvent declares.
func isProjectedKey(key string) bool {
	projected := reflect.TypeOf(publicEvent{})
	for i := 0; i < projected.NumField(); i++ {
		tag := projected.Field(i).Tag.Get("json")
		name := tag
		for j, r := range tag {
			if r == ',' {
				name = tag[:j]
				break
			}
		}
		if name == key {
			return true
		}
	}
	return false
}

// publicEvent must not embed message.Event: an embedded struct silently gains
// whatever is added to its parent, which is exactly what this file prevents.
func TestTheProjectionDoesNotEmbedTheInternalEvent(t *testing.T) {
	projected := reflect.TypeOf(publicEvent{})
	for i := 0; i < projected.NumField(); i++ {
		if projected.Field(i).Anonymous {
			t.Fatalf("publicEvent embeds %s — an embedded struct inherits new fields silently",
				projected.Field(i).Type)
		}
	}
}

// The replay path serializes through Emit, so a resumed event is the same
// projection a live one is. Two serialization paths would drift.
func TestReplayedEventsCarryTheSameProjection(t *testing.T) {
	h := NewEventHub(zap.NewNop(), nil)
	h.SetEventAuthorizer(func(eventPrincipal, message.Event) bool { return true })
	h.Emit(message.Event{Type: "tool.result", WorkspaceID: "ws_a", AgentID: "bot", SessionID: "s1"})
	cursor := formatCursor("ws_a", 1)
	h.Emit(message.Event{Type: "tool.result", WorkspaceID: "ws_a", AgentID: "bot", SessionID: "s2"})

	replayed, _, err := h.ResumeSince(subscriber("ws_a", "operator:alice", "operator", false), cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d", len(replayed))
	}
	var decoded map[string]any
	if err := json.Unmarshal(replayed[0], &decoded); err != nil {
		t.Fatal(err)
	}
	for key := range decoded {
		if !isProjectedKey(key) {
			t.Errorf("a replayed event carries %q, which the projection does not declare", key)
		}
	}
}

// TestTheHubNeverMarshalsAnEventDirectly is the structural half.
//
// Today project() produces byte-identical JSON to marshalling the event, because
// every field is classified public — so a hub that bypassed the projection would
// behave identically and no assertion on output could tell. That stops being
// true the moment somebody classifies a field internal, and the bypass would
// then be a silent leak introduced by an unrelated change.
//
// Reading the source makes the call site load-bearing NOW rather than later.
func TestTheHubNeverMarshalsAnEventDirectly(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "events.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Marshal" {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "json" {
			return true
		}
		// json.Marshal(project(...)) is the correct form. A bare identifier
		// named `event` is the bypass.
		if ident, ok := call.Args[0].(*ast.Ident); ok && strings.Contains(strings.ToLower(ident.Name), "event") {
			t.Errorf("events.go:%d marshals %s directly instead of project(%s) — an internal field would reach the wire",
				fileSet.Position(call.Pos()).Line, ident.Name, ident.Name)
		}
		return true
	})
}
