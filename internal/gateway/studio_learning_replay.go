package gateway

// replayStudioLearning rebuilds any macro/strategy updates that were durably
// appended to the ActionLog but not flushed before a prior process exit.
// Stores deduplicate by run_id, making startup replay idempotent.
func (s *Server) replayStudioLearning() {
	if s == nil || s.actions == nil || s.loader == nil {
		return
	}
	for _, def := range s.loader.All() {
		if def == nil || def.ID == "" {
			continue
		}
		events, err := s.actions.Tail(def.ID, 1000)
		if err != nil {
			continue
		}
		for _, event := range events {
			if s.workflowDistiller != nil {
				s.workflowDistiller.Observe(event)
			}
			if s.strategyCollector != nil {
				s.strategyCollector.Observe(event)
			}
		}
	}
}
