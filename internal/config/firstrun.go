// firstrun.go — "dumb install" bootstrap.
//
// On a virgin install, `soulacy serve` should run with zero file edits:
// no operator should ever have to `vim ~/.soulacy/soulspace/config.yaml`
// to generate an API key or set agent_dirs before the first launch.
// This file holds the helpers that make that true.
//
// EnsureBootstrap is called from cmd/soulacy/main.go after config.Load
// and EnsureDirs. It performs two side-effecting steps, each idempotent:
//
//   1. If the resolved config.yaml doesn't exist on disk, write a
//      minimal default with a freshly-generated API key.
//   2. If config.yaml DOES exist but server.api_key is empty AND the
//      operator hasn't explicitly bound to a non-loopback host (i.e.
//      they're not running behind a reverse proxy that handles auth),
//      generate a key and rewrite just that field — preserving every
//      other setting the operator may have edited.
//
// Returns a BootstrapResult so the caller can print a one-time banner
// telling the operator the URL + API key. Subsequent runs return
// `Bootstrap: nothing to do` and print no banner.

package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// BootstrapAction describes what (if anything) EnsureBootstrap did.
type BootstrapAction int

const (
	// BootstrapNoop — config.yaml already exists with a non-empty
	// api_key, or the operator is intentionally running keyless behind
	// a reverse proxy. Nothing changed.
	BootstrapNoop BootstrapAction = iota
	// BootstrapWroteConfig — config.yaml didn't exist; a full default
	// was written, including a generated api_key.
	BootstrapWroteConfig
	// BootstrapGeneratedKey — config.yaml existed but api_key was empty
	// on a loopback bind. Just the api_key was added (existing config
	// preserved).
	BootstrapGeneratedKey
)

// BootstrapResult tells the caller what happened so it can print an
// appropriate one-time banner. APIKey is non-empty whenever the action
// generated one — the caller should display it ONCE to the operator and
// never log it later.
type BootstrapResult struct {
	Action     BootstrapAction
	ConfigPath string
	APIKey     string
}

// EnsureBootstrap is the first-run helper. Safe to call on every boot
// — it's a no-op after the first one because the config file persists.
//
// `cfg` is the in-memory Config from Load (which may have an empty
// APIKey because the file didn't exist). `cfgPath` is the resolved
// config path from Load (which may also point at a not-yet-existing
// file). If EnsureBootstrap generates a key, it MUTATES `cfg` so the
// running gateway uses the key without a reload step.
func EnsureBootstrap(cfg *Config, cfgPath string) (BootstrapResult, error) {
	if cfg == nil {
		return BootstrapResult{}, fmt.Errorf("EnsureBootstrap: nil config")
	}
	if cfgPath == "" {
		// Without a resolved path we have nowhere to write. Defensive:
		// fall back to the workspace location so EnsureBootstrap is
		// still useful when called with a non-resolved path.
		ws, err := ResolveWorkspace()
		if err != nil {
			return BootstrapResult{}, fmt.Errorf("EnsureBootstrap: resolve workspace: %w", err)
		}
		cfgPath = ws.ConfigFile
	}

	// Case 1: config file is missing — write a full default.
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		key, err := generateAPIKey()
		if err != nil {
			return BootstrapResult{}, fmt.Errorf("EnsureBootstrap: generate key: %w", err)
		}
		if err := writeDefaultConfig(cfgPath, key); err != nil {
			return BootstrapResult{}, fmt.Errorf("EnsureBootstrap: write default config: %w", err)
		}
		cfg.Server.APIKey = key
		// Set the host explicitly so the in-memory config matches what
		// we just wrote — without this the running gateway uses
		// viper's defaults which may already be 127.0.0.1 anyway, but
		// the explicitness avoids any timing surprise.
		if cfg.Server.Host == "" {
			cfg.Server.Host = "127.0.0.1"
		}
		if cfg.Server.Port == 0 {
			cfg.Server.Port = 18789
		}
		return BootstrapResult{
			Action:     BootstrapWroteConfig,
			ConfigPath: cfgPath,
			APIKey:     key,
		}, nil
	} else if err != nil {
		return BootstrapResult{}, fmt.Errorf("EnsureBootstrap: stat %s: %w", cfgPath, err)
	}

	// Case 2: config file exists but api_key is empty on a loopback bind.
	// Generate one and patch the file. We deliberately do NOT touch the
	// key on non-loopback binds — the operator may legitimately be
	// running behind a reverse proxy that handles auth upstream, and
	// silently overriding their choice would surprise them.
	if cfg.Server.APIKey == "" && isLoopbackHostName(cfg.Server.Host) {
		key, err := generateAPIKey()
		if err != nil {
			return BootstrapResult{}, fmt.Errorf("EnsureBootstrap: generate key: %w", err)
		}
		if err := patchConfigAPIKey(cfgPath, key); err != nil {
			return BootstrapResult{}, fmt.Errorf("EnsureBootstrap: patch api_key: %w", err)
		}
		cfg.Server.APIKey = key
		return BootstrapResult{
			Action:     BootstrapGeneratedKey,
			ConfigPath: cfgPath,
			APIKey:     key,
		}, nil
	}

	return BootstrapResult{
		Action:     BootstrapNoop,
		ConfigPath: cfgPath,
	}, nil
}

