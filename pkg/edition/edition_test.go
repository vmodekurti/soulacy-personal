package edition

import "testing"

func TestRegistryDoesNotAllowEditionReplacement(t *testing.T) {
	registry, err := NewRegistry(PersonalDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(PersonalDescriptor()); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
}

func TestRegistryReturnsDefensiveDescriptorCopy(t *testing.T) {
	descriptor := Descriptor{ID: Team, DisplayName: "Teams", Commercial: true, Capabilities: []Capability{MultiUser}}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := registry.Resolve(Team)
	if !ok || !resolved.Has(MultiUser) {
		t.Fatal("registered capability was not resolved")
	}
	resolved.Capabilities[0] = HighAvailability
	again, _ := registry.Resolve(Team)
	if !again.Has(MultiUser) {
		t.Fatal("caller's mutation escaped into registry")
	}
}

func TestDescriptorValidation(t *testing.T) {
	tests := []Descriptor{
		{DisplayName: "missing id"},
		{ID: Team},
		{ID: Team, DisplayName: "self", Extends: Team},
		{ID: Team, DisplayName: "duplicate", Capabilities: []Capability{MultiUser, MultiUser}},
	}
	for _, descriptor := range tests {
		if err := descriptor.Validate(); err == nil {
			t.Fatalf("expected validation error for %#v", descriptor)
		}
	}
}

func TestRegistryRequiresParentAndInheritsCapabilities(t *testing.T) {
	registry, err := NewRegistry(PersonalDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Descriptor{ID: Scale, DisplayName: "Scale", Extends: Team}); err == nil {
		t.Fatal("missing parent registration succeeded")
	}
	if err := registry.Register(Descriptor{
		ID: Team, DisplayName: "Teams", Extends: Personal, Capabilities: []Capability{MultiUser},
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Descriptor{
		ID: Scale, DisplayName: "Scale", Extends: Team, Capabilities: []Capability{HighAvailability},
	}); err != nil {
		t.Fatal(err)
	}
	scale, _ := registry.Resolve(Scale)
	if !scale.Has(MultiUser) || !scale.Has(HighAvailability) {
		t.Fatalf("inherited capabilities missing: %#v", scale.Capabilities)
	}
}
