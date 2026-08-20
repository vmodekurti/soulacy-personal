package gateway

import "github.com/soulacy/soulacy/internal/config"

// withCfg publishes a configuration into a Server built by struct literal.
//
// Server.cfg is an atomic pointer (see configsnapshot.go), so it cannot be set
// in a composite literal. This keeps the ~17 literal-built test servers a
// one-line change instead of a two-statement rewrite, and — more usefully —
// means a test that forgets the configuration fails at compile time in the
// same shape it always did rather than silently getting an empty one.
func withCfg(s *Server, c *config.Config) *Server {
	s.setConfig(c)
	return s
}