// generateAPIKey returns a `sy_` prefixed hex key with 32 bytes of
// crypto/rand entropy. Format chosen to match the existing `sy_` keys
// the operator may already have in their setup notes / docs.
func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sy_" + hex.EncodeToString(b), nil
}

// writeDefaultConfig writes a complete, runnable config.yaml at path.
// Permissions: 0o600 because the file contains the api_key. The parent
// directory is expected to exist (EnsureDirs runs before this).
func writeDefaultConfig(path, apiKey string) error {
	const tmpl = `# Soulacy gateway configuration.
# Generated on first run. Edit freely — the gateway re-reads on restart.
# Docs: https://soulacy.dev/docs/configuration

server:
  host: 127.0.0.1
  port: 18789
  gui_enabled: true
  # Auto-generated on first run. Rotate any time with:
  #   sed -i '' 's/api_key:.*/api_key: "sy_NEWKEY"/' %s
  api_key: "%s"

runtime:
  max_concurrent_sessions: 100
  default_max_turns: 20
  max_agent_call_depth: 5
  python_bin: "python3"
  tool_timeout: "120s"
  # Sandbox python tools by default. Set enabled: false to disable.
  sandbox:
    enabled: true
    cpu_seconds: 30
    memory_mb: 512
    open_files: 256
    file_size_mb: 64

llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      # Pull this with: ollama pull llama3.3:70b
      model: "llama3.3:70b"

# RAG defaults — sqlite-vec embedded vector store, Ollama embeddings.
# Knowledge bases are created from the GUI (Knowledge page).
knowledge:
  embedding_provider: ollama
  embedding_model: nomic-embed-text
  chunk_size: 1000
  chunk_overlap: 200

# Channel adapters. http is always on; others are opt-in.
# To enable Telegram: set token + agent_id, then restart.
channels:
  http:
    enabled: true

log:
  level: info
  format: console
`
	body := fmt.Sprintf(tmpl, filepath.Base(path), apiKey)
	return os.WriteFile(path, []byte(body), 0o600)
}

// patchConfigAPIKey reads the existing config.yaml, adds (or replaces)
// the server.api_key field, and writes it back. Used in the Case 2 path
// where the operator has a config but no key yet.
//
// We do a textual patch rather than full YAML round-trip because viper
// + yaml.v3's marshal would reformat the file (strip comments, reorder
// keys), which would surprise an operator who carefully laid out their
// config. The patch handles three forms:
//
//  1. `api_key: ""` or `api_key:` under `server:` → replace the line.
//  2. Missing `api_key:` under `server:` → insert one.
//  3. Missing `server:` block entirely → append one.
func patchConfigAPIKey(path, apiKey string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	patched := injectAPIKey(string(body), apiKey)
	if patched == string(body) {
		// No change — defensive; we shouldn't be called when no patch
		// is needed.
		return nil
	}
	return os.WriteFile(path, []byte(patched), 0o600)
}

