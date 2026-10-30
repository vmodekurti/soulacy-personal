package managedbrowser

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func TestCookieParamPreservesSameSiteAndExpiry(t *testing.T) {
	exp := float64(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC).Unix())
	got := cookieParam(storageCookie{
		Name: "SID", Value: "v", Domain: ".google.com", Path: "",
		Expires: exp, HTTPOnly: true, Secure: true, SameSite: "Lax",
	})
	if got.Path != "/" {
		t.Errorf("empty path should default to /, got %q", got.Path)
	}
	if got.Domain != ".google.com" {
		t.Errorf("domain should be preserved for a non-prefixed cookie, got %q", got.Domain)
	}
	if got.SameSite != network.CookieSameSiteLax {
		t.Errorf("SameSite not preserved: got %q", got.SameSite)
	}
	if got.Expires == nil {
		t.Fatal("expiry was dropped; a persistent cookie must keep its expiry")
	}
	if ts := time.Time(*got.Expires).UTC(); ts.Year() != 2030 {
		t.Errorf("expiry mismatch: got %s", ts)
	}
}

func TestCookieParamSessionCookieHasNoExpiry(t *testing.T) {
	for _, exp := range []float64{0, -1} {
		got := cookieParam(storageCookie{Name: "s", Value: "v", Domain: "example.com", Expires: exp})
		if got.Expires != nil {
			t.Errorf("session cookie (expires=%v) must not set an expiry", exp)
		}
	}
}

func TestCookieParamHostPrefix(t *testing.T) {
	// __Host- cookies must be host-only (no Domain), Path=/, and Secure.
	// Regression: the old restore kept Domain, so Chromium rejected these and
	// the cookie never made it into the replayed session.
	got := cookieParam(storageCookie{
		Name: "__Host-session", Value: "v", Domain: "notebooklm.google.com",
		Path: "/app", Secure: false,
	})
	if got.Domain != "" {
		t.Errorf("__Host- cookie must not carry a Domain, got %q", got.Domain)
	}
	if got.URL != "https://notebooklm.google.com" {
		t.Errorf("__Host- cookie should be addressed by URL, got %q", got.URL)
	}
	if got.Path != "/" {
		t.Errorf("__Host- cookie must use Path=/, got %q", got.Path)
	}
	if !got.Secure {
		t.Error("__Host- cookie must be Secure")
	}
}

func TestCookieParamSecurePrefixForcesSecure(t *testing.T) {
	got := cookieParam(storageCookie{Name: "__Secure-x", Value: "v", Domain: ".google.com", Secure: false})
	if !got.Secure {
		t.Error("__Secure- cookie must be marked Secure even if the capture omitted it")
	}
	if got.Domain != ".google.com" {
		t.Errorf("__Secure- cookie keeps its Domain, got %q", got.Domain)
	}
}

func TestCookieParamSameSiteNoneForcesSecure(t *testing.T) {
	got := cookieParam(storageCookie{Name: "x", Value: "v", Domain: "example.com", Secure: false, SameSite: "None"})
	if !got.Secure {
		t.Error("SameSite=None requires Secure")
	}
}

func TestSameSiteType(t *testing.T) {
	cases := map[string]network.CookieSameSite{
		"Strict":  network.CookieSameSiteStrict,
		"lax":     network.CookieSameSiteLax,
		"NONE":    network.CookieSameSiteNone,
		"":        "",
		"unknown": "",
	}
	for in, want := range cases {
		if got := sameSiteType(in); got != want {
			t.Errorf("sameSiteType(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBrowserUserAgent(t *testing.T) {
	if strings.Contains(defaultBrowserUserAgent, "Headless") {
		t.Error("default UA must not advertise HeadlessChrome")
	}
	t.Setenv("SOULACY_BROWSER_USER_AGENT", "")
	if browserUserAgent() != defaultBrowserUserAgent {
		t.Error("empty override should fall back to the default UA")
	}
	t.Setenv("SOULACY_BROWSER_USER_AGENT", "Custom/1.0")
	if browserUserAgent() != "Custom/1.0" {
		t.Errorf("operator override not honored, got %q", browserUserAgent())
	}
}
