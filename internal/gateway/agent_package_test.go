package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Story 7 Bucket 7A — calendar-versioning validation, namespaced package_id
// validation, missing-requirements filter, and .soulacy-package.json sidecar
// writer. All pure helpers; no gateway wiring needed.

func TestValidateCalendarVersion(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"2026.07.14", true},
		{"2026.07.14.0", true},
		{"2026.07.14.1", true},
		{"2026.07.14.42", true},
		{"2026.7.14", false},  // no zero-padding
		{"26.07.14", false},   // wrong year width
		{"2026.13.01", false}, // bad month
		{"2026.07.32", false}, // bad day
		{"1.4.2", false},      // semver, not calver
		{"2026.07.14-rc.1", false},
	}
	for _, tc := range cases {
		err := validateCalendarVersion(tc.in)
		got := err == nil
		if got != tc.want {
			t.Errorf("validateCalendarVersion(%q) ok=%v want=%v (err=%v)", tc.in, got, tc.want, err)
		}
	}
}

func TestValidatePackageNamespace(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"soulacy/hackernews-digest", true},
		{"vasu/my-agent", true},
		{"acme-corp/production-2", true},
		{"a/b", false},                     // publisher must be 2-32 chars — "a" fails
		{"soulacy", false},                 // no namespace
		{"soulacy/", false},                // no package
		{"/foo", false},                    // no publisher
		{"Soulacy/foo", false},             // uppercase publisher
		{"soulacy/UPPER", false},           // uppercase package
		{"soulacy/foo/bar", false},         // triple segment
		{"soulacy/1foo-bar", true},         // digit start on package is allowed
		{"soulacy/-leading-hyphen", false}, // package starts with hyphen
	}
	for _, tc := range cases {
		err := validatePackageNamespace(tc.in)
		got := err == nil
		if got != tc.want {
			t.Errorf("validatePackageNamespace(%q) ok=%v want=%v (err=%v)", tc.in, got, tc.want, err)
		}
	}
}

func TestCollectMissingRequirements(t *testing.T) {
	in := []agentPackageRequirement{
		{Kind: "provider", Name: "ollama", Status: "available"}, // legacy — filtered out
		{Kind: "required_secret", Name: "telegram token", Status: "missing"},
		{Kind: "required_provider", Name: "openai", Status: "missing"},
		{Kind: "required_channel", Name: "slack", Status: "available"},
		{Kind: "required_peer_agent", Name: "librarian", Status: "missing"},
		{Kind: "required_channel", Name: "http", Status: "built_in"},
		{Kind: "required_mcp_server", Name: "playwright", Status: "declared"}, // NOT available — should surface
	}
	out := collectMissingRequirements(in)
	if len(out) != 4 {
		t.Fatalf("expected 4 missing, got %d: %+v", len(out), out)
	}
	names := map[string]bool{}
	for _, r := range out {
		names[r.Name] = true
	}
	for _, want := range []string{"telegram token", "openai", "librarian", "playwright"} {
		if !names[want] {
			t.Errorf("expected missing entry for %q, got %+v", want, out)
		}
	}
}

func TestWritePackageSidecar(t *testing.T) {
	dir := t.TempDir()
	pkg := &agentPackageResponse{
		SchemaVersion: PackageSchemaV2,
		Manifest: agentPackageManifest{
			AgentID:   "hackernews-digest",
			Name:      "HN Digest",
			PackageID: "soulacy/hackernews-digest",
			Version:   "2026.07.14",
			Publisher: &agentPackagePublisher{
				ID:          "soulacy",
				DisplayName: "Soulacy Core",
				TrustLevel:  "official",
			},
		},
		Integrity: agentPackageIntegrity{Verified: true},
	}
	if err := writePackageSidecar(dir, "hackernews-digest", pkg, false); err != nil {
		t.Fatalf("writePackageSidecar failed: %v", err)
	}
	sidecar := filepath.Join(dir, "hackernews-digest", ".soulacy-package.json")
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	var sc packageSidecar
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("sidecar parse: %v", err)
	}
	if sc.SchemaVersion != PackageSchemaV2 || sc.InstalledVersion != "2026.07.14" {
		t.Errorf("unexpected sidecar contents: %+v", sc)
	}
	if sc.Publisher == nil || sc.Publisher.ID != "soulacy" {
		t.Errorf("publisher not carried into sidecar: %+v", sc)
	}
	if sc.TrustLevelAtInstall != "official" {
		t.Errorf("trust level not persisted: %q", sc.TrustLevelAtInstall)
	}
	if !sc.SignatureVerified {
		t.Errorf("signature_verified should propagate from integrity.verified")
	}
	if sc.InstalledAt.IsZero() {
		t.Errorf("installed_at should be set to now, got zero")
	}
	if strings.TrimSpace(string(raw)) == "" {
		t.Errorf("sidecar file is empty")
	}
}

func TestPackageV1CutoffTime(t *testing.T) {
	cutoff := packageV1CutoffTime()
	want := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	if !cutoff.Equal(want) {
		t.Errorf("packageV1CutoffTime = %s, want %s", cutoff, want)
	}
}
