// Package gateway implements the Soulacy gateway server.
//
// SECURITY MODEL (privacy-first, security-first):
//   - All API endpoints require a Bearer token unless api_key is empty (dev mode only).
//   - Tokens are checked via constant-time comparison to prevent timing attacks.
//   - The server listens on localhost by default; TLS is supported for production.
//   - CORS is locked down: only the GUI origin is whitelisted.
//   - Rate limiting: 60 requests/minute per IP by default.
//   - Memory and agent files never leave the server over the API without auth.
//   - No telemetry, no analytics, no external calls except to LLM providers you configure.
//
// GUI PHILOSOPHY:
//   - Every framework capability is reachable through the GUI — no CLI required.
//   - The GUI is served as static files from the gateway itself (no separate server).
//   - WebSocket /ws/events streams real-time log events to the GUI.
//   - The REST API uses a clean resource model so the GUI can be replaced or extended.
package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	// Aliased so the package name doesn't shadow Go's builtin `recover()`
	// — which we now use in the WhatsApp dispatch goroutine. Without the
	// alias, `recover()` in user code resolves to "use of package as
	// expression", which is what tripped the audit-pass build.
	fibrecover "github.com/gofiber/fiber/v2/middleware/recover"
	fws "github.com/gofiber/websocket/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/builder"
	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/channels"
	httpchan "github.com/soulacy/soulacy/internal/channels/http"
	wachan "github.com/soulacy/soulacy/internal/channels/whatsapp"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/ratelimit"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/voice"
	"github.com/soulacy/soulacy/internal/webui"
	"github.com/soulacy/soulacy/internal/workboard"

	"github.com/fsnotify/fsnotify"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"strconv"
)

// Server is the Soulacy gateway server.
type Server struct {
	cfg             *config.Config
	cfgPath         string // path to config file on disk; empty = unknown
	app             *fiber.App
	engine          *runtime.Engine
	loader          *runtime.Loader
	llmRouter       *llm.Router
	channels        *channels.Registry
	scheduler       *scheduler.Scheduler
	httpChan        *httpchan.Adapter
	waChan          *wachan.Adapter          // nil if WhatsApp not configured
	skillLoader     runtime.SkillLoader      // nil if no skills installed
	actions         storage.ActionLogBackend // nil if action logging disabled
	mcp             *mcp.Client              // nil if no MCP servers configured
	hub             *EventHub
	authEngine      *auth.Engine         // nil until SetAuth() is called
	rbacManager     *rbac.Manager        // nil until SetRBAC() is called
	credVault       credentials.Vault    // nil until SetCredentialVault() is called
	builderRegistry *builder.Registry    // nil until SetBuilderRegistry() is called
	rateLimiter     *ratelimit.Manager   // nil until SetRateLimiter() is called
	apiKeyStore     apikeys.Store        // nil until SetAPIKeyStore() is called
	dlqStore        dlq.Store            // nil until SetDLQStore() is called
	historyStore    session.HistoryStore // nil until SetHistoryStore() is called
	resourceStore   session.ResourceStore
	tenantResolver  tenancy.Resolver
	tenantMembers   tenancy.MemberManager
	// idempotency replays completed mutations for a repeated Idempotency-Key
	// so a retry through a network partition cannot create a duplicate.
	idempotency  *idempotencyStore
	agentWatcher healthReporter // nil until SetAgentWatcher() is called (S2.13)
	log          *zap.Logger

	// buildTraces retains recent Studio build traces in a bounded in-memory ring
	// and, when SOULACY_STUDIO_TRACE_DIR is set, also persists each as a JSONL
	// file — so an autonomous build is fully debuggable after the fact. Lazily
	// initialised by studioTraceStore().
	buildTraces     *studio.BuildTraceStore
	buildTracesOnce sync.Once

	// Tool-catalog TTL cache. See toolCatalog() / InvalidateToolCatalog().
	toolCatalogMu    sync.Mutex
	toolCatalogCache []pyToolView
	toolCatalogAt    time.Time

	// healthProbeInFlight ensures at most one knowledge-store health probe
	// runs at a time. ListKBs is not context-aware, so without this a blocked
	// store would spawn (and leak) one goroutine per /health request (PERF-5).
	healthProbeInFlight atomic.Bool

	// costStore is the optional per-agent token-cost store (Task #32).
	// Wired via SetCostStore after construction. When nil, the /api/v1/costs
	// handlers return 503.
	costStore *costs.Store

	// runReg tracks cancellable in-flight chat/stream runs (Story #22).
	runReg         *runRegistry
	sessionOwnerMu sync.RWMutex
	sessionOwners  map[string]sessionOwner

	// workboardStore is the optional Kanban task store (Story 5). Wired via
	// SetWorkboardStore after construction. When nil, /api/v1/workboard
	// handlers return 503.
	workboardStore *workboard.Store

	// ingestWorker drains the durable KB ingestion job catalog. nil until
	// SetIngestWorker() is called; used only to wake it on a fresh upload.
	ingestWorker *knowledge.Worker

	// Plugin GUI mounts + scoped plugin tokens (Story E8). Wired via
	// SetPluginUI / SetCapEnforcer after construction; routes check at
	// request time. pluginTokens maps opaque bearer token → plugin ID.
	pluginMu     sync.RWMutex
	pluginUIs    []PluginUIMount
	pluginTokens map[string]string
	capsEnforcer *caps.Enforcer

	// pluginInstaller manages installer-owned plugins (Story E13). Wired
	// via SetPluginInstaller; install routes 503 until then.
	pluginInstaller *plugininstall.Installer

	// safetyPipeline runs E20 pre-installation introspection on staged
	// plugins. Wired via SetSafetyPipeline; nil = Preview.Security omitted.
	safetyPipeline *introspect.Pipeline

	// voiceMinter is the realtime-voice control plane (Story 11). Wired via
	// SetVoiceMinter; nil = voice unavailable (graceful fallback).
	// Guarded by pluginMu (same wire-after-New lifecycle).
	voiceMinter           VoiceMinter
	voiceSidecar          *voice.Sidecar
	workflowDistiller     *studio.WorkflowDistiller
	strategyCollector     *studio.StrategyFitCollector
	lessonStoreOnce       sync.Once
	lessonStoreCached     *studio.LessonStore
	preferenceStoreOnce   sync.Once
	preferenceStoreCached *studio.PreferenceStore
	generationProofMu     sync.Mutex
	generationProofs      map[string]generationProofRecord
	preferenceJobs        chan preferenceMineJob
	preferenceJobsWG      sync.WaitGroup
	learningReplayWG      sync.WaitGroup
}

// New creates and configures the Fiber server but does not start listening.
// cfgPath is the path to the config file on disk (for the config PATCH API);
// pass "" if it should be read-only. skillLoader may be nil if no skills are installed.
func New(
	cfg *config.Config,
	cfgPath string,
	engine *runtime.Engine,
	loader *runtime.Loader,
	llmRouter *llm.Router,
	chanReg *channels.Registry,
	sched *scheduler.Scheduler,
	httpChan *httpchan.Adapter,
	waChan *wachan.Adapter,
	skillLoader runtime.SkillLoader,
	actions storage.ActionLogBackend,
	mcpClient *mcp.Client,
	hub *EventHub,
	log *zap.Logger,
) *Server {
	s := &Server{
		cfg:              cfg,
		cfgPath:          cfgPath,
		engine:           engine,
		loader:           loader,
		llmRouter:        llmRouter,
		channels:         chanReg,
		scheduler:        sched,
		httpChan:         httpChan,
		waChan:           waChan,
		skillLoader:      skillLoader,
		actions:          actions,
		mcp:              mcpClient,
		hub:              hub,
		log:              log,
		runReg:           newRunRegistry(),
		sessionOwners:    make(map[string]sessionOwner),
		generationProofs: make(map[string]generationProofRecord),
		preferenceJobs:   make(chan preferenceMineJob, 128),
		idempotency:      newIdempotencyStore(),
	}
	if shouldUseDefaultPersonalResolver(cfg) {
		s.tenantResolver = defaultPersonalResolver()
	}
	go s.runPreferenceMiner()
	if s.hub != nil {
		s.hub.SetEventAuthorizer(s.authorizeEvent)
		if s.studioLearningEnabled() {
			s.workflowDistiller = studio.NewWorkflowDistiller(s.macroStore())
			s.hub.AddObserver(s.workflowDistiller.Observe)
			s.strategyCollector = studio.NewStrategyFitCollector(s.strategyFitStore(), s.resolveAgentStrategy)
			s.hub.AddObserver(s.strategyCollector.Observe)
			s.learningReplayWG.Add(1)
			go func() { defer s.learningReplayWG.Done(); s.replayStudioLearning() }()
		}
	}
	s.app = s.buildApp()
	return s
}

// errJSON writes a standard error envelope `{"error": err.Error()}` with the
// given HTTP status. It is the single helper for the gateway's error responses
// so the JSON shape stays consistent (contract tests pin this envelope).
func (s *Server) errJSON(c *fiber.Ctx, status int, err error) error {
	var confirmation *costs.ConfirmationRequiredError
	if errors.As(err, &confirmation) {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error": confirmation.Error(), "confirmation_required": true, "estimate": confirmation,
		})
	}
	return c.Status(status).JSON(fiber.Map{"error": err.Error()})
}

