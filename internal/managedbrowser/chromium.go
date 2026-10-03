package managedbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/soulacy/soulacy/internal/netguard"
)

type ChromiumFactory struct{ executable string }

func NewChromiumFactory(executable string) *ChromiumFactory {
	return &ChromiumFactory{executable: strings.TrimSpace(executable)}
}

func (f *ChromiumFactory) Available() (bool, string) {
	path, err := f.executablePath()
	if err != nil {
		return false, err.Error()
	}
	return true, "gateway-managed Chromium at " + path
}

func (f *ChromiumFactory) Open(ctx context.Context, req OpenRequest) (Browser, error) {
	path, err := f.executablePath()
	if err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp("", "soulacy-browser-*")
	if err != nil {
		return nil, err
	}
	proxy, err := startBrowserProxy()
	if err != nil {
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("start guarded browser proxy: %w", err)
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(path),
		chromedp.UserDataDir(profile),
		chromedp.ProxyServer(proxy.URL()),
		chromedp.Headless,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-features", "Translate,MediaRouter,OptimizationHints"),
		chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.Flag("disable-notifications", true),
		chromedp.Flag("disable-geolocation", true),
		chromedp.Flag("disable-quic", true),
		chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"),
		chromedp.Flag("webrtc-ip-handling-policy", "disable_non_proxied_udp"),
	)
	if runtime.GOOS == "linux" {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	b := &chromiumBrowser{ctx: tabCtx, cancel: func() { tabCancel(); allocCancel() }, profile: profile, proxy: proxy, allowed: append([]string(nil), req.AllowedDomains...)}

	var startupMu sync.Mutex
	startupFinished := false
	startupDone := make(chan struct{})
	go func() {
		timer := time.NewTimer(45 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-startupDone:
			return
		}
		startupMu.Lock()
		defer startupMu.Unlock()
		if !startupFinished {
			_ = b.Close()
		}
	}()
	defer func() {
		startupMu.Lock()
		startupFinished = true
		close(startupDone)
		startupMu.Unlock()
	}()
	if err := chromedp.Run(tabCtx,
		chromedp.ActionFunc(func(c context.Context) error {
			if err := cdpbrowser.SetDownloadBehavior(cdpbrowser.SetDownloadBehaviorBehaviorDeny).Do(c); err != nil {
				return err
			}
			_, err := page.AddScriptToEvaluateOnNewDocument(`
Object.defineProperty(window, 'open', { configurable: true, value: (url) => { if (url) location.assign(url); return null; } });
document.addEventListener('DOMContentLoaded', () => document.querySelectorAll('a[target]').forEach(a => a.removeAttribute('target')), { once: true });
`).Do(c)
			return err
		}),
	); err != nil {
		_ = b.Close()
		return nil, err
	}
	if err := b.guardTopLevelNavigation(tabCtx); err != nil {
		_ = b.Close()
		return nil, err
	}
	if err := b.seed(tabCtx, req.StorageState, req.AllowedDomains); err != nil {
		_ = b.Close()
		return nil, err
	}
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate(req.URL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('a[target]').forEach(a => a.removeAttribute('target'))`, nil),
	); err != nil {
		_ = b.Close()
		if blocked := b.takeBlockedURL(); blocked != "" {
			return nil, fmt.Errorf("%w: %s", ErrOutsideBoundary, blocked)
		}
		return nil, err
	}
	return b, nil
}

func (f *ChromiumFactory) executablePath() (string, error) {
	if f != nil && strings.TrimSpace(f.executable) != "" {
		if info, err := os.Stat(f.executable); err == nil && !info.IsDir() {
			return f.executable, nil
		}
		return "", fmt.Errorf("configured browser executable does not exist: %s", f.executable)
	}
	if env := strings.TrimSpace(os.Getenv("SOULACY_BROWSER_EXECUTABLE")); env != "" {
		if info, err := os.Stat(env); err == nil && !info.IsDir() {
			return env, nil
		}
		return "", fmt.Errorf("SOULACY_BROWSER_EXECUTABLE does not exist: %s", env)
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	for _, path := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("chromium or Google Chrome was not found; set SOULACY_BROWSER_EXECUTABLE")
}

type chromiumBrowser struct {
	ctx     context.Context
	cancel  context.CancelFunc
	profile string
	proxy   *browserProxy
	once    sync.Once
	allowed []string
	blockMu sync.Mutex
	blocked string
}

type storageState struct {
	Cookies []struct {
		Name, Value, Domain, Path string
		Expires                   float64 `json:"expires"`
		HTTPOnly                  bool    `json:"httpOnly"`
		Secure                    bool    `json:"secure"`
		SameSite                  string  `json:"sameSite"`
	} `json:"cookies"`
	Origins []struct {
		Origin       string `json:"origin"`
		LocalStorage []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"localStorage"`
	} `json:"origins"`
}

func (b *chromiumBrowser) seed(ctx context.Context, raw []byte, allowed []string) error {
	if len(raw) == 0 {
		return nil
	}
	var state storageState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("decode saved website session: %w", err)
	}
	cookies := make([]*network.CookieParam, 0, len(state.Cookies))
	for _, cookie := range state.Cookies {
		if !hostAllowed(cookie.Domain, allowed) {
			return ErrOutsideBoundary
		}
		path := cookie.Path
		if path == "" {
			path = "/"
		}
		cookies = append(cookies, &network.CookieParam{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: path, Secure: cookie.Secure, HTTPOnly: cookie.HTTPOnly})
	}
	if len(cookies) > 0 {
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error { return network.SetCookies(cookies).Do(c) })); err != nil {
			return fmt.Errorf("restore saved cookies: %w", err)
		}
	}
	for _, origin := range state.Origins {
		if len(origin.LocalStorage) == 0 {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(origin.Origin))
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || !hostAllowed(parsed.Hostname(), allowed) {
			return ErrOutsideBoundary
		}
		if err := netguard.CheckPublicContext(ctx, parsed.String()); err != nil {
			return fmt.Errorf("saved website origin is not public: %w", err)
		}
		entries, _ := json.Marshal(origin.LocalStorage)
		script := fmt.Sprintf(`for (const item of %s) localStorage.setItem(item.name, item.value)`, entries)
		if err := chromedp.Run(ctx, chromedp.Navigate(origin.Origin), chromedp.WaitReady("body", chromedp.ByQuery), chromedp.Evaluate(script, nil)); err != nil {
			return fmt.Errorf("restore saved local storage: %w", err)
		}
	}
	return nil
}

