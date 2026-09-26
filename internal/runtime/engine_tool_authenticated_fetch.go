package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/pkg/agent"
)

const authenticatedFetchTool = "authenticated_fetch"

func authenticatedFetchSchema(connections []authconnections.Connection) map[string]any {
	connectionSchema := map[string]any{
		"type":        "string",
		"description": "ID of one authenticated connection granted to this agent",
	}
	if len(connections) > 0 {
		choices := make([]map[string]any, 0, len(connections))
		for _, connection := range connections {
			choices = append(choices, map[string]any{
				"const": connection.ID,
				"title": connection.Name + " (" + strings.Join(connection.AllowedDomains, ", ") + ")",
			})
		}
		connectionSchema["oneOf"] = choices
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"connection_id": connectionSchema,
			"url": map[string]any{
				"type":        "string",
				"description": "HTTPS page URL inside the connection's approved domain",
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"description": "Maximum response bytes to read (default 512 KB, maximum 1 MB)",
			},
		},
		"required": []string{"connection_id", "url"},
	}
}

func (e *Engine) runAuthenticatedFetch(ctx context.Context, def *agent.Definition, args map[string]any) (string, error) {
	if e.authConnectionResolver == nil || def == nil {
		return "", fmt.Errorf("authenticated_fetch: authenticated connections are unavailable")
	}
	connectionID := strings.TrimSpace(argString(args, "connection_id"))
	if connectionID == "" || !stringInSlice(def.Connections, connectionID) {
		return "", fmt.Errorf("authenticated_fetch: connection is not declared by agent %q", def.ID)
	}
	rawURL := strings.TrimSpace(argString(args, "url"))
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("authenticated_fetch: url must be an HTTPS URL without embedded credentials")
	}
	lease, err := e.authConnectionResolver.Resolve(ctx, WorkspaceFromContext(ctx), SubjectFromContext(ctx), def.ID, connectionID)
	if err != nil {
		return "", fmt.Errorf("authenticated_fetch: %w", err)
	}
	if lease.Kind != authconnections.KindBrowser {
		return "", fmt.Errorf("authenticated_fetch: connection %q is not a browser session", connectionID)
	}
	if !authenticatedHostAllowed(parsed.Hostname(), lease.AllowedDomains) {
		return "", fmt.Errorf("authenticated_fetch: url is outside the connection's approved domains")
	}
	if err := checkSSRF(rawURL, e.ssrfProtection, e.allowPrivateHosts); err != nil {
		return "", err
	}

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return "", fmt.Errorf("authenticated_fetch: initialize cookie jar: %w", err)
	}
	if err := seedAuthenticatedCookieJar(jar, parsed, lease.BrowserState, lease.AllowedDomains); err != nil {
		return "", err
	}
	client := e.ssrfHTTPClient(30 * time.Second)
	client.Jar = jar
	guardRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !authenticatedHostAllowed(req.URL.Hostname(), lease.AllowedDomains) {
			return fmt.Errorf("authenticated_fetch: redirect left the approved domain")
		}
		if guardRedirect != nil {
			return guardRedirect(req, via)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("authenticated_fetch: build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Soulacy authenticated reader)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain;q=0.9,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("authenticated_fetch: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		e.authConnectionResolver.MarkNeedsAuthentication(ctx, WorkspaceFromContext(ctx), connectionID)
		return "", fmt.Errorf("authenticated_fetch: website rejected the saved session (HTTP %d); reconnect it before retrying", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("authenticated_fetch: website returned HTTP %d", resp.StatusCode)
	}
	maxBytes := argInt(args, "max_bytes", 512*1024)
	if maxBytes <= 0 {
		maxBytes = 512 * 1024
	}
	if maxBytes > 1024*1024 {
		maxBytes = 1024 * 1024
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", fmt.Errorf("authenticated_fetch: read response: %w", err)
	}
	truncated := len(body) > maxBytes
	if truncated {
		body = body[:maxBytes]
	}
	content := strings.TrimSpace(string(body))
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "html") {
		content = readableHTML(content)
	} else if strings.Contains(contentType, "json") && json.Valid(body) {
		var compact bytes.Buffer
		if json.Compact(&compact, body) == nil {
			content = compact.String()
		}
	}
	if content == "" {
		return "", fmt.Errorf("authenticated_fetch: page returned no readable content")
	}
	result := fmt.Sprintf("URL: %s\nStatus: %d\n\n%s", resp.Request.URL.String(), resp.StatusCode, content)
	if truncated {
		result += "\n\n[truncated — response exceeded max_bytes]"
	}
	return result, nil
}

func seedAuthenticatedCookieJar(jar http.CookieJar, target *url.URL, raw []byte, allowed []string) error {
	var state struct {
		Cookies []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Expires  float64 `json:"expires"`
			HTTPOnly bool    `json:"httpOnly"`
			Secure   bool    `json:"secure"`
			SameSite string  `json:"sameSite"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("authenticated_fetch: saved browser session is invalid")
	}
	byURL := map[string][]*http.Cookie{}
	now := time.Now()
	for _, saved := range state.Cookies {
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(saved.Domain)), ".")
		if saved.Name == "" || !authenticatedHostAllowed(domain, allowed) {
			continue
		}
		cookieURL := &url.URL{Scheme: "https", Host: domain, Path: "/"}
		cookie := &http.Cookie{Name: saved.Name, Value: saved.Value, Domain: saved.Domain, Path: saved.Path, HttpOnly: saved.HTTPOnly, Secure: saved.Secure}
		if cookie.Path == "" {
			cookie.Path = "/"
		}
		if saved.Expires > 0 {
			cookie.Expires = time.Unix(int64(saved.Expires), 0)
			if cookie.Expires.Before(now) {
				continue
			}
		}
		switch strings.ToLower(saved.SameSite) {
		case "strict":
			cookie.SameSite = http.SameSiteStrictMode
		case "lax":
			cookie.SameSite = http.SameSiteLaxMode
		case "none":
			cookie.SameSite = http.SameSiteNoneMode
		}
		byURL[cookieURL.String()] = append(byURL[cookieURL.String()], cookie)
	}
	for rawURL, cookies := range byURL {
		u, _ := url.Parse(rawURL)
		jar.SetCookies(u, cookies)
	}
	if len(jar.Cookies(target)) == 0 {
		return fmt.Errorf("authenticated_fetch: saved session has no cookies usable for this URL; reconnect it or use the browser executor")
	}
	return nil
}

func authenticatedHostAllowed(host string, allowed []string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, raw := range allowed {
		domain := strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), "."), ".")
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func stringInSlice(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func readableHTML(raw string) string {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	var out strings.Builder
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, suppressed bool) {
		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "script", "style", "noscript", "svg", "template":
				suppressed = true
			case "p", "div", "article", "section", "main", "h1", "h2", "h3", "h4", "li", "br", "tr":
				out.WriteByte('\n')
			}
		}
		if node.Type == html.TextNode && !suppressed {
			text := strings.Join(strings.Fields(node.Data), " ")
			if text != "" {
				out.WriteString(text)
				out.WriteByte(' ')
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, suppressed)
		}
	}
	walk(doc, false)
	lines := strings.Split(out.String(), "\n")
	clean := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n")
}
