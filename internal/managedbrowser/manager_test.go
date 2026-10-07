package managedbrowser

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/authconnections"
)

func TestManagedChromiumSmoke(t *testing.T) {
	if os.Getenv("SOULACY_MANAGED_BROWSER_SMOKE") != "1" {
		t.Skip("set SOULACY_MANAGED_BROWSER_SMOKE=1 to launch a real browser")
	}
	manager := New(nil, nil)
	result, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if result.SessionID == "" || result.Observation.URL != "https://example.com/" || result.Observation.Title != "Example Domain" {
		t.Fatalf("unexpected browser result: %+v", result)
	}
	if len(result.Observation.Elements) == 0 {
		t.Fatal("real browser did not expose the visible link")
	}
	blocked, err := manager.Act(t.Context(), result.SessionID, "genie", "admin", Action{Kind: "click", Ref: result.Observation.Elements[0].Ref})
	if !errors.Is(err, ErrOutsideBoundary) || blocked.Status != "blocked" || blocked.Fallback == "" {
		t.Fatalf("cross-domain navigation was not stopped with a fallback: result=%+v err=%v", blocked, err)
	}
}

type fakeResolver struct {
	lease authconnections.Lease
	err   error
}

func (r fakeResolver) Resolve(context.Context, string, string, string, string) (authconnections.Lease, error) {
	return r.lease, r.err
}

type fakeFactory struct {
	browser *fakeBrowser
	opened  OpenRequest
}

func (f *fakeFactory) Available() (bool, string) { return true, "test browser" }
func (f *fakeFactory) Open(_ context.Context, req OpenRequest) (Browser, error) {
	f.opened = req
	return f.browser, nil
}

type fakeBrowser struct {
	observations []Observation
	actions      []Action
	closed       bool
}

func (b *fakeBrowser) Observe(context.Context) (Observation, error) {
	if len(b.observations) == 0 {
		return Observation{}, errors.New("no observation")
	}
	return b.observations[0], nil
}

func (b *fakeBrowser) Act(_ context.Context, action Action) (Observation, error) {
	b.actions = append(b.actions, action)
	if len(b.observations) > 1 {
		b.observations = b.observations[1:]
	}
	return b.observations[0], nil
}

func (b *fakeBrowser) Close() error { b.closed = true; return nil }

func TestSavedSessionStaysInsideBrowserAndResult(t *testing.T) {
	const secret = "cookie-value-must-never-reach-model"
	browser := &fakeBrowser{observations: []Observation{{URL: "https://m.uber.com/go", Title: "Uber", Text: "Choose a ride", Elements: []Element{}}}}
	factory := &fakeFactory{browser: browser}
	manager := New(fakeResolver{lease: authconnections.Lease{
		ConnectionID: "conn", Kind: authconnections.KindBrowser, AllowedDomains: []string{"uber.com"}, BrowserState: []byte(secret),
	}}, factory)

	result, err := manager.Start(t.Context(), StartRequest{WorkspaceID: "personal", Subject: "admin", AgentID: "genie", URL: "https://m.uber.com/go", ConnectionID: "conn"})
	if err != nil {
		t.Fatal(err)
	}
	if string(factory.opened.StorageState) != secret {
		t.Fatal("browser did not receive encrypted-session lease")
	}
	if strings.Contains(result.Message+result.Observation.Text+result.Observation.Title, secret) {
		t.Fatal("browser session secret leaked in model-facing result")
	}
	if len(result.AllowedDomains) != 1 || result.AllowedDomains[0] != "uber.com" {
		t.Fatalf("allowed domains = %v", result.AllowedDomains)
	}
}

func TestParentDomainCookieDoesNotExpandNavigationBoundary(t *testing.T) {
	allowed := []string{"notebook.google.com"}
	if !cookieDomainAllowed(".google.com", allowed) {
		t.Fatal("parent-domain cookie that applies to approved host was rejected")
	}
	if cookieDomainAllowed("evil.test", allowed) {
		t.Fatal("foreign cookie domain was accepted")
	}
	if hostAllowed("accounts.google.com", allowed) {
		t.Fatal("parent-domain cookie expanded the browser navigation boundary")
	}
}

func TestSessionClosesWhenProviderLeavesDomain(t *testing.T) {
	browser := &fakeBrowser{observations: []Observation{
		{URL: "https://www.opentable.com/search", Elements: []Element{{Ref: "s1", Label: "Next"}}},
		{URL: "https://evil.example/collect", Elements: []Element{}},
	}}
	manager := New(nil, &fakeFactory{browser: browser})
	started, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://www.opentable.com/search"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Act(t.Context(), started.SessionID, "genie", "admin", Action{Kind: "click", Ref: "s1"})
	if !errors.Is(err, ErrOutsideBoundary) {
		t.Fatalf("error = %v, want outside boundary", err)
	}
	if result.Status != "blocked" || result.Fallback == "" || !browser.closed {
		t.Fatalf("boundary result = %+v, closed=%v", result, browser.closed)
	}
}

func TestOrdinaryClickCannotBypassFinalApproval(t *testing.T) {
	browser := &fakeBrowser{observations: []Observation{{URL: "https://m.uber.com/go", Elements: []Element{{Ref: "s7", Role: "button", Label: "Request ride"}}}}}
	manager := New(nil, &fakeFactory{browser: browser})
	started, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://m.uber.com/go"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Act(t.Context(), started.SessionID, "genie", "admin", Action{Kind: "click", Ref: "s7"})
	if !errors.Is(err, ErrCommitRequired) {
		t.Fatalf("error = %v, want commit required", err)
	}
	if len(browser.actions) != 0 {
		t.Fatal("consequential click reached browser before approval")
	}
}

