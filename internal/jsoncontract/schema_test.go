package jsoncontract

import (
	"strings"
	"testing"
)

func TestConditionalReportContract(t *testing.T) {
	schema := map[string]any{
		"type": "object", "required": []string{"op"},
		"properties": map[string]any{
			"op":     map[string]any{"enum": []string{"save", "read"}},
			"report": map[string]any{"$ref": "#/$defs/report"},
		},
		"$defs": map[string]any{"report": map[string]any{
			"type": "object", "required": []string{"multiplier"},
			"properties":           map[string]any{"multiplier": map[string]any{"type": "number", "minimum": 0}},
			"additionalProperties": false,
		}},
		"if":   map[string]any{"properties": map[string]any{"op": map[string]any{"const": "save"}}},
		"then": map[string]any{"required": []string{"report"}},
	}
	for _, tc := range []struct {
		name  string
		args  map[string]any
		valid bool
	}{
		{"read", map[string]any{"op": "read"}, true},
		{"valid integer", map[string]any{"op": "save", "report": map[string]any{"multiplier": 12}}, true},
		{"missing report", map[string]any{"op": "save"}, false},
		{"missing nested field", map[string]any{"op": "save", "report": map[string]any{}}, false},
		{"wrong type", map[string]any{"op": "save", "report": map[string]any{"multiplier": "12"}}, false},
		{"negative", map[string]any{"op": "save", "report": map[string]any{"multiplier": -1}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(schema, tc.args); (err == nil) != tc.valid {
				t.Fatalf("Validate = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

func TestSchemaReferencesCannotLoadExternalResources(t *testing.T) {
	for _, ref := range []string{"file:///etc/passwd", "https://example.com/schema.json"} {
		err := Validate(map[string]any{"$ref": ref}, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "external schema reference") {
			t.Fatalf("reference %q: %v", ref, err)
		}
	}
}
