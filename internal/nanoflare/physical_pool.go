package nanoflare

// RunnerResources measures the visible physical runner cgroup, including the
// agent and all runtime descendants. It is not per-Worker usage or CPU time.
// A missing snapshot means the platform/cgroup could not be measured.
type RunnerResources struct {
	MemoryBytes         uint64 `json:"memory_bytes"`
	MemoryLimitBytes    uint64 `json:"memory_limit_bytes"`
	MemoryLimited       bool   `json:"memory_limited"`
	MemoryPeakBytes     uint64 `json:"memory_peak_bytes"`
	MemoryPeakAvailable bool   `json:"memory_peak_available"`
	PIDs                uint64 `json:"pids"`
	PIDLimit            uint64 `json:"pid_limit"`
	PIDLimited          bool   `json:"pid_limited"`
	OOMKills            uint64 `json:"oom_kills"`
}
