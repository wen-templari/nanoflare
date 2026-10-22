package api

import (
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/prometheus/client_golang/prometheus"
)

func (s *Server) SetRunnerResourceReader(reader func() (*nanoflare.RunnerResources, time.Time)) {
	s.runnerResourceReader = reader
}

func collectRunnerResources(ch chan<- prometheus.Metric, s *Server) {
	if s.runnerResourceReader == nil {
		return
	}
	snapshot, at := s.runnerResourceReader()
	gauge := func(name, help string, value float64) {
		ch <- prometheus.MustNewConstMetric(metricDesc(name, help, nil), prometheus.GaugeValue, value)
	}
	age := time.Since(at).Seconds()
	available := snapshot != nil && !at.IsZero() && age >= 0 && age <= 20
	flag := 0.0
	if available {
		flag = 1
	}
	gauge("nanoflare_runner_resources_available", "Whether a supported authenticated runner resource snapshot is no more than 20 seconds old.", flag)
	if !at.IsZero() {
		gauge("nanoflare_runner_resource_sample_age_seconds", "Age of the last authenticated runner resource status response.", max(age, 0))
	}
	if !available {
		return
	}
	gauge("nanoflare_runner_memory_current_bytes", "Last observed physical runner cgroup memory, including agent, runtime descendants and charged cache.", float64(snapshot.MemoryBytes))
	if snapshot.MemoryPeakAvailable {
		gauge("nanoflare_runner_memory_peak_bytes", "Physical runner cgroup memory high-water mark over its current cgroup lifetime.", float64(snapshot.MemoryPeakBytes))
	}
	if snapshot.MemoryLimited {
		gauge("nanoflare_runner_memory_limit_bytes", "Configured finite memory limit of the visible runner cgroup.", float64(snapshot.MemoryLimitBytes))
	}
	gauge("nanoflare_runner_pids_current", "Last observed physical runner cgroup task count, including threads.", float64(snapshot.PIDs))
	if snapshot.PIDLimited {
		gauge("nanoflare_runner_pids_limit", "Configured finite task limit of the visible runner cgroup.", float64(snapshot.PIDLimit))
	}
	ch <- prometheus.MustNewConstMetric(metricDesc("nanoflare_runner_oom_kills_total", "OOM kills reported by the physical runner cgroup; resets when the cgroup is recreated.", nil), prometheus.CounterValue, float64(snapshot.OOMKills))
}
