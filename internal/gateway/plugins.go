// plugins.go — plugin GUI mounts and scoped plugin tokens (Story E8).
//
// Static plugin UIs are served at /plugins/<id>/ui/ (no auth — same policy
// as the main GUI bundle; they are code, not data). The Svelte shell renders
// them in a sandboxed iframe and hands each one a SCOPED PLUGIN TOKEN —
// an opaque bearer token bound to the `plugin:<id>` principal — never the
// user's API key or JWT.
//
// At the API layer plugin principals are DEFAULT-DENY: pluginGateMW consults
// a small route policy table; anything unlisted is 403, listed routes go
// through the capability enforcer (internal/caps) and every decision is
// audited. User requests are untouched — the gate ignores non-plugin
// principals entirely.
package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/caps"
)

// PluginUIMount describes one plugin's static UI contribution
// (from plugin.yaml `gui:`, validated by the loader).
type PluginUIMount struct {
	ID        string `json:"id"`
	StaticDir string `json:"-"`
	NavLabel  string `json:"label"`
	NavIcon   string `json:"icon,omitempty"`
}

// SetPluginUI wires the plugin GUI mounts. Call after New(), before Start().
func (s *Server) SetPluginUI(mounts []PluginUIMount) {
	s.pluginMu.Lock()
	defer s.pluginMu.Unlock()
	s.pluginUIs = mounts
}

// SetCapEnforcer wires the capability enforcer used for plugin-token
// requests. Without it plugin principals are denied outright (default-deny
// stays safe even when wiring is incomplete).
func (s *Server) SetCapEnforcer(e *caps.Enforcer) {
	s.pluginMu.Lock()
	defer s.pluginMu.Unlock()
	s.capsEnforcer = e
}

func (s *Server) pluginMount(id string) (PluginUIMount, bool) {
	s.pluginMu.RLock()
	defer s.pluginMu.RUnlock()
	for _, m := range s.pluginUIs {
		if m.ID == id {
			return m, true
		}
	}
	return PluginUIMount{}, false
}

// ---------------------------------------------------------------------------
// Static asset serving: GET /plugins/:pid/ui/*
// ---------------------------------------------------------------------------

func (s *Server) handlePluginUIAsset(c *fiber.Ctx) error {
	mount, ok := s.pluginMount(c.Params("pid"))
	if !ok {
		return s.errMsg(c, fiber.StatusNotFound, "unknown plugin")
	}
	// The plugin UI runs in a sandboxed iframe WITHOUT allow-same-origin, so its
	// document origin is opaque ("null"). A `<script type="module">` is fetched
	// with CORS; from a null origin the browser will download the file (200) but
	// then REFUSE TO EXECUTE it unless the response is CORS-readable. These are
	// public, static plugin assets (code, not data), so allow any origin to read
	// them. Without this, the iframe stays stuck on "Loading plugin UI…".
	c.Set("Access-Control-Allow-Origin", "*")
	c.Set("Cross-Origin-Resource-Policy", "cross-origin")
	rel := c.Params("*")
	if decoded, err := url.PathUnescape(rel); err == nil {
		rel = decoded
	}
	// Clean inside a rooted path so ".." cannot climb out, then verify the
	// result is still under the mount dir (belt and suspenders).
	rel = filepath.Clean("/" + rel)
	full := filepath.Join(mount.StaticDir, rel)
	if full != mount.StaticDir && !strings.HasPrefix(full, mount.StaticDir+string(filepath.Separator)) {
		return s.errMsg(c, fiber.StatusNotFound, "not found")
	}
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		index := filepath.Join(full, "index.html")
		if st2, err2 := os.Stat(index); err2 == nil && !st2.IsDir() {
			return c.SendFile(index)
		}
		return s.errMsg(c, fiber.StatusNotFound, "not found")
	}
	return c.SendFile(full)
}

// ---------------------------------------------------------------------------
// Shell API: list mounts, issue scoped tokens
// ---------------------------------------------------------------------------

// handleListPluginUIs returns the nav entries for the Svelte shell.
//
//	GET /api/v1/plugins/ui
func (s *Server) handleListPluginUIs(c *fiber.Ctx) error {
	s.pluginMu.RLock()
	defer s.pluginMu.RUnlock()
	type mountView struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Icon  string `json:"icon,omitempty"`
		URL   string `json:"url"`
	}
	out := make([]mountView, 0, len(s.pluginUIs))
	for _, m := range s.pluginUIs {
		out = append(out, mountView{
			ID: m.ID, Label: m.NavLabel, Icon: m.NavIcon,
			URL: "/plugins/" + m.ID + "/ui/",
		})
	}
	return c.JSON(fiber.Map{"mounts": out, "count": len(out)})
}

// handleIssuePluginToken mints (or returns the existing) scoped token for a
// mounted plugin. User-authenticated; the returned token authenticates as
// the `plugin:<id>` principal with ONLY that plugin's manifest capabilities.
//
//	POST /api/v1/plugins/:id/token
func (s *Server) handleIssuePluginToken(c *fiber.Ctx) error {
	id := c.Params("id")
	if _, ok := s.pluginMount(id); !ok {
		return s.errMsg(c, fiber.StatusNotFound, "unknown plugin")
	}
	s.pluginMu.Lock()
	defer s.pluginMu.Unlock()
	if s.pluginTokens == nil {
		s.pluginTokens = map[string]string{}
	}
	// Reuse an existing token for this plugin (idempotent issuance).
	for tok, pid := range s.pluginTokens {
		if pid == id {
			return c.JSON(fiber.Map{"token": tok, "principal": caps.PluginPrincipal(id)})
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "token generation failed")
	}
	tok := "splg_" + hex.EncodeToString(buf)
	s.pluginTokens[tok] = id
	return c.JSON(fiber.Map{"token": tok, "principal": caps.PluginPrincipal(id)})
}

