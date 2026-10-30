package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatJSONPreservesResponseEnvelope(t *testing.T) {
	const response = `{"reply":"Report verified","session_id":"s-1","metadata":{"task.outcome":"incomplete"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	routeMockGateway(t, srv)
	isolatedCLIPaths(t)
	resetCLIGlobals(t)
	old := outputJSON
	t.Cleanup(func() { outputJSON = old })
	for _, asJSON := range []bool{false, true} {
		outputJSON = asJSON
		gatewayURL = "http://mock-gateway:8080"
		apiKey = "test"
		cmd := buildChatCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--agent", "analyst", "hello"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["session_id"] != "s-1" || got["metadata"] == nil {
				t.Fatalf("lost response metadata: %s", out.String())
			}
		} else if out.String() != "Report verified\n" {
			t.Fatalf("plain output = %q", out.String())
		}
	}
}
