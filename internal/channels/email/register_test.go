package email

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

// The adapter is useless if the binary never registers it. This asserts the
// init() side effect actually landed in the registry and that the factory can
// build a working adapter from a plain config map.
func TestRegistry_EmailIsRegistered(t *testing.T) {
	a, ok, err := registry.NewChannel("email", map[string]any{
		"host":              "smtp.example.com",
		"port":              587,
		"username":          "me@example.com",
		"password":          "app-password",
		"default_output_to": "you@example.com",
	})
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}
	if !ok {
		t.Fatal(`"email" is not registered — the binary will not offer this channel`)
	}
	if a.ID() != "email" {
		t.Errorf("ID() = %q, want %q", a.ID(), "email")
	}
}

// A config the user is likely to get wrong must fail at construction, not at
// 3am when the first scheduled briefing tries to send.
func TestRegistry_MissingHostFailsAtConstruction(t *testing.T) {
	if _, _, err := registry.NewChannel("email", map[string]any{
		"username": "me@example.com",
	}); err == nil {
		t.Fatal("a config with no SMTP host must be rejected up front")
	}
}