// injectAPIKey is the pure-string side of patchConfigAPIKey, broken out
// for unit testing.
func injectAPIKey(body, apiKey string) string {
	keyLine := fmt.Sprintf("  api_key: \"%s\"", apiKey)
	// Case 1: existing `api_key:` line — replace value, preserve indent.
	if idx := indexAPIKeyLine(body); idx >= 0 {
		// Walk to end of line and substitute.
		end := idx
		for end < len(body) && body[end] != '\n' {
			end++
		}
		// Preserve the original indentation by reading from line start.
		lineStart := idx
		for lineStart > 0 && body[lineStart-1] != '\n' {
			lineStart--
		}
		indent := body[lineStart:idx]
		return body[:lineStart] + indent + fmt.Sprintf("api_key: \"%s\"", apiKey) + body[end:]
	}
	// Case 2: `server:` block exists but no api_key under it.
	if serverIdx := indexServerBlockStart(body); serverIdx >= 0 {
		// Insert the keyLine right after the `server:` line.
		eol := serverIdx
		for eol < len(body) && body[eol] != '\n' {
			eol++
		}
		insert := "\n" + keyLine
		return body[:eol] + insert + body[eol:]
	}
	// Case 3: no server: block at all — append a fresh block.
	suffix := ""
	if len(body) > 0 && body[len(body)-1] != '\n' {
		suffix = "\n"
	}
	return body + suffix + "\nserver:\n" + keyLine + "\n"
}

// indexAPIKeyLine returns the byte offset of the `api_key:` token in
// body, or -1 if not found. Matches only the standalone field; doesn't
// catch comments or string literals.
func indexAPIKeyLine(body string) int {
	const needle = "api_key:"
	i := 0
	for i < len(body) {
		j := indexOf(body[i:], needle)
		if j < 0 {
			return -1
		}
		abs := i + j
		// Reject matches in comment lines or string values.
		ls := abs
		for ls > 0 && body[ls-1] != '\n' {
			ls--
		}
		line := body[ls:abs]
		isComment := false
		for _, r := range line {
			if r == '#' {
				isComment = true
				break
			}
			if r != ' ' && r != '\t' {
				break
			}
		}
		if !isComment {
			return abs
		}
		i = abs + len(needle)
	}
	return -1
}

// indexServerBlockStart returns the byte offset where the `server:`
// block starts (the 's' of "server:"), or -1 if not found.
func indexServerBlockStart(body string) int {
	const needle = "\nserver:"
	if j := indexOf(body, needle); j >= 0 {
		return j + 1 // skip the leading newline
	}
	if hasPrefix(body, "server:") {
		return 0
	}
	return -1
}

// indexOf / hasPrefix — tiny stdlib replacements so this file doesn't
// drag in strings.Index just for two call sites.
func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// isLoopbackHostName checks whether host is a loopback / unspecified
// address that doesn't need an external auth gateway. Mirrors the
// isLoopbackHost check in internal/app — duplicated here to keep the
// config package free of an app dependency (config is imported by app,
// not the other way around).
func isLoopbackHostName(host string) bool {
	switch host {
	case "", "127.0.0.1", "localhost", "::1", "0.0.0.0":
		// 0.0.0.0 is technically not loopback, but operators who bind
		// it intentionally already accepted the security tradeoff (see
		// internal/app guard). For the api_key bootstrap path we treat
		// it as "loopback-equivalent" so a default install with
		// host=0.0.0.0 still gets a key generated.
		return true
	}
	return false
}
