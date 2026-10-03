package runtime

import (
	"encoding/json"
	"net/http"
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

func mergeForTest(t *testing.T, state map[string]any, observed []observedCookie) (map[string]any, bool) {
	t.Helper()
	raw, _ := json.Marshal(state)
	out, changed, err := mergeRenewedCookies(raw, observed, []string{"gartner.com"}, time.Unix(1_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		return nil, false
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	return got, true
}

func savedSession() map[string]any {
	return map[string]any{
		"cookies": []map[string]any{{"name": "sid", "value": "old", "domain": ".gartner.com", "path": "/", "expires": float64(1_000_100), "httpOnly": true, "secure": true, "sameSite": "Lax"}},
		"origins": []map[string]any{{"origin": "https://www.gartner.com", "localStorage": []any{}}},
	}
}

func TestMergeRenewedCookiesExtendsExpiryAndKeepsOtherState(t *testing.T) {
	got, changed := mergeForTest(t, savedSession(), []observedCookie{{
		host:   "www.gartner.com",
		cookie: &http.Cookie{Name: "sid", Value: "old", Domain: "gartner.com", Path: "/", MaxAge: 172800},
	}})
	if !changed {
		t.Fatal("expected the extended expiry to be saved")
	}
	cookie := got["cookies"].([]any)[0].(map[string]any)
	if cookie["expires"].(float64) != float64(1_000_000+172800) || cookie["httpOnly"] != true {
		t.Fatalf("cookie = %#v", cookie)
	}
	if _, ok := got["origins"]; !ok {
		t.Fatal("origins were dropped")
	}
}

func TestMergeRenewedCookiesIgnoresForeignDomainAndNoChange(t *testing.T) {
	_, changed := mergeForTest(t, savedSession(), []observedCookie{
		{host: "www.gartner.com", cookie: &http.Cookie{Name: "track", Value: "x", Domain: "evil.test", Path: "/"}},
		{host: "ads.evil.test", cookie: &http.Cookie{Name: "track", Value: "x", Path: "/"}},
		{host: "www.gartner.com", cookie: &http.Cookie{Name: "sid", Value: "old", Domain: "gartner.com", Path: "/"}},
	})
	if changed {
		t.Fatal("foreign cookies or an unchanged session must not rewrite the state")
	}
}

func TestMergeRenewedCookiesRotatesValueDeletesAndAdds(t *testing.T) {
	got, changed := mergeForTest(t, savedSession(), []observedCookie{
		{host: "www.gartner.com", cookie: &http.Cookie{Name: "sid", Value: "rotated", Domain: "gartner.com", Path: "/"}},
		{host: "www.gartner.com", cookie: &http.Cookie{Name: "fresh", Value: "n", Path: "/", MaxAge: 60}},
	})
	if !changed {
		t.Fatal("expected a change")
	}
	cookies := got["cookies"].([]any)
	if len(cookies) != 2 {
		t.Fatalf("cookies = %#v", cookies)
	}
	first := cookies[0].(map[string]any)
	if first["value"] != "rotated" || first["expires"].(float64) != 1_000_100 {
		t.Fatalf("rotation must change the value and keep the saved expiry: %#v", first)
	}
	second := cookies[1].(map[string]any)
	if second["name"] != "fresh" || second["domain"] != "www.gartner.com" {
		t.Fatalf("host-only cookie = %#v", second)
	}
	got, changed = mergeForTest(t, savedSession(), []observedCookie{{
		host: "www.gartner.com", cookie: &http.Cookie{Name: "sid", Domain: "gartner.com", Path: "/", MaxAge: -1},
	}})
	if !changed || len(got["cookies"].([]any)) != 0 {
		t.Fatalf("expired cookie must be removed: %#v", got)
	}
}
