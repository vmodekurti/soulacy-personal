package gateway

// Close drains bounded Studio learning queues and closes semantic storage.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeAutopilot()
	s.learningReplayWG.Wait()
	if s.workflowDistiller != nil {
		s.workflowDistiller.Close()
	}
	if s.strategyCollector != nil {
		s.strategyCollector.Close()
	}
	s.preferenceJobsWG.Wait()
	close(s.preferenceJobs)
	if s.lessonStoreCached != nil {
		return s.lessonStoreCached.Close()
	}
	return nil
}
