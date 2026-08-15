package apiversion

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCapabilitiesAreStableAndComplete(t *testing.T) {
	caps := Describe("0.2.0", "team")
	if caps.APIVersion != APIVersion || caps.ServerVersion != "0.2.0" || caps.DeploymentMode != "team" {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
	for _, feature := range []string{
		FeatureWorkspaceContexts, FeatureScopedCredentials, FeatureServiceAccounts,
		FeatureMemberManagement, FeatureIdempotentMutation, FeatureConcurrencyControl, FeatureOIDCLogin,
	} {
		if !caps.Has(feature) {
			t.Errorf("capability %q is not advertised", feature)
		}
	}
	// The payload must be byte-stable across calls so a client can cache it
	// and so two bug reports from the same build are comparable.
	first, _ := json.Marshal(Describe("0.2.0", "team"))
	second, _ := json.Marshal(Describe("0.2.0", "team"))
	if string(first) != string(second) {
		t.Fatalf("capability payload is not stable:\n%s\n%s", first, second)
	}
}

func TestClientTooOldIsRejectedWithActionableGuidance(t *testing.T) {
	caps := Describe("0.2.0", "team")
	caps.MinCLIVersion = "0.2.0"
	err := caps.CheckClient("0.1.9")
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("expected a typed incompatibility, got %v", err)
	}
	if incompatible.Code != CodeClientTooOld {
		t.Fatalf("code = %q", incompatible.Code)
	}
	if strings.TrimSpace(incompatible.Remedy) == "" {
		t.Fatal("an incompatibility with no remedy is just a dead end")
	}
	if !strings.Contains(incompatible.Message, "0.1.9") || !strings.Contains(incompatible.Message, "0.2.0") {
		t.Fatalf("message does not name both versions: %q", incompatible.Message)
	}
}

func TestClientTooNewIsRejectedWhenAnUpperBoundIsSet(t *testing.T) {
	caps := Describe("0.2.0", "team")
	caps.MaxCLIVersion = "0.3.0"
	if err := caps.CheckClient("0.3.0"); err != nil {
		t.Fatalf("the boundary version itself must be accepted: %v", err)
	}
	var incompatible *IncompatibleError
	if err := caps.CheckClient("0.4.0"); !errors.As(err, &incompatible) || incompatible.Code != CodeClientTooNew {
		t.Fatalf("expected client_too_new, got %v", err)
	}
}

// An unbounded MaxCLIVersion is the shipped default: a newer CLI negotiates
// features rather than assuming them, so it must not be locked out.
func TestNewerClientsAreAllowedByDefault(t *testing.T) {
	caps := Describe("0.2.0", "team")
	if caps.MaxCLIVersion != "" {
		t.Fatalf("MaxCLIVersion defaults to %q; it should be unbounded", caps.MaxCLIVersion)
	}
	if err := caps.CheckClient("99.0.0"); err != nil {
		t.Fatalf("a much newer client was rejected by default: %v", err)
	}
}

// Blocking a developer's own build would make this check hostile to exactly
// the people most likely to trip it.
func TestDevelopmentBuildsAreNotBlocked(t *testing.T) {
	caps := Describe("0.2.0", "team")
	caps.MinCLIVersion, caps.MaxCLIVersion = "1.0.0", "2.0.0"
	for _, version := range []string{"dev", "", "   ", "nightly-2026-08-15"} {
		if err := caps.CheckClient(version); err != nil {
			t.Errorf("development build %q was blocked: %v", version, err)
		}
	}
}

func TestFeatureHandshakeNamesTheMissingCapability(t *testing.T) {
	caps := Describe("0.2.0", "team")
	if err := caps.RequireFeature(FeatureScopedCredentials, "issuing a credential"); err != nil {
		t.Fatalf("a supported feature was refused: %v", err)
	}
	caps.Features = []string{FeatureOIDCLogin}
	err := caps.RequireFeature(FeatureIdempotentMutation, "submitting a run")
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) || incompatible.Code != CodeFeatureMissing {
		t.Fatalf("expected feature_not_supported, got %v", err)
	}
	if incompatible.Feature != FeatureIdempotentMutation || !strings.Contains(incompatible.Message, "submitting a run") {
		t.Fatalf("error does not identify the feature or the operation: %+v", incompatible)
	}
	if strings.TrimSpace(incompatible.Remedy) == "" {
		t.Fatal("missing remedy")
	}
}

func TestAPIVersionMismatchIsTypedAndEmptyClientIsTolerated(t *testing.T) {
	caps := Describe("0.2.0", "team")
	if err := caps.CheckAPIVersion(""); err != nil {
		t.Fatalf("a client that does not declare an API version must be tolerated: %v", err)
	}
	if err := caps.CheckAPIVersion(APIVersion); err != nil {
		t.Fatalf("matching API version rejected: %v", err)
	}
	var incompatible *IncompatibleError
	if err := caps.CheckAPIVersion("v2"); !errors.As(err, &incompatible) || incompatible.Code != CodeAPIMismatch {
		t.Fatalf("expected api_version_mismatch, got %v", err)
	}
}

func TestSemverParsingHandlesPrefixesSuffixesAndPartialVersions(t *testing.T) {
	cases := map[string]struct {
		input string
		want  semver
		ok    bool
	}{
		"plain":         {"1.2.3", semver{1, 2, 3}, true},
		"v prefix":      {"v1.2.3", semver{1, 2, 3}, true},
		"pre-release":   {"v0.2.0-rc1", semver{0, 2, 0}, true},
		"build meta":    {"0.2.0+deadbeef", semver{0, 2, 0}, true},
		"major.minor":   {"1.2", semver{1, 2, 0}, true},
		"major only":    {"3", semver{3, 0, 0}, true},
		"dev":           {"dev", semver{}, false},
		"empty":         {"", semver{}, false},
		"too many":      {"1.2.3.4", semver{}, false},
		"negative":      {"1.-2.3", semver{}, false},
		"not a number":  {"1.x.3", semver{}, false},
		"leading space": {"  1.2.3  ", semver{1, 2, 3}, true},
	}
	for name, tc := range cases {
		got, ok := parseSemver(tc.input)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: parseSemver(%q) = %+v,%v want %+v,%v", name, tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSemverOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.9.9", 1},
		{"0.1.0", "0.2.0", -1},
		{"0.9.9", "1.0.0", -1},
	}
	for _, tc := range cases {
		a, _ := parseSemver(tc.a)
		b, _ := parseSemver(tc.b)
		if got := compare(a, b); got != tc.want {
			t.Errorf("compare(%s,%s) = %d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// A client that does not know a field must ignore it rather than fail. This
// pins the additive-compatibility promise for the capability payload itself.
func TestUnknownCapabilityFieldsDoNotBreakDecoding(t *testing.T) {
	payload := `{"api_version":"v1","server_version":"9.9.9","features":["oidc-login"],"min_cli_version":"0.1.0","some_future_field":{"nested":true}}`
	var caps Capabilities
	if err := json.Unmarshal([]byte(payload), &caps); err != nil {
		t.Fatalf("an additive server field broke an older client: %v", err)
	}
	if caps.ServerVersion != "9.9.9" || !caps.Has(FeatureOIDCLogin) {
		t.Fatalf("known fields were lost: %+v", caps)
	}
}