// errMsg is errJSON for a plain string message. It produces the identical
// envelope `{"error": msg}` so callers with literal messages don't need to
// wrap them in errors.New.
func (s *Server) errMsg(c *fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

// SetAuth wires an auth.Engine into the server. Must be called before the
// first request is served. If not called, the server falls back to the legacy
// static-key check using s.cfg.Server.APIKey (identical to Phase 2 behaviour).
func (s *Server) SetAuth(e *auth.Engine) {
	s.authEngine = e
}

// SetRBAC wires an RBAC Manager into the server. Must be called before the
// first request is served. If not called, RBAC checks are skipped (all
// authenticated requests are allowed — equivalent to pre-Task-#31 behaviour).
func (s *Server) SetRBAC(m *rbac.Manager) {
	s.rbacManager = m
}

// SetCredentialVault wires a Vault into the server. Must be called before the
// first request is served. When nil, credential routes return 503.
func (s *Server) SetCredentialVault(v credentials.Vault) {
	s.credVault = v
}

// CredentialVault returns the current Vault (may be nil). Satisfies
// credentials.VaultProvider so the API can resolve the vault at request time.
func (s *Server) CredentialVault() credentials.Vault {
	return s.credVault
}

// SetBuilderRegistry wires a builder.Registry into the server for E4 gap
// detection. Must be called before the first request is served. When nil,
// the /builder/analyze and /builder/resolve routes are not registered.
func (s *Server) SetBuilderRegistry(r *builder.Registry) {
	s.builderRegistry = r
}

// SetCostStore wires a cost store into the server (Task #32). Must be called
// before the first request is served. When nil, /api/v1/costs handlers return
// 503.
func (s *Server) SetCostStore(cs *costs.Store) {
	s.costStore = cs
}

// SetWorkboardStore wires a workboard task store into the server (Story 5).
// Must be called before the first request is served. When nil,
// /api/v1/workboard handlers return 503.
func (s *Server) SetWorkboardStore(ws *workboard.Store) {
	s.workboardStore = ws
}

// SetRateLimiter wires a rate-limit Manager into the server (Task #33). Must
// be called before the first request is served. When nil, no per-user or
// per-agent rate limiting beyond the IP-level limiter is applied.
func (s *Server) SetRateLimiter(m *ratelimit.Manager) {
	s.rateLimiter = m
}

// RateLimiter returns the Manager (may be nil). Exposed for main.go to wire
// the token-recording callback into the engine.
func (s *Server) RateLimiter() *ratelimit.Manager {
	return s.rateLimiter
}

// SetAPIKeyStore wires a managed API-key store into the server.
// When nil, /admin/api-keys routes return 503.
func (s *Server) SetAPIKeyStore(st apikeys.Store) {
	s.apiKeyStore = st
}

// SetDLQStore wires a dead-letter queue store into the server.
// When nil, /admin/dlq routes return 503.
func (s *Server) SetDLQStore(st dlq.Store) {
	s.dlqStore = st
}

// SetHistoryStore wires a conversation history store into the server.
// When nil, /history routes return 503.
func (s *Server) SetHistoryStore(st session.HistoryStore) {
	s.historyStore = st
}

// SetResourceStore wires binary chat/session resources into the server.
func (s *Server) SetResourceStore(st session.ResourceStore) {
	s.resourceStore = st
}

// healthReporter is anything that can report its own liveness — satisfied by
// runtime.Watcher (S2.13/S2.4). Kept as a local interface to avoid widening the
// gateway's import surface.
type healthReporter interface{ Healthy() bool }

// SetAgentWatcher wires the hot-reload watcher so the deep health check can
// report whether it's still running.
func (s *Server) SetAgentWatcher(w healthReporter) {
	s.agentWatcher = w
}

// rlUserMW returns the per-user RPM middleware, or a no-op when no limiter
// is configured.
func (s *Server) rlUserMW() fiber.Handler {
	if s.rateLimiter == nil {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	return s.rateLimiter.UserRPMMiddleware()
}

// rlAgentMW returns the per-agent RPM middleware, or a no-op.
func (s *Server) rlAgentMW() fiber.Handler {
	if s.rateLimiter == nil {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	return s.rateLimiter.AgentRPMMiddleware()
}

// rlTokenMW returns the per-user token-quota middleware, or a no-op.
func (s *Server) rlTokenMW() fiber.Handler {
	if s.rateLimiter == nil {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	return s.rateLimiter.TokenQuotaMiddleware()
}

// rlAgentTokenMW returns the per-agent daily token-quota middleware, or a no-op.
func (s *Server) rlAgentTokenMW() fiber.Handler {
	if s.rateLimiter == nil {
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	return s.rateLimiter.AgentTokenQuotaMiddleware()
}

func (s *Server) authorizationRequired() bool {
	return s.cfg != nil && config.IsMultiUserMode(s.cfg.DeploymentMode())
}

func (s *Server) authorizationUnavailable(c *fiber.Ctx) error {
	return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "authorization service unavailable"})
}

func (s *Server) requireVerifiedWorkspaceIdentity(c *fiber.Ctx) error {
	if !s.authorizationRequired() {
		return nil
	}
	if _, ok := requestIdentity(c); !ok {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "verified workspace membership is required"})
	}
	return nil
}

// rbacMW returns an RBAC middleware for (resource, action). Personal mode
// preserves the legacy no-op fallback; Team and Scale fail closed.
func (s *Server) rbacMW(resource, action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.rbacManager == nil {
			if s.authorizationRequired() {
				return s.authorizationUnavailable(c)
			}
			return c.Next()
		}
		if err := s.requireVerifiedWorkspaceIdentity(c); err != nil {
			return err
		}
		return s.rbacManager.Require(resource, action)(c)
	}
}

// rbacAgentMW returns a per-agent RBAC middleware (param "id", given action).
func (s *Server) rbacAgentMW(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.rbacManager == nil {
			if s.authorizationRequired() {
				return s.authorizationUnavailable(c)
			}
			return c.Next()
		}
		if err := s.requireVerifiedWorkspaceIdentity(c); err != nil {
			return err
		}
		return s.rbacManager.RequireAgent("id", action)(c)
	}
}

func (s *Server) rbacAgentFromMW(resource, action string, sources ...rbac.AgentIDSource) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.rbacManager == nil {
			if s.authorizationRequired() {
				return s.authorizationUnavailable(c)
			}
			return c.Next()
		}
		if err := s.requireVerifiedWorkspaceIdentity(c); err != nil {
			return err
		}
		return s.rbacManager.RequireAgentFrom(resource, action, sources...)(c)
	}
}

// authHandler returns the Fiber middleware that enforces authentication.
// Uses the auth.Engine when available, otherwise falls back to the legacy
// static API-key check (allows a nil auth engine during tests).
func (s *Server) authHandler() fiber.Handler {
	if s.authEngine != nil {
		return s.authEngine.Middleware()
	}
	// Fallback: legacy static-key check (unchanged from Phase 2).
	return s.legacyAuthMiddleware()
}

