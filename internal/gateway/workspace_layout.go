package gateway

import "github.com/soulacy/soulacy/internal/wsroot"

// SetWorkspaceLayoutRoot enables the canonical Team/Scale filesystem layout.
// It must be called during startup, before handlers or background Studio work
// begin. Personal mode intentionally never calls it, so all existing Personal
// paths and capabilities remain unchanged.
func (s *Server) SetWorkspaceLayoutRoot(root string) {
	if s == nil {
		return
	}
	s.workspaceLayout = wsroot.NewLayout(root)
}
