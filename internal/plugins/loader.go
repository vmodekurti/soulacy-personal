// Package plugins implements the Soulacy plugin loader.
//
// Plugins extend Soulacy with new channels, LLM providers, and tool
// libraries without modifying the core codebase. Each plugin is a directory
// with a plugin.yaml manifest and optional Python tool implementations.
//
// Loading is best-effort: a malformed or incompatible plugin logs a warning
// and is skipped; the gateway continues with the remaining plugins.
//
// Plugin directory layout:
//
//	plugins/
//	  my-plugin/
//	    plugin.yaml           ← manifest (pkg/plugin.Manifest)
//	    tools/
//	      some_tool.py        ← Python tool implementations
//
// Tools are exposed to the engine namespaced as plugin__<pluginID>__<name>.
package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/pluginmigrate"
	"github.com/soulacy/soulacy/pkg/plugin"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

// ManifestMigrations converts every loaded plugin's declared migrations
// (Story 17) into the sdk/storage form the E16 runner applies. Order:
// plugins in load order, steps in declaration order.
func ManifestMigrations(all []*LoadedPlugin) []sdkstorage.Migration {
	var out []sdkstorage.Migration
	for _, lp := range all {
		for _, mg := range lp.Manifest.Migrations {
			out = append(out, sdkstorage.Migration{
				PluginID: lp.Manifest.ID,
				Name:     mg.Name,
				UpSQL:    mg.UpSQL,
			})
		}
	}
	return out
}

// LoadedPlugin is the runtime record of a successfully loaded plugin.
type LoadedPlugin struct {
	Manifest plugin.Manifest
	Dir      string
	Tools    []plugin.ToolSpec // all tools contributed by this plugin
	Caps     *caps.Set         // compiled capability set (default-deny; E5)

	// Settings is the plugin's plugins_config section from config.yaml
	// (Story E17; attached by plugins.Wire). Shape is plugin-owned; nil
	// when the operator configured nothing.
	Settings map[string]any
}

// Loader scans plugin directories, parses manifests, and exposes the combined
// tool catalog to the engine. Python tool libraries are the primary extension
// point; Go shared-object plugins are not supported.
type Loader struct {
	mu      sync.RWMutex
	plugins []*LoadedPlugin
	diags   []Diagnostic
	log     *zap.Logger
}

// Diagnostic records one plugin that was refused or skipped during the
// scan (Story E22). The host surfaces these in the Logs GUI so a silently
// absent plugin is always explainable.
type Diagnostic struct {
	Dir    string `json:"dir"`
	Reason string `json:"reason"`
}

// Diagnostics returns the load failures recorded by the last scan.
func (l *Loader) Diagnostics() []Diagnostic {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Diagnostic, len(l.diags))
	copy(out, l.diags)
	return out
}

// New creates a Loader and immediately scans the given directories.
// Missing directories are skipped silently; malformed plugins are warned and skipped.
func New(dirs []string, log *zap.Logger) *Loader {
	l := &Loader{log: log}
	for _, dir := range dirs {
		if err := l.scanDir(dir); err != nil {
			log.Warn("plugins: scan dir failed", zap.String("dir", dir), zap.Error(err))
		}
	}
	return l
}

// scanDir walks one plugin root directory, looking for plugin.yaml files one
// level deep (each immediate subdirectory is one plugin).
func (l *Loader) scanDir(root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil // dir doesn't exist yet — not an error on first run
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pluginDir := filepath.Join(root, e.Name())
		if err := l.loadPlugin(pluginDir); err != nil {
			l.log.Warn("plugins: load plugin failed",
				zap.String("dir", pluginDir),
				zap.Error(err),
			)
			l.mu.Lock()
			l.diags = append(l.diags, Diagnostic{Dir: pluginDir, Reason: err.Error()})
			l.mu.Unlock()
		}
	}
	return nil
}