func (s *Server) buildApp() *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:               "Soulacy Gateway",
		DisableStartupMessage: true,
		BodyLimit:             s.requestBodyLimit(),
		// Immutable makes c.Params/c.Query/etc. return copied strings instead of
		// views into fasthttp's reusable request buffer. REQUIRED here because we
		// retain handler strings (agent IDs from c.Params) in long-lived structures
		// (the agent loader map, scheduler entries, cron closures). Without it, a
		// later request's buffer reuse mutates those stored keys, producing
		// corrupted "phantom" agents (e.g. "daily-briefing" → "e/statusiefing").
		Immutable:   true,
		ReadTimeout: 30 * time.Second,
		// The HTTP deadline is the outermost configured request budget and is
		// intentionally longer than run/step/LLM/tool. Validation rejects an
		// inverted hierarchy before this server starts.
		WriteTimeout: s.httpRequestTimeout(),
		IdleTimeout:  120 * time.Second,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			status := fiber.StatusInternalServerError
			if fe, ok := err.(*fiber.Error); ok {
				status = fe.Code
			}
			return c.Status(status).JSON(fiber.Map{
				"error": err.Error(),
			})
		},
	})

	// --- Middleware ---
	app.Use(fibrecover.New())
	// Browser hardening applies to the GUI, plugin frames, API responses, and
	// errors alike. The CSP permits only bundled code; rich-media frames remain
	// limited to the same hosts the renderer explicitly sanitizes.
	app.Use(func(c *fiber.Ctx) error {
		c.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"base-uri 'none'",
			"object-src 'none'",
			"script-src 'self'",
			"style-src 'self' 'unsafe-inline'",
			"font-src 'self' data:",
			"img-src 'self' data: blob: https:",
			"media-src 'self' blob: https:",
			"connect-src 'self' ws: wss:",
			"frame-src 'self' https://www.youtube.com https://player.vimeo.com https://www.openstreetmap.org https://maps.google.com",
			"frame-ancestors 'self'",
			"form-action 'self'",
			"upgrade-insecure-requests",
		}, "; "))
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("Referrer-Policy", "no-referrer")
		c.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(self), payment=(), usb=()")
		c.Set("X-Frame-Options", "SAMEORIGIN")
		return c.Next()
	})
	// Request-ID middleware: assign a fresh UUID to every inbound request
	// (unless one is already supplied via X-Request-ID) and stash it on
	// fiber.Ctx Locals + response header so callers + downstream logs can
	// correlate. (PRODUCTION_AUDIT → MED/Observability)
	app.Use(func(c *fiber.Ctx) error {
		rid := c.Get("X-Request-ID")
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Locals("request_id", rid)
		c.Set("X-Request-ID", rid)
		return c.Next()
	})
	app.Use(logger.New(logger.Config{
		// Include request id in the access log so we can correlate it with
		// per-agent action logs and zap entries.
		Format: "[${time}] ${locals:request_id} ${method} ${path} → ${status} (${latency})\n",
	}))
	// Prometheus request timer. Runs after the access logger so the metric
	// captures the full handler latency including downstream middlewares.
	// (PRODUCTION_AUDIT → MED/Observability)
	app.Use(func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		route := c.Route().Path // pattern, not the substituted path — avoids label explosion
		if route == "" {
			route = c.Path()
		}
		status := strconv.Itoa(c.Response().StatusCode())
		labels := prometheus.Labels{
			"method": c.Method(),
			"route":  route,
			"status": status,
		}
		metrics.HTTPRequestDuration.With(labels).Observe(time.Since(start).Seconds())
		metrics.HTTPRequestsTotal.With(labels).Inc()
		return err
	})

	// CORS — gateway's own origin is always allowed. Explicit allow-list from
	// server.allowed_origins is honoured when set (production); otherwise we
	// fall back to the legacy localhost dev-server origins (3000/5173) for
	// local development. (PRODUCTION_AUDIT → LOW/Config)
	guiOrigin := fmt.Sprintf("http://%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	origins := []string{guiOrigin}
	if len(s.cfg.Server.AllowedOrigins) > 0 {
		origins = append(origins, s.cfg.Server.AllowedOrigins...)
	} else {
		origins = append(origins, "http://localhost:3000", "http://localhost:5173")
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins:     strings.Join(origins, ","),
		AllowHeaders:     "Origin, Content-Type, Authorization, X-Request-ID, X-Soulacy-Workspace",
		AllowMethods:     "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		AllowCredentials: false,
	}))

	// Rate limiting — 600 req/min per IP, but ONLY for the JSON API surface.
	// Static GUI assets, the WebSocket event stream, and health probes are
	// exempted: a hard-reload of the Svelte bundle fires dozens of asset
	// requests in a second and would otherwise blow the budget instantly.
	app.Use(limiter.New(limiter.Config{
		Max:        600,
		Expiration: time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		Next: func(c *fiber.Ctx) bool {
			p := c.Path()
			// Skip static assets, websocket, favicon, service worker, health
			if strings.HasPrefix(p, "/assets/") ||
				strings.HasPrefix(p, "/ws") ||
				p == "/" || p == "/favicon.ico" || p == "/sw.js" ||
				p == "/api/v1/health" {
				return true
			}
			return false
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "rate limit exceeded",
			})
		},
	}))

	// --- WebSocket event stream ---
	// PRODUCTION_AUDIT → CRITICAL/Security: previously unauthenticated. The
	// event stream carries prompts, tool inputs, tool outputs (which often
	// contain PII/secrets), agent memory writes — i.e. everything you'd
	// least want to expose. Auth is enforced via the same engine as the REST
	// API; browser WebSocket connections that can't set headers may pass the
	// credential as ?api_key= query param.
	// Story 19c: scoped plugin tokens are accepted here too — a plugin whose
	// manifest grants the events.subscribe capability (E5 grammar) may
	// stream the event feed; plugin tokens WITHOUT that grant get 403.
	// User credentials flow through the regular auth engine unchanged.
	app.Use("/ws", func(c *fiber.Ctx) error {
		if !fws.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		if handled, err := s.wsPluginTokenAuth(c); handled {
			return err
		}
		return s.authHandler()(c)
	})
	app.Use("/ws", func(c *fiber.Ctx) error {
		c.Locals(wsPrincipalKey, websocketPrincipalFromCtx(c))
		return c.Next()
	})
	app.Get("/ws/events", fws.New(s.hub.Handler))

	// --- WhatsApp webhook (unauthenticated — Meta doesn't know our API key) ---
	// GET: Meta webhook verification; POST: inbound messages.
	if s.waChan != nil {
		app.Get("/channels/whatsapp/webhook", func(c *fiber.Ctx) error {
			mode := c.Query("hub.mode")
			token := c.Query("hub.verify_token")
			challenge := c.Query("hub.challenge")
			if ch, ok := s.waChan.Verify(mode, token, challenge); ok {
				return c.SendString(ch)
			}
			return c.SendStatus(fiber.StatusForbidden)
		})
		app.Post("/channels/whatsapp/webhook", func(c *fiber.Ctx) error {
			// PRODUCTION_AUDIT hardening:
			//   - CRIT/Security: verify Meta's X-Hub-Signature-256 HMAC over
			//     the raw body before dispatching. Without this, anyone who
			//     learns the webhook URL can forge inbound messages and
			//     trigger agent runs / outbound WhatsApp sends.
			//   - HIGH/Reliability: bytes.Clone before handing to the
			//     goroutine (Fiber's body buffer ownership beyond the handler
			//     is unclear) + defer recover so a malformed-payload panic
			//     during json.Unmarshal doesn't crash the gateway.
			body := bytes.Clone(c.Body())
			sig := c.Get("X-Hub-Signature-256")
			if !s.waChan.VerifySignature(sig, body) {
				s.log.Warn("whatsapp webhook signature mismatch — rejecting",
					zap.String("remote", c.IP()))
				return c.SendStatus(fiber.StatusForbidden)
			}
			go func() {
				defer func() {
					if r := recover(); r != nil {
						s.log.Error("whatsapp webhook dispatch panicked",
							zap.Any("recover", r))
					}
				}()
				s.waChan.Dispatch(body)
			}()
			return c.SendStatus(fiber.StatusOK)
		})
	}

	// --- Debug endpoint (unauthenticated) — strictly no key material. ---
	// Reports auth posture + mode so the GUI knows which flow to present.
	app.Get("/ping", func(c *fiber.Ctx) error {
		authStatus := "required"
		authMode := "apikey"
		if s.cfg.Server.APIKey == "" && s.authEngine == nil {
			authStatus = "open"
			authMode = "none"
		} else if s.authEngine != nil {
			authMode = s.authEngine.Mode()
			if s.cfg.Server.APIKey == "" && authMode == "apikey" {
				authStatus = "open"
			}
		}
		return c.JSON(fiber.Map{
			"auth":   authStatus,
			"mode":   authMode,
			"status": "ok",
		})
	})

	// --- Auth endpoints (public — no auth middleware) ---
	// POST /api/v1/auth/token   — exchange static API key for JWT pair (jwt mode)
	// POST /api/v1/auth/refresh — rotate refresh token → new access token (jwt mode)
	// GET  /api/v1/auth/me      — return identity from current token (protected below)
	if s.authEngine != nil {
		app.Post("/api/v1/auth/token", s.authEngine.HandleTokenRequest)
		app.Post("/api/v1/auth/refresh", s.authEngine.HandleRefresh)
		app.Post("/api/v1/auth/logout", s.authEngine.HandleLogout)
		app.Get("/api/v1/auth/oidc/config", s.authEngine.HandleOIDCConfig)
		app.Get("/api/v1/auth/oidc/start", s.authEngine.HandleOIDCStart)
		app.Post("/api/v1/auth/oidc/start", s.authEngine.HandleOIDCStart)
		app.Get("/api/v1/auth/oidc/callback", s.authEngine.HandleOIDCCallback)
		app.Post("/api/v1/auth/oidc/complete", s.authEngine.HandleOIDCComplete)
		app.Post("/api/v1/auth/oidc/device/start", s.authEngine.HandleOIDCDeviceStart)
		app.Post("/api/v1/auth/oidc/device/poll", s.authEngine.HandleOIDCDevicePoll)
	}
	// Invitation acceptance requires an authenticated identity but deliberately
	// runs before workspace resolution: a new user has no membership yet.
	app.Post("/api/v1/invitations/accept", s.authWithPluginTokens(), s.rlUserMW(), s.handleAcceptInvitation)

	// --- Shared read-only chat sessions (public — no auth) ---
	// A share token is an unguessable capability, so the read view bypasses the
	// API key just like the static GUI does. Registered before the /api/v1
	// group so it isn't caught by the auth middleware.
	app.Get("/api/v1/shared/:token", s.handleShareView)

	// --- Plugin GUI mounts (Story E8) ---
	// Static plugin UIs — no auth, same policy as the main GUI bundle below.
	// Mounts are resolved at request time so SetPluginUI can run after New().
	app.Get("/plugins/:pid/ui/*", s.handlePluginUIAsset)

	// --- API routes ---
	// Auth middleware runs first (recognising scoped plugin tokens, E8),
	// then the plugin default-deny gate, then per-user rate limiting (after
	// claims are populated). RBAC and per-agent limits are applied per-route.
	// idempotencyMW runs after workspace context is established so replay keys
	// are namespaced by the verified workspace, and before handlers so a
	// duplicate never reaches one.
	api := app.Group("/api/v1", s.authWithPluginTokens(), s.workspaceContextMW(), s.pluginGateMW(), s.rlUserMW(), s.idempotencyMW())

	// Health
	api.Get("/health", s.handleHealth)
	// Capability negotiation. Deliberately alongside health: a client must be
	// able to discover compatibility before it knows whether it can
	// authenticate, otherwise an auth failure and a version failure are
	// indistinguishable.
	api.Get("/capabilities", s.handleCapabilities)

	// Plugin UI discovery + scoped token issuance (Story E8). User-facing:
	// the Svelte shell lists mounts for its nav and fetches a per-plugin
	// token for the sandboxed iframe. Plugin tokens themselves cannot reach
	// these routes (not in the gate's policy table).
	api.Get("/plugins/ui", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleListPluginUIs)
	api.Post("/plugins/:id/token", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleIssuePluginToken)

	// Plugin install & management (Story E13) — admin surface, config-level
	// rbac. Plugin principals are default-denied by pluginGateMW (E8).
	api.Get("/plugins/installed", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleListInstalledPlugins)
	api.Post("/plugins/install", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleStagePlugin)
	api.Post("/plugins/install/:staged/approve", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleApprovePlugin)
	api.Delete("/plugins/install/:staged", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleDiscardStagedPlugin)
	api.Post("/plugins/:id/enable", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleSetPluginEnabled(true))
	api.Post("/plugins/:id/disable", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleSetPluginEnabled(false))
	api.Post("/plugins/:id/reapprove", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleReapprovePlugin)
	api.Delete("/plugins/:id", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleRemovePlugin)

	// Auth identity — returns claims from the current token; useful for GUI.
	if s.authEngine != nil {
		api.Get("/auth/me", s.authEngine.HandleMe)
	}

	// Workspace membership administration uses the freshly resolved role on
	// every request rather than a role embedded in an older access token.
	api.Get("/workspace/members", s.handleListWorkspaceMembers)
	api.Patch("/workspace/members/:id/role", s.handleSetWorkspaceMemberRole)
	api.Patch("/workspace/members/:id/status", s.handleSetWorkspaceMemberStatus)
	api.Delete("/workspace/members/:id", s.handleRemoveWorkspaceMember)
	api.Get("/workspace/invitations", s.handleListWorkspaceInvitations)
	api.Post("/workspace/invitations", s.handleCreateWorkspaceInvitation)
	api.Get("/workspace/membership-audit", s.handleWorkspaceMembershipAudit)

	// Context resolution for the CLI and GUI. These report the identity and
	// workspace set the server verified for this request, which is what a
	// context switcher must display and act on.
	api.Get("/workspace/identity", s.handleWorkspaceIdentity)
	api.Get("/workspace/workspaces", s.handleListSelectableWorkspaces)
	api.Post("/workspace/select", s.handleSelectWorkspace)

	// Prometheus metrics. Wrapped in the API auth group so the same key
	// gates scraping. Scrape via:
	//   curl -H 'Authorization: Bearer <key>' http://gw/api/v1/metrics
	api.Get("/metrics", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), adaptor.HTTPHandler(metrics.Handler()))
	api.Post("/admin/restart", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleRestart)
	api.Get("/admin/audit", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleAdminAudit)
	api.Get("/onboarding/status", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleOnboardingStatus)
	// Per-page walkthrough: the same outcome told from whichever screen you are on.
	api.Get("/tour/:page", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleTour)
	api.Get("/readiness", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleReadiness)
	s.registerUpdatesRoutes(api)

	api.Get("/deployment/status", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleDeploymentStatus)
	api.Get("/security/readiness", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleSecurityReadiness)
	// S7 (Cohort F) — Security Doctor: per-agent synthesis view +
	// dry-run injection simulation without executing anything.
	api.Get("/agents/:id/security_doctor", s.rbacAgentMW(rbac.ActionRead), s.handleSecurityDoctor)
	api.Post("/agents/:id/security_doctor/dry_run", s.rbacAgentMW(rbac.ActionRead), s.handleSecurityDoctorDryRun)
	api.Get("/executors", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleExecutors)

	// Agents
	api.Get("/agents", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleListAgents)
	api.Post("/agents/validate", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleValidateAgent)
	api.Post("/agents/package/inspect", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleInspectAgentPackage)
	api.Post("/agents/package/import", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleImportAgentPackage)
	api.Get("/agents/:id", s.rbacAgentMW(rbac.ActionRead), s.handleGetAgent)
	api.Get("/agents/:id/yaml", s.rbacAgentMW(rbac.ActionRead), s.handleGetAgentYAML)
	api.Get("/agents/:id/package", s.rbacAgentMW(rbac.ActionRead), s.handleGetAgentPackage)
	api.Put("/agents/:id/yaml", s.rbacAgentMW(rbac.ActionWrite), s.handleUpdateAgentYAML)
	api.Get("/agents/:id/versions", s.rbacAgentMW(rbac.ActionRead), s.handleListAgentVersions)
	api.Get("/agents/:id/versions/:version", s.rbacAgentMW(rbac.ActionRead), s.handleGetAgentVersion)
	api.Post("/agents/:id/rollback", s.rbacAgentMW(rbac.ActionWrite), s.handleRollbackAgent)
	api.Get("/agents/:id/tier", s.rbacAgentMW(rbac.ActionRead), s.handleGetAgentTier)
	api.Post("/agents", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleCreateAgent)
	api.Put("/agents/:id", s.rbacAgentMW(rbac.ActionWrite), s.handleUpdateAgent)
	api.Delete("/agents/:id", s.rbacAgentMW(rbac.ActionDelete), s.handleDeleteAgent)
	api.Post("/agents/:id/enable", s.rbacAgentMW(rbac.ActionEnable), s.handleEnableAgent)
	api.Post("/agents/:id/disable", s.rbacAgentMW(rbac.ActionEnable), s.handleDisableAgent)

	// Realtime voice control plane (Story 11): availability + ephemeral
	// client keys for the browser's direct provider connection. Same RBAC
	// surface as chat (the panel lives in Chat).
	api.Get("/voice/status", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleVoiceStatus)
	api.Post("/voice/ephemeral", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleVoiceEphemeral)
	api.Get("/voice/capabilities", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleVoiceCapabilities)
	api.Post("/voice/transcribe", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleVoiceTranscribe)
	api.Post("/voice/synthesize", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleVoiceSynthesize)

	// Chat — token-quota (user + agent) + per-agent RPM checks applied on top of user RPM.
	api.Get("/chat/status", s.rbacMW(rbac.ResourceChat, rbac.ActionRead), s.handleChatStatus)
	api.Post("/chat", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{BodyField: "agent_id"}), s.rlTokenMW(), s.rlAgentTokenMW(), s.rlAgentMW(), s.handleChat)
	api.Post("/chat/feedback", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{BodyField: "agent_id"}), s.handleChatFeedback)
	api.Get("/learning/feedback", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleListChatFeedback)
	api.Post("/chat/stream", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{BodyField: "agent_id"}), s.rlTokenMW(), s.rlAgentTokenMW(), s.rlAgentMW(), s.handleChatStream)
	api.Get("/chat/stream", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{QueryParam: "agent_id"}), s.rlTokenMW(), s.rlAgentTokenMW(), s.rlAgentMW(), s.handleChatStream)
	api.Post("/webhooks/:agent_id", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{PathParam: "agent_id"}), s.rlTokenMW(), s.rlAgentTokenMW(), s.rlAgentMW(), s.handleGenericWebhook)
	api.Post("/chat/confirm", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleToolConfirm)
	// Cancel an in-flight run (Story #22): stop a slow local-model run.
	api.Post("/chat/cancel", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleChatCancel)
	api.Post("/chat/share", s.rbacMW(rbac.ResourceChat, rbac.ActionRead), s.handleCreateShare)
	api.Get("/chat/artifacts", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{QueryParam: "agent_id"}), s.handleChatArtifacts)
	api.Get("/chat/artifacts/download", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{QueryParam: "agent_id"}), s.handleChatArtifactDownload)
	api.Post("/chat/attachments", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{FormField: "agent_id"}), s.handleChatAttachmentUpload)
	api.Get("/chat/attachments", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{QueryParam: "agent_id"}), s.handleChatAttachments)
	api.Get("/chat/attachments/:id/download", s.rbacAgentFromMW(rbac.ResourceChat, rbac.ActionChat, rbac.AgentIDSource{QueryParam: "agent_id"}), s.handleChatAttachmentDownload)

	// Channels
	api.Get("/channels", s.rbacMW(rbac.ResourceChannels, rbac.ActionRead), s.handleListChannels)
	api.Get("/channels/metrics", s.rbacMW(rbac.ResourceChannels, rbac.ActionRead), s.handleChannelMetrics)
	api.Get("/channels/delivery-readiness", s.rbacMW(rbac.ResourceChannels, rbac.ActionRead), s.handleChannelDeliveryReadiness)
	api.Patch("/channels/:id", s.rbacMW(rbac.ResourceChannels, rbac.ActionWrite), s.handleUpdateChannel)
	api.Post("/channels/:id/test", s.rbacMW(rbac.ResourceChannels, rbac.ActionWrite), s.handleTestChannelDelivery)
	api.Post("/channels/:id/diagnose", s.rbacMW(rbac.ResourceChannels, rbac.ActionWrite), s.handleDiagnoseChannelDelivery)
	api.Post("/channels/whatsapp_web/pair", s.rbacMW(rbac.ResourceChannels, rbac.ActionWrite), s.handleStartWhatsAppWebPairing)
	api.Post("/channels/:id/enable", s.rbacMW(rbac.ResourceChannels, rbac.ActionEnable), s.handleEnableChannel)
	api.Post("/channels/:id/disable", s.rbacMW(rbac.ResourceChannels, rbac.ActionEnable), s.handleDisableChannel)

	// Schedule
	api.Get("/schedule", s.rbacMW(rbac.ResourceSchedule, rbac.ActionRead), s.handleListSchedule)
	api.Get("/schedule/status", s.rbacMW(rbac.ResourceSchedule, rbac.ActionRead), s.handleScheduleStatus)
	api.Post("/agents/:id/trigger", s.rbacAgentMW(rbac.ActionWrite), s.handleManualTrigger)
	api.Post("/agents/:id/replay", s.rbacAgentMW(rbac.ActionWrite), s.handleReplayAgentRun)
	api.Post("/agents/:id/schedule-output/test", s.rbacAgentMW(rbac.ActionWrite), s.handleTestScheduledOutput)
	api.Post("/agents/:id/clone", s.rbacAgentMW(rbac.ActionWrite), s.handleCloneAgent)
	api.Get("/agents/:id/actions", s.rbacAgentMW(rbac.ActionRead), s.handleAgentActions)

	// Session memory (existing)
	api.Get("/memory/:agent_id", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agent_id"}), s.handleListMemory)
	api.Delete("/memory/:agent_id/:session_id", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionDelete, rbac.AgentIDSource{PathParam: "agent_id"}), s.requirePathSessionMW("session_id", "agent_id"), s.handleDeleteMemorySession)

	// Brain memory — three-layer long-term agent memory (episodic / procedural / semantic)
	api.Get("/brain-memory", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleBrainMemoryStats)
	api.Get("/brain-memory/:agentID/episodic", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agentID"}), s.handleGetEpisodic)
	api.Post("/brain-memory/:agentID/episodic", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionWrite, rbac.AgentIDSource{PathParam: "agentID"}), s.handleWriteEpisodic)
	api.Delete("/brain-memory/:agentID/episodic", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionDelete, rbac.AgentIDSource{PathParam: "agentID"}), s.handleClearEpisodic)
	api.Get("/brain-memory/:agentID/procedural", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agentID"}), s.handleGetProcedural)
	api.Put("/brain-memory/:agentID/procedural", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionWrite, rbac.AgentIDSource{PathParam: "agentID"}), s.handleUpdateProcedural)
	api.Delete("/brain-memory/:agentID/procedural", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionDelete, rbac.AgentIDSource{PathParam: "agentID"}), s.handleClearProcedural)
	api.Post("/brain-memory/:agentID/context-preview", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agentID"}), s.handleContextPreview)
	// Versioned rulebooks (Story E23): history, single versions, rollback, lock.
	api.Get("/brain-memory/:agentID/rulebook", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agentID"}), s.handleRulebookHistory)
	api.Get("/brain-memory/:agentID/rulebook/:version", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agentID"}), s.handleRulebookVersion)
	api.Post("/brain-memory/:agentID/rulebook/rollback", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionWrite, rbac.AgentIDSource{PathParam: "agentID"}), s.handleRulebookRollback)
	api.Post("/brain-memory/:agentID/rulebook/lock", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionWrite, rbac.AgentIDSource{PathParam: "agentID"}), s.handleRulebookLock)
	api.Get("/learning/summary", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleLearningSummary)
	api.Get("/learning/evidence", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleLearningEvidence)
	api.Get("/proactive/suggestions", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleProactiveSuggestions)
	api.Get("/browser/trace", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleBrowserTrace)
	api.Get("/browser/artifact", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleBrowserArtifact)
	api.Get("/browser/status", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleBrowserStatus)
	api.Get("/mobile/status", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleMobileStatus)
	api.Post("/pairing/tokens", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleCreatePairingToken)
	api.Post("/pairing/redeem", s.handleRedeemPairingToken)
	api.Get("/approvals", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleListApprovals)
	api.Post("/approvals/:id/approve", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleResolveApproval(true))
	api.Post("/approvals/:id/deny", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleResolveApproval(false))
	api.Get("/push/public-key", s.handlePushPublicKey)
	api.Post("/push/subscribe", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handlePushSubscribe)
	api.Post("/push/unsubscribe", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handlePushUnsubscribe)
	api.Post("/push/test", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handlePushTest)
	s.wirePushNotifications()
	api.Get("/learning/proposals", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.handleListLearningProposals)
	api.Post("/learning/propose-from-run", s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite), s.handleProposeLearningFromRun)
	api.Post("/learning/reflect-recent-runs", s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite), s.handleReflectLearningFromRecentRuns)
	api.Patch("/learning/proposals/:id", s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite), s.handleUpdateLearningProposal)
	api.Post("/learning/proposals/:id/accept", s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite), s.handleAcceptLearningProposal)
	api.Post("/learning/proposals/:id/reject", s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite), s.handleRejectLearningProposal)
	api.Post("/learning/proposals/:id/disable", s.rbacMW(rbac.ResourceMemory, rbac.ActionWrite), s.handleDisableLearningProposal)

	// Ephemeral queues — live workflow handoff buffers shared by ReAct agents,
	// Studio workflows, MCP clients, and admin tooling. They are in-memory and
	// reset on gateway restart, by design.
	api.Get("/queues", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleQueueNames)
	api.Post("/queues", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleQueueCreate)
	api.Get("/queues/items", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleQueueList)
	api.Post("/queues/items", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleQueuePut)
	api.Post("/queues/take", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleQueueTake)
	api.Delete("/queues/items", s.rbacMW(rbac.ResourceAgents, rbac.ActionDelete), s.handleQueueClear)

	// Providers
	api.Get("/doctor", s.rbacMW(rbac.ResourceProviders, rbac.ActionRead), s.handleDoctor)
	api.Get("/providers", s.rbacMW(rbac.ResourceProviders, rbac.ActionRead), s.handleListProviders)
	api.Get("/providers/:id/models", s.rbacMW(rbac.ResourceProviders, rbac.ActionRead), s.handleListModels)
	api.Post("/providers/:id/model", s.rbacMW(rbac.ResourceProviders, rbac.ActionWrite), s.handleSetProviderModel)
	api.Post("/providers/:id", s.rbacMW(rbac.ResourceProviders, rbac.ActionWrite), s.handleSetProviderCredentials)
	api.Delete("/providers/:id", s.rbacMW(rbac.ResourceProviders, rbac.ActionWrite), s.handleDeleteProvider)

	// Skills (Agent Skills format — agentskills.io)
	api.Get("/skills", s.rbacMW(rbac.ResourceSkills, rbac.ActionRead), s.handleListSkills)
	api.Get("/skills/:name", s.rbacMW(rbac.ResourceSkills, rbac.ActionRead), s.handleGetSkill)
	api.Post("/skills/install", s.rbacMW(rbac.ResourceSkills, rbac.ActionWrite), s.handleInstallRegistrySkill)
	api.Post("/skills/provision-agenticskills", s.rbacMW(rbac.ResourceSkills, rbac.ActionWrite), s.handleProvisionAgenticSkill)
	api.Post("/skills/rescan", s.rbacMW(rbac.ResourceSkills, rbac.ActionWrite), s.handleRescanSkills)
	api.Get("/marketplace/status", s.rbacMW(rbac.ResourceSkills, rbac.ActionRead), s.handleMarketplaceStatus)

	// MCP (Model Context Protocol) — configured external servers + their tools
	api.Get("/mcp", s.rbacMW(rbac.ResourceMCP, rbac.ActionRead), s.handleListMCP)
	api.Post("/mcp", s.rbacMW(rbac.ResourceMCP, rbac.ActionWrite), s.handleCreateMCPServer)
	api.Patch("/mcp/:id", s.rbacMW(rbac.ResourceMCP, rbac.ActionWrite), s.handleUpdateMCPServer)
	api.Delete("/mcp/:id", s.rbacMW(rbac.ResourceMCP, rbac.ActionDelete), s.handleDeleteMCPServer)
	api.Post("/mcp/test", s.rbacMW(rbac.ResourceMCP, rbac.ActionRead), s.handleTestMCPServer)
	api.Post("/mcp/provision-glama", s.rbacMW(rbac.ResourceMCP, rbac.ActionWrite), s.handleProvisionGlama)
	api.Get("/mcp/registry/search", s.rbacMW(rbac.ResourceMCP, rbac.ActionRead), s.handleMCPRegistrySearch)
	api.Post("/mcp/provision-registry", s.rbacMW(rbac.ResourceMCP, rbac.ActionWrite), s.handleProvisionMCPRegistry)

	// Knowledge (RAG) — KBs, documents, search
	api.Get("/knowledge", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionRead), s.handleListKnowledge)
	api.Post("/knowledge", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionWrite), s.handleCreateKnowledge)
	api.Delete("/knowledge/:kb", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionDelete), s.handleDeleteKnowledge)
	api.Get("/knowledge/:kb/documents", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionRead), s.handleListKnowledgeDocuments)
	api.Post("/knowledge/:kb/documents", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionWrite), s.handleIngestDocument)
	api.Delete("/knowledge/:kb/documents/:doc", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionDelete), s.handleDeleteKnowledgeDocument)
	// Async ingestion: upload returns 202 + a job; these track and retry it.
	// The per-job routes deliberately live OUTSIDE /knowledge/... — a path like
	// /knowledge/jobs/:job collides with the /knowledge/:kb/... family (a KB
	// literally named "jobs" would shadow it).
	api.Get("/knowledge/:kb/jobs", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionRead), s.handleListIngestJobs)
	api.Get("/ingest-jobs/:job", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionRead), s.handleGetIngestJob)
	api.Post("/ingest-jobs/:job/retry", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionWrite), s.handleRetryIngestJob)
	api.Post("/knowledge/:kb/search", s.rbacMW(rbac.ResourceKnowledge, rbac.ActionRead), s.handleSearchKnowledge)

	// Unified tool catalog (python tools + MCP tools + Go built-ins)
	api.Get("/tool-catalog", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleToolCatalog)
	api.Post("/tools/run", s.rbacMW(rbac.ResourceChat, rbac.ActionChat), s.handleRunTool)

	// Conversational Agent Builder
	api.Post("/builder/chat", s.rbacMW(rbac.ResourceBuilder, rbac.ActionWrite), s.handleBuilderChat)
	api.Post("/builder/generate", s.rbacMW(rbac.ResourceBuilder, rbac.ActionWrite), s.handleBuilderGenerate)
	api.Post("/builder/deploy", s.rbacMW(rbac.ResourceBuilder, rbac.ActionWrite), s.handleBuilderDeploy)
	api.Delete("/builder/session/:id", s.rbacMW(rbac.ResourceBuilder, rbac.ActionWrite), s.handleBuilderDeleteSession)

	// Studio plugin backend: mandatory pre-generation prompt refinement — turn a
	// rough intent into a clear spec + assumptions + clarifying questions before
	// a workflow is compiled.
	api.Post("/studio/refine-prompt", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioRefinePrompt)
	// Studio plugin backend (Story S1.1): intent compiler.
	api.Post("/studio/compile", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioCompile)
	// Studio plugin backend: generate a ReAct/Plan-Execute AGENT (no fixed flow)
	// for intents that need a reasoning loop (local-first pivot).
	api.Post("/studio/compile-agent", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioCompileAgent)
	// ST-01: Studio's structured reading of an intent ("Studio understood…"),
	// with the blocking questions and — when previous_intent is supplied — the
	// visible change summary that proves a refine changed the BUILD and not just
	// the wording. Deterministic, no model call: read-only.
	api.Post("/studio/build-spec", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioBuildSpec)
	// ST-03 / ST-06: the plain-language plan — when it starts, what work happens
	// (including parallel groups and their DECLARED join policy), where the result
	// goes — so a user who cannot read a graph can still approve one. Read-only
	// projection of the posted draft; nothing is persisted.
	api.Post("/studio/plan-view", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioPlanView)
	// ST-09: the model capability registry the Strategy Advisor decides from, so
	// "Soulacy selected Workflow" can be checked rather than trusted.
	api.Get("/studio/model-capabilities", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioModelCapabilities)
	api.Get("/studio/strategy-fit", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioStrategyFit)
	api.Get("/studio/learning-memory", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioLearningMemory)
	api.Delete("/studio/learning-memory/:kind/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioDeleteLearningMemory)
	// Studio plugin backend: consolidated pre-save validation (missing tools/MCP/
	// channels/secrets, empty required args, invalid schedules).
	api.Post("/studio/preflight", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioPreflight)
	// Studio generation contract: graph + runtime preflight + authoring rules.
	api.Post("/studio/contract", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioContract)
	// Studio security review (S6, Cohort F): pre-save summary of trust
	// boundaries + network / file / channel / privileged tool usage +
	// confirmation requirements + safer-scoped-tool recommendations.
	api.Post("/studio/security_review", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioSecurityReview)
	// Studio plugin backend: builder-model strength advice (warn on weak models).
	api.Get("/studio/model-advice", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioModelAdvice)
	// Story 9 (Cohort B): intent-named preset catalog ("fast local" / "reliable
	// local" / "cloud quality") for the GUI's Studio picker.
	api.Get("/studio/presets", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioPresets)
	// Studio plugin backend: deterministic + iterative-LLM repair for the "Fix
	// automatically" action.
	api.Post("/studio/autowire", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioAutowire)
	// Studio plugin backend: AI troubleshoot of a runtime error ("Fix with AI").
	api.Post("/studio/troubleshoot", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioTroubleshoot)
	// Studio Architect: autonomous build-verify-repair loop ("Build until it works").
	api.Post("/studio/build", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioBuild)
	// Streaming variant: live progress as a text/event-stream.
	api.Post("/studio/build/stream", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioBuildStream)
	// Story 9 M (Cohort C): streamed generate pipeline — surfaces the 5
	// phases (clarify_intent → choose_strategy → build_graph → validate →
	// repair) as SSE events. Consumed by both the live-transcript panel
	// (default) and the wizard-mode stepped modal (buffered client-side).
	api.Post("/studio/generate/stream", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioGenerateStream)
	// Studio Architect: list/diagnose failed runs and self-heal the saved agent.
	api.Get("/studio/failed-runs", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioFailedRuns)
	api.Get("/studio/run-trace", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioRunTrace)
	api.Get("/studio/run-diagnosis", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioRunDiagnosis)
	api.Get("/studio/run-history", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioRunHistory)
	api.Get("/studio/build-trace", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioBuildTrace)
	api.Get("/studio/build-traces", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioBuildTraces)
	api.Post("/studio/diagnose-run", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioDiagnoseRun)
	api.Post("/studio/diagnose-session", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioDiagnoseSession)
	// Studio plugin backend (Wave 2): dry-run test and save-as-disabled-agent.
	api.Post("/studio/test", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioTest)
	// Try an UNSAVED reasoning agent against one sample question (ephemeral run).
	api.Post("/studio/try-agent", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioTryAgent)
	// What a Run Live of this draft would actually be allowed to do — the same
	// preview /studio/try-agent refuses with (409), WITHOUT running anything, so
	// the GUI can show the confirmation dialog before the operator commits.
	// Read-only: gated on ActionRead.
	api.Post("/studio/run-preview", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioRunPreview)
	// ST-07: ONE readiness verdict — preflight + generation contract + security
	// review + consent — replacing the client-side stitch of three endpoints.
	// A section the server could not evaluate is reported Unknown and forces
	// ok=false, so readiness can no longer come back green because a call failed
	// and the GUI quietly dropped it.
	api.Post("/studio/readiness", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioReadiness)
	// Studio plugin backend (M2): capability-tier consent plan + gated save.
	api.Post("/studio/plan", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioPlan)
	api.Post("/studio/save", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioSave)
	// Studio Canvas⇄Code (SOUL.yaml) view: serialize a draft to YAML, parse
	// edited YAML back to a draft (with lossiness warnings), and save authored
	// YAML directly to disk (code view is authoritative).
	api.Post("/studio/yaml", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioYAML)
	api.Post("/studio/from-yaml", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioFromYAML)
	api.Post("/studio/save-yaml", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioSaveYAML)
	api.Post("/studio/validate-yaml", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioValidateYAML)
	api.Post("/studio/fix-yaml", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioFixYAML)
	api.Get("/studio/rules", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioGetRules)
	api.Put("/studio/rules", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioSaveRules)
	// The store has always been append-only; this is what makes its history
	// reachable, so a deployment record's RulesVersion can be looked up.
	api.Get("/studio/rules/history", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioRulesHistory)
	api.Post("/studio/review-yaml", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioReviewYAML)
	// Studio plugin backend (M3): canvas-time graph validation (read-only).
	api.Post("/studio/validate", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioValidate)
	// Learn-from-Run-Live: propose repairs from the last run's node trace, and
	// apply one approved proposal (re-validated). Nothing is auto-applied.
	api.Post("/studio/repair-live", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioRepairLive)
	api.Post("/studio/apply-repair", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioApplyRepair)
	api.Post("/studio/diff", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioDiff)
	api.Post("/studio/add-step", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioAddStep)
	// Studio plugin backend (M6): starter templates (read-only).
	api.Get("/studio/templates", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioTemplates)
	// Phase 2: coarse composite-block catalog (read-only) — one node that
	// encapsulates a whole multi-step dance (e.g. NotebookLM podcast).
	api.Get("/studio/composite-blocks", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioCompositeBlocks)
	// Phase B: compile a plain-language connector gate into a flow predicate.
	api.Post("/studio/compile-gate", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioCompileGate)
	// Phase C: compile ONE node from its plain-language intent into concrete config.
	api.Post("/studio/compile-node", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioCompileNode)
	// Studio plugin backend (M6): user draft library (save/list/load/delete).
	api.Post("/studio/drafts", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioSaveDraft)
	api.Get("/studio/drafts", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioListDrafts)
	api.Get("/studio/drafts/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioLoadDraft)
	api.Delete("/studio/drafts/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionDelete), s.handleStudioDeleteDraft)
	// Studio plugin backend (M6): per-node re-describe.
	api.Post("/studio/refine", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioRefine)
	// Studio "My Workflows": list workflow-bearing agents + load one as a draft.
	api.Get("/studio/agents", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioListAgents)
	api.Get("/studio/agents/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioLoadAgent)
	// Built-in framework Python scaffolds for the Custom Python editor.
	api.Get("/studio/scaffolds", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleStudioScaffolds)
	// In-framework code generation for one Custom Python node.
	api.Post("/studio/codegen", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleStudioCodegen)

	// E4: Capability Gap Detection
	if s.builderRegistry != nil {
		analyzer := builder.NewGapAnalyzer(s.builderRegistry)
		builderAPI := builder.NewAPIHandler(analyzer, s.log)
		api.Post("/builder/analyze", s.rbacMW(rbac.ResourceBuilder, rbac.ActionWrite), builderAPI.HandleAnalyze)
		api.Post("/builder/resolve", s.rbacMW(rbac.ResourceBuilder, rbac.ActionWrite), builderAPI.HandleResolve)
	}

	// Templates (starter agent definitions — "New from template" flow)
	api.Get("/templates", s.rbacMW(rbac.ResourceTemplates, rbac.ActionRead), s.handleListTemplates)
	api.Post("/templates/:name/instantiate", s.rbacMW(rbac.ResourceTemplates, rbac.ActionWrite), s.handleInstantiateTemplate)
	api.Get("/templates/:name/readiness", s.rbacMW(rbac.ResourceTemplates, rbac.ActionRead), s.handleTemplateReadiness)
	api.Post("/templates/:name/mock-test", s.rbacMW(rbac.ResourceTemplates, rbac.ActionRead), s.handleTemplateMockTest)

	// Config (read / write config.yaml via API)
	api.Get("/config", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleGetConfig)
	api.Patch("/config", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handlePatchConfig)

	// Skill sources / package registries (Story E26: review URL → add source)
	api.Get("/registries", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleListRegistries)
	api.Get("/registries/search", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), s.handleSearchRegistries)
	api.Post("/registries/probe", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleProbeRegistry)
	api.Post("/registries", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), s.handleAddRegistry)

	// Logs (tail gateway log file)
	api.Get("/logs", s.rbacMW(rbac.ResourceLogs, rbac.ActionRead), s.handleGetLogs)
	api.Get("/support/bundle", s.rbacMW(rbac.ResourceLogs, rbac.ActionRead), s.handleSupportBundle)

	// RBAC management (admin only — enforced by static policy)
	if s.rbacManager != nil {
		api.Get("/rbac/policy", s.rbacMW(rbac.ResourceRBAC, rbac.ActionRead), s.rbacManager.HandleListPolicy)
		api.Get("/rbac/grants", s.rbacMW(rbac.ResourceRBAC, rbac.ActionRead), s.rbacManager.HandleListGrants)
		api.Get("/rbac/grants/:role", s.rbacMW(rbac.ResourceRBAC, rbac.ActionRead), s.rbacManager.HandleListGrantsForRole)
		api.Put("/rbac/grants/:role/:agent_id", s.rbacMW(rbac.ResourceRBAC, rbac.ActionWrite), s.rbacManager.HandleSetAgentGrant)
		api.Delete("/rbac/grants/:role/:agent_id", s.rbacMW(rbac.ResourceRBAC, rbac.ActionDelete), s.rbacManager.HandleDeleteAgentGrant)
	}

	// --- Rate Limit status (Task #33) ---
	// Always registered; returns 503 when no limiter is configured.
	api.Get("/rate-limit/status", func(c *fiber.Ctx) error {
		if s.rateLimiter == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "rate limiting not configured",
			})
		}
		return s.rateLimiter.HandleStatus(c)
	})

	// --- Cost Tracking (Task #32) ---
	// Routes are only registered when a cost store has been wired via
	// SetCostStore(). Note: SetCostStore must be called before the server
	// starts listening; buildApp() is called from New() so the store must
	// be wired after New() returns but before Listen() is called. Because
	// Fiber registers routes at build time, we register a stub here that
	// reads from s.costStore at request time so the nil guard works.
	api.Get("/costs", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleGetCosts)
	api.Get("/costs/status", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleCostStatus)
	api.Post("/costs/estimate", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleCostEstimate)
	api.Get("/costs/usage", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleGetCostUsage)
	api.Get("/costs/chargeback", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleGetCostChargeback)
	api.Get("/costs/reconciliations", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleGetCostReconciliations)
	api.Post("/costs/reconcile", s.rbacMW(rbac.ResourceMetrics, rbac.ActionWrite), s.handlePostCostReconciliation)
	api.Get("/costs/:agent_id", s.rbacAgentFromMW(rbac.ResourceMetrics, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agent_id"}), s.handleGetAgentCosts)
	api.Get("/ops/alerts/status", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleOpsAlertStatus)
	api.Post("/ops/alerts/test", s.rbacMW(rbac.ResourceChannels, rbac.ActionWrite), s.handleOpsAlertTest)
	api.Post("/ops/alerts/evaluate", s.rbacMW(rbac.ResourceChannels, rbac.ActionWrite), s.handleOpsAlertEvaluate)

	// --- Chat checkpoints & branching (Story 8) ---
	// Fork a session's conversation at a checkpoint entry into a new branch.
	api.Post("/history/:session_id/fork", s.rbacMW(rbac.ResourceChat, rbac.ActionWrite), s.requirePathSessionMW("session_id", ""), s.handleForkSession)

	// --- Run-level observability (Story 7) ---
	// Stores checked at request time; 503 when neither costs nor action log
	// is wired, 404 when the session has no recorded data.
	api.Get("/runs/ops-summary", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleOpsSummary)
	api.Get("/runs/slo-status", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleSLOStatus)
	api.Get("/runs/ledger", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleRunLedger)
	api.Get("/runs/events", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleRunEvents)
	api.Get("/runs/:session_id/metrics", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.requirePathSessionMW("session_id", ""), s.handleRunMetrics)
	// E4c — hung-session tracker snapshot for the Activity page's "Running now"
	// strip. Read-only, cheap, safe to poll every couple of seconds.
	api.Get("/activity/running", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleActivityRunning)

	// --- Workboard (Story 5) ---
	// s.workboardStore is checked at request time so SetWorkboardStore() can
	// be called after New() but before the server starts listening.
	api.Get("/workboard/tasks", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleWorkboardList)
	api.Post("/workboard/tasks", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleWorkboardCreate)
	api.Get("/workboard/tasks/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleWorkboardGet)
	api.Post("/workboard/tasks/:id/run", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleWorkboardRun)
	api.Get("/workboard/tasks/:id/runs", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleWorkboardListRuns)
	api.Patch("/workboard/tasks/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleWorkboardUpdate)
	api.Delete("/workboard/tasks/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionDelete), s.handleWorkboardDelete)
	// Artifact tracking (Story 13): files produced during runs.
	api.Get("/workboard/tasks/:id/artifacts", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleWorkboardArtifacts)
	api.Get("/workboard/artifacts/:id/download", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleWorkboardArtifactDownload)
	// Collaboration (Story 14): comments + reviewer notes.
	api.Get("/workboard/tasks/:id/comments", s.rbacMW(rbac.ResourceAgents, rbac.ActionRead), s.handleWorkboardComments)
	api.Post("/workboard/tasks/:id/comments", s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite), s.handleWorkboardAddComment)
	api.Delete("/workboard/comments/:id", s.rbacMW(rbac.ResourceAgents, rbac.ActionDelete), s.handleWorkboardDeleteComment)

	// --- Credential Vault ---
	// s.credVault is checked at request time so SetCredentialVault() can be
	// called after New() but before the server starts listening.
	credAPI := credentials.NewLazyAPI(s, s.log)
	api.Post("/credentials/:agentID", s.credentialAudit("credential.set"), s.denyGlobalCredentialScope, s.rbacAgentFromMW(rbac.ResourceCredentials, rbac.ActionSet, rbac.AgentIDSource{PathParam: "agentID"}), credAPI.HandleSet)
	api.Get("/credentials/:agentID", s.denyGlobalCredentialScope, s.rbacAgentFromMW(rbac.ResourceCredentials, rbac.ActionList, rbac.AgentIDSource{PathParam: "agentID"}), credAPI.HandleList)
	// Reading a DECRYPTED vault value is an act of trust on the level of writing
	// one, not of listing agents. Under agents:read the viewer role — whose whole
	// purpose is look-but-don't-touch — could GET the plaintext of any stored
	// credential, which defeats the point of encrypting the vault at all.
	// Listing key NAMES stays on read; fetching a VALUE requires write.
	api.Get("/credentials/:agentID/:key", s.credentialAudit("credential.reveal"), s.denyGlobalCredentialScope, s.rbacAgentFromMW(rbac.ResourceCredentials, rbac.ActionReveal, rbac.AgentIDSource{PathParam: "agentID"}), s.requireCredentialRevealConfirmation, credAPI.HandleGet)
	api.Delete("/credentials/:agentID/:key", s.credentialAudit("credential.delete"), s.denyGlobalCredentialScope, s.rbacAgentFromMW(rbac.ResourceCredentials, rbac.ActionDelete, rbac.AgentIDSource{PathParam: "agentID"}), credAPI.HandleDelete)
	// Credential rotation (type-assert to VersionedVault at request time)
	api.Post("/credentials/:agentID/:key/rotate", s.credentialAudit("credential.rotate"), s.denyGlobalCredentialScope, s.rbacAgentFromMW(rbac.ResourceCredentials, rbac.ActionRotate, rbac.AgentIDSource{PathParam: "agentID"}), func(c *fiber.Ctx) error {
		if s.credVault == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault not configured")
		}
		vv, ok := s.credVault.(credentials.VersionedVault)
		if !ok {
			return s.errMsg(c, fiber.StatusNotImplemented, "vault does not support rotation")
		}
		ver, err := vv.Rotate(c.Context(), c.Params("agentID"), c.Params("key"))
		if err != nil {
			s.log.Error("credential rotate failed", zap.Error(err))
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		return c.JSON(fiber.Map{"agent_id": c.Params("agentID"), "key": c.Params("key"), "new_version": ver})
	})
	api.Get("/credentials/:agentID/:key/versions", s.denyGlobalCredentialScope, s.rbacAgentFromMW(rbac.ResourceCredentials, rbac.ActionList, rbac.AgentIDSource{PathParam: "agentID"}), func(c *fiber.Ctx) error {
		if s.credVault == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault not configured")
		}
		vv, ok := s.credVault.(credentials.VersionedVault)
		if !ok {
			return s.errMsg(c, fiber.StatusNotImplemented, "vault does not support versioning")
		}
		versions, err := vv.ListVersions(c.Context(), c.Params("agentID"), c.Params("key"))
		if err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		return c.JSON(fiber.Map{"versions": versions})
	})

	// --- Global Secrets Store ---
	// Gateway-global secrets layered over the credential vault. The Manager is
	// built per-request from s.CredentialVault() so it's nil-safe and picks up
	// SetCredentialVault() called after New(). Values are never returned.
	api.Get("/secrets", s.rbacMW(rbac.ResourceSecrets, rbac.ActionList), s.handleListSecrets)
	api.Put("/secrets/:name", s.rbacMW(rbac.ResourceSecrets, rbac.ActionSet), s.handleSetSecret)
	api.Delete("/secrets/:name", s.rbacMW(rbac.ResourceSecrets, rbac.ActionDelete), s.handleDeleteSecret)

	// --- API Key Management (admin) ---
	api.Post("/admin/api-keys", s.rbacMW(rbac.ResourceCredentials, rbac.ActionSet), func(c *fiber.Ctx) error {
		if s.apiKeyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "api key store not configured")
		}
		err := apikeys.NewAPI(s.apiKeyStore, s.log).HandleCreate(c)
		target, details := apikeys.AuditSubject(c)
		s.recordAdminAudit(c, "credential.create", "credential", target, responseAuditStatus(c, err), details)
		return err
	})
	api.Get("/admin/api-keys", s.rbacMW(rbac.ResourceCredentials, rbac.ActionList), func(c *fiber.Ctx) error {
		if s.apiKeyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "api key store not configured")
		}
		return apikeys.NewAPI(s.apiKeyStore, s.log).HandleList(c)
	})
	api.Delete("/admin/api-keys/:id", s.rbacMW(rbac.ResourceCredentials, rbac.ActionDelete), func(c *fiber.Ctx) error {
		if s.apiKeyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "api key store not configured")
		}
		err := apikeys.NewAPI(s.apiKeyStore, s.log).HandleRevoke(c)
		s.recordAdminAudit(c, "credential.revoke", "credential", c.Params("id"), responseAuditStatus(c, err), nil)
		return err
	})
	api.Post("/admin/api-keys/:id/rotate", s.rbacMW(rbac.ResourceCredentials, rbac.ActionRotate), func(c *fiber.Ctx) error {
		if s.apiKeyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "api key store not configured")
		}
		err := apikeys.NewAPI(s.apiKeyStore, s.log).HandleRotate(c)
		_, details := apikeys.AuditSubject(c)
		s.recordAdminAudit(c, "credential.rotate", "credential", c.Params("id"), responseAuditStatus(c, err), details)
		return err
	})
	api.Patch("/admin/api-keys/:id/status", s.rbacMW(rbac.ResourceCredentials, rbac.ActionSet), func(c *fiber.Ctx) error {
		if s.apiKeyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "api key store not configured")
		}
		err := apikeys.NewAPI(s.apiKeyStore, s.log).HandleStatus(c)
		_, details := apikeys.AuditSubject(c)
		s.recordAdminAudit(c, "credential.status", "credential", c.Params("id"), responseAuditStatus(c, err), details)
		return err
	})
	api.Post("/admin/api-keys/validate", s.rbacMW(rbac.ResourceCredentials, rbac.ActionReveal), func(c *fiber.Ctx) error {
		if s.apiKeyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "api key store not configured")
		}
		return apikeys.NewAPI(s.apiKeyStore, s.log).HandleValidate(c)
	})

	// --- Dead-Letter Queue (admin) ---
	api.Get("/admin/dlq", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), func(c *fiber.Ctx) error {
		if s.dlqStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "dlq not configured")
		}
		queue := c.Query("queue", "")
		items, err := s.dlqStore.List(c.Context(), queue)
		if err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		if items == nil {
			items = []dlq.DeadLetter{}
		}
		return c.JSON(fiber.Map{"items": items, "count": len(items)})
	})
	api.Get("/admin/dlq/:id", s.rbacMW(rbac.ResourceConfig, rbac.ActionRead), func(c *fiber.Ctx) error {
		if s.dlqStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "dlq not configured")
		}
		item, err := s.dlqStore.Get(c.Context(), c.Params("id"))
		if err != nil {
			if err == dlq.ErrNotFound {
				return s.errMsg(c, fiber.StatusNotFound, "not found")
			}
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		return c.JSON(item)
	})
	api.Delete("/admin/dlq/:id", s.rbacMW(rbac.ResourceConfig, rbac.ActionWrite), func(c *fiber.Ctx) error {
		if s.dlqStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "dlq not configured")
		}
		if err := s.dlqStore.Delete(c.Context(), c.Params("id")); err != nil {
			if err == dlq.ErrNotFound {
				return s.errMsg(c, fiber.StatusNotFound, "not found")
			}
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		return c.JSON(fiber.Map{"status": "deleted", "id": c.Params("id")})
	})

	// --- Conversation History ---
	api.Get("/history/search", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), func(c *fiber.Ctx) error {
		if s.historyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "history store not configured")
		}
		searcher, ok := s.historyStore.(interface {
			Search(context.Context, string, string, int) ([]session.SearchHit, error)
		})
		if !ok {
			return s.errMsg(c, fiber.StatusNotImplemented, "history store does not support search")
		}
		query := strings.TrimSpace(c.Query("q"))
		if query == "" {
			return s.errMsg(c, fiber.StatusBadRequest, "q is required")
		}
		limit := c.QueryInt("limit", 50)
		hits, err := searcher.Search(c.Context(), c.Query("agent_id"), query, limit)
		if err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		if hits == nil {
			hits = []session.SearchHit{}
		}
		return c.JSON(fiber.Map{"hits": hits, "query": query})
	})
	api.Get("/history/:session_id", s.rbacMW(rbac.ResourceMemory, rbac.ActionRead), s.requirePathSessionMW("session_id", ""), func(c *fiber.Ctx) error {
		if s.historyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "history store not configured")
		}
		limit := c.QueryInt("limit", 100)
		entries, err := s.historyStore.Load(c.Context(), c.Params("session_id"), limit)
		if err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		if entries == nil {
			entries = []session.ConversationEntry{}
		}
		return c.JSON(fiber.Map{"entries": entries, "session_id": c.Params("session_id")})
	})
	api.Get("/history/agent/:agent_id", s.rbacAgentFromMW(rbac.ResourceMemory, rbac.ActionRead, rbac.AgentIDSource{PathParam: "agent_id"}), func(c *fiber.Ctx) error {
		if s.historyStore == nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "history store not configured")
		}
		limit := c.QueryInt("limit", 200)
		entries, err := s.historyStore.LoadForAgent(c.Context(), c.Params("agent_id"), limit)
		if err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		if entries == nil {
			entries = []session.ConversationEntry{}
		}
		return c.JSON(fiber.Map{"entries": entries, "agent_id": c.Params("agent_id")})
	})

	// --- Observability Dashboard ---
	api.Get("/admin/dashboard", s.rbacMW(rbac.ResourceMetrics, rbac.ActionRead), s.handleDashboard)

	// --- Static GUI (embedded Svelte build) ---
	// Registered last so all API and WS routes take precedence.
	// SPA fallback: any path not matched above returns index.html.
	//
	// mountStaticGUI holds the cache policy and is separated from the embed so it
	// can be exercised over an in-memory FS. Testing it through the real gateway
	// meant testing nothing: GUIEnabled is false in the test config, so any such
	// test silently skips forever.
	if s.cfg.Server.GUIEnabled {
		sub, err := fs.Sub(webui.FS, "dist")
		if err == nil {
			mountStaticGUI(app, http.FS(sub))
		} else {
			s.log.Warn("could not mount embedded GUI", zap.Error(err))
		}
	}

	return app
}

