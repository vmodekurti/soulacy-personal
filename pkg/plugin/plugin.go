// Package plugin defines the plugin interface for Soulacy.
// Plugins can contribute channel adapters, LLM providers, memory backends,
// and tool libraries. A plugin is a directory with a plugin.yaml manifest
// and optional Go binary or Python package.
package plugin

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Manifest is loaded from plugin.yaml at the root of a plugin directory.
//
// Two schema versions exist (see docs/EXTENSIBILITY.md §5.5):
//
//   - v1 (manifest_schema absent, 0, or 1): channels/providers are plain
//     string ID lists (informational), tools are Python tool libraries.
//   - v2 (manifest_schema: 2): channels declare runnable sidecars,
//     providers declare OpenAI-compatible endpoints, plus skills
//     directories, GUI mounts, credentials, and permissions.
type Manifest struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
	Author      string `yaml:"author"`
	License     string `yaml:"license"`

	// ManifestSchema selects the manifest grammar. 0/1 = legacy v1; 2 = v2.
	// Loaders must warn-and-skip plugins with schemas they don't know.
	ManifestSchema int `yaml:"manifest_schema,omitempty"`

	// SDKMajor declares the soulacy SDK major version this plugin was built
	// against (Story E22). 0 (unset) and the host's current major load;
	// anything newer is refused at load with an upgrade hint — an SDK-v2
	// plugin must never run against v1 contracts and fail subtly later.
	SDKMajor int `yaml:"sdk_major,omitempty"`

	// What this plugin contributes. ChannelEntry/ProviderEntry parse both
	// the v1 string form ("telegram") and the v2 map form (id + sidecar /
	// openai_compatible).
	Channels  []ChannelEntry  `yaml:"channels,omitempty"`
	Providers []ProviderEntry `yaml:"providers,omitempty"`
	Tools     []string        `yaml:"tools,omitempty"` // Python tool library IDs

	// Skills lists directories (relative to the plugin root) of agent
	// skills to add to the skill loader's search path (v2).
	Skills []string `yaml:"skills,omitempty"`

	// GUI declares a static UI mount rendered by the shell in a sandboxed
	// iframe (v2; serving lands with plugin tokens, E8).
	GUI *GUISpec `yaml:"gui,omitempty"`

	// Python package to install alongside this plugin
	PythonPackage string `yaml:"python_package,omitempty"`

	// Minimum Soulacy version required
	RequiresVersion string `yaml:"requires_version,omitempty"`

	// Permissions declares the host capabilities this plugin requests.
	// Plugins are default-deny security principals: anything not listed here
	// is refused at the host-API boundary. Validation and enforcement live in
	// internal/caps; this is pure manifest data.
	Permissions []Permission `yaml:"permissions,omitempty"`

	// Migrations declares schema steps for installed (non-compiled)
	// plugins (Story 17, manifest v2). Each step runs through the same
	// validation/runner as compiled-in plugin migrations (E16): tables
	// namespaced plugin_<id>_*, transactional, checksummed, applied-once.
	// A plugin whose migrations fail validation is refused at load.
	Migrations []MigrationEntry `yaml:"migrations,omitempty"`

	// Credentials declares the vault secrets this plugin's sidecars need.
	// Only the listed secrets are injected into the sidecar environment at
	// spawn; the vault path namespace must equal the plugin ID. Validation
	// and delegation live in internal/plugins (E6).
	Credentials []CredentialRef `yaml:"credentials,omitempty"`
}

// MigrationEntry is one declared schema step (Story 17).
//
//	migrations:
//	  - name: 001_create_items
//	    up_sql: CREATE TABLE plugin_myid_items (id INTEGER PRIMARY KEY)
type MigrationEntry struct {
	Name  string `yaml:"name" json:"name"`
	UpSQL string `yaml:"up_sql" json:"up_sql"`
}

// CredentialRef requests one vault secret for the plugin's sidecars.
//
//	credentials:
//	  - key: MATRIX_TOKEN        # env var name in the sidecar
//	    from: matrix-suite/token # vault path: <plugin-id>/<secret-key>
//
// Key must be an uppercase env-var name; From is `<namespace>/<key>` where
// the namespace MUST be the plugin's own ID — plugins can never reference
// another plugin's (or an agent's) secrets.
type CredentialRef struct {
	Key  string `yaml:"key" json:"key"`
	From string `yaml:"from" json:"from"`
}

// ---------------------------------------------------------------------------
// Manifest v2 contribution types
// ---------------------------------------------------------------------------

