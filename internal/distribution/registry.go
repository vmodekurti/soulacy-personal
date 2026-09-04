// Package distribution is the composition root for the currently built
// Soulacy distribution. The public Personal repository contains this registry;
// optional commercial source registers additive editions from separate files.
package distribution

import (
	"fmt"

	"github.com/soulacy/soulacy/pkg/edition"
)

var registry = mustPersonalRegistry()

func mustPersonalRegistry() *edition.Registry {
	registry, err := edition.NewRegistry(edition.PersonalDescriptor())
	if err != nil {
		panic(err)
	}
	return registry
}

// Register is the narrow distribution extension point. It deliberately
// exposes edition metadata only; service implementations are composed through
// their own ports as they are extracted from the current monolith.
func Register(descriptor edition.Descriptor) error {
	return registry.Register(descriptor)
}

// Resolve returns a registered edition or an actionable composition error.
func Resolve(id edition.ID) (edition.Descriptor, error) {
	descriptor, ok := registry.Resolve(id)
	if !ok {
		return edition.Descriptor{}, fmt.Errorf("edition %q is not compiled into this distribution", id)
	}
	return descriptor, nil
}
