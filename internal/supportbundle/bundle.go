package supportbundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/redact"
)

const DefaultLogTailBytes int64 = 256 * 1024

type Options struct {
	GatewayURL   string
	ConfigPath   string
	AgentDirs    []string
	LogDirs      []string
	Workspace    map[string]string
	Doctor       any
	ExtraJSON    map[string]any
	LogTailBytes int64
	// IncludeContent opts the bundle into raw log tails. Config and diagnostic
	// JSON are always redacted, but log bodies are the agent's actual work —
	// prompts, tool output, message text — so they are omitted unless the
	// caller asks for them explicitly. Defaulting to "include" would mean an
	// operator could hand a vendor a tenant's conversations without ever
	// deciding to.
	IncludeContent bool
	Now            time.Time
}

type Manifest struct {
	CreatedAt    string            `json:"created_at"`
	Version      string            `json:"version"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	GatewayURL   string            `json:"gateway_url,omitempty"`
	ConfigPath   string            `json:"config_path,omitempty"`
	Workspace    map[string]string `json:"workspace,omitempty"`
	Included     []string          `json:"included"`
	Redaction    string            `json:"redaction"`
	LogTailBytes int64             `json:"log_tail_bytes"`
	// ContentIncluded records the choice in the bundle itself, so whoever
	// receives it can tell whether they are holding log bodies.
	ContentIncluded bool `json:"content_included"`
}

func Write(w io.Writer, opts Options) (Manifest, error) {
	if opts.LogTailBytes <= 0 {
		opts.LogTailBytes = DefaultLogTailBytes
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	manifest := Manifest{
		CreatedAt:       opts.Now.UTC().Format(time.RFC3339),
		Version:         config.Version,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		GatewayURL:      opts.GatewayURL,
		ConfigPath:      opts.ConfigPath,
		Workspace:       opts.Workspace,
		Redaction:       "secret-like fields are replaced with ***REDACTED***; long opaque scalars are hashed",
		LogTailBytes:    opts.LogTailBytes,
		ContentIncluded: opts.IncludeContent,
	}

	zw := zip.NewWriter(w)
	add := func(name string, data []byte) error {
		name = filepath.ToSlash(strings.TrimPrefix(name, "/"))
		fw, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := fw.Write(data); err != nil {
			return err
		}
		manifest.Included = append(manifest.Included, name)
		return nil
	}

	doctorJSON, _ := json.MarshalIndent(redact.ValueForExport(opts.Doctor), "", "  ")
	if len(bytes.TrimSpace(doctorJSON)) == 0 || string(doctorJSON) == "null" {
		doctorJSON = []byte("{}")
	}
	if err := add("doctor.json", doctorJSON); err != nil {
		_ = zw.Close()
		return manifest, err
	}
	if err := addExtraJSON(add, opts.ExtraJSON); err != nil {
		_ = zw.Close()
		return manifest, err
	}

	if opts.ConfigPath != "" {
		if data, err := RedactedYAMLFile(opts.ConfigPath); err == nil {
			if err := add("config.redacted.yaml", data); err != nil {
				_ = zw.Close()
				return manifest, err
			}
		}
	}
	if err := addAgentManifests(add, opts.AgentDirs); err != nil {
		_ = zw.Close()
		return manifest, err
	}
	if opts.IncludeContent {
		if err := addRecentLogTails(add, opts.LogDirs, opts.LogTailBytes); err != nil {
			_ = zw.Close()
			return manifest, err
		}
	} else {
		manifest.Included = append(manifest.Included,
			"(log bodies omitted — re-request with explicit content confirmation)")
	}

	sort.Strings(manifest.Included)
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	if err := add("manifest.json", manifestJSON); err != nil {
		_ = zw.Close()
		return manifest, err
	}
	return manifest, zw.Close()
}

func addExtraJSON(add func(string, []byte) error, extra map[string]any) error {
	if len(extra) == 0 {
		return nil
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := extra[name]
		data, _ := json.MarshalIndent(redact.ValueForExport(value), "", "  ")
		if len(bytes.TrimSpace(data)) == 0 || string(data) == "null" {
			data = []byte("{}")
		}
		archiveName := safeArchiveName(strings.TrimSuffix(name, ".json")) + ".json"
		if err := add(archiveName, data); err != nil {
			return err
		}
	}
	return nil
}

func WriteFile(out string, opts Options) (string, Manifest, error) {
	if strings.TrimSpace(out) == "" {
		out = fmt.Sprintf("soulacy-support-%s.zip", time.Now().Format("20060102-150405"))
	}
	outAbs, err := filepath.Abs(out)
	if err != nil {
		return "", Manifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(outAbs), 0o700); err != nil {
		return "", Manifest{}, err
	}
	f, err := os.OpenFile(outAbs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", Manifest{}, err
	}
	defer f.Close()
	manifest, err := Write(f, opts)
	if err != nil {
		return "", manifest, err
	}
	return outAbs, manifest, nil
}

func RedactedYAMLFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return []byte(RedactText(string(raw))), nil
	}
	redactYAMLNode(&doc, "", false)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

func addAgentManifests(add func(string, []byte) error, dirs []string) error {
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "SOUL.yaml"))
		for _, path := range matches {
			data, err := RedactedYAMLFile(path)
			if err != nil {
				continue
			}
			agentID := filepath.Base(filepath.Dir(path))
			if err := add(filepath.Join("agents", safeArchiveName(agentID)+".SOUL.redacted.yaml"), data); err != nil {
				return err
			}
		}
	}
	return nil
}

func addRecentLogTails(add func(string, []byte) error, dirs []string, maxBytes int64) error {
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		type logFile struct {
			path string
			mod  time.Time
		}
		var logs []logFile
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			logs = append(logs, logFile{path: filepath.Join(dir, entry.Name()), mod: info.ModTime()})
		}
		sort.Slice(logs, func(i, j int) bool { return logs[i].mod.After(logs[j].mod) })
		if len(logs) > 12 {
			logs = logs[:12]
		}
		for _, lf := range logs {
			data, err := tailRedactedTextFile(lf.path, maxBytes)
			if err != nil {
				continue
			}
			name := filepath.Join("logs", safeArchiveName(filepath.Base(lf.path))+".tail.txt")
			if err := add(name, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func redactYAMLNode(n *yaml.Node, parentKey string, wholesale bool) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			redactYAMLNode(child, parentKey, wholesale)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1]
			redactYAMLNode(val, key, wholesale || isSecretKey(key) || isOpaqueConfigContainer(key))
		}
	case yaml.ScalarNode:
		if wholesale || isSecretKey(parentKey) {
			n.Value = "***REDACTED***"
			n.Tag = "!!str"
			return
		}
		n.Value = redactScalar(n.Value)
	}
}

func tailRedactedTextFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > maxBytes {
		if _, err := f.Seek(st.Size()-maxBytes, io.SeekStart); err != nil {
			return nil, err
		}
	} else if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return []byte(RedactText(string(data))), nil
}

// RedactText scrubs a support bundle's free text. Bundles leave the deployment
// and are routinely handed to a vendor, so they get personal-data removal on
// top of the credential scrub the rest of the codebase uses.
func RedactText(s string) string {
	return redact.PersonalText(s)
}

func redactScalar(s string) string {
	if looksLikeSecret(s) {
		return redactedHash(s)
	}
	return s
}

func isSecretKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, needle := range []string{
		"api_key", "apikey", "access_token", "refresh_token", "bot_token", "token",
		"secret", "password", "passwd", "private_key", "client_secret", "app_secret",
		"verify_token", "signing_secret", "signing_key", "webhook_secret", "authorization",
		"credential", "postgres_dsn", "database_url", "connection_string", "cookie",
	} {
		if k == needle || strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

func isOpaqueConfigContainer(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "env", "headers", "secrets", "credentials", "vault", "provider_config", "operator_config":
		return true
	default:
		return false
	}
}

func looksLikeSecret(s string) bool {
	if len(s) < 20 {
		return false
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "sk-"),
		strings.HasPrefix(lower, "xoxb-"),
		strings.HasPrefix(lower, "xapp-"),
		strings.HasPrefix(lower, "ghp_"),
		strings.HasPrefix(lower, "github_pat_"),
		strings.HasPrefix(lower, "ya29."),
		strings.HasPrefix(lower, "bot"),
		strings.Contains(lower, "api_key="),
		strings.Contains(lower, "token="):
		return true
	}
	alnum := 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			alnum++
		}
	}
	return len(s) >= 48 && alnum*100/len(s) >= 85
}

func redactedHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "***REDACTED:" + hex.EncodeToString(sum[:])[:12] + "***"
}

func safeArchiveName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return "unnamed"
	}
	return out
}