func (s *Server) httpRequestTimeout() time.Duration {
	if s != nil && s.cfg != nil {
		if d, err := time.ParseDuration(s.cfg.Runtime.Timeouts.HTTP); err == nil && d > 0 {
			return d
		}
	}
	return 16 * time.Minute
}

// mountStaticGUI serves the built GUI with the cache policy an upgradeable SPA
// needs: hashed assets forever, index.html revalidated.
//
// The revalidation half needs BOTH parts. "Cache-Control: no-cache" permits the
// browser to store the response and only obliges it to revalidate — which it
// cannot do without a validator to send in If-None-Match. The embedded FS
// reports no ModTime, so index.html went out with neither an ETag nor a
// Last-Modified, and Chrome served its stored copy: users ran the previous GUI
// after every upgrade until they hard-refreshed, while the header claiming to
// prevent exactly that was present and correct.
//
// ETag is scoped to the static handler on purpose — hashing every API response
// body to serve 304s is not a trade this needs.
func mountStaticGUI(app *fiber.App, root http.FileSystem) {
	app.Use("/", func(c *fiber.Ctx) error {
		// Vite emits content-hashed asset filenames, so those are immutable; the
		// entry document names the current hashes and must never be stale.
		if strings.HasPrefix(c.Path(), "/assets/") {
			c.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			c.Set("Cache-Control", "no-cache")
		}
		return c.Next()
	})
	app.Use("/", etag.New())
	app.Use("/", filesystem.New(filesystem.Config{
		Root:         root,
		Index:        "index.html",
		NotFoundFile: "index.html",
		Browse:       false,
	}))
}

