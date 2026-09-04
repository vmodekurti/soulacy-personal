package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/message"
)

// fakeAdapter is a minimal channel.Adapter for factory tests.
type fakeAdapter struct{ id string }

func (f *fakeAdapter) ID() string                                          { return f.id }
func (f *fakeAdapter) Name() string                                        { return f.id }
func (f *fakeAdapter) Start(context.Context, chan<- message.Message) error { return nil }
func (f *fakeAdapter) Send(context.Context, message.Message) error         { return nil }
func (f *fakeAdapter) Stop() error                                         { return nil }
func (f *fakeAdapter) Status() channel.AdapterStatus                       { return channel.AdapterStatus{} }

func TestRegisterChannel_AndNew(t *testing.T) {
	err := RegisterChannel("test-reg-chan", func(cfg map[string]any) (channel.Adapter, error) {
		id, _ := cfg["id"].(string)
		if id == "" {
			return nil, errors.New("id required")
		}
		return &fakeAdapter{id: id}, nil
	})
	if err != nil {
		t.Fatalf("RegisterChannel: %v", err)
	}

	a, ok, err := NewChannel("test-reg-chan", map[string]any{"id": "matrix"})
	if !ok || err != nil {
		t.Fatalf("NewChannel ok=%v err=%v", ok, err)
	}
	if a.ID() != "matrix" {
		t.Fatalf("adapter id = %q", a.ID())
	}

	// Factory errors surface with ok=true (the name WAS registered).
	_, ok, err = NewChannel("test-reg-chan", nil)
	if !ok || err == nil {
		t.Fatalf("want ok=true with factory error, got ok=%v err=%v", ok, err)
	}
}

func TestNewChannel_UnknownName_FallsThrough(t *testing.T) {
	a, ok, err := NewChannel("never-registered", nil)
	if ok || err != nil || a != nil {
		t.Fatalf("unknown name must report ok=false (strangler fallback), got %v %v %v", a, ok, err)
	}
}

func TestRegister_DuplicateAndInvalid(t *testing.T) {
	f := func(map[string]any) (channel.Adapter, error) { return &fakeAdapter{id: "x"}, nil }
	if err := RegisterChannel("dup-chan", f); err != nil {
		t.Fatal(err)
	}
	if err := RegisterChannel("dup-chan", f); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if err := RegisterChannel("", f); err == nil {
		t.Fatal("empty name accepted")
	}
	if err := RegisterChannel("nil-factory", nil); err == nil {
		t.Fatal("nil factory accepted")
	}
}

func TestMustRegister_PanicsOnError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegisterChannel did not panic on duplicate")
		}
	}()
	f := func(map[string]any) (channel.Adapter, error) { return &fakeAdapter{}, nil }
	MustRegisterChannel("must-chan", f)
	MustRegisterChannel("must-chan", f) // panics
}

func TestNames_SortedAndScopedPerKind(t *testing.T) {
	_ = RegisterChannel("z-chan", func(map[string]any) (channel.Adapter, error) { return &fakeAdapter{}, nil })
	_ = RegisterChannel("a-chan", func(map[string]any) (channel.Adapter, error) { return &fakeAdapter{}, nil })
	names := Channels()
	ai, zi := -1, -1
	for i, n := range names {
		if n == "a-chan" {
			ai = i
		}
		if n == "z-chan" {
			zi = i
		}
	}
	if ai == -1 || zi == -1 || ai > zi {
		t.Fatalf("Channels() not sorted or missing entries: %v", names)
	}
	// Channel names never leak into other kinds.
	for _, n := range Providers() {
		if strings.HasSuffix(n, "-chan") {
			t.Fatalf("channel name leaked into providers: %v", Providers())
		}
	}
}
