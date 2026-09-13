package config

import (
	"strings"
	"testing"
)

func TestPublishedFilesConfigValidation(t *testing.T) {
	for _, shares := range [][]PublishedFilesConfig{
		{{AgentID: "a", Root: "/"}}, {{AgentID: "", Root: "/srv/published/a"}},
		{{AgentID: "a", Root: "relative"}},
		{{AgentID: "a", Root: "/srv/a"}, {AgentID: "a", Root: "/srv/b"}},
	} {
		cfg := Config{Server: ServerConfig{Port: 1947, PublishedFiles: shares}}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "server.published_files") {
			t.Fatalf("accepted %+v: %v", shares, err)
		}
	}
	cfg := Config{Server: ServerConfig{Port: 1947, PublishedFiles: []PublishedFilesConfig{{AgentID: "a", Root: "/srv/published/a"}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
