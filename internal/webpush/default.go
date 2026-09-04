// default.go — a process-global push service so subsystems that don't own the
// gateway's *Service (the run-failure notifier, the scheduler) can still fan out
// a web push. webpush is a leaf package imported by all of them, so exposing the
// singleton here avoids an import cycle.
//
// The gateway registers the live service via SetDefault once it's constructed;
// callers use NotifyDefault, which is a safe no-op until then.
package webpush

import "sync"

var (
	defaultMu  sync.RWMutex
	defaultSvc *Service
)

// SetDefault registers the process-wide push service.
func SetDefault(s *Service) {
	defaultMu.Lock()
	defaultSvc = s
	defaultMu.Unlock()
}

// NotifyDefault sends a notification to every subscriber of the default service,
// returning the number delivered. It is a no-op (returns 0) when no service has
// been registered or there are no subscribers.
func NotifyDefault(n Notification) int {
	defaultMu.RLock()
	s := defaultSvc
	defaultMu.RUnlock()
	if s == nil || s.Count() == 0 {
		return 0
	}
	return s.Notify(n)
}
