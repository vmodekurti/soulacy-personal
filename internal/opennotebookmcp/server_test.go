package opennotebookmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateBaseURLLoopbackBoundary(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:5055", "http://localhost:5055", "https://[::1]:5055"} {
		if _, err := ValidateBaseURL(raw); err != nil {
			t.Errorf("ValidateBaseURL(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"https://notebooks.example.com", "http://192.168.1.8:5055", "http://user:pass@localhost:5055", "file:///tmp/notebook"} {
		if _, err := ValidateBaseURL(raw); err == nil {
			t.Errorf("ValidateBaseURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestMCPInitializeAndToolDiscovery(t *testing.T) {
	srv, err := New(DefaultBaseURL, "", "v-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	initData, ok := srv.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	if !ok || !bytes.Contains(initData, []byte(`"protocolVersion":"2025-06-18"`)) {
		t.Fatalf("initialize = %s", initData)
	}
	listData, ok := srv.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if !ok {
		t.Fatal("tools/list returned no response")
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listData, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Result.Tools) < 20 {
		t.Fatalf("tool count = %d, want at least 20", len(response.Result.Tools))
	}
	seen := map[string]bool{}
	for _, tool := range response.Result.Tools {
		seen[tool.Name] = true
		if strings.Contains(tool.Name, "delete") {
			t.Fatalf("destructive tool exposed: %s", tool.Name)
		}
	}
	for _, required := range []string{"open_notebook_add_text_source", "open_notebook_search", "open_notebook_ask", "open_notebook_generate_podcast"} {
		if !seen[required] {
			t.Errorf("missing tool %s", required)
		}
	}
}

func TestRepresentativeOpenNotebookCalls(t *testing.T) {
	var requests []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&data)
		typeName, _ := data["type"].(string)
		requests = append(requests, fmt.Sprintf("%s %s %s", r.Method, r.URL.RequestURI(), typeName))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/notebooks":
			_, _ = w.Write([]byte(`[{"id":"notebook:1","name":"Research"}]`))
		case "/api/sources/json":
			_, _ = w.Write([]byte(`{"id":"source:1","status":"processing"}`))
		case "/api/search":
			_, _ = w.Write([]byte(`[{"title":"Finding"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	srv, err := New(api.URL, "", "test", api.Client())
	if err != nil {
		t.Fatal(err)
	}

	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"open_notebook_list_notebooks", nil},
		{"open_notebook_add_text_source", map[string]any{"content": "HBR article text", "notebooks": []any{"notebook:1"}}},
		{"open_notebook_search", map[string]any{"query": "strategy", "limit": float64(5)}},
	} {
		params, _ := json.Marshal(map[string]any{"name": call.name, "arguments": call.args})
		result := srv.callTool(context.Background(), params)
		if result["isError"] != false {
			t.Fatalf("%s failed: %#v", call.name, result)
		}
	}
	want := []string{"GET /api/notebooks ", "POST /api/sources/json text", "POST /api/search "}
	if fmt.Sprint(requests) != fmt.Sprint(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestAPIFailureIsBoundedAndSanitized(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"bad input","internal":"secret-value"}`))
	}))
	defer api.Close()
	srv, _ := New(api.URL, "super-secret-token", "test", api.Client())
	_, err := srv.execute(context.Background(), "open_notebook_create_notebook", map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("expected API error")
	}
	if got := err.Error(); got != "Open Notebook returned HTTP 422: bad input" {
		t.Fatalf("error = %q", got)
	}
}

func TestURLSourceRejectsPrivateDestinations(t *testing.T) {
	srv, _ := New(DefaultBaseURL, "", "test", nil)
	_, err := srv.execute(context.Background(), "open_notebook_add_url_source", map[string]any{"url": "http://127.0.0.1/private"})
	if err == nil || !strings.Contains(err.Error(), "public HTTP(S) address") {
		t.Fatalf("error = %v", err)
	}
}

func TestServeUsesNewlineDelimitedJSONRPC(t *testing.T) {
	srv, _ := New(DefaultBaseURL, "", "test", nil)
	in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n" {
		t.Fatalf("output = %q", got)
	}
}
