package supportbundle

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactedYAMLFileMasksSecretKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("llm:\n  providers:\n    openai:\n      api_key: sk-test-secret-value-1234567890\nplain: visible\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := RedactedYAMLFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "sk-test-secret-value") {
		t.Fatalf("secret leaked in redacted yaml:\n%s", got)
	}
	if !strings.Contains(got, "plain: visible") || !strings.Contains(got, "***REDACTED***") {
		t.Fatalf("unexpected redacted yaml:\n%s", got)
	}
}

func TestWriteIncludesRedactedConfigAndManifest(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.yaml")
	agents := filepath.Join(root, "agents")
	logs := filepath.Join(root, "logs")
	if err := os.MkdirAll(filepath.Join(agents, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("channels:\n  slack:\n    bot_token: xoxb-super-secret-token-value-1234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "demo", "SOUL.yaml"), []byte("id: demo\nllm:\n  api_key: supersecretvalueabcdefghijklmnopqrstuvwxyz0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "demo.log"), []byte("token=supersecretvalueabcdefghijklmnopqrstuvwxyz0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	_, err := Write(&buf, Options{
		ConfigPath: cfg,
		AgentDirs:  []string{agents},
		LogDirs:    []string{logs},
		Workspace:  map[string]string{"root": root, "agents": agents, "logs": logs},
		Doctor:     map[string]any{"ok": true},
		Now:        time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	var all strings.Builder
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		all.Write(data)
	}
	for _, want := range []string{"manifest.json", "doctor.json", "config.redacted.yaml", "agents/demo.SOUL.redacted.yaml"} {
		if !names[want] {
			t.Fatalf("bundle missing %s; got %#v", want, names)
		}
	}
	joined := all.String()
	for _, forbidden := range []string{"xoxb-super-secret", "supersecretvalueabcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("bundle leaked %q:\n%s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "***REDACTED***") {
		t.Fatalf("bundle did not include redaction marker:\n%s", joined)
	}
}

func TestWriteIncludesExtraJSONDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	_, err := Write(&buf, Options{
		Doctor: map[string]any{"ok": true},
		ExtraJSON: map[string]any{
			"readiness": map[string]any{
				"status": "ready",
				"token":  "xoxb-extra-secret-token-value-1234567890",
			},
		},
		Now: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var readiness string
	for _, f := range zr.File {
		if f.Name != "readiness.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		readiness = string(data)
	}
	if readiness == "" {
		t.Fatalf("bundle missing readiness.json")
	}
	if !strings.Contains(readiness, `"status": "ready"`) {
		t.Fatalf("unexpected readiness.json:\n%s", readiness)
	}
	if strings.Contains(readiness, "xoxb-extra-secret") {
		t.Fatalf("extra diagnostic leaked secret:\n%s", readiness)
	}
}

func TestSupportBundleNeverExportsSeededCredentialsAtAnyDepth(t *testing.T) {
	root := t.TempDir()
	agents, logs, secretsDir := filepath.Join(root, "agents"), filepath.Join(root, "logs"), filepath.Join(root, "secrets")
	for _, dir := range []string{filepath.Join(agents, "demo"), logs, secretsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{
		"provider":   "sk-provider-credential-abcdefghijklmnopqrstuvwxyz",
		"mcp_env":    "mcp-env-value-that-looks-ordinary",
		"mcp_header": "mcp-header-value-that-looks-ordinary",
		"jwt":        "jwt-signing-key-abcdefghijklmnopqrstuvwxyz",
		"whatsapp":   "whatsapp-verify-token-abcdefghijklmnopqrstuvwxyz",
		"postgres":   "inline-postgres-password",
		"vault":      "encrypted-vault-plaintext-sentinel",
		"doctor":     "doctor-nested-secret-abcdefghijklmnopqrstuvwxyz",
	}
	cfg := filepath.Join(root, "config.yaml")
	configBody := "llm:\n  providers:\n    openai:\n      api_key: " + values["provider"] +
		"\nmcp:\n  servers:\n    weather:\n      env:\n        CUSTOM_NAME: " + values["mcp_env"] +
		"\n      headers:\n        X-Custom: " + values["mcp_header"] +
		"\nauth:\n  jwt_secret: " + values["jwt"] +
		"\nchannels:\n  whatsapp:\n    verify_token: " + values["whatsapp"] +
		"\nstorage:\n  postgres_dsn: postgres://user:" + values["postgres"] + "@db/soulacy\n"
	if err := os.WriteFile(cfg, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "credentials.db"), []byte(values["vault"]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "soulacy.log"), []byte("Authorization: Bearer "+values["provider"]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "demo", "SOUL.yaml"), []byte("id: demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	_, err := Write(&buf, Options{
		ConfigPath: cfg, AgentDirs: []string{agents}, LogDirs: []string{logs},
		Doctor:    map[string]any{"nested": map[string]any{"signing_key": values["doctor"]}},
		ExtraJSON: map[string]any{"mcp-status": map[string]any{"headers": map[string]any{"X-Custom": values["mcp_header"]}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range zr.File {
		rc, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		all.Write(b)
	}
	joined := all.String()
	for name, value := range values {
		if strings.Contains(joined, value) {
			t.Fatalf("%s value leaked anywhere in bundle", name)
		}
	}
	for _, key := range []string{"CUSTOM_NAME", "X-Custom", "postgres_dsn", "jwt_secret", "verify_token"} {
		if !strings.Contains(joined, key) {
			t.Errorf("redaction removed diagnostic key name %q", key)
		}
	}
}
