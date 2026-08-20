// stopadapter_test.go — disabling a channel has to take effect immediately.
//
// StartAdapter existed and was used live by the WhatsApp pairing flow, so
// CONNECTING a channel without a restart was already possible. Disconnecting
// one was not, and a save path can only be hot if both directions are: an
// operator who can enable live but must restart to disable has a restart in
// their workflow either way, which is why the handlers went on telling them to
// restart for both.
package channels

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
)

// stoppableAdapter records whether it was told to stop. Distinct from the
// package's existing fakeAdapter, whose Stop is a no-op returning nil — the
// point here is observing that Stop was CALLED and what it returned.
type stoppableAdapter struct {
	id      string
	mu      sync.Mutex
	stopped bool
	stopErr error
}

func (f *stoppableAdapter) ID() string   { return f.id }
func (f *stoppableAdapter) Name() string { return f.id }

func (f *stoppableAdapter) Start(_ context.Context, _ chan<- message.Message) error { return nil }

func (f *stoppableAdapter) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return f.stopErr
}

func (f *stoppableAdapter) Send(_ context.Context, _ message.Message) error { return nil }

func (f *stoppableAdapter) Status() AdapterStatus { return AdapterStatus{Connected: true} }

func (f *stoppableAdapter) wasStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func TestStoppingAnAdapterRemovesItAndStopsIt(t *testing.T) {
	reg := NewRegistry(1)
	adapter := &stoppableAdapter{id: "slack"}
	if err := reg.StartAdapter(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}

	stopped, err := reg.StopAdapter("slack")
	if err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("StopAdapter reported nothing to stop")
	}
	if !adapter.wasStopped() {
		t.Error("the adapter was removed from the registry but never told to stop, so the connection " +
			"to the provider is still open")
	}
	// The half that makes the disable real: no send may resolve it any more.
	if err := reg.Send(context.Background(), message.Message{Channel: "slack"}); err == nil {
		t.Error("a disabled channel still accepted an outbound send")
	}
}

func TestStoppingAnAdapterThatIsNotThereIsNotAnError(t *testing.T) {
	// Disabling a channel that never connected is the ordinary case for a
	// config edit made before the credentials were valid. Reporting it as a
	// failure would make the GUI show a red state for a successful change.
	reg := NewRegistry(1)
	stopped, err := reg.StopAdapter("never-started")
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if stopped {
		t.Error("StopAdapter claimed to have stopped an adapter that was never registered")
	}
}

func TestAnAdapterThatFailsToStopIsStillDeregistered(t *testing.T) {
	// The disable has taken effect regardless — it is out of the registry, so
	// nothing can route to it. The error is reported so an operator who sees a
	// lingering session in the provider's dashboard knows why, rather than
	// concluding the disable did not work and trying again.
	reg := NewRegistry(1)
	adapter := &stoppableAdapter{id: "telegram", stopErr: errors.New("websocket close timed out")}
	if err := reg.StartAdapter(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}

	stopped, err := reg.StopAdapter("telegram")
	if !stopped {
		t.Error("stopped = false, want true — it was registered")
	}
	if err == nil {
		t.Error("the adapter's failure to close was swallowed")
	}
	if sendErr := reg.Send(context.Background(), message.Message{Channel: "telegram"}); sendErr == nil {
		t.Error("a channel whose adapter failed to close is still routable, so the disable did not " +
			"take effect")
	}
}

func TestAdaptersReturnsWhatWasRegistered(t *testing.T) {
	// Adapters() is how a hot reload learns which adapter IDs a config
	// produces — by building them and asking, rather than by inferring from a
	// naming convention that every future channel type would have to obey.
	reg := NewRegistry(1)
	reg.Register(&stoppableAdapter{id: "slack"})
	reg.Register(&stoppableAdapter{id: "slack-support"})

	ids := map[string]bool{}
	for _, adapter := range reg.Adapters() {
		ids[adapter.ID()] = true
	}
	if len(ids) != 2 || !ids["slack"] || !ids["slack-support"] {
		t.Errorf("Adapters() = %v, want both the default and the suffixed adapter", ids)
	}
}
