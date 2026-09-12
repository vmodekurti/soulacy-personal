package main

import "testing"

func TestFirstRunBannerKeyRedactsEnvironmentCredential(t *testing.T) {
	t.Setenv("SOULACY_SERVER_API_KEY", "sy_do_not_log")

	display, guidance := firstRunBannerKey("sy_do_not_log")
	if display != "(configured by environment)" {
		t.Fatalf("display = %q, want environment placeholder", display)
	}
	if guidance != "Reveal the key securely in your cloud console." {
		t.Fatalf("guidance = %q", guidance)
	}
}

func TestFirstRunBannerKeyShowsLocallyGeneratedCredential(t *testing.T) {
	t.Setenv("SOULACY_SERVER_API_KEY", "")

	display, _ := firstRunBannerKey("sy_local")
	if display != "sy_local" {
		t.Fatalf("display = %q, want local key", display)
	}
}
