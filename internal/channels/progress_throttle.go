package channels

import (
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// ProgressThrottle coalesces rapid progress updates and emits at most one
// per Interval (default 1500ms). The last-received event within each window
// is forwarded; earlier ones are dropped. One throttle per active run.
type ProgressThrottle struct {
	Interval time.Duration // default 1500ms
	In       chan message.ProgressEvent
	Out      chan message.ProgressEvent
}

// NewProgressThrottle creates a ProgressThrottle with the given interval.
// If interval is zero, it defaults to 1500ms.
func NewProgressThrottle(interval time.Duration) *ProgressThrottle {
	if interval <= 0 {
		interval = 1500 * time.Millisecond
	}
	return &ProgressThrottle{
		Interval: interval,
		In:       make(chan message.ProgressEvent, 64),
		Out:      make(chan message.ProgressEvent, 64),
	}
}

// Start begins the debouncing loop. Returns a stop func. Non-blocking.
// On each tick, if a new event arrived since the last tick, the latest one
// is forwarded to Out; earlier events in the same window are dropped.
func (t *ProgressThrottle) Start() (stop func()) {
	quit := make(chan struct{})
	go func() {
		ticker := time.NewTicker(t.Interval)
		defer ticker.Stop()
		var pending *message.ProgressEvent
		for {
			select {
			case ev := <-t.In:
				copy := ev
				pending = &copy
			case <-ticker.C:
				if pending != nil {
					select {
					case t.Out <- *pending:
					default:
						// Out is full; drop rather than block.
					}
					pending = nil
				}
			case <-quit:
				return
			}
		}
	}()
	return func() { close(quit) }
}
