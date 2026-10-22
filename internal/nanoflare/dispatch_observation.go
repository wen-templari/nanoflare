package nanoflare

import "time"

// DispatchObservation measures admitted invocation lifetime using the trusted Go
// clock. It includes loading/I/O/response streaming, and never claims CPU time.
type DispatchObservation struct {
	WorkerID           string
	DeploymentID       string
	RequestChainID     string
	InvocationID       string
	ParentInvocationID string
	At                 time.Time
	ElapsedMS          float64
	Outcome            string
}
type DispatchCounters struct {
	Invocations    uint64  `json:"invocations"`
	Completed      uint64  `json:"completed"`
	Canceled       uint64  `json:"canceled"`
	ElapsedTotalMS float64 `json:"elapsed_total_ms"`
}

func (s *Service) SetDispatchObservationSink(sink func(DispatchObservation)) {
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	s.dispatch.observe = sink
}
func (s *Service) DispatchCounters(worker string) DispatchCounters {
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	return s.dispatch.counts[worker]
}
func (s *Service) admitDispatchObservationLocked(worker string) {
	if s.dispatch.counts == nil {
		s.dispatch.counts = map[string]DispatchCounters{}
	}
	if _, exists := s.dispatch.counts[worker]; !exists && len(s.dispatch.counts) >= 4096 {
		s.telemetry.dropped.Add(1)
		return
	}
	count := s.dispatch.counts[worker]
	count.Invocations++
	s.dispatch.counts[worker] = count
}
func (s *Service) observeDispatchLocked(token string, lease *dispatchLease, outcome string) (DispatchObservation, bool) {
	if lease.Observed || lease.AdmittedAt.IsZero() {
		return DispatchObservation{}, false
	}
	lease.Observed = true
	now := time.Now().UTC()
	chain := lease.Root
	if chain == "" {
		chain = token
	}
	event := DispatchObservation{WorkerID: lease.Target.App.ID, DeploymentID: lease.Target.Deployment.ID, RequestChainID: correlationID(chain), InvocationID: correlationID(token), ParentInvocationID: correlationID(lease.Parent), At: now, ElapsedMS: max(0, float64(now.Sub(lease.AdmittedAt))/float64(time.Millisecond)), Outcome: outcome}
	if count, exists := s.dispatch.counts[event.WorkerID]; exists {
		if outcome == "completed" {
			count.Completed++
		} else {
			count.Canceled++
		}
		count.ElapsedTotalMS += event.ElapsedMS
		s.dispatch.counts[event.WorkerID] = count
	}
	return event, true
}
