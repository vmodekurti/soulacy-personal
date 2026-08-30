package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeConnectionBoundary(t *testing.T) {
	base, domains, err := normalizeConnectionBoundary("https://members.hbr.org/login?next=/home#form", []string{"hbr.org", ".HBR.org"})
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://members.hbr.org/login" {
		t.Fatalf("base = %q", base)
	}
	if len(domains) != 1 || domains[0] != "hbr.org" {
		t.Fatalf("domains = %v", domains)
	}
	for _, test := range []struct {
		name, raw string
		domains   []string
	}{
		{"plaintext", "http://hbr.org/login", nil},
		{"credentials", "https://user:pass@hbr.org/login", nil},
		{"loopback", "https://127.0.0.1/login", nil},
		{"local", "https://app.local/login", nil},
		{"foreign boundary", "https://hbr.org/login", []string{"evil.test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := normalizeConnectionBoundary(test.raw, test.domains); err == nil {
				t.Fatal("expected boundary validation failure")
			}
		})
	}
}

func TestValidateBrowserStorageStateDomainConfinement(t *testing.T) {
	valid := json.RawMessage(`{"cookies":[{"name":"session","value":"opaque","domain":".hbr.org","path":"/"}],"origins":[{"origin":"https://members.hbr.org","localStorage":[{"name":"state","value":"opaque"}]}]}`)
	if err := validateBrowserStorageState(valid, []string{"hbr.org"}); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	foreignCookie := json.RawMessage(`{"cookies":[{"name":"session","value":"opaque","domain":"evil.test"}],"origins":[]}`)
	if err := validateBrowserStorageState(foreignCookie, []string{"hbr.org"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("foreign cookie error = %v", err)
	}
	foreignOrigin := json.RawMessage(`{"cookies":[],"origins":[{"origin":"https://evil.test","localStorage":[{"name":"state","value":"opaque"}]}]}`)
	if err := validateBrowserStorageState(foreignOrigin, []string{"hbr.org"}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("foreign origin error = %v", err)
	}
}
