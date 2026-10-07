// Package managedbrowser provides Soulacy's gateway-owned website action
// runtime. It deliberately exposes a small semantic surface instead of raw
// browser scripting so the model cannot read cookies, run JavaScript, or
// silently submit a consequential form.
package managedbrowser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/publicsuffix"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/netguard"
)

const (
	defaultSessionTTL  = 20 * time.Minute
	defaultMaxSessions = 8
)

var (
	ErrUnavailable     = errors.New("managed browser is unavailable")
	ErrCapacity        = errors.New("managed browser session capacity is full")
	ErrSessionNotFound = errors.New("managed browser session was not found or expired")
	ErrOutsideBoundary = errors.New("browser left the approved provider domain")
	ErrSensitiveField  = errors.New("credentials and payment details must be entered directly on the provider website")
	ErrCommitRequired  = errors.New("this looks like a final external action; use website_commit so Soulacy can show an approval")
	ErrProviderBlocked = errors.New("the provider requires a human security check or is blocking automation")
)

// Resolver is intentionally the narrow secret-bearing interface. The lease is
// passed straight to the browser backend and is never included in a Result.
type Resolver interface {
	Resolve(ctx context.Context, workspaceID, subject, agentID, connectionID string) (authconnections.Lease, error)
}

// Factory creates one isolated browser process per managed session.
type Factory interface {
	Available() (bool, string)
	Open(ctx context.Context, req OpenRequest) (Browser, error)
}

type Browser interface {
	Observe(ctx context.Context) (Observation, error)
	Act(ctx context.Context, action Action) (Observation, error)
	Close() error
}

type OpenRequest struct {
	URL            string
	AllowedDomains []string
	StorageState   []byte
}

type Action struct {
	Kind  string
	Ref   string
	Value string
}

type Element struct {
	Ref           string `json:"ref"`
	Role          string `json:"role,omitempty"`
	Label         string `json:"label,omitempty"`
	Type          string `json:"type,omitempty"`
	Placeholder   string `json:"placeholder,omitempty"`
	Sensitive     bool   `json:"sensitive,omitempty"`
	Consequential bool   `json:"consequential,omitempty"`
}

type Observation struct {
	URL      string    `json:"url"`
	Title    string    `json:"title,omitempty"`
	Text     string    `json:"text,omitempty"`
	Elements []Element `json:"elements"`
	Blocker  string    `json:"blocker,omitempty"`
}

type StartRequest struct {
	WorkspaceID  string
	Subject      string
	AgentID      string
	URL          string
	ConnectionID string
}

type Result struct {
	SessionID      string      `json:"session_id"`
	AllowedDomains []string    `json:"allowed_domains"`
	Observation    Observation `json:"observation"`
	Status         string      `json:"status"`
	Message        string      `json:"message,omitempty"`
	Fallback       string      `json:"fallback,omitempty"`
}

type CommitReview struct {
	Provider string `json:"provider"`
	Action   string `json:"action"`
	Item     string `json:"item"`
	Schedule string `json:"schedule,omitempty"`
	Terms    string `json:"terms,omitempty"`
	Total    string `json:"total"`
}

type session struct {
	opMu      sync.Mutex
	id        string
	agentID   string
	subject   string
	allowed   []string
	browser   Browser
	updatedAt time.Time
	busy      int
}

type Manager struct {
	mu          sync.Mutex
	resolver    Resolver
	factory     Factory
	sessions    map[string]*session
	ttl         time.Duration
	maxSessions int
	now         func() time.Time
}

func New(resolver Resolver, factory Factory) *Manager {
	if factory == nil {
		factory = NewChromiumFactory("")
	}
	return &Manager{resolver: resolver, factory: factory, sessions: map[string]*session{}, ttl: defaultSessionTTL, maxSessions: defaultMaxSessions, now: time.Now}
}

func (m *Manager) Available() (bool, string) {
	if m == nil || m.factory == nil {
		return false, "managed browser runtime is not configured"
	}
	return m.factory.Available()
}

