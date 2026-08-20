package gateway

import "github.com/soulacy/soulacy/internal/config"

// configsnapshot.go — one read path for the running configuration.
//
// WHY AN ACCESSOR AND NOT A MUTEX. The gateway's configuration is read on
// essentially every request and rewritten by the file watcher whenever
// config.yaml changes, which — since the config API writes the file — means
// whenever an operator saves anything. Guarding the field with an RWMutex
// would remove the race and introduce a subtler one: a handler that reads the
// config twice in a single request can straddle a reload and act on two
// different configurations. Handing out an immutable snapshot pointer removes
// both. A request sees one configuration from beginning to end.
//
// WHY WRITES CLONE. The snapshot is shared by every in-flight request, so
// writing through it would put the race back exactly where it was. mutateConfig
// copies, mutates the copy, and publishes it; readers already holding the old
// pointer finish their request against the configuration they started with,
// which is the behaviour they would have had anyway had the write landed a
// microsecond later.

// config returns the current configuration snapshot. It never returns nil, so
// handlers can dereference it without a guard — a Server whose config was
// never set is a programming error at construction, and the alternative (nil
// checks at 340 call sites, most of them missing) is how the original code
// panicked in tests instead of failing at wiring time.
func (s *Server) config() *config.Config {
	if s == nil {
		return &config.Config{}
	}
	if c := s.cfg.Load(); c != nil {
		return c
	}
	return &config.Config{}
}

// setConfig publishes a new configuration snapshot wholesale. Used by the
// constructor and by ReloadConfig, which has already built a complete Config
// from disk.
func (s *Server) setConfig(c *config.Config) {
	if c == nil {
		c = &config.Config{}
	}
	s.cfg.Store(c)
}

// mutateConfig applies an in-place-looking edit safely: it shallow-copies the
// current snapshot, hands the copy to fn, and publishes the result.
//
// A SHALLOW copy is deliberate and is sufficient for its callers, which either
// set a scalar field or replace a whole map. It is NOT sufficient for a caller
// that wants to insert into an existing map — that would mutate the map the
// old snapshot still points at. Callers that need to change a map build a new
// one and assign it, and there is a guard test that no handler writes through
// s.config() directly, because that is the mistake this design invites.
func (s *Server) mutateConfig(fn func(*config.Config)) {
	if s == nil || fn == nil {
		return
	}
	next := *s.config()
	fn(&next)
	s.cfg.Store(&next)
}

// setProviderConfig publishes one provider's settings into the live snapshot.
//
// It rebuilds the provider map rather than assigning into the existing one.
// The existing map is shared with every snapshot ever handed out, so writing
// into it would be visible to requests that are mid-flight against the OLD
// configuration — the precise race the snapshot exists to remove, reintroduced
// one map entry at a time.
func (s *Server) setProviderConfig(id string, pc config.ProviderConfig) {
	s.mutateConfig(func(c *config.Config) {
		next := make(map[string]config.ProviderConfig, len(c.LLM.Providers)+1)
		for k, v := range c.LLM.Providers {
			next[k] = v
		}
		next[id] = pc
		c.LLM.Providers = next
	})
}

// providerConfig reads one provider's settings from the current snapshot. The
// zero value for an unconfigured provider is the same answer the old map index
// gave.
func (s *Server) providerConfig(id string) config.ProviderConfig {
	return s.config().LLM.Providers[id]
}

// setChannelConfig publishes one channel's settings into the live snapshot,
// rebuilding the outer map for the same reason setProviderConfig does.
func (s *Server) setChannelConfig(id string, block map[string]any) {
	s.mutateConfig(func(c *config.Config) {
		next := make(map[string]map[string]any, len(c.Channels)+1)
		for k, v := range c.Channels {
			next[k] = v
		}
		next[id] = block
		c.Channels = next
	})
}