func (b *chromiumBrowser) Observe(ctx context.Context) (Observation, error) {
	var obs Observation
	runCtx, cancel := b.operationContext(ctx)
	defer cancel()
	if err := chromedp.Run(runCtx,
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Evaluate(observeScript, &obs),
	); err != nil {
		return Observation{}, err
	}
	if obs.Elements == nil {
		obs.Elements = []Element{}
	}
	return obs, nil
}

func (b *chromiumBrowser) Act(ctx context.Context, action Action) (Observation, error) {
	if !validRef.MatchString(action.Ref) {
		return Observation{}, errors.New("invalid browser element ref")
	}
	runCtx, cancel := b.operationContext(ctx)
	defer cancel()
	selector := `[data-soulacy-ref="` + action.Ref + `"]`
	var meta Element
	metaScript := fmt.Sprintf(`(() => { const e=document.querySelector(%q); if(!e) return null; const label=e.getAttribute('aria-label')||e.innerText||e.value||e.placeholder||''; const formText=(e.closest('form')?.innerText||'').slice(0,1000); return {ref:%q,role:e.getAttribute('role')||e.tagName.toLowerCase(),label,type:e.type||'',placeholder:e.placeholder||'',sensitive:/password|passcode|otp|one-time|cc-|card|cvv|cvc|security.code|secret|token|routing|iban|swift|bank.account|account.number|tax.id|social.security|\bssn\b|\bpin\b/i.test([e.type,e.name,e.id,e.autocomplete,e.getAttribute('aria-label'),e.placeholder].join(' ')),consequential:/confirm|book|reserve|order|buy|purchase|checkout|submit|pay|request.ride|schedule.ride|cancel|send|finalize|complete|finish|accept|agree|authorize|transfer|delete|remove|subscribe|unsubscribe|enroll|donate|bid|apply|publish|post/i.test(label+' '+((e.type==='submit'||e.tagName==='BUTTON')?formText:''))}; })()`, selector, action.Ref)
	if err := chromedp.Run(runCtx, chromedp.Evaluate(metaScript, &meta)); err != nil {
		return Observation{}, err
	}
	if meta.Ref == "" {
		return Observation{}, errors.New("browser element ref is stale; inspect the page again")
	}
	if meta.Sensitive && (action.Kind == "fill" || action.Kind == "select") {
		return Observation{}, ErrSensitiveField
	}
	var task chromedp.Action
	switch action.Kind {
	case "click":
		task = chromedp.Click(selector, chromedp.ByQuery)
	case "fill":
		task = chromedp.Tasks{chromedp.Focus(selector, chromedp.ByQuery), chromedp.SetValue(selector, "", chromedp.ByQuery), chromedp.SendKeys(selector, action.Value, chromedp.ByQuery)}
	case "select":
		value, _ := json.Marshal(action.Value)
		script := fmt.Sprintf(`(() => { const e=document.querySelector(%q); const wanted=%s; const option=[...e.options].find(o => o.value===wanted || o.textContent.trim()===wanted); if(!option) throw new Error('option not found'); e.value=option.value; e.dispatchEvent(new Event('input',{bubbles:true})); e.dispatchEvent(new Event('change',{bubbles:true})); })()`, selector, value)
		task = chromedp.Evaluate(script, nil)
	case "press":
		if !allowedKey(action.Value) {
			return Observation{}, errors.New("press supports Enter, Escape, Tab, and arrow keys")
		}
		task = chromedp.KeyEvent(action.Value)
	default:
		return Observation{}, fmt.Errorf("unsupported website action %q", action.Kind)
	}
	if err := chromedp.Run(runCtx, task, chromedp.Sleep(900*time.Millisecond)); err != nil {
		if blocked := b.takeBlockedURL(); blocked != "" {
			return Observation{URL: blocked}, fmt.Errorf("%w: %s", ErrOutsideBoundary, blocked)
		}
		return Observation{}, err
	}
	if blocked := b.takeBlockedURL(); blocked != "" {
		return Observation{URL: blocked}, fmt.Errorf("%w: %s", ErrOutsideBoundary, blocked)
	}
	return b.Observe(runCtx)
}