func (m *Manager) Start(ctx context.Context, req StartRequest) (Result, error) {
	if ok, detail := m.Available(); !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnavailable, detail)
	}
	target, allowed, err := normalizeTarget(ctx, req.URL)
	if err != nil {
		return Result{}, err
	}
	var state []byte
	if id := strings.TrimSpace(req.ConnectionID); id != "" {
		if m.resolver == nil {
			return Result{}, fmt.Errorf("website connection %q cannot be opened: %w", id, ErrUnavailable)
		}
		lease, resolveErr := m.resolver.Resolve(ctx, req.WorkspaceID, req.Subject, req.AgentID, id)
		if resolveErr != nil {
			return Result{}, fmt.Errorf("website connection %q: %w", id, resolveErr)
		}
		if lease.Kind != authconnections.KindBrowser {
			return Result{}, fmt.Errorf("website connection %q is not a browser session", id)
		}
		if !hostAllowed(target.Hostname(), lease.AllowedDomains) {
			return Result{}, fmt.Errorf("target is outside website connection %q: %w", id, ErrOutsideBoundary)
		}
		allowed = append([]string(nil), lease.AllowedDomains...)
		state = lease.BrowserState
	}
	browser, err := m.factory.Open(ctx, OpenRequest{URL: target.String(), AllowedDomains: allowed, StorageState: state})
	if err != nil {
		if errors.Is(err, ErrOutsideBoundary) {
			return boundaryResult(target.String(), err)
		}
		return Result{}, fmt.Errorf("launch managed browser: %w", err)
	}
	obs, err := browser.Observe(ctx)
	if err != nil {
		_ = browser.Close()
		return Result{}, fmt.Errorf("inspect provider page: %w", err)
	}
	if err := validateObservation(ctx, obs, allowed); err != nil {
		_ = browser.Close()
		return boundaryResult(target.String(), err)
	}
	if strings.TrimSpace(obs.Blocker) != "" {
		_ = browser.Close()
		return providerBlockedResult(obs)
	}

	now := m.now().UTC()
	s := &session{id: "web_" + strings.ReplaceAll(uuid.NewString(), "-", ""), agentID: req.AgentID, subject: req.Subject, allowed: allowed, browser: browser, updatedAt: now}
	m.mu.Lock()
	m.evictLocked(now)
	for len(m.sessions) >= m.maxSessions {
		if !m.evictOldestLocked() {
			m.mu.Unlock()
			_ = browser.Close()
			return Result{}, ErrCapacity
		}
	}
	m.sessions[s.id] = s
	m.mu.Unlock()
	return resultFor(s, obs, "ready", "Provider page opened in an isolated, domain-bound browser session."), nil
}

