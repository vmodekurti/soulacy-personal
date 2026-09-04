package registry

// Package-registry provider registry (Story E19). Follows the E10 factory
// pattern: providers self-register from init(), hosts resolve each
// config.yaml `registries:` entry by its `type` key.

import "github.com/soulacy/soulacy/sdk/pkgregistry"

// PkgRegistryFactory builds a pkgregistry.Provider from one `registries:`
// config entry (schemaless map — the host passes the YAML block verbatim
// plus the entry's `id`).
type PkgRegistryFactory func(cfg map[string]any) (pkgregistry.Provider, error)

var pkgRegistries registry[PkgRegistryFactory]

// RegisterPkgRegistry registers a package-registry provider factory under
// name (the config `type` key, e.g. "http", "git"). Duplicate names and nil
// factories error (call from init(); treat a non-nil error as a programmer
// mistake).
func RegisterPkgRegistry(name string, f PkgRegistryFactory) error {
	return pkgRegistries.register("package registry", name, f, f == nil)
}

// MustRegisterPkgRegistry is RegisterPkgRegistry that panics on error — the
// idiomatic form inside driver init() functions.
func MustRegisterPkgRegistry(name string, f PkgRegistryFactory) {
	if err := RegisterPkgRegistry(name, f); err != nil {
		panic(err)
	}
}

// NewPkgRegistry instantiates the named provider type ("" or unknown names
// report ok=false so hosts can warn and skip the config entry).
func NewPkgRegistry(name string, cfg map[string]any) (pkgregistry.Provider, bool, error) {
	f, ok := pkgRegistries.lookup(name)
	if !ok {
		return nil, false, nil
	}
	p, err := f(cfg)
	return p, true, err
}

// PkgRegistries lists registered provider type names (sorted).
func PkgRegistries() []string { return pkgRegistries.names() }
