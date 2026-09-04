// message_test.go — tests for the message package.
package message

import "testing"

func TestTextReturnsOnePart(t *testing.T) {
	parts := Text("hello world")
	if len(parts) != 1 {
		t.Fatalf("Text: len = %d, want 1", len(parts))
	}
	if parts[0].Type != ContentText {
		t.Errorf("Type = %q, want ContentText", parts[0].Type)
	}
	if parts[0].Text != "hello world" {
		t.Errorf("Text = %q, want 'hello world'", parts[0].Text)
	}
}

func TestTextEmptyString(t *testing.T) {
	parts := Text("")
	if len(parts) != 1 {
		t.Fatalf("Text(''): len = %d, want 1", len(parts))
	}
	if parts[0].Text != "" {
		t.Errorf("Text = %q, want empty", parts[0].Text)
	}
}

func TestRoleConstants(t *testing.T) {
	// Ensure the string values are stable — other packages key on them.
	if RoleUser != "user" {
		t.Errorf("RoleUser = %q, want user", RoleUser)
	}
	if RoleAssistant != "assistant" {
		t.Errorf("RoleAssistant = %q", RoleAssistant)
	}
	if RoleSystem != "system" {
		t.Errorf("RoleSystem = %q", RoleSystem)
	}
	if RoleTool != "tool" {
		t.Errorf("RoleTool = %q", RoleTool)
	}
}

func TestContentTypeConstants(t *testing.T) {
	if ContentText != "text" {
		t.Errorf("ContentText = %q, want text", ContentText)
	}
}

// ---------------------------------------------------------------------------
// TypedPart — Kind(), MarshalJSON(), UnmarshalPartJSON()
// ---------------------------------------------------------------------------

func TestTextPartKindAndMarshal(t *testing.T) {
	p := TextPart{Text: "hello"}
	if p.Kind() != "text" {
		t.Errorf("Kind = %q", p.Kind())
	}
	data, err := p.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	got, err := UnmarshalPartJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalPartJSON: %v", err)
	}
	if tp, ok := got.(TextPart); !ok || tp.Text != "hello" {
		t.Errorf("round-trip: %+v", got)
	}
}

func TestFilePartRoundTrip(t *testing.T) {
	p := FilePart{ID: "f1", MIMEType: "application/pdf", Filename: "report.pdf"}
	data, _ := p.MarshalJSON()
	got, err := UnmarshalPartJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalPartJSON FilePart: %v", err)
	}
	fp, ok := got.(FilePart)
	if !ok || fp.ID != "f1" || fp.Filename != "report.pdf" {
		t.Errorf("FilePart round-trip: %+v", got)
	}
}

func TestImagePartRoundTrip(t *testing.T) {
	p := ImagePart{ID: "img1", MIMEType: "image/png", Width: 800, Height: 600}
	data, _ := p.MarshalJSON()
	got, err := UnmarshalPartJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalPartJSON ImagePart: %v", err)
	}
	ip, ok := got.(ImagePart)
	if !ok || ip.Width != 800 || ip.Height != 600 {
		t.Errorf("ImagePart round-trip: %+v", got)
	}
}

func TestAudioPartRoundTrip(t *testing.T) {
	p := AudioPart{ID: "a1", MIMEType: "audio/mpeg", DurationSec: 42}
	data, _ := p.MarshalJSON()
	got, err := UnmarshalPartJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalPartJSON AudioPart: %v", err)
	}
	ap, ok := got.(AudioPart)
	if !ok || ap.DurationSec != 42 {
		t.Errorf("AudioPart round-trip: %+v", got)
	}
}

func TestLocationPartRoundTrip(t *testing.T) {
	p := LocationPart{Lat: 37.7749, Lon: -122.4194, Label: "San Francisco"}
	data, _ := p.MarshalJSON()
	got, err := UnmarshalPartJSON(data)
	if err != nil {
		t.Fatalf("UnmarshalPartJSON LocationPart: %v", err)
	}
	lp, ok := got.(LocationPart)
	if !ok || lp.Label != "San Francisco" {
		t.Errorf("LocationPart round-trip: %+v", got)
	}
}

func TestUnmarshalPartJSONInvalidJSON(t *testing.T) {
	_, err := UnmarshalPartJSON([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestUnmarshalPartJSONUnknownKind(t *testing.T) {
	_, err := UnmarshalPartJSON([]byte(`{"kind":"video","id":"v1"}`))
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
}

func TestUnmarshalPartJSONMissingKind(t *testing.T) {
	_, err := UnmarshalPartJSON([]byte(`{"id":"x1","mime_type":"image/png"}`))
	if err == nil {
		t.Fatal("expected error for missing kind, got nil")
	}
}

func TestImagePartOmitemptyZeroDimensions(t *testing.T) {
	p := ImagePart{ID: "img2", MIMEType: "image/jpeg"}
	data, _ := p.MarshalJSON()
	// Width and Height are 0 → should be omitted from JSON.
	s := string(data)
	if containsField(s, "width") || containsField(s, "height") {
		t.Errorf("zero Width/Height should be omitempty: %s", s)
	}
}

func TestLocationPartOmitemptyLabel(t *testing.T) {
	p := LocationPart{Lat: 0, Lon: 0}
	data, _ := p.MarshalJSON()
	if containsField(string(data), "label") {
		t.Errorf("empty Label should be omitempty: %s", data)
	}
}

func containsField(json, field string) bool {
	return len(json) > 0 && (len(json) > len(field)+2) &&
		(func() bool {
			for i := 0; i < len(json)-len(field)-1; i++ {
				if json[i] == '"' && json[i+1:i+1+len(field)] == field && json[i+1+len(field)] == '"' {
					return true
				}
			}
			return false
		}())
}
