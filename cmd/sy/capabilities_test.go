package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/apiversion"
)

func TestVersionCommandIsRegisteredAndSupportsJSON(t *testing.T) {
	root := buildRoot()
	cmd, _, err := root.Find([]string{"version"})
	if err != nil || cmd == nil || cmd.Name() != "version" {
		t.Fatalf("missing sy version: %v", err)
	}
	if root.PersistentFlags().Lookup("idempotency-key") == nil {
		t.Fatal("the global --idempotency-key flag is missing")
	}
}

// An incompatibility that does not say what to run is a dead end for the
// operator who hits it.
func TestIncompatibilityIsRenderedWithVersionsAndARemedy(t *testing.T) {
	typed := &apiversion.IncompatibleError{
		Code: apiversion.CodeClientTooOld, Message: "this server requires CLI 0.2.0 or newer; you are running 0.1.9",
		Remedy: "run 'sy upgrade'", ClientVersion: "0.1.9", ServerVersion: "0.2.0",
	}
	rendered, ok := describeIncompatibility(typed)
	if !ok {
		t.Fatal("a typed incompatibility was not recognized")
	}
	for _, want := range []string{"0.1.9", "0.2.0", "sy upgrade", "fix:"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered incompatibility is missing %q:\n%s", want, rendered)
		}
	}
	if _, ok := describeIncompatibility(errors.New("connection refused")); ok {
		t.Error("an ordinary error was misreported as an incompatibility")
	}
}

// A server too old to publish capabilities must keep working. Refusing there
// would break every existing deployment the day this ships.
func TestHandshakeIsPermissiveWhenCapabilitiesAreUnavailable(t *testing.T) {
	original := cachedCaps
	t.Cleanup(func() { cachedCaps = original })

	capabilitiesOnce.Do(func() {}) // pin the cache without a network call
	cachedCaps, cachedCapsErr = apiversion.Capabilities{}, nil
	if err := requireServerFeature(apiversion.FeatureScopedCredentials, "issuing a credential"); err != nil {
		t.Fatalf("an empty capability document blocked an operation: %v", err)
	}

	cachedCaps = apiversion.Describe("0.2.0", "team")
	if err := requireServerFeature(apiversion.FeatureScopedCredentials, "issuing a credential"); err != nil {
		t.Fatalf("a supported feature was refused: %v", err)
	}

	cachedCaps = apiversion.Capabilities{APIVersion: apiversion.APIVersion, ServerVersion: "0.2.0", Features: []string{apiversion.FeatureOIDCLogin}}
	err := requireServerFeature(apiversion.FeatureScopedCredentials, "issuing a credential")
	var typed *apiversion.IncompatibleError
	if !errors.As(err, &typed) || typed.Code != apiversion.CodeFeatureMissing {
		t.Fatalf("an unsupported feature was allowed through: %v", err)
	}
}
