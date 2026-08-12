package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestBrowserSecurityHeadersApplyToGUIAndAPI(t *testing.T) {
	s := newTestGateway(t, "secret")
	for _, path := range []string{"/", "/api/v1/health"} {
		req, _ := http.NewRequest(http.MethodGet, path, nil)
		resp, err := s.app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'self'", "script-src 'self'"} {
			if !strings.Contains(csp, directive) {
				t.Errorf("%s CSP missing %q: %q", path, directive, csp)
			}
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("X-Frame-Options") != "SAMEORIGIN" || resp.Header.Get("Permissions-Policy") == "" {
			t.Errorf("%s security headers incomplete: %#v", path, resp.Header)
		}
	}
}