func (s *Server) requestBodyLimit() int {
	limit := int64(50 << 20)
	if s != nil && s.cfg != nil && s.cfg.Knowledge.MaxDocumentBytes > 0 {
		limit = s.cfg.Knowledge.MaxDocumentBytes
	}
	// Keep a little room for JSON/form overhead while the handler enforces the
	// exact document payload limit.
	const overhead = int64(1 << 20)
	if limit > int64(^uint(0)>>1)-overhead {
		return int(^uint(0) >> 1)
	}
	return int(limit + overhead)
}

// handleGetCosts handles GET /api/v1/costs
// Query params: ?since=<duration|date>, ?agent_id=<id> (optional filter)
func (s *Server) handleGetCosts(c *fiber.Ctx) error {
	if s.costStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "cost tracking not enabled",
		})
	}
	since, label, err := parseCostSince(c.Query("since", ""))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	rows, err := s.costStore.SumByAgent(c.Context(), since)
	if err != nil {
		s.log.Error("costs: SumByAgent failed", zap.Error(err))
		return s.errMsg(c, fiber.StatusInternalServerError, "internal error")
	}
	agentFilter := c.Query("agent_id", "")
	if agentFilter != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if r.AgentID == agentFilter {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	if rows == nil {
		rows = []costs.AgentCost{}
	}
	return c.JSON(fiber.Map{
		"by_agent":     rows,
		"period":       label,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// handleGetCostUsage exposes bounded, prompt-free per-call accounting for
// chargeback and provider reconciliation.
func (s *Server) handleGetCostUsage(c *fiber.Ctx) error {
	if s.costStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "cost tracking not enabled"})
	}
	since, label, err := parseCostSince(c.Query("since", "24h"))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	limit, err := strconv.Atoi(c.Query("limit", "200"))
	if err != nil || limit <= 0 || limit > 1000 {
		return s.errMsg(c, fiber.StatusBadRequest, "limit must be between 1 and 1000")
	}
	records, err := s.costStore.ListUsage(c.Context(), since, limit)
	if err != nil {
		s.log.Error("costs: ListUsage failed", zap.Error(err))
		return s.errMsg(c, fiber.StatusInternalServerError, "internal error")
	}
	if records == nil {
		records = []costs.UsageRecord{}
	}
	return c.JSON(fiber.Map{"usage": records, "period": label, "generated_at": time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) handleGetCostChargeback(c *fiber.Ctx) error {
	if s.costStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "cost tracking not enabled"})
	}
	since, label, err := parseCostSince(c.Query("since", "30d"))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	var dimensions []string
	if raw := strings.TrimSpace(c.Query("group_by")); raw != "" {
		dimensions = strings.Split(raw, ",")
	}
	rows, err := s.costStore.Chargeback(c.Context(), since, dimensions)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(fiber.Map{"chargeback": rows, "period": label, "group_by": dimensions,
		"generated_at": time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) handleGetCostReconciliations(c *fiber.Ctx) error {
	if s.costStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "cost tracking not enabled"})
	}
	limit, err := strconv.Atoi(c.Query("limit", "100"))
	if err != nil || limit <= 0 || limit > 1000 {
		return s.errMsg(c, fiber.StatusBadRequest, "limit must be between 1 and 1000")
	}
	items, err := s.costStore.ListReconciliations(c.Context(), limit)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "internal error")
	}
	return c.JSON(fiber.Map{"reconciliations": items})
}

