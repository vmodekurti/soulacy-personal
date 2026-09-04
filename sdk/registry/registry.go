// Package registry provides database/sql-style named factory registries
// (Story E10). Drivers self-register from init():
//
//	import _ "example.com/soulacy-matrix" // registers channel "matrix"
//
//	func init() {
//	    registry.RegisterChannel("matrix", func(cfg map[string]any) (channel.Adapter, error) {
//	        return newMatrixAdapter(cfg)
//	    })
//	}
//
// The host resolves config entries against these registries with fallback
// to its built-in wiring (strangler pattern) until every built-in is
// registry-routed.
//
// Compatibility: function signatures are frozen per SDK major version;
// factory config maps are schemaless by design (validated by the factory).
package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/soulacy/soulacy/sdk/channel"
	"github.com/soulacy/soulacy/sdk/llm"
	"github.com/soulacy/soulacy/sdk/queue"
	"github.com/soulacy/soulacy/sdk/vector"
)

// Factory signatures. cfg carries the entry's configuration map verbatim;
// factories must validate it and fail with a descriptive error.
type (
	ChannelFactory  func(cfg map[string]any) (channel.Adapter, error)
	ProviderFactory func(cfg map[string]any) (llm.Provider, error)
	QueueFactory    func(cfg map[string]any) (queue.Backend, error)
	VectorFactory   func(cfg map[string]any) (vector.Backend, error)
)

// registry is a concurrency-safe name → factory map.
type registry[F any] struct {
	mu sync.RWMutex
	m  map[string]F
}

func (r *registry[F]) register(kind, name string, f F, isNil bool) error {
	if name == "" {
		return fmt.Errorf("registry: %s factory name must not be empty", kind)
	}
	if isNil {
		return fmt.Errorf("registry: %s factory %q is nil", kind, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]F{}
	}
	if _, dup := r.m[name]; dup {
		return fmt.Errorf("registry: %s factory %q already registered", kind, name)
	}
	r.m[name] = f
	return nil
}

func (r *registry[F]) lookup(name string) (F, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.m[name]
	return f, ok
}

func (r *registry[F]) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

var (
	channels  registry[ChannelFactory]
	providers registry[ProviderFactory]
	queues    registry[QueueFactory]
	vectors   registry[VectorFactory]
)

// RegisterChannel registers a channel adapter factory under name.
// Duplicate names and nil factories error (call from init(); treat a
// non-nil error as a programmer mistake).
func RegisterChannel(name string, f ChannelFactory) error {
	return channels.register("channel", name, f, f == nil)
}

// RegisterProvider registers an LLM provider factory under name.
func RegisterProvider(name string, f ProviderFactory) error {
	return providers.register("provider", name, f, f == nil)
}

// RegisterQueue registers a queue backend factory under name.
func RegisterQueue(name string, f QueueFactory) error {
	return queues.register("queue", name, f, f == nil)
}

// RegisterVector registers a vector backend factory under name.
func RegisterVector(name string, f VectorFactory) error {
	return vectors.register("vector", name, f, f == nil)
}

// MustRegisterChannel is RegisterChannel that panics on error — the
// idiomatic form inside driver init() functions.
func MustRegisterChannel(name string, f ChannelFactory) {
	if err := RegisterChannel(name, f); err != nil {
		panic(err)
	}
}

// MustRegisterProvider is RegisterProvider that panics on error.
func MustRegisterProvider(name string, f ProviderFactory) {
	if err := RegisterProvider(name, f); err != nil {
		panic(err)
	}
}

// MustRegisterQueue is RegisterQueue that panics on error.
func MustRegisterQueue(name string, f QueueFactory) {
	if err := RegisterQueue(name, f); err != nil {
		panic(err)
	}
}

// MustRegisterVector is RegisterVector that panics on error.
func MustRegisterVector(name string, f VectorFactory) {
	if err := RegisterVector(name, f); err != nil {
		panic(err)
	}
}

// NewChannel instantiates the named channel adapter ("" or unknown names
// report ok=false so hosts can fall back to built-in wiring).
func NewChannel(name string, cfg map[string]any) (channel.Adapter, bool, error) {
	f, ok := channels.lookup(name)
	if !ok {
		return nil, false, nil
	}
	a, err := f(cfg)
	return a, true, err
}

// NewProvider instantiates the named LLM provider.
func NewProvider(name string, cfg map[string]any) (llm.Provider, bool, error) {
	f, ok := providers.lookup(name)
	if !ok {
		return nil, false, nil
	}
	p, err := f(cfg)
	return p, true, err
}

// NewQueue instantiates the named queue backend.
func NewQueue(name string, cfg map[string]any) (queue.Backend, bool, error) {
	f, ok := queues.lookup(name)
	if !ok {
		return nil, false, nil
	}
	q, err := f(cfg)
	return q, true, err
}

// NewVector instantiates the named vector backend.
func NewVector(name string, cfg map[string]any) (vector.Backend, bool, error) {
	f, ok := vectors.lookup(name)
	if !ok {
		return nil, false, nil
	}
	v, err := f(cfg)
	return v, true, err
}

// Channels lists registered channel factory names (sorted).
func Channels() []string { return channels.names() }

// Providers lists registered provider factory names (sorted).
func Providers() []string { return providers.names() }

// Queues lists registered queue factory names (sorted).
func Queues() []string { return queues.names() }

// Vectors lists registered vector factory names (sorted).
func Vectors() []string { return vectors.names() }
