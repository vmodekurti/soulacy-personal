package gateway

import (
	"fmt"
	"testing"
	"time"
)

func TestScreenshotManifestFreshness(t *testing.T) {
	fresh := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	generated, ageHours, ok := screenshotManifestFreshness(fmt.Sprintf(`{"generated_at":%q,"routes":[{"name":"dashboard"}]}`, fresh), 24*time.Hour)
	if !ok {
		t.Fatal("fresh screenshot manifest should pass")
	}
	if generated != fresh {
		t.Fatalf("generated_at = %q, want %q", generated, fresh)
	}
	if ageHours < 0 || ageHours > 3 {
		t.Fatalf("ageHours = %d, want around 2", ageHours)
	}

	stale := time.Now().UTC().Add(-15 * 24 * time.Hour).Format(time.RFC3339)
	if _, _, ok := screenshotManifestFreshness(fmt.Sprintf(`{"generated_at":%q,"routes":[{"name":"dashboard"}]}`, stale), 14*24*time.Hour); ok {
		t.Fatal("stale screenshot manifest should warn")
	}
	if _, _, ok := screenshotManifestFreshness(`{"generated_at":"bad","routes":[{"name":"dashboard"}]}`, 24*time.Hour); ok {
		t.Fatal("invalid timestamp should warn")
	}
	if _, _, ok := screenshotManifestFreshness(`{"generated_at":"2026-01-01T00:00:00Z","routes":[]}`, 24*time.Hour); ok {
		t.Fatal("manifest without routes should warn")
	}
}
