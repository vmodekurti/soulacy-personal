package ratelimit

import "sync"

// reload.go — changing the limits without restarting.
//
// The Manager took its Config by value at construction and every check read it
// directly, so `rate_limit.*` had a full editing surface in the config API and
// no effect at all until the process restarted. An operator raising a limit
// because a tenant was being throttled would watch the throttling continue,
// with the settings page telling them it had stopped.
//
// WHAT IS AND IS NOT SWAPPED. Only the limits and the enable flag. The COUNTER
// backend is not: switching memory↔redis live would discard every in-flight
// window, which resets everybody's usage to zero and hands out a free burst at
// exactly the moment somebody is trying to tighten a limit. The backend stays
// a boot-time choice, and the limits — the part anybody actually tunes — do
// not.
//
// THE WINDOWS ARE DEDUCTIBLY UNAFFECTED by a limit change, and that is the
// behaviour to want. Lowering a limit applies to the CURRENT window
// immediately, so a tenant already over the new limit is refused at once rather
// than at the top of the next minute; raising it releases them at once for the
// same reason. Resetting the windows on change would have been simpler and
// would mean a limit change is also an amnesty.

// SetConfig swaps the live limits.
//
// Backend and RedisURL on the incoming config are ignored — see above — so a
// caller passing a whole reloaded config block cannot accidentally rebuild the
// counter underneath the windows it is counting into.
func (m *Manager) SetConfig(cfg Config) {
	if m == nil {
		return
	}
	m.cfgMu.Lock()
	defer m.cfgMu.Unlock()
	cfg.Backend = m.cfg.Backend
	cfg.RedisURL = m.cfg.RedisURL
	m.cfg = cfg
}

// config returns a copy of the live limits.
//
// Every read of m.cfg goes through this. A struct copy under a read lock is
// cheaper than the map lookup already happening on the same path, and it is
// the difference between a config swap being safe and being a data race on
// every request the gateway serves.
func (m *Manager) config() Config {
	if m == nil {
		return Config{}
	}
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.cfg
}

// cfgGuard is embedded in Manager. Declared here rather than beside the struct
// so the reason for its existence sits with the code that needs it.
type cfgGuard struct{ cfgMu sync.RWMutex }
