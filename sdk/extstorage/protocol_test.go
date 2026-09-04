package extstorage

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseMessage_Request(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","id":7,"method":"vector.search","params":{"agent_id":"a1","query":"hello","top_k":3}}`)
	m, err := ParseMessage(line)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if m.JSONRPC != Version2 {
		t.Errorf("jsonrpc = %q, want %q", m.JSONRPC, Version2)
	}
	if m.ID == nil || *m.ID != 7 {
		t.Errorf("id = %v, want 7", m.ID)
	}
	if m.Method != MethodVectorSearch {
		t.Errorf("method = %q, want %q", m.Method, MethodVectorSearch)
	}
	var p VectorSearchParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		t.Fatalf("params: %v", err)
	}
	if p.AgentID != "a1" || p.Query != "hello" || p.TopK != 3 {
		t.Errorf("params = %+v", p)
	}
	if !m.IsRequest() || m.IsNotification() || m.IsResponse() {
		t.Errorf("classification wrong: req=%v notif=%v resp=%v",
			m.IsRequest(), m.IsNotification(), m.IsResponse())
	}
}

func TestParseMessage_Response(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","id":7,"result":{"results":[]}}`)
	m, err := ParseMessage(line)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if !m.IsResponse() || m.IsRequest() {
		t.Error("expected response classification")
	}
}

func TestParseMessage_Notification(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"queue.message","params":{"subscription_id":"s1","subject":"x.y","data":"aGk="}}`)
	m, err := ParseMessage(line)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if !m.IsNotification() {
		t.Error("expected notification classification")
	}
}

func TestParseMessage_Malformed(t *testing.T) {
	cases := map[string]string{
		"invalid json":    `{nope`,
		"wrong version":   `{"jsonrpc":"1.0","id":1,"method":"x"}`,
		"missing version": `{"id":1,"method":"x"}`,
		"no method or id": `{"jsonrpc":"2.0"}`,
	}
	for name, line := range cases {
		if _, err := ParseMessage([]byte(line)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestWriteMessage_OneLine(t *testing.T) {
	var buf bytes.Buffer
	id := int64(3)
	err := WriteMessage(&buf, Message{
		JSONRPC: Version2, ID: &id, Method: MethodNegotiate,
		Params: MustParams(NegotiateParams{Protocol: 1, Name: "test"}),
	})
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	s := buf.String()
	if !strings.HasSuffix(s, "\n") || strings.Count(s, "\n") != 1 {
		t.Errorf("not exactly one NDJSON line: %q", s)
	}
	if _, err := ParseMessage([]byte(strings.TrimSpace(s))); err != nil {
		t.Errorf("round-trip: %v", err)
	}
}

func TestNewRequestResponseHelpers(t *testing.T) {
	req, err := NewRequest(5, MethodVectorWrite, VectorWriteParams{})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if req.ID == nil || *req.ID != 5 || req.Method != MethodVectorWrite || req.JSONRPC != Version2 {
		t.Errorf("req = %+v", req)
	}

	resp, err := NewResponse(5, VectorSearchResult{})
	if err != nil {
		t.Fatalf("NewResponse: %v", err)
	}
	if resp.ID == nil || *resp.ID != 5 || resp.Result == nil || !resp.IsResponse() {
		t.Errorf("resp = %+v", resp)
	}

	errResp := NewErrorResponse(5, CodeMethodNotFound, "no such method")
	if errResp.Error == nil || errResp.Error.Code != CodeMethodNotFound {
		t.Errorf("errResp = %+v", errResp)
	}
	if !errResp.IsResponse() {
		t.Error("error response must classify as response")
	}
}

func TestNegotiate(t *testing.T) {
	// Happy path: sidecar speaks a higher version → min wins.
	v, err := Negotiate(NegotiateParams{Protocol: ProtocolVersion + 5, Name: "vec-sidecar"})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if v != ProtocolVersion {
		t.Errorf("v = %d, want %d", v, ProtocolVersion)
	}

	// Sidecar speaks a lower (valid) version → sidecar's version wins.
	if ProtocolVersion > 1 {
		v, err = Negotiate(NegotiateParams{Protocol: 1, Name: "old"})
		if err != nil || v != 1 {
			t.Errorf("v=%d err=%v, want 1,nil", v, err)
		}
	}

	// Failures.
	if _, err := Negotiate(NegotiateParams{Protocol: 0, Name: "x"}); err == nil {
		t.Error("zero protocol must error")
	}
	if _, err := Negotiate(NegotiateParams{Protocol: 1}); err == nil {
		t.Error("missing name must error")
	}
}

func TestSharedDirInNegotiate(t *testing.T) {
	// The host advertises the per-run scratch directory in the negotiate
	// REQUEST params; conforming sidecars echo it in the result so the host
	// can verify the contract was understood.
	p := NegotiateParams{Protocol: 1, Name: "host", SharedDir: "/abs/scratch/run-1"}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back NegotiateParams
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.SharedDir != p.SharedDir {
		t.Errorf("shared dir round-trip: %q", back.SharedDir)
	}
	r := NegotiateResult{Protocol: 1, SharedDir: p.SharedDir}
	if r.SharedDir != p.SharedDir {
		t.Errorf("result shared dir: %q", r.SharedDir)
	}
}
