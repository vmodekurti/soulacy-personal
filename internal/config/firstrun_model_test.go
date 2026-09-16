package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tagsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
}

// The shipped default used to name a ~40GB model nobody has, which disabled
// every built-in agent on a fresh install. Whatever is actually present wins.
func TestFirstRunNamesAModelThatIsActuallyInstalled(t *testing.T) {
	srv := tagsServer(t, `{"models":[
		{"name":"gemma4:latest","size":5000000000},
		{"name":"qwen3-coder:30b","size":18000000000},
		{"name":"nomic-embed-text:latest","size":270000000}]}`)
	defer srv.Close()

	got := detectInstalledOllamaModel(srv.URL)
	if got != "qwen3-coder:30b" {
		t.Fatalf("want the largest installed chat model, got %q", got)
	}
}

// An embedding model cannot hold a conversation. Choosing one would produce a
// config that looks configured and fails on the first message.
func TestFirstRunNeverPicksAnEmbeddingModel(t *testing.T) {
	srv := tagsServer(t, `{"models":[{"name":"nomic-embed-text:latest","size":270000000}]}`)
	defer srv.Close()

	if got := detectInstalledOllamaModel(srv.URL); got != "" {
		t.Fatalf("an embedding-only machine has no usable chat model; got %q", got)
	}
	for _, name := range []string{"nomic-embed-text", "mxbai-embed-large", "bge-m3", "all-minilm", "gte-base"} {
		if !isEmbeddingModelName(name) {
			t.Errorf("%s should be recognised as an embedding model", name)
		}
	}
	for _, name := range []string{"qwen3:8b", "llama3.2:3b", "gemma4:latest"} {
		if isEmbeddingModelName(name) {
			t.Errorf("%s is a chat model", name)
		}
	}
}

func TestFirstRunWritesNoModelWhenNothingIsInstalled(t *testing.T) {
	srv := tagsServer(t, `{"models":[]}`)
	defer srv.Close()

	if got := detectInstalledOllamaModel(srv.URL); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
	block := defaultOllamaModelBlock(srv.URL)
	if !strings.Contains(block, `model: ""`) {
		t.Errorf("an empty model is the honest default; got %q", block)
	}
	// It must also say where to fix it, or the user is left with a blank.
	if !strings.Contains(strings.ToLower(block), "dashboard") {
		t.Errorf("the comment should point at the fix; got %q", block)
	}
}

// No runtime at all is the common case on a cloud box. It must not hang or
// invent a model.
func TestFirstRunToleratesNoRuntime(t *testing.T) {
	if got := detectInstalledOllamaModel("http://127.0.0.1:1"); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
	block := defaultOllamaModelBlock("http://127.0.0.1:1")
	if !strings.Contains(block, `model: ""`) {
		t.Errorf("want an empty model; got %q", block)
	}
}

func TestFirstRunQuotesTheDetectedModelIntoValidYAML(t *testing.T) {
	srv := tagsServer(t, `{"models":[{"name":"qwen3:8b","size":5000000000}]}`)
	defer srv.Close()

	block := defaultOllamaModelBlock(srv.URL)
	if !strings.Contains(block, `model: "qwen3:8b"`) {
		t.Fatalf("model must be quoted, since a bare qwen3:8b is not valid YAML here; got %q", block)
	}
	for _, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "      ") {
			t.Errorf("every line needs the template's indentation; got %q", line)
		}
	}
}
