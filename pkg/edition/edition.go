// Package edition defines the stable product-edition contract shared by the
// open-source Personal distribution and optional commercial distributions.
//
// This package deliberately contains no application wiring. A commercial
// module may depend on it to describe the capabilities it contributes; the
// Personal module never needs to import that commercial module.
package edition

import (
	"fmt"
	"sort"
	"strings"
)

// ID is the stable, persisted identifier for a Soulacy distribution.
type ID string

const (
	Personal ID = "personal"
	Team     ID = "team"
	Scale    ID = "scale"
)

// Capability is a product-level feature supplied by an edition. It is not an
// authorization permission: RBAC still decides whether a particular user may
// perform an action after the edition makes the feature available.
type Capability string

const (
	MultiUser            Capability = "multi_user"
	WorkspaceAdmin       Capability = "workspace_admin"
	CentralizedProviders Capability = "centralized_providers"
	ExternalIdentity     Capability = "external_identity"
	AuditReporting       Capability = "audit_reporting"
	DistributedRuntime   Capability = "distributed_runtime"
	HighAvailability     Capability = "high_availability"
)

// Descriptor is immutable-by-convention distribution metadata. Extends makes
// the commercial layering explicit: Scale extends Team, and Team extends
// Personal. Capabilities contains the effective (inherited) capability set.
type Descriptor struct {
	ID           ID
	DisplayName  string
	Commercial   bool
	Extends      ID
	Capabilities []Capability
}

// Has reports whether the edition supplies capability.
func (d Descriptor) Has(capability Capability) bool {
	for _, candidate := range d.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// Validate rejects malformed descriptors at the composition boundary instead
// of allowing an incorrectly assembled distribution to fail much later.
func (d Descriptor) Validate() error {
	if strings.TrimSpace(string(d.ID)) == "" {
		return fmt.Errorf("edition id is required")
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return fmt.Errorf("edition %q: display name is required", d.ID)
	}
	if d.Extends == d.ID {
		return fmt.Errorf("edition %q: cannot extend itself", d.ID)
	}
	seen := make(map[Capability]struct{}, len(d.Capabilities))
	for _, capability := range d.Capabilities {
		if strings.TrimSpace(string(capability)) == "" {
			return fmt.Errorf("edition %q: empty capability", d.ID)
		}
		if _, exists := seen[capability]; exists {
			return fmt.Errorf("edition %q: duplicate capability %q", d.ID, capability)
		}
		seen[capability] = struct{}{}
	}
	return nil
}

// Registry is the composition-time catalog. Register returns errors for
// duplicate IDs, preventing a commercial extension from silently replacing
// the Personal implementation.
type Registry struct {
	descriptors map[ID]Descriptor
}

func NewRegistry(descriptors ...Descriptor) (*Registry, error) {
	r := &Registry{descriptors: make(map[ID]Descriptor, len(descriptors))}
	for _, descriptor := range descriptors {
		if err := r.Register(descriptor); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Register(descriptor Descriptor) error {
	if r == nil {
		return fmt.Errorf("edition registry is nil")
	}
	if err := descriptor.Validate(); err != nil {
		return err
	}
	if _, exists := r.descriptors[descriptor.ID]; exists {
		return fmt.Errorf("edition %q is already registered", descriptor.ID)
	}
	if descriptor.Extends != "" {
		parent, exists := r.descriptors[descriptor.Extends]
		if !exists {
			return fmt.Errorf("edition %q extends unknown edition %q", descriptor.ID, descriptor.Extends)
		}
		descriptor.Capabilities = append(append([]Capability(nil), parent.Capabilities...), descriptor.Capabilities...)
		descriptor.Capabilities = uniqueCapabilities(descriptor.Capabilities)
	}
	descriptor.Capabilities = append([]Capability(nil), descriptor.Capabilities...)
	r.descriptors[descriptor.ID] = descriptor
	return nil
}

func uniqueCapabilities(capabilities []Capability) []Capability {
	seen := make(map[Capability]struct{}, len(capabilities))
	unique := make([]Capability, 0, len(capabilities))
	for _, capability := range capabilities {
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		unique = append(unique, capability)
	}
	return unique
}

func (r *Registry) Resolve(id ID) (Descriptor, bool) {
	if r == nil {
		return Descriptor{}, false
	}
	descriptor, ok := r.descriptors[id]
	descriptor.Capabilities = append([]Capability(nil), descriptor.Capabilities...)
	return descriptor, ok
}

func (r *Registry) IDs() []ID {
	if r == nil {
		return nil
	}
	ids := make([]ID, 0, len(r.descriptors))
	for id := range r.descriptors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// PersonalDescriptor is the only implementation supplied by the open-source
// distribution. Teams and Scale register their own descriptors from the
// commercial composition root.
func PersonalDescriptor() Descriptor {
	return Descriptor{ID: Personal, DisplayName: "Personal"}
}