func (s *Server) handlePostCostReconciliation(c *fiber.Ctx) error {
	if s.costStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "cost tracking not enabled"})
	}
	var req struct {
		Provider  string  `json:"provider"`
		Start     string  `json:"period_start"`
		End       string  `json:"period_end"`
		ActualUSD float64 `json:"actual_usd"`
		Source    string  `json:"source"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if strings.TrimSpace(req.Provider) == "" || req.ActualUSD < 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "provider and non-negative actual_usd are required")
	}
	start, err := parseReconciliationTime(req.Start)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, fmt.Errorf("period_start: %w", err))
	}
	end, err := parseReconciliationTime(req.End)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, fmt.Errorf("period_end: %w", err))
	}
	if !end.After(start) {
		return s.errMsg(c, fiber.StatusBadRequest, "period_end must be after period_start")
	}
	item, err := s.costStore.ReconcileProvider(c.Context(), strings.TrimSpace(req.Provider), start, end,
		int64(req.ActualUSD*1_000_000+0.5), strings.TrimSpace(req.Source))
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "internal error")
	}
	return c.JSON(item)
}

func parseReconciliationTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("must be RFC3339 or YYYY-MM-DD")
}

// handleGetAgentCosts handles GET /api/v1/costs/:agent_id
// Query params: ?since=<duration|date>
func (s *Server) handleGetAgentCosts(c *fiber.Ctx) error {
	if s.costStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "cost tracking not enabled",
		})
	}
	agentID := c.Params("agent_id")
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id is required")
	}
	since, _, err := parseCostSince(c.Query("since", ""))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	sessions, err := s.costStore.SumBySession(c.Context(), agentID, since)
	if err != nil {
		s.log.Error("costs: SumBySession failed", zap.String("agent_id", agentID), zap.Error(err))
		return s.errMsg(c, fiber.StatusInternalServerError, "internal error")
	}
	if sessions == nil {
		sessions = []costs.SessionCost{}
	}
	var totalTokens int
	var totalCost float64
	for _, sess := range sessions {
		totalTokens += sess.TotalTokens
		totalCost += sess.CostUSD
	}
	return c.JSON(fiber.Map{
		"agent_id":   agentID,
		"by_session": sessions,
		"total": fiber.Map{
			"total_tokens": totalTokens,
			"cost_usd":     totalCost,
		},
	})
}

// parseCostSince parses a ?since query parameter.
// Accepts empty string (all records), Go durations ("24h", "7d"), or date strings.
func parseCostSince(s string) (time.Time, string, error) {
	if s == "" {
		return time.Time{}, "", nil
	}
	// "Xd" day shorthand → X*24h
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days float64
		if _, err := fmt.Sscanf(s[:len(s)-1], "%f", &days); err == nil {
			return time.Now().Add(-time.Duration(days * 24 * float64(time.Hour))), s, nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			d = -d
		}
		return time.Now().Add(-d), s, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, s, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, s, nil
	}
	return time.Time{}, "", fmt.Errorf("invalid since param %q: use a duration (e.g. 24h) or date (e.g. 2026-01-01)", s)
}

// legacyAuthMiddleware is the Phase-2 static API-key check. It is used only
// when SetAuth() was not called (e.g. unit tests that construct Server directly).
// Production startup always calls SetAuth() with an auth.Engine.
func (s *Server) legacyAuthMiddleware() fiber.Handler {
	if s.cfg.Server.APIKey == "" {
		// Test/embedding compatibility only: production always wires auth.Engine,
		// whose middleware fails closed when no verifier is effective.
		s.log.Warn("legacy gateway constructed without auth engine; authentication is bypassed")
		return func(c *fiber.Ctx) error { return c.Next() }
	}
	return func(c *fiber.Ctx) error {
		got := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
		if got == "" {
			got = c.Query("api_key")
		}
		if !gwSecretEqual(got, s.cfg.Server.APIKey) {
			return s.errMsg(c, fiber.StatusUnauthorized, "invalid or missing API key")
		}
		// Directly-constructed gateways are retained for tests and embedding.
		// Their static key has the same authority as the production auth engine's
		// static key, so attach the same claims rather than creating an
		// authenticated-but-anonymous request that bypasses ownership semantics.
		auth.SetClaims(c, &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "api-key"},
			Role:             "admin", Kind: "access", PrincipalKind: "static_api_key", CredentialID: "static-api-key",
		})
		return c.Next()
	}
}

// gwSecretEqual is the gateway-local copy of the constant-time comparison used
// by the legacy auth path. The canonical implementation lives in internal/auth.
func gwSecretEqual(got, want string) bool {
	if want == "" {
		return false
	}
	gh := sha256.Sum256([]byte(got))
	wh := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gh[:], wh[:]) == 1
}

// isLoopbackHost reports whether the configured bind host only exposes the
// gateway to the local machine. An empty host means "all interfaces" in Go's
// net package, so it is treated as NON-loopback (unsafe) here.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// checkAuthBindSafety enforces SEC-4 using the auth engine's effective
// verifier state rather than its mere allocation.
func (s *Server) checkAuthBindSafety() error {
	cfg := s.cfg
	if cfg.Server.AllowUnauthenticated {
		s.log.Error("SECURITY WARNING: unauthenticated startup override is enabled",
			zap.String("host", cfg.Server.Host),
			zap.String("remediation", "remove --allow-unauthenticated and configure server.api_key or OIDC"))
		return nil
	}
	authDisabled := s.authEngine == nil || !s.authEngine.Effective()
	if !authDisabled {
		return nil
	}
	if isLoopbackHost(cfg.Server.Host) {
		return nil // local-only + no key: allowed (dev convenience)
	}
	return fmt.Errorf(
		"refusing to start: gateway bound to non-loopback host %q with no API key — "+
			"this exposes an unauthenticated gateway to the network. "+
			"Set server.api_key (generate one with `openssl rand -hex 32`), bind to 127.0.0.1, "+
			"or pass --allow-unauthenticated to override",
		cfg.Server.Host,
	)
}

// Listen starts the server. Blocks until ctx is cancelled or an error occurs.
func (s *Server) Listen(ctx context.Context) error {
	if err := s.checkAuthBindSafety(); err != nil {
		return err
	}

	// Story 2 / S1.x — probe providers and validate every agent's model BEFORE
	// serving. Agents whose configured model is unavailable are quarantined
	// (disabled in memory) with a clear log line, so a typo or an un-pulled
	// Ollama model surfaces at startup instead of as a 404 on the first user
	// message (or, worse, silently on every cron fire).
	s.validateAgentsAtBoot(ctx)

	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)

	// Start config watcher
	if s.cfgPath != "" {
		go s.watchConfig(ctx)
	}

	// Start release updates checker
	s.startUpdatesChecker()

	// Start TLS if configured
	if s.cfg.Server.TLSCert != "" && s.cfg.Server.TLSKey != "" {
		s.log.Info("gateway listening with TLS", zap.String("addr", addr))
		go func() {
			<-ctx.Done()
			_ = s.app.Shutdown()
		}()
		return s.app.ListenTLS(addr, s.cfg.Server.TLSCert, s.cfg.Server.TLSKey)
	}

	s.log.Info("gateway listening", zap.String("addr", addr))
	go func() {
		<-ctx.Done()
		_ = s.app.Shutdown()
	}()
	return s.app.Listen(addr)
}

// validateAgentsAtBoot probes each registered provider for its model list and
// validates every loaded agent against it. Agents with hard errors (model not
// available on the provider, provider not registered, blocked by
// allowed_providers) are disabled in memory and logged at ERROR so the operator
// sees the problem immediately. A provider that fails the probe is logged as a
// reachability warning but does not by itself disable agents (the model list is
// simply unavailable for validation). (Story 2 / S1.x)
func (s *Server) validateAgentsAtBoot(ctx context.Context) {
	if s.loader == nil || s.llmRouter == nil {
		return
	}
	opts := s.agentValidationOptions(ctx)
	opts.AuthoritativeModels = true

	// Reachability: a registered provider with no probed model list was
	// unreachable (bad base_url / down host / bad key). Surface it once.
	for _, id := range opts.RegisteredProviders {
		if len(opts.ProviderModels[id]) == 0 {
			s.log.Warn("provider unreachable at startup — model validation skipped for it; check base_url/api_key/host",
				zap.String("provider", id))
		}
	}

	var disabled, warned int
	for _, def := range s.loader.All() {
		if def == nil || !def.Enabled {
			continue
		}
		report := agentvalidate.Definition(def, def.SourcePath, opts, agentvalidate.Report{})
		if report.Errors > 0 {
			for _, f := range report.Findings {
				if f.Severity == agentvalidate.Error {
					s.log.Error("agent disabled at boot: invalid LLM configuration",
						zap.String("agent", def.ID),
						zap.String("field", f.Field),
						zap.String("problem", f.Message),
						zap.String("fix", f.Suggestion))
				}
			}
			if s.loader.SetEnabledInMemory(def.ID, false) {
				disabled++
			}
			continue
		}
		for _, f := range report.Findings {
			if f.Severity == agentvalidate.Warn {
				s.log.Warn("agent config warning",
					zap.String("agent", def.ID),
					zap.String("field", f.Field),
					zap.String("problem", f.Message))
				warned++
			}
		}
	}
	if disabled > 0 || warned > 0 {
		s.log.Info("boot agent validation complete",
			zap.Int("disabled", disabled), zap.Int("warnings", warned))
	}
}

// watchConfig uses fsnotify to monitor config.yaml for changes and reloads it.
func (s *Server) watchConfig(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.log.Warn("gateway: failed to start config watcher", zap.Error(err))
		return
	}
	defer watcher.Close()

	cfgDir := filepath.Dir(s.cfgPath)
	cfgFile := filepath.Base(s.cfgPath)

	if err := watcher.Add(cfgDir); err != nil {
		s.log.Warn("gateway: failed to watch config directory", zap.String("dir", cfgDir), zap.Error(err))
		return
	}

	// Debounce timer
	var timer *time.Timer
	delay := 100 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			s.log.Warn("gateway: config watcher error", zap.Error(err))
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) != cfgFile {
				continue
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(delay, func() {
					if err := s.ReloadConfig(); err != nil {
						s.log.Error("gateway: failed to hot-reload config", zap.Error(err))
					}
				})
			}
		}
	}
}
