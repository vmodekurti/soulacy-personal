package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/runtime"
)

type portalSession struct {
	server                      *Server
	authorization, cookie, host string
}

func (s *Server) withPortalAccess(c *fiber.Ctx, ctx context.Context, agentID string) context.Context {
	if agentID != runtime.GenieAgentID {
		return ctx
	}
	if _, ok := requestPrincipal(c); !ok {
		return ctx
	}
	authorization := strings.Clone(c.Get("Authorization"))
	cookie := strings.Clone(c.Cookies("soulacy_access"))
	// Authenticated query-key clients (including the legacy stream transport)
	// get the same validated credential, never the installation owner's key.
	if authorization == "" && cookie == "" && c.Query("api_key") != "" {
		authorization = "Bearer " + strings.Clone(c.Query("api_key"))
	}
	if authorization == "" && cookie == "" {
		return ctx
	}
	return runtime.WithPortalAccess(ctx, &portalSession{server: s, authorization: authorization, cookie: cookie, host: strings.Clone(c.Get("Host"))})
}

func (p *portalSession) PortalActions(ctx context.Context, query string) (any, error) {
	if runtime.ActiveAgentFromContext(ctx) != runtime.GenieAgentID {
		return nil, fmt.Errorf("only Genie may use portal access")
	}
	terms := strings.Fields(strings.ToLower(query))
	results := []portalActionSpec{}
	for _, a := range portalCatalog {
		haystack := strings.ToLower(a.Name + " " + a.Description + " " + a.Params)
		match := len(terms) == 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				match = true
				break
			}
		}
		if match {
			results = append(results, a)
		}
	}
	return map[string]any{"actions": results, "count": len(results), "instructions": "Use portal_read/update/confirm matching tool_mode. Params are a JSON object of named path/query values, body a JSON object. Read before editing. All actions use your signed-in permissions. Keys, passwords, sign-in, approval decisions and file uploads stay in the Portal. More actions are available through Genie builder, missions and connector tools.", "secure_settings_url": "/#providers", "protected_settings": portalProtectedSettings}, nil
}

func (p *portalSession) PortalAction(ctx context.Context, mode, name string, params, body map[string]any) (any, error) {
	if runtime.ActiveAgentFromContext(ctx) != runtime.GenieAgentID {
		return nil, fmt.Errorf("only Genie may use portal access")
	}
	var action *portalActionSpec
	for i := range portalCatalog {
		if portalCatalog[i].Name == name {
			action = &portalCatalog[i]
			break
		}
	}
	if action == nil {
		return nil, fmt.Errorf("unknown portal action %q; discover it with portal_actions", name)
	}
	if action.Mode != mode {
		return nil, fmt.Errorf("%s requires portal_%s", name, action.Mode)
	}
	if portalHasCredentials(body) || portalHasCredentials(params) {
		return nil, fmt.Errorf("enter credentials directly in the secure Portal settings; credentials and approval acknowledgements cannot be passed through chat tools")
	}
	if err := p.allowPortalMutation(*action, params, body); err != nil {
		return nil, err
	}
	path, err := portalActionPath(*action, params)
	if err != nil {
		return nil, err
	}
	if name == "runs.list" && (params == nil || params["limit"] == nil) {
		if strings.Contains(path, "?") {
			path += "&limit=10"
		} else {
			path += "?limit=10"
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if body == nil {
		raw = nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, action.Method, "http://soulacy.internal/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Host = p.host
	req.Header.Set("Content-Type", "application/json")
	if p.authorization != "" {
		req.Header.Set("Authorization", p.authorization)
	}
	if p.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "soulacy_access", Value: p.cookie})
	}
	// The same auth, scopes, RBAC, object ownership, validation, persistence,
	// hot reload and audit hooks used by the browser all run again here.
	resp, err := p.server.app.Test(req, -1)
	if err != nil {
		return nil, fmt.Errorf("portal request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPGatewayResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMCPGatewayResponseBytes {
		return nil, fmt.Errorf("portal result is too large; narrow the filters or page size")
	}
	var value any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, fmt.Errorf("portal returned a non-JSON result (HTTP %d)", resp.StatusCode)
		}
	}
	value = redactPortalValue(value)
	result := map[string]any{"action": name, "ok": resp.StatusCode >= 200 && resp.StatusCode < 300, "http_status": resp.StatusCode, "result": value}
	if resp.StatusCode == 409 {
		result["next_step"] = "Review the portal's existing approval or conflict details; do not retry with invented acknowledgement."
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		result["next_step"] = "This action is unavailable with the current signed-in permissions. Use the Portal to sign in or request access."
	}
	return result, nil
}

func portalActionPath(a portalActionSpec, params map[string]any) (string, error) {
	path := a.Path
	used := map[string]bool{}
	for _, part := range strings.Split(path, "/") {
		if !strings.HasPrefix(part, ":") {
			continue
		}
		key := part[1:]
		v, ok := params[key].(string)
		if !ok || strings.TrimSpace(v) == "" || v == "." || v == ".." || strings.ContainsAny(v, "/\\?#%\x00\r\n") {
			return "", fmt.Errorf("%s requires a valid %s", a.Name, key)
		}
		path = strings.ReplaceAll(path, part, url.PathEscape(v))
		used[key] = true
	}
	allowed := map[string]bool{}
	for _, part := range strings.Split(a.Params, ",") {
		fields := strings.Fields(part)
		if len(fields) > 0 {
			allowed[fields[0]] = true
		}
	}
	query := url.Values{}
	for k, v := range params {
		if used[k] {
			continue
		}
		if !allowed[k] {
			return "", fmt.Errorf("unknown parameter %q for %s", k, a.Name)
		}
		switch v.(type) {
		case string, float64, int, bool:
			query.Set(k, fmt.Sprint(v))
		default:
			return "", fmt.Errorf("parameter %s must be a scalar", k)
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return path, nil
}

func portalSensitiveKey(key string) bool {
	k := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	return k == "env" || k == "headers" || k == "credentials" || k == "authorization" || k == "cookie" || k == "cookies" || k == "password" || k == "secret" || k == "token" || k == "api_key" || k == "apikey" || strings.HasSuffix(k, "_password") || strings.HasSuffix(k, "_secret") || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "_api_key") || strings.Contains(k, "acknowledge")
}
func portalHasCredentials(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if portalSensitiveKey(k) || portalHasCredentials(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if portalHasCredentials(v) {
				return true
			}
		}
	}
	return false
}
func redactPortalValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if portalSensitiveKey(k) {
				if v == "" || v == nil {
					x[k] = ""
				} else {
					x[k] = "[redacted]"
				}
			} else {
				x[k] = redactPortalValue(v)
			}
		}
	case []any:
		for i, v := range x {
			x[i] = redactPortalValue(v)
		}
	}
	return v
}
