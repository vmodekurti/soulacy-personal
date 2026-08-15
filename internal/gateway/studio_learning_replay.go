package gateway

import "github.com/soulacy/soulacy/internal/wsroot"

// replayStudioLearning rebuilds any macro/strategy updates that were durably
// appended to the ActionLog but not flushed before a prior process exit.
// Stores deduplicate by run_id, making startup replay idempotent.
func (s *Server) replayStudioLearning() {
	if s == nil || s.actions == nil || s.loader == nil {
		return
	}
	// Replay covers every tenant. The action log is keyed by agent ID, so this
	// sweeps workspace by workspace rather than reading a flattened registry.
	s.eachWorkspace(func(scope agentScope) {
		for _, def := range scope.All() {
			if def == nil || def.ID == "" {
				continue
			}
			events, err := s.actionLogForWorkspace(scope.workspaceID).Tail(def.ID, 1000)
			if err != nil {
				continue
			}
			for _, event := range events {
				// Two workspaces may run an agent with the same ID, and the
				// action log's per-agent file holds both. Replaying an event
				// under the workspace currently being swept — rather than the
				// one the event records — would teach a tenant from another
				// tenant's runs, so events that do not belong here are skipped
				// rather than re-attributed.
				if wsroot.Normalize(event.WorkspaceID) != scope.workspaceID {
					continue
				}
				if s.workflowDistiller != nil {
					s.workflowDistiller.Observe(event)
				}
				if s.strategyCollector != nil {
					s.strategyCollector.Observe(event)
				}
			}
		}
	})
}
