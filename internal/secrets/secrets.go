// Package secrets provides a single, gateway-global secrets store layered on
// top of the encrypted credential vault (internal/credentials). All secret
// values live only at runtime in the encrypted vault under the workspace
// (~/.soulacy/soulspace/credentials.db) — never in plaintext config once
// migrated.
//
// Resolution precedence for any secret is: vault → environment → config file.
// The Manager can also overlay vault values onto an in-memory *config.Config at
// startup (vault wins), and migrate plaintext secrets out of config.yaml into
// the vault on first run.
//
// Vault keys use the secret's canonical name, which for structured secrets is
// the dotted config path (e.g. "llm.providers.anthropic.api_key",
// "channels.slack.bot_token", "server.api_key"). Custom/tool secrets use a
// free-form name the operator chooses (e.g. "ALPHAVANTAGE_API_KEY").
package secrets

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/credentials"
)

// GlobalScope is the reserved vault agentID under which gateway-global secrets
// are stored. Real agent IDs never collide with it (leading/trailing
// underscores are not valid agent IDs).
const GlobalScope = "__global__"

// Category groups secrets for display.
type Category string

const (
	CategoryLLM     Category = "llm"
	CategoryChannel Category = "channel"
	CategoryServer  Category = "server"
	CategoryTool    Category = "tool"
)

// Descriptor describes one known secret slot. Value is never included.
type Descriptor struct {
	Name        string   `json:"name"`        // canonical name == vault key (dotted path for structured)
	Category    Category `json:"category"`    // llm | channel | server | tool
	EnvVar      string   `json:"env_var"`     // environment fallback, if any
	Description string   `json:"description"` // human-friendly label
	// Set reports whether this secret RESOLVES to a value — by the same
	// vault → env → config precedence Resolve uses and this package documents.
	//
	// It used to mean "present in the vault" alone, which made readiness refuse
	// to save or run for anyone who had configured a key by environment variable
	// or plaintext config. The resulting blocker was unclearable by its own
	// instructions ("add the credential in Settings → Secrets" — it was already
	// there), and the same preflight simultaneously reported the provider as
	// available, because provider registration DOES honour the other two legs.
	Set bool `json:"set"`
	// Source says where the value came from: "vault", "env", "config", or "" when
	// unset. Set alone cannot distinguish "safely in the vault" from "sitting in
	// plaintext config", which is a real difference the secrets UI should keep
	// showing — and is what the migration prompt keys on.
	Source string `json:"source,omitempty"`
}

// Secret sources, in resolution order.
const (
	SourceVault  = "vault"
	SourceEnv    = "env"
	SourceConfig = "config"
)

// Manager wraps the credential vault for global-scope secret operations. It is
// nil-safe: a Manager built from a nil vault degrades gracefully (Get returns
// not-found, mutating ops return ErrNoVault) so the gateway still runs.
type Manager struct {
	vault credentials.Vault
}

// New returns a Manager over the given vault. vault may be nil.
func New(vault credentials.Vault) *Manager { return &Manager{vault: vault} }

// Enabled reports whether a backing vault is available.
func (m *Manager) Enabled() bool { return m != nil && m.vault != nil }

// Set stores (or overwrites) a secret value in the vault.
func (m *Manager) Set(ctx context.Context, name, value string) error {
	if !m.Enabled() {
		return ErrNoVault
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}
	return m.vault.Set(ctx, GlobalScope, name, []byte(value))
}

// Get returns the secret value and whether it was present.
func (m *Manager) Get(ctx context.Context, name string) (string, bool) {
	if !m.Enabled() {
		return "", false
	}
	b, err := m.vault.Get(ctx, GlobalScope, name)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Delete removes a secret. Deleting an absent secret is not an error.
func (m *Manager) Delete(ctx context.Context, name string) error {
	if !m.Enabled() {
		return ErrNoVault
	}
	return m.vault.Delete(ctx, GlobalScope, name)
}

// List returns the names of all stored global secrets, sorted.
func (m *Manager) List(ctx context.Context) ([]string, error) {
	if !m.Enabled() {
		return nil, nil
	}
	names, err := m.vault.List(ctx, GlobalScope)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// Catalog returns the known + custom secret descriptors for the given config,
// each marked with whether a value RESOLVES (vault → env → config, the same
// precedence Resolve implements) and where from. Structured slots (LLM
// providers, channel tokens, server key) are derived from cfg; any additional
// vault entries are surfaced as CategoryTool ("custom").
func (m *Manager) Catalog(ctx context.Context, cfg *config.Config) []Descriptor {
	stored := map[string]bool{}
	if names, err := m.List(ctx); err == nil {
		for _, n := range names {
			stored[n] = true
		}
	}

	// The other two legs of the documented precedence. Without these, a key held
	// in an environment variable or still in plaintext config reads as missing.
	cfgVals := secretValuesInConfig(cfg)

	var out []Descriptor
	seen := map[string]bool{}
	add := func(d Descriptor) {
		if seen[d.Name] {
			return
		}
		seen[d.Name] = true
		switch {
		case stored[d.Name]:
			d.Set, d.Source = true, SourceVault
		case d.EnvVar != "" && strings.TrimSpace(os.Getenv(d.EnvVar)) != "":
			d.Set, d.Source = true, SourceEnv
		case strings.TrimSpace(cfgVals[d.Name]) != "":
			d.Set, d.Source = true, SourceConfig
		default:
			d.Set, d.Source = false, ""
		}
		out = append(out, d)
	}

	for _, d := range structuredDescriptors(cfg) {
		add(d)
	}
	// Surface any stored secret that isn't a known structured slot as a custom
	// (tool) secret so it's visible/manageable in CLI + GUI.
	for name := range stored {
		if seen[name] {
			continue
		}
		add(Descriptor{Name: name, Category: CategoryTool, Description: name})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}