func (b *chromiumBrowser) guardTopLevelNavigation(ctx context.Context) error {
	var root cdp.FrameID
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		tree, err := page.GetFrameTree().Do(c)
		if err != nil {
			return err
		}
		root = tree.Frame.ID
		return fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", ResourceType: network.ResourceTypeDocument, RequestStage: fetch.RequestStageRequest}}).Do(c)
	})); err != nil {
		return fmt.Errorf("enable domain boundary: %w", err)
	}
	chromedp.ListenTarget(ctx, func(event any) {
		paused, ok := event.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		go b.handlePausedRequest(ctx, root, paused)
	})
	return nil
}

func (b *chromiumBrowser) handlePausedRequest(ctx context.Context, root cdp.FrameID, paused *fetch.EventRequestPaused) {
	execCtx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Target)
	if paused.FrameID == root && !b.navigationAllowed(paused.Request.URL) {
		b.blockMu.Lock()
		b.blocked = paused.Request.URL
		b.blockMu.Unlock()
		_ = fetch.FailRequest(paused.RequestID, network.ErrorReasonBlockedByClient).Do(execCtx)
		return
	}
	_ = fetch.ContinueRequest(paused.RequestID).Do(execCtx)
}

func (b *chromiumBrowser) navigationAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if parsed.Scheme == "about" || parsed.Scheme == "data" {
		return true
	}
	return parsed.Scheme == "https" && parsed.User == nil && hostAllowed(parsed.Hostname(), b.allowed)
}