func TestCommitRequiresReviewAndReturnsProviderEvidence(t *testing.T) {
	browser := &fakeBrowser{observations: []Observation{
		{URL: "https://www.opentable.com/booking", Elements: []Element{{Ref: "s3", Label: "Confirm reservation"}}},
		{URL: "https://www.opentable.com/confirmation/123", Title: "Reservation confirmed", Text: "Confirmation 123", Elements: []Element{}},
	}}
	manager := New(nil, &fakeFactory{browser: browser})
	started, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://www.opentable.com/booking"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Commit(t.Context(), started.SessionID, "genie", "admin", "s3", CommitReview{}); err == nil {
		t.Fatal("commit without exact review was accepted")
	}
	result, err := manager.Commit(t.Context(), started.SessionID, "genie", "admin", "s3", CommitReview{
		Provider: "OpenTable", Action: "Reserve table", Item: "Table for 2", Schedule: "7:00 PM", Terms: "Standard cancellation", Total: "$0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "submitted" || !strings.Contains(result.Observation.Text, "Confirmation 123") {
		t.Fatalf("commit result = %+v", result)
	}
}

func TestCommitRefusesOrdinaryControl(t *testing.T) {
	browser := &fakeBrowser{observations: []Observation{{URL: "https://www.opentable.com/search", Elements: []Element{{Ref: "s1", Label: "Search"}}}}}
	manager := New(nil, &fakeFactory{browser: browser})
	started, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://www.opentable.com/search"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Commit(t.Context(), started.SessionID, "genie", "admin", "s1", CommitReview{Provider: "OpenTable", Action: "Search", Item: "Restaurants", Total: "$0"})
	if err == nil || !strings.Contains(err.Error(), "consequential control") {
		t.Fatalf("error = %v, want consequential control refusal", err)
	}
	if len(browser.actions) != 0 {
		t.Fatal("ordinary control reached browser through commit")
	}
}

func TestEnterCannotBypassFinalApproval(t *testing.T) {
	browser := &fakeBrowser{observations: []Observation{{URL: "https://m.uber.com/go", Elements: []Element{{Ref: "s2", Role: "input", Label: "Destination"}}}}}
	manager := New(nil, &fakeFactory{browser: browser})
	started, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://m.uber.com/go"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Act(t.Context(), started.SessionID, "genie", "admin", Action{Kind: "press", Ref: "s2", Value: "Enter"})
	if !errors.Is(err, ErrCommitRequired) {
		t.Fatalf("error = %v, want commit required", err)
	}
}

func TestStartDoesNotEvictBusySession(t *testing.T) {
	existingBrowser := &fakeBrowser{observations: []Observation{{URL: "https://example.com/", Elements: []Element{}}}}
	manager := New(nil, &fakeFactory{browser: existingBrowser})
	manager.maxSessions = 1
	started, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.sessions[started.SessionID].busy = 1
	manager.mu.Unlock()
	newBrowser := &fakeBrowser{observations: []Observation{{URL: "https://example.org/", Elements: []Element{}}}}
	manager.factory = &fakeFactory{browser: newBrowser}
	_, err = manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://example.org/"})
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("error = %v, want capacity", err)
	}
	if existingBrowser.closed {
		t.Fatal("busy session was closed")
	}
	if !newBrowser.closed {
		t.Fatal("new browser was not closed after capacity refusal")
	}
}

func TestNavigationBoundaryOnlyAllowsApprovedHTTPSDomain(t *testing.T) {
	b := &chromiumBrowser{allowed: []string{"uber.com"}}
	for _, raw := range []string{"https://uber.com/", "https://m.uber.com/go", "about:blank"} {
		if !b.navigationAllowed(raw) {
			t.Fatalf("expected %q to be allowed", raw)
		}
	}
	for _, raw := range []string{"http://uber.com/", "https://uber.com.evil.example/", "https://example.com/", "file:///etc/passwd"} {
		if b.navigationAllowed(raw) {
			t.Fatalf("expected %q to be blocked", raw)
		}
	}
}

func TestProviderSecurityBlockClosesSession(t *testing.T) {
	browser := &fakeBrowser{observations: []Observation{{URL: "https://m.uber.com/go", Blocker: "Verify you are human", Elements: []Element{}}}}
	manager := New(nil, &fakeFactory{browser: browser})
	result, err := manager.Start(t.Context(), StartRequest{Subject: "admin", AgentID: "genie", URL: "https://m.uber.com/go"})
	if !errors.Is(err, ErrProviderBlocked) {
		t.Fatalf("error = %v, want provider blocked", err)
	}
	if result.Status != "blocked" || result.Fallback == "" || !browser.closed {
		t.Fatalf("result = %+v, closed=%v", result, browser.closed)
	}
}

func TestCommitRejectsUnknownTotal(t *testing.T) {
	err := validateReview(CommitReview{Provider: "Provider", Action: "Buy", Item: "Item", Total: "unknown"})
	if err == nil || !strings.Contains(err.Error(), "exact total") {
		t.Fatalf("error = %v, want exact total", err)
	}
}

func TestConsequentialCoverageIncludesIrreversibleActions(t *testing.T) {
	for _, label := range []string{"Complete purchase", "Authorize transfer", "Delete account", "Subscribe now", "Submit application"} {
		if !consequential(label) {
			t.Fatalf("%q was not classified as consequential", label)
		}
	}
}
