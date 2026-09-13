package config

import (
	"github.com/soulacy/soulacy/internal/safeundo"
	"strings"
	"testing"
)

func TestSafeUndoConfigurationIsExplicitAndFailClosed(t *testing.T) {
	good := safeundo.Resource{ID: "notes", AgentID: "assistant", Name: "Notes", Kind: "webdav_text", URL: "https://documents.example.com/notes.md", ConditionalWrites: true}
	cfg := Config{Server: ServerConfig{Port: 1947, SafeUndo: safeundo.Config{Resources: []safeundo.Resource{good}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*safeundo.Resource){
		func(r *safeundo.Resource) { r.ConditionalWrites = false },
		func(r *safeundo.Resource) { r.URL = "http://example.com/file" },
		func(r *safeundo.Resource) { r.URL = "https://user:secret@example.com/file" },
		func(r *safeundo.Resource) { r.URL = "https://example.com/file?token=secret" },
		func(r *safeundo.Resource) { r.Kind = "send_email" },
		func(r *safeundo.Resource) { r.Kind = "json_record" },
		func(r *safeundo.Resource) { r.Fields = []string{"bad-for-document"} },
		func(r *safeundo.Resource) { r.TokenEnv = "secret token" },
	} {
		copy := good
		change(&copy)
		cfg.Server.SafeUndo.Resources = []safeundo.Resource{copy}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "server.safe_undo") {
			t.Fatal(copy.ID, err)
		}
	}
	cfg.Server.SafeUndo.Resources = []safeundo.Resource{good, good}
	if err := cfg.Validate(); err == nil {
		t.Fatal("duplicate target accepted")
	}
	cfg.Server.SafeUndo.Resources = nil
	if err := cfg.Validate(); err != nil {
		t.Fatal("disabled config rejected", err)
	}
}