// loadPlugin reads and validates the manifest in dir, then indexes the plugin's
// tool contributions.
func (l *Loader) loadPlugin(dir string) error {
	manifestPath := filepath.Join(dir, "plugin.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // directory without a manifest is silently ignored
		}
		return fmt.Errorf("read manifest: %w", err)
	}

	var m plugin.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if m.ID == "" {
		return fmt.Errorf("manifest missing required field 'id'")
	}

	// Install gate (Story E13): installer-managed plugins load only while
	// enabled AND their manifest permissions match what the operator
	// approved — an update that widens its grants stops loading until a
	// human re-approves it in the GUI. Hand-installed plugins (no install
	// metadata) are unaffected.
	if v := plugininstall.Gate(dir, m.Permissions, m.Credentials); !v.Load {
		l.log.Warn("plugins: plugin skipped by install state",
			zap.String("id", m.ID), zap.String("reason", v.Reason))
		return nil
	}

	// SDK-major gate (Story E22, extends the E7 schema gate): a plugin
	// built against a newer SDK major must never load against this host's
	// older contracts — refuse with an upgrade hint instead of failing
	// subtly at the first incompatible call.
	if m.SDKMajor > CurrentSDKMajor {
		return fmt.Errorf("sdk_major %d is newer than this host supports (%d); upgrade soulacy or install a plugin version built for SDK v%d",
			m.SDKMajor, CurrentSDKMajor, CurrentSDKMajor)
	}

	// Schema gate (Story E7). Unknown future schemas are skipped outright;
	// v1 manifests keep loading but any v2-only declarations are ignored
	// with a warning; v2 manifests get full contribution validation.
	switch {
	case m.ManifestSchema > SupportedManifestSchema:
		return fmt.Errorf("manifest_schema %d is newer than supported (%d); upgrade soulacy or pin an older plugin version",
			m.ManifestSchema, SupportedManifestSchema)
	case m.ManifestSchema == 2:
		if err := validateManifestV2(m, dir); err != nil {
			return fmt.Errorf("manifest_schema 2 validation: %w", err)
		}
	default: // v1 (absent, 0, or 1)
		warnSkippedV2Contributions(m, l.log)
	}

	// Validate declared capabilities (Story E5). Plugins are default-deny
	// principals; a manifest asking for unknown capabilities or mismatched
	// scopes is refused outright rather than silently narrowed.
	capSet, err := caps.NewSet(m.ID, m.Permissions)
	if err != nil {
		return fmt.Errorf("invalid permissions: %w", err)
	}

	// Validate credential declarations (Story E6): own-namespace vault paths
	// and unique, well-formed env keys. Invalid declarations refuse the
	// plugin — silently spawning a sidecar without its secrets would be worse.
	if err := ValidateCredentialRefs(m.ID, m.Credentials); err != nil {
		return fmt.Errorf("invalid credentials: %w", err)
	}

	// Manifest-declared migrations (Story 17): validate every step against
	// the E16 namespace/statement rules NOW — a plugin whose schema would
	// be refused at apply time must not load at all (warn+skip upstream).
	seenMig := make(map[string]bool, len(m.Migrations))
	for i, mg := range m.Migrations {
		if strings.TrimSpace(mg.Name) == "" || strings.TrimSpace(mg.UpSQL) == "" {
			return fmt.Errorf("migrations[%d]: name and up_sql are required", i)
		}
		if seenMig[mg.Name] {
			return fmt.Errorf("migrations[%d]: duplicate name %q", i, mg.Name)
		}
		seenMig[mg.Name] = true
		if err := pluginmigrate.Validate(m.ID, mg.UpSQL); err != nil {
			return fmt.Errorf("migrations[%d] (%s): %w", i, mg.Name, err)
		}
	}

	lp := &LoadedPlugin{Manifest: m, Dir: dir, Caps: capSet}

	// m.Tools is []string — names of tool libraries declared in the manifest.
	// Build a set so auto-discovery below can skip undeclared files when the
	// manifest is explicit, or include everything when the manifest omits Tools.
	declaredTools := make(map[string]bool, len(m.Tools))
	for _, name := range m.Tools {
		declaredTools[name] = true
	}

	// Auto-discover Python tools in a tools/ subdirectory.
	// Each .py file whose base name (without .py) is either declared in the
	// manifest's tools list OR the manifest declares no tools at all (open
	// discovery) is registered as a ToolSpec with a python: handler.
	toolsDir := filepath.Join(dir, "tools")
	if toolEntries, dirErr := os.ReadDir(toolsDir); dirErr == nil {
		for _, e := range toolEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
				continue
			}
			funcName := strings.TrimSuffix(e.Name(), ".py")
			// Skip if manifest has an explicit tools list and this file is not in it.
			if len(declaredTools) > 0 && !declaredTools[funcName] {
				continue
			}
			pyPath := filepath.Join(toolsDir, e.Name())
			spec := plugin.ToolSpec{
				Name:        funcName,
				Description: extractPythonDocstring(pyPath),
				Handler:     fmt.Sprintf("python:%s::%s", pyPath, funcName),
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			}
			lp.Tools = append(lp.Tools, spec)
		}
	}

	l.mu.Lock()
	l.plugins = append(l.plugins, lp)
	l.mu.Unlock()

	l.log.Info("plugins: loaded",
		zap.String("id", m.ID),
		zap.String("name", m.Name),
		zap.Int("tools", len(lp.Tools)),
	)
	return nil
}