// ChannelEntry is one entry under `channels:`. It accepts the legacy v1
// scalar form (just an adapter ID string) and the v2 map form declaring a
// runnable sidecar (External Channel Protocol,
// docs/EXTERNAL_CHANNEL_PROTOCOL.md).
type ChannelEntry struct {
	ID      string       `yaml:"id" json:"id"`
	AgentID string       `yaml:"agent_id,omitempty" json:"agent_id,omitempty"`
	Sidecar *SidecarSpec `yaml:"sidecar,omitempty" json:"sidecar,omitempty"`
}

// UnmarshalYAML accepts `- telegram` (v1) and `- {id: …, sidecar: …}` (v2).
func (c *ChannelEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		c.ID = node.Value
		return nil
	}
	type raw ChannelEntry // avoid recursion
	var r raw
	if err := node.Decode(&r); err != nil {
		return fmt.Errorf("channel entry: %w", err)
	}
	*c = ChannelEntry(r)
	return nil
}

// SidecarSpec is the subprocess implementing an external channel.
type SidecarSpec struct {
	Command string   `yaml:"command" json:"command"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
}

// ProviderEntry is one entry under `providers:`. Scalar form (v1) names a
// provider ID; map form (v2) declares an OpenAI-compatible endpoint that the
// host wraps with its existing provider implementation.
type ProviderEntry struct {
	ID               string                `yaml:"id" json:"id"`
	OpenAICompatible *OpenAICompatibleSpec `yaml:"openai_compatible,omitempty" json:"openai_compatible,omitempty"`
}

// UnmarshalYAML accepts `- ollama` (v1) and `- {id: …, openai_compatible: …}` (v2).
func (p *ProviderEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		p.ID = node.Value
		return nil
	}
	type raw ProviderEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return fmt.Errorf("provider entry: %w", err)
	}
	*p = ProviderEntry(r)
	return nil
}

// OpenAICompatibleSpec points at any OpenAI-compatible inference endpoint.
// APIKeyEnv names an environment variable of the HOST process holding the
// key (optional for keyless local servers).
type OpenAICompatibleSpec struct {
	BaseURL   string `yaml:"base_url" json:"base_url"`
	APIKeyEnv string `yaml:"api_key_env,omitempty" json:"api_key_env,omitempty"`
	Model     string `yaml:"model,omitempty" json:"model,omitempty"`
}

// GUISpec declares a plugin UI mount (v2). Static is a directory relative to
// the plugin root; Nav describes the shell navigation entry.
type GUISpec struct {
	Nav    NavSpec `yaml:"nav" json:"nav"`
	Static string  `yaml:"static" json:"static"`
}

// NavSpec is the navigation entry for a plugin GUI mount.
type NavSpec struct {
	Label string `yaml:"label" json:"label"`
	Icon  string `yaml:"icon,omitempty" json:"icon,omitempty"`
}

// Permission is one capability grant requested in plugin.yaml.
//
// Cap uses the `resource.action` grammar (e.g. "vector.search",
// "channel.send", "events.subscribe"). Exactly one scope list applies per
// capability — which one is determined by the capability's registered scope
// kind. An empty scope list grants the capability unscoped; "*" as a list
// entry matches any value.
//
//	permissions:
//	  - cap: vector.search
//	    agents: [support-bot]
//	  - cap: channel.send
//	    channels: [matrix]
//	  - cap: events.subscribe
//	    types: [run.finished]
type Permission struct {
	Cap      string   `yaml:"cap" json:"cap"`
	Agents   []string `yaml:"agents,omitempty" json:"agents,omitempty"`
	Channels []string `yaml:"channels,omitempty" json:"channels,omitempty"`
	Types    []string `yaml:"types,omitempty" json:"types,omitempty"`
}

// Plugin is the runtime representation of a loaded plugin.
type Plugin interface {
	// ID returns the unique plugin identifier.
	ID() string

	// Name returns the human-readable name.
	Name() string

	// Init is called once when the plugin is loaded.
	// The registry is passed so the plugin can register its contributions.
	Init(registry Registry) error

	// Shutdown is called when Soulacy is stopping.
	Shutdown() error
}

// Registry is the interface plugins use to register their contributions.
type Registry interface {
	RegisterChannel(id string, factory ChannelFactory) error
	RegisterProvider(id string, factory ProviderFactory) error
	RegisterToolLibrary(id string, lib ToolLibrary) error
}

// ChannelFactory creates a new channel adapter instance given config.
type ChannelFactory func(config map[string]any) (any, error)

// ProviderFactory creates a new LLM provider instance given config.
type ProviderFactory func(config map[string]any) (any, error)

// ToolLibrary is a named set of tools contributed by a plugin.
type ToolLibrary interface {
	Name() string
	Tools() []ToolSpec
}

// ToolSpec describes a single tool in a library.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Handler     string         `json:"handler"` // "python:path/to/file.py::function_name"
}
