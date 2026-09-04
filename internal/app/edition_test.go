package app

import (
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/pkg/edition"
)

func TestNewAcceptsRegisteredConfiguredEdition(t *testing.T) {
	cfg := &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}}
	descriptor := edition.Descriptor{
		ID: edition.Team, DisplayName: "Teams", Commercial: true, Extends: edition.Personal,
		Capabilities: []edition.Capability{edition.MultiUser},
	}
	a, err := New(cfg, WithEdition(descriptor))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Edition(); got.ID != edition.Team || !got.Has(edition.MultiUser) {
		t.Fatalf("unexpected edition: %#v", got)
	}
}

func TestNewDoesNotImplicitlyCompileCommercialEdition(t *testing.T) {
	cfg := &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}}
	if _, err := New(cfg); err == nil {
		t.Fatal("commercial configuration booted without a registered commercial edition")
	}
}

func TestNewRejectsMismatchedDistribution(t *testing.T) {
	cfg := &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}}
	_, err := New(cfg, WithEdition(edition.PersonalDescriptor()))
	if err == nil {
		t.Fatal("mismatched edition was accepted")
	}
}