func (s *Server) lookupPluginToken(bearer string) (string, bool) {
	if bearer == "" || !strings.HasPrefix(bearer, "splg_") {
		return "", false
	}
	s.pluginMu.RLock()
	defer s.pluginMu.RUnlock()
	id, ok := s.pluginTokens[bearer]
	return id, ok
}

// ---------------------------------------------------------------------------
// Auth integration
// ---------------------------------------------------------------------------

// authWithPluginTokens recognises scoped plugin tokens before delegating to
// the regular auth stack. A matching token authenticates the request as the
// plugin principal (Subject "plugin:<id>"); everything else flows through
// the user auth middleware unchanged.
func (s *Server) authWithPluginTokens() fiber.Handler {
	inner := s.authHandler()
	return func(c *fiber.Ctx) error {
		bearer := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		if id, ok := s.lookupPluginToken(bearer); ok {
			auth.SetClaims(c, &auth.Claims{
				RegisteredClaims: jwt.RegisteredClaims{Subject: string(caps.PluginPrincipal(id))},
				Kind:             "access",
			})
			return c.Next()
		}
		return inner(c)
	}
}

// ---------------------------------------------------------------------------
// Plugin gate: default-deny route policy for plugin principals
// ---------------------------------------------------------------------------

// pluginRoute maps one API route to the capability that admits plugin
// principals. Cap "" means the route is freely accessible to any
// authenticated plugin (liveness only). Scope is resolved per request;
// nil scopeFn = unscoped check (only unrestricted grants pass).
type pluginRoute struct {
	method  string
	prefix  string // c.Path() prefix
	suffix  string // optional c.Path() suffix
	cap     string
	scopeFn func(c *fiber.Ctx) string
}

// pluginRoutePolicy is the full set of API routes plugin tokens may touch.
// Everything else is 403. Grow this table alongside the capability registry
// (docs/PLUGIN_CAPABILITIES.md) — one entry, one cap, one test.
var pluginRoutePolicy = []pluginRoute{
	{method: fiber.MethodGet, prefix: "/api/v1/health"}, // liveness, no cap
	{method: fiber.MethodPost, prefix: "/api/v1/knowledge/", suffix: "/search",
		cap: caps.CapVectorSearch}, // unscoped: agent-restricted grants are refused
}

func matchPluginRoute(method, path string) (pluginRoute, bool) {
	for _, r := range pluginRoutePolicy {
		if r.method != method {
			continue
		}
		if !strings.HasPrefix(path, r.prefix) {
			continue
		}
		if r.suffix != "" && !strings.HasSuffix(path, r.suffix) {
			continue
		}
		return r, true
	}
	return pluginRoute{}, false
}

// pluginGateMW enforces the default-deny policy for plugin principals.
// Mounted directly after auth on the /api/v1 group; ignores user requests.
func (s *Server) pluginGateMW() fiber.Handler {
	return func(c *fiber.Ctx) error {
		cl := auth.ClaimsFromCtx(c)
		if cl == nil || !strings.HasPrefix(cl.Subject, caps.PrincipalPrefix) {
			return c.Next() // not a plugin — user RBAC governs as before
		}
		principal := caps.Principal(cl.Subject)
		route, ok := matchPluginRoute(c.Method(), c.Path())
		if !ok {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error":     "plugin tokens cannot access this endpoint",
				"principal": string(principal),
			})
		}
		if route.cap == "" {
			return c.Next()
		}
		s.pluginMu.RLock()
		enf := s.capsEnforcer
		s.pluginMu.RUnlock()
		if enf == nil {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "capability enforcement unavailable",
			})
		}
		scope := ""
		if route.scopeFn != nil {
			scope = route.scopeFn(c)
		}
		if d := enf.Check(principal, route.cap, scope); !d.Allowed {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error":     "capability denied",
				"principal": string(principal),
				"required":  route.cap,
				"reason":    d.Reason,
			})
		}
		return c.Next()
	}
}

// wsPluginTokenAuth admits scoped plugin tokens on the WebSocket event
// stream (Story 19c). Returns handled=false when the credential is not a
// plugin token (user auth proceeds); otherwise the decision is final:
// plugins need the events.subscribe capability (E5 grammar) — the event
// feed carries prompts and tool I/O, so a bare valid token is NOT enough.
// WebSocket clients that cannot set headers pass the token as ?api_key=.
func (s *Server) wsPluginTokenAuth(c *fiber.Ctx) (bool, error) {
	bearer := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
	if bearer == "" {
		bearer = c.Query("api_key")
	}
	id, ok := s.lookupPluginToken(bearer)
	if !ok {
		return false, nil
	}
	s.pluginMu.RLock()
	enforcer := s.capsEnforcer
	s.pluginMu.RUnlock()
	if enforcer == nil {
		return true, c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "plugin capabilities unavailable; event stream denied",
		})
	}
	if d := enforcer.Check(caps.PluginPrincipal(id), caps.CapEventsSubscribe, ""); !d.Allowed {
		return true, c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "plugin lacks the events.subscribe capability: " + d.Reason,
		})
	}
	auth.SetClaims(c, &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: string(caps.PluginPrincipal(id))},
		Kind:             "access",
	})
	return true, c.Next()
}