func (b *chromiumBrowser) takeBlockedURL() string {
	b.blockMu.Lock()
	defer b.blockMu.Unlock()
	blocked := b.blocked
	b.blocked = ""
	return blocked
}

func (b *chromiumBrowser) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := 30 * time.Second
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < timeout {
			timeout = remaining
		}
	}
	ctx, cancel := context.WithTimeout(b.ctx, timeout)
	go func() {
		select {
		case <-parent.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (b *chromiumBrowser) Close() error {
	b.once.Do(func() {
		b.cancel()
		if b.proxy != nil {
			_ = b.proxy.Close()
		}
		_ = os.RemoveAll(b.profile)
	})
	return nil
}

var validRef = regexp.MustCompile(`^s[0-9]{1,3}$`)

func allowedKey(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enter", "escape", "tab", "arrowup", "arrowdown", "arrowleft", "arrowright":
		return true
	}
	return false
}

var observeScript = `(() => {
  document.querySelectorAll('[data-soulacy-ref]').forEach(e => e.removeAttribute('data-soulacy-ref'));
  document.querySelectorAll('a[target]').forEach(a => a.removeAttribute('target'));
  const visible = e => { const s=getComputedStyle(e), r=e.getBoundingClientRect(); return s.visibility!=='hidden' && s.display!=='none' && r.width>0 && r.height>0; };
  const label = e => {
    const id=e.id && document.querySelector('label[for="'+CSS.escape(e.id)+'"]');
    return (e.getAttribute('aria-label') || (id && id.innerText) || e.innerText || e.value || e.placeholder || e.name || '').replace(/\s+/g,' ').trim().slice(0,240);
  };
  const sensitive = e => /password|passcode|otp|one-time|cc-|card|cvv|cvc|security.code|secret|token|routing|iban|swift|bank.account|account.number|tax.id|social.security|\bssn\b|\bpin\b/i.test([e.type,e.name,e.id,e.autocomplete,e.getAttribute('aria-label'),e.placeholder].join(' '));
	const consequential = e => /confirm|book|reserve|order|buy|purchase|checkout|submit|pay|request.ride|schedule.ride|cancel|send|finalize|complete|finish|accept|agree|authorize|transfer|delete|remove|subscribe|unsubscribe|enroll|donate|bid|apply|publish|post/i.test(label(e)+' '+(((e.type==='submit'||e.tagName==='BUTTON') && e.closest('form')) ? e.closest('form').innerText.slice(0,1000) : ''));
  const nodes=[...document.querySelectorAll('a,button,input:not([type=hidden]),select,textarea,[role=button],[role=link],[contenteditable=true]')].filter(visible).slice(0,80);
  const elements=nodes.map((e,i) => { const ref='s'+(i+1); e.setAttribute('data-soulacy-ref',ref); return {ref,role:e.getAttribute('role')||e.tagName.toLowerCase(),label:label(e),type:e.type||'',placeholder:e.placeholder||'',sensitive:sensitive(e),consequential:consequential(e)}; });
  const text=(document.body.innerText||'').replace(/\n{3,}/g,'\n\n').trim().slice(0,16000);
  const blocker=/captcha|verify you are human|unusual traffic|access denied|temporarily blocked|automation is not supported/i.test(text) ? 'The provider is requiring a human/security check or blocking automation.' : '';
  return {url:location.href,title:document.title||'',text,elements,blocker};
})()`