func (m *Manager) Observe(ctx context.Context, sessionID, agentID, subject string) (Result, error) {
	s, release, err := m.acquire(sessionID, agentID, subject)
	if err != nil {
		return Result{}, err
	}
	defer release()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	obs, err := s.browser.Observe(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := validateObservation(ctx, obs, s.allowed); err != nil {
		m.drop(sessionID)
		return boundaryResult(obs.URL, err)
	}
	if strings.TrimSpace(obs.Blocker) != "" {
		m.drop(sessionID)
		return providerBlockedResult(obs)
	}
	return resultFor(s, obs, "ready", "Current provider page inspected."), nil
}

func (m *Manager) Act(ctx context.Context, sessionID, agentID, subject string, action Action) (Result, error) {
	s, release, err := m.acquire(sessionID, agentID, subject)
	if err != nil {
		return Result{}, err
	}
	defer release()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	action.Kind = strings.ToLower(strings.TrimSpace(action.Kind))
	action.Ref = strings.TrimSpace(action.Ref)
	if action.Ref == "" {
		return Result{}, errors.New("element ref is required")
	}
	if action.Kind != "click" && action.Kind != "fill" && action.Kind != "select" && action.Kind != "press" {
		return Result{}, fmt.Errorf("unsupported website action %q", action.Kind)
	}
	if action.Kind == "press" && strings.EqualFold(strings.TrimSpace(action.Value), "enter") {
		return Result{}, ErrCommitRequired
	}
	if action.Kind == "click" || action.Kind == "press" {
		obs, observeErr := s.browser.Observe(ctx)
		if observeErr != nil {
			return Result{}, observeErr
		}
		if err := validateObservation(ctx, obs, s.allowed); err != nil {
			m.drop(sessionID)
			return boundaryResult(obs.URL, err)
		}
		if strings.TrimSpace(obs.Blocker) != "" {
			m.drop(sessionID)
			return providerBlockedResult(obs)
		}
		for _, element := range obs.Elements {
			if element.Ref == action.Ref && (element.Consequential || consequential(element.Label+" "+element.Role+" "+element.Type)) {
				return Result{}, ErrCommitRequired
			}
		}
	}
	obs, err := s.browser.Act(ctx, action)
	if err != nil {
		if errors.Is(err, ErrOutsideBoundary) {
			m.drop(sessionID)
			return boundaryResult(obs.URL, err)
		}
		return Result{}, err
	}
	if err := validateObservation(ctx, obs, s.allowed); err != nil {
		m.drop(sessionID)
		return boundaryResult(obs.URL, err)
	}
	if strings.TrimSpace(obs.Blocker) != "" {
		m.drop(sessionID)
		return providerBlockedResult(obs)
	}
	return resultFor(s, obs, "ready", "Provider page updated. Continue until the exact final action is visible."), nil
}

func (m *Manager) Commit(ctx context.Context, sessionID, agentID, subject, ref string, review CommitReview) (Result, error) {
	if err := validateReview(review); err != nil {
		return Result{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Result{}, errors.New("element ref is required")
	}
	s, release, err := m.acquire(sessionID, agentID, subject)
	if err != nil {
		return Result{}, err
	}
	defer release()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	current, err := s.browser.Observe(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := validateObservation(ctx, current, s.allowed); err != nil {
		m.drop(sessionID)
		return boundaryResult(current.URL, err)
	}
	if strings.TrimSpace(current.Blocker) != "" {
		m.drop(sessionID)
		return providerBlockedResult(current)
	}
	consequentialRef := false
	for _, element := range current.Elements {
		if element.Ref == ref {
			consequentialRef = element.Consequential || consequential(element.Label+" "+element.Role+" "+element.Type)
			break
		}
	}
	if !consequentialRef {
		return Result{}, errors.New("final approval can only submit the consequential control currently shown by the provider")
	}
	obs, err := s.browser.Act(ctx, Action{Kind: "click", Ref: ref})
	if err != nil {
		if errors.Is(err, ErrOutsideBoundary) {
			m.drop(sessionID)
			return boundaryResult(obs.URL, err)
		}
		return Result{}, err
	}
	if err := validateObservation(ctx, obs, s.allowed); err != nil {
		m.drop(sessionID)
		return boundaryResult(obs.URL, err)
	}
	if strings.TrimSpace(obs.Blocker) != "" {
		m.drop(sessionID)
		return providerBlockedResult(obs)
	}
	return resultFor(s, obs, "submitted", "The approved action was submitted. Verify the provider confirmation shown in this observation before reporting success."), nil
}

func (m *Manager) CloseSession(sessionID, agentID, subject string) error {
	s, release, err := m.acquire(sessionID, agentID, subject)
	if err != nil {
		return err
	}
	defer release()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	m.mu.Lock()
	delete(m.sessions, s.id)
	m.mu.Unlock()
	return s.browser.Close()
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*session{}
	m.mu.Unlock()
	var first error
	for _, s := range sessions {
		s.opMu.Lock()
		err := s.browser.Close()
		s.opMu.Unlock()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) acquire(id, agentID, subject string) (*session, func(), error) {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictLocked(now)
	s := m.sessions[strings.TrimSpace(id)]
	if s == nil || s.agentID != agentID || s.subject != subject {
		return nil, nil, ErrSessionNotFound
	}
	s.updatedAt = now
	s.busy++
	return s, func() {
		m.mu.Lock()
		if s.busy > 0 {
			s.busy--
		}
		m.mu.Unlock()
	}, nil
}

func (m *Manager) drop(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s != nil {
		_ = s.browser.Close()
	}
}

func (m *Manager) evictLocked(now time.Time) {
	for id, s := range m.sessions {
		if s.busy == 0 && now.Sub(s.updatedAt) > m.ttl {
			delete(m.sessions, id)
			go s.browser.Close()
		}
	}
}

func (m *Manager) evictOldestLocked() bool {
	var oldest *session
	for _, s := range m.sessions {
		if s.busy > 0 {
			continue
		}
		if oldest == nil || s.updatedAt.Before(oldest.updatedAt) {
			oldest = s
		}
	}
	if oldest != nil {
		delete(m.sessions, oldest.id)
		go oldest.browser.Close()
		return true
	}
	return false
}

func normalizeTarget(ctx context.Context, raw string) (*url.URL, []string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, nil, errors.New("website URL must be public HTTPS without embedded credentials")
	}
	if err := netguard.CheckPublicContext(ctx, parsed.String()); err != nil {
		return nil, nil, err
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	boundary, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		boundary = host
	}
	return parsed, []string{boundary}, nil
}

func validateObservation(ctx context.Context, obs Observation, allowed []string) error {
	parsed, err := url.Parse(strings.TrimSpace(obs.URL))
	if err != nil || parsed.Scheme != "https" || !hostAllowed(parsed.Hostname(), allowed) {
		return fmt.Errorf("%w: %s", ErrOutsideBoundary, obs.URL)
	}
	if err := netguard.CheckPublicContext(ctx, parsed.String()); err != nil {
		return fmt.Errorf("%w: %v", ErrOutsideBoundary, err)
	}
	return nil
}

func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "."))
	for _, domain := range allowed {
		domain = strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func cookieDomainAllowed(cookieDomain string, allowed []string) bool {
	cookieDomain = strings.ToLower(strings.Trim(strings.TrimSpace(cookieDomain), "."))
	if cookieDomain == "" {
		return false
	}
	for _, domain := range allowed {
		domain = strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
		if domain == cookieDomain || strings.HasSuffix(domain, "."+cookieDomain) {
			return true
		}
	}
	return false
}

func validateReview(review CommitReview) error {
	if strings.TrimSpace(review.Provider) == "" || strings.TrimSpace(review.Action) == "" || strings.TrimSpace(review.Item) == "" || strings.TrimSpace(review.Total) == "" {
		return errors.New("final approval requires provider, action, item, and total")
	}
	for _, value := range []string{review.Provider, review.Action, review.Item, review.Schedule, review.Terms, review.Total} {
		if len(value) > 500 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("approval details must be single-line values no longer than 500 characters")
		}
	}
	total := strings.ToLower(strings.TrimSpace(review.Total))
	if total == "unknown" || total == "tbd" || total == "n/a" || total == "unspecified" || strings.Contains(total, "not available") {
		return errors.New("final approval requires an exact total or $0 when there is no charge")
	}
	return nil
}

func consequential(label string) bool {
	label = strings.ToLower(strings.Join(strings.Fields(label), " "))
	for _, phrase := range []string{"confirm", "book", "reserve", "order", "buy", "purchase", "checkout", "submit", "pay", "request ride", "schedule ride", "cancel", "send", "finalize", "complete", "finish", "accept", "agree", "authorize", "transfer", "delete", "remove", "subscribe", "unsubscribe", "enroll", "donate", "bid", "apply", "publish", "post"} {
		if strings.Contains(label, phrase) {
			return true
		}
	}
	return false
}

func resultFor(s *session, obs Observation, status, message string) Result {
	return Result{SessionID: s.id, AllowedDomains: append([]string(nil), s.allowed...), Observation: obs, Status: status, Message: message}
}

func boundaryResult(attempted string, err error) (Result, error) {
	return Result{Status: "blocked", Message: err.Error(), Fallback: "The provider left its approved domain. Try the provider's official app, phone number, or another official provider route.", Observation: Observation{URL: attempted}}, err
}

func providerBlockedResult(obs Observation) (Result, error) {
	message := strings.TrimSpace(obs.Blocker)
	if message == "" {
		message = ErrProviderBlocked.Error()
	}
	return Result{Status: "blocked", Message: message, Fallback: "Complete the provider's security check in Website Access, use the provider's official app, or choose another official provider route.", Observation: obs}, fmt.Errorf("%w: %s", ErrProviderBlocked, message)
}

func (m *Manager) Status() map[string]any {
	ok, detail := m.Available()
	m.mu.Lock()
	active := len(m.sessions)
	m.mu.Unlock()
	return map[string]any{"available": ok, "detail": detail, "active_sessions": active, "session_ttl_minutes": int(m.ttl.Minutes()), "max_sessions": m.maxSessions}
}
