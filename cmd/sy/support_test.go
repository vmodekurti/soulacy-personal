package main

import "testing"

func TestSupportBundleOptionsIncludesReleaseMetadata(t *testing.T) {
	t.Setenv("SOULACY_UPDATE_MANIFEST", "https://releases.example.test/soulacy/manifest.json")
	opts := supportBundleOptions()
	release, ok := opts.ExtraJSON["release"].(map[string]any)
	if !ok {
		t.Fatalf("release metadata missing: %#v", opts.ExtraJSON)
	}
	if got := release["update_manifest"]; got != "https://releases.example.test/soulacy/manifest.json" {
		t.Fatalf("update_manifest = %#v", got)
	}
	if got := release["updates_ready"]; got != true {
		t.Fatalf("updates_ready = %#v", got)
	}
	evidence, ok := opts.ExtraJSON["operator_evidence"].(map[string]any)
	if !ok {
		t.Fatalf("operator evidence missing: %#v", opts.ExtraJSON)
	}
	if evidence["source"] != "cli" {
		t.Fatalf("operator evidence source = %#v", evidence["source"])
	}
	artifacts, ok := evidence["live_gateway_artifacts"].([]map[string]string)
	if !ok || len(artifacts) < 4 {
		t.Fatalf("live gateway artifacts missing: %#v", evidence["live_gateway_artifacts"])
	}
}