// resolveToolHandler normalises the handler path for a plugin.ToolSpec.
// Relative python: paths are resolved against pluginDir.
func resolveToolHandler(spec *plugin.ToolSpec, pluginDir string) error {
	h := spec.Handler
	if h == "" {
		return nil
	}
	if !strings.HasPrefix(h, "python:") {
		return fmt.Errorf("unsupported handler scheme %q (only python:... is supported)", h)
	}
	rest := strings.TrimPrefix(h, "python:")
	parts := strings.SplitN(rest, "::", 2)
	if len(parts) != 2 {
		return fmt.Errorf("handler %q must be python:<path>::<function>", h)
	}
	pyPath, funcName := parts[0], parts[1]
	if !filepath.IsAbs(pyPath) {
		pyPath = filepath.Join(pluginDir, pyPath)
	}
	if _, err := os.Stat(pyPath); err != nil {
		return fmt.Errorf("handler file %q not found: %w", pyPath, err)
	}
	spec.Handler = fmt.Sprintf("python:%s::%s", pyPath, funcName)
	return nil
}

// All returns a snapshot of all successfully loaded plugins.
func (l *Loader) All() []*LoadedPlugin {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*LoadedPlugin, len(l.plugins))
	copy(out, l.plugins)
	return out
}

// AllTools returns the combined tool catalog from all loaded plugins.
// Tools are namespaced as plugin__<pluginID>__<toolName> to avoid collisions
// with SOUL.yaml tools and MCP tools.
func (l *Loader) AllTools() []plugin.ToolSpec {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []plugin.ToolSpec
	for _, p := range l.plugins {
		for _, t := range p.Tools {
			namespaced := t
			namespaced.Name = fmt.Sprintf("plugin__%s__%s", p.Manifest.ID, t.Name)
			out = append(out, namespaced)
		}
	}
	return out
}

// Count returns the number of successfully loaded plugins.
func (l *Loader) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.plugins)
}

// --- helpers ---

// extractPythonDocstring reads the first triple-quoted docstring from a Python
// file (capped at the first 4 KB). Used to auto-populate ToolSpec.Description
// for auto-discovered Python tools so they show up usefully in the tool catalog.
func extractPythonDocstring(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	src := string(buf[:n])
	for _, q := range []string{`"""`, `'''`} {
		start := strings.Index(src, q)
		if start < 0 {
			continue
		}
		rest := src[start+3:]
		end := strings.Index(rest, q)
		if end < 0 {
			continue
		}
		doc := strings.TrimSpace(rest[:end])
		// Keep just the first paragraph.
		if i := strings.Index(doc, "\n\n"); i >= 0 {
			doc = doc[:i]
		}
		doc = strings.ReplaceAll(doc, "\n", " ")
		if len(doc) > 200 {
			doc = doc[:200] + "…"
		}
		return doc
	}
	return ""
}
