package runtime

import (
	"encoding/json"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/soulacy/soulacy/internal/authconnections"
)

func TestAuthenticatedHostAllowed(t *testing.T) {
	allowed := []string{"hbr.org"}
	for _, host := range []string{"hbr.org", "www.hbr.org", "deep.members.hbr.org"} {
		if !authenticatedHostAllowed(host, allowed) {
			t.Fatalf("expected %q to be allowed", host)
		}
	}
	for _, host := range []string{"evilhbr.org", "hbr.org.evil.test", "evil.test", ""} {
		if authenticatedHostAllowed(host, allowed) {
			t.Fatalf("expected %q to be rejected", host)
		}
	}
}

func TestSeedAuthenticatedCookieJarFiltersOutsideAndExpiredCookies(t *testing.T) {
	target, _ := url.Parse("https://members.hbr.org/article")
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]any{"cookies": []map[string]any{
		{"name": "session", "value": "kept", "domain": ".hbr.org", "path": "/", "secure": true, "expires": time.Now().Add(time.Hour).Unix()},
		{"name": "expired", "value": "drop", "domain": ".hbr.org", "path": "/", "expires": time.Now().Add(-time.Hour).Unix()},
		{"name": "foreign", "value": "drop", "domain": ".evil.test", "path": "/", "expires": time.Now().Add(time.Hour).Unix()},
	}})
	if err := seedAuthenticatedCookieJar(jar, target, state, []string{"hbr.org"}); err != nil {
		t.Fatal(err)
	}
	cookies := jar.Cookies(target)
	if len(cookies) != 1 || cookies[0].Name != "session" || cookies[0].Value != "kept" {
		t.Fatalf("cookies = %#v", cookies)
	}
}

func TestSeedAuthenticatedCookieJarRejectsSessionWithoutUsableCookie(t *testing.T) {
	target, _ := url.Parse("https://hbr.org/article")
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	state := []byte(`{"cookies":[{"name":"session","value":"secret","domain":"evil.test"}]}`)
	if err := seedAuthenticatedCookieJar(jar, target, state, []string{"hbr.org"}); err == nil {
		t.Fatal("expected a saved session without an approved cookie to fail")
	}
}

func TestReadableHTMLSuppressesExecutableContent(t *testing.T) {
	got := readableHTML(`<html><head><style>secret-style</style><script>secret-script()</script></head><body><main><h1>Research</h1><p>Member article text.</p></main></body></html>`)
	if strings.Contains(got, "secret-style") || strings.Contains(got, "secret-script") {
		t.Fatalf("executable content leaked into readable text: %q", got)
	}
	if !strings.Contains(got, "Research") || !strings.Contains(got, "Member article text.") {
		t.Fatalf("readable content missing: %q", got)
	}
}

func TestAuthenticatedFetchSchemaContainsOnlySecretFreeMetadata(t *testing.T) {
	schema := authenticatedFetchSchema([]authconnections.Connection{{ID: "conn_1", Name: "HBR", AllowedDomains: []string{"hbr.org"}}})
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "conn_1") || !strings.Contains(text, "HBR") || !strings.Contains(text, "hbr.org") {
		t.Fatalf("connection metadata missing from schema: %s", text)
	}
	for _, forbidden := range []string{"cookie", "refresh_token", "password", "secret-value"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("schema unexpectedly contains %q: %s", forbidden, text)
		}
	}
}
