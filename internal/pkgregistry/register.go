package pkgregistry

// Built-in package-registry providers self-register with the SDK factory
// registry (Story E19, same E10 pattern as channels/providers/queues/
// vectors/reasoning). Linked into the binary via the generated blank-import
// file cmd/soulacy/builtins_gen.go.

import (
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
	"github.com/soulacy/soulacy/sdk/registry"
)

func init() {
	registry.MustRegisterPkgRegistry("http", func(cfg map[string]any) (sdkpkg.Provider, error) {
		return newHTTPProvider(cfg)
	})
	registry.MustRegisterPkgRegistry("git", func(cfg map[string]any) (sdkpkg.Provider, error) {
		return newGitProvider(cfg)
	})
	// skills.sh-style directories (Story E26): search + inline file trees +
	// partner security audits.
	registry.MustRegisterPkgRegistry("skillssh", func(cfg map[string]any) (sdkpkg.Provider, error) {
		return newSkillsShProvider(cfg)
	})
}
