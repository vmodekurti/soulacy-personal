// manifest2.go — plugin manifest v2 validation and contribution accessors
// (Story E7, docs/EXTENSIBILITY.md §5.5).
//
// Schema handling:
//   - manifest_schema 0/1 (absent = legacy): Python tools only. Any v2-only
//     declarations (sidecar channels, openai_compatible providers, skills,
//     gui) are warned and skipped — the plugin itself keeps loading, so old
//     installations never break.
//   - manifest_schema 2: full validation; a malformed contribution refuses
//     the plugin with a precise error.
//   - anything newer: warn-and-skip the whole plugin (we will not guess at
//     grammars from the future).
package plugins

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/plugin"
)

// CurrentSDKMajor is the soulacy SDK major version this host implements
// (Story E22). Plugins declaring a newer sdk_major are refused at load.
const CurrentSDKMajor = 1

// SupportedManifestSchema is the newest manifest grammar this build understands.
const SupportedManifestSchema = 2

// validateManifestV2 checks every v2 contribution. dir is the plugin root
// (for skills/gui path checks).
func validateManifestV2(m plugin.Manifest, dir string) error {
	for i, ch := range m.Channels {
		if ch.ID == "" {
			return fmt.Errorf("channels[%d]: missing required field 'id'", i)
		}
		if ch.Sidecar == nil {
			return fmt.Errorf("channels[%d] (%s): manifest_schema 2 channels must declare a sidecar", i, ch.ID)
		}
		if ch.Sidecar.Command == "" {
			return fmt.Errorf("channels[%d] (%s): sidecar.command is required", i, ch.ID)
		}
		if ch.AgentID == "" {
			return fmt.Errorf("channels[%d] (%s): agent_id is required (the agent that receives this channel's messages)", i, ch.ID)
		}
	}
	for i, p := range m.Providers {
		if p.ID == "" {
			return fmt.Errorf("providers[%d]: missing required field 'id'", i)
		}
		if p.OpenAICompatible == nil {
			return fmt.Errorf("providers[%d] (%s): manifest_schema 2 providers must declare openai_compatible", i, p.ID)
		}
		if p.OpenAICompatible.BaseURL == "" {
			return fmt.Errorf("providers[%d] (%s): openai_compatible.base_url is required", i, p.ID)
		}
	}
	for i, s := range m.Skills {
		p := filepath.Join(dir, s)
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("skills[%d]: %q is not a directory under the plugin root", i, s)
		}
	}
	if m.GUI != nil {
		if m.GUI.Static == "" {
			return fmt.Errorf("gui: 'static' directory is required")
		}
		p := filepath.Join(dir, m.GUI.Static)
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("gui: static directory %q does not exist under the plugin root", m.GUI.Static)
		}
		if m.GUI.Nav.Label == "" {
			return fmt.Errorf("gui: nav.label is required")
		}
	}
	return nil
}

// warnSkippedV2Contributions logs v2-only declarations found in a v1
// manifest. They are ignored (no breakage), never guessed at.
func warnSkippedV2Contributions(m plugin.Manifest, log *zap.Logger) {
	for _, ch := range m.Channels {
		if ch.Sidecar != nil {
			log.Warn("plugins: sidecar channel requires manifest_schema 2; skipped",
				zap.String("plugin", m.ID), zap.String("channel", ch.ID))
		}
	}
	for _, p := range m.Providers {
		if p.OpenAICompatible != nil {
			log.Warn("plugins: openai_compatible provider requires manifest_schema 2; skipped",
				zap.String("plugin", m.ID), zap.String("provider", p.ID))
		}
	}
	if len(m.Skills) > 0 {
		log.Warn("plugins: skills directories require manifest_schema 2; skipped",
			zap.String("plugin", m.ID))
	}
	if m.GUI != nil {
		log.Warn("plugins: gui mount requires manifest_schema 2; skipped",
			zap.String("plugin", m.ID))
	}
}

// isV2 reports whether the plugin declared (and validated as) schema v2.
func (lp *LoadedPlugin) isV2() bool {
	return lp.Manifest.ManifestSchema == 2
}

// SidecarChannels returns the validated sidecar channel declarations of a
// v2 plugin (empty for v1 manifests — their channel strings are
// informational only).
func (lp *LoadedPlugin) SidecarChannels() []plugin.ChannelEntry {
	if !lp.isV2() {
		return nil
	}
	out := make([]plugin.ChannelEntry, 0, len(lp.Manifest.Channels))
	for _, ch := range lp.Manifest.Channels {
		if ch.Sidecar != nil {
			out = append(out, ch)
		}
	}
	return out
}

// OpenAIProviders returns the validated provider declarations of a v2 plugin.
func (lp *LoadedPlugin) OpenAIProviders() []plugin.ProviderEntry {
	if !lp.isV2() {
		return nil
	}
	out := make([]plugin.ProviderEntry, 0, len(lp.Manifest.Providers))
	for _, p := range lp.Manifest.Providers {
		if p.OpenAICompatible != nil {
			out = append(out, p)
		}
	}
	return out
}

// SkillDirs returns absolute paths of the plugin's skills directories (v2).
func (lp *LoadedPlugin) SkillDirs() []string {
	if !lp.isV2() {
		return nil
	}
	out := make([]string, 0, len(lp.Manifest.Skills))
	for _, s := range lp.Manifest.Skills {
		out = append(out, filepath.Join(lp.Dir, s))
	}
	return out
}

// GUIMount returns the absolute static dir and nav spec of the plugin's GUI
// mount, or ok=false when none is declared (or the manifest is v1).
func (lp *LoadedPlugin) GUIMount() (staticDir string, nav plugin.NavSpec, ok bool) {
	if !lp.isV2() || lp.Manifest.GUI == nil {
		return "", plugin.NavSpec{}, false
	}
	return filepath.Join(lp.Dir, lp.Manifest.GUI.Static), lp.Manifest.GUI.Nav, true
}
