package external

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseFrame_KnownFrames(t *testing.T) {
	cases := []struct {
		line     string
		wantType string
	}{
		{`{"type":"hello","protocol":1,"name":"matrix","capabilities":["send"]}`, "hello"},
		{`{"type":"status","connected":true,"detail":"linked"}`, "status"},
		{`{"type":"message","id":"m1","chat_id":"c1","text":"hi"}`, "message"},
		{`{"type":"error","detail":"boom"}`, "error"},
	}
	for _, c := range cases {
		f, err := ParseFrame([]byte(c.line))
		if err != nil {
			t.Errorf("ParseFrame(%s): %v", c.line, err)
			continue
		}
		if f.Type != c.wantType {
			t.Errorf("type = %q, want %q", f.Type, c.wantType)
		}
	}
}

func TestParseFrame_HelloFields(t *testing.T) {
	f, err := ParseFrame([]byte(`{"type":"hello","protocol":1,"name":"matrix","capabilities":["send","status"]}`))
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if f.Protocol != 1 || f.Name != "matrix" || len(f.Capabilities) != 2 {
		t.Errorf("frame = %+v", f)
	}
}

func TestParseFrame_UnknownTypeIsNotAnError(t *testing.T) {
	// Forward compatibility: unknown frame types parse fine; callers ignore them.
	f, err := ParseFrame([]byte(`{"type":"telemetry.v9","whatever":42}`))
	if err != nil {
		t.Fatalf("unknown frame type should parse: %v", err)
	}
	if f.Type != "telemetry.v9" {
		t.Errorf("type = %q", f.Type)
	}
}

func TestParseFrame_Garbage(t *testing.T) {
	if _, err := ParseFrame([]byte(`{not json`)); err == nil {
		t.Error("garbage should error")
	}
	if _, err := ParseFrame([]byte(`{"no_type":1}`)); err == nil {
		t.Error("missing type should error")
	}
}

func TestWriteFrame_NDJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{Type: "send", To: "chat-9", Text: "hello"}); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Error("frame must be newline-terminated (NDJSON)")
	}
	if strings.Count(out, "\n") != 1 {
		t.Error("frame must be exactly one line")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if m["type"] != "send" || m["to"] != "chat-9" || m["text"] != "hello" {
		t.Errorf("frame = %v", m)
	}
}

func TestNegotiate(t *testing.T) {
	// Sidecar speaks our version → agree on it.
	v, err := Negotiate(Frame{Type: "hello", Protocol: 1, Name: "x"})
	if err != nil || v != 1 {
		t.Errorf("Negotiate(v1) = %d, %v", v, err)
	}
	// Sidecar is newer → agree on OUR (lower) version.
	v, err = Negotiate(Frame{Type: "hello", Protocol: 99, Name: "x"})
	if err != nil || v != ProtocolVersion {
		t.Errorf("Negotiate(v99) = %d, %v; want gateway version", v, err)
	}
	// Sidecar declares no/zero version → reject.
	if _, err := Negotiate(Frame{Type: "hello", Name: "x"}); err == nil {
		t.Error("zero protocol version should be rejected")
	}
	// Not a hello frame → reject.
	if _, err := Negotiate(Frame{Type: "status", Protocol: 1}); err == nil {
		t.Error("non-hello frame should be rejected")
	}
	// Missing name → reject (identity is required).
	if _, err := Negotiate(Frame{Type: "hello", Protocol: 1}); err == nil {
		t.Error("hello without name should be rejected")
	}
}
